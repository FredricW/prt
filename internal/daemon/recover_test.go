package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// ctxGit blocks Diff until its context is cancelled, signalling entered.
type ctxGit struct {
	*gitx.Fake
	entered chan struct{}
}

func (g *ctxGit) Diff(ctx context.Context, _, _ string) (string, error) {
	close(g.entered)
	<-ctx.Done()
	return "", ctx.Err()
}

// Quitting mid-analysis must not leave the item in Error (it would never be
// analysed again automatically): it goes back to "not analysed".
func TestCloseMidRunResetsState(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, branchItem("a", 1))
	g := &ctxGit{Fake: branchGit("a"), entered: make(chan struct{})}
	c := newClient(t, st, g, nil, nil)
	id := model.BranchItemID("a")
	if err := c.Analyse(ctx, false, id); err != nil {
		t.Fatal(err)
	}
	<-g.entered
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	it, err := st.Item(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if it.State != "" || it.StateNote != "" {
		t.Fatalf("after close mid-run: state %q note %q, want not analysed", it.State, it.StateNote)
	}
}

// A process killed mid-run leaves Fetching/Analysing in the store; the next
// start must reset them so they are analysed again (and stop spinning).
func TestStartupRecoversBusyStates(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fetching := branchItem("f", 1)
	fetching.State = model.StateFetching
	analysing := branchItem("g", 1)
	analysing.State, analysing.StateNote, analysing.AnalysedSHA = model.StateAnalysing, "label", "h-g"
	moved := branchItem("m", 1)
	moved.State, moved.AnalysedSHA = model.StateAnalysing, "h-old"
	ready := branchItem("r", 1)
	ready.State = model.StateReady
	put(t, st, fetching, analysing, moved, ready)
	_ = newClient(t, st, branchGit(), nil, nil)
	want := map[string]model.ItemState{
		fetching.ID: "", analysing.ID: model.StateReady, moved.ID: model.StateStale, ready.ID: model.StateReady,
	}
	for id, w := range want {
		it, err := st.Item(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if it.State != w {
			t.Errorf("%s: state %q, want %q", id, it.State, w)
		}
	}
}

// A Stale item keeps showing the analysed head's diff, matching the stored
// files and units, until it is refreshed.
func TestDetailStaleKeepsAnalysedDiff(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, branchItem("a", 1))
	c := newClient(t, st, branchGit("a"), nil, nil)
	id := model.BranchItemID("a")
	if err := c.RunSync(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := c.upsertDiscovered(ctx, id, func(it *model.Item) {
		applyBranch(it, gitx.Ref{Name: "a", SHA: "h2"}, "main", 1, 1)
	}); err != nil {
		t.Fatal(err)
	}
	det, err := c.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if det.Item.State != model.StateStale || det.Item.AnalysedSHA != "h-a" {
		t.Fatalf("item = state %q analysed %q, want stale analysed at h-a", det.Item.State, det.Item.AnalysedSHA)
	}
	if !strings.Contains(det.Diff, "a.txt") || len(det.Units) == 0 {
		t.Fatalf("stale detail: diff %q, %d units", det.Diff, len(det.Units))
	}
}

// The daemon queues never-analysed items itself, so the cache fills with no
// TUI attached.
func TestAnalysePending(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	done := branchItem("done", 5)
	done.State = model.StateReady
	put(t, st, branchItem("a", 1), branchItem("b", 2), done)
	c := newClient(t, st, branchGit("a", "b", "done"), nil, nil)
	if err := c.AnalysePending(ctx); err != nil {
		t.Fatal(err)
	}
	got := waitReady(t, c, 2)
	if got[model.BranchItemID("a")] != model.StateReady || got[model.BranchItemID("b")] != model.StateReady {
		t.Fatalf("ready = %v, want a and b", got)
	}
	if _, ok := got[done.ID]; ok {
		t.Fatalf("already analysed item was re-run: %v", got)
	}
}

// With no GitHub client, discovery says why in the status bar.
func TestDiscoverWithoutGitHubSendsNote(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, newTestStore(t), discoverGit(), nil, nil)
	c.SetGitHubNote("GitHub disabled (no token); local branches only")
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range drain(c.Events()) {
		if e.Type == proto.EventLog && strings.Contains(e.Note, "no token") {
			found = true
		}
	}
	if !found {
		t.Fatal("no EventLog note explaining that GitHub is disabled")
	}
}

// A discovery pass that changes nothing must not emit item events (each one
// makes clients reload the whole list).
func TestDiscoverUnchangedEmitsNoItemEvents(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, newTestStore(t), discoverGit(), nil, nil)
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if first := drain(c.Events()); !hasEvent(first, proto.EventItem, "") {
		t.Fatalf("first discovery emitted no item events: %v", first)
	}
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	for _, e := range drain(c.Events()) {
		if e.Type == proto.EventItem {
			t.Fatalf("unchanged discovery emitted %+v", e)
		}
	}
}
