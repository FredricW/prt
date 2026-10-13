package codeview

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/fredricw/prt/internal/diffview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/golden")

const plainDiff = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n package a\n-var x = 1\n+var x = 2\n"

func plainRenderer() *lipgloss.Renderer { return lipgloss.NewRenderer(io.Discard) }

func colourRenderer() *lipgloss.Renderer {
	r := lipgloss.NewRenderer(io.Discard, termenv.WithProfile(termenv.ANSI256))
	r.SetColorProfile(termenv.ANSI256)
	return r
}

// loadDetail reads the synthetic fixture (a fresh copy per call).
func loadDetail(t testing.TB) proto.Detail {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "detail.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var d proto.Detail
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return d
}

func newModel(t testing.TB, d proto.Detail, w, h int) Model {
	t.Helper()
	m := New(plainRenderer())
	m.SetSize(w, h)
	m.SetDetail(d)
	return m
}

// allOff turns every filter off so every unit shows.
func allOff(m *Model) {
	for _, f := range AllFilters {
		m.SetFilter(f, false)
	}
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+r":
		return tea.KeyMsg{Type: tea.KeyCtrlR}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// press sends keys and fails when one is not consumed.
func press(t *testing.T, m *Model, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if !m.HandleKey(keyMsg(k)) {
			t.Fatalf("key %q not consumed", k)
		}
	}
}

func lines(m Model) []string { return strings.Split(m.View(), "\n") }

// paneOf returns the right-pane part of a view line at width 120 (list 40
// cells, gap 1).
func paneOf(line string) string { return ansi.Cut(line, 41, 1000) }

func contains(t *testing.T, view, want string) {
	t.Helper()
	if !strings.Contains(view, want) {
		t.Errorf("view lacks %q:\n%s", want, view)
	}
}

func lacks(t *testing.T, view, unwanted string) {
	t.Helper()
	if strings.Contains(view, unwanted) {
		t.Errorf("view has %q:\n%s", unwanted, view)
	}
}

func TestGolden(t *testing.T) {
	cases := map[string]func(t *testing.T) Model{
		"default": func(t *testing.T) Model { return newModel(t, loadDetail(t), 120, 30) },
		"bar_mine_off": func(t *testing.T) Model {
			m := newModel(t, loadDetail(t), 120, 30)
			press(t, &m, "f", "3", "J", "J")
			return m
		},
		"all_off": func(t *testing.T) Model {
			m := newModel(t, loadDetail(t), 120, 30)
			allOff(&m)
			return m
		},
		"noise_expanded": func(t *testing.T) Model {
			m := newModel(t, loadDetail(t), 120, 30)
			allOff(&m)
			press(t, &m, "z")
			return m
		},
		"moved": func(t *testing.T) Model {
			m := newModel(t, loadDetail(t), 120, 30)
			allOff(&m)
			press(t, &m, "J")
			return m
		},
		"labels_missing": func(t *testing.T) Model {
			d := loadDetail(t)
			d.Agg.LabelsMissing = true
			return newModel(t, d, 120, 30)
		},
		"pending": func(t *testing.T) Model {
			d := loadDetail(t)
			d.Agg = nil
			return newModel(t, d, 120, 30)
		},
		"plain": func(t *testing.T) Model {
			d := loadDetail(t)
			d.Units, d.Diff = nil, plainDiff
			return newModel(t, d, 120, 30)
		},
		"overlay": func(t *testing.T) Model {
			m := newModel(t, loadDetail(t), 120, 30)
			press(t, &m, "L")
			return m
		},
		"narrow": func(t *testing.T) Model {
			m := newModel(t, loadDetail(t), 50, 16)
			allOff(&m)
			return m
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			m := build(t)
			got := m.View() + "\n"
			path := filepath.Join("testdata", "golden", name+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("View mismatch\n--- got\n%s\n--- want\n%s", got, want)
			}
		})
	}
}

func TestFileList(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 30)
	ls := lines(m)
	want := []struct{ prefix, cells string }{
		{"▸ web/src/api/client.ts", "█·□"},
		{"  web/src/api/session.ts", "·█"},
		{"── not yours ─", ""},
		{"  web/legacy/util.ts", "▒█"},
	}
	for i, w := range want {
		list := ansi.Cut(ls[i], 0, 40)
		if !strings.HasPrefix(list, w.prefix) {
			t.Errorf("list row %d = %q, want prefix %q", i, list, w.prefix)
		}
		if w.cells != "" && !strings.HasSuffix(list, " "+w.cells) {
			t.Errorf("list row %d = %q, want cells %q at the end", i, list, w.cells)
		}
	}
}

func TestLeftTrunc(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"web/src/api/client.ts", 30, "web/src/api/client.ts"},
		{"web/src/api/client.ts", 16, "…/api/client.ts"},
		{"web/src/api/client.ts", 10, "…client.ts"},
		{"web/src/api/client.ts", 6, "…nt.ts"},
		{"x", 1, "x"},
		{"xyz", 1, "…"},
	}
	for _, tt := range tests {
		if got := leftTrunc(tt.in, tt.width); got != tt.want {
			t.Errorf("leftTrunc(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
	}
}

func TestChipsAndMarks(t *testing.T) {
	m := newModel(t, loadDetail(t), 160, 40)
	allOff(&m)
	v := m.View()
	contains(t, v, "┌ modified · feature · behaviour ✓ .91 · breaking .86 · b2 · tests ✗ · auth · error_path · risk 4 · eyes")
	contains(t, v, "│ L10–15 · +3 −1 · export async function fetchJSON(url: string, opts?: RequestInit)\n")
	contains(t, v, "┌ added · feature · ?behaviour ✗ .55 · undocumented · tests ✓ · risk 2 · skim · ?")
	contains(t, v, "~    11 +   const res")
	contains(t, v, "◆    12 +   if (!res.ok)")
	contains(t, v, "⋯    13 +   console.log")
	contains(t, v, " ▸11    -   const res = await fetch(url);") // the line cursor starts on the first changed line
	contains(t, v, "  10 10   export async function")
	contains(t, v, "·    41 + export function retryDelay")
}

// Regression: an unanswered behaviour label was read as a confident "no",
// so the default behaviour filter hid the unit and the chip said ✗ 1.0.
func TestUnknownBooleans(t *testing.T) {
	d := loadDetail(t)
	for i := range d.Agg.Units {
		if a := &d.Agg.Units[i]; a.UnitID == "u1" {
			a.Behaviour, a.BehaviourP, a.BehaviourUnknown = false, 0, true
			a.Covered, a.CoveredP, a.CoveredUnknown, a.Untested = false, 0, true, false
		}
	}
	m := newModel(t, d, 160, 40) // default filters: needs eyes, behaviour, mine
	v := m.View()
	contains(t, v, "┌ modified · feature · behaviour ? · breaking .86 · b2 · tests ? · auth")
	lacks(t, v, "behaviour ✗")
}

// Regression: with no owned files (no user.owns, no CODEOWNERS match) the
// default "mine" filter hid every unit of every PR.
func TestMineIgnoredWithoutOwnedFiles(t *testing.T) {
	d := loadDetail(t)
	for i := range d.Files {
		d.Files[i].Owned = false
	}
	for i := range d.Agg.Files {
		d.Agg.Files[i].Owned = false
	}
	for i := range d.Agg.Units {
		d.Agg.Units[i].Owned = false
	}
	m := newModel(t, d, 120, 30)
	if !m.FilterOn(FilterMine) {
		t.Fatal("mine should stay on (it is a default)")
	}
	contains(t, m.View(), "┌ modified · feature · behaviour ✓") // u1: eyes, behaviour
	press(t, &m, "f")
	contains(t, lines(m)[0], "[3 mine ✓ (no owned files, ignored)]")
}

func TestNoiseCollapseAndExpand(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 30)
	allOff(&m)
	v := m.View()
	contains(t, v, "▸ formatting · +2 −2 (z to expand)")
	lacks(t, v, "const TIMEOUT = 5000")
	// u3 is unsure: always expanded.
	contains(t, v, "return Math.min(1000 * 2 ** n, 30000);")
	press(t, &m, "z")
	v = m.View()
	lacks(t, v, "(z to expand)")
	contains(t, v, "const TIMEOUT = 5000")
	contains(t, v, "┌ modified · formatting · behaviour ✗ 1.0 · tests ✓ · risk 1 · skip")
	press(t, &m, "z")
	contains(t, m.View(), "▸ formatting · +2 −2 (z to expand)")
}

func TestMovedOrigin(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 30)
	allOff(&m)
	press(t, &m, "J")
	contains(t, m.View(), "▸ moved from web/legacy/util.ts:12 · parseCookie · +3 −0 (z to expand)")
	press(t, &m, "z")
	v := m.View()
	contains(t, v, "┌ moved · refactor")
	contains(t, v, "│ moved from web/legacy/util.ts:12")
}

func TestDefaultFiltersAndToggle(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 30)
	for i, f := range AllFilters {
		if got, want := m.FilterOn(f), i < 3; got != want {
			t.Errorf("FilterOn(%s) = %v, want %v", f, got, want)
		}
	}
	v := m.View()
	contains(t, v, "┌ modified · feature · behaviour ✓")
	contains(t, v, "2 units hidden by filters (f)")
	lacks(t, v, "formatting")
	press(t, &m, "J", "J")
	if m.SelectedFile() != "web/legacy/util.ts" {
		t.Fatalf("selected %q", m.SelectedFile())
	}
	v = m.View()
	contains(t, v, "2 units hidden by filters (f)")
	lacks(t, v, "┌")

	// f, 3: mine off; u7 (eyes, behaviour, not owned) now passes; u6 is
	// skim and still hidden (AND).
	press(t, &m, "f")
	if !m.Capturing() {
		t.Fatal("bar open but not capturing")
	}
	contains(t, lines(m)[0], "[1 needs eyes ✓] [2 behaviour ✓] [3 mine ✓] [4 risk ≥ 3] [5 untested]")
	press(t, &m, "3")
	if m.FilterOn(FilterMine) {
		t.Fatal("mine still on")
	}
	v = m.View()
	contains(t, lines(m)[0], "[3 mine]")
	contains(t, v, "┌ modified · bugfix · behaviour ✓ .95")
	contains(t, v, "1 unit hidden by filters (f)")
	// Adding untested (5) keeps u7 (untested); adding unsure (7) hides it.
	press(t, &m, "5")
	contains(t, m.View(), "┌ modified · bugfix")
	press(t, &m, "7")
	contains(t, m.View(), "2 units hidden by filters (f)")
	// has comments (8) has data from M4: no unit of the fixture has a
	// comment, so everything is hidden.
	press(t, &m, "8")
	if !m.FilterOn(FilterComments) {
		t.Error("has comments did not turn on")
	}
	contains(t, m.View(), "2 units hidden by filters (f)")
	press(t, &m, "8")
	// Without Agg.Since there is no last look: no data yet.
	press(t, &m, "0")
	if m.FilterOn(FilterSince) {
		t.Error("changed since last look turned on without data")
	}
	contains(t, lines(m)[0], "changed since last look: no data yet")
}

func TestCapturing(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 30)
	if m.Capturing() {
		t.Fatal("capturing at start")
	}
	for _, k := range []string{"1", "2", "q", "?", "r", "o", "c", "esc"} {
		if m.HandleKey(keyMsg(k)) {
			t.Errorf("key %q consumed with the bar closed", k)
		}
	}
	press(t, &m, "f", "1")
	if m.FilterOn(FilterNeedsEyes) {
		t.Error("1 did not toggle needs eyes")
	}
	if m.HandleKey(keyMsg("q")) {
		t.Error("q consumed with the bar open")
	}
	press(t, &m, "esc")
	if m.Capturing() {
		t.Error("esc did not close the bar")
	}
	if m.HandleKey(keyMsg("1")) || m.HandleKey(keyMsg("esc")) {
		t.Error("1/esc consumed after closing the bar")
	}
}

func TestNextPrevUnit(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 30)
	cur := func() string {
		u, ok := m.CurrentUnit()
		if !ok {
			return ""
		}
		return u.ID
	}
	if cur() != "u1" {
		t.Fatalf("start unit %q", cur())
	}
	steps := []struct {
		key, unit, file string
	}{
		{"n", "u5", "web/src/api/session.ts"},
		{"n", "u5", "web/src/api/session.ts"}, // u6/u7 hidden by filters
		{"p", "u1", "web/src/api/client.ts"},
		{"p", "u1", "web/src/api/client.ts"},
	}
	for i, s := range steps {
		press(t, &m, s.key)
		if cur() != s.unit || m.SelectedFile() != s.file {
			t.Fatalf("step %d (%s): unit %q file %q, want %q %q", i, s.key, cur(), m.SelectedFile(), s.unit, s.file)
		}
	}
	allOff(&m)
	var got []string
	for range 8 {
		press(t, &m, "n")
		got = append(got, cur())
	}
	if want := "u2 u3 u4 u5 u6 u7 u7 u7"; strings.Join(got, " ") != want {
		t.Errorf("n sequence = %q, want %q", strings.Join(got, " "), want)
	}
	if m.p.headOf("u7") != m.off {
		t.Errorf("u7 header row %d not at the top (offset %d)", m.p.headOf("u7"), m.off)
	}
	press(t, &m, "p")
	if cur() != "u6" {
		t.Errorf("p = %q, want u6", cur())
	}
}

func TestFileNavigation(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 30)
	want := []string{"web/src/api/session.ts", "web/legacy/util.ts", "web/legacy/util.ts"}
	for i, w := range want {
		press(t, &m, "J")
		if m.SelectedFile() != w {
			t.Fatalf("J %d: %q, want %q", i, m.SelectedFile(), w)
		}
	}
	press(t, &m, "[", "K", "K")
	if m.SelectedFile() != "web/src/api/client.ts" {
		t.Errorf("K: %q", m.SelectedFile())
	}
	press(t, &m, "]")
	if m.SelectedFile() != "web/src/api/session.ts" {
		t.Errorf("]: %q", m.SelectedFile())
	}
}

func TestFocusUnit(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 30)
	if m.FocusUnit("nope") {
		t.Error("FocusUnit of an unknown unit succeeded")
	}
	if !m.FocusUnit("u7") {
		t.Fatal("FocusUnit(u7) failed")
	}
	if u, ok := m.CurrentUnit(); !ok || u.ID != "u7" || m.SelectedFile() != "web/legacy/util.ts" {
		t.Fatalf("after FocusUnit(u7): %q %v %q", u.ID, ok, m.SelectedFile())
	}
	contains(t, m.View(), "┌ modified · bugfix")
	allOff(&m)
	if !m.FocusUnit("u2") {
		t.Fatal("FocusUnit(u2) failed")
	}
	if u, _ := m.CurrentUnit(); u.ID != "u2" {
		t.Fatalf("current %q", u.ID)
	}
	v := m.View()
	contains(t, v, "const TIMEOUT = 5000")
	if !strings.HasPrefix(paneOf(lines(m)[1]), "┌ modified · formatting") {
		t.Errorf("u2 header not at top: %q", lines(m)[1])
	}
}

func TestRawLabelsOverlay(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 30)
	press(t, &m, "L")
	if !m.Capturing() {
		t.Fatal("overlay not capturing")
	}
	v := m.View()
	contains(t, v, "raw labels — esc/L closes")
	contains(t, v, "web/src/api/client.ts · fetchJSON")
	contains(t, v, "unit  behaviour              yes                p .91  conf .91  typesafe")
	contains(t, v, "L12   suspicious")
	contains(t, v, "L13   leftover")
	if !m.HandleKey(keyMsg("q")) {
		t.Error("q not swallowed by the overlay")
	}
	if m.HandleKey(keyMsg("ctrl+c")) {
		t.Error("ctrl+c swallowed by the overlay")
	}
	press(t, &m, "esc")
	if m.Capturing() {
		t.Error("esc did not close the overlay")
	}
	lacks(t, m.View(), "raw labels")
	press(t, &m, "L", "L")
	if m.Capturing() {
		t.Error("L did not close the overlay")
	}
}

func TestLabelsMissing(t *testing.T) {
	d := loadDetail(t)
	d.Agg.LabelsMissing = true
	m := newModel(t, d, 120, 30)
	ls := lines(m)
	if !strings.HasPrefix(ls[0], "labels unavailable — filters off") {
		t.Errorf("top line = %q", ls[0])
	}
	v := m.View()
	lacks(t, v, "behaviour")
	lacks(t, v, "units hidden")
	press(t, &m, "J", "J")
	contains(t, m.View(), "┌ modified\n")
}

func TestPending(t *testing.T) {
	d := loadDetail(t)
	d.Agg = nil
	m := newModel(t, d, 120, 30)
	ls := lines(m)
	for i, p := range []string{"▸ web/src/api/client.ts ", "  web/src/api/session.ts ", "── not yours", "  web/legacy/util.ts "} {
		if !strings.HasPrefix(ls[i], p) {
			t.Errorf("row %d = %q, want prefix %q", i, ls[i], p)
		}
	}
	v := m.View()
	contains(t, v, "const TIMEOUT = 5000")
	contains(t, v, "┌ modified\n")
	lacks(t, v, "█")
	lacks(t, v, "(z to expand)")
	if m.FilterOn(FilterMine) != true {
		t.Error("filters lost")
	}
}

func TestPlainFallback(t *testing.T) {
	d := loadDetail(t)
	d.Units, d.Diff = nil, plainDiff
	m := newModel(t, d, 60, 8)
	dv := diffview.New(plainRenderer())
	dv.SetSize(60, 8)
	dv.SetDiff(plainDiff)
	if m.View() != dv.View() {
		t.Errorf("plain view differs:\n%s\n---\n%s", m.View(), dv.View())
	}
	if _, ok := m.CurrentUnit(); ok {
		t.Error("CurrentUnit in plain mode")
	}
	press(t, &m, "j")
	if m.HandleKey(keyMsg("f")) || m.Capturing() {
		t.Error("filters in plain mode")
	}
}

func TestSetDetailKeepsState(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 10)
	allOff(&m)
	press(t, &m, "z", "j", "j")
	m.SetFilter(FilterUnsure, false)
	off := m.off
	m.SetDetail(loadDetail(t))
	if m.off != off || m.SelectedFile() != "web/src/api/client.ts" || !m.expandedFiles["web/src/api/client.ts"] {
		t.Errorf("reload lost state: off %d/%d file %q", m.off, off, m.SelectedFile())
	}
	press(t, &m, "J")
	m.SetDetail(loadDetail(t))
	if m.SelectedFile() != "web/src/api/session.ts" {
		t.Errorf("reload lost selection: %q", m.SelectedFile())
	}
	m.SetFilter(FilterBehaviour, true)
	other := loadDetail(t)
	other.Item.ID = "pr:8"
	m.SetDetail(other)
	if m.SelectedFile() != "web/src/api/client.ts" || m.off != 0 || len(m.expandedFiles) != 0 {
		t.Errorf("new item kept view state: %q off %d", m.SelectedFile(), m.off)
	}
	if !m.FilterOn(FilterBehaviour) || m.FilterOn(FilterMine) {
		t.Error("new item reset filters")
	}
	m.Clear()
	if m.SelectedFile() != "" || !strings.Contains(m.View(), "(no diff yet)") {
		t.Errorf("Clear: %q", m.View())
	}
}

func TestMouse(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 10)
	allOff(&m)
	m.SetOrigin(0, 2)
	wheel := func(x, y int, b tea.MouseButton) {
		m, _ = m.Update(tea.MouseMsg{X: x, Y: y, Button: b, Action: tea.MouseActionPress})
	}
	wheel(60, 5, tea.MouseButtonWheelDown)
	if m.off != wheelStep {
		t.Errorf("pane wheel: off %d", m.off)
	}
	wheel(60, 5, tea.MouseButtonWheelUp)
	if m.off != 0 {
		t.Errorf("pane wheel up: off %d", m.off)
	}
	wheel(5, 5, tea.MouseButtonWheelDown)
	if m.SelectedFile() != "web/src/api/session.ts" {
		t.Errorf("list wheel: %q", m.SelectedFile())
	}
	wheel(5, 2+3, tea.MouseButtonLeft) // list row 3: util.ts (after divider)
	if m.SelectedFile() != "web/legacy/util.ts" {
		t.Errorf("click: %q", m.SelectedFile())
	}
	wheel(5, 2+2, tea.MouseButtonLeft) // divider: no change
	if m.SelectedFile() != "web/legacy/util.ts" {
		t.Errorf("click on divider: %q", m.SelectedFile())
	}
}

func TestViewDimensions(t *testing.T) {
	type state struct {
		name string
		prep func(m *Model)
		mod  func(d *proto.Detail)
	}
	states := []state{
		{"default", nil, nil},
		{"bar", func(m *Model) { m.HandleKey(keyMsg("f")) }, nil},
		{"overlay", func(m *Model) { m.HandleKey(keyMsg("L")) }, nil},
		{"all-expanded", func(m *Model) { allOff(m); m.HandleKey(keyMsg("z")) }, nil},
		{"scrolled", func(m *Model) { allOff(m); m.HandleKey(keyMsg("G")) }, nil},
		{"note", func(m *Model) { m.HandleKey(keyMsg("n")); m.HandleKey(keyMsg("n")) }, nil},
		{"labels-missing", nil, func(d *proto.Detail) { d.Agg.LabelsMissing = true }},
		{"pending", nil, func(d *proto.Detail) { d.Agg = nil }},
		{"plain", nil, func(d *proto.Detail) { d.Units, d.Diff = nil, plainDiff }},
		{"tricky", func(m *Model) { allOff(m) }, func(d *proto.Detail) {
			d.Units[0].Path = "日本語/\x1b[31mevil\x07/" + strings.Repeat("deep/", 20) + "file.ts"
			d.Agg.Files[0].Path = d.Units[0].Path
			d.Units[0].Diff = "+\t日本語🙂 \x1b[2J" + strings.Repeat("x", 400) + "\n-" + strings.Repeat("y", 400)
		}},
	}
	renderers := map[string]*lipgloss.Renderer{"plain": plainRenderer(), "colour": colourRenderer()}
	for _, st := range states {
		for rname, r := range renderers {
			for _, w := range []int{1, 20, 40, 59, 60, 80, 160} {
				for _, h := range []int{1, 2, 5, 30} {
					d := loadDetail(t)
					if st.mod != nil {
						st.mod(&d)
					}
					m := New(r)
					m.SetSize(w, h)
					m.SetDetail(d)
					if st.prep != nil {
						st.prep(&m)
					}
					checkView(t, fmt.Sprintf("%s/%s/w%d/h%d", st.name, rname, w, h), m.View(), w, h)
				}
			}
		}
	}
}

func checkView(t *testing.T, label, view string, w, h int) {
	t.Helper()
	ls := strings.Split(view, "\n")
	if len(ls) != h {
		t.Errorf("%s: %d lines, want %d", label, len(ls), h)
	}
	for i, l := range ls {
		if got := ansi.StringWidth(l); got > w {
			t.Errorf("%s: line %d width %d > %d: %q", label, i, got, w, l)
		}
		if strings.ContainsAny(ansi.Strip(l), "\x1b\x07\t") {
			t.Errorf("%s: line %d has control characters: %q", label, i, l)
		}
	}
}

// bigDetail is one file with a single 5,000-line unit.
func bigDetail() proto.Detail {
	var b strings.Builder
	for i := range 5000 {
		if i%2 == 0 {
			fmt.Fprintf(&b, "-\tvalue := compute(%d, old)\n", i)
		} else {
			fmt.Fprintf(&b, "+\tvalue := compute(%d, new)\n", i)
		}
	}
	u := model.ChangeUnit{ID: "big", Path: "big.go", Diff: b.String(), Kind: model.UnitModified,
		Range: model.Range{Old: model.Span{Start: 1, Count: 2500}, New: model.Span{Start: 1, Count: 2500}}, Adds: 2500, Dels: 2500}
	return proto.Detail{Item: model.Item{ID: "pr:9"}, Units: []model.ChangeUnit{u}}
}

func TestLazyRendering(t *testing.T) {
	m := newModel(t, bigDetail(), 120, 40)
	_ = m.View()
	if m.cache.renders != 39 {
		t.Errorf("first View rendered %d rows, want 39", m.cache.renders)
	}
	_ = m.View()
	if m.cache.renders != 39 {
		t.Errorf("second View rendered %d rows, want cached", m.cache.renders)
	}
	// j moves the line cursor: the row it left and the row it is on now.
	press(t, &m, "j")
	_ = m.View()
	if m.cache.renders != 41 {
		t.Errorf("cursor down: %d rows, want 41", m.cache.renders)
	}
	// ctrl+d scrolls half a page (19 rows): 19 new rows, and the cursor,
	// scrolled out of view, moves onto the first visible line.
	press(t, &m, "ctrl+d")
	_ = m.View()
	if m.cache.renders != 41+19+1 {
		t.Errorf("half page: %d rows, want %d", m.cache.renders, 41+19+1)
	}
	if len(m.cache.spans) > 2*80 {
		t.Errorf("intra-line spans for %d rows, want only visible pairs", len(m.cache.spans))
	}
}

func TestIntraLineHighlight(t *testing.T) {
	m := New(colourRenderer())
	m.SetSize(120, 30)
	m.SetDetail(loadDetail(t))
	v := m.View()
	if !strings.Contains(v, m.st.addEmph.Render(`, { ...opts, credentials: "include" }`)) {
		t.Errorf("view lacks emphasised addition:\n%q", v)
	}
}

func TestProbText(t *testing.T) {
	tests := []struct {
		p    float64
		want string
	}{
		{.86, ".86"}, {1.0, "1.0"}, {.999, "1.0"}, {.05, ".05"}, {0, ".00"}, {-1, ".00"}, {.6, ".60"},
	}
	for _, tt := range tests {
		if got := ProbText(tt.p); got != tt.want {
			t.Errorf("ProbText(%v) = %q, want %q", tt.p, got, tt.want)
		}
	}
	if UnsurePrefix(.59) != "?" || UnsurePrefix(.60) != "" || UnsurePrefix(.9) != "" {
		t.Error("UnsurePrefix threshold")
	}
}

func TestGlyphs(t *testing.T) {
	attn := []struct {
		a      model.Attention
		unsure bool
		want   string
	}{
		{model.AttnEyes, false, "█"}, {model.AttnSkim, false, "▒"}, {model.AttnSkip, false, "·"},
		{model.AttnEyes, true, "□"}, {model.AttnSkim, true, "□"}, {model.AttnSkip, true, "◌"},
	}
	for _, tt := range attn {
		if got := AttentionGlyph(tt.a, tt.unsure); got != tt.want {
			t.Errorf("AttentionGlyph(%s, %v) = %q, want %q", tt.a, tt.unsure, got, tt.want)
		}
	}
	marks := map[model.Mark]string{model.MarkSuspicious: "◆", model.MarkValue: "~", model.MarkLeftover: "⋯", model.MarkNone: "·", "": "·"}
	for mk, want := range marks {
		if got := MarkGlyph(mk); got != want {
			t.Errorf("MarkGlyph(%q) = %q, want %q", mk, got, want)
		}
	}
}

func TestUnitLines(t *testing.T) {
	u := model.ChangeUnit{Diff: " a\n-b\n+c\n\n\\ No newline at end of file\n+d\n", Range: model.Range{Old: model.Span{Start: 5}, New: model.Span{Start: 7}}}
	ls := unitLines(&u)
	got := make([]string, len(ls))
	for i, l := range ls {
		got[i] = fmt.Sprintf("%c%d/%d/%v", l.Op, l.OldNo, l.NewNo, l.NoEOL)
	}
	want := " 5/7/false -6/0/false +0/8/false  7/9/true +0/10/false"
	if strings.Join(got, " ") != want {
		t.Errorf("unitLines = %q, want %q", strings.Join(got, " "), want)
	}
}

func BenchmarkView5000(b *testing.B) {
	m := newModel(b, bigDetail(), 120, 50)
	b.ReportAllocs()
	for b.Loop() {
		m.cache.lines = map[int]string{}
		m.scrollBy(7)
		if m.off >= m.maxOffset() {
			m.off = 0
		}
		_ = m.View()
	}
}
