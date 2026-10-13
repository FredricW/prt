package route

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// gatherFixture: group {g1, g2, gt}; caller c1 calls g1, i1 imports g2,
// test t1 tests g1, gt (a test in the group) tests cut1, g2 imports d1,
// cfg is a config file, x is unrelated.
func gatherFixture() (model.Group, []model.ChangeUnit, []model.Link, []model.UnitAgg) {
	mk := func(id, p string, order int) model.ChangeUnit {
		return model.ChangeUnit{
			ID: id, Path: p, Order: order, Symbol: strings.ToUpper(id), Signature: "func " + strings.ToUpper(id) + "()",
			Range: model.Range{New: model.Span{Start: 10, Count: 3}}, Diff: "@@ -10,2 +10,3 @@\n ctx\n-old\n+new\n+more\n",
		}
	}
	units := []model.ChangeUnit{
		mk("g1", "src/a.go", 1), mk("g2", "src/b.go", 2), mk("gt", "src/a_test.go", 3),
		mk("c1", "src/c.go", 5), mk("i1", "src/i.go", 4), mk("t1", "src/b_test.go", 6),
		mk("cut1", "src/cut.go", 7), mk("d1", "src/types.go", 8), mk("cfg", "config/app.yaml", 9),
		mk("x", "src/x.go", 10), mk("envu", "src/env.go", 11),
	}
	links := []model.Link{
		{From: "c1", To: "g1", Kind: model.LinkCalls},
		{From: "i1", To: "g2", Kind: model.LinkImports},
		{From: "g1", To: "g2", Kind: model.LinkCalls}, // inside the group
		{From: "t1", To: "g1", Kind: model.LinkTests},
		{From: "gt", To: "cut1", Kind: model.LinkTests},
		{From: "g2", To: "d1", Kind: model.LinkImports},
		{From: "x", To: "c1", Kind: model.LinkCalls},
		{From: "ghost", To: "g1", Kind: model.LinkCalls}, // unknown unit
	}
	aggs := []model.UnitAgg{{UnitID: "envu", Contract: "env/flag"}}
	g := model.Group{ID: "g", UnitIDs: []string{"g1", "g2", "gt"}}
	return g, units, links, aggs
}

func blockIDs(bs []model.ContextBlock) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.Kind+":"+b.UnitID)
	}
	return out
}

func TestGatherKinds(t *testing.T) {
	g, units, links, aggs := gatherFixture()
	tests := []struct {
		kinds []string
		want  []string
	}{
		{[]string{"callers"}, []string{"callers:i1", "callers:c1"}},
		{[]string{"tests"}, []string{"tests:t1"}},
		{[]string{"code_under_test"}, []string{"code_under_test:cut1"}},
		{[]string{"types"}, []string{"types:d1"}},
		{[]string{"config"}, []string{"config:cfg", "config:envu"}},
		// Canonical kind order regardless of the order asked.
		{[]string{"code_under_test", "tests", "callers"}, []string{"callers:i1", "callers:c1", "tests:t1", "code_under_test:cut1"}},
		{nil, nil},
	}
	for _, tt := range tests {
		got := blockIDs(Gather(tt.kinds, g, units, links, aggs))
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Gather(%v) = %v, want %v", tt.kinds, got, tt.want)
		}
	}
}

func TestGatherBlockBody(t *testing.T) {
	g, units, links, aggs := gatherFixture()
	bs := Gather([]string{"callers"}, g, units, links, aggs)
	b := bs[1]
	if b.Title != "caller src/c.go C1" {
		t.Errorf("title = %q", b.Title)
	}
	want := "src/c.go:10-12\nfunc C1()\n@@ -10,2 +10,3 @@\n ctx\n-old\n+new\n+more"
	if b.Body != want {
		t.Errorf("body = %q, want %q", b.Body, want)
	}
}

func TestGatherDeterministicAndDeduped(t *testing.T) {
	g, units, links, aggs := gatherFixture()
	// c1 is both a caller and (via an extra link) a test of the group.
	links = append(links, model.Link{From: "c1", To: "g2", Kind: model.LinkTests})
	first := Gather([]string{"callers", "tests", "types", "config", "code_under_test"}, g, units, links, aggs)
	rev := append([]model.Link(nil), links...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	revUnits := append([]model.ChangeUnit(nil), units...)
	for i, j := 0, len(revUnits)-1; i < j; i, j = i+1, j-1 {
		revUnits[i], revUnits[j] = revUnits[j], revUnits[i]
	}
	second := Gather([]string{"code_under_test", "config", "types", "tests", "callers"}, g, revUnits, rev, aggs)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("not deterministic:\n%v\n%v", blockIDs(first), blockIDs(second))
	}
	seen := map[string]bool{}
	for _, b := range first {
		if seen[b.UnitID] {
			t.Errorf("unit %s twice", b.UnitID)
		}
		seen[b.UnitID] = true
		if b.UnitID == "g1" || b.UnitID == "g2" || b.UnitID == "gt" {
			t.Errorf("group unit %s used as context", b.UnitID)
		}
	}
}

func TestGatherCap(t *testing.T) {
	g := model.Group{ID: "g", UnitIDs: []string{"g1"}}
	units := []model.ChangeUnit{{ID: "g1", Path: "a.go", Order: 0}}
	var links []model.Link
	big := strings.Repeat("+"+strings.Repeat("x", 99)+"\n", 100) // ~10 KB
	for i, id := range []string{"c1", "c2", "c3", "c4"} {
		units = append(units, model.ChangeUnit{ID: id, Path: id + ".go", Order: i + 1, Diff: big, Range: model.Range{New: model.Span{Start: 1, Count: 100}}})
		links = append(links, model.Link{From: id, To: "g1", Kind: model.LinkCalls})
	}
	bs := Gather([]string{"callers"}, g, units, links, nil)
	if got := blockIDs(bs); !reflect.DeepEqual(got, []string{"callers:c1", "callers:c2", "callers:c3"}) {
		t.Fatalf("blocks = %v", got)
	}
	total := 0
	for _, b := range bs {
		total += len(b.Body)
	}
	if total > MaxContextBytes {
		t.Errorf("total = %d > %d", total, MaxContextBytes)
	}
	last := bs[2].Body
	if !strings.HasSuffix(last, " lines elided …") || !strings.Contains(last, "\n… ") {
		t.Errorf("last block not cut: %q", last[max(0, len(last)-60):])
	}
	if bs[0].Body != Gather([]string{"callers"}, g, units[:2], links[:1], nil)[0].Body {
		t.Error("first block was cut")
	}
}

func TestCapBlocksTiny(t *testing.T) {
	long := strings.Repeat("y", 40)
	bs := []model.ContextBlock{{Body: "aaaa"}, {Body: "line one\n" + long + "\n" + long}}
	if got := capBlocks(bs, 6); len(got) != 1 {
		t.Errorf("tiny cap kept %d blocks, want only the whole first block", len(got))
	}
	got := capBlocks(bs, 4+len("line one\n")+len(elided(2)))
	if len(got) != 2 || got[1].Body != "line one\n"+elided(2) {
		t.Errorf("cut = %+v", got)
	}
}
