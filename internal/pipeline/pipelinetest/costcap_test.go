package pipelinetest_test

import (
	"context"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/pipeline/pipelinetest"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/testutil"
)

// TestFixtureDemoCostPlan pins how the default per-item cost cap plans the
// fixture items on the `make demo` path: the fixture config, the built-in
// routing (cost_cap_usd 0.40), and reviews replayed from the recordings
// under the configured model names, with a miss degrading as in the demo.
// The plan is decided before any call, from review.PlanCost estimates, so
// it does not depend on what the replay answers. Every routed group of
// every fixture item fits the cap; #101's fifteen deep groups reserve
// about $0.36 (DECISIONS: cost cap kept at $0.40, M5.2).
func TestFixtureDemoCostPlan(t *testing.T) {
	dir := testutil.RecordingsDir(t, "review")
	cfg := config.Defaults()
	tiers := review.Tiers{
		Fast: &review.Replay{Name: cfg.Models.Fast, Dir: dir, MissUnavailable: true},
		Deep: &review.Replay{Name: cfg.Models.Deep, Dir: dir, MissUnavailable: true},
	}
	f := openFixture(t, tiers, nil)
	if got := config.DefaultRouting().CostCapUSD; got != 0.40 {
		t.Fatalf("default cost cap %.2f; this test pins the $0.40 plan", got)
	}
	want := map[int]struct{ deep, fast int }{101: {15, 0}, 102: {6, 3}, 103: {1, 0}, 104: {0, 0}, 106: {5, 4}}
	outs, err := f.RunAll(context.Background(), pipeline.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range pipelinetest.PRNumbers {
		o := outs[n]
		deep, fast, over := 0, 0, 0
		planned := 0.0
		for _, r := range o.Reviews {
			tier := r.Tier
			if r.EscalatedOf != nil { // counted by the tier it was routed to
				tier = *r.EscalatedOf
			}
			switch tier {
			case model.TierDeep:
				deep++
			case model.TierFast:
				fast++
			}
			if r.Tier != model.TierNone && r.Status == model.ReviewQueued {
				over++
			}
			planned += r.EstCost
		}
		t.Logf("#%d: %d deep, %d fast routed, %d over the cap, $%.4f planned of $%.2f", n, deep, fast, over, planned, o.Agg.Review.CapUSD)
		if w := want[n]; deep != w.deep || fast != w.fast {
			t.Errorf("#%d: %d deep and %d fast groups routed, want %d and %d", n, deep, fast, w.deep, w.fast)
		}
		if over != 0 || o.Agg.Review.OverCap != 0 || planned > 0.40 {
			t.Errorf("#%d: %d groups queued over the cap (stats %d), $%.4f planned", n, over, o.Agg.Review.OverCap, planned)
		}
	}
}
