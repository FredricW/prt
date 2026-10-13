// Command recordjev records real Jev responses for the fixture PRs #101–#104
// by running the real pipeline over a freshly generated fixture repo (see
// internal/pipeline/pipelinetest and DECISIONS "M1 Jev recordings flow").
//
// Modes:
//
//	recordjev              dry run: counts the requests a recording run
//	                       would send (with a fake client) and how many are
//	                       already recorded; no network, no spend
//	recordjev --live       records with the real endpoint (JEV_API_KEY,
//	                       JEV_BASE_URL); requests already recorded are not
//	                       sent; stops before a call that could push the
//	                       ledger's cumulative Jev spend over --cap; appends
//	                       the run's spend to --spend
//	recordjev --synthetic  writes heuristic recordings marked
//	                       "synthetic": true (only if live recording is
//	                       blocked); no network
//
// Recording files are written only by jev.Recorder, which never sees the
// API key. The key is read from the environment and never printed.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fredricw/prt/internal/fixturegen"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/pipeline/pipelinetest"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// config is the parsed command line.
type config struct {
	live, synthetic bool
	out, spend, src string
	capUSD          float64
}

// env looks up an environment variable ("" when unset); injected for tests.
type env func(string) string

// run is main without process globals; it returns the exit code.
func run(ctx context.Context, args []string, getenv env, stdout, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		return 2
	}
	if cfg.live && getenv("JEV_API_KEY") == "" {
		fmt.Fprintln(stderr, "recordjev: --live needs JEV_API_KEY (an AI Gateway API key) in the environment; nothing was sent")
		return 2
	}
	if err := execute(ctx, cfg, getenv, stdout); err != nil {
		fmt.Fprintln(stderr, "recordjev:", err)
		return 1
	}
	return 0
}

// parseFlags parses args; defaults are relative to the module root.
func parseFlags(args []string, stderr io.Writer) (config, error) {
	root := moduleRoot()
	fs := flag.NewFlagSet("recordjev", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c config
	fs.BoolVar(&c.live, "live", false, "call the real Jev endpoint (needs JEV_API_KEY; spends money)")
	fs.BoolVar(&c.synthetic, "synthetic", false, "write heuristic recordings marked synthetic (no network)")
	fs.StringVar(&c.out, "out", filepath.Join(root, "testdata", "recordings", "jev"), "recordings directory")
	fs.StringVar(&c.spend, "spend", filepath.Join(root, "testdata", "recordings", "SPEND.md"), "spend ledger")
	fs.Float64Var(&c.capUSD, "cap", 1.00, "cap on cumulative Jev spend in USD (ledger + this run)")
	fs.StringVar(&c.src, "src", filepath.Join(root, "testdata", "fixtures"), "fixture sources (base/, prs/, prs.json, prt.toml)")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.live && c.synthetic {
		fmt.Fprintln(stderr, "recordjev: --live and --synthetic are exclusive")
		return c, errors.New("exclusive flags")
	}
	return c, nil
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod ("." if none).
func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// execute generates the fixture repo into a temp dir and runs the chosen mode.
func execute(ctx context.Context, cfg config, getenv env, stdout io.Writer) error {
	tmp, err := os.MkdirTemp("", "prt-recordjev-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	// fixturegen only writes under a "testdata" directory.
	repo := filepath.Join(tmp, "testdata", "fixture-repo")
	if err := fixturegen.Generate(ctx, cfg.src, repo); err != nil {
		return fmt.Errorf("generate fixture repo: %w", err)
	}
	prs := filepath.Join(cfg.src, "prs.json")
	switch {
	case cfg.live:
		return recordLive(ctx, cfg, repo, prs, getenv, stdout)
	case cfg.synthetic:
		return recordSynthetic(ctx, cfg, repo, prs, stdout)
	}
	return dryRun(ctx, cfg, repo, prs, stdout)
}

// dryRun counts the requests a recording run would send.
func dryRun(ctx context.Context, cfg config, repo, prs string, stdout io.Writer) error {
	c := &counter{next: &jev.Fake{}, dir: cfg.out}
	outs, err := pipelinetest.FixtureRun(ctx, repo, prs, c)
	if err != nil {
		return err
	}
	if err := checkOutcomes(outs); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "dry run (no network): %d Jev requests, ~%d tokens estimated, %d already recorded in %s, %d to record\n",
		c.requests, c.tokens, c.recorded, cfg.out, c.requests-c.recorded)
	fmt.Fprintln(stdout, "record with: go run ./tools/recordjev --live   (needs JEV_API_KEY; cap $"+fmt.Sprintf("%.2f", cfg.capUSD)+")")
	return nil
}

// recordLive records with the real endpoint behind the budget guard and
// appends the run to the ledger, also when the run failed after spending.
func recordLive(ctx context.Context, cfg config, repo, prs string, getenv env, stdout io.Writer) error {
	prior, err := ledgerSpent(cfg.spend, "jev")
	if err != nil {
		return err
	}
	live := jev.NewHTTP(getenv("JEV_BASE_URL"), getenv("JEV_API_KEY"))
	g := &guard{next: &jev.Recorder{Live: live, Dir: cfg.out}, dir: cfg.out, capUSD: cfg.capUSD, prior: prior}
	outs, runErr := pipelinetest.FixtureRun(ctx, repo, prs, g)
	if runErr == nil {
		runErr = checkOutcomes(outs)
	}
	calls, reused, spent := g.totals()
	row := ledgerRow{At: time.Now().UTC(), Service: "jev", Calls: calls, Cost: spent, Cumulative: prior + spent, Written: calls}
	if err := appendLedger(cfg.spend, row); err != nil {
		return errors.Join(runErr, err)
	}
	fmt.Fprintf(stdout, "live: %d calls, %d reused from recordings, run cost $%.4f, cumulative Jev spend $%.4f (cap $%.2f), %d recordings written to %s\n",
		calls, reused, spent, prior+spent, cfg.capUSD, calls, cfg.out)
	if runErr == nil {
		warnExpectations(outs, stdout)
	}
	return runErr
}

// recordSynthetic writes heuristic recordings for requests not yet recorded.
func recordSynthetic(ctx context.Context, cfg config, repo, prs string, stdout io.Writer) error {
	fake := &jev.Fake{Answer: heuristicAnswer}
	g := &guard{next: &jev.Recorder{Live: fake, Dir: cfg.out, Synthetic: true}, dir: cfg.out, capUSD: 1e9, acceptSynthetic: true}
	outs, err := pipelinetest.FixtureRun(ctx, repo, prs, g)
	if err == nil {
		err = checkOutcomes(outs)
	}
	calls, reused, _ := g.totals()
	fmt.Fprintf(stdout, "synthetic: %d recordings written, %d reused, in %s\n", calls, reused, cfg.out)
	if err == nil {
		warnExpectations(outs, stdout)
	}
	return err
}

// checkOutcomes fails on any failed stage or missing labels, quoting the
// exact stage error.
func checkOutcomes(outs map[int]pipelinetest.Outcome) error {
	if ps := pipelinetest.Problems(outs); len(ps) > 0 {
		return errors.New(strings.Join(ps, "\n"))
	}
	return nil
}

// warnExpectations prints how the recorded answers deviate from what the
// replay test asserts (the run itself succeeded; re-recording will not fix
// model judgments, so these are warnings for the integrator).
func warnExpectations(outs map[int]pipelinetest.Outcome, stdout io.Writer) {
	ps := pipelinetest.Expectations(outs)
	if len(ps) == 0 {
		fmt.Fprintln(stdout, "replay expectations: all hold")
		return
	}
	fmt.Fprintf(stdout, "WARNING: %d replay expectations do not hold with these recordings:\n", len(ps))
	for _, p := range ps {
		fmt.Fprintln(stdout, "  -", p)
	}
}

// counter is the dry-run client: it counts requests, estimates their
// tokens, notes which are already recorded and answers with next.
type counter struct {
	next jev.Client
	dir  string

	mu                         sync.Mutex
	requests, tokens, recorded int
}

// Evaluate implements jev.Client.
func (c *counter) Evaluate(ctx context.Context, req jev.Request) (jev.Response, error) {
	if req.Model == "" {
		req.Model = jev.Model
	}
	b, err := json.Marshal(req)
	if err != nil {
		return jev.Response{}, fmt.Errorf("recordjev: encode: %w", err)
	}
	key, err := jev.RequestKey(req)
	if err != nil {
		return jev.Response{}, fmt.Errorf("recordjev: request key: %w", err)
	}
	_, statErr := os.Stat(filepath.Join(c.dir, key+".json"))
	c.mu.Lock()
	c.requests++
	c.tokens += jev.EstimateTokens(string(b))
	if statErr == nil {
		c.recorded++
	}
	c.mu.Unlock()
	return c.next.Evaluate(ctx, req)
}
