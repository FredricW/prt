package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/pipeline/pipelinetest"
	"github.com/fredricw/prt/internal/review"
)

// Recording kinds, as the directories under testdata/recordings name them.
const (
	kindJev    = "jev"
	kindEmbed  = "embed"
	kindReview = "review"
)

// unusedRun runs the fixture PRs and then the fixture branches the way the
// replay tests do — Jev answers, embeddings and reviews all replayed
// strictly from --out, --embed-out and --review-out, the fixture repo's
// models, the default routing — and lists the recording files no request
// of that run reads. Every replay test, `make snapshot` and `make demo`
// send exactly the requests of this run (the pipeline is deterministic
// over the fixture), so these files are used by nothing. With prune they
// are deleted. A request without a recording fails the run: record first.
// No network, no spend.
func unusedRun(ctx context.Context, cfg config, repo, prs string, stdout io.Writer) error {
	fast, deep, err := fixtureModels(ctx, repo)
	if err != nil {
		return err
	}
	log := &keyLog{}
	jc := &jevKeys{next: &jev.Replay{Dir: cfg.out}, log: log}
	emb := &embedKeys{next: &embed.Replay{Dir: cfg.embedOut}, log: log}
	tiers := review.Tiers{
		Fast: &reviewKeys{next: &review.Replay{Name: fast, Dir: cfg.reviewOut}, log: log},
		Deep: &reviewKeys{next: &review.Replay{Name: deep, Dir: cfg.reviewOut}, log: log},
	}
	outs, branches, err := pipelinetest.FixtureRunWithBranches(ctx, repo, prs, jc,
		pipelinetest.WithEmbedder(emb), pipelinetest.WithReviewers(tiers))
	if err != nil {
		return err
	}
	if err := checkRun(outs, branches); err != nil {
		return err
	}
	if m := log.missList(); len(m) > 0 {
		return fmt.Errorf("recordjev: %d requests have no recording; record them before listing unused ones: %s", len(m), strings.Join(m[:min(3, len(m))], "; "))
	}
	var unused []string
	for _, d := range []struct{ kind, dir string }{{kindJev, cfg.out}, {kindEmbed, cfg.embedOut}, {kindReview, cfg.reviewOut}} {
		files, err := unusedFiles(d.dir, log.used(d.kind))
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s: %d recordings used, %d unused in %s\n", d.kind, len(log.used(d.kind)), len(files), d.dir)
		unused = append(unused, files...)
	}
	if !cfg.prune {
		for _, f := range unused {
			fmt.Fprintln(stdout, "unused:", f)
		}
		if len(unused) > 0 {
			fmt.Fprintln(stdout, "delete them with: go run ./tools/recordjev --unused --prune")
		}
		return nil
	}
	for _, f := range unused {
		if err := os.Remove(f); err != nil {
			return fmt.Errorf("recordjev: prune %s: %w", f, err)
		}
	}
	fmt.Fprintf(stdout, "pruned %d unused recordings\n", len(unused))
	return nil
}

// unusedFiles returns the *.json files of dir whose key (the file name
// without .json) is not in used, sorted.
func unusedFiles(dir string, used map[string]bool) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("recordjev: list %s: %w", dir, err)
	}
	var out []string
	for _, f := range files {
		if !used[strings.TrimSuffix(filepath.Base(f), ".json")] {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out, nil
}

// keyLog collects the recording keys a replay run reads, by kind, and the
// requests that missed.
type keyLog struct {
	mu     sync.Mutex
	keys   map[string]map[string]bool
	misses []string
}

// add records one key read (and the error when it missed).
func (l *keyLog) add(kind, key string, miss error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if miss != nil {
		l.misses = append(l.misses, miss.Error())
		return
	}
	if l.keys == nil {
		l.keys = map[string]map[string]bool{}
	}
	if l.keys[kind] == nil {
		l.keys[kind] = map[string]bool{}
	}
	l.keys[kind][key] = true
}

// used returns a copy of the keys read for kind.
func (l *keyLog) used(kind string) map[string]bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]bool, len(l.keys[kind]))
	for k := range l.keys[kind] {
		out[k] = true
	}
	return out
}

// missList returns the misses.
func (l *keyLog) missList() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.misses...)
}

// jevKeys logs the key of every Jev request it passes to next.
type jevKeys struct {
	next jev.Client
	log  *keyLog
}

func (j *jevKeys) Evaluate(ctx context.Context, req jev.Request) (jev.Response, error) {
	key, err := jev.RequestKey(req)
	if err != nil {
		return jev.Response{}, fmt.Errorf("recordjev: request key: %w", err)
	}
	resp, err := j.next.Evaluate(ctx, req)
	j.log.add(kindJev, key, missOnly(err, jev.ErrNoRecording))
	return resp, err
}

// embedKeys logs the key of every text it passes to next.
type embedKeys struct {
	next embed.Embedder
	log  *keyLog
}

func (e *embedKeys) Model() string { return e.next.Model() }

func (e *embedKeys) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	v, err := e.next.Embed(ctx, texts)
	miss := missOnly(err, embed.ErrNoRecording)
	for _, t := range texts {
		e.log.add(kindEmbed, embed.TextKey(e.next.Model(), t), miss)
	}
	return v, err
}

// reviewKeys logs the key of every review request it passes to next.
type reviewKeys struct {
	next review.Reviewer
	log  *keyLog
}

func (r *reviewKeys) Model() string { return r.next.Model() }

func (r *reviewKeys) Review(ctx context.Context, req review.Request) (review.Result, error) {
	key, err := review.RequestKey(r.next.Model(), req)
	if err != nil {
		return review.Result{}, fmt.Errorf("recordjev: review key: %w", err)
	}
	res, err := r.next.Review(ctx, req)
	r.log.add(kindReview, key, missOnly(err, review.ErrNoRecording))
	return res, err
}

// missOnly returns err when it wraps the kind's ErrNoRecording, else nil
// (a served recording, also one that answers with an error such as a
// recorded truncation, counts as used).
func missOnly(err, noRecording error) error {
	if errors.Is(err, noRecording) {
		return err
	}
	return nil
}
