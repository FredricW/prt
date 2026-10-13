package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/fredricw/prt/internal/cmdline"
	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeview"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/theme"
)

// ---- Overview layout --------------------------------------------------------

// branchDetail is detail_full as a branch item with a branch state block.
func branchDetail(t *testing.T) proto.Detail {
	t.Helper()
	d := loadDetail(t, "detail_full.json")
	d.Item.Kind, d.Item.Number, d.Item.Ref, d.Item.ID = model.KindBranch, 0, "feat/session", "branch:feat/session"
	d.Item.Title = "Rework session refresh"
	d.Agg.Branch = &model.BranchInfo{
		State: model.BranchAbandoned, P: 0.70, Known: true, Rule: "no commits for 16 days",
		Probs:    map[string]float64{model.BranchWIP: 0.2, model.BranchReady: 0.05, model.BranchSpike: 0.03, model.BranchAbandoned: 0.70, model.BranchSuperseded: 0.02},
		Unpushed: 7, IdleDays: 16,
		Commits: []model.BranchCommit{
			{SHA: "a1b2c3d4e5", Subject: "refresh tokens early"}, {SHA: "b2", Subject: "device list"},
			{SHA: "c3", Subject: "wip"}, {SHA: "d4", Subject: "tests"}, {SHA: "e5", Subject: "fmt"},
			{SHA: "f6", Subject: "more"}, {SHA: "g7", Subject: "last"},
		},
		SuggestedTitle: "Refresh sessions before expiry",
	}
	return d
}

func TestOverviewLayoutGolden(t *testing.T) {
	full := loadDetail(t, "detail_full.json")
	since := loadDetail(t, "detail_since.json")
	tests := []struct {
		name  string
		d     proto.Detail
		width int
	}{
		{"overview_two_column", full, 140},
		{"overview_one_column", full, 90},
		{"overview_since_two_column", since, 140},
		{"overview_branch", branchDetail(t), 140},
		{"overview_branch_one_column", branchDetail(t), 90},
	}
	r := plainRenderer()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := overviewPage(newStyles(r), newMarkdown(r), tt.d, fixedNow, tt.width)
			got := strings.Join(p.lines, "\n")
			if strings.Contains(got, "\x1b[") {
				t.Fatal("overview contains ANSI escapes")
			}
			if len(p.lines) != len(p.anchors) {
				t.Errorf("%d lines but %d anchors", len(p.lines), len(p.anchors))
			}
			for _, l := range p.lines {
				if w := lipgloss.Width(l); w > tt.width {
					t.Errorf("line wider than %d (%d): %q", tt.width, w, l)
				}
			}
			golden(t, tt.name, got+"\n")
		})
	}
}

// Two columns from 100 cells: Headlines beside Check, Risk beside
// Fidelity; one column below.
func TestOverviewColumns(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	r := plainRenderer()
	for _, tt := range []struct {
		width int
		two   bool
	}{{140, true}, {100, true}, {99, false}, {90, false}} {
		p := overviewPage(newStyles(r), newMarkdown(r), d, fixedNow, tt.width)
		h, c := p.boxes[blockHeadlines], p.boxes[blockCheck]
		if (h == c) != tt.two {
			t.Errorf("width %d: Headlines at line %d, Check at %d; two columns = %v", tt.width, h, c, tt.two)
		}
	}
}

// Anchors and clicks map to the box under the pointer in the two-column
// layout: the left half of a line opens the left box's unit, the right half
// the right box's.
func TestOverviewTwoColumnAnchors(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	r := plainRenderer()
	p := overviewPage(newStyles(r), newMarkdown(r), d, fixedNow, 140)
	o := newOverview()
	o.setPage(p)
	lineWith := func(sub string) int {
		for i, l := range p.lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("no line with %q", sub)
		return -1
	}
	lw := (140 - colGap) / 2
	right := lw + colGap + 3
	tests := []struct {
		name   string
		line   int
		x      int
		want   string
		anchor string
	}{
		{"risk chips (left)", lineWith("Add a device list page"), 4, "u-refresh", "u-refresh"},
		{"fidelity bullet (right)", lineWith("Add a device list page"), right, "u-devices", "u-refresh"},
		{"third hot spot beside a stretched box", lineWith("web/src/devices.tsx:40"), right, "u-devices", "u-devices"},
		{"stretched box beside it", lineWith("web/src/devices.tsx:40"), 4, "", "u-devices"},
		{"box border", lineWith("Add a device list page"), 0, "", "u-refresh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := o.clickAt(tt.line, tt.x)
			if got != tt.want {
				t.Errorf("clickAt(%d, %d) = %q, want %q (line %q)", tt.line, tt.x, got, tt.want, p.lines[tt.line])
			}
			if o.anchors[tt.line] != tt.anchor {
				t.Errorf("anchor of line %d = %q, want %q", tt.line, o.anchors[tt.line], tt.anchor)
			}
		})
	}
	// A minimap cell keeps its column inside the box: the second cell of
	// session.ts opens the second unit.
	row := lineWith("web/src/session.ts  ")
	cs := o.cells[row]
	if len(cs) != 1 {
		t.Fatalf("cells on the minimap row: %+v", cs)
	}
	if got, _ := o.clickAt(row, cs[0].col+1); got != "u-expired" {
		t.Errorf("second minimap cell opens %q", got)
	}
	if stripCell(p.lines[row], cs[0].col, 2) != "█▒" {
		t.Errorf("cell run at column %d does not hold the cells: %q", cs[0].col, p.lines[row])
	}
}

// stripCell returns n display cells of s from column col.
func stripCell(s string, col, n int) string {
	rs := []rune(s)
	if col+n > len(rs) {
		return ""
	}
	return string(rs[col : col+n])
}

// Clicking a minimap cell through the root model in the two-column layout
// (a 160-column terminal) opens that cell's unit.
func TestOverviewClickTwoColumns(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	m, p := overviewFocused(t, d)
	m = update(t, m, tea.WindowSizeMsg{Width: 160, Height: 60})
	_, mw, _ := m.dims()
	if mw < twoColumnMin {
		t.Fatalf("main pane %d too narrow for two columns", mw)
	}
	row := lineOf(t, m, "web/src/session.ts  ")
	c := m.overview.cells[row][0]
	m.overview.vp.SetYOffset(row)
	sw, _, _ := m.dims()
	y := row - m.overview.vp.YOffset + 2
	m = update(t, m, mouseClick(sw+2+c.col+1, y))
	if m.tab != TabCode || fmt.Sprint(p.focused) != "[u-expired]" {
		t.Errorf("click on the second cell: tab %v focused %v", m.tab, p.focused)
	}
}

func TestBranchBlock(t *testing.T) {
	r := plainRenderer()
	render := func(d proto.Detail) string {
		got, _ := overviewText(newStyles(r), newMarkdown(r), d, fixedNow, 90)
		return got
	}
	d := branchDetail(t)
	got := render(d)
	for _, want := range []string{
		blockBranch, "abandoned? .70", "rule: no commits for 16 days",
		"abandoned .70 · WIP .20 · ready for PR .05 · spike .03 · superseded .02",
		"7 unpushed commits", "a1b2c3d refresh tokens early", "fmt", "… 2 more",
		"Suggested PR title: Refresh sessions before expiry  ai·from group names",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("branch block lacks %q:\n%s", want, got)
		}
	}
	for _, absent := range []string{blockHeadlines, blockSince} {
		if strings.Contains(got, absent) {
			t.Errorf("branch overview has %q", absent)
		}
	}
	tests := []struct {
		name   string
		mutate func(b *model.BranchInfo)
		want   string
	}{
		{"superseded", func(b *model.BranchInfo) { b.State, b.P, b.SupersededBy, b.Rule = model.BranchSuperseded, 0.77, 12, "" }, "superseded by #12 .77"},
		{"labels unavailable", func(b *model.BranchInfo) { b.Known, b.Rule, b.Probs = false, "", nil }, "state unknown — labels unavailable"},
		{"rule without Jev", func(b *model.BranchInfo) { b.Known, b.P = false, 1 }, "abandoned? 1.0"},
		{"title note", func(b *model.BranchInfo) { b.SuggestedTitle, b.TitleNote = "", "no review model" }, "Suggested PR title: none — no review model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := branchDetail(t)
			tt.mutate(d.Agg.Branch)
			if got := render(d); !strings.Contains(got, tt.want) {
				t.Errorf("lacks %q:\n%s", tt.want, got)
			}
		})
	}
	d.Agg.Branch = nil
	if got := render(d); !strings.Contains(got, "branch state not computed yet") {
		t.Errorf("nil branch info:\n%s", got)
	}
}

// S on a branch prefills the create-PR title from the suggested title.
func TestCreatePRTitleFromSuggestion(t *testing.T) {
	items := m4Items()
	f := newFake(items...)
	d := proto.Detail{Item: items[1], Agg: &model.ItemAgg{Branch: &model.BranchInfo{SuggestedTitle: "Add x to the export"}}}
	f.details[items[1].ID] = d
	m := newTestModel(t, f, Options{})
	m.picker.moveTo(1)
	m = feed[detailMsg](t, m, m.selectionChanged())
	m, last := withScriptedForms(m)
	m = pressRun(t, m, "S")
	if m.form == nil || last().v.Title != "Add x to the export" {
		t.Fatalf("form %v title %q", m.form != nil, last().v.Title)
	}
	d.Agg.Branch.SuggestedTitle = ""
	m.detail = &d
	m.form = nil
	_ = pressRun(t, m, "S")
	if last().v.Title != "Add feature x" {
		t.Errorf("fallback title %q", last().v.Title)
	}
}

// ---- header, picker, tabs ---------------------------------------------------

func TestHeaderLine(t *testing.T) {
	f := newFake(threeItems()...)
	m := newTestModel(t, f, Options{Repo: "acme/portal", Role: "frontend"})
	if h := m.viewHeader(); !strings.HasPrefix(h, "prt · acme/portal · 2 PRs · 1 branch · role: frontend") || lipgloss.Width(h) > m.width {
		t.Errorf("header %q", h)
	}
	if h := newTestModel(t, newFake(threeItems()...), Options{}).viewHeader(); h != "prt · 2 PRs · 1 branch" {
		t.Errorf("header without repo and role %q", h)
	}
	m = press(t, m, "s", " ")
	busy := threeItems()[1]
	busy.State = model.StateAnalysing
	m = update(t, m, eventMsg{Type: proto.EventItem, ItemID: busy.ID, Item: &busy})
	m = update(t, m, eventMsg{Type: proto.EventLog, Note: "GitHub unavailable"})
	h := m.viewHeader()
	if !strings.HasSuffix(h, "sort: age · 1 selected · 1 busy · GitHub unavailable") {
		t.Errorf("header right side %q", h)
	}
	if v := m.View(); !strings.HasPrefix(v, h+"\n") {
		t.Errorf("header is not the first line:\n%s", v)
	}
}

func TestTabBar(t *testing.T) {
	f := newFake(threeItems()...)
	it := threeItems()[0]
	it.Author = "ingrid"
	it.UpdatedAt = ago(49 * 3600 * 1e9)
	f.details["pr:1"] = proto.Detail{Item: it, Units: []model.ChangeUnit{{ID: "u1"}, {ID: "u2"}},
		Agg: &model.ItemAgg{Arch: &model.ArchGraph{Modules: []model.ArchModule{{ID: "a"}, {ID: "b", Hop: 1}}}}}
	m := newTestModel(t, f, Options{})
	m = update(t, m, tea.WindowSizeMsg{Width: 160, Height: 30})
	m = feed[detailMsg](t, m, m.selectionChanged())
	m, _ = withFakePane(m)
	m, _ = withFakeArch(m)
	_, mw, _ := m.dims()
	tests := []struct {
		key, want string
	}{
		{"1", "  [1 Overview]  2 Code   3 Architecture "},
		{"2", "   1 Overview  [2 Code]  3 Architecture "},
		{"3", "   1 Overview   2 Code  [3 Architecture]"},
	}
	ctx := map[string]string{"1": "@ingrid · 2d · main ← feat/one", "2": "#1 · 2 chunks", "3": "2 modules · 1 touched"}
	for _, tt := range tests {
		m = update(t, m, keyMsg(tt.key))
		bar := m.viewTabs(mw)
		if !strings.HasPrefix(bar, tt.want) || !strings.HasSuffix(bar, ctx[tt.key]) {
			t.Errorf("%s: tab bar %q", tt.key, bar)
		}
		if strings.Contains(bar, "later") {
			t.Error("Architecture still marked later")
		}
	}
}

// The selected Needs-you row has a dim why line; marks show "[x]".
func TestPickerWhyAndMarks(t *testing.T) {
	items := []model.Item{
		{ID: "pr:1", Kind: model.KindPR, Number: 1, Title: "one", Section: model.SectionNeedsYou, Urgency: 2, Why: "asked you · risk"},
		{ID: "pr:2", Kind: model.KindPR, Number: 2, Title: "two", Section: model.SectionNeedsYou, Urgency: 1, Why: "age"},
	}
	got := renderPickerCtx(items, 0, 40, 12, rowCtx{now: fixedNow, marked: map[string]bool{"pr:2": true}})
	if !strings.Contains(got, "why: asked you · risk") || strings.Contains(got, "why: age") {
		t.Errorf("why line only on the selected row:\n%s", got)
	}
	if !strings.Contains(got, "  [x] ▯▯▯▯ #2 two") || !strings.Contains(got, "▸ ▯▯▯▯ #1 one") {
		t.Errorf("marks:\n%s", got)
	}
}

func TestPickerClickWithWhyLine(t *testing.T) {
	items := []model.Item{
		{ID: "pr:1", Kind: model.KindPR, Number: 1, Title: "one", Section: model.SectionNeedsYou, Urgency: 2, Why: "asked you", State: model.StateReady},
		{ID: "pr:2", Kind: model.KindPR, Number: 2, Title: "two", Section: model.SectionNeedsYou, Urgency: 1, State: model.StateReady},
	}
	m := newTestModel(t, newFake(items...), Options{})
	// Screen rows: 0 header, 1 border, 2 divider, 3–5 pr:1 (with its why
	// line), 6–7 pr:2.
	for _, tt := range []struct {
		y    int
		want string
	}{{5, "pr:1"}, {6, "pr:2"}, {3, "pr:1"}, {7, "pr:2"}} {
		m = update(t, m, mouseClick(5, tt.y))
		if got := selectedID(m); got != tt.want {
			t.Errorf("click on row %d selected %s, want %s", tt.y, got, tt.want)
		}
	}
	// The wheel inside the box moves the cursor.
	m = update(t, m, tea.MouseMsg{X: 5, Y: 4, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if got := selectedID(m); got != "pr:1" {
		t.Errorf("wheel up selected %s", got)
	}
}

func TestMultiSelect(t *testing.T) {
	m := newTestModel(t, newFake(threeItems()...), Options{})
	m = press(t, m, " ")
	if !m.marked["pr:1"] || !strings.Contains(m.note, "overlap view comes in M6") {
		t.Fatalf("space: marked %v note %q", m.marked, m.note)
	}
	m = press(t, m, "j", " ")
	if len(m.marked) != 2 || m.note != "" {
		t.Errorf("second mark: %v, note %q (the M6 note is shown once)", m.marked, m.note)
	}
	if !strings.Contains(m.viewHeader(), "2 selected") || !strings.Contains(m.View(), "[x]") {
		t.Errorf("header/marks:\n%s", m.View())
	}
	m = press(t, m, " ")
	if len(m.marked) != 1 {
		t.Errorf("space did not toggle off: %v", m.marked)
	}
	m = press(t, m, "esc")
	if len(m.marked) != 0 || m.focus != FocusPicker || strings.Contains(m.viewHeader(), "selected") {
		t.Errorf("esc: marks %v focus %v", m.marked, m.focus)
	}
}

func TestRemove(t *testing.T) {
	items := threeItems()
	items = append(items, model.Item{ID: "pr:9", Kind: model.KindPR, Number: 9, Title: "nine", Section: model.SectionAdded,
		Source: model.SourceAdded, State: model.StateReady})
	f := newFake(items...)
	m := newTestModel(t, f, Options{})
	m = pressRun(t, m, "x")
	if m.note != "only added items can be removed" || len(f.removes) != 0 {
		t.Errorf("x on a ranked item: note %q removes %v", m.note, f.removes)
	}
	m = press(t, m, "G")
	m = pressRun(t, m, "x")
	if fmt.Sprint(f.removes) != "[pr:9]" || m.note != "removed #9 nine" {
		t.Errorf("x on an added item: removes %v note %q", f.removes, m.note)
	}
}

// ---- search and add ---------------------------------------------------------

func searchModel(t *testing.T) (Model, *fakeClient, *fakeSearch) {
	t.Helper()
	f := newFake(threeItems()...)
	f.hits = map[proto.SearchSource][]proto.SearchHit{proto.SearchLocal: {
		{Source: proto.SearchOpen, Kind: model.KindPR, Number: 99, Title: "New thing", Query: "#99", ItemID: "pr:99"},
		{Source: proto.SearchOpen, Kind: model.KindPR, Number: 2, Title: "two", Query: "#2", ItemID: "pr:2", Listed: true},
	}}
	m, s := withFakeSearch(newTestModel(t, f, Options{}))
	return m, f, s
}

func TestSearchOpenTypeAndAdd(t *testing.T) {
	m, f, s := searchModel(t)
	m = press(t, m, "/")
	if !s.active || len(s.sizes) == 0 {
		t.Fatalf("/ did not open the prompt (sizes %v)", s.sizes)
	}
	sw, _, _ := m.dims()
	if got := s.sizes[len(s.sizes)-1]; got[0] != sw-3 || got[1] != searchRows(m.sidebarInner()) {
		t.Errorf("prompt size %v", got)
	}
	m = typeKeys(m, "9q") // q types, it does not quit
	if fmt.Sprint(f.searches) != "[{9 local 0} {9q local 0}]" || len(s.hits) != 2 {
		t.Fatalf("searches %v hits %v", f.searches, s.hits)
	}
	v := m.View()
	if !strings.Contains(v, "/ 9q") || !strings.Contains(v, "#99 New thing") || !strings.Contains(v, "enter add") {
		t.Errorf("prompt not in the sidebar:\n%s", v)
	}
	f.added = model.Item{ID: "pr:99", Kind: model.KindPR, Number: 99, Title: "New thing", Section: model.SectionAdded, Source: model.SourceAdded}
	m = pressRun(t, m, "enter")
	if fmt.Sprint(f.adds) != "[#99]" || s.active {
		t.Fatalf("enter: adds %v prompt open %v", f.adds, s.active)
	}
	items, _ := f.Items(context.Background()) // the reload the add queued
	m = update(t, m, reloadedMsg{items: items})
	if selectedID(m) != "pr:99" {
		t.Errorf("cursor on %s, want the added item", selectedID(m))
	}
}

func TestSearchListedHitMovesCursor(t *testing.T) {
	m, f, s := searchModel(t)
	m = press(t, m, "/")
	m = typeKeys(m, "two")
	m = press(t, m, "down")
	m = pressRun(t, m, "enter")
	if len(f.adds) != 0 || s.active || selectedID(m) != "pr:2" {
		t.Errorf("listed hit: adds %v open %v selected %s", f.adds, s.active, selectedID(m))
	}
}

func TestSearchPreview(t *testing.T) {
	m, f, s := searchModel(t)
	f.previewed = model.Item{ID: "pr:99", Kind: model.KindPR, Number: 99, Title: "New thing", Source: model.SourcePreview}
	f.details["pr:99"] = proto.Detail{Item: f.previewed}
	m = press(t, m, "/")
	m = typeKeys(m, "9")
	m = pressRun(t, m, "tab")
	for _, msg := range run(m.loadPreview(m.previewID, m.previewSeq)) {
		m = update(t, m, msg)
	}
	if fmt.Sprint(f.previews) != "[#99]" || m.previewID != "pr:99" || m.preview == nil {
		t.Fatalf("tab: previews %v id %q detail %v", f.previews, m.previewID, m.preview != nil)
	}
	v := m.View()
	if !strings.Contains(v, previewLine) || !strings.Contains(v, "#99 New thing") || selectedID(m) != "pr:1" {
		t.Errorf("preview not shown (selected %s):\n%s", selectedID(m), v)
	}
	// An event for the preview item reloads it.
	_, cmd := updateCmd(t, m, eventMsg{Type: proto.EventPatch, ItemID: "pr:99"})
	seen := false
	for _, msg := range run(cmd) {
		if _, ok := msg.(previewDetailMsg); ok {
			seen = true
		}
	}
	if !seen {
		t.Error("a patch of the preview item did not reload it")
	}
	m = pressRun(t, m, "esc")
	if s.active || m.previewID != "" || fmt.Sprint(f.removes) != "[pr:99]" {
		t.Errorf("esc: open %v preview %q removes %v", s.active, m.previewID, f.removes)
	}
	if strings.Contains(m.View(), previewLine) {
		t.Error("preview still shown after esc")
	}
}

// enter on the previewed result adds it (the daemon promotes the preview)
// without removing it.
func TestSearchPreviewThenAdd(t *testing.T) {
	m, f, _ := searchModel(t)
	f.previewed = model.Item{ID: "pr:99", Kind: model.KindPR, Number: 99}
	f.added = model.Item{ID: "pr:99", Kind: model.KindPR, Number: 99, Section: model.SectionAdded, Source: model.SourceAdded}
	m = press(t, m, "/")
	m = typeKeys(m, "9")
	m = pressRun(t, m, "tab")
	m = pressRun(t, m, "enter")
	if len(f.removes) != 0 || fmt.Sprint(f.adds) != "[#99]" || m.previewID != "" {
		t.Errorf("removes %v adds %v preview %q", f.removes, f.adds, m.previewID)
	}
}

func TestSearchMouseGoesToPrompt(t *testing.T) {
	m, _, s := searchModel(t)
	m = press(t, m, "/")
	m = typeKeys(m, "9")
	listRows, pad, lines := m.searchLayout()
	if len(lines) != 3 {
		t.Fatalf("search view lines %v", lines)
	}
	y := 2 + listRows + pad + 1 // the second result
	m = update(t, m, mouseClick(5, y))
	if len(s.mice) != 1 || s.mice[0].X != 3 || s.mice[0].Y != 1 {
		t.Errorf("mouse at the prompt: %+v", s.mice)
	}
	m = update(t, m, mouseClick(5, 3)) // the list above still selects
	if len(s.mice) != 1 || selectedID(m) != "pr:1" {
		t.Errorf("list click: mice %d selected %s", len(s.mice), selectedID(m))
	}
}

// ---- command line -------------------------------------------------------------

func TestCommandLine(t *testing.T) {
	reviews := []model.Review{
		{GroupID: "g1", Tier: model.TierFast},
		{GroupID: "g2", Tier: model.TierDeep},
		{GroupID: "g3", Tier: model.TierNone, UserSkipped: true},
	}
	tests := []struct {
		name    string
		line    string
		sub     cmdline.Submitted
		setup   func(m Model, p *fakePane, pp *fakePipe) Model
		actions []proto.ReviewActionParams
		caps    []proto.CapParams
		adds    []string
		note    string
		quits   bool
	}{
		{name: "add", line: "add #7", sub: cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Add, Arg: "#7"}},
			adds: []string{"#7"}, note: "add failed: not found"},
		{name: "tier deep on all groups", line: "tier deep", sub: cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Tier, Tier: "deep"}},
			actions: []proto.ReviewActionParams{{ItemID: "pr:1", GroupID: "g1", Action: proto.ActionDeep}, {ItemID: "pr:1", GroupID: "g3", Action: proto.ActionDeep}},
			note:    "tier deep: 2 groups re-routing…"},
		{name: "tier rules on all groups", line: "tier rules", sub: cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Tier, Tier: "rules"}},
			actions: []proto.ReviewActionParams{{ItemID: "pr:1", GroupID: "g3", Action: proto.ActionReset}},
			note:    "tier rules: 1 group re-routing…"},
		{name: "tier deep on the focused group by change", line: "tier deep", sub: cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Tier, Tier: "deep"}},
			setup: func(m Model, p *fakePane, _ *fakePipe) Model {
				p.mode, p.unit, p.unitOK = codeview.ModeChange, model.ChangeUnit{ID: "u-b"}, true
				m.tab = TabCode
				return m
			},
			actions: []proto.ReviewActionParams{{ItemID: "pr:1", GroupID: "g2", Action: proto.ActionDeep}}, note: "re-reviewing on deep…"},
		{name: "tier none on the pipeline row", line: "tier none", sub: cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Tier, Tier: "none"}},
			setup: func(m Model, _ *fakePane, pp *fakePipe) Model {
				pp.selGroup = "g1"
				m.tab = TabPipeline
				return m
			},
			actions: []proto.ReviewActionParams{{ItemID: "pr:1", GroupID: "g1", Action: proto.ActionSkip}}, note: "review skipped; re-routing…"},
		{name: "tier fast refused", line: "tier fast", sub: cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Tier, Tier: "fast"}},
			note: "fast is chosen by the rules; use :tier rules"},
		{name: "parse error", line: "foo", note: "unknown command :foo"},
		{name: "cap", line: "cap 1.50", sub: cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Cap, CapUSD: 1.5}},
			caps: []proto.CapParams{{ItemID: "pr:1", CapUSD: 1.5}}, note: "cost cap for this item: $1.50"},
		{name: "cap default", line: "cap default", sub: cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Cap, Arg: "default"}},
			caps: []proto.CapParams{{ItemID: "pr:1"}}, note: "cost cap for this item: the routing file's default"},
		{name: "quit", line: "q", sub: cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Quit}}, quits: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := threeItems()
			f := newFake(items...)
			f.details["pr:1"] = proto.Detail{Item: items[0], Reviews: reviews,
				Groups: []model.Group{{ID: "g1", UnitIDs: []string{"u-a"}}, {ID: "g2", UnitIDs: []string{"u-b"}}}}
			m, p := withFakePane(newTestModel(t, f, Options{}))
			m, pp := withFakePipe(m)
			m = feed[detailMsg](t, m, m.selectionChanged())
			next := map[string]cmdline.Submitted{}
			if tt.line != "foo" {
				next[tt.line] = tt.sub
			}
			m, c := withFakeCmd(m, next)
			if tt.setup != nil {
				m = tt.setup(m, p, pp)
			}
			m = press(t, m, ":")
			if !c.active || !strings.HasPrefix(m.viewStatus(), ":") {
				t.Fatalf(": did not open the command line (status %q)", m.viewStatus())
			}
			m = typeKeys(m, tt.line)
			m, cmd := updateCmd(t, m, keyMsg("enter"))
			if tt.quits {
				if !isQuit(cmd) {
					t.Fatal(":q did not quit")
				}
				return
			}
			for _, msg := range run(cmd) {
				m = update(t, m, msg)
			}
			if fmt.Sprint(f.reviewActions) != fmt.Sprint(tt.actions) || fmt.Sprint(f.caps) != fmt.Sprint(tt.caps) ||
				fmt.Sprint(f.adds) != fmt.Sprint(tt.adds) {
				t.Errorf("actions %v caps %v adds %v", f.reviewActions, f.caps, f.adds)
			}
			if m.note != tt.note || c.active {
				t.Errorf("note %q (want %q), open %v", m.note, tt.note, c.active)
			}
		})
	}
}

// :q saves an open composer first, like ctrl+c.
func TestCommandQuitSavesComposer(t *testing.T) {
	m, f, p, _ := m4Model(t)
	m, _ = withFakeCmd(m, map[string]cmdline.Submitted{"q": {Cmd: cmdline.Command{Kind: cmdline.Quit}}})
	p.composerOp = &codeview.CommentOp{Kind: codeview.CommentOpSave, Comment: model.Comment{Body: "draft"}}
	m = press(t, m, ":")
	m = typeKeys(m, "q")
	_, cmd := updateCmd(t, m, keyMsg("enter"))
	if cmd == nil {
		t.Fatal("no command")
	}
	var kinds []string
	var flat func(c tea.Cmd)
	flat = func(c tea.Cmd) { // batches and sequences are slices of commands
		if c == nil {
			return
		}
		msg := c()
		if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice {
			for i := range v.Len() {
				if sub, ok := v.Index(i).Interface().(tea.Cmd); ok {
					flat(sub)
				}
			}
			return
		}
		kinds = append(kinds, fmt.Sprintf("%T", msg))
	}
	flat(cmd)
	if fmt.Sprint(kinds) != "[tui.commentDoneMsg tea.QuitMsg]" || len(f.cc.saves) != 1 {
		t.Errorf(":q ran %v, saves %v; want the save, then quit", kinds, f.cc.saves)
	}
}

// ---- Architecture tab ---------------------------------------------------------

func archModel(t *testing.T) (Model, *fakeArch, *fakePane) {
	t.Helper()
	items := threeItems()
	f := newFake(items...)
	f.details["pr:1"] = proto.Detail{Item: items[0], Units: []model.ChangeUnit{
		{ID: "u-a", Path: "web/a.ts"}, {ID: "u-b", Path: "web/b.ts"}, {ID: "u-b2", Path: "web/b.ts"},
	}}
	m, p := withFakePane(newTestModel(t, f, Options{}))
	m, a := withFakeArch(m)
	m = feed[detailMsg](t, m, m.selectionChanged())
	return m, a, p
}

func TestArchitectureTab(t *testing.T) {
	m, a, p := archModel(t)
	if fmt.Sprint(a.details) != "[pr:1]" || a.clears == 0 {
		t.Errorf("SetDetail %v, Clear %d", a.details, a.clears)
	}
	sw, mw, bh := m.dims()
	if a.origin != [2]int{sw + 2, 2} || a.size != [2]int{mw, bh - 1} {
		t.Errorf("origin %v size %v", a.origin, a.size)
	}
	m = press(t, m, "3", "enter")
	if m.tab != TabArch || m.focus != FocusMain || !strings.Contains(m.View(), "fake architecture page") {
		t.Fatalf("3: tab %v focus %v", m.tab, m.focus)
	}
	m = press(t, m, "h", "l", "j", "+", "-", "d")
	if got := strings.Join(a.offered, " "); got != "h l j + - d" {
		t.Errorf("keys offered to the page: %q", got)
	}
	// enter opens the file list, which captures 1 and q; enter in it jumps.
	a.jumpPath = "web/b.ts"
	m = press(t, m, "enter", "1")
	if m.tab != TabArch || !a.capturing {
		t.Fatalf("file list: tab %v capturing %v", m.tab, a.capturing)
	}
	if _, cmd := updateCmd(t, m, keyMsg("q")); isQuit(cmd) {
		t.Fatal("q quit while the file list captures keys")
	}
	m = press(t, m, "enter")
	if m.tab != TabCode || fmt.Sprint(p.focused) != "[u-b]" {
		t.Errorf("jump: tab %v focused %v", m.tab, p.focused)
	}
	// The status line lists the page's hints; help its groups.
	m = press(t, m, "3")
	if s := m.viewStatus(); !strings.Contains(s, "l focus right") || !strings.Contains(s, "1 2 3 tabs") {
		t.Errorf("status %q", s)
	}
	m = press(t, m, "?")
	if v := m.View(); !strings.Contains(v, "Keys · architecture") || !strings.Contains(v, "more depth") {
		t.Errorf("help:\n%s", v)
	}
	m = press(t, m, "?", "esc")
	if m.focus != FocusPicker {
		t.Errorf("esc on the tab: focus %v", m.focus)
	}
}

func TestArchitectureMouse(t *testing.T) {
	m, a, p := archModel(t)
	m = press(t, m, "3")
	a.jumpPath = "web/a.ts"
	sw, _, _ := m.dims()
	m = update(t, m, mouseClick(sw+5, 6))
	if len(a.mice) != 1 || m.tab != TabCode || fmt.Sprint(p.focused) != "[u-a]" {
		t.Errorf("mouse: %d messages, tab %v focused %v", len(a.mice), m.tab, p.focused)
	}
}

func TestArchKeysRebindable(t *testing.T) {
	km, err := KeyMapFromConfig(map[string]string{"arch_depth_up": "]", "arch_depth_down": "[", "diff_only": "D",
		"left": "H", "right": "L", "search": "ctrl+f", "remove": "X", "command": ";", "select": "v", "architecture": "4",
		"side_by_side": "U", "edit_file": "E", "open_unit": "O"})
	if err != nil {
		t.Fatal(err)
	}
	ak, ck := km.archKeys(), km.codeKeys()
	got := fmt.Sprint(ak.DepthUp.Keys(), ak.DepthDown.Keys(), ak.DiffOnly.Keys(), ak.Left.Keys(), ak.Right.Keys(),
		km.Search.Keys(), km.Remove.Keys(), km.Command.Keys(), km.Select.Keys(), km.Architecture.Keys(),
		ck.SideBySide.Keys(), ck.EditFile.Keys(), ck.OpenUnit.Keys())
	if got != "[]] [[] [D] [H] [L] [ctrl+f] [X] [;] [v] [4] [U] [E] [O]" {
		t.Errorf("rebound = %s", got)
	}
	def := DefaultKeyMap()
	if def.Select.Help().Key != "space" || fmt.Sprint(def.Select.Keys()) != "[ ]" {
		t.Errorf("select = %v (%q)", def.Select.Keys(), def.Select.Help().Key)
	}
	if km, _ := KeyMapFromConfig(map[string]string{"select": "space"}); fmt.Sprint(km.Select.Keys()) != "[ ]" || km.Select.Help().Key != "space" {
		t.Errorf("select = space: %v", km.Select.Keys())
	}
	acts := " " + strings.Join(KeyActions(), " ") + " "
	for _, a := range []string{"search", "remove", "command", "select", "architecture", "side_by_side", "edit_file",
		"open_unit", "arch_depth_up", "arch_depth_down", "diff_only", "left", "right"} {
		if !strings.Contains(acts, " "+a+" ") {
			t.Errorf("KeyActions missing %s", a)
		}
	}
}

// ---- Code page M5 keys --------------------------------------------------------

func TestCodeLayoutAndOpenKeys(t *testing.T) {
	var opened []string
	type edit struct {
		path string
		line int
	}
	var edits []edit
	o := Options{
		Root:     "/repo",
		Open:     func(u string) error { opened = append(opened, u); return nil },
		EditFile: func(path string, line int) tea.Cmd { edits = append(edits, edit{path, line}); return nil },
	}
	items := threeItems()
	f := newFake(items...)
	f.details["pr:1"] = proto.Detail{Item: items[0]}
	m, p := withFakePane(newTestModel(t, f, o))
	m = feed[detailMsg](t, m, m.selectionChanged())
	m = press(t, m, "2", "enter")
	if k := p.keys; fmt.Sprint(k.SideBySide.Keys(), k.EditFile.Keys(), k.OpenUnit.Keys()) != "[u] [e] [o]" {
		t.Fatalf("code keys %v %v %v", k.SideBySide.Keys(), k.EditFile.Keys(), k.OpenUnit.Keys())
	}
	m = press(t, m, "u")
	_, mw, _ := m.dims()
	want := fmt.Sprintf("side-by-side needs 140 columns (pane has %d) — showing unified", mw)
	if p.layout != codeview.LayoutSplit || m.note != want {
		t.Errorf("u: layout %v note %q", p.layout, m.note)
	}
	if s := m.viewStatus(); s != want {
		t.Errorf("status %q", s)
	}
	m = press(t, m, "u")
	if p.layout != codeview.LayoutUnified || m.note != "diff: unified" {
		t.Errorf("u again: layout %v note %q", p.layout, m.note)
	}
	p.note = "side-by-side needs ≥140 columns"
	m = press(t, m, "u")
	if m.note != p.note {
		t.Errorf("the page's own note: %q", m.note)
	}

	p.unit, p.unitOK = model.ChangeUnit{ID: "u-a", Path: "web/a.ts"}, true
	m = pressRun(t, m, "e")
	if len(edits) != 1 || edits[0] != (edit{filepath.Join("/repo", "web/a.ts"), 12}) {
		t.Errorf("e: edits %+v", edits)
	}
	m = pressRun(t, m, "o")
	if fmt.Sprint(opened) != "[https://github.com/o/r/pull/1/files]" || m.note != "opened web/a.ts:12 on GitHub" {
		t.Errorf("o: opened %v note %q", opened, m.note)
	}
}

func TestCodeOpenWithoutURLOrOpener(t *testing.T) {
	items := threeItems()
	f := newFake(items...)
	m, p := withFakePane(newTestModel(t, f, Options{}))
	m = feed[detailMsg](t, m, m.selectionChanged())
	p.unit, p.unitOK = model.ChangeUnit{ID: "u-a", Path: "web/a.ts"}, true
	m = press(t, m, "2", "enter", "o")
	if m.note != "no opener configured" {
		t.Errorf("note %q", m.note)
	}
}

// ---- c checkout from every tab ------------------------------------------------

func TestCheckoutFromEveryTab(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want int // checkout calls
	}{
		{"picker", nil, 1},
		{"overview", []string{"enter"}, 1},
		{"code", []string{"2", "enter"}, 1},
		{"architecture", []string{"3", "enter", "esc", "enter"}, 1},
		{"pipeline: c is the call log", []string{"P"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(threeItems()...)
			m, _ := withFakePane(newTestModel(t, f, Options{}))
			m, pp := withFakePipe(m)
			m, a := withFakeArch(m)
			pp.actions["c"] = pipeview.Action{Kind: pipeview.ActCallLog, GroupID: "g1"}
			m = feed[detailMsg](t, m, m.selectionChanged())
			m = press(t, m, tt.keys...)
			if tt.name == "architecture" && (a.capturing || m.tab != TabArch || m.focus != FocusMain) {
				t.Fatalf("setup: capturing %v tab %v focus %v", a.capturing, m.tab, m.focus)
			}
			m = pressRun(t, m, "c")
			if len(f.checkouts) != tt.want {
				t.Errorf("checkouts %v, want %d", f.checkouts, tt.want)
			}
			if tt.want == 1 && m.note != "checked out feat/x" {
				t.Errorf("note %q", m.note)
			}
		})
	}
}

// ---- help completeness --------------------------------------------------------

// specKeys encodes the spec's keybinding table (Keybindings and
// navigation) plus the Code page and Pipeline page tables and the
// Architecture tab's keys (design v2): scope → keys → action words.
// Scopes: picker, item (any tab of an item), global, overview, code,
// pipeline, arch.
//
// Rows that intentionally differ in meaning (DECISIONS): on the Code page g
// toggles by file / by change (home is top) and o opens the unit on GitHub;
// S on a branch without a PR creates one; on the Pipeline page r re-runs
// the stage and c opens the call log (refresh and checkout are not reachable
// there); a on the Code page accepts an AI finding first; space is page
// down outside the picker.
var specKeys = []struct {
	scope string
	keys  []string
	words string
}{
	{"picker", []string{"j", "k", "g", "G"}, "move, top, bottom"},
	{"picker", []string{"/"}, "search and add"},
	{"picker", []string{"x"}, "remove an added item"},
	{"picker", []string{"s"}, "cycle sort"},
	{"picker", []string{" ", "esc"}, "multi-select, clear"},
	{"item", []string{"r"}, "refresh"},
	{"item", []string{"1", "2", "3"}, "Overview, Code, Architecture tabs"},
	{"item", []string{"P"}, "Pipeline page"},
	{"global", []string{"tab", "shift+tab"}, "cycle focus"},
	{"overview", []string{"enter"}, "jump to the unit behind the focused block"},
	{"overview", []string{"L"}, "raw labels"},
	{"code", []string{"L"}, "raw labels"},
	{"code", []string{"!"}, "mark a label wrong"},
	{"overview", []string{"w"}, "urgency weights"},
	{"item", []string{"c"}, "git checkout"},
	{"item", []string{"a", "A"}, "approve, request changes"},
	{"item", []string{"o"}, "open on GitHub"},
	{"global", []string{":"}, "command line"},
	{"global", []string{"?"}, "help overlay"},
	{"global", []string{"q", "ctrl+c"}, "quit"},
	{"code", []string{"g"}, "toggle by file / by change"},
	{"code", []string{"u"}, "unified / side-by-side"},
	{"code", []string{"n", "p"}, "next / previous unit"},
	{"code", []string{"J", "K"}, "next / previous file or group"},
	{"code", []string{"f", "1", "9"}, "open filters, toggle one"},
	{"code", []string{"z"}, "expand collapsed noise"},
	{"code", []string{"C", "V"}, "comment on line, line selection"},
	{"code", []string{"a", "d", "!"}, "accept / dismiss AI finding, override label"},
	{"code", []string{"t"}, "thread panel"},
	{"code", []string{"S"}, "submit review"},
	{"code", []string{"M", "X"}, "merge groups, split a group"},
	{"code", []string{"R"}, "re-review on deep"},
	{"code", []string{"e", "o"}, "open in $EDITOR, open the unit on GitHub"},
	{"pipeline", []string{"D", "s", "r", "e", "c", "$"}, "force deep, skip, re-run, edit rules, call log, cost cap"},
	{"arch", []string{"h", "j", "k", "l", "enter", "+", "-", "d"}, "move focus, files, depth, diff-only"},
}

// scopeScreens maps a spec scope to the help screens it must appear in.
var scopeScreens = map[string][]string{
	"picker":   {"picker"},
	"item":     {"picker", "overview", "code", "pipeline", "arch"},
	"global":   {"picker", "overview", "code", "pipeline", "arch"},
	"overview": {"overview"},
	"code":     {"code"},
	"pipeline": {"pipeline"},
	"arch":     {"arch"},
}

func TestHelpListsEverySpecKey(t *testing.T) {
	items := threeItems()
	f := newFake(items...)
	f.details["pr:1"] = proto.Detail{Item: items[0]}
	m := newTestModel(t, f, Options{}) // the real Code, Pipeline and Architecture pages
	m = feed[detailMsg](t, m, m.selectionChanged())
	screens := map[string]struct {
		focus Focus
		tab   Tab
	}{
		"picker": {FocusPicker, TabOverview}, "overview": {FocusMain, TabOverview}, "code": {FocusMain, TabCode},
		"pipeline": {FocusMain, TabPipeline}, "arch": {FocusMain, TabArch},
	}
	keysOf := map[string]map[string]bool{}
	for name, s := range screens {
		m.focus, m.tab = s.focus, s.tab
		_, groups := m.helpGroups()
		ks := map[string]bool{}
		for _, g := range groups {
			for _, b := range g {
				if !b.Enabled() {
					continue
				}
				for _, k := range b.Keys() {
					ks[k] = true
				}
			}
		}
		keysOf[name] = ks
	}
	for _, row := range specKeys {
		for _, screen := range scopeScreens[row.scope] {
			for _, k := range row.keys {
				if !keysOf[screen][k] {
					t.Errorf("help · %s lacks %q (%s: %s)", screen, k, row.scope, row.words)
				}
			}
		}
	}
}

// Every help binding is reachable: the overlay's rows render without
// escape codes and list each group.
func TestHelpOverlayPerScreen(t *testing.T) {
	m := newTestModel(t, newFake(threeItems()...), Options{})
	m = feed[detailMsg](t, m, m.selectionChanged())
	for _, tt := range []struct {
		keys []string
		want string
	}{
		{[]string{"?"}, "Keys · picker"},
		{[]string{"enter", "?"}, "Keys · overview"},
		{[]string{"enter", "2", "?"}, "Keys · code"},
		{[]string{"enter", "3", "?"}, "Keys · architecture"},
		{[]string{"enter", "P", "?"}, "Keys · pipeline"},
	} {
		m2 := press(t, m, tt.keys...)
		v := m2.View()
		if !strings.Contains(v, tt.want) || strings.Contains(v, "\x1b[") {
			t.Errorf("%v: help lacks %q:\n%s", tt.keys, tt.want, v)
		}
	}
}

// ---- hints, colour, snapshot --------------------------------------------------

func TestKeyHintBar(t *testing.T) {
	m := newTestModel(t, newFake(threeItems()...), Options{})
	s := m.viewStatus()
	for _, want := range []string{"j/k move", "space select", "/ search and add", "1 2 3 tabs"} {
		if !strings.Contains(s, want) {
			t.Errorf("picker hints lack %q: %q", want, s)
		}
	}
	m = press(t, m, "enter")
	s = m.viewStatus()
	for _, want := range []string{"j/k scroll", "enter jump to unit", "L raw labels", "w urgency weights", "a approve"} {
		if !strings.Contains(s, want) {
			t.Errorf("overview hints lack %q: %q", want, s)
		}
	}
	m = press(t, m, "x") // not a key here: no note
	m.note = "a note"
	if m.viewStatus() != "a note" {
		t.Errorf("a note replaces the hints: %q", m.viewStatus())
	}
}

func TestColourRenderer(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	r.SetHasDarkBackground(true)
	items := []model.Item{
		{ID: "pr:1", Kind: model.KindPR, Number: 1, Title: "one", Section: model.SectionNeedsYou, Urgency: 9, UrgencyQuartile: 1, State: model.StateReady},
		{ID: "pr:2", Kind: model.KindPR, Number: 2, Title: "two", Section: model.SectionNeedsYou, Urgency: 1, UrgencyQuartile: 4, State: model.StateReady},
	}
	f := newFake(items...)
	m := NewWithOptions(f, r, Options{Now: func() time.Time { return fixedNow }})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = update(t, m, itemsMsg{items: items})
	v := m.View()
	if !strings.Contains(v, "\x1b[") {
		t.Fatal("a TrueColor renderer drew no colour")
	}
	th := theme.New(r)
	if !strings.Contains(v, th.UrgencyBar(4)) {
		t.Error("the urgency bar is not theme.UrgencyBar (accent cells)")
	}
	if !strings.Contains(v, th.Accent.Bold(true).Render("prt")) {
		t.Error("the header's prt is not in the accent colour")
	}
	if !strings.Contains(v, th.Tab("1 Overview", true)) {
		t.Error("the active tab is not theme.Tab")
	}
}

func TestSnapshotWithFakeClient(t *testing.T) {
	items := threeItems()
	f := newFake(items...)
	f.details["pr:1"] = proto.Detail{Item: items[0], Agg: &model.ItemAgg{}}
	f.hits = map[proto.SearchSource][]proto.SearchHit{proto.SearchLocal: {
		{Kind: model.KindPR, Number: 1, Title: "one", Query: "#1", ItemID: "pr:1", Listed: true},
	}}
	opts := Options{Now: func() time.Time { return fixedNow }, Repo: "o/r"}
	var a, b bytes.Buffer
	if err := SnapshotWithOptions(context.Background(), f, &a, 120, 30, 1, opts); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotWithOptions(context.Background(), f, &b, 120, 30, 1, opts); err != nil {
		t.Fatal(err)
	}
	s := a.String()
	if s != b.String() {
		t.Error("snapshot is not byte-stable")
	}
	if strings.Contains(s, "\x1b[") {
		t.Error("snapshot has escape codes")
	}
	for _, want := range []string{
		"=== screen: picker + overview · pr:1 ===", "=== screen: picker + code-split · pr:1 ===",
		"=== screen: picker + architecture · pr:1 ===", "=== screen: search · #1 ===", "prt · o/r · 2 PRs · 1 branch",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("snapshot lacks %q", want)
		}
	}
	f.searchErr = fmt.Errorf("no search")
	var c bytes.Buffer
	if err := SnapshotWithOptions(context.Background(), f, &c, 120, 30, 1, opts); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.String(), "=== screen: search") {
		t.Error("search screen drawn although Search fails")
	}
}
