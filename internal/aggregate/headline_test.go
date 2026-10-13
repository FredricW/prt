package aggregate

import (
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// headlineUnit builds unit facts for the headline rules from short labels
// (unit id "u") at path, with the aggregate facts Item would derive.
func headlineUnit(path string, kind model.UnitKind, ls ...shortLabel) unitFacts {
	return headlineUnitID("u", path, kind, ls...)
}

// headlineUnitID is headlineUnit for unit id.
func headlineUnitID(id, path string, kind model.UnitKind, ls ...shortLabel) unitFacts {
	u := model.ChangeUnit{ID: id, Path: path, Kind: kind, Diff: "+a\n+b", Adds: 2, Range: model.Range{New: model.Span{Start: 1, Count: 2}}}
	all := make([]model.Label, 0, len(ls))
	for _, s := range ls {
		if s.U == "" {
			s.U = id
		}
		all = append(all, s.label())
	}
	ul := newIndex("pr:1", all, []model.ChangeUnit{u}).units[id]
	return unitFacts{unit: u, agg: unitAgg(u, ul, false, true, defaultUnsureBelow), labels: ul, line: 1}
}

func TestHeadlineRules(t *testing.T) {
	feature := shortLabel{Q: "kind", V: "feature", P: 0.9}
	behaves := shortLabel{Q: "behaviour", P: 0.9}
	tests := []struct {
		name  string
		f     unitFacts
		kinds []string
		cand  string // the candidate's kind, "" for none
	}{
		{"breaking at the threshold", headlineUnit("a.go", model.UnitModified, feature, shortLabel{Q: "breaking", P: 0.6}),
			[]string{headlineBreaking}, ""},
		{"breaking below the threshold is unsure", headlineUnit("a.go", model.UnitModified, feature, shortLabel{Q: "breaking", P: 0.55}),
			nil, headlineBreaking},
		{"breaking below a half is no candidate", headlineUnit("a.go", model.UnitModified, feature, shortLabel{Q: "breaking", P: 0.45}),
			nil, ""},
		{"contract by its own P", headlineUnit("a.go", model.UnitModified, feature, shortLabel{Q: "contract", V: "api shape", P: 0.62}),
			[]string{"api shape"}, ""},
		{"contract not none but unlikely", headlineUnit("a.go", model.UnitModified, feature, shortLabel{Q: "contract", V: "api shape", P: 0.45}),
			nil, ""},
		{"breaking subsumes the contract claim", headlineUnit("a.go", model.UnitModified, feature,
			shortLabel{Q: "breaking", P: 0.8}, shortLabel{Q: "contract", V: "api shape", P: 0.9}), []string{headlineBreaking}, ""},
		// The unit is listed through its contract row, so its unsure
		// breaking claim is no separate candidate (no double count).
		{"unsure breaking leaves the contract row", headlineUnit("a.go", model.UnitModified, feature,
			shortLabel{Q: "breaking", P: 0.55}, shortLabel{Q: "contract", V: "schema/migration", P: 0.9}), []string{"schema/migration"}, ""},
		{"an additive signature is no headline", headlineUnit("a.go", model.UnitModified, feature,
			shortLabel{Q: "breaking", P: 0.2}, shortLabel{Q: "contract", V: "signature", P: 0.95}), nil, ""},
		{"nor a candidate", headlineUnit("a.go", model.UnitModified, feature, shortLabel{Q: "contract", V: "signature", P: 0.55}),
			nil, ""},
		{"contract candidate", headlineUnit("a.go", model.UnitModified, feature, shortLabel{Q: "contract", V: "env/flag", P: 0.58}),
			nil, "env/flag"},
		{"one candidate per unit, the most likely", headlineUnit("a.go", model.UnitModified, feature,
			shortLabel{Q: "breaking", P: 0.52}, shortLabel{Q: "contract", V: "api shape", P: 0.57}), nil, "api shape"},
		{"contract none", headlineUnit("a.go", model.UnitModified, feature, shortLabel{Q: "contract", V: "none", P: 0.95}),
			nil, ""},
		{"test file cannot break callers", headlineUnit("web/src/__tests__/a.test.ts", model.UnitModified, feature,
			shortLabel{Q: "breaking", P: 0.9}, shortLabel{Q: "contract", V: "signature", P: 0.9}), nil, ""},
		{"kind test cannot break callers", headlineUnit("web/src/helpers.ts", model.UnitModified, shortLabel{Q: "kind", V: "test", P: 0.9},
			shortLabel{Q: "breaking", P: 0.9}), nil, ""},
		{"dependency bump is not a contract", headlineUnit("go.mod", model.UnitModified, shortLabel{Q: "kind", V: "dependency", P: 0.9},
			shortLabel{Q: "breaking", P: 0.7}, shortLabel{Q: "contract", V: "signature", P: 0.7}), nil, ""},
		{"a verbatim move keeps the contract", headlineUnit("a.go", model.UnitMoved, feature,
			shortLabel{Q: "breaking", P: 0.9}, shortLabel{Q: "contract", V: "signature", P: 0.9}), nil, ""},
		{"generated output follows its source", headlineUnit("gen.go", model.UnitModified, shortLabel{Q: "kind", V: "generated", P: 0.9},
			shortLabel{Q: "breaking", P: 0.9}), nil, ""},
		{"test weakened in a test file", headlineUnit("a_test.go", model.UnitModified, shortLabel{Q: "kind", V: "test", P: 0.9},
			shortLabel{Q: "test_weakened", P: 0.8}), []string{headlineTestWeakened}, ""},
		{"test weakened candidate", headlineUnit("a_test.go", model.UnitModified, shortLabel{Q: "test_weakened", P: 0.52}), nil, headlineTestWeakened},
		{"pii in a unit that changes behaviour", headlineUnit("a.go", model.UnitModified, feature, behaves,
			shortLabel{Q: "pii_or_secret", L: 2, P: 0.7}), []string{headlinePII}, ""},
		{"pii in a rename is not new exposure", headlineUnit("a.go", model.UnitModified, feature, shortLabel{Q: "behaviour", P: 0.1},
			shortLabel{Q: "pii_or_secret", L: 2, P: 0.7}), nil, ""},
		{"pii in test data", headlineUnit("a_test.go", model.UnitModified, behaves, shortLabel{Q: "pii_or_secret", L: 1, P: 0.65}),
			[]string{headlinePII}, ""},
		{"pii candidate", headlineUnit("a.go", model.UnitModified, feature, behaves, shortLabel{Q: "pii_or_secret", L: 1, P: 0.55}),
			nil, headlinePII},
		// A unit listed for one claim offers no candidate for another.
		{"a listed unit has no candidate", headlineUnit("a.go", model.UnitModified, feature, behaves,
			shortLabel{Q: "breaking", P: 0.8}, shortLabel{Q: "pii_or_secret", L: 1, P: 0.55}), []string{headlineBreaking}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, cand := unitHeadlines(tc.f)
			var kinds []string
			for _, h := range rows {
				kinds = append(kinds, h.Kind)
				if h.P < headlineMinP {
					t.Errorf("row %+v below the threshold", h)
				}
			}
			assertEqual(t, "kinds", kinds, tc.kinds)
			got := ""
			if cand != nil {
				got = cand.Kind
				if cand.P < headlineCandidateP || cand.P >= headlineMinP || cand.UnitID != "u" {
					t.Errorf("candidate %+v: want unit u and P in [.50, .60)", *cand)
				}
			}
			if got != tc.cand {
				t.Errorf("candidate = %q, want %q", got, tc.cand)
			}
		})
	}
}

// TestHeadlineCandidatesMerged: external-contract candidates are one per
// file and kind, and none for a file and kind a row already lists.
func TestHeadlineCandidatesMerged(t *testing.T) {
	feature := shortLabel{Q: "kind", V: "feature", P: 0.9}
	us := []unitFacts{
		headlineUnitID("a", "api.go", model.UnitModified, feature, shortLabel{Q: "contract", V: "api shape", P: 0.8}),
		headlineUnitID("b", "api.go", model.UnitModified, feature, shortLabel{Q: "contract", V: "api shape", P: 0.55}),
		headlineUnitID("c", "cfg.go", model.UnitModified, feature, shortLabel{Q: "contract", V: "env/flag", P: 0.52}),
		headlineUnitID("d", "cfg.go", model.UnitModified, feature, shortLabel{Q: "contract", V: "env/flag", P: 0.58}),
		headlineUnitID("e", "x.go", model.UnitModified, feature, shortLabel{Q: "breaking", P: 0.51}),
	}
	rows, cands := headlines(us)
	var got, gotRows []string
	for _, h := range cands {
		got = append(got, h.UnitID)
	}
	for _, h := range rows {
		gotRows = append(gotRows, h.UnitID)
	}
	assertEqual(t, "rows", gotRows, []string{"a"})
	// e (breaking ranks first), then cfg.go's most likely env/flag; b is
	// covered by api.go's api shape row.
	assertEqual(t, "candidates", got, []string{"e", "d"})
}

func TestMergeContracts(t *testing.T) {
	rows := []model.Headline{
		{UnitID: "a", Path: "api.go", Kind: "api shape", P: 0.7},
		{UnitID: "b", Path: "api.go", Kind: "breaking", P: 0.9},
		{UnitID: "c", Path: "api.go", Kind: "api shape", P: 0.9},
		{UnitID: "d", Path: "api.go", Kind: "env/flag", P: 0.65},
		{UnitID: "e", Path: "other.go", Kind: "api shape", P: 0.6},
		{UnitID: "f", Path: "api.go", Kind: "breaking", P: 0.7},
		{UnitID: "g", Path: "api.go", Kind: "api shape", P: 0.9},
	}
	var got []string
	for _, h := range mergeContracts(rows) {
		got = append(got, h.UnitID)
	}
	// One api shape row for api.go (the most likely, earliest on ties), its
	// env/flag row, other.go's row; breaking rows stay one per unit.
	assertEqual(t, "merged", got, []string{"c", "b", "d", "e", "f"})
}

func TestInterfaceHeadlineNeedsEyes(t *testing.T) {
	feature := shortLabel{Q: "kind", V: "feature", P: 0.9}
	skim := shortLabel{Q: "depth", V: "skim", P: 0.9}
	tests := []struct {
		name string
		f    unitFacts
		want model.Attention
	}{
		{"breaking unit read whatever its depth", headlineUnit("a.go", model.UnitModified, feature, skim, shortLabel{Q: "breaking", P: 0.7}), model.AttnEyes},
		{"api shape unit", headlineUnit("a.go", model.UnitModified, feature, skim, shortLabel{Q: "contract", V: "api shape", P: 0.8}), model.AttnEyes},
		{"additive signature stays skim", headlineUnit("a.go", model.UnitModified, feature, skim, shortLabel{Q: "contract", V: "signature", P: 0.9}), model.AttnSkim},
		{"unsure breaking stays skim", headlineUnit("a.go", model.UnitModified, feature, skim, shortLabel{Q: "breaking", P: 0.55}), model.AttnSkim},
		{"test file stays skim", headlineUnit("a_test.go", model.UnitModified, feature, skim, shortLabel{Q: "breaking", P: 0.9}), model.AttnSkim},
	}
	for _, tc := range tests {
		if got := tc.f.agg.Attention; got != tc.want {
			t.Errorf("%s: attention %s, want %s", tc.name, got, tc.want)
		}
	}
}

// TestHeadlinesUnsureCounted: Item reports the left-out candidates, and a
// unit listed through another claim is not counted again.
func TestHeadlinesUnsureCounted(t *testing.T) {
	in := loadFixture(t, "profile.json")
	set := func(q string, p float64) {
		for i, l := range in.Labels {
			if l.UnitID == "u4" && l.Question == q {
				in.Labels[i].P, in.Labels[i].Confidence = p, p
			}
		}
	}
	// u4's breaking answer drops to .55, but its api shape claim (.75)
	// still lists it: no candidate.
	set("breaking", 0.55)
	if agg := Item(in); agg.HeadlinesUnsure != 0 || len(agg.HeadlineCandidates) != 0 {
		t.Fatalf("listed unit counted as unsure: %d %+v", agg.HeadlinesUnsure, agg.HeadlineCandidates)
	}
	set("contract", 0.45)
	agg := Item(in)
	if agg.HeadlinesUnsure != 1 || len(agg.HeadlineCandidates) != 1 {
		t.Fatalf("HeadlinesUnsure = %d, candidates %+v, want 1", agg.HeadlinesUnsure, agg.HeadlineCandidates)
	}
	if c := agg.HeadlineCandidates[0]; c.UnitID != "u4" || c.Kind != headlineBreaking || c.P != 0.55 {
		t.Errorf("candidate %+v, want u4 breaking .55", c)
	}
	for _, h := range agg.Headlines {
		if h.UnitID == "u4" && h.Kind == headlineBreaking {
			t.Errorf("u4 breaking at .55 listed: %+v", h)
		}
	}
}
