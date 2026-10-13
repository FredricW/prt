package daemon

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
)

// labelReads records the label and override reads a store serves.
type labelReads struct {
	store.Store
	mu        sync.Mutex
	labels    [][]string
	overrides int
}

func (s *labelReads) Labels(ctx context.Context, ids []string) ([]model.Label, error) {
	s.mu.Lock()
	s.labels = append(s.labels, slices.Clone(ids))
	s.mu.Unlock()
	return s.Store.Labels(ctx, ids)
}

func (s *labelReads) Overrides(ctx context.Context, ids []string) ([]model.Override, error) {
	s.mu.Lock()
	s.overrides++
	s.mu.Unlock()
	return s.Store.Overrides(ctx, ids)
}

// reset forgets the reads so far.
func (s *labelReads) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.labels, s.overrides = nil, 0
}

func (s *labelReads) counts() ([][]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.labels), s.overrides
}

// leanEnv is an analysed branch item "a" over a store that records label
// reads, with an override on its unit.
func leanEnv(t *testing.T) (*InProcess, *labelReads, string) {
	t.Helper()
	ctx := context.Background()
	st := &labelReads{Store: newTestStore(t)}
	put(t, st, branchItem("a", 1))
	c := newClient(t, st, branchGit("a"), nil, nil)
	id := model.BranchItemID("a")
	if err := c.RunSync(ctx, id); err != nil {
		t.Fatal(err)
	}
	units, err := st.Units(ctx, id)
	if err != nil || len(units) != 1 {
		t.Fatalf("units = %+v, %v", units, err)
	}
	if err := st.PutLabels(ctx, []model.Label{{UnitID: model.ItemSubject(id), Question: model.QShape, Value: "mixed", P: 0.7}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutOverride(ctx, model.Override{UnitID: units[0].ID, Question: model.QShape, Value: "feature", At: testNow}); err != nil {
		t.Fatal(err)
	}
	st.reset()
	return c, st, id
}

// DetailOf with NoLabels reads no label and no override; everything else
// is what the full Detail serves.
func TestDetailOfNoLabelsReadsNoLabels(t *testing.T) {
	ctx := context.Background()
	c, st, id := leanEnv(t)
	lean, err := c.DetailOf(ctx, proto.DetailParams{ItemID: id, NoLabels: true})
	if err != nil {
		t.Fatal(err)
	}
	if ls, ovs := st.counts(); len(ls) != 0 || ovs != 0 {
		t.Fatalf("lean detail read labels %v and overrides %d times", ls, ovs)
	}
	if !lean.LabelsOmitted || lean.Labels != nil {
		t.Fatalf("lean detail: omitted %v, %d labels", lean.LabelsOmitted, len(lean.Labels))
	}
	full, err := c.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if full.LabelsOmitted || len(full.Labels) == 0 || !anyOverridden(full.Labels) {
		t.Fatalf("full detail: omitted %v, %d labels, overridden %v", full.LabelsOmitted, len(full.Labels), anyOverridden(full.Labels))
	}
	full.Labels, full.LabelsOmitted = nil, true
	if jsonOf(t, full) != jsonOf(t, lean) {
		t.Errorf("lean detail differs from the full one beyond labels:\n%s\n%s", jsonOf(t, lean), jsonOf(t, full))
	}
}

func anyOverridden(ls []model.Label) bool {
	for _, l := range ls {
		if l.Overridden != nil {
			return true
		}
	}
	return false
}

// Labels reads only the requested subjects (all the item's when none is
// given) and applies overrides as Detail does.
func TestLabelsReadsRequestedSubjects(t *testing.T) {
	ctx := context.Background()
	c, st, id := leanEnv(t)
	full, err := c.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	unit := full.Units[0].ID
	st.reset()
	one, err := c.Labels(ctx, proto.LabelsParams{ItemID: id, UnitIDs: []string{unit}})
	if err != nil {
		t.Fatal(err)
	}
	if ls, _ := st.counts(); len(ls) != 1 || !reflect.DeepEqual(ls[0], []string{unit}) {
		t.Fatalf("label reads = %v, want only %s", ls, unit)
	}
	if len(one) == 0 || !anyOverridden(one) {
		t.Fatalf("one unit's labels = %+v, want them with the override", one)
	}
	for _, l := range one {
		if l.UnitID != unit {
			t.Errorf("label of %s served for %s", l.UnitID, unit)
		}
	}
	all, err := c.Labels(ctx, proto.LabelsParams{ItemID: id})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all, full.Labels) {
		t.Errorf("all labels = %d, want Detail's %d", len(all), len(full.Labels))
	}
	if _, err := c.Labels(ctx, proto.LabelsParams{ItemID: "branch:missing"}); err == nil {
		t.Error("labels of an unknown item: want an error")
	}
}

// Socket parity for the M5.2 methods on an idle item.
func TestSocketDetailOfAndLabels(t *testing.T) {
	ctx := context.Background()
	c, _, id := leanEnv(t)
	want := map[string]any{}
	for name, p := range map[string]proto.DetailParams{"lean": {ItemID: id, NoLabels: true}, "full": {ItemID: id}} {
		d, err := c.DetailOf(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		want[name] = d
	}
	ls, err := c.Labels(ctx, proto.LabelsParams{ItemID: id})
	if err != nil {
		t.Fatal(err)
	}
	want["labels"] = ls
	sock, _, _ := startServer(t, c)
	for name, p := range map[string]proto.DetailParams{"lean": {ItemID: id, NoLabels: true}, "full": {ItemID: id}} {
		d, err := sock.DetailOf(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if jsonOf(t, d) != jsonOf(t, want[name]) {
			t.Errorf("%s detail differs over the socket", name)
		}
	}
	sls, err := sock.Labels(ctx, proto.LabelsParams{ItemID: id})
	if err != nil || jsonOf(t, sls) != jsonOf(t, want["labels"]) {
		t.Errorf("labels differ over the socket: %v", err)
	}
	if _, err := sock.Labels(ctx, proto.LabelsParams{ItemID: "branch:missing"}); err == nil {
		t.Error("labels of an unknown item over the socket: want an error")
	}
}

// Detail serves only the current run's rows, and finishing a run keeps
// the item's last keepRuns runs in the store.
func TestDetailCurrentRunAndRunLogPrune(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, branchItem("a", 1))
	c := newClient(t, st, branchGit("a"), nil, nil)
	id := model.BranchItemID("a")
	for range keepRuns + 3 {
		if err := c.RunSync(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	all, err := st.RunLogs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if n := fetchRows(all); n != keepRuns {
		t.Errorf("stored runs = %d, want the last %d", n, keepRuns)
	}
	d, err := c.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if fetchRows(d.RunLogs) != 1 || d.RunLogs[0].Stage != model.StageFetch {
		t.Fatalf("detail run log = %+v, want one run starting at fetch", d.RunLogs)
	}
	last := all[len(all)-len(d.RunLogs):]
	if !reflect.DeepEqual(last, d.RunLogs) {
		t.Errorf("detail run log is not the last run's rows")
	}
}

func fetchRows(rls []model.RunLog) int {
	n := 0
	for _, rl := range rls {
		if rl.Stage == model.StageFetch {
			n++
		}
	}
	return n
}

// Labels nothing references are swept when the client starts; removing
// an item prunes its labels except those of a unit another item holds,
// and a removed preview keeps its labels until the next start.
func TestLabelPruning(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, branchItem("a", 1), branchItem("b", 1))
	pre := newClient(t, st, branchGit("a", "b"), nil, nil)
	for _, n := range []string{"a", "b"} {
		if err := pre.RunSync(ctx, model.BranchItemID(n)); err != nil {
			t.Fatal(err)
		}
	}
	_ = pre.Close()
	unitOf := func(id string) string {
		us, err := st.Units(ctx, id)
		if err != nil || len(us) == 0 {
			t.Fatalf("units of %s: %+v, %v", id, us, err)
		}
		return us[0].ID
	}
	ua, ub := unitOf(model.BranchItemID("a")), unitOf(model.BranchItemID("b"))
	if err := st.PutLabels(ctx, []model.Label{{UnitID: "ghost", Question: "q", Value: "v"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutOverride(ctx, model.Override{UnitID: "ghost", Question: "q", Value: "no"}); err != nil {
		t.Fatal(err)
	}
	c := newClient(t, st, branchGit("a", "b"), nil, nil)
	if n := labelCount(t, st, "ghost"); n != 0 {
		t.Errorf("%d orphan labels left after start", n)
	}
	if ovs, _ := st.AllOverrides(ctx); len(ovs) != 1 {
		t.Errorf("overrides after the sweep = %+v; user data must stay", ovs)
	}
	if labelCount(t, st, ua) == 0 || labelCount(t, st, ub) == 0 {
		t.Fatal("the sweep removed labels of stored units")
	}
	if err := c.Remove(ctx, model.BranchItemID("a")); err != nil {
		t.Fatal(err)
	}
	if labelCount(t, st, ua) != 0 || labelCount(t, st, model.ItemSubject(model.BranchItemID("a"))) != 0 {
		t.Error("labels of a removed item kept")
	}
	if labelCount(t, st, ub) == 0 {
		t.Error("labels of another item's unit removed")
	}

	// A preview's labels stay when it is discarded (tabbing back is free).
	b := stored(t, st, model.BranchItemID("b"))
	b.Source = model.SourcePreview
	put(t, st, b)
	if err := c.RemovePreview(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if labelCount(t, st, ub) == 0 {
		t.Error("a discarded preview's labels were pruned at once")
	}
}

func labelCount(t *testing.T, st store.Store, subject string) int {
	t.Helper()
	ls, err := st.Labels(context.Background(), []string{subject})
	if err != nil {
		t.Fatal(err)
	}
	return len(ls)
}
