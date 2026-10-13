package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/store"
)

// runRec replaces a client's pipeline run (InProcess.run) and records the
// options each job ran with. With block set, a run reports the review stage
// and waits until its context ends (a user stop or an interruption).
type runRec struct {
	emit func(model.RunLog)

	mu      sync.Mutex
	runs    map[string][]pipeline.RunOptions
	block   bool
	started chan string
}

func recordRuns(c *InProcess) *runRec {
	r := &runRec{emit: c.d.Emit, runs: map[string][]pipeline.RunOptions{}, started: make(chan string, 64)}
	c.run = r.run
	return r
}

func (r *runRec) run(ctx context.Context, it model.Item, opt pipeline.RunOptions) (string, error) {
	r.mu.Lock()
	r.runs[it.ID] = append(r.runs[it.ID], opt)
	block := r.block
	r.mu.Unlock()
	r.emit(model.RunLog{ItemID: it.ID, Stage: model.StageFetch, Status: model.StageRunning})
	r.started <- it.ID
	if !block {
		return it.HeadSHA, nil
	}
	r.emit(model.RunLog{ItemID: it.ID, Stage: model.StageReview, Status: model.StageRunning})
	<-ctx.Done()
	return "", fmt.Errorf("review: %w", context.Cause(ctx))
}

func (r *runRec) of(id string) []pipeline.RunOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pipeline.RunOptions(nil), r.runs[id]...)
}

func (r *runRec) setBlock(b bool) {
	r.mu.Lock()
	r.block = b
	r.mu.Unlock()
}

// waitStarted waits until id's run started.
func (r *runRec) waitStarted(t *testing.T, ids ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	timeout := time.After(5 * time.Second)
	for len(want) > 0 {
		select {
		case id := <-r.started:
			delete(want, id)
		case <-timeout:
			t.Fatalf("runs not started: %v", want)
		}
	}
}

// onlyRun returns id's only run's options, failing when it ran ≠ 1 times.
func (r *runRec) onlyRun(t *testing.T, id string) pipeline.RunOptions {
	t.Helper()
	runs := r.of(id)
	if len(runs) != 1 {
		t.Fatalf("%s ran %d times (%+v), want once", id, len(runs), runs)
	}
	return runs[0]
}

// withStrategy sets the auto_review strategy.
func withStrategy(a model.AutoReview) func(*pipeline.Deps) {
	return func(d *pipeline.Deps) { d.Config.Pipeline.AutoReview = string(a) }
}

func stored(t *testing.T, st store.Store, id string) model.Item {
	t.Helper()
	it, err := st.Item(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func prItem(n int, sec model.Section) model.Item {
	return model.Item{ID: model.PRItemID(n), Kind: model.KindPR, Number: n, HeadSHA: fmt.Sprintf("p%d", n),
		Source: model.SourceRanked, Section: sec}
}

func analysedItem(it model.Item) model.Item {
	it.AnalysedSHA, it.State = it.HeadSHA, model.StateReady
	return it
}

// The strategy is applied when a job starts: covered or requested items
// run as queued; others run free-only (aggregate-only when their head is
// analysed) and end not analysed; automatic jobs of stopped items do not
// run (DECISIONS "M5.1 user override").
func TestGateDecidesWhenJobStarts(t *testing.T) {
	free := pipeline.RunOptions{FreeOnly: true}
	aggFree := pipeline.RunOptions{AggregateOnly: true, FreeOnly: true}
	branch := branchItem("x", 1)
	requested := branch
	requested.RequestedSHA = branch.HeadSHA
	oldRequest := branch
	oldRequest.RequestedSHA = "h-old"
	stale := branch
	stale.AnalysedSHA, stale.State = "h-old", model.StateStale
	stopped := branch
	stopped.State, stopped.StateNote = model.StateStopped, "jev"
	preview := prItem(5, model.SectionNeedsYou)
	preview.Source = model.SourcePreview

	cases := []struct {
		name      string
		auto      model.AutoReview
		item      model.Item
		j         job
		wantRun   bool
		want      pipeline.RunOptions
		wantState model.ItemState
		wantReq   string
	}{
		{"needs-you PR under needs-you", model.AutoNeedsYou, prItem(1, model.SectionNeedsYou), job{}, true, pipeline.RunOptions{}, model.StateReady, ""},
		{"yours PR under needs-you", model.AutoNeedsYou, prItem(2, model.SectionYours), job{}, true, free, model.StateNotAnalysed, ""},
		{"yours PR under needs-you+yours", model.AutoNeedsYouYours, prItem(2, model.SectionYours), job{}, true, pipeline.RunOptions{}, model.StateReady, ""},
		{"involved PR under needs-you+yours", model.AutoNeedsYouYours, prItem(3, model.SectionInvolved), job{}, true, free, model.StateNotAnalysed, ""},
		{"branch under needs-you+yours", model.AutoNeedsYouYours, branch, job{force: true}, true, pipeline.RunOptions{Force: true, FreeOnly: true}, model.StateNotAnalysed, ""},
		{"branch under all", model.AutoAll, branch, job{}, true, pipeline.RunOptions{}, model.StateReady, ""},
		{"needs-you PR under manual", model.AutoManual, prItem(1, model.SectionNeedsYou), job{}, true, free, model.StateNotAnalysed, ""},
		{"explicit branch under manual", model.AutoManual, branch, job{explicit: true}, true, pipeline.RunOptions{}, model.StateReady, branch.HeadSHA},
		{"requested head under manual", model.AutoManual, requested, job{}, true, pipeline.RunOptions{}, model.StateReady, branch.HeadSHA},
		{"old request, new head", model.AutoManual, oldRequest, job{}, true, free, model.StateNotAnalysed, "h-old"},
		{"analysed branch, forced from a stage", model.AutoNeedsYou, analysedItem(branch), job{force: true, fromStage: model.StageReview}, true, aggFree, model.StateReady, ""},
		{"analysed branch, aggregate-only", model.AutoNeedsYou, analysedItem(branch), job{aggOnly: true, rerun: true}, true, aggFree, model.StateReady, ""},
		{"stale branch keeps its old analysis", model.AutoNeedsYou, stale, job{}, true, aggFree, model.StateStale, ""},
		{"preview of a needs-you PR", model.AutoNeedsYou, preview, job{user: true}, true, free, model.StateNotAnalysed, ""},
		{"preview under all", model.AutoAll, preview, job{user: true}, true, pipeline.RunOptions{}, model.StateReady, ""},
		{"stopped item, automatic job", model.AutoAll, stopped, job{}, false, pipeline.RunOptions{}, model.StateStopped, ""},
		{"stopped item, explicit job", model.AutoManual, stopped, job{explicit: true}, true, pipeline.RunOptions{}, model.StateReady, branch.HeadSHA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			c := newClient(t, st, branchGit("x"), nil, withStrategy(tc.auto))
			put(t, st, tc.item) // after start-up, which deletes leftover previews
			rec := recordRuns(c)
			j := tc.j
			j.itemID = tc.item.ID
			c.q.push(j)
			c.wg.Wait()
			runs := rec.of(tc.item.ID)
			if !tc.wantRun {
				if len(runs) != 0 {
					t.Fatalf("ran %+v, want no run", runs)
				}
			} else if len(runs) != 1 || runs[0] != tc.want {
				t.Fatalf("runs = %+v, want one with %+v", runs, tc.want)
			}
			it := stored(t, st, tc.item.ID)
			if it.State != tc.wantState || it.RequestedSHA != tc.wantReq {
				t.Errorf("item state %q requested %q, want %q / %q", it.State, it.RequestedSHA, tc.wantState, tc.wantReq)
			}
			if tc.wantState == model.StateNotAnalysed && (it.AnalysedSHA != "" || it.StateNote != "") {
				t.Errorf("not-analysed item has analysed %q note %q", it.AnalysedSHA, it.StateNote)
			}
		})
	}
}

// A strategy change applies to jobs already queued: the decision is taken
// when the job starts.
func TestGateUsesStrategyAtStart(t *testing.T) {
	st := newTestStore(t)
	put(t, st, branchItem("busy", 9), branchItem("x", 1))
	c := newClient(t, st, branchGit("busy", "x"), nil, func(d *pipeline.Deps) {
		d.Config.Pipeline.AutoReview = string(model.AutoAll)
		d.Config.Pipeline.Concurrency = 1
	})
	rec := recordRuns(c)
	rec.setBlock(true)
	c.q.push(job{itemID: model.BranchItemID("busy"), explicit: true})
	rec.waitStarted(t, model.BranchItemID("busy"))
	c.q.push(job{itemID: model.BranchItemID("x")}) // queued under "all"
	cfg := c.cfg()
	cfg.Pipeline.AutoReview = string(model.AutoManual)
	c.p.SetConfig(cfg)
	rec.setBlock(false)
	c.cancelRun(model.BranchItemID("busy"))
	c.wg.Wait()
	if got := rec.onlyRun(t, model.BranchItemID("x")); !got.FreeOnly {
		t.Errorf("queued job ran %+v after :auto manual, want FreeOnly", got)
	}
}

// Every daemon call site is classified as explicit (the user acted on that
// item) or automatic (gated). The item is a branch under needs-you, so an
// automatic job runs FreeOnly.
func TestCallSiteClassification(t *testing.T) {
	ctx := context.Background()
	id := model.BranchItemID("x")
	cases := []struct {
		name     string
		item     model.Item
		call     func(c *InProcess) error
		explicit bool
	}{
		{"refresh first load", branchItem("x", 1), func(c *InProcess) error {
			return c.Refresh(ctx, proto.ItemParams{ItemID: id})
		}, false},
		{"refresh forced, not by hand", branchItem("x", 1), func(c *InProcess) error {
			return c.Refresh(ctx, proto.ItemParams{ItemID: id, Force: true})
		}, false},
		{"refresh from stage, not by hand", branchItem("x", 1), func(c *InProcess) error {
			return c.Refresh(ctx, proto.ItemParams{ItemID: id, FromStage: model.StageJev})
		}, false},
		{"refresh by hand", branchItem("x", 1), func(c *InProcess) error {
			return c.Refresh(ctx, proto.ItemParams{ItemID: id, User: true})
		}, true},
		{"refresh by hand, forced from a stage", branchItem("x", 1), func(c *InProcess) error {
			return c.Refresh(ctx, proto.ItemParams{ItemID: id, User: true, Force: true, FromStage: model.StageReview})
		}, true},
		{"add", branchItem("x", 1), func(c *InProcess) error {
			_, err := c.Add(ctx, "x")
			return err
		}, true},
		{"tab preview", model.Item{}, func(c *InProcess) error {
			_, err := c.Preview(ctx, "x")
			return err
		}, false},
		{"group edit", branchItem("x", 1), func(c *InProcess) error {
			return c.EditGroups(ctx, proto.GroupEditParams{ItemID: id, Edit: model.GroupEdit{Op: model.EditReset}})
		}, true},
		{"review action", branchItem("x", 1), func(c *InProcess) error {
			return c.ReviewAction(ctx, proto.ReviewActionParams{ItemID: id, GroupID: "g1", Action: proto.ActionSkip})
		}, true},
		{"cost cap", branchItem("x", 1), func(c *InProcess) error {
			return c.SetCap(ctx, proto.CapParams{ItemID: id, CapUSD: 2})
		}, true},
		{"override of the item", analysedItem(branchItem("x", 1)), func(c *InProcess) error {
			return c.Override(ctx, proto.OverrideParams{ItemID: id, Override: model.Override{UnitID: "u1", Question: "risk", Value: "2"}})
		}, true},
		{"analyse pending", branchItem("x", 1), func(c *InProcess) error {
			return c.AnalysePending(ctx)
		}, false},
		{"analyse (direct call)", branchItem("x", 1), func(c *InProcess) error {
			return c.Analyse(ctx, false, id)
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			if tc.item.ID != "" {
				put(t, st, tc.item)
			}
			c := newClient(t, st, branchGit("x"), nil, withStrategy(model.AutoNeedsYou))
			rec := recordRuns(c)
			if err := tc.call(c); err != nil {
				t.Fatal(err)
			}
			c.wg.Wait()
			runs := rec.of(id)
			if len(runs) == 0 {
				t.Fatal("no run")
			}
			if got := runs[len(runs)-1]; got.FreeOnly == tc.explicit {
				t.Errorf("run options %+v: explicit = %v, want %v", got, !got.FreeOnly, tc.explicit)
			}
			if req := stored(t, st, id).RequestedSHA; (req == "h-x") != tc.explicit {
				t.Errorf("RequestedSHA = %q, explicit %v", req, tc.explicit)
			}
		})
	}
}

// An override follows its unit to the other items holding it; those
// follow-ups are automatic, so an item the strategy does not allow is only
// re-aggregated free-only, while the overridden item runs as asked.
func TestOverrideFollowUpIsAutomatic(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a, b := analysedItem(branchItem("a", 1)), analysedItem(branchItem("b", 1))
	put(t, st, a, b)
	for _, it := range []model.Item{a, b} {
		if err := st.PutUnits(ctx, it.ID, []model.ChangeUnit{{ID: "shared", ItemID: it.ID, Path: "x.go"}}); err != nil {
			t.Fatal(err)
		}
	}
	c := newClient(t, st, branchGit("a", "b"), nil, withStrategy(model.AutoNeedsYou))
	rec := recordRuns(c)
	if err := c.Override(ctx, proto.OverrideParams{ItemID: a.ID, Override: model.Override{UnitID: "shared", Question: "risk", Value: "2"}}); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	if got := rec.onlyRun(t, a.ID); got != (pipeline.RunOptions{AggregateOnly: true}) {
		t.Errorf("overridden item ran %+v", got)
	}
	if got := rec.onlyRun(t, b.ID); got != (pipeline.RunOptions{AggregateOnly: true, FreeOnly: true}) {
		t.Errorf("follow-up ran %+v, want aggregate-only free-only", got)
	}
}

// An explicit run interrupted by a restart resumes on the next start (its
// head is still requested); a new head needs a new request.
func TestRequestedSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	inFlight := branchItem("x", 1)
	inFlight.State, inFlight.StateNote, inFlight.RequestedSHA = model.StateAnalysing, "jev", inFlight.HeadSHA
	moved := branchItem("y", 1)
	moved.RequestedSHA = "h-old"
	put(t, st, inFlight, moved)
	c := newClient(t, st, branchGit("x", "y"), nil, withStrategy(model.AutoManual))
	rec := recordRuns(c)
	if it := stored(t, st, inFlight.ID); it.State != "" {
		t.Fatalf("after start: state %q, want reset", it.State)
	}
	if err := c.AnalysePending(ctx); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	if got := rec.onlyRun(t, inFlight.ID); got.FreeOnly {
		t.Errorf("interrupted explicit run resumed free-only: %+v", got)
	}
	if got := rec.onlyRun(t, moved.ID); !got.FreeOnly {
		t.Errorf("item pushed since its request ran %+v, want FreeOnly", got)
	}
	if it := stored(t, st, moved.ID); it.State != model.StateNotAnalysed {
		t.Errorf("moved item state %q", it.State)
	}
}

// AnalysePending takes never-run items and not-analysed items the strategy
// now allows; never stopped, stale, failed or ready ones.
func TestAnalysePendingPicks(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mk := func(name string, state model.ItemState, sec model.Section) model.Item {
		it := branchItem(name, 1)
		it.State = state
		if sec != "" {
			it.Kind, it.Section = model.KindPR, sec
		}
		return it
	}
	items := []model.Item{
		mk("new", "", ""),
		mk("na-covered", model.StateNotAnalysed, model.SectionNeedsYou),
		mk("na-branch", model.StateNotAnalysed, ""),
		mk("stopped", model.StateStopped, model.SectionNeedsYou),
		mk("stale", model.StateStale, model.SectionNeedsYou),
		mk("error", model.StateError, model.SectionNeedsYou),
		mk("ready", model.StateReady, model.SectionNeedsYou),
	}
	put(t, st, items...)
	c := newClient(t, st, branchGit(), nil, withStrategy(model.AutoNeedsYou))
	rec := recordRuns(c)
	if err := c.AnalysePending(ctx); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	for _, it := range items {
		ran := len(rec.of(it.ID)) > 0
		want := it.ID == model.BranchItemID("new") || it.ID == model.BranchItemID("na-covered")
		if ran != want {
			t.Errorf("%s ran = %v, want %v", it.ID, ran, want)
		}
	}
}

// Creating a PR from a branch the user analysed carries the request over;
// from an unanalysed branch the PR's first run is automatic.
func TestAnalyseCreatedPRClassification(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		branch   model.Item
		explicit bool
	}{
		{"analysed branch", analysedItem(branchItem("x", 1)), true},
		{"requested branch", func() model.Item { b := branchItem("x", 1); b.RequestedSHA = b.HeadSHA; return b }(), true},
		{"untouched branch", branchItem("x", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			pr := prItem(9, model.SectionYours)
			put(t, st, pr)
			c := newClient(t, st, branchGit("x"), nil, withStrategy(model.AutoNeedsYou))
			rec := recordRuns(c)
			if err := c.analyseCreatedPR(ctx, tc.branch, pr.ID); err != nil {
				t.Fatal(err)
			}
			c.wg.Wait()
			if got := rec.onlyRun(t, pr.ID); got.FreeOnly == tc.explicit {
				t.Errorf("PR run %+v, explicit %v", got, tc.explicit)
			}
		})
	}
}

// tripwire is a Jev client, embedder and reviewer that fails the test when
// called: no automatic path may spend on an item outside the strategy.
// With guard set, only calls made while a guarded item runs count (the
// client must then run one job at a time).
type tripwire struct {
	t     *testing.T
	calls atomic.Int64
	guard map[string]bool
	cur   atomic.Value // string: the item whose run emitted the last row
}

func (w *tripwire) hit(what string) error {
	if w.guard != nil {
		cur, _ := w.cur.Load().(string)
		if !w.guard[cur] {
			return errors.New("tripwire: unguarded")
		}
	}
	w.calls.Add(1)
	w.t.Errorf("model call (%s) on an automatic path outside the strategy", what)
	return errors.New("tripwire")
}

func (w *tripwire) Evaluate(context.Context, jev.Request) (jev.Response, error) {
	return jev.Response{}, w.hit("jev")
}

func (w *tripwire) Embed(context.Context, []string) ([][]float32, error) {
	return nil, w.hit("embed")
}

func (w *tripwire) Review(context.Context, review.Request) (review.Result, error) {
	return review.Result{}, w.hit("review")
}

func (w *tripwire) Model() string { return "tripwire:none" }

var (
	_ jev.Client      = (*tripwire)(nil)
	_ embed.Embedder  = (*tripwire)(nil)
	_ review.Reviewer = (*tripwire)(nil)
)

// tripDeps wires w as every model client under strategy a.
func tripDeps(w *tripwire, a model.AutoReview, more ...func(*pipeline.Deps)) func(*pipeline.Deps) {
	return func(d *pipeline.Deps) {
		d.Jev, d.Embedder, d.Reviewers = w, w, review.Tiers{Fast: w, Deep: w}
		d.Config.Pipeline.AutoReview = string(a)
		user := d.Emit
		d.Emit = func(rl model.RunLog) {
			w.cur.Store(rl.ItemID)
			if user != nil {
				user(rl)
			}
		}
		for _, f := range more {
			f(d)
		}
	}
}

// analysedStore returns a store whose branches were analysed (no model
// clients) by a client that is closed again.
func analysedStore(t *testing.T, dirs []string, names ...string) store.Store {
	t.Helper()
	st := newTestStore(t)
	for _, n := range names {
		put(t, st, branchItem(n, 1))
	}
	c := newClient(t, st, branchGit(names...), nil, func(d *pipeline.Deps) { d.ConfigDirs = dirs })
	for _, n := range names {
		if err := c.RunSync(context.Background(), model.BranchItemID(n)); err != nil {
			t.Fatal(err)
		}
	}
	_ = c.Close()
	return st
}

// waitState polls the stored item until it reaches want (5 s).
func waitState(t *testing.T, st store.Store, id string, want model.ItemState) model.Item {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		it, err := st.Item(context.Background(), id)
		if err == nil && it.State == want {
			return it
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: state %q, want %q", id, it.State, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// End to end (real pipeline, needs pipeline.RunOptions.FreeOnly): every
// automatic path leaves an item outside the strategy without a model call.
func TestAutomaticPathsNeverSpend(t *testing.T) {
	ctx := context.Background()
	x := model.BranchItemID("x")

	t.Run("daemon start", func(t *testing.T) {
		st := newTestStore(t)
		put(t, st, branchItem("x", 1))
		w := &tripwire{t: t}
		c := newClient(t, st, branchGit("x"), nil, tripDeps(w, model.AutoNeedsYou))
		if err := c.AnalysePending(ctx); err != nil {
			t.Fatal(err)
		}
		c.wg.Wait()
		it := waitState(t, st, x, model.StateNotAnalysed)
		if it.AnalysedSHA != "" {
			t.Errorf("free-only run set AnalysedSHA %q", it.AnalysedSHA)
		}
	})

	t.Run("discovery tick", func(t *testing.T) {
		st := newTestStore(t)
		g := branchGit("x")
		g.Ahead = map[string]int{"main..x": 1}
		w := &tripwire{t: t}
		c := newClient(t, st, g, nil, tripDeps(w, model.AutoNeedsYou))
		if err := c.Discover(ctx); err != nil {
			t.Fatal(err)
		}
		if err := c.AnalysePending(ctx); err != nil {
			t.Fatal(err)
		}
		c.wg.Wait()
		waitState(t, st, x, model.StateNotAnalysed)
	})

	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("first-load refresh force=%v", force), func(t *testing.T) {
			st := newTestStore(t)
			put(t, st, branchItem("x", 1))
			w := &tripwire{t: t}
			c := newClient(t, st, branchGit("x"), nil, tripDeps(w, model.AutoNeedsYou))
			if err := c.Refresh(ctx, proto.ItemParams{ItemID: x, Force: force}); err != nil {
				t.Fatal(err)
			}
			c.wg.Wait()
			waitState(t, st, x, model.StateNotAnalysed)
		})
	}

	t.Run("involved PR refresh", func(t *testing.T) {
		st := newTestStore(t)
		pr := prItem(10, model.SectionInvolved)
		pr.Ref, pr.BaseRef = "feat/10", "main"
		put(t, st, pr)
		ghc := discoverGH()
		ghc.Diffs = map[int]string{10: oneLineDiff("ten.go")}
		w := &tripwire{t: t}
		c := newClient(t, st, branchGit(), ghc, tripDeps(w, model.AutoNeedsYouYours))
		if err := c.Refresh(ctx, proto.ItemParams{ItemID: pr.ID}); err != nil {
			t.Fatal(err)
		}
		c.wg.Wait()
		waitState(t, st, pr.ID, model.StateNotAnalysed)
	})

	t.Run("tab preview", func(t *testing.T) {
		st := newTestStore(t)
		w := &tripwire{t: t}
		c := newClient(t, st, branchGit("x"), nil, tripDeps(w, model.AutoNeedsYou))
		if _, err := c.Preview(ctx, "x"); err != nil {
			t.Fatal(err)
		}
		c.wg.Wait()
		waitState(t, st, x, model.StateNotAnalysed)
	})

	t.Run("stale on push", func(t *testing.T) {
		st := analysedStore(t, nil, "x")
		g := branchGit("x")
		g.RefList[0].SHA = "h-x2"
		g.Ahead = map[string]int{"main..x": 1}
		w := &tripwire{t: t}
		c := newClient(t, st, g, nil, tripDeps(w, model.AutoNeedsYou))
		if err := c.Discover(ctx); err != nil {
			t.Fatal(err)
		}
		if err := c.AnalysePending(ctx); err != nil {
			t.Fatal(err)
		}
		c.wg.Wait()
		if it := stored(t, st, x); it.State != model.StateStale || it.AnalysedSHA != "h-x" {
			t.Errorf("after push: state %q analysed %q, want stale at h-x", it.State, it.AnalysedSHA)
		}
	})

	t.Run("routing reload", func(t *testing.T) {
		dirs := []string{t.TempDir()}
		st := analysedStore(t, dirs, "x") // no reviewers: the row stays unreviewed
		w := &tripwire{t: t}
		c := newClient(t, st, branchGit("x"), nil, tripDeps(w, model.AutoNeedsYou, func(d *pipeline.Deps) { d.ConfigDirs = dirs }))
		writeFile(t, dirs[0], "prt.routing.toml", "[[rule]]\nwhen = \"true\"\ntier = \"fast\"\n")
		info, err := c.Routing(ctx, proto.RoutingParams{Reload: true})
		if err != nil || info.Err != "" || info.Rerouted == 0 {
			t.Fatalf("reload = %+v, %v; want a re-route", info, err)
		}
		c.wg.Wait()
		waitState(t, st, x, model.StateReady)
	})

	t.Run("override follow-up on another item", func(t *testing.T) {
		st := analysedStore(t, nil, "a", "b")
		for _, id := range []string{model.BranchItemID("a"), model.BranchItemID("b")} {
			units, err := st.Units(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			units = append(units, model.ChangeUnit{ID: "shared", ItemID: id, Path: "shared.go", Order: len(units)})
			if err := st.PutUnits(ctx, id, units); err != nil {
				t.Fatal(err)
			}
		}
		w := &tripwire{t: t, guard: map[string]bool{model.BranchItemID("b"): true}}
		c := newClient(t, st, branchGit("a", "b"), nil, tripDeps(w, model.AutoNeedsYou, func(d *pipeline.Deps) {
			d.Config.Pipeline.Concurrency = 1
		}))
		if err := c.Override(ctx, proto.OverrideParams{ItemID: model.BranchItemID("a"), Override: model.Override{UnitID: "shared", Question: "risk", Value: "2"}}); err != nil {
			t.Fatal(err)
		}
		c.wg.Wait()
		waitState(t, st, model.BranchItemID("b"), model.StateReady)
	})

	t.Run("calibration sample 1.0", func(t *testing.T) {
		st := newTestStore(t)
		put(t, st, branchItem("x", 1))
		w := &tripwire{t: t}
		c := newClient(t, st, branchGit("x"), nil, tripDeps(w, model.AutoNeedsYou, func(d *pipeline.Deps) {
			d.Routing.CalibrationSample = 1.0
		}))
		if err := c.AnalysePending(ctx); err != nil {
			t.Fatal(err)
		}
		c.wg.Wait()
		waitState(t, st, x, model.StateNotAnalysed)
	})
}

// countingModels are model fakes that answer (and count) every call.
type countingModels struct {
	jev  *jev.Fake
	emb  *embed.Fake
	fast *review.Fake
	deep *review.Fake
}

func newCountingModels() *countingModels {
	answer := review.FakeRespond(0.9)
	return &countingModels{
		jev: &jev.Fake{}, emb: &embed.Fake{},
		fast: &review.Fake{Name: "fake:fast", Respond: answer},
		deep: &review.Fake{Name: "fake:deep", Respond: answer},
	}
}

func (m *countingModels) deps(a model.AutoReview) func(*pipeline.Deps) {
	return func(d *pipeline.Deps) {
		d.Jev, d.Embedder, d.Reviewers = m.jev, m.emb, review.Tiers{Fast: m.fast, Deep: m.deep}
		d.Config.Pipeline.AutoReview = string(a)
	}
}

// jevCalls counts Jev requests; call it only after the runs finished
// (c.wg.Wait orders it after the fake's writes).
func (m *countingModels) jevCalls() int {
	return len(m.jev.Calls)
}

// writeFile writes dir/name, creating dir.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A hand-made refresh runs the model stages even under manual, and records
// the requested head.
func TestRefreshByHandRunsModelsUnderManual(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, branchItem("x", 1))
	m := newCountingModels()
	c := newClient(t, st, branchGit("x"), nil, m.deps(model.AutoManual))
	id := model.BranchItemID("x")
	if err := c.Refresh(ctx, proto.ItemParams{ItemID: id, User: true}); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	it := waitState(t, st, id, model.StateReady)
	if it.RequestedSHA != "h-x" || it.AnalysedSHA != "h-x" {
		t.Errorf("item requested %q analysed %q, want h-x", it.RequestedSHA, it.AnalysedSHA)
	}
	if m.jevCalls() == 0 {
		t.Error("no Jev call on an explicit run")
	}
}
