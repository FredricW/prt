package daemon

import (
	"context"
	"sync"
	"testing"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
)

// branchGit serves a one-line diff for each named branch (head "h-<name>").
func branchGit(names ...string) *gitx.Fake {
	g := &gitx.Fake{MergeBases: map[string]string{}, Diffs: map[string]string{}}
	for _, n := range names {
		head := "h-" + n
		g.RefList = append(g.RefList, gitx.Ref{Name: n, SHA: head})
		g.MergeBases["main.."+head] = "b1"
		g.Diffs["b1.."+head] = oneLineDiff(n + ".txt")
	}
	return g
}

func branchItem(name string, urgency float64) model.Item {
	return model.Item{ID: model.BranchItemID(name), Kind: model.KindBranch, Ref: name, BaseRef: "main",
		HeadSHA: "h-" + name, Urgency: urgency, Source: model.SourceRanked, Section: model.SectionBranches}
}

func TestWorkerPoolRunsByUrgency(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, branchItem("low", 1), branchItem("high", 3), branchItem("mid", 2))
	var mu sync.Mutex
	var order []string
	c := newClient(t, st, branchGit("low", "high", "mid"), nil, func(d *pipeline.Deps) {
		d.Config.Pipeline.Concurrency = 1
		d.Emit = func(rl model.RunLog) {
			mu.Lock()
			defer mu.Unlock()
			if rl.Stage == model.StageFetch && rl.Status == model.StageRunning {
				order = append(order, rl.ItemID)
			}
		}
	})
	if err := c.Analyse(ctx, false, model.BranchItemID("low"), model.BranchItemID("high"), model.BranchItemID("mid")); err != nil {
		t.Fatal(err)
	}
	// Close drops waiting jobs, so wait for all three via events first.
	waitReady(t, c, 3)
	_ = c.Close()
	want := []string{model.BranchItemID("high"), model.BranchItemID("mid"), model.BranchItemID("low")}
	if len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for _, n := range []string{"low", "high", "mid"} {
		it, err := st.Item(ctx, model.BranchItemID(n))
		if err != nil {
			t.Fatal(err)
		}
		if it.State != model.StateReady || it.Additions != 1 {
			t.Errorf("%s = %+v", n, it)
		}
	}
}

// waitReady consumes events until n distinct items reached ready or error.
func waitReady(t *testing.T, c *InProcess, n int) map[string]model.ItemState {
	t.Helper()
	done := map[string]model.ItemState{}
	seenStates := map[model.ItemState]bool{}
	waitEvent(t, c.Events(), func(e proto.Event) bool {
		if e.Type != proto.EventItem || e.Item == nil {
			return false
		}
		seenStates[e.Item.State] = true
		if e.Item.State == model.StateReady || e.Item.State == model.StateError {
			done[e.ItemID] = e.Item.State
		}
		return len(done) == n
	})
	if !seenStates[model.StateFetching] || !seenStates[model.StateAnalysing] {
		t.Errorf("states seen = %v, want fetching and analysing", seenStates)
	}
	return done
}

func TestAnalyseSetsStageNotes(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, branchItem("a", 1))
	c := newClient(t, st, branchGit("a"), nil, nil)
	if err := c.Refresh(ctx, proto.ItemParams{ItemID: model.BranchItemID("a")}); err != nil {
		t.Fatal(err)
	}
	notes := map[string]bool{}
	waitEvent(t, c.Events(), func(e proto.Event) bool {
		if e.Type != proto.EventItem {
			return false
		}
		if e.Item.State == model.StateAnalysing {
			notes[e.Item.StateNote] = true
		}
		return e.Item.State == model.StateReady
	})
	if !notes["parse"] || !notes["aggregate"] {
		t.Fatalf("analysing notes = %v", notes)
	}
	if err := c.Refresh(ctx, proto.ItemParams{ItemID: "branch:missing"}); err == nil {
		t.Fatal("refresh of unknown item should fail")
	}
}

func TestAnalyseErrorState(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, model.Item{ID: model.BranchItemID("broken"), Kind: model.KindBranch, Ref: "broken", HeadSHA: "nope", Source: model.SourceRanked})
	c := newClient(t, st, &gitx.Fake{}, nil, nil)
	if err := c.Analyse(ctx, true, model.BranchItemID("broken")); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, c.Events(), func(e proto.Event) bool {
		if e.Type != proto.EventItem || e.Item.State != model.StateError {
			return false
		}
		if e.Item.StateNote == "" {
			t.Error("error state without reason")
		}
		return true
	})
}

func TestItemsQuartilesAndOrder(t *testing.T) {
	st := newTestStore(t)
	put(t, st,
		model.Item{ID: "a", Section: model.SectionBranches, Urgency: 4},
		model.Item{ID: "b", Section: model.SectionNeedsYou, Urgency: 2},
		model.Item{ID: "c", Section: model.SectionNeedsYou, Urgency: 0.5},
		model.Item{ID: "d", Section: model.SectionAdded, Urgency: 0},
		model.Item{ID: "e", Section: model.SectionYours, Urgency: -1},
		model.Item{ID: "f", Section: model.SectionNeedsYou, Urgency: 2, UpdatedAt: testNow},
		model.Item{ID: "g", Section: model.SectionNeedsYou, Urgency: 3.1},
	)
	c := newClient(t, st, &gitx.Fake{}, nil, nil)
	items, err := c.Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"g", "f", "b", "c", "e", "a", "d"}
	wantQ := map[string]int{"a": 4, "b": 2, "c": 1, "d": 0, "e": 0, "f": 2, "g": 4}
	for i, it := range items {
		if it.ID != wantOrder[i] {
			t.Fatalf("order[%d] = %s, want %s (all %v)", i, it.ID, wantOrder[i], items)
		}
		if it.UrgencyQuartile != wantQ[it.ID] {
			t.Errorf("%s quartile = %d, want %d", it.ID, it.UrgencyQuartile, wantQ[it.ID])
		}
	}
}

func TestCheckout(t *testing.T) {
	tests := []struct {
		name       string
		dirty      bool
		force      bool
		ref        string
		wantDirty  bool
		wantCO     bool
		wantErr    bool
		wantCalled bool
	}{
		{name: "clean", ref: "feat/x", wantCO: true, wantCalled: true},
		{name: "dirty prompts", dirty: true, ref: "feat/x", wantDirty: true},
		{name: "dirty forced", dirty: true, force: true, ref: "feat/x", wantCO: true, wantCalled: true},
		{name: "no ref", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			put(t, st, model.Item{ID: "x", Ref: tc.ref})
			g := &gitx.Fake{Dirty: tc.dirty}
			c := newClient(t, st, g, nil, nil)
			res, err := c.Checkout(context.Background(), proto.CheckoutParams{ItemID: "x", Force: tc.force})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if res.Dirty != tc.wantDirty || res.CheckedOut != tc.wantCO {
				t.Fatalf("res = %+v", res)
			}
			if called := len(g.CheckedOut) > 0; called != tc.wantCalled {
				t.Fatalf("checked out = %v", g.CheckedOut)
			}
		})
	}
}

func TestAdd(t *testing.T) {
	ghFake := &gh.Fake{Login: "me", PRs: map[int]gh.PR{
		4: {Number: 4, Title: "Dark mode toggle", HeadRef: "feat/dark", HeadSHA: "h-feat/dark", BaseRef: "main"},
	}}
	tests := []struct {
		name    string
		query   string
		offline bool
		wantID  string
		wantErr bool
	}{
		{name: "hash number", query: "#4", wantID: model.PRItemID(4)},
		{name: "bare number", query: "4", wantID: model.PRItemID(4)},
		{name: "local ref", query: "feat/x", wantID: model.BranchItemID("feat/x")},
		{name: "title search", query: "dark mode", wantID: model.PRItemID(4)},
		{name: "no match", query: "nothing here", wantErr: true},
		{name: "empty", query: "  ", wantErr: true},
		{name: "number offline", query: "#4", offline: true, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := newTestStore(t)
			g := branchGit("feat/x", "feat/dark")
			var ghc gh.Client = ghFake
			if tc.offline {
				ghc = nil
			}
			c := newClient(t, st, g, ghc, nil)
			it, err := c.Add(ctx, tc.query)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if tc.wantErr {
				return
			}
			if it.ID != tc.wantID || it.Section != model.SectionAdded || it.Source != model.SourceAdded {
				t.Fatalf("item = %+v", it)
			}
			if st := waitReady(t, c, 1)[it.ID]; st != model.StateReady {
				t.Fatalf("analysis state = %q", st)
			}
		})
	}
}

func TestAddExistingKeepsSection(t *testing.T) {
	st := newTestStore(t)
	put(t, st, branchItem("feat/x", 1))
	c := newClient(t, st, branchGit("feat/x"), nil, nil)
	it, err := c.Add(context.Background(), "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	if it.Section != model.SectionBranches || it.Source != model.SourceRanked {
		t.Fatalf("item = %+v", it)
	}
}

func TestRemove(t *testing.T) {
	st := newTestStore(t)
	put(t, st, model.Item{ID: "x"})
	c := newClient(t, st, &gitx.Fake{}, nil, nil)
	if err := c.Remove(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if len(itemMap(t, c)) != 0 || !hasEvent(drain(c.Events()), proto.EventRemove, "x") {
		t.Fatal("item not removed or no event")
	}
}

func TestQueueDedupes(t *testing.T) {
	var wg sync.WaitGroup
	block := make(chan struct{})
	var mu sync.Mutex
	runs := map[string]int{}
	q := newQueue(1, &wg, func(j job) {
		<-block
		mu.Lock()
		runs[j.itemID]++
		mu.Unlock()
	})
	q.push(job{itemID: "a"}) // starts and blocks
	q.push(job{itemID: "a"}) // running, not forced: dropped
	q.push(job{itemID: "b"}, job{itemID: "b"})
	q.push(job{itemID: "a", force: true}) // re-queued after the running one
	close(block)
	wg.Wait()
	if runs["a"] != 2 || runs["b"] != 1 {
		t.Fatalf("runs = %v", runs)
	}
}
