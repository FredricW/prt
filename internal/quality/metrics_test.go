package quality

import (
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// mkUnit builds a unit from a diff body ("+", "-" and " " lines) starting at
// the given old and new lines; Adds, Dels and the range counts follow the
// body.
func mkUnit(id, path string, oldStart, newStart int, body ...string) model.ChangeUnit {
	u := model.ChangeUnit{ID: id, Path: path, Diff: strings.Join(body, "\n")}
	oldN, newN := 0, 0
	for _, l := range body {
		switch {
		case strings.HasPrefix(l, "+"):
			u.Adds++
			newN++
		case strings.HasPrefix(l, "-"):
			u.Dels++
			oldN++
		default:
			oldN++
			newN++
		}
	}
	u.Range = model.Range{Old: model.Span{Start: oldStart, Count: oldN}, New: model.Span{Start: newStart, Count: newN}}
	return u
}

func loc(path string, first, last int, also ...string) Loc {
	return Loc{Path: path, Lines: []int{first, last}, AlsoAt: also}
}

func oldLoc(path string, first, last int) Loc {
	l := loc(path, first, last)
	l.Side = SideOld
	return l
}

func TestSpansOf(t *testing.T) {
	for name, tc := range map[string]struct {
		u            model.ChangeUnit
		newS, oldS   span
		hasN, hasOld bool
	}{
		"context excluded": {
			u:    mkUnit("u", "a.go", 10, 10, " c", " c", "+a", "-d", "+b", " c"),
			newS: span{"a.go", 12, 13}, oldS: span{"a.go", 12, 12}, hasN: true, hasOld: true,
		},
		"pure deletion in a modified unit uses the new range": {
			u:    mkUnit("u", "a.go", 5, 5, " c", "-d", "-d", " c"),
			newS: span{"a.go", 5, 6}, oldS: span{"a.go", 6, 7}, hasN: true, hasOld: true,
		},
		"deleted file has no new side": {
			u:    mkUnit("u", "a.go", 1, 0, "-x", "-y"),
			oldS: span{"a.go", 1, 2}, hasOld: true,
		},
		"added file has no old side": {
			u:    mkUnit("u", "a.go", 0, 1, "+x"),
			newS: span{"a.go", 1, 1}, hasN: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := spansOf(tc.u)
			if s.hasNew != tc.hasN || s.hasOld != tc.hasOld || (tc.hasN && s.new != tc.newS) || (tc.hasOld && s.old != tc.oldS) {
				t.Errorf("spans = %+v", s)
			}
		})
	}
}

func TestOverlaps(t *testing.T) {
	u := spansOf(mkUnit("u", "a.go", 20, 10, " c", "+x", "+y", "-z", " c")) // new 11-12, old 21-21
	for name, tc := range map[string]struct {
		l    Loc
		want bool
	}{
		"inside":                 {loc("a.go", 11, 11), true},
		"touches first line":     {loc("a.go", 5, 11), true},
		"touches last line":      {loc("a.go", 12, 30), true},
		"context line only":      {loc("a.go", 10, 10), false},
		"after":                  {loc("a.go", 13, 14), false},
		"other path":             {loc("b.go", 11, 12), false},
		"also_at matches":        {loc("b.go", 1, 2, "a.go:12-12"), true},
		"also_at other path":     {loc("b.go", 1, 2, "c.go:11-12"), false},
		"old side":               {oldLoc("a.go", 21, 21), true},
		"old side misses":        {oldLoc("a.go", 11, 12), false},
		"also_at takes the side": {func() Loc { l := oldLoc("b.go", 1, 1); l.AlsoAt = []string{"a.go:21"}; return l }(), true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := u.overlaps(regionOf(tc.l)); got != tc.want {
				t.Errorf("overlaps = %v, want %v", got, tc.want)
			}
		})
	}
}

// fixture is a small synthetic item: four units in two files.
func fixture() (Outcome, map[string]model.ChangeUnit) {
	units := []model.ChangeUnit{
		mkUnit("u1", "a.go", 1, 1, "+a1", "+a2", "+a3"),             // a.go new 1-3
		mkUnit("u2", "a.go", 10, 20, " c", "+b1", "+b2", " c"),      // a.go new 21-22
		mkUnit("u3", "b.go", 5, 0, "-d1", "-d2", "-d3", "-d4"),      // b.go old 5-8 (deleted)
		mkUnit("u4", "a_test.go", 0, 1, "+t1", "+t2", "+t3", "+t4"), // a_test.go new 1-4
	}
	byID := map[string]model.ChangeUnit{}
	for _, u := range units {
		byID[u.ID] = u
	}
	agg := model.ItemAgg{
		Added: 9, Removed: 4,
		Units: []model.UnitAgg{
			{UnitID: "u1", Attention: model.AttnEyes, Kind: "feature"},
			{UnitID: "u2", Attention: model.AttnSkim, Kind: "feature", Unsure: true},
			{UnitID: "u3", Attention: model.AttnEyes, Kind: "refactor"},
			{UnitID: "u4", Attention: model.AttnEyes, Kind: "test", Unsure: true},
		},
		UnsureUnits: []string{"u2", "u4"},
	}
	return Outcome{Units: units, Agg: agg}, byID
}

func TestEyesRecallAndPrecision(t *testing.T) {
	o, _ := fixture()
	ix := newIndex(o)
	tr := Truth{NeedsEyes: []NeedsEyes{
		{Loc: loc("a.go", 2, 2), Certainty: Firm},             // u1 eyes: hit
		{Loc: loc("a.go", 21, 21), Certainty: Firm},           // u2 skim: miss
		{Loc: oldLoc("b.go", 8, 9), Certainty: Firm},          // u3 eyes, old side: hit
		{Loc: loc("x.go", 1, 1, "a.go:3-3"), Certainty: Firm}, // also_at u1: hit
		{Loc: loc("c.go", 1, 1), Certainty: Soft},             // soft, unmatched: no miss
	}}
	if got := eyesRecall(tr, ix); got != (Ratio{3, 4}) {
		t.Errorf("recall = %+v, want 3/4", got)
	}
	// Eyes units u1, u3, u4: u1 and u3 overlap entries; u4 (a test) does not.
	if got := eyesPrecision(tr, ix); got != (Ratio{2, 3}) {
		t.Errorf("precision = %+v, want 2/3", got)
	}
	// A soft entry over u4 makes it a match, never a false positive.
	tr.NeedsEyes = append(tr.NeedsEyes, NeedsEyes{Loc: loc("a_test.go", 1, 1), Certainty: Soft})
	if got := eyesPrecision(tr, ix); got != (Ratio{3, 3}) {
		t.Errorf("precision with soft entry = %+v, want 3/3", got)
	}
	if got := eyesRecall(Truth{}, ix); got.Applies() {
		t.Errorf("recall without firm entries = %+v, want n/a", got)
	}
	o.Agg.Units = nil
	if got := eyesPrecision(tr, newIndex(o)); got.Applies() {
		t.Errorf("precision without eyes units = %+v, want n/a", got)
	}
}

func TestHeadlines(t *testing.T) {
	o, _ := fixture()
	ix := newIndex(o)
	tr := Truth{
		Headlines: []TruthHeadline{
			{Loc: loc("a.go", 1, 1), Certainty: Firm},
			{Loc: oldLoc("b.go", 6, 6), Certainty: Soft},
			{Loc: loc("z.go", 1, 1), Certainty: Firm}, // never matched
		},
		NotHeadlines: []NotHeadline{{Loc: loc("a.go", 1, 30)}, {Loc: loc("a_test.go", 1, 9)}},
	}
	hs := []model.Headline{
		{UnitID: "u1", Kind: "breaking"},                          // matches a firm headline (and a not_headline): ok
		{UnitID: "u1", Kind: "signature"},                         // same unit, second row: ok
		{UnitID: "u3", Kind: "signature"},                         // matches the soft headline: ok
		{UnitID: "u2", Kind: "api shape"},                         // only a not_headline: FP
		{UnitID: "u4", Kind: "breaking"},                          // a not_headline: FP
		{UnitID: "gone", Path: "q.go", Line: 3, Kind: "breaking"}, // unknown unit, nothing: FP
	}
	if got := headlinePrecision(tr, hs, ix); got != (Ratio{3, 6}) {
		t.Errorf("precision = %+v, want 3/6", got)
	}
	if got := headlineRecall(tr, hs, ix); got != (Ratio{1, 2}) {
		t.Errorf("recall = %+v, want 1/2 (soft entries never count)", got)
	}
	if got := headlinePrecision(tr, nil, ix); got.Applies() {
		t.Errorf("precision without headlines = %+v, want n/a", got)
	}
	// A headline whose unit is unknown is matched by its own path and line.
	tr.Headlines = append(tr.Headlines, TruthHeadline{Loc: loc("q.go", 3, 3), Certainty: Firm})
	if got := headlineRecall(tr, hs, ix); got != (Ratio{2, 3}) {
		t.Errorf("recall with path:line match = %+v, want 2/3", got)
	}
}

func TestHotSpotsTop3(t *testing.T) {
	o, _ := fixture()
	ix := newIndex(o)
	truth := func(ranks ...int) Truth {
		var tr Truth
		locs := []Loc{loc("a.go", 1, 1), loc("a.go", 21, 22), oldLoc("b.go", 5, 5), loc("a_test.go", 1, 1)}
		for i, r := range ranks {
			tr.HotSpots = append(tr.HotSpots, TruthHotSpot{Loc: locs[i], Rank: r, Certainty: Firm})
		}
		return tr
	}
	hs := func(ids ...string) []model.HotSpot {
		out := make([]model.HotSpot, len(ids))
		for i, id := range ids {
			out[i] = model.HotSpot{UnitID: id}
		}
		return out
	}
	for name, tc := range map[string]struct {
		tr   Truth
		hs   []model.HotSpot
		want Ratio
	}{
		"all three hit":                     {truth(1, 2, 3), hs("u3", "u1", "u2"), Ratio{3, 3}},
		"fourth slot ignored":               {truth(1, 2, 3), hs("u4", "u4", "u4", "u1"), Ratio{0, 3}},
		"rank 4 entry is not a target":      {truth(1, 2, 3, 4), hs("u4", "u1"), Ratio{1, 3}},
		"denominator is the ranked count":   {truth(1), hs("u1", "u2", "u3"), Ratio{1, 1}},
		"two slots on one entry are capped": {truth(1), hs("u1", "u1"), Ratio{1, 1}},
		"no truth hot spots":                {truth(), hs("u1"), Ratio{}},
		"pipeline has none":                 {truth(1, 2), nil, Ratio{0, 2}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := hotSpotsTop3(tc.tr, tc.hs, ix); got != tc.want {
				t.Errorf("hot spots = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFidelity(t *testing.T) {
	tr := Truth{Fidelity: Fidelity{Applicable: true, Bullets: []BulletTruth{
		{Text: "Add  `x`", Status: BulletImplemented},
		{Text: "Remove y", Status: BulletNotImplemented},
		{Text: "Tidy z", Status: BulletPartial},
		{Text: "Missing", Status: BulletImplemented},
	}}}
	got := []model.BulletAgg{
		{Index: 1, Text: "Remove y", Implemented: true}, // wrong
		{Index: 2, Text: "Add `x`", Implemented: true},  // right (text match, whitespace normalised)
		{Index: 3, Text: "Tidy zz", Implemented: false}, // position fallback, partial: right
	}
	if r := fidelity(tr, got); r != (Ratio{2, 4}) {
		t.Errorf("fidelity = %+v, want 2/4 (a bullet the pipeline lacks is wrong)", r)
	}
	tr.Fidelity.Applicable = false
	if r := fidelity(tr, got); r.Applies() {
		t.Errorf("not applicable: %+v", r)
	}
}

func TestUndocumentedRecall(t *testing.T) {
	o, _ := fixture()
	ix := newIndex(o)
	tr := Truth{Fidelity: Fidelity{Applicable: true, Undocumented: []UndocumentedTruth{
		{Loc: loc("a.go", 21, 21), Certainty: Firm},
		{Loc: loc("a.go", 1, 1), Certainty: Firm},
		{Loc: loc("a_test.go", 1, 1), Certainty: Soft},
	}}}
	if got := undocumentedRecall(tr, []string{"u2", "u4"}, ix); got != (Ratio{1, 2}) {
		t.Errorf("undocumented = %+v, want 1/2", got)
	}
}

func TestLinesUnsureAndMustRead(t *testing.T) {
	o, _ := fixture()
	o.Labels = []model.Label{{UnitID: "u1"}, {UnitID: "u1", Line: 1}, {UnitID: "u2"}, {UnitID: "item:pr:1"}}
	o.Agg.Unsure = 2
	row := Score(Truth{LinesThatMatter: LinesThatMatter{MustRead: 8, Tolerance: 0.25}}, o)
	// Eyes units u1 (3), u3 (4), u4 (4) = 11 of 13 changed lines.
	if got := row.Ratios[MLinesToRead]; got != (Ratio{11, 13}) {
		t.Errorf("lines to read = %+v", got)
	}
	if got := row.Ratios[MUnsureLabels]; got != (Ratio{2, 3}) {
		t.Errorf("unsure labels = %+v, want 2/3 (item labels not counted)", got)
	}
	if got := row.Ratios[MUnsureUnits]; got != (Ratio{2, 4}) {
		t.Errorf("unsure units = %+v", got)
	}
	if row.MustReadRange != [2]int{6, 10} || row.Ratios[MMustRead] != (Ratio{0, 1}) {
		t.Errorf("must read %v %+v, want [6 10] and 0/1", row.MustReadRange, row.Ratios[MMustRead])
	}
	rng, in := mustRead(LinesThatMatter{MustRead: 100, Tolerance: 0.35}, 135)
	if rng != [2]int{65, 135} || in != (Ratio{1, 1}) {
		t.Errorf("must read 100±35%% = %v %+v", rng, in)
	}
	rng, in = mustRead(LinesThatMatter{MustRead: 0, Tolerance: 1}, 0)
	if rng != [2]int{0, 0} || in != (Ratio{1, 1}) {
		t.Errorf("must read 0 = %v %+v", rng, in)
	}
}

func TestBranchStateAndFileKind(t *testing.T) {
	want := &BranchTruth{State: "spike", StateOK: []string{"spike", "WIP"}}
	for got, r := range map[string]Ratio{"spike": {1, 1}, "WIP": {1, 1}, "abandoned": {0, 1}} {
		if b := branchState(want, &model.BranchInfo{State: got}); b != r {
			t.Errorf("state %q = %+v, want %+v", got, b, r)
		}
	}
	if b := branchState(want, nil); b != (Ratio{0, 1}) {
		t.Errorf("no block = %+v", b)
	}
	if b := branchState(nil, &model.BranchInfo{State: "spike"}); b.Applies() {
		t.Errorf("no [branch] = %+v", b)
	}

	o, _ := fixture()
	o.Units = append(o.Units, mkUnit("u5", "a.go", 40, 40, "+r1", "+r2", "+r3", "+r4"))
	o.Agg.Units = append(o.Agg.Units, model.UnitAgg{UnitID: "u5", Kind: "refactor"})
	ix := newIndex(o)
	files := []FileTruth{
		{Path: "a.go", Kind: "refactor"},                          // feature (5 lines) beats refactor (4): miss
		{Path: "a.go", Kind: "bugfix", Also: []string{"feature"}}, // feature accepted via also
		{Path: "b.go", Kind: "refactor"},
		{Path: "nope.go", Kind: "feature"},
	}
	if got := fileKind(files, ix); got != (Ratio{2, 4}) {
		t.Errorf("file kind = %+v, want 2/4", got)
	}
	if d := dominant(map[string]int{"b": 2, "a": 2, "c": 1}); d != "a" {
		t.Errorf("dominant tie = %q", d)
	}
}
