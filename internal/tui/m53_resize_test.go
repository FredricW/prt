package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/uistate"
)

// resizeRig records what a resize reaches: the saved layouts and the
// recording Code, Pipeline and Architecture pages.
type resizeRig struct {
	saved []uistate.Layout
	err   error
	code  *fakePane
	pipe  *fakePipe
	arch  *fakeArch
}

// resizeModel is a w × 30 model on threeItems (pr:1 loaded) with recording
// pages and a SaveLayout recorder.
func resizeModel(t *testing.T, w int) (Model, *resizeRig) {
	t.Helper()
	rig := &resizeRig{}
	o := Options{SaveLayout: func(l uistate.Layout) error {
		rig.saved = append(rig.saved, l)
		return rig.err
	}}
	m := newTestModel(t, newFake(threeItems()...), o)
	m, rig.code = withFakePane(m)
	m, rig.pipe = withFakePipe(m)
	m, rig.arch = withFakeArch(m)
	m = update(t, m, tea.WindowSizeMsg{Width: w, Height: 30})
	m = feed[detailMsg](t, m, m.selectionChanged())
	return m, rig
}

// applyAll applies msg and every message its command produces.
func applyAll(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	m, cmd := updateCmd(t, m, msg)
	for _, out := range run(cmd) {
		m = update(t, m, out)
	}
	return m
}

func leftMotion(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion}
}

func release(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease}
}

// clampWant is the documented clamp: [20, min(80, width − 42)].
func clampWant(w, x int) int { return max(20, min(min(80, w-42), x)) }

func TestSideWidthClamp(t *testing.T) {
	for _, tc := range []struct{ width, user, want int }{
		{120, 0, 36},  // automatic
		{80, 0, 32},   // automatic, a narrow terminal
		{120, 10, 20}, // below the minimum
		{120, 50, 50},
		{120, 100, 78}, // the main pane keeps 40 columns
		{200, 100, 80}, // the maximum
		{80, 60, 38},
		{62, 30, 24},  // just room for the minimum; the automatic 24 is the limit
		{62, 21, 21},  // a user width below the automatic one
		{65, 30, 26},  // never clamped below the automatic width
		{69, 40, 27},  // width − 42 and the automatic width agree
		{75, 40, 33},  // the main pane keeps 40 (automatic 30)
		{61, 30, 24},  // no room: automatic
		{40, 25, 16},  // tiny: automatic
		{100, -5, 36}, // a bad stored value is automatic
	} {
		m := Model{width: tc.width, sideUser: tc.user}
		if got := m.sideWidth(); got != tc.want {
			t.Errorf("width %d user %d: %d, want %d", tc.width, tc.user, got, tc.want)
		}
	}
}

func TestSidebarDrag(t *testing.T) {
	for _, w := range []int{80, 100, 120, 140, 200} {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			m, rig := resizeModel(t, w)
			m.focus = FocusMain // a light box border, so the drag's heavy one shows
			sw0, sel := m.sideWidth(), selectedID(m)
			m = update(t, m, mouseClick(sw0, 5))
			if !m.sideDrag || selectedID(m) != sel {
				t.Fatalf("press on the border: drag %v selected %s", m.sideDrag, selectedID(m))
			}
			for _, x := range []int{sw0 + 7, 5, w + 10, sw0 + 4} {
				m = update(t, m, leftMotion(x, 9))
				if got := m.sideWidth(); got != clampWant(w, x) {
					t.Errorf("motion to %d: width %d, want %d", x, got, clampWant(w, x))
				}
			}
			sw := m.sideWidth()
			if s := m.viewStatus(); s != fmt.Sprintf("resize picker: %d cols · release to keep", sw) {
				t.Errorf("drag status %q", s)
			}
			if r := []rune(ansi.Strip(strings.Split(m.View(), "\n")[2]))[sw]; r != '┃' {
				t.Errorf("border during the drag %q", r)
			}
			if len(rig.saved) != 0 {
				t.Errorf("saved during the drag: %v", rig.saved)
			}
			m = applyAll(t, m, release(sw0+4, 9))
			if m.sideDrag || m.sideWidth() != clampWant(w, sw0+4) {
				t.Fatalf("release: drag %v width %d", m.sideDrag, m.sideWidth())
			}
			if want := []uistate.Layout{{SidebarWidth: sw}}; fmt.Sprint(rig.saved) != fmt.Sprint(want) {
				t.Errorf("saved %v, want %v", rig.saved, want)
			}
			if r := []rune(ansi.Strip(strings.Split(m.View(), "\n")[2]))[sw]; r != '│' {
				t.Errorf("border after the drag %q", r)
			}
			// The main pane follows: origins and sizes.
			o := [2]int{sw + 2, 2}
			if rig.pipe.origin != o || rig.arch.origin != o || rig.code.origin != o {
				t.Errorf("origins pipe %v arch %v code %v, want %v", rig.pipe.origin, rig.arch.origin, rig.code.origin, o)
			}
			if got := rig.code.sizes[len(rig.code.sizes)-1]; got != [2]int{w - sw - 2, 27} {
				t.Errorf("code size %v", got)
			}
			if rig.arch.size != [2]int{w - sw - 2, 27} {
				t.Errorf("arch size %v", rig.arch.size)
			}
			// Motion without a drag does nothing.
			m = update(t, m, leftMotion(sw+6, 9))
			if m.sideWidth() != sw || len(rig.saved) != 1 {
				t.Errorf("motion without a press: width %d saves %d", m.sideWidth(), len(rig.saved))
			}
			// A press on a picker row still selects (rows 5–6 are pr:2).
			m = update(t, m, mouseClick(3, 5))
			if m.sideDrag || selectedID(m) != "pr:2" {
				t.Errorf("row press: drag %v selected %s", m.sideDrag, selectedID(m))
			}
		})
	}
}

// With colour the dragged border is the accent colour, every line keeps the
// terminal's width, and the Overview keeps its scroll position.
func TestSidebarDragColourAndScroll(t *testing.T) {
	d := richDetail(t)
	f := newFake(d.Item)
	f.details[d.Item.ID] = d
	m := NewWithOptions(f, trueColour(), Options{Now: func() time.Time { return fixedNow }})
	m = update(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = update(t, m, itemsMsg{items: []model.Item{d.Item}})
	m = feed[detailMsg](t, m, m.selectionChanged())
	m.overview.vp.SetYOffset(10)
	sw := m.sideWidth()
	m = update(t, m, mouseClick(sw, 3))
	m = update(t, m, leftMotion(sw+12, 3))
	v := m.View()
	if !strings.Contains(v, m.st.accent.Render("┃")) {
		t.Error("the dragged border is not drawn in the accent colour")
	}
	for i, l := range strings.Split(v, "\n") {
		if n := ansi.StringWidth(l); n > 140 {
			t.Errorf("line %d is %d wide", i, n)
		}
	}
	if m.overview.vp.YOffset != 10 {
		t.Errorf("the drag scrolled the Overview to %d", m.overview.vp.YOffset)
	}
}

// The gap after the border is a handle too; a press and release without a
// motion changes nothing (automatic stays automatic) and saves once.
func TestSidebarDragFromGap(t *testing.T) {
	m, rig := resizeModel(t, 120)
	sw := m.sideWidth()
	m = update(t, m, mouseClick(sw+1, 12))
	if !m.sideDrag {
		t.Fatal("press on the gap did not start a drag")
	}
	m = applyAll(t, m, tea.MouseMsg{X: sw + 1, Y: 12, Action: tea.MouseActionRelease})
	if m.sideDrag || m.sideUser != 0 || m.sideWidth() != sw {
		t.Errorf("click on the gap: drag %v user %d width %d", m.sideDrag, m.sideUser, m.sideWidth())
	}
	if fmt.Sprint(rig.saved) != fmt.Sprint([]uistate.Layout{{}}) {
		t.Errorf("saved %v", rig.saved)
	}
	// Row 0 (the header line) and the bottom line are no handles.
	for _, y := range []int{0, 29} {
		if m2 := update(t, m, mouseClick(sw, y)); m2.sideDrag {
			t.Errorf("row %d started a drag", y)
		}
	}
}

// While the border is dragged every mouse message goes to the drag: the
// pages and the picker see none of them.
func TestSidebarDragTakesEveryMouseMessage(t *testing.T) {
	m, rig := resizeModel(t, 140)
	m = press(t, m, "2")
	sw, sel := m.sideWidth(), selectedID(m)
	m = update(t, m, mouseClick(sw, 4))
	for _, msg := range []tea.MouseMsg{
		leftMotion(sw+30, 10),
		{X: sw + 40, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress},
		{X: sw + 50, Y: 10, Button: tea.MouseButtonRight, Action: tea.MouseActionPress},
	} {
		m = update(t, m, msg)
	}
	if len(rig.code.mice) != 0 || selectedID(m) != sel || m.tab != TabCode || !m.sideDrag {
		t.Errorf("during the drag: code got %d messages, selected %s, tab %v, drag %v", len(rig.code.mice), selectedID(m), m.tab, m.sideDrag)
	}
	m = update(t, m, release(sw+30, 10))
	if m.sideDrag || m.sideWidth() != clampWant(140, sw+30) {
		t.Errorf("release: drag %v width %d", m.sideDrag, m.sideWidth())
	}
}

// A lost release (let go outside the window) does not leave the drag
// stuck: the next left press ends it where it is (saved) and is handled as
// a press, so it selects the row it lands on instead of resizing the
// picker to it; motion without a button ends it too.
func TestSidebarDragLostRelease(t *testing.T) {
	m, rig := resizeModel(t, 120)
	sw := m.sideWidth()
	m = update(t, m, mouseClick(sw, 4))
	m = update(t, m, leftMotion(sw+10, 9))
	wide := m.sideWidth()
	m = applyAll(t, m, mouseClick(3, 5)) // rows 5–6 are pr:2
	if m.sideDrag || m.sideWidth() != wide || selectedID(m) != "pr:2" {
		t.Errorf("press after a lost release: drag %v width %d (want %d) selected %s", m.sideDrag, m.sideWidth(), wide, selectedID(m))
	}
	if fmt.Sprint(rig.saved) != fmt.Sprint([]uistate.Layout{{SidebarWidth: wide}}) {
		t.Errorf("saved %v", rig.saved)
	}
	m = applyAll(t, m, release(3, 5))
	if m.sideWidth() != wide {
		t.Errorf("the press's own release resized the picker to %d", m.sideWidth())
	}
	// Motion with no button.
	m = update(t, m, mouseClick(wide, 4))
	m = update(t, m, leftMotion(wide+4, 9))
	m = applyAll(t, m, tea.MouseMsg{X: wide + 20, Y: 9, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
	if m.sideDrag || m.sideWidth() != wide+4 || len(rig.saved) != 2 {
		t.Errorf("buttonless motion: drag %v width %d saves %v", m.sideDrag, m.sideWidth(), rig.saved)
	}
}

// On 62–69 columns the automatic width is wider than width − 42: `>` and a
// drag that does not move the border never shrink the picker.
func TestResizeNeverShrinksBelowAutomatic(t *testing.T) {
	m, _ := resizeModel(t, 65)
	auto := m.sideWidth()
	if auto != 26 {
		t.Fatalf("automatic width on 65 columns = %d", auto)
	}
	m = applyAll(t, m, keyMsg(">"))
	if m.sideWidth() < auto || m.note != fmt.Sprintf("picker: %d cols", m.sideWidth()) {
		t.Errorf("> on 65 columns: width %d (automatic %d) note %q", m.sideWidth(), auto, m.note)
	}
	m, _ = resizeModel(t, 65)
	m = update(t, m, mouseClick(auto, 4))
	m = update(t, m, leftMotion(auto, 9))
	if m.sideWidth() != auto {
		t.Errorf("a drag held on the border moved it to %d (automatic %d)", m.sideWidth(), auto)
	}
	m = applyAll(t, m, keyMsg("<"))
	if m.sideWidth() != auto-2 {
		t.Errorf("< on 65 columns: width %d", m.sideWidth())
	}
}

// Saves run on command goroutines in any order: one older than the last
// written does nothing, so the file ends with the newest widths; a save
// still pending when the program ends is written by FlushLayout.
func TestLayoutSavesOrdered(t *testing.T) {
	m, rig := resizeModel(t, 120)
	var cmds []tea.Cmd
	for range 3 {
		var cmd tea.Cmd
		m, cmd = updateCmd(t, m, keyMsg(">"))
		cmds = append(cmds, cmd)
	}
	// The newest first, then the older ones.
	for i := len(cmds) - 1; i >= 0; i-- {
		run(cmds[i])
	}
	if fmt.Sprint(rig.saved) != fmt.Sprint([]uistate.Layout{{SidebarWidth: 42}}) {
		t.Errorf("saved %v, want only the newest width", rig.saved)
	}
	if err := m.FlushLayout(); err != nil || len(rig.saved) != 1 {
		t.Errorf("flush with nothing pending: %v, saves %v", err, rig.saved)
	}
	// A press whose save never ran is flushed.
	m, _ = updateCmd(t, m, keyMsg("<"))
	if err := m.FlushLayout(); err != nil || fmt.Sprint(rig.saved[len(rig.saved)-1]) != fmt.Sprint(uistate.Layout{SidebarWidth: 40}) {
		t.Errorf("flush: %v, saves %v", err, rig.saved)
	}
}

// Below 62 columns the picker is automatic: no drag, and the keys say so.
func TestNoResizeOnTinyTerminals(t *testing.T) {
	m, rig := resizeModel(t, 60)
	sw := m.sideWidth()
	if m = update(t, m, mouseClick(sw, 5)); m.sideDrag {
		t.Error("a drag started on a 60-column terminal")
	}
	m = applyAll(t, m, keyMsg(">"))
	if m.note != "the picker's width is automatic below 62 columns" || m.sideWidth() != sw || len(rig.saved) != 0 {
		t.Errorf("> on 60 columns: note %q width %d saves %v", m.note, m.sideWidth(), rig.saved)
	}
}

func TestResizeKeys(t *testing.T) {
	m, rig := resizeModel(t, 120)
	m = applyAll(t, m, keyMsg(">"))
	if m.sideWidth() != 38 || m.note != "picker: 38 cols" {
		t.Errorf("> in the picker: width %d note %q", m.sideWidth(), m.note)
	}
	m = applyAll(t, m, keyMsg("<"))
	m = applyAll(t, m, keyMsg("<"))
	if m.sideWidth() != 34 {
		t.Errorf("< <: width %d", m.sideWidth())
	}
	// The Overview with focus moves the picker too.
	m = press(t, m, "enter")
	m = applyAll(t, m, keyMsg(">"))
	if m.sideWidth() != 36 || m.focus != FocusMain {
		t.Errorf("> on the Overview: width %d focus %v", m.sideWidth(), m.focus)
	}
	// The Code tab with focus moves the list.
	rig.code.listWidth = 30
	m = press(t, m, "2")
	m = applyAll(t, m, keyMsg(">"))
	if fmt.Sprint(rig.code.listCalls) != "[32]" || m.note != "code list: 32 cols" || m.sideWidth() != 36 {
		t.Errorf("> on Code: list calls %v note %q width %d", rig.code.listCalls, m.note, m.sideWidth())
	}
	m = applyAll(t, m, keyMsg("<"))
	if fmt.Sprint(rig.code.listCalls) != "[32 30]" {
		t.Errorf("< on Code: list calls %v", rig.code.listCalls)
	}
	lay := func(side, list int) uistate.Layout { return uistate.Layout{SidebarWidth: side, CodeListWidth: list} }
	want := []uistate.Layout{lay(38, 0), lay(36, 0), lay(34, 0), lay(36, 0), lay(36, 32), lay(36, 30)}
	if fmt.Sprint(rig.saved) != fmt.Sprint(want) {
		t.Errorf("saved %v, want %v", rig.saved, want)
	}
	// A hidden list says so and saves nothing.
	rig.code.listWidth = 0
	m = applyAll(t, m, keyMsg(">"))
	if m.note != "list hidden below 60 columns" || len(rig.saved) != len(want) || len(rig.code.listCalls) != 2 {
		t.Errorf("hidden list: note %q saves %d calls %v", m.note, len(rig.saved), rig.code.listCalls)
	}
	// The picker with focus on the Code tab moves the picker.
	m = press(t, m, "esc")
	m = applyAll(t, m, keyMsg(">"))
	if m.focus != FocusPicker || m.sideWidth() != 38 {
		t.Errorf("> in the picker on Code: focus %v width %d", m.focus, m.sideWidth())
	}
	// A failed save is a note.
	rig.err = errors.New("read-only file system")
	m = applyAll(t, m, keyMsg("<"))
	if m.note != "could not save layout: read-only file system" {
		t.Errorf("save error note %q", m.note)
	}
}

func TestCodeListDrag(t *testing.T) {
	m, rig := resizeModel(t, 120)
	m = press(t, m, "2")
	sw := m.sideWidth()
	// A press on the page goes to it as before (it starts its own drag at
	// its border).
	m = update(t, m, mouseClick(sw+20, 6))
	if len(rig.code.mice) != 1 {
		t.Fatalf("press on the Code page: %d messages", len(rig.code.mice))
	}
	rig.code.dragging, rig.code.listWidth, rig.code.dropWidth = true, 30, 44
	if s := m.viewStatus(); s != "resize code list: 30 cols · release to keep" {
		t.Errorf("status %q", s)
	}
	sel := selectedID(m)
	for _, msg := range []tea.MouseMsg{
		leftMotion(3, 4), // over the picker
		{X: 3, Y: 4, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress},
		leftMotion(sw+50, 1), // over the tab bar
	} {
		m = update(t, m, msg)
	}
	if len(rig.code.mice) != 4 || selectedID(m) != sel {
		t.Errorf("during the list drag: page got %d messages, selected %s", len(rig.code.mice), selectedID(m))
	}
	m = applyAll(t, m, release(3, 4))
	if rig.code.dragging || m.codeListUser != 44 {
		t.Errorf("release: dragging %v list %d", rig.code.dragging, m.codeListUser)
	}
	if want := []uistate.Layout{{CodeListWidth: 44}}; fmt.Sprint(rig.saved) != fmt.Sprint(want) {
		t.Errorf("saved %v, want %v", rig.saved, want)
	}
	// Not dragging: a picker press is the picker's again.
	m = update(t, m, mouseClick(3, 5))
	if len(rig.code.mice) != 5 || selectedID(m) != "pr:2" {
		t.Errorf("after the drag: page messages %d selected %s", len(rig.code.mice), selectedID(m))
	}
}

func TestLayoutOptionApplied(t *testing.T) {
	o := Options{Layout: uistate.Layout{SidebarWidth: 50, CodeListWidth: 30}}
	m := NewWithOptions(newFake(threeItems()...), plainRenderer(), o)
	for _, tc := range []struct{ w, want int }{{120, 50}, {80, 38}, {60, 24}, {140, 50}} {
		m = update(t, m, tea.WindowSizeMsg{Width: tc.w, Height: 30})
		if got := m.sideWidth(); got != tc.want {
			t.Errorf("width %d: picker %d, want %d (stored 50, clamped when applied)", tc.w, got, tc.want)
		}
	}
	if m.codeListUser != 30 {
		t.Errorf("code list width %d", m.codeListUser)
	}
	m, p := withFakePane(m)
	m.applyLayout(o.Layout)
	if fmt.Sprint(p.listCalls) != "[30]" {
		t.Errorf("Code page list calls %v", p.listCalls)
	}
	// Without SaveLayout nothing is persisted and nothing fails.
	if cmd := m.saveLayout(); cmd != nil {
		t.Error("saveLayout without Options.SaveLayout")
	}
}

// The side-by-side threshold is the Code component's width: a wide picker
// at 140 columns degrades it, narrowing brings it back.
func TestSplitFollowsSidebar(t *testing.T) {
	t.Run("real pane", func(t *testing.T) {
		items := threeItems()
		f := newFake(items...)
		f.details["pr:1"] = groupedDetail(items[0])
		m := newTestModel(t, f, Options{})
		m = update(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
		m = feed[detailMsg](t, m, m.selectionChanged())
		m = press(t, m, "2")
		m.code.SetDiffLayout(codeview.LayoutSplit)
		m.layoutNote()
		if m.note != "diff: side-by-side" {
			t.Fatalf("140 columns, default picker: %q", m.note)
		}
		m = press(t, m, ">")
		if !strings.Contains(m.note, "showing unified") {
			t.Errorf("wide picker: %q", m.note)
		}
		m = press(t, m, "<")
		if strings.Contains(m.note, "showing unified") {
			t.Errorf("narrowed again: %q", m.note)
		}
	})
	t.Run("widths", func(t *testing.T) {
		m, rig := resizeModel(t, 140)
		m = press(t, m, "2")
		rig.code.layout = codeview.LayoutSplit
		m = press(t, m, ">")
		if got := rig.code.sizes[len(rig.code.sizes)-1]; got[0] != 100 || got[0] >= codeview.SplitMinWidth {
			t.Errorf("code width %v", got)
		}
		want := "side-by-side needs a 102-column pane (pane has 100; < narrows the picker) — showing unified"
		if m.note != want {
			t.Errorf("note %q, want %q", m.note, want)
		}
		m = press(t, m, "<")
		if got := rig.code.sizes[len(rig.code.sizes)-1]; got[0] != codeview.SplitMinWidth || m.note != "diff: side-by-side" {
			t.Errorf("narrowed: code width %v note %q", got, m.note)
		}
	})
}

// ---- clicks still land after a resize ------------------------------------------------

func TestTabClicksAfterResize(t *testing.T) {
	for _, w := range []int{100, 120, 140, 200} {
		m, p := tabModel(t, w, false, nil)
		m.sideUser = 50
		m.relayout()
		for _, c := range []struct {
			label string
			want  Tab
		}{{"2 Code", TabCode}, {"3 Architecture", TabArch}, {"1 Overview", TabOverview}} {
			x, ok := tabX(t, m, c.label)
			if !ok {
				t.Fatalf("w=%d: tab %q not drawn", w, c.label)
			}
			updates := p.updates
			m = update(t, m, mouseClick(x, 1))
			if m.tab != c.want || p.updates != updates {
				t.Errorf("w=%d: click on %q: tab %v, page updates %d → %d", w, c.label, m.tab, updates, p.updates)
			}
		}
	}
}

func TestPickerClicksAfterResize(t *testing.T) {
	m, _ := resizeModel(t, 140)
	m.sideUser = 60
	m.relayout()
	// Column 50 was the main pane before; now it is a picker row.
	m = update(t, m, mouseClick(50, 5))
	if selectedID(m) != "pr:2" {
		t.Errorf("click at column 50: selected %s", selectedID(m))
	}
	// And the Code page gets main-pane clicks with its new origin.
	m = press(t, m, "2")
	rig := m.code.(*fakePane)
	m = update(t, m, mouseClick(70, 8))
	if len(rig.mice) != 1 || rig.origin != [2]int{62, 2} {
		t.Errorf("code clicks %d origin %v", len(rig.mice), rig.origin)
	}
}

func TestOverviewClicksAfterResize(t *testing.T) {
	m, p := blockModel(t, nil)
	m.sideUser = 50
	m.relayout()
	sw, _, _ := m.dims()
	if sw != 50 {
		t.Fatalf("picker %d", sw)
	}
	at := func(m Model, sub string) (x, y int) {
		for i, l := range strings.Split(m.overview.view(), "\n") {
			if c := strings.Index(l, sub); c >= 0 {
				return sw + 2 + ansi.StringWidth(l[:c]) + 1, i + 2
			}
		}
		t.Fatalf("no %q in view:\n%s", sub, m.overview.view())
		return 0, 0
	}
	x, y := at(m, "+4 more")
	m = update(t, m, mouseClick(x, y))
	if m.overview.list == nil || m.overview.list.block != blockHeadlines {
		t.Fatalf("click on +4 more after a resize: %+v", m.overview.list)
	}
	m = press(t, m, "esc", "esc")
	m.overview.vp.GotoTop()
	x, y = at(m, "refresh(token)")
	m = update(t, m, mouseClick(x, y))
	if fmt.Sprint(p.focused) != "[u-refresh]" {
		t.Errorf("click on a headline row: %v", p.focused)
	}
}

func TestMinimapClickAfterResize(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	m, p := overviewFocused(t, d)
	m.sideUser = 30
	m.relayout()
	row := lineOf(t, m, "web/src/session.ts  ")
	cs := m.overview.cells[row]
	if len(cs) != 1 || len(cs[0].ids) != 2 {
		t.Fatalf("minimap row %d cells = %+v", row, cs)
	}
	m.overview.vp.SetYOffset(row)
	sw, _, _ := m.dims()
	if sw != 30 {
		t.Fatalf("picker %d", sw)
	}
	m = update(t, m, mouseClick(sw+2+cs[0].col+1, row-m.overview.vp.YOffset+2))
	if m.tab != TabCode || fmt.Sprint(p.focused) != "[u-expired]" {
		t.Errorf("click on the second cell after a resize: tab %v focused %v", m.tab, p.focused)
	}
}

// The help overlay lists the resize keys in every scope and the mouse drag.
func TestHelpListsResize(t *testing.T) {
	m := newTestModel(t, newFake(threeItems()...), Options{})
	m = update(t, m, tea.WindowSizeMsg{Width: 200, Height: 120})
	m = feed[detailMsg](t, m, m.selectionChanged())
	for _, ks := range [][]string{{"?"}, {"enter", "?"}, {"enter", "2", "?"}, {"enter", "3", "?"}, {"enter", "P", "?"}} {
		v := press(t, m, ks...).View()
		for _, want := range []string{"< narrower pane", "> wider pane", "drag the picker's or the Code list's right border"} {
			if !strings.Contains(v, want) {
				t.Errorf("%v: help lacks %q", ks, want)
			}
		}
		if strings.Contains(v, "fold repo") {
			t.Errorf("%v: a single repo's help lists the workspace keys", ks)
		}
	}
	// The hint bar does not grow (snapshots stay byte-stable).
	if s := m.viewStatus(); strings.Contains(s, "narrower") || strings.Contains(s, "wider") {
		t.Errorf("hint bar %q", s)
	}
}
