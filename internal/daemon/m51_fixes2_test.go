package daemon

import (
	"context"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
)

// A not-analysed PR that discovery moves into Needs you is queued by
// Discover itself, so a TUI with no daemon (--no-daemon: its ticks call only
// Discover) starts it under the needs-you strategy. An uncovered
// not-analysed PR stays as it is.
func TestDiscoverQueuesNewlyCoveredNotAnalysed(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	covered := prItem(1, model.SectionInvolved)
	covered.HeadSHA, covered.State = "s1", model.StateNotAnalysed
	other := prItem(2, model.SectionInvolved)
	other.HeadSHA, other.State = "s2", model.StateNotAnalysed
	put(t, st, covered, other)
	ghc := &gh.Fake{Login: "me", PRs: map[int]gh.PR{
		1: {Number: 1, Title: "now requested", Author: "bob", HeadRef: "feat/1", HeadSHA: "s1", BaseRef: "main", ReviewRequested: true},
		2: {Number: 2, Title: "involved", Author: "bob", HeadRef: "feat/2", HeadSHA: "s2", BaseRef: "main", ViewerCommented: true},
	}}
	g := &gitx.Fake{RefList: []gitx.Ref{{Name: "main", SHA: "b1"}}}
	c := newClient(t, st, g, ghc, withStrategy(model.AutoNeedsYou))
	rec := recordRuns(c)
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	if got := rec.onlyRun(t, covered.ID); got.FreeOnly {
		t.Errorf("newly covered PR ran %+v, want a model-backed run", got)
	}
	if runs := rec.of(other.ID); len(runs) != 0 {
		t.Errorf("uncovered not-analysed PR ran %+v", runs)
	}
}

// A push on a not-analysed branch outside the strategy makes it never-run
// again, so the next automatic pass re-runs its free stages for the new
// head (it never keeps showing the old head's files as current).
func TestPushOnNotAnalysedItemRerunsFreeStages(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	it := branchItem("x", 1)
	it.State = model.StateNotAnalysed
	put(t, st, it)
	g := branchGit("x")
	g.RefList = append([]gitx.Ref{{Name: "main", SHA: "b1"}}, gitx.Ref{Name: "x", SHA: "h-x2"})
	g.Ahead = map[string]int{"main..x": 1}
	c := newClient(t, st, g, nil, withStrategy(model.AutoNeedsYou))
	rec := recordRuns(c)
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, st, it.ID); got.State != "" || got.HeadSHA != "h-x2" {
		t.Fatalf("after push: state %q head %q, want never-run at h-x2", got.State, got.HeadSHA)
	}
	if err := c.AnalysePending(ctx); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	if got := rec.onlyRun(t, it.ID); !got.FreeOnly {
		t.Errorf("run %+v, want free-only", got)
	}
	if got := stored(t, st, it.ID); got.State != model.StateNotAnalysed {
		t.Errorf("state %q, want not analysed", got.State)
	}
}

// Stop all while re-aggregations of analysed items are queued (a weight
// change, then ctrl+k) keeps those items Ready, so later automatic jobs
// still reach them; an unanalysed queued item is stopped at queued.
func TestStopAllKeepsAnalysedItemsReady(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a, b, n := analysedItem(branchItem("a", 3)), analysedItem(branchItem("b", 2)), branchItem("n", 1)
	put(t, st, a, b, n)
	c := newClient(t, st, branchGit("a", "b", "n"), nil, func(d *pipeline.Deps) {
		d.Config.Pipeline.Concurrency = 1
	})
	rec := recordRuns(c)
	rec.setBlock(true)
	c.q.push(job{itemID: a.ID, urgency: 3, rerun: true, aggOnly: true},
		job{itemID: b.ID, urgency: 2, rerun: true, aggOnly: true},
		job{itemID: n.ID, urgency: 1})
	rec.waitStarted(t, a.ID)
	if _, err := c.Stop(ctx, proto.StopParams{All: true}); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	for _, id := range []string{a.ID, b.ID} {
		if got := stored(t, st, id); got.State != model.StateReady {
			t.Errorf("%s state %q (%q), want ready", id, got.State, got.StateNote)
		}
	}
	if got := stored(t, st, n.ID); got.State != model.StateStopped || got.StateNote != stopQueued {
		t.Errorf("unanalysed item state %q (%q), want stopped at queued", got.State, got.StateNote)
	}
}

// A stop between stages (a StageStopped row with no Running row first)
// names the stage the run log marks stopped.
func TestStopBetweenStagesNamesStoppedStage(t *testing.T) {
	st := newTestStore(t)
	put(t, st, branchItem("x", 1))
	c := newClient(t, st, branchGit("x"), nil, nil)
	id := model.BranchItemID("x")
	e := &runEntry{cancel: func(error) {}}
	c.runMu.Lock()
	c.runs[id] = e
	c.runMu.Unlock()
	c.d.Emit(model.RunLog{ItemID: id, Stage: model.StageEmbed, Status: model.StageRunning})
	c.d.Emit(model.RunLog{ItemID: id, Stage: model.StageEmbed, Status: model.StageDone})
	c.d.Emit(model.RunLog{ItemID: id, Stage: model.StageJev, Status: model.StageStopped})
	if got := c.runStage(e); got != model.StageJev {
		t.Errorf("stage = %q, want %q", got, model.StageJev)
	}
}

// A save that changes only auto_review (`:auto manual`) succeeds on a
// configuration with values config.Load accepts but the page's rules do
// not (concurrency = 0 means the default); changing such a key to a bad
// value is still refused.
func TestSaveSettingsValidatesChangedKeysOnly(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, nil, func(d *pipeline.Deps) {
		writeFile(t, d.ConfigDirs[0], config.MainFile, "[pipeline]\nconcurrency = 0\n")
		cfg, err := config.Load(d.ConfigDirs)
		if err != nil {
			t.Fatal(err)
		}
		d.Config = cfg
	})
	v := settingsValues(e.c)
	if v.Concurrency != 0 {
		t.Fatalf("concurrency %d, want 0", v.Concurrency)
	}
	v.AutoReview = string(model.AutoManual)
	if _, err := e.c.SaveSettings(ctx, proto.SaveSettingsParams{Values: v}); err != nil {
		t.Fatalf(":auto manual: %v", err)
	}
	if got := e.c.cfg().Pipeline.Strategy(); got != model.AutoManual {
		t.Errorf("strategy %q", got)
	}
	v = settingsValues(e.c)
	v.MaxGroupUnits = 0
	if _, err := e.c.SaveSettings(ctx, proto.SaveSettingsParams{Values: v}); err == nil {
		t.Error("a changed bad value was accepted")
	}
}
