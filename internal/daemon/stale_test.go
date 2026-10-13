package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// blockingGit blocks Diff until release is closed, signalling entered first.
type blockingGit struct {
	*gitx.Fake
	entered chan struct{}
	release chan struct{}
}

func (b *blockingGit) Diff(ctx context.Context, base, head string) (string, error) {
	close(b.entered)
	<-b.release
	return b.Fake.Diff(ctx, base, head)
}

// waitSettled waits until the item has been busy (fetching/analysing) and
// then left the busy states, and returns that item.
func waitSettled(t *testing.T, c *InProcess, id string) model.Item {
	t.Helper()
	var got model.Item
	busy := false
	waitEvent(t, c.Events(), func(e proto.Event) bool {
		if e.Type != proto.EventItem || e.Item == nil || e.ItemID != id {
			return false
		}
		switch e.Item.State {
		case model.StateFetching, model.StateAnalysing:
			busy = true
		case model.StateReady, model.StateStale, model.StateError:
			got = *e.Item
			return busy
		}
		return false
	})
	return got
}

// A push that lands while an item is analysed must leave it Stale, not Ready
// with an empty diff for the new head.
func TestPushDuringAnalysisMarksStale(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, branchItem("a", 1)) // head h-a
	bg := &blockingGit{Fake: branchGit("a"), entered: make(chan struct{}), release: make(chan struct{})}
	c := newClient(t, st, bg, nil, nil)
	id := model.BranchItemID("a")
	if err := c.Analyse(ctx, false, id); err != nil {
		t.Fatal(err)
	}
	<-bg.entered
	if err := c.upsertDiscovered(ctx, id, func(it *model.Item) {
		applyBranch(it, gitx.Ref{Name: "a", SHA: "h2"}, "main", 1, 1)
	}); err != nil {
		t.Fatal(err)
	}
	close(bg.release)
	got := waitSettled(t, c, id)
	if got.State != model.StateStale || got.StateNote != staleNote || got.HeadSHA != "h2" {
		t.Fatalf("item = state %q note %q head %q, want stale at h2", got.State, got.StateNote, got.HeadSHA)
	}
}

// `r` (forced refresh) must analyse the branch's new local head, not the
// head discovery last stored.
func TestForcedRefreshResolvesNewHead(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	it := branchItem("a", 1)
	it.State = model.StateReady
	put(t, st, it) // stored head h-a
	g := branchGit("a")
	g.RefList[0].SHA = "h-new" // a new local commit
	g.MergeBases["main..h-new"] = "b1"
	g.Diffs["b1..h-new"] = oneLineDiff("new.txt")
	c := newClient(t, st, g, nil, nil)
	id := model.BranchItemID("a")
	if err := c.Refresh(ctx, proto.ItemParams{ItemID: id, Force: true}); err != nil {
		t.Fatal(err)
	}
	got := waitSettled(t, c, id)
	if got.State != model.StateReady || got.HeadSHA != "h-new" {
		t.Fatalf("item = state %q head %q, want ready at h-new", got.State, got.HeadSHA)
	}
	det, err := c.Detail(ctx, id)
	if err != nil || !strings.Contains(det.Diff, "new.txt") {
		t.Fatalf("detail diff = %q, %v", det.Diff, err)
	}
}

func TestCheckoutForkPR(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st,
		model.Item{ID: "fork", Kind: model.KindPR, Number: 12, Ref: "patch-1", HeadSHA: "f12", CrossRepo: true},
		model.Item{ID: "fork-remote", Kind: model.KindPR, Number: 13, Ref: "main", HeadSHA: "f13", CrossRepo: true},
	)
	g := &gitx.Fake{SHAs: map[string]string{"f12": "f12"}, RefList: []gitx.Ref{{Name: "patch-1", SHA: "mine"}}}
	c := newClient(t, st, g, nil, nil)
	res, err := c.Checkout(ctx, proto.CheckoutParams{ItemID: "fork"})
	if err != nil || !res.CheckedOut || res.Ref != "f12" {
		t.Fatalf("fork checkout = %+v, %v", res, err)
	}
	if len(g.CheckedOut) != 1 || g.CheckedOut[0] != "f12" {
		t.Fatalf("checked out %v, want the head commit, never the same-named local branch", g.CheckedOut)
	}
	if _, err := c.Checkout(ctx, proto.CheckoutParams{ItemID: "fork-remote"}); err == nil || !strings.Contains(err.Error(), "gh pr checkout 13") {
		t.Fatalf("fork head not local: err = %v", err)
	}
	if len(g.CheckedOut) != 1 {
		t.Fatalf("must not check out the local %q branch: %v", "main", g.CheckedOut)
	}
}
