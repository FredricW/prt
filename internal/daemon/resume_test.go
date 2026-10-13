package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/review"
)

// optsLog wraps a client's pipeline run and records each run's options.
type optsLog struct {
	mu   sync.Mutex
	opts []pipeline.RunOptions
}

func logOpts(c *InProcess) *optsLog {
	l := &optsLog{}
	run := c.run
	c.run = func(ctx context.Context, it model.Item, opt pipeline.RunOptions) (string, error) {
		l.mu.Lock()
		l.opts = append(l.opts, opt)
		l.mu.Unlock()
		return run(ctx, it, opt)
	}
	return l
}

func (l *optsLog) last(t *testing.T) pipeline.RunOptions {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.opts) == 0 {
		t.Fatal("no run")
	}
	return l.opts[len(l.opts)-1]
}

// waitRowDone polls the item's routing table until group's row is done.
func waitRowDone(t *testing.T, c *InProcess, id, group string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, err := c.d.Store.ItemReviews(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.GroupID == group && r.Status == model.ReviewDone {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("row %s never done: %+v", group, rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A Pipeline-page `r` on review, stopped mid-review and resumed, keeps its
// FromStage (M5.2): the group it had not re-reviewed yet is reviewed again
// (its old cached review bypassed), and the group it had re-reviewed is
// served from the review it paid for before the stop.
func TestResumeKeepsFromStage(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, branchItem("x", 1))
	g := branchGit("x")
	g.Diffs["b1..h-x"] = oneLineDiff("p.txt") + oneLineDiff("q.txt")
	rv := newGatedReviewer()
	c := newClient(t, st, g, nil, func(d *pipeline.Deps) { d.Reviewers = review.Tiers{Fast: rv, Deep: rv} })
	id := model.BranchItemID("x")
	if err := c.RunSync(ctx, id); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ItemReviews(ctx, id)
	if err != nil || len(rows) != 2 {
		t.Fatalf("routing rows = %+v, %v; want two groups", rows, err)
	}
	runs := logOpts(c)

	release := rv.arm(1) // one group's review answers, the other blocks
	defer release()
	if err := c.Refresh(ctx, proto.ItemParams{ItemID: id, FromStage: model.StageReview, User: true}); err != nil {
		t.Fatal(err)
	}
	blocked := rv.waitBlocked(t)
	done := rows[0].GroupID
	if done == blocked {
		done = rows[1].GroupID
	}
	waitRowDone(t, c, id, done)
	if _, err := c.Stop(ctx, proto.StopParams{ItemID: id}); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	waitState(t, st, id, model.StateStopped)
	rec, ok := c.loadStop(ctx, id)
	if want := (stopRecord{FromStage: model.StageReview, RunFrom: model.StageReview, RunStage: model.StageReview}); !ok || rec != want {
		t.Fatalf("stop record = %+v (%v), want %+v", rec, ok, want)
	}

	before := rv.callCounts()
	release()
	if _, err := c.Resume(ctx, proto.ResumeParams{ItemID: id}); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	waitState(t, st, id, model.StateReady)
	after := rv.callCounts()
	if n := after[blocked] - before[blocked]; n != 1 {
		t.Errorf("group not re-reviewed before the stop: %d review calls on resume, want 1 (its old review bypassed)", n)
	}
	if n := after[done] - before[done]; n != 0 {
		t.Errorf("group re-reviewed before the stop: %d review calls on resume, want 0 (served from its new review)", n)
	}
	if got := runs.last(t).FromStage; got != model.StageAggregate {
		t.Errorf("resumed run from %q, want aggregate (review continued from the caches the stopped run refreshed)", got)
	}
	final, err := st.ItemReviews(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range final {
		if r.Status != model.ReviewDone {
			t.Errorf("row %s %s after resume", r.GroupID, r.Status)
		}
	}
	if _, ok := c.loadStop(ctx, id); ok {
		t.Error("stop record kept after resume")
	}
}

// Stopping a queued job remembers its options; Resume queues them.
func TestResumeKeepsQueuedOptions(t *testing.T) {
	ctx := context.Background()
	c, rec := stopClient(t, 1, "a", "b")
	a, b := model.BranchItemID("a"), model.BranchItemID("b")
	if err := c.Analyse(ctx, false, a); err != nil {
		t.Fatal(err)
	}
	rec.waitStarted(t, a)
	c.q.push(job{itemID: b, force: true, fromStage: model.StageParse, explicit: true})
	if res, err := c.Stop(ctx, proto.StopParams{ItemID: b}); err != nil || len(res.Stopped) != 1 {
		t.Fatalf("stop b = %+v, %v", res, err)
	}
	if _, err := c.Stop(ctx, proto.StopParams{ItemID: a}); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	rec.setBlock(false)
	if _, err := c.Resume(ctx, proto.ResumeParams{ItemID: b}); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	got := rec.onlyRun(t, b)
	if got.FromStage != model.StageParse || !got.Force {
		t.Errorf("resumed b with %+v, want FromStage parse and Force", got)
	}
	// a's run had neither: nothing to remember, a plain resume.
	if _, err := c.Resume(ctx, proto.ResumeParams{ItemID: a}); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	if runs := rec.of(a); len(runs) != 2 || runs[1] != (pipeline.RunOptions{}) {
		t.Errorf("a's runs = %+v, want a plain resumed run", runs)
	}
}

// A full run that completes forgets a stop record left by an earlier stop.
func TestCompletedRunForgetsStopRecord(t *testing.T) {
	ctx := context.Background()
	c, rec := stopClient(t, 1, "a")
	a := model.BranchItemID("a")
	c.rememberStoppedRun(a, pipeline.RunOptions{FromStage: model.StageCluster}, model.StageRoute)
	if _, ok := c.loadStop(ctx, a); !ok {
		t.Fatal("no stop record")
	}
	rec.setBlock(false)
	if err := c.Refresh(ctx, proto.ItemParams{ItemID: a, User: true}); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	if _, ok := c.loadStop(ctx, a); ok {
		t.Error("stop record kept after a completed run")
	}
}

func TestResumeOptions(t *testing.T) {
	ctx := context.Background()
	rows := []model.Review{
		{GroupID: "done", ItemID: "branch:a", Tier: model.TierFast, Status: model.ReviewDone},
		{GroupID: "stopped", ItemID: "branch:a", Tier: model.TierDeep, Status: model.ReviewStopped},
		{GroupID: "overcap", ItemID: "branch:a", Tier: model.TierDeep, Status: model.ReviewQueued},
		{GroupID: "none", ItemID: "branch:a", Tier: model.TierNone, Status: model.ReviewSkipped},
	}
	fromReview := func(stoppedIn model.Stage) stopRecord {
		return stopRecord{FromStage: model.StageReview, RunFrom: model.StageReview, RunStage: stoppedIn}
	}
	for _, tc := range []struct {
		name    string
		rec     stopRecord
		from    model.Stage
		want    model.Stage
		dropped bool
	}{
		{"no FromStage", fromReview(model.StageReview), "", "", false},
		{"merged with an earlier request", fromReview(model.StageReview), model.StageParse, model.StageParse, false},
		{"stopped before its FromStage", stopRecord{RunFrom: model.StageReview, RunStage: model.StageCluster}, model.StageReview, model.StageReview, false},
		{"stopped in review", fromReview(model.StageReview), model.StageReview, model.StageAggregate, true},
		{"stopped in a later stage", stopRecord{RunFrom: model.StageCluster, RunStage: model.StageRoute}, model.StageCluster, model.StageRoute, false},
		{"stopped in aggregate", fromReview(model.StageAggregate), model.StageReview, model.StageAggregate, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			c := newClient(t, st, branchGit(), nil, nil)
			if err := st.PutItemReviews(ctx, "branch:a", rows); err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				if err := st.PutReview(ctx, r); err != nil {
					t.Fatal(err)
				}
			}
			got := c.resumeOptions(ctx, "branch:a", tc.rec, pipeline.RunOptions{FromStage: tc.from, Force: true})
			if got.FromStage != tc.want || !got.Force {
				t.Errorf("options = %+v, want FromStage %q (Force kept)", got, tc.want)
			}
			for _, r := range rows {
				_, ok, err := st.Review(ctx, r.GroupID, r.Tier)
				if err != nil {
					t.Fatal(err)
				}
				gone := tc.dropped && (r.GroupID == "stopped" || r.GroupID == "overcap")
				if ok == gone {
					t.Errorf("cached review of %s: stored %v, want %v", r.GroupID, ok, !gone)
				}
			}
		})
	}
}
