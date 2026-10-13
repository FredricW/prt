package aggregate

import (
	"reflect"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

// termValue returns the raw value of the named urgency term.
func termValue(terms []model.UrgencyTerm, name string) (float64, bool) {
	for _, t := range terms {
		if t.Name == name {
			return t.Value, true
		}
	}
	return 0, false
}

// breakingUnits returns n units labelled breaking.
func breakingUnits(n int) []model.UnitAgg {
	out := make([]model.UnitAgg, n)
	for i := range out {
		out[i] = model.UnitAgg{Breaking: true}
	}
	return out
}

func TestUrgencyLabelTerms(t *testing.T) {
	w := config.Defaults().Rank
	pr := model.Item{ID: "pr:1", Kind: model.KindPR}
	branch := model.Item{ID: "branch:x", Kind: model.KindBranch}
	prior := &model.ItemAgg{
		RiskMax: 4, TestGap: 2,
		Units: []model.UnitAgg{
			{Breaking: true, Relevance: 2.5},
			{Relevance: 3.5},
			{Noise: true, Breaking: true, Relevance: 5}, // noise: relevance ignored, breaking counted
			{BreakingUnknown: true},                     // unknown: claims nothing
		},
	}
	type want struct{ breaking, risk, testGap, relevance float64 }
	tests := []struct {
		name    string
		item    model.Item
		prior   *model.ItemAgg
		want    want
		absent  []string
		present []string
	}{
		{"PR with prior", pr, prior, want{2, 4, 2, 3.5}, []string{"w_unpushed"}, []string{"w_requested", "w_release", "w_ci_red"}},
		{"branch with prior", branch, prior, want{2, 4, 2, 3.5}, []string{"w_requested", "w_release", "w_ci_red"}, []string{"w_unpushed"}},
		{"no prior", pr, nil, want{}, nil, nil},
		{"unknown labels", pr, &model.ItemAgg{Units: []model.UnitAgg{{BreakingUnknown: true, CoveredUnknown: true}}}, want{}, nil, nil},
		{"breaking bound", pr, &model.ItemAgg{Units: breakingUnits(35)}, want{breaking: maxBreakingTerm}, nil, nil},
		{"breaking below bound", pr, &model.ItemAgg{Units: breakingUnits(2)}, want{breaking: 2}, nil, nil},
		{"only noise relevance", branch, &model.ItemAgg{Units: []model.UnitAgg{{Noise: true, Relevance: 4}}}, want{}, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			terms, total, _ := Urgency(Input{Item: tc.item, Weights: w, Prior: tc.prior}, 0)
			got := map[string]float64{}
			for name, wantV := range map[string]float64{
				"w_breaking": tc.want.breaking, "w_risk": tc.want.risk,
				"w_test_gap": tc.want.testGap, "w_relevance": tc.want.relevance,
			} {
				v, ok := termValue(terms, name)
				if !ok {
					t.Fatalf("term %s missing", name)
				}
				if !near(v, wantV) {
					t.Errorf("%s = %v, want %v", name, v, wantV)
				}
				got[name] = v
			}
			wantTotal := w.WBreaking*got["w_breaking"] + w.WRisk*got["w_risk"] +
				w.WTestGap*got["w_test_gap"] + w.WRelevance*got["w_relevance"]
			if !near(total, wantTotal) {
				t.Errorf("total = %v, want %v", total, wantTotal)
			}
			for _, n := range tc.absent {
				if _, ok := contrib(terms, n); ok {
					t.Errorf("term %s should be absent", n)
				}
			}
			for _, n := range tc.present {
				if _, ok := contrib(terms, n); !ok {
					t.Errorf("term %s missing", n)
				}
			}
		})
	}
}

// The Prior path: discovery's Urgency on the stored record gives the same
// terms, total and why as Item computed for that record.
func TestUrgencyPriorMatchesItem(t *testing.T) {
	in := loadFixture(t, "profile.json")
	in.AgeDays = 3
	agg := Item(in)
	in.Prior = &agg
	terms, total, why := Urgency(in, agg.Minutes)
	if !reflect.DeepEqual(terms, agg.UrgencyTerms) || !near(total, agg.Urgency) || why != agg.Why {
		t.Fatalf("Urgency(prior) = %+v %v %q, Item = %+v %v %q", terms, total, why, agg.UrgencyTerms, agg.Urgency, agg.Why)
	}
}

// Profile fixture: u3 and u4 breaking, risk max 5, two untested risky
// units, max relevance 4.6 (u1). Not requested, age 0.
func TestProfileUrgency(t *testing.T) {
	agg := Item(loadFixture(t, "profile.json"))
	for name, want := range map[string]float64{
		"w_breaking": 2.0 * 2, "w_risk": 0.8 * 5, "w_test_gap": 0.6 * 2, "w_relevance": 0.5 * 4.6,
		"w_requested": 0, "w_minutes": -0.02 * 6.8,
	} {
		if c, _ := contrib(agg.UrgencyTerms, name); !near(c, want) {
			t.Errorf("%s contribution = %v, want %v", name, c, want)
		}
	}
	if want := 4.0 + 4.0 + 1.2 + 2.3 - 0.136; !near(agg.Urgency, want) {
		t.Errorf("urgency = %v, want %v", agg.Urgency, want)
	}
	// breaking and risk tie at 4.0: term order keeps breaking first.
	if agg.Why != "breaking · risk · relevant to you" {
		t.Errorf("why = %q", agg.Why)
	}
}

func TestWhyWords(t *testing.T) {
	tests := []struct {
		name  string
		terms []model.UrgencyTerm
		want  string
	}{
		{"top three by contribution", []model.UrgencyTerm{
			{Words: "asked you", Contrib: 3}, {Words: "breaking", Contrib: 6}, {Words: "risk", Contrib: 4},
			{Words: "untested", Contrib: 1.2}, {Words: "size", Contrib: -0.5},
		}, "breaking · risk · asked you"},
		{"fewer than three positive", []model.UrgencyTerm{
			{Words: "release", Contrib: 2}, {Words: "age", Contrib: 0}, {Words: "size", Contrib: -1},
		}, "release"},
		{"none positive", []model.UrgencyTerm{{Words: "size", Contrib: -1}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Why(tc.terms); got != tc.want {
				t.Errorf("why = %q, want %q", got, tc.want)
			}
		})
	}
}

// labelled builds a short label for an ad-hoc input.
func labelled(u, q, v string, p, s float64) model.Label {
	return shortLabel{U: u, Q: q, V: v, P: p, S: s}.label()
}

// A requested, breaking, risk-5, untested PR outranks a dependency bump with
// green CI and a mechanical rename of unknown (0) risk, all else equal.
func TestUrgencyOrdering(t *testing.T) {
	w := config.Defaults().Rank
	risky := Input{
		Item:    model.Item{ID: "pr:1", Kind: model.KindPR, ReviewRequested: true, CI: model.CIGreen},
		Units:   []model.ChangeUnit{{ID: "a1", Path: "server/session.go", Adds: 8, Dels: 4}},
		Weights: w, AgeDays: 2,
		Labels: []model.Label{
			labelled("a1", model.QKind, "feature", 0.9, 0),
			labelled("a1", model.QBreaking, "", 0.9, 0),
			labelled("a1", model.QRisk, "5", 0.8, 5),
			labelled("a1", model.QCovered, "", 0.1, 0),
			labelled("a1", model.QRelevance, "3", 0.8, 3),
			labelled("a1", model.QDepth, "read", 0.9, 0),
		},
	}
	bump := func(requested bool) Input {
		in := Input{
			Item: model.Item{ID: "pr:2", Kind: model.KindPR, ReviewRequested: requested, CI: model.CIGreen},
			Units: []model.ChangeUnit{
				{ID: "b1", Path: "go.mod", Adds: 2, Dels: 2},
				{ID: "b2", Path: "a/x.go", Adds: 2, Dels: 2},
				{ID: "b3", Path: "b/y.go", Adds: 2, Dels: 2},
			},
			Groups:  []model.Group{{ID: "g", UnitIDs: []string{"b2", "b3"}, Mechanical: true}},
			Weights: w, AgeDays: 2,
		}
		for _, id := range []string{"b1", "b2", "b3"} {
			kind := "refactor"
			if id == "b1" {
				kind = model.KindDependency
			}
			in.Labels = append(in.Labels,
				labelled(id, model.QKind, kind, 0.9, 0),
				labelled(id, model.QBreaking, "", 0.05, 0),
				labelled(id, model.QCovered, "", 0.9, 0),
				labelled(id, model.QRelevance, "1", 0.8, 1),
				labelled(id, model.QDepth, "skim", 0.9, 0),
			)
		}
		return in
	}
	a := Item(risky)
	for _, requested := range []bool{false, true} {
		b := Item(bump(requested))
		if a.Urgency <= b.Urgency {
			t.Errorf("requested bump=%v: risky PR %v (%s) should outrank bump %v (%s)",
				requested, a.Urgency, a.Why, b.Urgency, b.Why)
		}
	}
	// risk 0.8×5 = 4.0, asked you 3.0, breaking 2.0×1, relevance 0.5×3, untested 0.6×1.
	if a.Why != "risk · asked you · breaking" {
		t.Errorf("risky why = %q", a.Why)
	}
}
