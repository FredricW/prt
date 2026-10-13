package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	prtconfig "github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline/pipelinetest"
	"github.com/fredricw/prt/internal/review"
)

// ErrReviewBudget is returned (instead of calling the reviewer) when a
// review call could push the Anthropic spend over --review-cap or the
// total model spend over totalCapUSD. It is not review.ErrUnavailable, so
// the review fails loudly and the tool exits non-zero.
var ErrReviewBudget = errors.New("recordjev: review budget cap reached")

// totalCapUSD is the cap on real model spend across all services (DECISIONS
// "Real model spend capped at $5").
const totalCapUSD = 5.00

// anthropicBaseURLEnv overrides the Messages API base URL of the live
// reviewers (a local server in tests, or a proxy); unset means
// review.AnthropicBaseURL.
const anthropicBaseURLEnv = "PRT_ANTHROPIC_BASE_URL"

// dryRunConfidence is the confidence of the dry run's fake reviews (above
// escalate_below, so the dry run counts no escalations).
const dryRunConfidence = 0.9

// recordReview runs review mode: Jev answers are replayed strictly from
// --out (a miss fails the run; Jev is never called), embeddings as chosen
// by --embed, and the reviewers are the fixture repo's configured models —
// fakes in the dry run, the live adapters behind recorder and budget guard
// with --live.
func recordReview(ctx context.Context, cfg config, repo, prs string, getenv env, stdout io.Writer, opts []pipelinetest.Option) error {
	fast, deep, err := fixtureModels(ctx, repo)
	if err != nil {
		return err
	}
	jc := &jev.Replay{Dir: cfg.out}
	if cfg.live {
		return reviewLive(ctx, cfg, repo, prs, jc, [2]string{fast, deep}, getenv, stdout, opts)
	}
	return reviewDryRun(ctx, cfg, repo, prs, jc, [2]string{fast, deep}, stdout, opts)
}

// fixtureModels returns the fast and deep model specs of the fixture repo's
// config (the same config pipelinetest.FixtureRun loads).
func fixtureModels(ctx context.Context, repo string) (fast, deep string, err error) {
	g, err := gitx.Open(ctx, repo)
	if err != nil {
		return "", "", fmt.Errorf("open fixture repo: %w", err)
	}
	gitDir, err := g.GitDir(ctx)
	if err != nil {
		return "", "", fmt.Errorf("fixture git dir: %w", err)
	}
	c, err := prtconfig.Load([]string{filepath.Join(gitDir, "prt")})
	if err != nil {
		return "", "", fmt.Errorf("fixture config: %w", err)
	}
	return c.Models.Fast, c.Models.Deep, nil
}

// reviewDryRun runs the pipeline with fake reviewers and prints how many
// review requests a live run would send, their estimated tokens and
// upper-bound cost, and how many are already recorded. No network, no spend.
func reviewDryRun(ctx context.Context, cfg config, repo, prs string, jc jev.Client, specs [2]string, stdout io.Writer, opts []pipelinetest.Option) error {
	counters := [2]*reviewCounter{}
	for i, spec := range specs {
		counters[i] = &reviewCounter{next: &review.Fake{Name: spec, Respond: review.FakeRespond(dryRunConfidence)}, dir: cfg.reviewOut}
	}
	tiers := review.Tiers{Fast: counters[0], Deep: counters[1]}
	outs, branches, err := fixtureRun(ctx, cfg, repo, prs, jc, append(opts, pipelinetest.WithReviewers(tiers)))
	if err != nil {
		return err
	}
	if err := checkRun(outs, branches); err != nil {
		return err
	}
	prior, err := ledgerSpent(cfg.spend, "anthropic")
	if err != nil {
		return err
	}
	var sum reviewCount
	for _, c := range counters {
		sum.add(c.snapshot())
	}
	fmt.Fprintf(stdout, "review dry run (no LLM calls): %d requests (fast %d, deep %d; %s), ~%d input tokens estimated, upper-bound cost $%.4f\n",
		sum.requests, counters[0].snapshot().requests, counters[1].snapshot().requests, sum.taskSummary(), sum.tokens, sum.cost)
	fmt.Fprintf(stdout, "review recordings: %d already recorded in %s, %d to record (upper bound $%.4f; at most $%.4f more if every new call were cut off at max_tokens and retried with the cap doubled); anthropic spent $%.4f of the $%.2f review cap\n",
		sum.recorded, cfg.reviewOut, sum.requests-sum.recorded, sum.toRecordCost, sum.retryCost, prior, cfg.reviewCap)
	fmt.Fprintf(stdout, "fake reviews answer confidence %.2f, so escalations do not show here; a live run may add deep calls within the cap\n", dryRunConfidence)
	branchFlag := ""
	if cfg.branches {
		branchFlag = " --branches"
	}
	fmt.Fprintf(stdout, "record with: make record-review   (go run ./tools/recordjev --live --review --embed %s%s; needs %s)\n", cfg.embed, branchFlag, review.AnthropicKeyEnv)
	return nil
}

// reviewLive records real review calls: each tier's adapter is wrapped in
// review.Recorder and a reviewGuard sharing one budget. A ledger row
// (service "anthropic") is appended after the run, also a failed one.
func reviewLive(ctx context.Context, cfg config, repo, prs string, jc jev.Client, specs [2]string, getenv env, stdout io.Writer, opts []pipelinetest.Option) error {
	prior, err := ledgerSpent(cfg.spend, "anthropic")
	if err != nil {
		return err
	}
	total, err := ledgerTotal(cfg.spend)
	if err != nil {
		return err
	}
	budget := &reviewBudget{capUSD: cfg.reviewCap, totalCap: totalCapUSD, prior: prior, priorTotal: total}
	guards := [2]*reviewGuard{}
	for i, spec := range specs {
		live, err := liveReviewer(spec, getenv)
		if err != nil {
			return err
		}
		guards[i] = &reviewGuard{next: &review.Recorder{Live: live, Dir: cfg.reviewOut}, dir: cfg.reviewOut, budget: budget}
	}
	tiers := review.Tiers{Fast: guards[0], Deep: guards[1]}
	outs, branches, runErr := fixtureRun(ctx, cfg, repo, prs, jc, append(opts, pipelinetest.WithReviewers(tiers)))
	if runErr == nil {
		runErr = checkRun(outs, branches)
	}
	var st guardStats
	for _, g := range guards {
		st.add(g.stats())
	}
	spent, refused := budget.totals()
	row := ledgerRow{At: time.Now().UTC(), Service: "anthropic", Calls: st.calls, Cost: spent, Cumulative: prior + spent, Written: st.written}
	if err := appendLedger(cfg.spend, row); err != nil {
		return errors.Join(runErr, err)
	}
	fmt.Fprintf(stdout, "review live: %d calls, %d reused from recordings, run cost $%.4f, cumulative anthropic spend $%.4f (cap $%.2f; all services $%.4f of $%.2f), %d recordings written to %s\n",
		st.calls, st.reused, spent, prior+spent, cfg.reviewCap, total+spent, totalCapUSD, st.written, cfg.reviewOut)
	for _, w := range st.underEstimates {
		fmt.Fprintln(stdout, "WARNING: input-token estimate below actual usage:", w)
	}
	for _, tr := range st.truncated {
		fmt.Fprintln(stdout, "TRUNCATED (recorded; the pipeline retries it with the cap doubled):", tr)
	}
	for _, f := range st.failures {
		fmt.Fprintln(stdout, "FAILED:", f)
	}
	// Refused calls never reach note, so they are not counted here.
	if n := len(st.failures) + st.moreFailures; n > 0 {
		runErr = errors.Join(runErr, fmt.Errorf("recordjev: %d review calls failed", n))
	}
	if refused > 0 {
		runErr = errors.Join(runErr, fmt.Errorf("%w: %d review requests not sent", ErrReviewBudget, refused))
	}
	// A truncated call is not a failure while its retry succeeds; a row
	// that still failed (both attempts cut off, or a failed retry) is.
	if rows := failedRows(outs, branches); len(rows) > 0 {
		runErr = errors.Join(runErr, fmt.Errorf("recordjev: %d review rows failed:\n%s", len(rows), strings.Join(rows, "\n")))
	}
	if runErr == nil {
		warnReviewExpectations(outs, stdout)
		warnBranchExpectations(branches, stdout)
	}
	return runErr
}

// failedRows lists the routing rows that ended failed, per item, with
// their error.
func failedRows(outs map[int]pipelinetest.Outcome, branches map[string]pipelinetest.Outcome) []string {
	var out []string
	add := func(id string, o pipelinetest.Outcome) {
		for _, r := range o.Reviews {
			if r.Status == model.ReviewFailed {
				out = append(out, fmt.Sprintf("%s group %q (%s): %s", id, r.GroupName, r.Tier, r.Err))
			}
		}
	}
	for _, n := range pipelinetest.PRNumbers {
		if o, ok := outs[n]; ok {
			add(fmt.Sprintf("#%d", n), o)
		}
	}
	for _, ref := range pipelinetest.BranchRefs {
		if o, ok := branches[ref]; ok {
			add(ref, o)
		}
	}
	return out
}

// liveReviewer builds the live adapter for spec with the key from the
// environment (never printed), honouring PRT_ANTHROPIC_BASE_URL.
func liveReviewer(spec string, getenv env) (review.Reviewer, error) {
	r, err := review.FromSpec(spec, getenv(review.AnthropicKeyEnv))
	if err != nil {
		return nil, fmt.Errorf("fixture models: %w", err)
	}
	if a, ok := r.(*review.Anthropic); ok {
		if base := getenv(anthropicBaseURLEnv); base != "" {
			a.BaseURL = base
		}
	}
	return r, nil
}

// warnReviewExpectations prints how the recorded run deviates from what
// the review replay test asserts.
func warnReviewExpectations(outs map[int]pipelinetest.Outcome, stdout io.Writer) {
	ps := pipelinetest.ReviewExpectations(outs)
	if len(ps) == 0 {
		fmt.Fprintln(stdout, "review expectations: all hold")
		return
	}
	fmt.Fprintf(stdout, "WARNING: %d review expectations do not hold with these recordings:\n", len(ps))
	for _, p := range ps {
		fmt.Fprintln(stdout, "  -", p)
	}
}

// reviewBudget is the spend budget shared by the review guards of one run.
// A call is refused when prior + spent + reserved + its estimate exceeds
// capUSD, or when priorTotal + spent + reserved + its estimate exceeds
// totalCap. Estimates are review.EstimateCost upper bounds; a settled call
// releases its reservation and adds its actual cost.
type reviewBudget struct {
	capUSD     float64 // --review-cap: cumulative Anthropic spend
	totalCap   float64 // cumulative spend over all services
	prior      float64 // Anthropic ledger spend before this run
	priorTotal float64 // ledger spend of all services before this run

	mu       sync.Mutex
	spent    float64
	reserved float64
	refused  int
}

// reserve books est for a call or refuses it with ErrReviewBudget.
func (b *reviewBudget) reserve(est float64, what string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	pending := b.spent + b.reserved + est
	if b.prior+pending > b.capUSD {
		b.refused++
		return fmt.Errorf("%w: %s not sent: anthropic $%.4f before this run + $%.4f this run + $%.4f in flight + estimate $%.4f > review cap $%.2f",
			ErrReviewBudget, what, b.prior, b.spent, b.reserved, est, b.capUSD)
	}
	if b.priorTotal+pending > b.totalCap {
		b.refused++
		return fmt.Errorf("%w: %s not sent: all services $%.4f before this run + $%.4f this run + $%.4f in flight + estimate $%.4f > total cap $%.2f",
			ErrReviewBudget, what, b.priorTotal, b.spent, b.reserved, est, b.totalCap)
	}
	b.reserved += est
	return nil
}

// settle releases a reservation and adds the call's actual cost.
func (b *reviewBudget) settle(est, cost float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reserved -= est
	b.spent += cost
}

// totals returns this run's spend and the number of refused calls.
func (b *reviewBudget) totals() (spent float64, refused int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent, b.refused
}

// reviewGuard sits in front of a review.Recorder over a live reviewer.
// For each request it:
//
//  1. serves an existing real recording from dir (no spend); synthetic
//     recordings do not count and are re-recorded;
//  2. reserves review.EstimateCost against the shared budget, refusing
//     with ErrReviewBudget when it does not fit;
//  3. calls next and settles the reservation with Result.Cost (also when
//     the call failed after spending, e.g. an unparseable review).
type reviewGuard struct {
	next   review.Reviewer
	dir    string
	budget *reviewBudget

	mu sync.Mutex
	st guardStats
}

// guardStats are one guard's counters.
type guardStats struct {
	calls          int // calls sent to next that were billed or succeeded
	reused         int // requests served from recordings
	written        int // calls the Recorder wrote (successful or truncated)
	underEstimates []string
	// truncated are the calls cut off at max_tokens (recorded as
	// Truncated; the pipeline retries them with a doubled cap).
	truncated []string
	// failures are the first maxFailuresShown failed live calls (the
	// pipeline marks such rows failed and carries on, so without this a
	// run where every call is rejected would exit 0); moreFailures counts
	// the rest.
	failures     []string
	moreFailures int
}

// maxFailuresShown bounds the failed calls one guard reports.
const maxFailuresShown = 5

// add folds o into s.
func (s *guardStats) add(o guardStats) {
	s.calls += o.calls
	s.reused += o.reused
	s.written += o.written
	s.underEstimates = append(s.underEstimates, o.underEstimates...)
	s.truncated = append(s.truncated, o.truncated...)
	s.failures = append(s.failures, o.failures...)
	s.moreFailures += o.moreFailures
}

var _ review.Reviewer = (*reviewGuard)(nil)

// Model implements review.Reviewer.
func (g *reviewGuard) Model() string { return g.next.Model() }

// Ready implements review.Checker by asking the wrapped reviewer.
func (g *reviewGuard) Ready() error { return review.Ready(g.next) }

// Review implements review.Reviewer.
func (g *reviewGuard) Review(ctx context.Context, req review.Request) (review.Result, error) {
	rec, ok, err := review.LoadRecording(g.dir, g.Model(), req)
	if err != nil {
		return review.Result{}, err
	}
	if ok && !rec.Synthetic {
		g.mu.Lock()
		g.st.reused++
		g.mu.Unlock()
		return (&review.Replay{Name: g.Model(), Dir: g.dir}).Review(ctx, req)
	}
	key, err := review.RequestKey(g.Model(), req)
	if err != nil {
		return review.Result{}, fmt.Errorf("recordjev: review key: %w", err)
	}
	est := review.EstimateCost(g.Model(), req)
	if err := g.budget.reserve(est, fmt.Sprintf("%s request %s (%s)", req.Task, key, g.Model())); err != nil {
		return review.Result{}, err
	}
	res, err := g.next.Review(ctx, req)
	g.budget.settle(est, res.Cost)
	g.note(req, key, res, err)
	return res, err
}

// note updates the counters after a call.
func (g *reviewGuard) note(req review.Request, key string, res review.Result, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	truncated := review.IsTruncated(res, err)
	if err == nil || res.Cost > 0 || truncated { // a cut-off response is billed
		g.st.calls++
	}
	switch {
	case err == nil:
		g.st.written++
	case truncated:
		g.st.written++
		g.st.truncated = append(g.st.truncated, fmt.Sprintf("%s %s (max_tokens %d, %d output tokens)", req.Task, key, req.MaxTokens, res.OutputTokens))
	case len(g.st.failures) < maxFailuresShown:
		g.st.failures = append(g.st.failures, fmt.Sprintf("%s %s: %v", req.Task, key, err))
	default:
		g.st.moreFailures++
	}
	if est := review.EstimateInputTokens(req); res.InputTokens > est {
		g.st.underEstimates = append(g.st.underEstimates, fmt.Sprintf("%s %s: estimated %d, actual %d", req.Task, key, est, res.InputTokens))
	}
}

// stats returns a copy of the counters.
func (g *reviewGuard) stats() guardStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.st
	s.underEstimates = append([]string(nil), g.st.underEstimates...)
	s.truncated = append([]string(nil), g.st.truncated...)
	s.failures = append([]string(nil), g.st.failures...)
	return s
}

// reviewCount is what the dry run counts.
type reviewCount struct {
	requests, recorded, tokens int
	cost, toRecordCost         float64
	// retryCost bounds the truncation retries of the requests to record:
	// review.EstimateCost of each one grown (review.Grow), as if every new
	// call were cut off at max_tokens.
	retryCost float64
	byTask    map[review.Task]int
}

// add folds o into c.
func (c *reviewCount) add(o reviewCount) {
	c.requests += o.requests
	c.recorded += o.recorded
	c.tokens += o.tokens
	c.cost += o.cost
	c.toRecordCost += o.toRecordCost
	c.retryCost += o.retryCost
	for t, n := range o.byTask {
		if c.byTask == nil {
			c.byTask = map[review.Task]int{}
		}
		c.byTask[t] += n
	}
}

// taskSummary renders the per-task counts ("review 5, tldr 4").
func (c reviewCount) taskSummary() string {
	if len(c.byTask) == 0 {
		return "none"
	}
	tasks := make([]string, 0, len(c.byTask))
	for t := range c.byTask {
		tasks = append(tasks, string(t))
	}
	sort.Strings(tasks)
	for i, t := range tasks {
		tasks[i] = fmt.Sprintf("%s %d", t, c.byTask[review.Task(t)])
	}
	return strings.Join(tasks, ", ")
}

// reviewCounter is the dry-run reviewer: it counts requests, estimates
// their tokens and upper-bound cost, notes which are already recorded (for
// real) and answers with next.
type reviewCounter struct {
	next review.Reviewer
	dir  string

	mu sync.Mutex
	c  reviewCount
}

var _ review.Reviewer = (*reviewCounter)(nil)

// Model implements review.Reviewer.
func (c *reviewCounter) Model() string { return c.next.Model() }

// Review implements review.Reviewer.
func (c *reviewCounter) Review(ctx context.Context, req review.Request) (review.Result, error) {
	rec, ok, err := review.LoadRecording(c.dir, c.Model(), req)
	if err != nil {
		return review.Result{}, err
	}
	recorded := ok && !rec.Synthetic
	est := review.EstimateCost(c.Model(), req)
	c.mu.Lock()
	c.c.requests++
	c.c.tokens += review.EstimateInputTokens(req)
	c.c.cost += est
	if c.c.byTask == nil {
		c.c.byTask = map[review.Task]int{}
	}
	c.c.byTask[req.Task]++
	if recorded {
		c.c.recorded++
	} else {
		c.c.toRecordCost += est
		if grown, ok := review.Grow(req); ok {
			c.c.retryCost += review.EstimateCost(c.Model(), grown)
		}
	}
	c.mu.Unlock()
	return c.next.Review(ctx, req)
}

// snapshot returns a copy of the counts.
func (c *reviewCounter) snapshot() reviewCount {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.c
	out.byTask = make(map[review.Task]int, len(c.c.byTask))
	for t, n := range c.c.byTask {
		out.byTask[t] = n
	}
	return out
}
