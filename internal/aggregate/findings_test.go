package aggregate

import (
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func finding(unit string, line int, sev model.Severity, claim string) model.Finding {
	return model.Finding{UnitID: unit, Line: line, Severity: sev, Claim: claim}
}

func TestHotSpotReasonFromFindings(t *testing.T) {
	in := loadFixture(t, "profile.json")
	in.Reviews = []model.Review{
		{GroupID: "g1", Findings: []model.Finding{
			finding("u1", 12, model.SevLow, "minor nit about naming here"),
			finding("u1", 11, model.SevHigh, "Cache never expires, so stale profiles leak across sessions"),
			finding("u3", 43, model.SevMedium, "First row wins: logout drops errors silently"),
			finding("u4", 9, model.SevMedium, "later line in the same row"),
			finding("u4", 8, model.SevMedium, "Earlier line wins, five words: cut"),
			finding("", 1, model.SevHigh, "no unit, ignored"),
		}},
		{GroupID: "g2", Findings: []model.Finding{
			finding("u3", 41, model.SevMedium, "second row loses the tie"),
			finding("u1", 10, model.SevHigh, "second row high loses too"),
		}},
	}
	agg := Item(in)
	want := map[string]string{
		"u1": "Cache never expires, so stale", // high beats low; first row beats second
		"u3": "First row wins: logout drops",  // equal severity: row order before line
		"u4": "Earlier line wins, five words", // same row: lower line; trailing ":" trimmed
	}
	if len(agg.HotSpots) != 3 {
		t.Fatalf("hot spots = %+v", agg.HotSpots)
	}
	for _, h := range agg.HotSpots {
		if h.Reason != want[h.UnitID] {
			t.Errorf("%s reason = %q, want %q", h.UnitID, h.Reason, want[h.UnitID])
		}
	}
	// Scores are unchanged by findings.
	if !near(agg.HotSpots[0].Score, model.UnitHotSpot(unitByID(t, agg, "u1"))) {
		t.Errorf("score = %v", agg.HotSpots[0].Score)
	}
	if g := agg.Groups[0]; g.Findings != 6 {
		t.Errorf("g1 findings = %d, want 6 (its routing row)", g.Findings)
	}
}

func TestFindingReasons(t *testing.T) {
	tests := []struct {
		name    string
		reviews []model.Review
		want    map[string]string
	}{
		{"none", nil, map[string]string{}},
		{"unknown severity ranks below low", []model.Review{{Findings: []model.Finding{
			finding("u", 1, "", "unranked claim"),
			finding("u", 2, model.SevLow, "low claim wins"),
		}}}, map[string]string{"u": "low claim wins"}},
		{"empty claim falls back", []model.Review{{Findings: []model.Finding{
			finding("u", 1, model.SevHigh, "   "),
		}}}, map[string]string{}},
		{"five-word cut", []model.Review{{Findings: []model.Finding{
			finding("u", 1, model.SevHigh, "one two three four five, six seven"),
		}}}, map[string]string{"u": "one two three four five"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertEqual(t, "reasons", findingReasons(tc.reviews), tc.want)
		})
	}
}

func TestHotSpotReasonFallsBackToLabels(t *testing.T) {
	in := loadFixture(t, "profile.json")
	in.Reviews = []model.Review{{GroupID: "g1", Findings: []model.Finding{
		finding("u1", 10, model.SevHigh, "Cache key ignores tenant id"),
	}}}
	agg := Item(in)
	want := map[string]string{
		"u1": "Cache key ignores tenant id",
		"u3": "auth, crash, untested", // no finding: label reason
		"u4": "risk 3 of 5",
	}
	for _, h := range agg.HotSpots {
		if h.Reason != want[h.UnitID] {
			t.Errorf("%s reason = %q, want %q", h.UnitID, h.Reason, want[h.UnitID])
		}
	}
}

func kindFacts(kind string, uk model.UnitKind, format bool) unitFacts {
	return unitFacts{unit: model.ChangeUnit{Kind: uk, Format: format}, agg: model.UnitAgg{Kind: kind}}
}

func TestGroupKind(t *testing.T) {
	feature := kindFacts("feature", model.UnitModified, false)
	tests := []struct {
		name    string
		members []unitFacts
		want    string
	}{
		{"uniform", []unitFacts{feature, feature}, "feature"},
		{"moved", []unitFacts{kindFacts("", model.UnitMoved, false), kindFacts("refactor", model.UnitMoved, false)}, "moved"},
		{"formatting by parse and kind", []unitFacts{
			kindFacts("", model.UnitModified, true), kindFacts(model.KindFormatting, model.UnitModified, false),
		}, model.KindFormatting},
		{"unknown label", []unitFacts{feature, kindFacts(model.LabelUnknown, model.UnitModified, false)}, "unknown"},
		{"missing label", []unitFacts{feature, kindFacts("", model.UnitModified, false)}, "unknown"},
		{"unknown beats mixed", []unitFacts{feature, kindFacts("test", model.UnitAdded, false), kindFacts("", model.UnitAdded, false)}, "unknown"},
		{"mixed", []unitFacts{feature, kindFacts("test", model.UnitAdded, false)}, "mixed"},
		{"moved with feature", []unitFacts{feature, kindFacts("", model.UnitMoved, false)}, "mixed"},
		{"empty", nil, "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := groupKind(tc.members); got != tc.want {
				t.Errorf("groupKind = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGroupHotSpotAndFindings(t *testing.T) {
	in := loadFixture(t, "profile.json")
	in.Groups = append(in.Groups, model.Group{ID: "g2", UnitIDs: []string{"u5", "u6"}})
	in.Reviews = []model.Review{
		{GroupID: "g2", Findings: []model.Finding{finding("u5", 3, model.SevLow, "typo")}},
		{GroupID: "g1", Findings: []model.Finding{finding("u1", 10, model.SevHigh, "a"), finding("u4", 7, model.SevLow, "b")}},
		{GroupID: "g1", Findings: []model.Finding{finding("u1", 11, model.SevHigh, "duplicate row ignored")}},
	}
	agg := Item(in)
	if len(agg.Groups) != 2 {
		t.Fatalf("groups = %+v", agg.Groups)
	}
	g1, g2 := agg.Groups[0], agg.Groups[1]
	u1, u4 := unitByID(t, agg, "u1"), unitByID(t, agg, "u4")
	if want := max(model.UnitHotSpot(u1), model.UnitHotSpot(u4)); !near(g1.HotSpot, want) || want <= 0 {
		t.Errorf("g1 hot spot = %v, want %v", g1.HotSpot, want)
	}
	if g1.Kind != "feature" || g1.Findings != 2 {
		t.Errorf("g1 kind %q findings %d, want feature 2", g1.Kind, g1.Findings)
	}
	// u5 docs (relevance 1, risk 1, covered 0.05) and u6 moved without labels.
	if want := model.UnitHotSpot(unitByID(t, agg, "u5")); !near(g2.HotSpot, want) {
		t.Errorf("g2 hot spot = %v, want %v", g2.HotSpot, want)
	}
	if g2.Kind != "mixed" || g2.Findings != 1 {
		t.Errorf("g2 kind %q findings %d, want mixed 1", g2.Kind, g2.Findings)
	}
}
