package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// loadDetail reads a proto.Detail fixture from testdata.
func loadDetail(t *testing.T, name string) proto.Detail {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var d proto.Detail
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return d
}

// renderOverview renders d's Overview without colour at width 80.
func renderOverviewText(d proto.Detail) (string, []string) {
	r := plainRenderer()
	return overviewText(newStyles(r), newMarkdown(r), d, fixedNow, 80)
}

func TestOverviewGolden(t *testing.T) {
	full := loadDetail(t, "detail_full.json")

	empty := full
	empty.Agg = &model.ItemAgg{ItemID: full.Item.ID, Added: 3, Removed: 1, UnitsTotal: 1,
		Files: []model.FileAgg{{Path: "README.md", UnitIDs: []string{"u-readme"}}},
		Units: []model.UnitAgg{{UnitID: "u-readme", Attention: model.AttnSkip}}}
	empty.Item.Body = ""

	missing := full
	missing.Agg = &model.ItemAgg{ItemID: full.Item.ID, LabelsMissing: true, Added: 312, Removed: 88, UnitsTotal: 6}

	pending := full
	pending.Agg = nil

	tests := []struct {
		name string
		d    proto.Detail
	}{
		{"overview_full", full},
		{"overview_empty_blocks", empty},
		{"overview_labels_missing", missing},
		{"overview_agg_nil", pending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, anchors := renderOverviewText(tt.d)
			if strings.Contains(got, "\x1b[") {
				t.Fatal("overview contains ANSI escapes")
			}
			if n := len(strings.Split(got, "\n")); n != len(anchors) {
				t.Errorf("%d lines but %d anchors", n, len(anchors))
			}
			golden(t, tt.name, got+"\n")
		})
	}
}

// Every block is present even when empty (spec: collapse to a dim line,
// never omit).
func TestOverviewEmptyBlocksCollapse(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	d.Agg = &model.ItemAgg{}
	got, _ := renderOverviewText(d)
	for _, want := range []string{
		"no changed lines", "+0 −0 → ~0 lines that matter · ~0 min · 0 of 0 chunks need eyes",
		"no contract changes", "nothing flagged", "risk unknown", "no risky chunks",
		"no description bullets", "no units", "no modules", "nothing to compare yet (first look)", "all labels confident",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("empty overview missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "coherence") {
		t.Error("coherence shown without a shape label")
	}
}

func TestOverviewProbabilities(t *testing.T) {
	got, _ := renderOverviewText(loadDetail(t, "detail_full.json"))
	lines := strings.Split(got, "\n")
	find := func(sub string) string {
		for _, l := range lines {
			if strings.Contains(l, sub) {
				return l
			}
		}
		t.Fatalf("no line with %q:\n%s", sub, got)
		return ""
	}
	tests := []struct{ sub, prefix, prob string }{
		{"refresh(token)", "  breaking", ".86"},    // sure
		{"Refresh(ctx, id)", "? signature", ".55"}, // below .60: '?' and unsure
		{"Refresh tokens before", "  ✓ implemented", ".91"},
		{"Add a device list", "? ✓ implemented", ".58"},
		{"coherence: mixed", "  coherence", ".72"},
	}
	for _, tt := range tests {
		l := find(tt.sub)
		if !strings.HasPrefix(l, tt.prefix) || !strings.HasSuffix(l, tt.prob) {
			t.Errorf("line %q: want prefix %q and probability %q", l, tt.prefix, tt.prob)
		}
	}
}

// A headline whose unit has no symbol (Text = path) does not repeat the path.
func TestOverviewHeadlineWithoutSymbol(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	d.Agg = &model.ItemAgg{Headlines: []model.Headline{
		{UnitID: "u", Path: "web/src/api/index.ts", Line: 2, Kind: "breaking", Text: "web/src/api/index.ts", P: 0.83},
	}}
	got, _ := renderOverviewText(d)
	if !strings.Contains(got, "breaking  (top level)  web/src/api/index.ts:2  .83") {
		t.Errorf("headline row not as expected:\n%s", got)
	}
}

func TestCompositionBar(t *testing.T) {
	a := &model.ItemAgg{Composition: map[string]float64{"refactor": 0.5, "feature": 0.5, "fix": 0.0},
		EffectiveLines: 90, NoiseLines: 10}
	segs := compositionSegments(a)
	if got := compositionLegend(segs); got != "f feature 45% · r refactor 45% · ░ noise 10%" {
		t.Errorf("legend = %q", got)
	}
	if got := compositionBar(segs, 20); got != "fffffffffrrrrrrrrr░░" {
		t.Errorf("bar = %q", got)
	}
	// Name clash: second "f…" kind takes its next free letter.
	segs = compositionSegments(&model.ItemAgg{Composition: map[string]float64{"feature": 0.6, "formatting": 0.4}})
	if segs[0].glyph != "f" || segs[1].glyph != "o" {
		t.Errorf("glyphs = %q, %q", segs[0].glyph, segs[1].glyph)
	}
	// A tiny share still gets one cell.
	if got := cellWidths([]float64{0.99, 0.01}, 10); fmt.Sprint(got) != "[9 1]" {
		t.Errorf("cellWidths = %v", got)
	}
}

func TestRiskWordAndChips(t *testing.T) {
	for r, want := range map[float64]string{5: "HIGH", 4: "HIGH", 3.5: "MED", 3: "MED", 2: "LOW", 1: "LOW"} {
		if got := riskWord(r); got != want {
			t.Errorf("riskWord(%v) = %s, want %s", r, got, want)
		}
	}
	if got := riskChips(map[string]int{"pii": 1, "auth": 2, "data_write": 1, "money": 0}); got != "auth ×2  data write ×1  pii ×1" {
		t.Errorf("chips = %q", got)
	}
}

// overviewFocused returns a model showing detail_full in the Overview with
// the main pane focused, and the recording Code pane.
func overviewFocused(t *testing.T, d proto.Detail) (Model, *fakePane) {
	t.Helper()
	f := newFake(d.Item)
	f.details[d.Item.ID] = d
	m, p := withFakePane(newTestModel(t, f, Options{}))
	m = feed[detailMsg](t, m, m.selectionChanged())
	m = update(t, m, keyMsg("enter"))
	if m.detail == nil || m.focus != FocusMain || m.tab != TabOverview {
		t.Fatalf("setup: detail %v focus %v tab %v", m.detail != nil, m.focus, m.tab)
	}
	return m, p
}

// lineOf returns the content line index of the first Overview line with sub.
func lineOf(t *testing.T, m Model, sub string) int {
	t.Helper()
	text, _ := overviewText(m.st, m.md, *m.detail, fixedNow, m.overview.vp.Width-2)
	for i, l := range strings.Split(text, "\n") {
		if strings.Contains(l, sub) {
			return i
		}
	}
	t.Fatalf("no overview line with %q", sub)
	return 0
}

func TestOverviewEnterJumpsToUnit(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	m, p := overviewFocused(t, d)
	m = update(t, m, keyMsg("enter"))
	if m.tab != TabCode {
		t.Fatalf("enter: tab %v, want Code", m.tab)
	}
	if fmt.Sprint(p.focused) != "[u-refresh]" {
		t.Errorf("FocusUnit calls = %v, want [u-refresh] (first file's first unit)", p.focused)
	}
	if !strings.Contains(m.note, "unit not found") {
		t.Errorf("a failed FocusUnit should leave a note, got %q", m.note)
	}

	// Scrolled so the "Check before approving" block is at the top: the
	// first anchored line after it is the top hot spot.
	m = update(t, m, keyMsg("1"))
	m.overview.vp.SetYOffset(lineOf(t, m, "Check before approving"))
	p.focusOK = true
	m = update(t, m, keyMsg("enter"))
	if m.tab != TabCode || p.focused[len(p.focused)-1] != "u-refresh" {
		t.Errorf("hot spot jump: tab %v, focused %v", m.tab, p.focused)
	}
	m = update(t, m, keyMsg("1"))
	m.overview.vp.SetYOffset(lineOf(t, m, "undocumented"))
	m = update(t, m, keyMsg("enter"))
	if got := p.focused[len(p.focused)-1]; got != "u-server" || m.note != "" {
		t.Errorf("undocumented jump focused %s (note %q)", got, m.note)
	}
}

func TestOverviewEnterWithoutUnit(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	d.Agg = &model.ItemAgg{}
	m, p := overviewFocused(t, d)
	for _, k := range []string{"enter", "L"} {
		m = update(t, m, keyMsg(k))
		if m.tab != TabOverview || m.overview.raw || m.note != "no unit here" {
			t.Errorf("%s without anchors: tab %v raw %v note %q", k, m.tab, m.overview.raw, m.note)
		}
	}
	if len(p.focused) != 0 {
		t.Errorf("FocusUnit called: %v", p.focused)
	}
}

func TestOverviewRawLabels(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	m, _ := overviewFocused(t, d)
	m.overview.vp.SetYOffset(lineOf(t, m, "Headlines"))
	for _, closeKey := range []string{"L", "esc"} {
		m = update(t, m, keyMsg("L"))
		if !m.overview.raw {
			t.Fatal("L did not open the raw-labels overlay")
		}
		v := m.View()
		for _, want := range []string{"Raw labels", "web/src/session.ts · refresh", "breaking", "risk_tags.auth", "L14"} {
			if !strings.Contains(v, want) {
				t.Errorf("overlay missing %q:\n%s", want, v)
			}
		}
		m = update(t, m, keyMsg("2")) // captured: no tab switch
		if m.tab != TabOverview {
			t.Fatal("overlay let 2 switch tabs")
		}
		m = update(t, m, keyMsg(closeKey))
		if m.overview.raw || m.focus != FocusMain {
			t.Errorf("%s: raw %v focus %v; want closed, main focused", closeKey, m.overview.raw, m.focus)
		}
	}
}

func TestOverviewClickOpensUnit(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	m, p := overviewFocused(t, d)
	row := lineOf(t, m, "server/api/session.go  ") // minimap row
	m.overview.vp.SetYOffset(row)                  // may clamp near the bottom
	sw, _, _ := m.dims()
	y := row - m.overview.vp.YOffset + 1 // row 0 of the main pane is the tab bar
	m = update(t, m, mouseClick(sw+4, y))
	if m.tab != TabCode || fmt.Sprint(p.focused) != "[u-server]" {
		t.Errorf("click on a minimap row: tab %v focused %v", m.tab, p.focused)
	}
}

// Spec: "Clicking or enter on a cell opens that unit in Code" — a click on
// the second cell of a row opens the second unit, not the row's first.
func TestOverviewClickOnMinimapCell(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	m, p := overviewFocused(t, d)
	row := lineOf(t, m, "web/src/session.ts  ")
	c, ok := m.overview.cells[row]
	if !ok || len(c.ids) != 2 {
		t.Fatalf("minimap row %d cells = %+v", row, c)
	}
	m.overview.vp.SetYOffset(row)
	sw, _, _ := m.dims()
	y := row - m.overview.vp.YOffset + 1
	m = update(t, m, mouseClick(sw+2+c.col+1, y))
	if m.tab != TabCode || fmt.Sprint(p.focused) != "[u-expired]" {
		t.Errorf("click on the second cell: tab %v focused %v, want u-expired", m.tab, p.focused)
	}
}

func TestKeyMapFromConfigM1Actions(t *testing.T) {
	def := DefaultKeyMap()
	if fmt.Sprint(def.NextFile.Keys(), def.PrevFile.Keys()) != "[J ]] [K []" {
		t.Errorf("file keys = %v %v", def.NextFile.Keys(), def.PrevFile.Keys())
	}
	km, err := KeyMapFromConfig(map[string]string{
		"next_unit": "N", "prev_unit": "P", "filters": "F", "expand": "Z", "raw_labels": "R", "next_file": "ctrl+j",
	})
	if err != nil {
		t.Fatal(err)
	}
	ck := km.codeKeys()
	got := fmt.Sprint(ck.NextUnit.Keys(), ck.PrevUnit.Keys(), ck.Filters.Keys(), ck.Expand.Keys(), ck.RawLabels.Keys(), ck.NextFile.Keys())
	if got != "[N] [P] [F] [Z] [R] [ctrl+j]" {
		t.Errorf("code keys = %s", got)
	}
	if fmt.Sprint(ck.Close.Keys()) != "[esc]" || fmt.Sprint(ck.Toggle[2].Keys()) != "[3]" {
		t.Errorf("close %v toggle3 %v", ck.Close.Keys(), ck.Toggle[2].Keys())
	}
	acts := strings.Join(KeyActions(), " ")
	for _, a := range []string{"next_unit", "prev_unit", "filters", "expand", "raw_labels"} {
		if !strings.Contains(acts, a) {
			t.Errorf("KeyActions missing %s", a)
		}
	}
}
