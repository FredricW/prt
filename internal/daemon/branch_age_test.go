package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

// A branch analysed while fresh (Jev: WIP) turns abandoned? once it has
// been idle for abandoned_days, at the next discovery and in Detail, with
// no new analysis (its head never moves).
func TestDiscoveryAgesBranchState(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	last := testNow.Add(-20 * 24 * time.Hour)
	g := branchGit("feat/idle")
	g.RefList[0].CommitDate = last
	g.Ahead = map[string]int{"main..feat/idle": 1}
	it := branchItem("feat/idle", 1)
	it.AnalysedSHA, it.State, it.LastCommitAt = it.HeadSHA, model.StateReady, last
	it.BranchState, it.BranchStateP = model.BranchWIP, 0.7
	put(t, st, it)
	if err := st.PutAgg(ctx, model.ItemAgg{ItemID: it.ID, Branch: &model.BranchInfo{
		State: model.BranchWIP, P: 0.7, Known: true, IdleDays: 3,
		Probs: map[string]float64{model.BranchWIP: 0.7, model.BranchAbandoned: 0.2, model.BranchReady: 0.1},
	}}); err != nil {
		t.Fatal(err)
	}
	c := newClient(t, st, g, nil, nil)
	captureJobs(c)
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.Item(ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BranchState != model.BranchAbandoned || got.BranchStateP != 0.2 {
		t.Errorf("after discovery: state %q P %v, want abandoned? .2", got.BranchState, got.BranchStateP)
	}
	det, err := c.Detail(ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b := det.Agg.Branch; b == nil || b.State != model.BranchAbandoned || b.IdleDays != 20 || b.Rule != "no commits for 20 days" || !b.Known {
		t.Errorf("detail branch block = %+v", det.Agg.Branch)
	}
}
