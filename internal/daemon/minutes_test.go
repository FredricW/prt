package daemon

import (
	"context"
	"testing"

	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
)

// Regression: a discovery pass after analysis must keep the label-based
// minutes the aggregate stage stored, not reset them to the line-count
// stand-in. A pass that moves the head falls back to the stand-in.
func TestDiscoveryKeepsAggregateMinutes(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	it := branchItem("a", 0)
	it.Additions, it.Deletions = 40, 10 // stand-in: 5 minutes
	it.Minutes, it.State, it.AnalysedSHA = 7, model.StateReady, it.HeadSHA
	put(t, st, it)
	if err := st.PutAgg(ctx, model.ItemAgg{ItemID: it.ID, Minutes: 7}); err != nil {
		t.Fatal(err)
	}
	c := newClient(t, st, branchGit("a"), nil, nil)
	pass := func(sha string) model.Item {
		t.Helper()
		if err := c.upsertDiscovered(ctx, it.ID, func(x *model.Item) {
			applyBranch(x, gitx.Ref{Name: "a", SHA: sha}, "main", 1, 0)
		}); err != nil {
			t.Fatal(err)
		}
		got, err := st.Item(ctx, it.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := pass(it.HeadSHA); got.Minutes != 7 {
		t.Fatalf("same head: minutes = %v, want the aggregate's 7", got.Minutes)
	}
	if got := pass("h-moved"); got.Minutes != 5 || got.State != model.StateStale {
		t.Fatalf("moved head: minutes = %v state %q, want stand-in 5 and stale", got.Minutes, got.State)
	}
}
