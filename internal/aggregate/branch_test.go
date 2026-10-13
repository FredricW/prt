package aggregate

import (
	"math"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

// The ready-for-PR staleness bonus: w_ready_stale × log1p(idle days), for
// branches whose state is ready for PR only, read from Prior.Branch in
// Urgency and from Input.Branch (the record being built) in Item.
func TestUrgencyReadyStale(t *testing.T) {
	w := config.Defaults().Rank
	branch := model.Item{ID: "branch:x", Kind: model.KindBranch, Unpushed: 2}
	pr := model.Item{ID: "pr:1", Kind: model.KindPR}
	state := func(s string) *model.BranchInfo { return &model.BranchInfo{State: s, Known: true} }
	tests := []struct {
		name   string
		item   model.Item
		branch *model.BranchInfo
		idle   float64
		want   float64 // value; -1 = term absent
		words  string
	}{
		{"ready, idle 10", branch, state(model.BranchReady), 10, math.Log1p(10), "ready, waiting"},
		{"ready, idle 0", branch, state(model.BranchReady), 0, 0, "ready, waiting"},
		{"ready, negative idle", branch, state(model.BranchReady), -3, 0, "ready, waiting"},
		{"WIP", branch, state(model.BranchWIP), 10, -1, ""},
		{"abandoned", branch, state(model.BranchAbandoned), 30, -1, ""},
		{"no branch block", branch, nil, 10, -1, ""},
		{"PR never", pr, state(model.BranchReady), 10, -1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var prior *model.ItemAgg
			if tc.branch != nil {
				prior = &model.ItemAgg{Branch: tc.branch}
			}
			viaPrior, _, _ := Urgency(Input{Item: tc.item, Weights: w, IdleDays: tc.idle, Prior: prior}, 0)
			viaItem := Item(Input{Item: tc.item, Weights: w, IdleDays: tc.idle, Branch: tc.branch}).UrgencyTerms
			for path, terms := range map[string][]model.UrgencyTerm{"Urgency": viaPrior, "Item": viaItem} {
				var term *model.UrgencyTerm
				for i := range terms {
					if terms[i].Name == "w_ready_stale" {
						term = &terms[i]
					}
				}
				if tc.want < 0 {
					if term != nil {
						t.Errorf("%s: term present: %+v", path, *term)
					}
					continue
				}
				if term == nil {
					t.Fatalf("%s: term missing", path)
				}
				if !near(term.Value, tc.want) || !near(term.Contrib, w.WReadyStale*tc.want) || term.Words != tc.words {
					t.Errorf("%s: term %+v, want value %v", path, *term, tc.want)
				}
			}
			if _, ok := contrib(viaPrior, "w_unpushed"); ok == (tc.item.Kind == model.KindPR) {
				t.Errorf("w_unpushed presence wrong for %s", tc.item.Kind)
			}
		})
	}
}

func TestItemCopiesBranch(t *testing.T) {
	b := &model.BranchInfo{State: model.BranchSpike, P: 0.7, Known: true}
	agg := Item(Input{Item: model.Item{ID: "branch:x", Kind: model.KindBranch}, Branch: b})
	if agg.Branch != b {
		t.Errorf("Branch not copied")
	}
}

func TestIdleDays(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		last time.Time
		want float64
	}{
		{time.Time{}, 0},
		{now.Add(time.Hour), 0},
		{now.Add(-36 * time.Hour), 1.5},
		{now.AddDate(0, 0, -14), 14},
	} {
		if got := IdleDays(tc.last, now); !near(got, tc.want) {
			t.Errorf("IdleDays(%v) = %v, want %v", tc.last, got, tc.want)
		}
	}
}
