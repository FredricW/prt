package daemon

import (
	"context"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/testutil"
)

// TestFixtureRepoDiscoverAndRun runs discovery and the pipeline against a
// freshly generated fixture repo and the offline GitHub fixture.
func TestFixtureRepoDiscoverAndRun(t *testing.T) {
	ctx := context.Background()
	repo := testutil.FixtureRepo(t)
	fake, err := gh.LoadFixture(testutil.FixturePRs(t))
	if err != nil {
		t.Fatal(err)
	}
	g, err := gitx.Open(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	c := NewInProcess(pipeline.Deps{Git: g, GH: fake, Store: newTestStore(t), Config: config.Defaults()})
	defer func() { _ = c.Close() }()
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	m := itemMap(t, c)
	want := []string{
		model.PRItemID(101), model.PRItemID(102), model.PRItemID(103), model.PRItemID(104), model.PRItemID(106),
		model.BranchItemID("05-spike-dark-mode"),
	}
	for _, id := range want {
		if _, ok := m[id]; !ok {
			t.Errorf("missing %s; have %v", id, keys(m))
		}
	}
	if len(m) != len(want) {
		t.Errorf("items = %v, want exactly %v", keys(m), want)
	}
	sections := map[string]model.Section{
		model.PRItemID(101):                      model.SectionNeedsYou, // review requested
		model.PRItemID(102):                      model.SectionYours,    // authored
		model.PRItemID(103):                      model.SectionNeedsYou, // review requested
		model.PRItemID(104):                      model.SectionNeedsYou, // approved, CI red
		model.PRItemID(106):                      model.SectionInvolved, // commented, nothing asked of you
		model.BranchItemID("05-spike-dark-mode"): model.SectionBranches,
	}
	for id, sec := range sections {
		if got := m[id].Section; got != sec {
			t.Errorf("%s section = %q, want %q", id, got, sec)
		}
	}
	for _, id := range want {
		if _, ok := m[id]; !ok {
			continue
		}
		if err := c.RunSync(ctx, id); err != nil {
			t.Errorf("run %s: %v", id, err)
			continue
		}
		det, err := c.Detail(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if det.Diff == "" || len(det.Units) == 0 || det.Agg == nil || det.Item.State != model.StateReady {
			t.Errorf("%s: diff %d bytes, %d units, agg %v, state %q", id, len(det.Diff), len(det.Units), det.Agg != nil, det.Item.State)
		}
	}
}
