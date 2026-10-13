package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
)

// M5.1: live settings, FreeOnly runs and stop/resume.

// failJev, failEmbedder and failReviewer fail the test on any call.
type failJev struct{ t *testing.T }

func (f failJev) Evaluate(context.Context, jev.Request) (jev.Response, error) {
	f.t.Errorf("Jev called in a run that must not spend")
	return jev.Response{}, jev.ErrUnavailable
}

type failEmbedder struct{ t *testing.T }

func (f failEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	f.t.Errorf("embedder called in a run that must not spend")
	return nil, embed.ErrUnavailable
}

func (failEmbedder) Model() string { return "fake:embed" }

type failReviewer struct {
	t    *testing.T
	name string
}

func (f failReviewer) Review(_ context.Context, req review.Request) (review.Result, error) {
	f.t.Errorf("reviewer %s called (%s) in a run that must not spend", f.name, req.Task)
	return review.Result{}, review.ErrUnavailable
}

func (f failReviewer) Model() string { return f.name }

// failModels are models whose every call fails the test; the names match
// m3Env's, so cache keys are the same.
func failModels(t *testing.T) Models {
	return Models{
		Jev: failJev{t}, Embedder: failEmbedder{t},
		Reviewers: review.Tiers{Fast: failReviewer{t, "fake:fast"}, Deep: failReviewer{t, "fake:deep"}},
	}
}

// gateReviewer answers like review.FakeRespond(0.9) but holds the review
// calls block selects (by 1-based review call number) until release is
// closed (nil: until the call's context is done). Prose calls never wait.
type gateReviewer struct {
	name    string
	inner   *review.Fake
	block   func(n int) bool
	started chan struct{} // one value per held call
	release chan struct{}

	mu      sync.Mutex
	reviews int
}

func newGate(name string, block func(int) bool, release chan struct{}) *gateReviewer {
	return &gateReviewer{
		name: name, inner: &review.Fake{Name: name, Respond: review.FakeRespond(0.9)},
		block: block, started: make(chan struct{}, 16), release: release,
	}
}

func (g *gateReviewer) Model() string { return g.name }

func (g *gateReviewer) Review(ctx context.Context, req review.Request) (review.Result, error) {
	if req.Task == review.TaskReview {
		g.mu.Lock()
		g.reviews++
		n := g.reviews
		g.mu.Unlock()
		if g.block != nil && g.block(n) {
			g.started <- struct{}{}
			select {
			case <-ctx.Done():
				return review.Result{}, ctx.Err()
			case <-g.release:
			}
		}
	}
	return g.inner.Review(ctx, req)
}

// reviewCount counts the review calls made (held ones included).
func (g *gateReviewer) reviewCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reviews
}

// stagesOf lists the stages of rows (in order, without duplicates).
func stagesOf(rows []model.RunLog) []model.Stage {
	seen := map[model.Stage]bool{}
	var out []model.Stage
	for _, rl := range rows {
		if !seen[rl.Stage] {
			seen[rl.Stage] = true
			out = append(out, rl.Stage)
		}
	}
	return out
}

func storedRunLogs(t *testing.T, e *labelEnv) []model.RunLog {
	t.Helper()
	rls, err := e.st.RunLogs(context.Background(), e.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rls
}

// A FreeOnly full run does fetch, parse and link and spends nothing.
func TestFreeOnlyRunNoModelCalls(t *testing.T) {
	e := newLabelEnv(t)
	p := e.pipeline(nil, func(d *Deps) {
		m := failModels(t)
		d.Jev, d.Embedder, d.Reviewers = m.Jev, m.Embedder, m.Reviewers
	})
	ctx := context.Background()
	if err := p.RunWith(ctx, e.item, RunOptions{FreeOnly: true}); err != nil {
		t.Fatal(err)
	}
	free := map[model.Stage]bool{model.StageFetch: true, model.StageParse: true, model.StageLink: true}
	for _, rows := range [][]model.RunLog{e.events, storedRunLogs(t, e)} {
		if got := stagesOf(rows); len(got) != 3 {
			t.Errorf("stages %v, want fetch, parse, link", got)
		}
		for _, rl := range rows {
			if !free[rl.Stage] {
				t.Errorf("free-only run has a %s row: %+v", rl.Stage, rl)
			}
		}
	}
	if rl := e.stage(t, model.StageLink); rl.Status != model.StageDone {
		t.Errorf("link row %+v", rl)
	}
	if fs, err := e.st.Files(ctx, e.item.ID); err != nil || len(fs) == 0 {
		t.Errorf("files not stored: %v %v", fs, err)
	}
	e.units(t)
	if _, ok, _ := e.st.Agg(ctx, e.item.ID); ok {
		t.Error("free-only run stored an aggregate")
	}
	if gs, _ := e.st.Groups(ctx, e.item.ID); len(gs) != 0 {
		t.Errorf("free-only run stored groups %+v", gs)
	}
	if rows := itemRows(t, e); len(rows) != 0 {
		t.Errorf("free-only run stored routing rows %+v", rows)
	}
	if ls := e.labels(t); len(ls) != 0 {
		t.Errorf("free-only run stored labels %+v", ls)
	}
}

// The FreeOnly guards keep the model names and refuse every call.
func TestFreeOnlyGuards(t *testing.T) {
	m := freeOnlyModels(failModels(t))
	ctx := context.Background()
	if _, err := m.Jev.Evaluate(ctx, jev.Request{}); !errors.Is(err, jev.ErrUnavailable) {
		t.Errorf("jev guard err %v", err)
	}
	if _, err := m.Embedder.Embed(ctx, []string{"x"}); !errors.Is(err, embed.ErrUnavailable) || m.Embedder.Model() != "fake:embed" {
		t.Errorf("embed guard err %v model %q", err, m.Embedder.Model())
	}
	for _, rev := range []review.Reviewer{m.Reviewers.Fast, m.Reviewers.Deep} {
		if _, err := rev.Review(ctx, review.Request{}); !errors.Is(err, review.ErrUnavailable) || review.Ready(rev) == nil {
			t.Errorf("reviewer guard %s err %v ready %v", rev.Model(), err, review.Ready(rev))
		}
	}
	if m.Reviewers.Fast.Model() != "fake:fast" || m.Reviewers.Deep.Model() != "fake:deep" {
		t.Error("guards lost the model names")
	}
	if none := freeOnlyModels(Models{}); none.Jev != nil || none.Embedder != nil || none.Reviewers.Fast != nil || none.Reviewers.Deep != nil {
		t.Errorf("nil clients guarded: %+v", none)
	}
}

// A settings change re-aggregates with FreeOnly: new weights apply, the
// TL;DR comes from its cache or stays as stored, nothing is called.
func TestFreeOnlyAggregateOnlyCacheOnly(t *testing.T) {
	e, p, _ := m3Env(t, review.FakeRespond(0.9))
	ctx := context.Background()
	e.run(t, p)
	markAnalysed(t, e)
	prev := e.agg(t)
	if prev.TLDR != review.FakeTLDR {
		t.Fatalf("first TL;DR %q", prev.TLDR)
	}
	// Mark the stored block so "kept" and "from the cache" differ.
	const stored = "stored TL;DR"
	branch := &model.BranchInfo{State: model.BranchWIP, P: 0.7, Known: true, SuggestedTitle: "feat: users"}
	since := &model.SinceLooked{BaseSHA: "b0", HeadSHA: "h1", ChangedUnits: []string{}}
	putAgg := func() {
		t.Helper()
		a := e.agg(t)
		a.TLDR, a.Branch, a.Since = stored, branch, since
		if err := e.st.PutAgg(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	putAgg()

	cfg := p.Config()
	cfg.Rank.WAge += 10
	cfg.Rank.WMinutes += 10
	p.SetConfig(cfg)
	p.SetModels(failModels(t))

	aggOnly := RunOptions{AggregateOnly: true, FreeOnly: true}
	e.events = nil
	if err := p.RunWith(ctx, e.item, aggOnly); err != nil {
		t.Fatal(err)
	}
	got := e.agg(t)
	if got.Urgency == prev.Urgency {
		t.Errorf("urgency %v unchanged by the new weights", got.Urgency)
	}
	if got.TLDR != review.FakeTLDR {
		t.Errorf("cache hit: TL;DR %q, want the cached %q", got.TLDR, review.FakeTLDR)
	}
	if got.Branch == nil || got.Branch.State != branch.State || got.Branch.SuggestedTitle != branch.SuggestedTitle || got.Since == nil || got.Since.BaseSHA != "b0" {
		t.Errorf("branch %+v since %+v not kept", got.Branch, got.Since)
	}
	if rl := e.stage(t, model.StageAggregate); rl.Status == model.StageFailed || rl.Calls != 0 {
		t.Errorf("aggregate row %+v", rl)
	}

	// A record the cache has no TL;DR for keeps the stored text.
	putAgg()
	build := p.fn.buildTLDR
	p.fn.buildTLDR = func(a model.ItemAgg) review.Request {
		req := build(a)
		req.Prompt += "\n(changed)"
		return req
	}
	e.events = nil
	if err := p.RunWith(ctx, e.item, aggOnly); err != nil {
		t.Fatal(err)
	}
	if got := e.agg(t); got.TLDR != stored {
		t.Errorf("cache miss: TL;DR %q, want the stored %q", got.TLDR, stored)
	}
	if rl := e.stage(t, model.StageAggregate); rl.Calls != 0 {
		t.Errorf("aggregate row %+v", rl)
	}
}

// Calibration samples are taken only by runs allowed to spend.
func TestFreeOnlyNoCalibration(t *testing.T) {
	e, p, _ := m3Env(t, review.FakeRespond(0.9), func(d *Deps) {
		d.Routing.CalibrationSample = 1
		d.Routing.EscalateBelow = 0
	})
	ctx := context.Background()
	samples := func() int {
		t.Helper()
		cs, err := e.st.Calibrations(ctx, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return len(cs)
	}
	if err := p.RunWith(ctx, e.item, RunOptions{FreeOnly: true}); err != nil {
		t.Fatal(err)
	}
	if n := samples(); n != 0 {
		t.Fatalf("free-only run took %d calibration samples", n)
	}
	e.run(t, p)
	covered := samples()
	if covered == 0 {
		t.Fatal("covered run took no calibration sample at rate 1")
	}
	markAnalysed(t, e)
	p.SetModels(failModels(t))
	for _, opt := range []RunOptions{{FreeOnly: true}, {FreeOnly: true, AggregateOnly: true}} {
		if err := p.RunWith(ctx, e.item, opt); err != nil {
			t.Fatal(err)
		}
	}
	if n := samples(); n != covered {
		t.Errorf("free-only runs took %d more calibration samples", n-covered)
	}
}

// blockJev holds every call until its context is done.
type blockJev struct {
	once    sync.Once
	started chan struct{}
}

func (b *blockJev) Evaluate(ctx context.Context, _ jev.Request) (jev.Response, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return jev.Response{}, ctx.Err()
}

// runAsync starts a run and returns its result channel.
func runAsync(ctx context.Context, p *Pipeline, it model.Item) <-chan error {
	done := make(chan error, 1)
	go func() { done <- p.RunWith(ctx, it, RunOptions{}) }()
	return done
}

// wait returns the run's error, failing the test after a timeout.
func wait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("run did not finish")
		return nil
	}
}

// checkStoppedAt checks the run stopped at stage s: a stopped row for s
// (emitted and stored), no row of a later stage.
func checkStoppedAt(t *testing.T, e *labelEnv, err error, s model.Stage) {
	t.Helper()
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v, want ErrStopped", err)
	}
	for _, rows := range [][]model.RunLog{e.events, storedRunLogs(t, e)} {
		var last model.RunLog
		for _, rl := range rows {
			if stageIndex(rl.Stage) > stageIndex(s) {
				t.Errorf("row after the stop: %+v", rl)
			}
			if rl.Stage == s {
				last = rl
			}
		}
		if last.Status != model.StageStopped || last.Err != "" || last.Note != noteStopped {
			t.Errorf("%s row %+v, want stopped", s, last)
		}
	}
}

func TestStopDuringJev(t *testing.T) {
	e := newLabelEnv(t)
	bj := &blockJev{started: make(chan struct{})}
	emb := &embed.Fake{}
	p := e.pipeline(bj, func(d *Deps) { d.Embedder = emb })
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	e.events = nil
	done := runAsync(ctx, p, e.item)
	<-bj.started
	cancel(ErrStopped)
	err := wait(t, done)
	checkStoppedAt(t, e, err, model.StageJev)
	bg := context.Background()
	if ls := e.labels(t); len(ls) != 0 {
		t.Errorf("labels stored by a stopped Jev stage: %+v", ls)
	}
	if _, ok, _ := e.st.Agg(bg, e.item.ID); ok {
		t.Error("aggregate stored after a stop")
	}
	if gs, _ := e.st.Groups(bg, e.item.ID); len(gs) != 0 {
		t.Errorf("groups stored after a stop: %+v", gs)
	}
	if emb.Calls == 0 {
		t.Fatal("embed stage did not run before Jev")
	}
	// Resume: embeddings come from the cache.
	before := emb.Calls
	p.SetModels(Models{Jev: &jev.Fake{}, Embedder: emb})
	e.run(t, p)
	if emb.Calls != before {
		t.Errorf("resume embedded again (%d calls)", emb.Calls-before)
	}
	if rl := e.stage(t, model.StageEmbed); rl.Status != model.StageCached {
		t.Errorf("resume embed row %+v", rl)
	}
}

// A stop that lands between stages stops the next one before it starts.
func TestStopBetweenStages(t *testing.T) {
	e := newLabelEnv(t)
	p := e.pipeline(nil)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrStopped)
	e.events = nil
	err := p.RunWith(ctx, e.item, RunOptions{})
	checkStoppedAt(t, e, err, model.StageFetch)
	if len(e.events) != 1 {
		t.Errorf("events %+v, want the one stopped fetch row", e.events)
	}
	// The aggregate-only path stops the same way.
	e.events = nil
	err = p.RunWith(ctx, e.item, RunOptions{AggregateOnly: true})
	if !errors.Is(err, ErrStopped) {
		t.Errorf("aggregate-only err %v", err)
	}
}

// stopEnv runs m3Env with concurrency 1 and a fast reviewer that answers
// the first review and holds the rest until the run's context is done;
// cancel is called with cause once the second review is in flight. The
// third routed group is then still queued.
func stopEnv(t *testing.T, cause error) (*labelEnv, *Pipeline, *gateReviewer, error) {
	t.Helper()
	gate := newGate("fake:fast", func(n int) bool { return n >= 2 }, nil)
	e, p, _ := m3Env(t, review.FakeRespond(0.9), func(d *Deps) {
		d.Reviewers.Fast = gate
		d.Config.Pipeline.Concurrency = 1
	})
	var ctx context.Context
	var stop func()
	if cause != nil {
		c, cancel := context.WithCancelCause(context.Background())
		ctx, stop = c, func() { cancel(cause) }
	} else {
		c, cancel := context.WithCancel(context.Background())
		ctx, stop = c, cancel
	}
	defer stop()
	e.events = nil
	done := runAsync(ctx, p, e.item)
	select {
	case <-gate.started:
	case <-time.After(20 * time.Second):
		t.Fatal("second review never started")
	}
	stop()
	return e, p, gate, wait(t, done)
}

// routedRows splits the routed (non-none) rows by status.
func routedRows(t *testing.T, e *labelEnv) map[model.ReviewStatus][]model.Review {
	t.Helper()
	out := map[model.ReviewStatus][]model.Review{}
	for _, r := range itemRows(t, e) {
		if r.Tier != model.TierNone {
			out[r.Status] = append(out[r.Status], r)
		}
	}
	return out
}

func TestStopDuringReview(t *testing.T) {
	e, _, _, err := stopEnv(t, ErrStopped)
	checkStoppedAt(t, e, err, model.StageReview)
	rows := routedRows(t, e)
	if len(rows[model.ReviewDone]) != 1 || len(rows[model.ReviewStopped]) != 2 || len(rows) != 2 {
		t.Fatalf("routed rows by status %v, want 1 done and 2 stopped (in flight, queued)", statusCounts(rows))
	}
	ctx := context.Background()
	for _, r := range rows[model.ReviewStopped] {
		if r.Attempts != 0 || r.Err != "" || r.Calibration != nil {
			t.Errorf("stopped row %+v", r)
		}
		if _, ok, _ := e.st.Review(ctx, r.GroupID, r.Tier); ok {
			t.Errorf("stopped group %s has a cached review", r.GroupID)
		}
	}
	done := rows[model.ReviewDone][0]
	if _, ok, _ := e.st.Review(ctx, done.GroupID, done.Tier); !ok {
		t.Errorf("finished review %s not cached", done.GroupID)
	}
	if _, ok, _ := e.st.Agg(ctx, e.item.ID); ok {
		t.Error("aggregate stored after a stop")
	}
}

func statusCounts(rows map[model.ReviewStatus][]model.Review) map[model.ReviewStatus]int {
	out := map[model.ReviewStatus]int{}
	for s, rs := range rows {
		out[s] = len(rs)
	}
	return out
}

// Resuming after a stop pays only for what did not finish.
func TestResumeAfterStopReusesCaches(t *testing.T) {
	e, p, gate, err := stopEnv(t, ErrStopped)
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v", err)
	}
	m := p.CurrentModels()
	jc := m.Jev.(*jev.Fake)
	emb := m.Embedder.(*embed.Fake)
	jevBefore, embBefore := len(jc.Calls), emb.Calls
	stoppedRows := len(routedRows(t, e)[model.ReviewStopped])
	reviewsBefore := gate.reviewCount()
	gate.block = nil // the resumed calls answer

	e.run(t, p)
	if n := len(jc.Calls) - jevBefore; n != 0 {
		t.Errorf("resume made %d Jev calls", n)
	}
	if n := emb.Calls - embBefore; n != 0 {
		t.Errorf("resume made %d embed calls", n)
	}
	if n := gate.reviewCount() - reviewsBefore; n != stoppedRows {
		t.Errorf("resume made %d review calls, want %d (the stopped rows)", n, stoppedRows)
	}
	rows := routedRows(t, e)
	if len(rows[model.ReviewDone]) != 3 || len(rows) != 1 {
		t.Errorf("rows after resume %v, want all done", statusCounts(rows))
	}
	for _, s := range []model.Stage{model.StageJev, model.StageEmbed} {
		if rl := e.stage(t, s); rl.Status != model.StageCached {
			t.Errorf("resume %s row %+v, want cached", s, rl)
		}
	}
	e.agg(t)
}

// A cancellation that is not a stop (quit, removal) keeps the old result:
// a failed stage and queued rows, never stopped.
func TestCancelNotStopKeepsBehaviour(t *testing.T) {
	e, _, _, err := stopEnv(t, nil)
	if err == nil || errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v, want a plain cancellation", err)
	}
	for _, rl := range e.events {
		if rl.Status == model.StageStopped {
			t.Errorf("stopped row on a plain cancel: %+v", rl)
		}
	}
	if rl := e.stage(t, model.StageReview); rl.Status != model.StageFailed {
		t.Errorf("review row %+v, want failed", rl)
	}
	rows := routedRows(t, e)
	if len(rows[model.ReviewStopped]) != 0 || len(rows[model.ReviewQueued]) == 0 {
		t.Errorf("rows %v, want queued and none stopped", statusCounts(rows))
	}
}

// A run keeps the configuration and models it started with; the next run
// uses the new ones.
func TestSetConfigDuringRun(t *testing.T) {
	// Reference urgency under the default weights.
	eRef, pRef, _ := m3Env(t, review.FakeRespond(0.9))
	eRef.run(t, pRef)
	want := eRef.agg(t).Urgency

	release := make(chan struct{})
	gate := newGate("fake:fast", func(n int) bool { return n == 1 }, release)
	e, p, _ := m3Env(t, review.FakeRespond(0.9), func(d *Deps) { d.Reviewers.Fast = gate })
	e.events = nil
	done := make(chan error, 1)
	go func() { done <- p.Run(context.Background(), e.item) }()
	<-gate.started

	cfg := p.Config()
	cfg.Rank.WAge += 50
	cfg.Pipeline.Concurrency = 1
	newFast := &review.Fake{Name: "fake:fast-2", Respond: review.FakeRespond(0.9)}
	newJev := &jev.Fake{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.SetConfig(cfg)
			p.SetModels(Models{Jev: newJev, Embedder: &embed.Fake{}, Reviewers: review.Tiers{Fast: newFast, Deep: &review.Fake{Name: "fake:deep"}}})
			_ = p.Config()
			_ = p.CurrentModels()
		}()
	}
	wg.Wait()
	close(release)
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if len(newFast.Calls) != 0 || len(newJev.Calls) != 0 {
		t.Errorf("running run used the new models: fast %d, jev %d calls", len(newFast.Calls), len(newJev.Calls))
	}
	if got := e.agg(t).Urgency; got != want {
		t.Errorf("running run urgency %v, want %v (the weights it started with)", got, want)
	}
	if gate.reviewCount() == 0 {
		t.Error("old reviewer not used")
	}

	e.run(t, p)
	if reviewCalls(newFast) == 0 {
		t.Error("next run did not use the new fast reviewer")
	}
	if got := e.agg(t).Urgency; got == want {
		t.Errorf("next run urgency %v, want the new weights to change it", got)
	}
	if c := p.Config(); c.Pipeline.Concurrency != 1 || c.Rank.WAge != cfg.Rank.WAge {
		t.Errorf("Config() = %+v", c.Pipeline)
	}
}
