package codeview

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/theme"
)

// splitModel is the detail fixture at w×h with every filter off and the
// side-by-side layout requested.
func splitModel(t *testing.T, d proto.Detail, w, h int) Model {
	t.Helper()
	m := New(plainRenderer())
	m.SetSize(w, h)
	m.SetDiffLayout(LayoutSplit)
	m.SetDetail(d)
	allOff(&m)
	return m
}

// oneUnit is a detail with a single unit of diff body (no hunk header) in
// path, numbered from line 1 on both sides.
func oneUnit(path, body string) proto.Detail {
	old, nw := 0, 0
	for _, l := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "-"):
			old++
		case strings.HasPrefix(l, "+"):
			nw++
		default:
			old++
			nw++
		}
	}
	u := model.ChangeUnit{ID: "s1", Path: path, Diff: body, Kind: model.UnitModified,
		Range: model.Range{Old: model.Span{Start: 1, Count: old}, New: model.Span{Start: 1, Count: nw}}}
	return proto.Detail{Item: model.Item{ID: "pr:50"}, Units: []model.ChangeUnit{u},
		Files: []model.File{{Path: path, Owned: true}}}
}

func TestSplitPairs(t *testing.T) {
	tests := []struct {
		name, body string
		want       []srow // parts unset
	}{
		{"deletions then additions", "-a\n-b\n+c\n+d", []srow{{left: 0, right: 2}, {left: 1, right: 3}}},
		{"more deletions", "-a\n-b\n-c\n+d", []srow{{left: 0, right: 3}, {left: 1, right: -1}, {left: 2, right: -1}}},
		{"more additions", "-a\n+b\n+c", []srow{{left: 0, right: 1}, {left: -1, right: 2}}},
		{"pure addition", "+a\n+b", []srow{{left: -1, right: 0}, {left: -1, right: 1}}},
		{"pure deletion", "-a\n-b", []srow{{left: 0, right: -1}, {left: 1, right: -1}}},
		{"context on both sides", " a\n-b\n+c\n d", []srow{{left: 0, right: 0}, {left: 1, right: 2}, {left: 3, right: 3}}},
		{"additions before deletions do not pair", "+a\n-b", []srow{{left: -1, right: 0}, {left: 1, right: -1}}},
		{"runs pair within their hunk run only", "-a\n+b\n c\n-d\n+e\n+f", []srow{{left: 0, right: 1}, {left: 2, right: 2},
			{left: 3, right: 4}, {left: -1, right: 5}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := model.ChangeUnit{Diff: tt.body, Range: model.Range{Old: model.Span{Start: 1}, New: model.Span{Start: 1}}}
			if got := splitPairs(unitLines(&u)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("pairs = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// splitRows returns the pane content rows of the view that are side-by-side
// rows (their separator at the left column's width).
func splitRows(t *testing.T, m Model) []string {
	t.Helper()
	lay := m.layout()
	g := newSplitGeom(lay.paneW, m.p.gutter)
	var out []string
	for _, l := range strings.Split(m.View(), "\n") {
		pane := ansi.Cut(l, lay.paneX, lay.paneX+lay.paneW)
		if ansi.Cut(pane, g.lw, g.lw+1) == "│" {
			out = append(out, pane)
		}
	}
	return out
}

func TestSplitWrapsLongLines(t *testing.T) {
	long := strings.Repeat("abcdefghij", 30) // 300 cells
	d := oneUnit("notes.txt", " ctx\n-short\n+"+long+"\n end")
	m := splitModel(t, d, 160, 30)
	checkView(t, "split wrap", m.View(), 160, 30)
	lay := m.layout()
	g := newSplitGeom(lay.paneW, m.p.gutter)
	rows := splitRows(t, m)
	wantParts := (300 + g.rt - 1) / g.rt
	if len(rows) != 2+wantParts {
		t.Fatalf("%d split rows, want %d (ctx, %d wrapped, ctx):\n%s", len(rows), 2+wantParts, wantParts, strings.Join(rows, "\n"))
	}
	var right strings.Builder
	for i, r := range rows[1 : 1+wantParts] {
		if w := ansi.StringWidth(r); w != lay.paneW {
			t.Errorf("row %d is %d cells, want %d (columns aligned)", i, w, lay.paneW)
		}
		right.WriteString(strings.TrimSpace(ansi.Cut(r, g.lw+1+splitGutter(m.p.gutter), lay.paneW)))
	}
	if right.String() != long {
		t.Errorf("wrapped text = %q, want the whole line", right.String())
	}
	if !strings.Contains(rows[1], "- short") || strings.TrimSpace(ansi.Cut(rows[2], 0, g.lw)) != "" {
		t.Errorf("the old side is one row and blank below it:\n%s\n%s", rows[1], rows[2])
	}
	if !m.cursorVisible() || m.p.rows[m.curRow].pair != 0 {
		t.Error("cursor not on the pair's first row")
	}
	press(t, &m, "j")
	if pos, _ := m.CursorLine(); pos.Line != 3 || pos.Side != model.SideNew {
		t.Errorf("j skips the continuation rows to the next pair: %+v", pos)
	}
}

func TestSplitDegradesBelowMinWidth(t *testing.T) {
	m := splitModel(t, loadDetail(t), 120, 30)
	if m.DiffLayout() != LayoutSplit || m.layout().split {
		t.Fatalf("layout %v split %v", m.DiffLayout(), m.layout().split)
	}
	if ls := lines(m); ls[0] != noteSplitNarrow {
		t.Errorf("top line = %q, want the degrade note", ls[0])
	}
	if hasKind(m, rowSplit) || !hasKind(m, rowLine) {
		t.Error("degraded layout is not unified")
	}
	m.SetSize(SplitMinWidth, 30)
	if !m.layout().split || !hasKind(m, rowSplit) || hasKind(m, rowLine) {
		t.Fatal("widening to the minimum did not restore side-by-side")
	}
	if strings.Contains(m.View(), noteSplitNarrow) {
		t.Error("degrade note kept at full width")
	}
	m.SetSize(SplitMinWidth-1, 30)
	if m.layout().split {
		t.Error("narrowing kept side-by-side")
	}
}

func hasKind(m Model, k rowKind) bool {
	for _, r := range m.p.rows {
		if r.kind == k {
			return true
		}
	}
	return false
}

func TestLayoutPersists(t *testing.T) {
	m := splitModel(t, loadGroups(t), 160, 40)
	press(t, &m, "n")
	before, _ := m.CursorLine()
	m.SetDetail(loadGroups(t))
	if !hasKind(m, rowSplit) {
		t.Fatal("reload lost side-by-side")
	}
	if after, _ := m.CursorLine(); after != before {
		t.Errorf("reload moved the cursor: %+v → %+v", before, after)
	}
	press(t, &m, "g")
	if m.Mode() != ModeChange || !hasKind(m, rowSplit) {
		t.Fatal("g lost side-by-side")
	}
	press(t, &m, "g")
	if m.Mode() != ModeFile || !hasKind(m, rowSplit) {
		t.Fatal("g back lost side-by-side")
	}
	other := loadDetail(t)
	m.SetDetail(other)
	if m.DiffLayout() != LayoutSplit || !hasKind(m, rowSplit) {
		t.Error("another item reset the layout")
	}
	press(t, &m, "u")
	if m.DiffLayout() != LayoutUnified || hasKind(m, rowSplit) || m.Note() != "layout: unified" {
		t.Errorf("u: layout %v note %q", m.DiffLayout(), m.Note())
	}
	if m.SetDiffLayout(LayoutUnified) {
		t.Error("SetDiffLayout reported a change to the same layout")
	}
	m.Clear()
	if m.DiffLayout() != LayoutUnified {
		t.Error("Clear changed the layout")
	}
}

func TestSplitGutterMarksOnNewSideOnly(t *testing.T) {
	m := splitModel(t, loadDetail(t), 160, 30)
	g := newSplitGeom(m.layout().paneW, m.p.gutter)
	var oldMarks, newMarks string
	for _, r := range splitRows(t, m) {
		oldMarks += ansi.Cut(r, 0, 1)
		newMarks += ansi.Cut(r, g.lw+1, g.lw+2)
	}
	if strings.Trim(oldMarks, " ") != "" {
		t.Errorf("old side mark cells %q, want blank", oldMarks)
	}
	for _, mk := range []string{"~", "◆", "⋯", "·"} {
		if !strings.Contains(newMarks, mk) {
			t.Errorf("new side mark cells %q lack %q", newMarks, mk)
		}
	}
	// ❱ of a thread on a deleted line is drawn on the new side too.
	c := splitModel(t, loadComments(t), 160, 50)
	gc := newSplitGeom(c.layout().paneW, c.p.gutter)
	row := lineWith(t, c.View(), "const res = await fetch(url);")
	pane := ansi.Cut(row, c.layout().paneX, c.layout().paneX+c.layout().paneW)
	if ansi.Cut(pane, 0, 1) != " " || ansi.Cut(pane, gc.lw+1, gc.lw+2) != "❱" {
		t.Errorf("thread mark not on the new side: %q", pane)
	}
}

func TestSplitCursorAndCommentSides(t *testing.T) {
	saveAt := func(t *testing.T, m *Model) model.Comment {
		t.Helper()
		press(t, m, "C")
		typeText(t, m, "x")
		press(t, m, "ctrl+s")
		op, ok := m.PendingComment()
		if !ok || op.Kind != CommentOpSave {
			t.Fatalf("no save (note %q)", m.Note())
		}
		return op.Comment
	}
	// A paired row stands for its new line.
	m := splitModel(t, loadComments(t), 160, 50)
	if pos, _ := m.CursorLine(); pos.Side != model.SideNew || pos.Line != 11 {
		t.Fatalf("start %+v, want new 11 (the pair's new side)", pos)
	}
	if c := saveAt(t, &m); c.Side != model.SideNew || c.Line != 11 {
		t.Errorf("C on a paired row: %s %d", c.Side, c.Line)
	}
	// A deletion with no partner (left only) comments on the old side.
	m = splitModel(t, loadDetail(t), 160, 30)
	selectFile(t, &m, "web/legacy/util.ts")
	if pos, _ := m.CursorLine(); pos.Side != model.SideOld || pos.Line != 12 || pos.UnitID != "u6" {
		t.Fatalf("pure deletion cursor %+v", pos)
	}
	if !strings.Contains(lineWith(t, m.View(), "export function parseCookie"), "▸12 -") {
		t.Error("cursor glyph not in the old column")
	}
	if c := saveAt(t, &m); c.Side != model.SideOld || c.Line != 12 {
		t.Errorf("C on a left-side row: %s %d", c.Side, c.Line)
	}
	// From unified, a cursor on a deleted line stays on the old side.
	u := newModel(t, loadDetail(t), 160, 30)
	allOff(&u)
	if pos, _ := u.CursorLine(); pos.Side != model.SideOld || pos.Line != 11 {
		t.Fatalf("unified start %+v", pos)
	}
	u.SetDiffLayout(LayoutSplit)
	if pos, _ := u.CursorLine(); pos.Side != model.SideOld || pos.Line != 11 || u.rowDeco(u.curRow) != decoCursorOld {
		t.Errorf("switch to split: %+v deco %d", pos, u.rowDeco(u.curRow))
	}
	if c := saveAt(t, &u); c.Side != model.SideOld || c.Line != 11 {
		t.Errorf("C on the old side of a pair: %s %d", c.Side, c.Line)
	}
	// V then C across pairs keeps both ends' sides.
	v := splitModel(t, loadComments(t), 160, 50)
	press(t, &v, "V", "j", "C")
	typeText(t, &v, "range")
	press(t, &v, "ctrl+s")
	op, _ := v.PendingComment()
	if op.Comment.StartLine != 11 || op.Comment.Line != 12 || op.Comment.Side != model.SideNew {
		t.Errorf("range in split = %+v", op.Comment)
	}
}

func TestOpenKeys(t *testing.T) {
	tests := []struct {
		name  string
		prep  func(t *testing.T, m *Model)
		key   string
		want  OpenRequest
		taken bool
	}{
		{"e on a deleted line opens the nearest new line", func(*testing.T, *Model) {}, "e",
			OpenRequest{Path: client, Line: 11, Side: model.SideOld, UnitID: "u1"}, true},
		{"e on an added line", func(t *testing.T, m *Model) { moveTo(t, m, model.SideNew, 12, "") }, "e",
			OpenRequest{Path: client, Line: 12, Side: model.SideNew, UnitID: "u1"}, true},
		{"o opens the unit on GitHub", func(t *testing.T, m *Model) { moveTo(t, m, model.SideNew, 13, "") }, "o",
			OpenRequest{Browser: true, Path: client, Line: 13, Side: model.SideNew, UnitID: "u1"}, true},
		{"e on a collapsed unit opens its first line", func(t *testing.T, m *Model) {
			for range 40 {
				if m.curRow >= 0 && m.p.rows[m.curRow].kind == rowCollapsed {
					return
				}
				press(t, m, "j")
			}
			t.Fatal("no collapsed unit reached")
		}, "e", OpenRequest{Path: client, Line: 32, Side: model.SideNew, UnitID: "u2"}, true},
		{"e on a comment block opens its line", func(t *testing.T, m *Model) { moveTo(t, m, "", 0, "thread:T1") }, "e",
			OpenRequest{Path: client, Line: 11, Side: model.SideOld, UnitID: "u1"}, true},
		{"o without a unit in the pane is not consumed", func(t *testing.T, m *Model) {
			m.SetFilter(FilterSince, true)
			m.SetFilter(FilterComments, true)
			selectFile(t, m, "web/legacy/util.ts")
			m.SetFilter(FilterNeedsEyes, true)
			m.SetFilter(FilterUnsure, true)
		}, "o", OpenRequest{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := commentModel(t, 120, 40)
			tt.prep(t, &m)
			if got := m.HandleKey(keyMsg(tt.key)); got != tt.taken {
				t.Fatalf("consumed %v, want %v (note %q)", got, tt.taken, m.Note())
			}
			got, ok := m.PendingOpen()
			if ok != tt.taken || got != tt.want {
				t.Errorf("PendingOpen = %+v %v, want %+v", got, ok, tt.want)
			}
			if _, again := m.PendingOpen(); again {
				t.Error("PendingOpen did not clear")
			}
		})
	}
	// The plain diff fallback has no cursor: e and o fall through.
	d := loadDetail(t)
	d.Units, d.Diff = nil, plainDiff
	p := newModel(t, d, 120, 30)
	if p.HandleKey(keyMsg("e")) || p.HandleKey(keyMsg("o")) {
		t.Error("e/o consumed in the plain fallback")
	}
}

func TestNewSideLine(t *testing.T) {
	u := model.ChangeUnit{Diff: " a\n-b\n-c\n+d\n e\n-f", Range: model.Range{Old: model.Span{Start: 10}, New: model.Span{Start: 20}}}
	ls := unitLines(&u)
	for li, want := range []int{20, 21, 21, 21, 22, 22} {
		if got := newSideLine(ls, li, 1); got != want {
			t.Errorf("line %d (%c) → %d, want %d", li, ls[li].Op, got, want)
		}
	}
	if got := newSideLine([]diff.Line{{Op: diff.Del, OldNo: 3}}, 0, 7); got != 7 {
		t.Errorf("pure deletion → %d, want the default", got)
	}
}

func TestSyntaxColour(t *testing.T) {
	d := oneUnit("main.go", " package main\n-func old() int { return 1 }\n+func add(a, b int) int { return a + b }")
	r := trueColourRenderer()
	th := theme.New(r)
	for _, layout := range []Layout{LayoutUnified, LayoutSplit} {
		m := New(r)
		m.SetSize(160, 20)
		m.SetDiffLayout(layout)
		m.SetDetail(d)
		v := m.View()
		kw := styledWith(v, sgrColour(r, th.P.SynKeyword))
		if !strings.Contains(kw, "func") || !strings.Contains(kw, "return") || !strings.Contains(kw, "package") {
			t.Errorf("%s: keyword-coloured text %q", layout, kw)
		}
		if fn := styledWith(v, sgrColour(r, th.P.SynFunction)); !strings.Contains(fn, "add") {
			t.Errorf("%s: function-coloured text %q", layout, fn)
		}
	}
	// Ascii: the line is drawn exactly as its text.
	m := newModel(t, d, 160, 20)
	v := m.View()
	if strings.Contains(v, "\x1b") {
		t.Error("escape codes without colour")
	}
	contains(t, v, "    2 + func add(a, b int) int { return a + b }")
	// An unknown language is plain even with colour.
	u := New(r)
	u.SetSize(160, 20)
	u.SetDetail(oneUnit("notes.txt", "+func x() { return }"))
	if kw := styledWith(u.View(), sgrColour(r, th.P.SynKeyword)); kw != "" {
		t.Errorf("unknown language coloured %q", kw)
	}
}

// staleReviews marks g-user's routing row stale after an override.
func staleReviews(d proto.Detail) proto.Detail {
	d = withReviews(d)
	d.Reviews[0].Status = model.ReviewStaleLabels
	d.Reviews[0].StaleNote = "label overridden: risk 4→2 (web/src/api/client.ts:11)"
	d.Reviews[0].Prompts = []string{"security"}
	d.Reviews[0].Ms = 7200
	return d
}

func TestStaleLabels(t *testing.T) {
	m := byChangeModel(t, staleReviews(loadGroups(t)), 160, 60, true)
	if got := m.groupMeta(&m.c.groups[0]); got != "3u 3f · b2 .82 · fast · ⚑ stale labels" {
		t.Errorf("meta = %q", got)
	}
	v := m.View()
	contains(t, v, "    3u 3f · b2 .82 · fast · ⚑ stale l…") // cut at the list's width
	contains(t, v, "routed: fast · security · ⚑ stale labels")
	contains(t, v, "⚑ label overridden: risk 4→2 (web/src/api/client.ts:11)")
	contains(t, v, "findings (3) · ⚑ stale labels")
	contains(t, v, "! high · fast · security · 7.2s")
	press(t, &m, "R")
	if id, ok := m.PendingReview(); !ok || id != "g-user" {
		t.Errorf("R on a stale group: %q %v", id, ok)
	}
	// A stale review without findings still reads as reviewed.
	d := staleReviews(loadGroups(t))
	d.Reviews[0].Findings = nil
	m = byChangeModel(t, d, 160, 60, true)
	contains(t, m.View(), "findings: none · ⚑ stale labels")
	// By file the findings stay inline.
	f := newModel(t, staleReviews(loadGroups(t)), 120, 40)
	allOff(&f)
	contains(t, f.View(), "! [high] opts is forwarded")
}

func TestOverrideFromAnyUnitLine(t *testing.T) {
	m := commentModel(t, 120, 40)
	moveTo(t, &m, model.SideNew, 14, "") // a context line
	press(t, &m, "!")
	if m.picker == nil || m.picker.unit != "u1" {
		t.Fatalf("picker %+v", m.picker)
	}
	var qs []string
	for _, l := range m.picker.labels {
		qs = append(qs, l.Question)
	}
	if got := strings.Join(qs, " "); got != "behaviour breaking kind risk" {
		t.Errorf("picker labels %q (unit scope only, by question)", got)
	}
	press(t, &m, "enter", "enter")
	op, ok := m.PendingComment()
	if !ok || op.Kind != CommentOpOverride || op.Override.Question != "behaviour" {
		t.Fatalf("op %+v", op)
	}
	if !strings.HasPrefix(m.Note(), "override filed — re-aggregating") {
		t.Errorf("note %q", m.Note())
	}
	// Side-by-side too.
	s := splitModel(t, loadComments(t), 160, 50)
	press(t, &s, "!", "enter", "enter")
	if op, ok := s.PendingComment(); !ok || op.Kind != CommentOpOverride {
		t.Errorf("split override op %+v", op)
	}
}

func TestM5Bindings(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 30)
	keys := func(bs ...[]string) string { return fmt.Sprint(bs) }
	var short []string
	for _, b := range m.ShortBindings() {
		short = append(short, b.Help().Key)
	}
	var help []string
	for _, row := range m.HelpBindings() {
		for _, b := range row {
			help = append(help, b.Help().Key+"="+b.Help().Desc)
		}
	}
	for _, k := range []string{"u", "e", "o"} {
		if !strings.Contains(" "+strings.Join(short, " ")+" ", " "+k+" ") {
			t.Errorf("short hints %s lack %q", keys(short), k)
		}
	}
	h := strings.Join(help, ",")
	for _, want := range []string{"u=unified / side-by-side", "e=open in $EDITOR", "o=open unit on GitHub"} {
		if !strings.Contains(h, want) {
			t.Errorf("help lacks %q: %s", want, h)
		}
	}
}

func TestSplitViewDimensions(t *testing.T) {
	tricky := func(d *proto.Detail) {
		d.Units[0].Diff = "+\t日本語🙂 \x1b[2J" + strings.Repeat("x", 400) + "\n-" + strings.Repeat("y", 400) + "\n " + strings.Repeat("語", 120)
	}
	for _, r := range []*lipgloss.Renderer{plainRenderer(), trueColourRenderer()} {
		for _, w := range []int{139, 140, 141, 160, 200} {
			for _, h := range []int{1, 2, 5, 30} {
				for i, mod := range []func(*proto.Detail){nil, tricky} {
					d := loadComments(t)
					if mod != nil {
						mod(&d)
					}
					m := New(r)
					m.SetSize(w, h)
					m.SetDiffLayout(LayoutSplit)
					m.SetDetail(d)
					allOff(&m)
					checkView(t, fmt.Sprintf("split/%d/w%d/h%d", i, w, h), m.View(), w, h)
					m.HandleKey(keyMsg("C"))
					checkView(t, fmt.Sprintf("split-composer/%d/w%d/h%d", i, w, h), m.View(), w, h)
				}
			}
		}
	}
}

func TestGoldenSplit(t *testing.T) {
	cases := map[string]func(t *testing.T) Model{
		"split_default": func(t *testing.T) Model { return splitModel(t, loadDetail(t), 160, 30) },
		"split_by_change": func(t *testing.T) Model {
			m := splitModel(t, withReviews(loadGroups(t)), 160, 40)
			press(t, &m, "g")
			return m
		},
		"split_narrow_degraded": func(t *testing.T) Model { return splitModel(t, loadDetail(t), 120, 30) },
		"split_comments": func(t *testing.T) Model {
			m := splitModel(t, loadComments(t), 180, 40)
			moveTo(t, &m, model.SideNew, 12, "")
			press(t, &m, "C")
			typeText(t, &m, "composer under the pair")
			return m
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			m := build(t)
			checkGolden(t, name, m.View()+"\n")
		})
	}
}

func trueColourRenderer() *lipgloss.Renderer {
	r := lipgloss.NewRenderer(io.Discard, termenv.WithProfile(termenv.TrueColor))
	r.SetColorProfile(termenv.TrueColor)
	r.SetHasDarkBackground(true)
	return r
}

// sgrColour is c's truecolor digits as they follow 38; or 48; in an SGR
// sequence ("2;r;g;b").
func sgrColour(r *lipgloss.Renderer, c lipgloss.TerminalColor) string {
	s := r.NewStyle().Foreground(c).Render("X")
	return strings.TrimPrefix(s[strings.Index(s, "[")+1:strings.Index(s, "m")], "38;")
}

// emphasised returns the text drawn on background colour (sgrColour).
func emphasised(s, colour string) string { return textUnder(s, "48;"+colour) }

// styledWith returns the text drawn in foreground colour (sgrColour).
func styledWith(s, colour string) string { return textUnder(s, "38;"+colour) }

// textUnder collects the text printed while the active SGR sequence
// contains want.
func textUnder(s, want string) string {
	var b strings.Builder
	on := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := strings.IndexByte(s[i:], 'm')
			if j < 0 {
				break
			}
			on = strings.Contains(";"+s[i+2:i+j]+";", ";"+want+";")
			i += j + 1
			continue
		}
		if on && s[i] != '\n' {
			b.WriteByte(s[i])
		}
		i++
	}
	return b.String()
}

// lineWith returns the first view line containing sub (escape codes
// ignored when matching).
func lineWith(t *testing.T, view, sub string) string {
	t.Helper()
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(ansi.Strip(l), sub) {
			return l
		}
	}
	t.Fatalf("no line with %q:\n%s", sub, ansi.Strip(view))
	return ""
}
