package daemon

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/review"
)

// gatedReviewer answers like review.FakeRespond. While armed, the first
// pass review calls answer and every later one blocks until the gate is
// released or its context ends (a user stop). It counts review calls per
// group; prose tasks (titles, TL;DR) never block.
type gatedReviewer struct {
	name    string
	entered chan string // groups whose call blocked

	mu    sync.Mutex
	gate  chan struct{}
	pass  int
	calls map[string]int
}

func newGatedReviewer() *gatedReviewer {
	return &gatedReviewer{name: "fake:gated", entered: make(chan string, 16), calls: map[string]int{}}
}

func (r *gatedReviewer) Model() string { return r.name }

func (r *gatedReviewer) Review(ctx context.Context, req review.Request) (review.Result, error) {
	if req.Task == review.TaskReview {
		r.mu.Lock()
		r.calls[req.GroupID]++
		gate := r.gate
		if gate != nil && r.pass > 0 {
			r.pass--
			gate = nil
		}
		r.mu.Unlock()
		if gate != nil {
			r.entered <- req.GroupID
			select {
			case <-gate:
			case <-ctx.Done():
				return review.Result{}, context.Cause(ctx)
			}
		}
	}
	return review.FakeRespond(0.9)(req)
}

// arm blocks review calls after the next pass ones until release.
func (r *gatedReviewer) arm(pass int) (release func()) {
	gate := make(chan struct{})
	r.mu.Lock()
	r.gate, r.pass = gate, pass
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.gate = nil
			r.mu.Unlock()
			close(gate)
		})
	}
}

// waitBlocked returns the group of the next call that blocked.
func (r *gatedReviewer) waitBlocked(t *testing.T) string {
	t.Helper()
	select {
	case g := <-r.entered:
		return g
	case <-time.After(5 * time.Second):
		t.Fatal("no review call blocked")
		return ""
	}
}

// callCounts copies the review calls per group.
func (r *gatedReviewer) callCounts() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.calls))
	for k, v := range r.calls {
		out[k] = v
	}
	return out
}

// snapEnv is one branch item "a" analysed with gated reviewers.
type snapEnv struct {
	c  *InProcess
	g  *gitx.Fake
	rv *gatedReviewer
	id string
}

func newSnapEnv(t *testing.T) *snapEnv {
	t.Helper()
	st := newTestStore(t)
	put(t, st, branchItem("a", 1))
	e := &snapEnv{g: branchGit("a"), rv: newGatedReviewer(), id: model.BranchItemID("a")}
	e.c = newClient(t, st, e.g, nil, func(d *pipeline.Deps) { d.Reviewers = review.Tiers{Fast: e.rv, Deep: e.rv} })
	if err := e.c.RunSync(context.Background(), e.id); err != nil {
		t.Fatal(err)
	}
	return e
}

// push moves the branch to a new head whose diff touches new.txt.
func (e *snapEnv) push() {
	e.g.RefList[0].SHA = "h-new"
	e.g.MergeBases["main..h-new"] = "b1"
	e.g.Diffs["b1..h-new"] = oneLineDiff("new.txt")
}

// sameAnalysis fails unless got serves want's analysis parts.
func sameAnalysis(t *testing.T, what string, want, got proto.Detail) {
	t.Helper()
	pairs := map[string][2]any{
		"diff": {want.Diff, got.Diff}, "files": {want.Files, got.Files}, "units": {want.Units, got.Units},
		"labels": {want.Labels, got.Labels}, "links": {want.Links, got.Links}, "groups": {want.Groups, got.Groups},
		"reviews": {want.Reviews, got.Reviews}, "agg": {want.Agg, got.Agg},
	}
	for name, p := range pairs {
		if !reflect.DeepEqual(p[0], p[1]) {
			t.Errorf("%s: %s differ from the analysis before the run", what, name)
		}
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// While a re-run of an analysed item is in flight, Detail serves the last
// complete analysis with Run.Snapshot set and live comments; when the run
// completes it serves the new analysis and Run is nil (M5.2).
func TestSnapshotServedDuringRerun(t *testing.T) {
	ctx := context.Background()
	e := newSnapEnv(t)
	c, id := e.c, e.id
	old, err := c.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if old.Run != nil || old.Agg == nil || !strings.Contains(old.Diff, "a.txt") || len(old.Labels) == 0 || len(old.Reviews) != 1 {
		t.Fatalf("analysed detail = run %+v agg %v diff %q, %d labels, %d rows", old.Run, old.Agg != nil, old.Diff, len(old.Labels), len(old.Reviews))
	}

	e.push()
	release := e.rv.arm(0)
	defer release()
	if err := c.Refresh(ctx, proto.ItemParams{ItemID: id, Force: true, User: true}); err != nil {
		t.Fatal(err)
	}
	e.rv.waitBlocked(t)
	if us, err := c.d.Store.Units(ctx, id); err != nil || len(us) != 1 || us[0].Path != "new.txt" {
		t.Fatalf("the run should have stored the new head's units by now: %+v, %v", us, err)
	}

	during, err := c.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r := during.Run; r == nil || !r.Snapshot || r.SnapshotSHA != "h-a" || r.Stage != model.StageReview {
		t.Fatalf("run status = %+v, want a snapshot of h-a at review", r)
	}
	sameAnalysis(t, "during the run", old, during)
	if during.Item.HeadSHA != "h-new" || during.Item.State != model.StateAnalysing {
		t.Errorf("item = head %q state %q, want the live item", during.Item.HeadSHA, during.Item.State)
	}
	if rows := during.Run.Reviews; len(rows) != 1 || rows[0].Status != model.ReviewRunning {
		t.Errorf("live routing rows = %+v, want the running review", rows)
	}

	// Comments are live: one saved during the run shows at once.
	cm, err := c.SaveComment(ctx, proto.CommentParams{ItemID: id, Comment: model.Comment{Path: "new.txt", Line: 2, Side: model.SideNew, Body: "while it runs"}})
	if err != nil {
		t.Fatal(err)
	}
	withComment, err := c.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !hasComment(withComment.Comments, cm.ID) || withComment.Run == nil || !withComment.Run.Snapshot {
		t.Errorf("comment %s saved during the run: comments %+v, run %+v", cm.ID, withComment.Comments, withComment.Run)
	}

	// The lean Detail and Labels serve the snapshot too.
	lean, err := c.DetailOf(ctx, proto.DetailParams{ItemID: id, NoLabels: true})
	if err != nil {
		t.Fatal(err)
	}
	if !lean.LabelsOmitted || lean.Labels != nil || lean.Run == nil || !lean.Run.Snapshot || !reflect.DeepEqual(lean.Units, old.Units) {
		t.Errorf("lean detail during the run = omitted %v, %d labels, run %+v", lean.LabelsOmitted, len(lean.Labels), lean.Run)
	}
	all, err := c.Labels(ctx, proto.LabelsParams{ItemID: id})
	if err != nil || !reflect.DeepEqual(all, old.Labels) {
		t.Errorf("labels during the run = %d (%v), want the snapshot's %d", len(all), err, len(old.Labels))
	}
	unit := old.Units[0].ID
	one, err := c.Labels(ctx, proto.LabelsParams{ItemID: id, UnitIDs: []string{unit}})
	if err != nil || len(one) == 0 {
		t.Fatalf("labels of one unit = %+v, %v", one, err)
	}
	for _, l := range one {
		if l.UnitID != unit {
			t.Errorf("label of %s served for unit %s", l.UnitID, unit)
		}
	}

	// The socket serves the same, Run and LabelsOmitted included.
	sock, _, _ := startServer(t, c)
	sLean, err := sock.DetailOf(ctx, proto.DetailParams{ItemID: id, NoLabels: true})
	if err != nil {
		t.Fatal(err)
	}
	if jsonOf(t, sLean) != jsonOf(t, lean) {
		t.Errorf("socket lean detail differs from in-process:\n%s\n%s", jsonOf(t, sLean), jsonOf(t, lean))
	}
	sOne, err := sock.Labels(ctx, proto.LabelsParams{ItemID: id, UnitIDs: []string{unit}})
	if err != nil || jsonOf(t, sOne) != jsonOf(t, one) {
		t.Errorf("socket labels differ: %v", err)
	}

	release()
	c.wg.Wait()
	after, err := c.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Run != nil || c.snaps.has(id) {
		t.Fatalf("after the run: run %+v, snapshot held %v", after.Run, c.snaps.has(id))
	}
	if after.Item.AnalysedSHA != "h-new" || !strings.Contains(after.Diff, "new.txt") || len(after.Units) != 1 || after.Units[0].Path != "new.txt" {
		t.Errorf("after the run: analysed %q diff %q units %+v, want the new head's analysis", after.Item.AnalysedSHA, after.Diff, after.Units)
	}
	if reflect.DeepEqual(after.Agg, old.Agg) {
		t.Error("after the run the aggregate is still the old one")
	}
}

func hasComment(cs []model.Comment, id string) bool {
	for _, c := range cs {
		if c.ID == id {
			return true
		}
	}
	return false
}

// A stopped re-run leaves the store a mix: Detail keeps serving the
// snapshot (with Run nil: nothing is in flight) until a run completes.
func TestSnapshotKeptAfterStop(t *testing.T) {
	ctx := context.Background()
	e := newSnapEnv(t)
	c, id := e.c, e.id
	old, err := c.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	release := e.rv.arm(0)
	defer release()
	if err := c.Refresh(ctx, proto.ItemParams{ItemID: id, FromStage: model.StageReview, User: true}); err != nil {
		t.Fatal(err)
	}
	e.rv.waitBlocked(t)
	if _, err := c.Stop(ctx, proto.StopParams{ItemID: id}); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	waitState(t, c.d.Store, id, model.StateStopped)
	if rows, _ := c.d.Store.ItemReviews(ctx, id); len(rows) != 1 || rows[0].Status != model.ReviewStopped {
		t.Fatalf("stored rows after the stop = %+v, want the stopped review", rows)
	}
	stopped, err := c.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Run != nil || stopped.Item.State != model.StateStopped {
		t.Errorf("after the stop: run %+v, state %q", stopped.Run, stopped.Item.State)
	}
	sameAnalysis(t, "after the stop", old, stopped)

	release()
	if _, err := c.Resume(ctx, proto.ResumeParams{ItemID: id}); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	done, err := c.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.snaps.has(id) || done.Run != nil || len(done.Reviews) != 1 || done.Reviews[0].Status != model.ReviewDone {
		t.Errorf("after the resumed run: snapshot %v, run %+v, rows %+v", c.snaps.has(id), done.Run, done.Reviews)
	}
}

// A first run and an aggregate-only run have nothing to snapshot: Detail
// reports the stage only.
func TestRunStatusWithoutSnapshot(t *testing.T) {
	ctx := context.Background()
	t.Run("first run", func(t *testing.T) {
		c, rec := stopClient(t, 1, "a")
		id := model.BranchItemID("a")
		if err := c.Analyse(ctx, false, id); err != nil {
			t.Fatal(err)
		}
		rec.waitStarted(t, id)
		d, err := c.Detail(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Run == nil || d.Run.Snapshot || d.Run.Stage != model.StageReview || c.snaps.len() != 0 {
			t.Errorf("first run: run %+v, %d snapshots", d.Run, c.snaps.len())
		}
		if _, err := c.Stop(ctx, proto.StopParams{ItemID: id}); err != nil {
			t.Fatal(err)
		}
		c.wg.Wait()
		if d, _ := c.Detail(ctx, id); d.Run != nil {
			t.Errorf("after the stop: run %+v", d.Run)
		}
	})
	t.Run("aggregate only", func(t *testing.T) {
		e := newSnapEnv(t)
		rec := recordRuns(e.c)
		rec.setBlock(true)
		e.c.q.push(job{itemID: e.id, aggOnly: true, rerun: true, explicit: true})
		rec.waitStarted(t, e.id)
		d, err := e.c.Detail(ctx, e.id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Run == nil || d.Run.Snapshot || e.c.snaps.len() != 0 {
			t.Errorf("aggregate-only run: run %+v, %d snapshots", d.Run, e.c.snaps.len())
		}
		if _, err := e.c.Stop(ctx, proto.StopParams{ItemID: e.id}); err != nil {
			t.Fatal(err)
		}
		e.c.wg.Wait()
	})
}

func TestSnapshotsBounded(t *testing.T) {
	s := newSnapshots(3)
	for _, id := range []string{"a", "b", "c", "d"} {
		s.put(id, &analysis{head: id})
	}
	if s.len() != 3 || s.has("a") || !s.has("d") {
		t.Fatalf("after 4 puts: %d held, a %v, d %v; want the oldest dropped", s.len(), s.has("a"), s.has("d"))
	}
	s.put("b", &analysis{head: "b2"}) // replacing makes it the newest
	s.put("e", &analysis{head: "e"})
	if s.has("c") || !s.has("b") || s.get("b").head != "b2" {
		t.Fatalf("eviction order wrong: c %v, b %v", s.has("c"), s.has("b"))
	}
	s.drop("b")
	if s.has("b") || s.len() != 2 {
		t.Fatalf("drop: %d held", s.len())
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := string(rune('k' + i))
			s.put(id, &analysis{})
			_ = s.get(id)
			s.drop(id)
		}()
	}
	wg.Wait()
	if s.len() > 3 {
		t.Fatalf("%d held, bound 3", s.len())
	}
}
