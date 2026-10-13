package tui

import (
	"fmt"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/surface"
	"github.com/fredricw/prt/internal/theme"
)

// richDetail is detail_full with every list block longer than its cap and
// a change surface.
func richDetail(t *testing.T) proto.Detail {
	t.Helper()
	d := loadDetail(t, "detail_full.json")
	a := d.Agg
	units := []string{"u-refresh", "u-expired", "u-devices", "u-test", "u-server", "u-format"}
	a.Headlines = []model.Headline{
		{UnitID: "u-server", Path: "server/api/session.go", Line: 20, Kind: "signature", Text: "Refresh(ctx, id)", P: 0.95},
		{UnitID: "u-refresh", Path: "web/src/session.ts", Line: 12, Kind: "breaking", Text: "refresh(token)", P: 0.70},
		{UnitID: "u-expired", Path: "web/src/session.ts", Line: 60, Kind: "breaking", Text: "isExpired()", P: 0.90},
	}
	for i := range 5 {
		a.Headlines = append(a.Headlines, model.Headline{UnitID: units[i%len(units)], Path: "x.go", Line: i + 1,
			Kind: "env/flag", Text: fmt.Sprintf("FLAG_%d", i), P: 0.8 - float64(i)/100})
	}
	a.HeadlinesUnsure = 3
	a.RiskTags = map[string]int{"auth": 5, "async": 4, "data_write": 3, "money": 2, "pii": 1, "io_in_loop": 1}
	a.Bullets = nil
	for i := range 6 {
		a.Bullets = append(a.Bullets, model.BulletAgg{Index: i + 1, Text: fmt.Sprintf("bullet %d", i+1), Implemented: i%2 == 0,
			UnitIDs: []string{units[i%len(units)]}, P: 0.9})
	}
	a.Files = nil
	for i := range 10 {
		a.Files = append(a.Files, model.FileAgg{Path: fmt.Sprintf("web/f%d.ts", i), Owned: true, UnitIDs: []string{units[i%len(units)]}})
	}
	a.Slice = nil
	for i := range 6 {
		a.Slice = append(a.Slice, model.SliceRow{Module: fmt.Sprintf("web/m%d/*", i), Owned: true, Units: 2, Eyes: 1})
	}
	a.Surface = &model.ChangeSurface{API: []model.SurfaceEntry{}, DB: []model.SurfaceEntry{}, Flags: []model.SurfaceEntry{
		{Area: model.SurfaceFlag, Change: model.SurfaceAdded, What: "flag", Name: "user-preferences-v2",
			Path: "web/src/flags.ts", Line: 4, UnitID: "u-devices", Confidence: 0.92, Source: model.SurfaceFromCode},
	}, Note: "head not readable: diff only"}
	a.Surface.API = append(a.Surface.API,
		model.SurfaceEntry{Area: model.SurfaceAPI, Change: model.SurfaceRemoved, What: "route", Name: "GET /api/v1/health",
			Path: "server/api/health.go", Line: 12, UnitID: "u-server", Confidence: 0.97, Source: model.SurfaceFromCode},
		model.SurfaceEntry{Area: model.SurfaceAPI, Change: model.SurfaceAdded, What: "route", Name: "GET /api/v1/users/{id}/preferences",
			Path: "server/api/prefs.go", Line: 30, UnitID: "u-refresh", Confidence: 0.55, Source: model.SurfaceFromJev},
	)
	for i := range 4 {
		a.Surface.API = append(a.Surface.API, model.SurfaceEntry{Area: model.SurfaceAPI, Change: model.SurfaceChanged, What: "route",
			Name: fmt.Sprintf("POST /api/v1/x%d", i), Path: "server/api/x.go", Line: 40 + i, UnitID: "u-test", Confidence: 0.8})
	}
	return d
}

// section cuts box title's lines out of a rendered page (one column).
func section(page, title string) string {
	i := strings.Index(page, "─ "+title+" ")
	if i < 0 {
		return ""
	}
	rest := page[i:]
	if j := strings.Index(rest, "└"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func renderAt(d proto.Detail, w int) string {
	r := plainRenderer()
	p := overviewPage(newStyles(r), newMarkdown(r), d, fixedNow, w)
	return strings.Join(p.lines, "\n")
}

func TestBlockCaps(t *testing.T) {
	d := richDetail(t)
	page := renderAt(d, 90)
	tests := []struct {
		title string
		want  []string
		lacks []string
	}{
		{blockHeadlines, []string{"+4 more", "+3 below .60 (unsure)"}, []string{"FLAG_2"}},
		{blockCheck, []string{"+1 more"}, []string{"clock skew"}},
		{blockRisk, []string{"auth ×5 · async ×4 · data write ×3 · money ×2", "+2 more", "test gap"}, []string{"pii ×1"}},
		{blockFidelity, []string{"✓ 3 of 6 bullets implemented", "✗ not found    bullet 2", "+4 more", "coherence: mixed"}, nil},
		{blockMinimap, []string{"web/f5.ts", "+4 files", "█ eyes ▒ skim · skip □ unsure"}, []string{"web/f6.ts"}},
		{blockSlice, []string{"web/m3/*", "+2 more"}, []string{"web/m4/*"}},
		{blockAPI, []string{"+2 more", "head not readable: diff only"}, []string{"POST /api/v1/x2"}},
		{blockDB, []string{surface.Empty(model.SurfaceDB)}, nil},
		{blockFlags, []string{"user-preferences-v2", "web/src/flags.ts:4"}, nil},
	}
	for _, tt := range tests {
		sec := section(page, tt.title)
		if sec == "" {
			t.Errorf("no %q box:\n%s", tt.title, page)
			continue
		}
		for _, w := range tt.want {
			if !strings.Contains(sec, w) {
				t.Errorf("%s lacks %q:\n%s", tt.title, w, sec)
			}
		}
		for _, w := range tt.lacks {
			if strings.Contains(sec, w) {
				t.Errorf("%s shows %q past its cap:\n%s", tt.title, w, sec)
			}
		}
	}
	// Headlines: breaking first (highest P first), then by P.
	hl := section(page, blockHeadlines)
	a, b, c := strings.Index(hl, "isExpired()"), strings.Index(hl, "refresh(token)"), strings.Index(hl, "Refresh(ctx, id)")
	if ordered := a >= 0 && a < b && b < c; !ordered {
		t.Errorf("headline order wrong:\n%s", hl)
	}
	// No box grows past its cap: every box stays short.
	r := plainRenderer()
	p := overviewPage(newStyles(r), newMarkdown(r), d, fixedNow, 90)
	for title, rc := range p.rects {
		if title != blockDescription && rc.y1-rc.y0+1 > 12 {
			t.Errorf("%s is %d lines tall", title, rc.y1-rc.y0+1)
		}
	}
	// The full lists hold every row.
	st := newStyles(r)
	for title, want := range map[string]string{blockHeadlines: "FLAG_4", blockRisk: "pii ×1", blockMinimap: "web/f9.ts",
		blockSlice: "web/m5/*", blockAPI: "POST /api/v1/x3", blockFidelity: "bullet 6", blockCheck: "clock skew"} {
		full, ok := fullBlock(st, d, defaultOvOpts(), title, 86)
		if !ok || !strings.Contains(strings.Join(full.lines, "\n"), want) {
			t.Errorf("full %s list lacks %q", title, want)
		}
	}
}

func TestGitFactsFilesCapped(t *testing.T) {
	d := richDetail(t)
	d.Agg.LabelsMissing = true
	for i := range 3 {
		d.Files = append(d.Files, model.File{Path: fmt.Sprintf("extra%d.go", i), Adds: 1})
	}
	page := renderAt(d, 90)
	files := section(page, blockFiles)
	if !strings.Contains(files, "+3 more") || strings.Contains(files, "extra0.go") {
		t.Errorf("Files not capped:\n%s", files)
	}
	// The change blocks need no labels: they show on the git-facts page.
	if !strings.Contains(section(page, blockAPI), "GET /api/v1/health") || !strings.Contains(section(page, blockRisk), "needs labels") {
		t.Errorf("git facts page:\n%s", page)
	}
}

// blockModel is a model on richDetail with the main pane focused and the
// recording Code pane.
func blockModel(t *testing.T, km *KeyMap) (Model, *fakePane) {
	t.Helper()
	d := richDetail(t)
	f := newFake(d.Item)
	f.details[d.Item.ID] = d
	m, p := withFakePane(newTestModel(t, f, Options{Keys: km}))
	m = update(t, m, tea.WindowSizeMsg{Width: 130, Height: 40})
	m = feed[detailMsg](t, m, m.selectionChanged())
	m = update(t, m, keyMsg("enter"))
	if m.focus != FocusMain || m.detail == nil {
		t.Fatal("setup")
	}
	return m, p
}

func TestBlockFocusAndOverlayKeys(t *testing.T) {
	m, p := blockModel(t, nil)
	m = press(t, m, "J")
	if m.overview.focus != blockHeadlines {
		t.Fatalf("J focused %q", m.overview.focus)
	}
	if !strings.Contains(m.View(), "┏━ "+blockHeadlines) {
		t.Errorf("the focused box is not drawn focused:\n%s", m.View())
	}
	m = press(t, m, "J")
	if m.overview.focus != blockCheck {
		t.Errorf("second J focused %q", m.overview.focus)
	}
	m = press(t, m, "K", "enter")
	l := m.overview.list
	if l == nil || l.block != blockHeadlines || !m.overview.raw {
		t.Fatalf("enter did not open the Headlines list: %+v", l)
	}
	v := m.View()
	for _, want := range []string{"FLAG_4", "isExpired()", "▸ "} {
		if !strings.Contains(v, want) {
			t.Errorf("list lacks %q:\n%s", want, v)
		}
	}
	if !strings.Contains(m.viewStatus(), "jump to unit") {
		t.Errorf("status %q", m.viewStatus())
	}
	m = press(t, m, "j", "enter")
	if m.tab != TabCode || fmt.Sprint(p.focused) != "[u-refresh]" || m.overview.raw {
		t.Fatalf("enter on row 2: tab %v focused %v raw %v", m.tab, p.focused, m.overview.raw)
	}
	// Back on the Overview the block is still focused; J in the list moves
	// to the next block's list; esc closes, then unfocuses, then leaves.
	m = press(t, m, "1", "enter", "J")
	if m.overview.list == nil || m.overview.list.block != blockCheck {
		t.Fatalf("J in the list: %+v", m.overview.list)
	}
	m = press(t, m, "esc")
	if m.overview.raw || m.overview.focus != blockCheck || m.focus != FocusMain {
		t.Errorf("esc: raw %v focus %q pane %v", m.overview.raw, m.overview.focus, m.focus)
	}
	m = press(t, m, "esc")
	if m.overview.focus != "" || m.focus != FocusMain {
		t.Errorf("second esc: block %q pane %v", m.overview.focus, m.focus)
	}
	m = press(t, m, "esc")
	if m.focus != FocusPicker {
		t.Errorf("third esc: pane %v", m.focus)
	}
}

func TestSpaceAndEnterWithoutBlockFocus(t *testing.T) {
	m, p := blockModel(t, nil)
	m = press(t, m, " ")
	if m.overview.vp.YOffset == 0 || m.overview.raw {
		t.Errorf("space without a focused block: offset %d raw %v", m.overview.vp.YOffset, m.overview.raw)
	}
	m.overview.vp.GotoTop()
	m = press(t, m, "enter")
	if m.tab != TabCode || len(p.focused) != 1 {
		t.Errorf("enter without a focused block: tab %v focused %v", m.tab, p.focused)
	}
	m = press(t, m, "1", "J", " ")
	if m.overview.list == nil {
		t.Error("space on a focused block did not open its list")
	}
}

func TestBlockMouse(t *testing.T) {
	m, p := blockModel(t, nil)
	sw, _, _ := m.dims()
	at := func(m Model, sub string) (x, y int) {
		for i, l := range strings.Split(m.overview.view(), "\n") {
			if c := strings.Index(l, sub); c >= 0 {
				return sw + 2 + ansi.StringWidth(l[:c]) + 1, i + 2
			}
		}
		t.Fatalf("no %q in view:\n%s", sub, m.overview.view())
		return 0, 0
	}
	// "+4 more" under Headlines opens its list.
	x, y := at(m, "+4 more")
	m = update(t, m, mouseClick(x, y))
	if m.overview.list == nil || m.overview.list.block != blockHeadlines {
		t.Fatalf("click on +4 more: %+v", m.overview.list)
	}
	// A row of the list opens its unit.
	_, y = at(m, "isExpired()")
	m = update(t, m, mouseClick(sw+4, y))
	if m.tab != TabCode || fmt.Sprint(p.focused) != "[u-expired]" {
		t.Fatalf("click on a list row: tab %v focused %v", m.tab, p.focused)
	}
	// A click on a box's border focuses it; on a row it still jumps.
	m = press(t, m, "1")
	m.overview.vp.GotoTop()
	m.overview.focus = ""
	m.redrawOverview()
	r := m.overview.rects[blockCheck]
	m = update(t, m, mouseClick(sw+2+r.x0+1, r.y0+2))
	if m.overview.focus != blockCheck {
		t.Errorf("click on the Check border focused %q", m.overview.focus)
	}
	x, y = at(m, "refresh(token)")
	m = update(t, m, mouseClick(x, y))
	if fmt.Sprint(p.focused) != "[u-expired u-refresh]" {
		t.Errorf("click on a headline row: %v", p.focused)
	}
}

func TestBlockKeysRebindableAndInHelp(t *testing.T) {
	km, err := KeyMapFromConfig(map[string]string{"next_block": "N", "prev_block": "B", "open_block": "x"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := blockModel(t, &km)
	m = press(t, m, "N", "x")
	if m.overview.list == nil || m.overview.list.block != blockHeadlines {
		t.Fatalf("rebound block keys: focus %q list %+v", m.overview.focus, m.overview.list)
	}
	m = press(t, m, "esc", "?")
	v := m.View()
	for _, want := range []string{"next block", "previous block", "open block list"} {
		if !strings.Contains(v, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	if s := newTestModel(t, newFake(threeItems()...), Options{}); !strings.Contains(press(t, s, "enter").viewStatus(), "J/K blocks") {
		t.Errorf("overview hints lack J/K blocks: %q", press(t, s, "enter").viewStatus())
	}
}

// L on a focused block shows its first unit's raw labels.
func TestBlockRawLabels(t *testing.T) {
	m, _ := blockModel(t, nil)
	m = press(t, m, "J", "J")
	m, cmd := updateCmd(t, m, keyMsg("L"))
	if !m.overview.raw || m.overview.rawUnit != "u-refresh" {
		t.Fatalf("L on Check: raw %v unit %q", m.overview.raw, m.overview.rawUnit)
	}
	_ = cmd
}

// ---- (E) composition bar and legends ----------------------------------------

func trueColour() *lipgloss.Renderer {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	r.SetHasDarkBackground(true)
	return r
}

func TestCompositionColour(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	segs := compositionSegments(d.Agg)
	th := theme.New(trueColour())
	bar := compositionBar(th, segs, 40)
	shares := make([]float64, len(segs))
	for i, s := range segs {
		shares[i] = s.share
	}
	for i, n := range cellWidths(shares, 40) {
		want := th.Kind(segs[i].name).Render(strings.Repeat(barCell, n))
		if !strings.Contains(bar, want) {
			t.Errorf("bar lacks the %s segment in its colour (%q):\n%q", segs[i].name, want, bar)
		}
	}
	if !strings.Contains(bar, "38;2;58;65;77") { // the design's noise #3a414d
		t.Errorf("noise is not in the noise colour: %q", bar)
	}
	if got := ansi.Strip(bar); got != strings.Repeat(barCell, 40) {
		t.Errorf("bar cells %q", got)
	}
	legend := compositionLegend(th, segs)
	if got := ansi.Strip(legend); got != "■ refactor 50%  ■ feature 32%  ■ noise 10%  ■ test 9%" {
		t.Errorf("legend %q", got)
	}
	for _, s := range segs {
		if !strings.Contains(legend, th.Kind(s.name).Render(legendKey)) {
			t.Errorf("legend square of %s not in its colour", s.name)
		}
	}
	// Without colour: letters and ░, the plain legend.
	plain := newStyles(plainRenderer()).th
	if got := compositionBar(plain, segs, 40); strings.ContainsAny(got, barCell+"\x1b") || !strings.Contains(got, noiseGlyph) {
		t.Errorf("plain bar %q", got)
	}
}

func TestMinimapColour(t *testing.T) {
	th := theme.New(trueColour())
	if got, want := minimapCell(th, model.UnitAgg{Attention: model.AttnEyes}), th.Attention(model.AttnEyes).Render("■"); got != want {
		t.Errorf("eyes cell %q, want %q", got, want)
	}
	if got, want := minimapCell(th, model.UnitAgg{Attention: model.AttnSkip, Unsure: true}), th.Blue.Render("□"); got != want {
		t.Errorf("unsure cell %q, want %q", got, want)
	}
	legend := minimapLegend(th)
	if got := ansi.Strip(legend); got != "■ eyes ■ skim ■ skip □ unsure" {
		t.Errorf("legend %q", got)
	}
	for _, a := range []model.Attention{model.AttnEyes, model.AttnSkim, model.AttnSkip} {
		if !strings.Contains(legend, th.Attention(a).Render("■")) {
			t.Errorf("legend %s square not in its colour", a)
		}
	}
	plain := newStyles(plainRenderer()).th
	if got := minimapLegend(plain); got != "█ eyes ▒ skim · skip □ unsure" {
		t.Errorf("plain legend %q", got)
	}
	if got := minimapCell(plain, model.UnitAgg{Attention: model.AttnSkim}); got != codeview.AttentionGlyph(model.AttnSkim, false) {
		t.Errorf("plain cell %q", got)
	}
}

// ---- (F) structured change blocks ---------------------------------------------

func TestSurfaceBlockRows(t *testing.T) {
	d := richDetail(t)
	page := renderAt(d, 90)
	api := section(page, blockAPI)
	rows := surface.Rows(d.Agg.Surface.API)
	for i, r := range rows[:capSurface] {
		if !strings.Contains(api, r.Text) || !strings.Contains(api, r.Loc+"  "+codeview.ProbText(r.Confidence)) {
			t.Errorf("row %d (%+v) not drawn:\n%s", i, r, api)
		}
	}
	unsure := rows[1]
	if !strings.Contains(api, "? "+unsure.Glyph+" "+unsure.Text) {
		t.Errorf("the .55 row lacks the ? gutter:\n%s", api)
	}
	if !strings.Contains(api, "  "+rows[0].Glyph+" "+rows[0].Text) {
		t.Errorf("first row:\n%s", api)
	}
	// Not computed yet (an analysis from before M5.2), with the refresh key.
	d.Agg.Surface = nil
	km, err := KeyMapFromConfig(map[string]string{"refresh": "R"})
	if err != nil {
		t.Fatal(err)
	}
	o := ovOpts{runKey: km.Refresh.Help().Key}
	r := plainRenderer()
	page = strings.Join(overviewPageWith(newStyles(r), newMarkdown(r), d, fixedNow, 90, o).lines, "\n")
	for _, title := range []string{blockAPI, blockDB, blockFlags} {
		if !strings.Contains(section(page, title), "not computed — R refreshes") {
			t.Errorf("%s with no surface:\n%s", title, section(page, title))
		}
	}
	// Empty lists say so explicitly.
	d.Agg.Surface = &model.ChangeSurface{API: []model.SurfaceEntry{}, DB: []model.SurfaceEntry{}, Flags: []model.SurfaceEntry{}}
	page = renderAt(d, 90)
	for title, area := range map[string]string{blockAPI: model.SurfaceAPI, blockDB: model.SurfaceDB, blockFlags: model.SurfaceFlag} {
		if !strings.Contains(section(page, title), surface.Empty(area)) {
			t.Errorf("%s empty state:\n%s", title, section(page, title))
		}
	}
}

func TestSurfaceBlocksOnBranchesAndJump(t *testing.T) {
	b := branchDetail(t)
	b.Agg.Surface = richDetail(t).Agg.Surface
	if !strings.Contains(section(renderAt(b, 90), blockAPI), "GET /api/v1/health") {
		t.Errorf("branch overview lacks the change blocks:\n%s", renderAt(b, 90))
	}
	// enter on a row (viewport top) and a click on it open its unit.
	m, p := blockModel(t, nil)
	row := lineOf(t, m, "GET /api/v1/health")
	m.overview.vp.SetYOffset(row)
	m = press(t, m, "enter")
	if m.tab != TabCode || fmt.Sprint(p.focused) != "[u-server]" {
		t.Fatalf("enter on the API row: tab %v focused %v", m.tab, p.focused)
	}
	m = press(t, m, "1")
	m.overview.vp.SetYOffset(row)
	sw, _, _ := m.dims()
	y := row - m.overview.vp.YOffset + 2
	m = update(t, m, mouseClick(sw+6, y))
	if fmt.Sprint(p.focused) != "[u-server u-server]" {
		t.Errorf("click on the API row: %v", p.focused)
	}
}
