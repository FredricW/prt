// Command recordjev records real Jev responses for the fixture PRs
// (pipelinetest.PRNumbers: #101–#104 and, since M5.2, #106) by running the
// real pipeline over a freshly generated fixture repo (see
// internal/pipeline/pipelinetest and DECISIONS "M1 Jev recordings flow").
//
// Modes:
//
//	recordjev              dry run: counts the requests a recording run
//	                       would send (with a fake client) and how many are
//	                       already recorded, with separate counts for the
//	                       M2 requests and the M4 comment_addressed ones
//	                       (#101's two earlier viewer comments); no
//	                       network, no spend
//	recordjev --live       records with the real endpoint (JEV_API_KEY,
//	                       JEV_BASE_URL); requests already recorded are not
//	                       sent; stops before a call that could push the
//	                       ledger's cumulative Jev spend over --cap; appends
//	                       the run's spend to --spend
//	recordjev --synthetic  writes heuristic recordings marked
//	                       "synthetic": true (only if live recording is
//	                       blocked); no network
//
// Embeddings (--embed, any mode; pair requests depend on embed candidates,
// so recording and replay must use the same choice):
//
//	--embed none    no embedder: stage 3 is skipped
//	--embed replay  embed.Replay over --embed-out (the default when that
//	                directory holds recordings, else none)
//	--embed live    local Ollama (OLLAMA_HOST, default
//	                http://127.0.0.1:11434; embeddinggemma-2) through
//	                embed.Recorder, writing --embed-out; free, so allowed in
//	                the dry run (which then records embeddings only); appends
//	                an "ollama" ledger row with cost 0. If Ollama cannot be
//	                reached the error, date and command go to --blocked
//	                (docs/BLOCKED.md) and the tool exits non-zero.
//
// Reviews (--review, M3): runs the same fixture pipeline to record the
// stage-7 LLM calls (group reviews, TL;DR, group titles) into --review-out
// (testdata/recordings/review). Jev answers are replayed strictly from
// --out (a missing recording is an error; Jev is never called), embeddings
// follow --embed, and the reviewers are the fixture repo's configured
// models (models.fast / models.deep).
//
//	recordjev --review         dry run: fake reviewers (confidence 0.9);
//	                           prints the review requests, estimated input
//	                           tokens, the upper-bound cost and how many are
//	                           already recorded; no network, no spend
//	recordjev --live --review  records with the Anthropic Messages API
//	                           (PRT_ANTHROPIC_API_KEY; PRT_ANTHROPIC_BASE_URL
//	                           overrides the endpoint). Recorded requests are
//	                           served without a call; a call is refused when
//	                           the Anthropic ledger spend + this run + in-flight
//	                           estimates + its review.EstimateCost would pass
//	                           --review-cap (default $3.00), or the ledger's
//	                           total over all services would pass $5.00; an
//	                           "anthropic" ledger row is appended after every
//	                           run, also a failed one
//
// Branches (--branches, any mode; M5): after the PRs, the fixture's local
// branches without a PR (pipelinetest.BranchRefs: 05-spike-dark-mode) run
// as branch items in the same store, so their unit labels, pairs, names,
// branch.state (Jev) and reviews, TL;DR, titles and suggested PR title
// (review mode) are counted or recorded too. The dry run then prints an
// "M5 requests" line.
//
// Unused recordings (--unused, M5.2): replays the PRs and branches with
// Jev, embeddings and reviews all read strictly from their directories and
// lists every recording file no request of that run reads; --prune deletes
// them. No network, no spend.
//
//	recordjev --unused           list
//	recordjev --unused --prune   delete
//
// A review cut off at max_tokens is recorded too (review.Recording
// Truncated) and is not counted as a failed call: the pipeline retries it
// with the cap doubled, a request of its own. The run fails when a routing
// row still ends failed.
//
// The Makefile targets are:
//
//	make record-review      go run ./tools/recordjev --live --review --embed replay
//	make record-review-dry  go run ./tools/recordjev --review --embed replay
//
// Recording files are written only by jev.Recorder and review.Recorder,
// which never see API keys. Keys are read from the environment and never
// printed.
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
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline/pipelinetest"
	"github.com/fredricw/prt/internal/review"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// config is the parsed command line.
type config struct {
	live, synthetic bool
	out, spend, src string
	capUSD          float64

	embed, embedOut, blocked string
	args                     []string // as given, for BLOCKED.md

	review    bool    // record review calls instead of Jev answers
	reviewOut string  // review recordings directory
	reviewCap float64 // cap on cumulative Anthropic spend in USD

	// branches also runs the fixture's local branches without a PR
	// (pipelinetest.BranchRefs, M5) after the PRs, in every mode.
	branches bool

	// unused lists the recordings no replay reads (unusedRun); prune
	// deletes them.
	unused, prune bool
}

// env looks up an environment variable ("" when unset); injected for tests.
type env func(string) string

// run is main without process globals; it returns the exit code.
func run(ctx context.Context, args []string, getenv env, stdout, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		return 2
	}
	switch {
	case cfg.live && cfg.review && getenv(review.AnthropicKeyEnv) == "":
		fmt.Fprintf(stderr, "recordjev: --live --review needs %s in the environment; nothing was sent\n", review.AnthropicKeyEnv)
		return 2
	case cfg.live && !cfg.review && getenv("JEV_API_KEY") == "":
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
	fs.StringVar(&c.embed, "embed", "", "embedder: none, replay or live (default: replay when --embed-out has recordings, else none)")
	fs.StringVar(&c.embedOut, "embed-out", filepath.Join(root, "testdata", "recordings", "embed"), "embedding recordings directory")
	fs.StringVar(&c.blocked, "blocked", filepath.Join(root, "docs", "BLOCKED.md"), "where an unreachable Ollama is recorded (--embed live)")
	fs.BoolVar(&c.review, "review", false, "record review LLM calls (Jev replayed from --out); with --live needs "+review.AnthropicKeyEnv)
	fs.StringVar(&c.reviewOut, "review-out", filepath.Join(root, "testdata", "recordings", "review"), "review recordings directory")
	fs.Float64Var(&c.reviewCap, "review-cap", 3.00, "cap on cumulative Anthropic spend in USD (ledger + this run)")
	fs.BoolVar(&c.branches, "branches", false, "also run the fixture's local branches without a PR (05-spike-dark-mode) after the PRs")
	fs.BoolVar(&c.unused, "unused", false, "list the recordings (jev, embed, review) that no replay of the fixture PRs and branches reads; no network")
	fs.BoolVar(&c.prune, "prune", false, "with --unused: delete the unused recordings")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	c.args = args
	if c.prune && !c.unused {
		fmt.Fprintln(stderr, "recordjev: --prune needs --unused")
		return c, errors.New("prune without unused")
	}
	if c.unused && (c.live || c.synthetic || c.review) {
		fmt.Fprintln(stderr, "recordjev: --unused runs alone (no --live, --synthetic or --review)")
		return c, errors.New("exclusive flags")
	}
	if c.live && c.synthetic {
		fmt.Fprintln(stderr, "recordjev: --live and --synthetic are exclusive")
		return c, errors.New("exclusive flags")
	}
	if c.review && c.synthetic {
		fmt.Fprintln(stderr, "recordjev: --review and --synthetic are exclusive")
		return c, errors.New("exclusive flags")
	}
	mode, err := resolveEmbedMode(c.embed, c.embedOut)
	if err != nil {
		fmt.Fprintln(stderr, "recordjev:", err)
		return c, err
	}
	c.embed = mode
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
	if cfg.unused {
		// Reads every kind of recording itself; no embed run to report.
		return unusedRun(ctx, cfg, repo, prs, stdout)
	}
	emb, err := newEmbedRun(ctx, cfg, getenv)
	if err != nil {
		return err
	}
	opts := emb.options()
	switch {
	case cfg.review:
		err = recordReview(ctx, cfg, repo, prs, getenv, stdout, opts)
	case cfg.live:
		err = recordLive(ctx, cfg, repo, prs, getenv, stdout, opts)
	case cfg.synthetic:
		err = recordSynthetic(ctx, cfg, repo, prs, stdout, opts)
	default:
		err = dryRun(ctx, cfg, repo, prs, stdout, opts)
	}
	return errors.Join(err, emb.finish(stdout))
}

// dryRun counts the requests a recording run would send (with fake Jev
// answers, so name and split requests, which depend on the pair answers,
// are approximate). With --embed live it also records embeddings.
func dryRun(ctx context.Context, cfg config, repo, prs string, stdout io.Writer, opts []pipelinetest.Option) error {
	c := &counter{next: &jev.Fake{}, dir: cfg.out}
	outs, branches, err := fixtureRun(ctx, cfg, repo, prs, c, opts)
	if err != nil {
		return err
	}
	if err := checkRun(outs, branches); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "dry run (no Jev calls): %d Jev requests, ~%d tokens estimated, %d already recorded in %s, %d to record\n",
		c.requests, c.tokens, c.recorded, cfg.out, c.requests-c.recorded)
	fmt.Fprintln(stdout, c.m2Summary())
	fmt.Fprintln(stdout, c.m4Summary())
	flags := "--live --embed " + cfg.embed
	if cfg.branches {
		fmt.Fprintln(stdout, c.m5Summary())
		flags += " --branches"
	}
	fmt.Fprintln(stdout, "record with: go run ./tools/recordjev "+flags+"   (needs JEV_API_KEY; cap $"+fmt.Sprintf("%.2f", cfg.capUSD)+")")
	return nil
}

// recordLive records with the real endpoint behind the budget guard and
// appends the run to the ledger, also when the run failed after spending.
func recordLive(ctx context.Context, cfg config, repo, prs string, getenv env, stdout io.Writer, opts []pipelinetest.Option) error {
	prior, err := ledgerSpent(cfg.spend, "jev")
	if err != nil {
		return err
	}
	live := jev.NewHTTP(getenv("JEV_BASE_URL"), getenv("JEV_API_KEY"))
	g := &guard{next: &jev.Recorder{Live: live, Dir: cfg.out}, dir: cfg.out, capUSD: cfg.capUSD, prior: prior}
	outs, branches, runErr := fixtureRun(ctx, cfg, repo, prs, g, opts)
	if runErr == nil {
		runErr = checkRun(outs, branches)
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
		warnGroupExpectations(outs, stdout)
		warnCommentExpectations(outs, stdout)
		warnBranchExpectations(branches, stdout)
	}
	return runErr
}

// warnCommentExpectations prints how the recorded run's comment layer and
// #101's comment_addressed answers deviate from pipelinetest's
// CommentExpectations and AddressedExpectations (asserted by the replay
// tests; the latter once the answers are recorded).
func warnCommentExpectations(outs map[int]pipelinetest.Outcome, stdout io.Writer) {
	ps := pipelinetest.CommentExpectations(outs)
	addressed, recorded := pipelinetest.AddressedExpectations(outs[101])
	if !recorded {
		addressed = append(addressed, "#101: comment_addressed answers unknown (requests failed or were refused)")
	}
	ps = append(ps, addressed...)
	if len(ps) == 0 {
		fmt.Fprintln(stdout, "comment expectations: all hold")
		return
	}
	fmt.Fprintf(stdout, "WARNING: %d comment expectations do not hold with these recordings:\n", len(ps))
	for _, p := range ps {
		fmt.Fprintln(stdout, "  -", p)
	}
}

// recordSynthetic writes heuristic recordings for requests not yet recorded.
func recordSynthetic(ctx context.Context, cfg config, repo, prs string, stdout io.Writer, opts []pipelinetest.Option) error {
	fake := &jev.Fake{Answer: heuristicAnswer}
	g := &guard{next: &jev.Recorder{Live: fake, Dir: cfg.out, Synthetic: true}, dir: cfg.out, capUSD: 1e9, acceptSynthetic: true}
	outs, branches, err := fixtureRun(ctx, cfg, repo, prs, g, opts)
	if err == nil {
		err = checkRun(outs, branches)
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

// warnGroupExpectations prints how the recorded run's grouping deviates
// from pipelinetest.GroupExpectations (asserted by the replay test once M2
// recordings exist).
func warnGroupExpectations(outs map[int]pipelinetest.Outcome, stdout io.Writer) {
	ps := pipelinetest.GroupExpectations(outs)
	if len(ps) == 0 {
		fmt.Fprintln(stdout, "grouping expectations: all hold")
		return
	}
	fmt.Fprintf(stdout, "WARNING: %d grouping expectations do not hold with these recordings:\n", len(ps))
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
	m2                         map[string]*kindCount // by m2Kind
	addressed                  kindCount             // comment_addressed (M4)
	branch                     kindCount             // branch.state (M5)
}

// isAddressed reports whether req asks comment_addressed (M4 "Since you
// looked": one request per earlier comment of the viewer).
func isAddressed(req jev.Request) bool {
	for name := range req.Questions {
		if base, _, _ := strings.Cut(name, "__"); base == model.QCommentAddressed {
			return true
		}
	}
	return false
}

// m4Summary renders the comment_addressed request counts.
func (c *counter) m4Summary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.addressed
	return fmt.Sprintf("M4 requests: %s %d (%d recorded, %d to record) — one per earlier viewer comment (Since you looked)",
		model.QCommentAddressed, a.requests, a.recorded, a.requests-a.recorded)
}

// kindCount counts requests of one M2 kind and how many are recorded.
type kindCount struct{ requests, recorded int }

// m2Kinds are the M2 request kinds in display order, by question id.
var m2Kinds = []struct{ kind, question string }{
	{"pairs", model.QRelation},
	{"names", model.QNameBullet},
	{"split", model.QSeparatePR},
}

// m2Kind returns the M2 kind of a request ("" for label requests): the
// kind of its first question whose wire name is an M2 question id (or
// "<id>__…").
func m2Kind(req jev.Request) string {
	for name := range req.Questions {
		base, _, _ := strings.Cut(name, "__")
		for _, k := range m2Kinds {
			if base == k.question {
				return k.kind
			}
		}
	}
	return ""
}

// m2Summary renders the M2 request counts.
func (c *counter) m2Summary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	parts := make([]string, 0, len(m2Kinds))
	for _, k := range m2Kinds {
		n := c.m2[k.kind]
		if n == nil {
			n = &kindCount{}
		}
		parts = append(parts, fmt.Sprintf("%s %d (%d recorded)", k.kind, n.requests, n.recorded))
	}
	return "M2 requests: " + strings.Join(parts, ", ") + " — names and split depend on the pair answers, so live counts may differ"
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
	c.tokens += jev.EstimateRequestTokensLen(len(b))
	if statErr == nil {
		c.recorded++
	}
	if k := m2Kind(req); k != "" {
		if c.m2 == nil {
			c.m2 = map[string]*kindCount{}
		}
		if c.m2[k] == nil {
			c.m2[k] = &kindCount{}
		}
		c.m2[k].requests++
		if statErr == nil {
			c.m2[k].recorded++
		}
	}
	if isAddressed(req) {
		c.addressed.requests++
		if statErr == nil {
			c.addressed.recorded++
		}
	}
	if isBranchState(req) {
		c.branch.requests++
		if statErr == nil {
			c.branch.recorded++
		}
	}
	c.mu.Unlock()
	return c.next.Evaluate(ctx, req)
}
