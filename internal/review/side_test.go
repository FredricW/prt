package review

import (
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// modifiedUnit deletes old lines 21 and 22 and adds new line 21 after
// context lines 19 and 20. Its ranges are wider than the slice of diff it
// holds (old 19–23, new 19–24), so new line 22 is in the new span but not
// numbered by the diff.
func modifiedUnit() model.ChangeUnit {
	return model.ChangeUnit{
		ID: "u-mod", Path: "a.go", Kind: model.UnitModified,
		Range: model.Range{Old: model.Span{Start: 19, Count: 5}, New: model.Span{Start: 19, Count: 6}},
		Diff:  "@@ -19,5 +19,6 @@\n ctx19\n same20\n-old21\n-old22\n+new21\n",
	}
}

func TestFindingSide(t *testing.T) {
	deletion := model.ChangeUnit{ID: "u-del", Path: "a.go", Kind: model.UnitDeleted,
		Range: model.Range{Old: model.Span{Start: 40, Count: 2}, New: model.Span{Start: 39, Count: 0}},
		Diff:  "-func Gone() {}\n-\n"}
	added := model.ChangeUnit{ID: "u-add", Path: "a.go", Kind: model.UnitAdded,
		Range: model.Range{New: model.Span{Start: 1, Count: 2}}, Diff: "+a\n+b\n"}
	// Old span 10–12, new span 10–14: old 12 is deleted and new 12 is an
	// added line, so "12" is ambiguous and counts as new.
	grown := model.ChangeUnit{ID: "u-grow", Path: "a.go", Kind: model.UnitModified,
		Range: model.Range{Old: model.Span{Start: 10, Count: 3}, New: model.Span{Start: 10, Count: 5}},
		Diff:  " c10\n c11\n-o12\n+n12\n+n13\n+n14\n"}
	// Old span 30–35 (6 lines), new span 30–31: old 33 is outside the new
	// span.
	shrunk := model.ChangeUnit{ID: "u-shrink", Path: "a.go", Kind: model.UnitModified,
		Range: model.Range{Old: model.Span{Start: 30, Count: 6}, New: model.Span{Start: 30, Count: 2}},
		Diff:  " c30\n-o31\n-o32\n-o33\n-o34\n-o35\n+n31\n"}
	cases := []struct {
		name string
		unit model.ChangeUnit
		line int
		want model.Side
	}{
		{"pure deletion", deletion, 40, model.SideOld},
		{"pure deletion, line outside", deletion, 7, model.SideOld},
		{"added file", added, 2, ""},
		{"old span only", shrunk, 33, model.SideOld},
		{"both spans, deleted old line, no new line there", modifiedUnit(), 22, model.SideOld},
		{"both spans, deleted old line but added new line", modifiedUnit(), 21, ""},
		{"both spans, context line", modifiedUnit(), 20, ""},
		{"both spans, deleted and added at the same number", grown, 12, ""},
		{"new span only", grown, 14, ""},
		{"outside both spans", modifiedUnit(), 90, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FindingSide(c.unit, c.line); got != c.want {
				t.Errorf("FindingSide(%s, %d) = %q, want %q", c.unit.ID, c.line, got, c.want)
			}
		})
	}
}

// AnchorFindings sets the side without changing which unit is chosen, and
// resets a stale side on re-anchoring.
func TestAnchorFindingsSetsSide(t *testing.T) {
	units := append(sampleUnits(), modifiedUnit())
	fs := []model.Finding{
		{Path: "a.go", Line: 22, Claim: "deleted line"},
		{Path: "a.go", Line: 21, Claim: "added line"},
		{Path: "server/internal/billing/legacy.go", Line: 40, Claim: "deleted function"},
		{Path: "web/src/api/client.ts", Line: 11, Side: model.SideOld, Claim: "stale side"},
		{Path: "other.go", Line: 3, Side: model.SideOld, Claim: "no unit"},
	}
	got := AnchorFindings(fs, units)
	want := []struct {
		unit string
		side model.Side
	}{{"u-mod", model.SideOld}, {"u-mod", ""}, {"u-legacy", model.SideOld}, {"u-client", ""}, {"", ""}}
	for i, w := range want {
		if got[i].UnitID != w.unit || got[i].Side != w.side {
			t.Errorf("%s: unit %q side %q, want %q %q", got[i].Claim, got[i].UnitID, got[i].Side, w.unit, w.side)
		}
	}
}
