package aggregate

import (
	"math"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

func contrib(terms []model.UrgencyTerm, name string) (float64, bool) {
	for _, t := range terms {
		if t.Name == name {
			return t.Contrib, true
		}
	}
	return 0, false
}

func TestItemUrgency(t *testing.T) {
	w := config.Defaults().Rank
	units := []model.ChangeUnit{{Adds: 30, Dels: 10}, {Adds: 5, Dels: 5}} // 50 lines → 5 minutes
	tests := []struct {
		name    string
		in      Input
		want    float64
		why     string
		absent  []string
		present []string
	}{
		{
			name: "requested release PR",
			in: Input{Item: model.Item{ID: "pr:1", Kind: model.KindPR, ReviewRequested: true},
				Units: units, Weights: w, Release: true, AgeDays: math.E - 1},
			want:    3.0 + 2.0 + 0.4 - 0.02*5,
			why:     "asked you · release · age",
			absent:  []string{"w_unpushed"},
			present: []string{"w_requested", "w_release", "w_ci_red", "w_breaking"},
		},
		{
			name: "CI red after approval",
			in: Input{Item: model.Item{ID: "pr:2", Kind: model.KindPR}, Units: units, Weights: w,
				CIRedAfterApproval: true},
			want: 2.5 - 0.1,
			why:  "CI red after approval",
		},
		{
			name: "branch with unpushed commits",
			in: Input{Item: model.Item{ID: "branch:x", Kind: model.KindBranch, Unpushed: 2, ReviewRequested: true},
				Units: units, Weights: w, Release: true},
			want:    0.3*2 - 0.1,
			why:     "unpushed",
			absent:  []string{"w_requested", "w_release", "w_ci_red"},
			present: []string{"w_unpushed", "w_age", "w_minutes"},
		},
		{
			name: "nothing positive",
			in:   Input{Item: model.Item{ID: "pr:3", Kind: model.KindPR}, Units: units, Weights: w},
			want: -0.1,
			why:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agg := Item(tc.in)
			if agg.Added != 35 || agg.Removed != 15 || agg.UnitsTotal != 2 || agg.Minutes != 5 {
				t.Fatalf("counts = %+v", agg)
			}
			if math.Abs(agg.Urgency-tc.want) > 1e-9 {
				t.Errorf("urgency = %v, want %v (terms %+v)", agg.Urgency, tc.want, agg.UrgencyTerms)
			}
			if agg.Why != tc.why {
				t.Errorf("why = %q, want %q", agg.Why, tc.why)
			}
			for _, n := range tc.absent {
				if _, ok := contrib(agg.UrgencyTerms, n); ok {
					t.Errorf("term %s should be absent", n)
				}
			}
			for _, n := range tc.present {
				if _, ok := contrib(agg.UrgencyTerms, n); !ok {
					t.Errorf("term %s missing", n)
				}
			}
			if c, _ := contrib(agg.UrgencyTerms, "w_minutes"); c > 0 {
				t.Errorf("minutes contribution must be negative, got %v", c)
			}
		})
	}
}

func TestWhyTopThree(t *testing.T) {
	terms := []model.UrgencyTerm{
		{Words: "a", Contrib: 1}, {Words: "b", Contrib: 3}, {Words: "c", Contrib: 2},
		{Words: "d", Contrib: 0.5}, {Words: "e", Contrib: -4}, {Words: "f", Contrib: 2},
	}
	if got := Why(terms); got != "b · c · f" {
		t.Fatalf("why = %q", got)
	}
}

func TestFacts(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	globs := []string{"release/*", "hotfix/**"}
	tests := []struct {
		name    string
		it      model.Item
		age     float64
		release bool
		ciRed   bool
	}{
		{"created two days ago", model.Item{CreatedAt: now.Add(-48 * time.Hour), BaseRef: "main"}, 2, false, false},
		{"falls back to updated", model.Item{UpdatedAt: now.Add(-24 * time.Hour), BaseRef: "release/1.2"}, 1, true, false},
		{"future date clamps", model.Item{CreatedAt: now.Add(time.Hour), BaseRef: "hotfix/a/b"}, 0, true, false},
		{"approved and red", model.Item{ViewerApproved: true, CI: model.CIRed}, 0, false, true},
		{"red but not approved", model.Item{CI: model.CIRed}, 0, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			age, rel, red := Facts(tc.it, globs, now)
			if math.Abs(age-tc.age) > 1e-9 || rel != tc.release || red != tc.ciRed {
				t.Fatalf("Facts = %v %v %v, want %v %v %v", age, rel, red, tc.age, tc.release, tc.ciRed)
			}
		})
	}
}
