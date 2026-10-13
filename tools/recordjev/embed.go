package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/pipeline/pipelinetest"
)

// Embedding modes (--embed).
const (
	embedNone   = "none"
	embedReplay = "replay"
	embedLive   = "live"
)

// resolveEmbedMode returns mode, or for "" the default: replay when dir
// holds recordings, else none.
func resolveEmbedMode(mode, dir string) (string, error) {
	switch mode {
	case embedNone, embedReplay, embedLive:
		return mode, nil
	case "":
		if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) > 0 {
			return embedReplay, nil
		}
		return embedNone, nil
	}
	return "", fmt.Errorf("--embed %q: want none, replay or live", mode)
}

// embedRun is the embedder of one recordjev run with its bookkeeping:
// texts asked, texts already recorded, live calls and the first error.
type embedRun struct {
	mode string
	dir  string
	emb  embed.Embedder // nil for mode none
	live *countingEmbedder

	prior                 float64 // ollama ledger spend before this run
	ledgerPath, cmd, blkd string

	mu              sync.Mutex
	texts, recorded int
	firstErr        error
}

// newEmbedRun builds the embedder for cfg. Live mode wraps Ollama
// (OLLAMA_HOST, default http://127.0.0.1:11434; model embeddinggemma-2) in
// embed.Recorder and first checks that Ollama answers, so no Jev request
// is made with embeddings silently missing.
func newEmbedRun(ctx context.Context, cfg config, getenv env) (*embedRun, error) {
	r := &embedRun{mode: cfg.embed, dir: cfg.embedOut, ledgerPath: cfg.spend, cmd: cfg.command(), blkd: cfg.blocked}
	switch cfg.embed {
	case embedReplay:
		r.emb = &embed.Replay{Dir: cfg.embedOut}
	case embedLive:
		ollama := embed.NewOllama(getenv("OLLAMA_HOST"), embed.DefaultOllamaModel)
		if _, err := ollama.Embed(ctx, []string{"prt recordjev preflight"}); err != nil {
			return nil, r.block(err)
		}
		prior, err := ledgerSpent(cfg.spend, "ollama")
		if err != nil {
			return nil, err
		}
		r.prior = prior
		r.live = &countingEmbedder{next: ollama}
		r.emb = &embed.Recorder{Live: r.live, Dir: cfg.embedOut}
	}
	return r, nil
}

// options returns the FixtureRun options for this embedder.
func (r *embedRun) options() []pipelinetest.Option {
	if r.emb == nil {
		return nil
	}
	return []pipelinetest.Option{pipelinetest.WithEmbedder(r)}
}

// Model implements embed.Embedder.
func (r *embedRun) Model() string { return r.emb.Model() }

// Embed implements embed.Embedder: it counts texts and how many were
// already recorded, then delegates; the first error is kept (the pipeline
// treats embed.ErrUnavailable as "skip stage 3", so it would otherwise be
// invisible here).
func (r *embedRun) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	m := r.emb.Model()
	recorded := 0
	for _, t := range texts {
		if _, err := os.Stat(filepath.Join(r.dir, embed.TextKey(m, t)+".json")); err == nil {
			recorded++
		}
	}
	vecs, err := r.emb.Embed(ctx, texts)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts += len(texts)
	r.recorded += recorded
	if err != nil && r.firstErr == nil {
		r.firstErr = err
	}
	return vecs, err
}

// finish reports the embedding counts, appends the ollama ledger row in
// live mode, and in live mode turns an embedding error into a BLOCKED.md
// entry and a non-zero exit.
func (r *embedRun) finish(stdout io.Writer) error {
	if r.emb == nil {
		fmt.Fprintln(stdout, "embeddings: none (stage 3 skipped; pair candidates come from mechanical shapes only)")
		return nil
	}
	r.mu.Lock()
	texts, recorded, firstErr := r.texts, r.recorded, r.firstErr
	r.mu.Unlock()
	fmt.Fprintf(stdout, "embeddings (%s, %s): %d texts, %d already recorded in %s, %d to record\n",
		r.mode, r.emb.Model(), texts, recorded, r.dir, texts-recorded)
	if r.mode != embedLive {
		return nil
	}
	calls, written := r.live.totals()
	row := ledgerRow{At: time.Now().UTC(), Service: "ollama", Calls: calls, Cost: 0, Cumulative: r.prior, Written: written}
	if err := appendLedger(r.ledgerPath, row); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "ollama: %d calls, %d embedding recordings written to %s ($0)\n", calls, written, r.dir)
	if firstErr != nil {
		return r.block(firstErr)
	}
	return nil
}

// block records an Ollama failure in BLOCKED.md (exact error, date and
// command) and returns an error for a non-zero exit.
func (r *embedRun) block(cause error) error {
	entry := fmt.Sprintf("\n## %s — Ollama embeddings unreachable\n\n- **Command:** `%s`\n- **Error:** `%s`\n- **Effect:** no embedding recordings were written; tests fall back to `embed.Fake` (or skip stage 3).\n",
		time.Now().UTC().Format(time.RFC3339), r.cmd, cause)
	if err := appendBlocked(r.blkd, entry); err != nil {
		return errors.Join(cause, err)
	}
	return fmt.Errorf("embed live: %w (recorded in %s)", cause, r.blkd)
}

// appendBlocked appends entry to the blocked-work file, creating it with a
// header when missing.
func appendBlocked(path, entry string) error {
	_, err := os.Stat(path)
	header := errors.Is(err, fs.ErrNotExist)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("blocked file: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open blocked file: %w", err)
	}
	if header {
		entry = "# Blocked\n\nWork that could not be done unattended, with the exact error.\n" + entry
	}
	if _, err := f.WriteString(entry); err != nil {
		_ = f.Close()
		return fmt.Errorf("write blocked file: %w", err)
	}
	return f.Close()
}

// countingEmbedder counts calls and texts sent to the live embedder.
type countingEmbedder struct {
	next embed.Embedder

	mu           sync.Mutex
	calls, texts int
}

// Model implements embed.Embedder.
func (c *countingEmbedder) Model() string { return c.next.Model() }

// Embed implements embed.Embedder.
func (c *countingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	vecs, err := c.next.Embed(ctx, texts)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if err == nil {
		c.texts += len(texts)
	}
	return vecs, err
}

func (c *countingEmbedder) totals() (calls, texts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.texts
}

// command renders the invocation for BLOCKED.md.
func (c config) command() string {
	return strings.TrimSpace("go run ./tools/recordjev " + strings.Join(c.args, " "))
}
