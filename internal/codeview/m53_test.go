package codeview

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/theme"
)

// mouse sends one mouse message at absolute (x, y).
func mouse(m *Model, x, y int, b tea.MouseButton, a tea.MouseAction) {
	*m, _ = m.Update(tea.MouseMsg{X: x, Y: y, Button: b, Action: a})
}

// borderX is the page column of the list's drag handle: the box's right
// border, or the separator column when the page is not boxed.
func borderX(lay layout) int {
	if lay.boxed {
		return lay.listW - 1
	}
	return lay.listW
}

// autoWidth is the automatic list width at page width w (unchanged by M5.3).
func autoWidth(w int) int { return max(listMin, min(listMax, w/3)) }

func TestSetListWidthClamps(t *testing.T) {
	tests := []struct {
		w, req, want int
	}{
		{60, 0, 20}, {80, 0, 26}, {102, 0, 34}, {140, 0, 40}, {200, 0, 40}, // 0: automatic
		{80, 10, 16}, {80, 16, 16}, {80, 30, 30}, {80, 40, 40}, {80, 41, 40}, {80, 500, 40},
		{60, 17, 17}, {60, 30, 20}, // the pane keeps 40 columns
		{102, 62, 62}, {102, 63, 62},
		{140, 48, 48}, {140, 100, 100}, {140, 101, 100},
		{200, 160, 160}, {200, 999, 160},
		{140, -5, 40}, // negative reads as automatic
		{50, 30, 0},   // hidden below 60 columns
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("w%d_req%d", tt.w, tt.req), func(t *testing.T) {
			m := newModel(t, loadDetail(t), tt.w, 30)
			m.SetListWidth(tt.req)
			if got := m.ListWidth(); got != tt.want {
				t.Fatalf("ListWidth = %d, want %d", got, tt.want)
			}
			lay := m.layout()
			if lay.listW > 0 && (lay.paneW != tt.w-lay.listW-3 || lay.listIn != lay.listW-2) {
				t.Errorf("layout %+v does not follow the list width", lay)
			}
			if tt.req > 0 && lay.listW > 0 && tt.w-lay.listW < paneKeepW && lay.listW > listMin {
				t.Errorf("pane side has %d columns, want ≥ %d", tt.w-lay.listW, paneKeepW)
			}
			checkView(t, "after SetListWidth", m.View(), tt.w, 30)
		})
	}
}

func TestListWidthRememberedAcrossSizes(t *testing.T) {
	m := newModel(t, loadDetail(t), 140, 30)
	m.SetListWidth(48)
	for _, st := range []struct{ w, want int }{
		{140, 48}, {80, 40}, {50, 0}, {60, 20}, {200, 48}, {140, 48},
	} {
		m.SetSize(st.w, 30)
		if got := m.ListWidth(); got != st.want {
			t.Errorf("at %d columns ListWidth = %d, want %d", st.w, got, st.want)
		}
		checkView(t, fmt.Sprintf("%d columns", st.w), m.View(), st.w, 30)
	}
	m.SetListWidth(0)
	if got := m.ListWidth(); got != autoWidth(140) {
		t.Errorf("SetListWidth(0) = %d, want the automatic %d", got, autoWidth(140))
	}
	if _, ok := m.PendingListWidth(); ok {
		t.Error("SetListWidth recorded a pending width (only drags do)")
	}
}

func TestDragResize(t *testing.T) {
	const ox, oy = 3, 2
	for _, w := range []int{60, 80, 102, 140, 200} {
		for _, boxed := range []bool{true, false} {
			h := 30
			if !boxed {
				h = 2 // below minBoxH body rows: no boxes, a separator column
			}
			t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
				m := newModel(t, loadDetail(t), w, h)
				m.SetOrigin(ox, oy)
				lay := m.layout()
				if lay.boxed != boxed || lay.listW != autoWidth(w) {
					t.Fatalf("layout %+v, want boxed %v and the automatic width %d", lay, boxed, autoWidth(w))
				}
				grab := borderX(lay) - lay.listW // -1 boxed, 0 unboxed
				hi := max(listMin, w-paneKeepW)
				mouse(&m, ox+borderX(lay), oy+lay.top+lay.bodyH-1, tea.MouseButtonLeft, tea.MouseActionPress)
				if !m.Dragging() {
					t.Fatal("a press on the border did not start a drag")
				}
				if got := m.ListWidth(); got != autoWidth(w) {
					t.Errorf("the press moved the border to %d", got)
				}
				want := 0
				for _, target := range []int{0, w + 20, 30, 48} {
					mouse(&m, ox+target+grab, oy, tea.MouseButtonLeft, tea.MouseActionMotion)
					want = clamp(target, listMin, hi)
					if got := m.ListWidth(); got != want {
						t.Errorf("motion to width %d: ListWidth = %d, want %d", target, got, want)
					}
					if !m.Dragging() {
						t.Fatal("motion ended the drag")
					}
					v := m.View()
					checkView(t, "while dragging", v, w, h)
					if ls := strings.Split(v, "\n"); ls[0] != dragNote(want) {
						t.Errorf("bar line = %q, want %q", ls[0], dragNote(want))
					}
				}
				if _, ok := m.PendingListWidth(); ok {
					t.Error("pending width before the release")
				}
				mouse(&m, ox+want+grab, oy, tea.MouseButtonLeft, tea.MouseActionRelease)
				if m.Dragging() {
					t.Error("release did not end the drag")
				}
				if got := m.ListWidth(); got != want {
					t.Errorf("after release ListWidth = %d, want %d", got, want)
				}
				if got, ok := m.PendingListWidth(); !ok || got != want {
					t.Errorf("PendingListWidth = %d, %v; want %d, true", got, ok, want)
				}
				if _, ok := m.PendingListWidth(); ok {
					t.Error("PendingListWidth returned the width twice")
				}
				if v := m.View(); strings.Contains(v, "resize list") || strings.Contains(v, dragEdge) {
					t.Errorf("drag affordance left after the release:\n%s", v)
				}
			})
		}
	}
}

// Grabbed on the gap after the box, the border keeps its place left of the
// pointer instead of jumping under it.
func TestDragFromGap(t *testing.T) {
	m := newModel(t, loadDetail(t), 140, 30)
	lay := m.layout()
	mouse(&m, lay.listW, 5, tea.MouseButtonLeft, tea.MouseActionPress) // the gap
	if !m.Dragging() {
		t.Fatal("a press on the gap did not start a drag")
	}
	mouse(&m, lay.listW, 5, tea.MouseButtonLeft, tea.MouseActionMotion)
	if got := m.ListWidth(); got != lay.listW {
		t.Errorf("motion in place moved the border to %d", got)
	}
	mouse(&m, 50, 5, tea.MouseButtonLeft, tea.MouseActionMotion)
	if got := m.ListWidth(); got != 50 {
		t.Errorf("ListWidth = %d, want 50 (the gap under the pointer)", got)
	}
	// An X10 release reports no button.
	mouse(&m, 50, 5, tea.MouseButtonNone, tea.MouseActionRelease)
	if m.Dragging() {
		t.Error("a release without a button did not end the drag")
	}
	if got, ok := m.PendingListWidth(); !ok || got != 50 {
		t.Errorf("PendingListWidth = %d, %v", got, ok)
	}
}

func TestPressAndReleaseInPlaceRecordsNothing(t *testing.T) {
	m := newModel(t, loadDetail(t), 140, 30)
	before := m.View()
	lay := m.layout()
	mouse(&m, borderX(lay), 3, tea.MouseButtonLeft, tea.MouseActionPress)
	mouse(&m, borderX(lay), 9, tea.MouseButtonLeft, tea.MouseActionMotion) // along the border
	mouse(&m, borderX(lay), 9, tea.MouseButtonLeft, tea.MouseActionRelease)
	if _, ok := m.PendingListWidth(); ok {
		t.Error("a drag that did not move the border recorded a width")
	}
	if m.listResize.user != 0 {
		t.Errorf("the automatic width became %d", m.listResize.user)
	}
	if got := m.View(); got != before {
		t.Errorf("view changed by a press and release in place:\n%s\nwant\n%s", got, before)
	}
}

func TestDraggingOnlyBetweenPressAndRelease(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 10)
	m.SetOrigin(0, 2)
	steps := []struct {
		name   string
		x, y   int
		b      tea.MouseButton
		a      tea.MouseAction
		active bool
	}{
		{"motion without a press", 60, 5, tea.MouseButtonLeft, tea.MouseActionMotion, false},
		{"release without a press", 39, 5, tea.MouseButtonLeft, tea.MouseActionRelease, false},
		{"wheel on the border", 39, 5, tea.MouseButtonWheelDown, tea.MouseActionPress, false},
		{"right press on the border", 39, 5, tea.MouseButtonRight, tea.MouseActionPress, false},
		{"press above the body", 39, 1, tea.MouseButtonLeft, tea.MouseActionPress, false},
		{"press below the body", 39, 12, tea.MouseButtonLeft, tea.MouseActionPress, false},
		{"press on the border", 39, 5, tea.MouseButtonLeft, tea.MouseActionPress, true},
		{"motion", 50, 5, tea.MouseButtonLeft, tea.MouseActionMotion, true},
		{"wheel while dragging", 50, 5, tea.MouseButtonWheelUp, tea.MouseActionPress, true},
		{"release", 50, 5, tea.MouseButtonLeft, tea.MouseActionRelease, false},
		{"motion after the release", 70, 5, tea.MouseButtonLeft, tea.MouseActionMotion, false},
	}
	for _, s := range steps {
		mouse(&m, s.x, s.y, s.b, s.a)
		if m.Dragging() != s.active {
			t.Fatalf("%s: Dragging = %v, want %v", s.name, m.Dragging(), s.active)
		}
	}
	if got := m.ListWidth(); got != 51 {
		t.Errorf("ListWidth = %d, want 51 (motion after the release ignored)", got)
	}
}

func TestMotionWithoutPressDoesNothing(t *testing.T) {
	m := newModel(t, loadDetail(t), 140, 30)
	before := m.View()
	for _, x := range []int{0, 20, 39, 40, 80, 139} {
		mouse(&m, x, 5, tea.MouseButtonLeft, tea.MouseActionMotion)
	}
	if m.Dragging() || m.ListWidth() != 40 || m.View() != before {
		t.Errorf("motion without a press: dragging %v width %d", m.Dragging(), m.ListWidth())
	}
	if _, ok := m.PendingListWidth(); ok {
		t.Error("pending width without a drag")
	}
}

func TestPressOnListRowStillSelects(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 10)
	allOff(&m)
	m.SetOrigin(0, 2)
	lay := m.layout()
	// The last content column of the list (left of the border) is a row.
	mouse(&m, lay.listW-2, 2+4, tea.MouseButtonLeft, tea.MouseActionPress)
	if m.Dragging() {
		t.Fatal("a press on a list row started a drag")
	}
	if m.SelectedFile() != "web/legacy/util.ts" {
		t.Errorf("click on row 3: %q", m.SelectedFile())
	}
}

// A press while dragging means the release was lost (released outside the
// terminal): the drag ends and the press acts as usual.
func TestPressWhileDraggingEndsTheDrag(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 10)
	allOff(&m)
	m.SetOrigin(0, 2)
	mouse(&m, 39, 5, tea.MouseButtonLeft, tea.MouseActionPress)
	mouse(&m, 45, 5, tea.MouseButtonLeft, tea.MouseActionMotion)
	mouse(&m, 5, 2+4, tea.MouseButtonLeft, tea.MouseActionPress)
	if m.Dragging() {
		t.Fatal("a second press kept the drag")
	}
	if got, ok := m.PendingListWidth(); !ok || got != 46 {
		t.Errorf("PendingListWidth = %d, %v; want 46", got, ok)
	}
	if m.SelectedFile() != "web/legacy/util.ts" {
		t.Errorf("the press did not select row 3: %q", m.SelectedFile())
	}
}

func TestDragDisabled(t *testing.T) {
	plainDetail := loadDetail(t)
	plainDetail.Units, plainDetail.Diff = nil, plainDiff
	tests := []struct {
		name string
		make func(t *testing.T) Model
	}{
		{"composer open", func(t *testing.T) Model {
			m := commentModel(t, 140, 30)
			press(t, &m, "C")
			if m.comp == nil {
				t.Fatal("C did not open the composer")
			}
			return m
		}},
		{"thread panel open", func(t *testing.T) Model {
			m := commentModel(t, 140, 30)
			press(t, &m, "t")
			if m.panel == nil {
				t.Fatal("t did not open the thread panel")
			}
			return m
		}},
		{"label picker open", func(t *testing.T) Model {
			m := commentModel(t, 140, 30)
			press(t, &m, "!")
			if m.picker == nil {
				t.Fatal("! did not open the label picker")
			}
			return m
		}},
		{"plain diff", func(t *testing.T) Model { return newModel(t, plainDetail, 140, 30) }},
		{"below 60 columns", func(t *testing.T) Model { return newModel(t, loadDetail(t), 59, 30) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.make(t)
			width := m.ListWidth()
			for _, x := range []int{14, 15, 16, 19, 20, 39, 40} {
				mouse(&m, x, 5, tea.MouseButtonLeft, tea.MouseActionPress)
				mouse(&m, x+10, 5, tea.MouseButtonLeft, tea.MouseActionMotion)
				if m.Dragging() {
					t.Fatalf("press at %d started a drag", x)
				}
				mouse(&m, x+10, 5, tea.MouseButtonLeft, tea.MouseActionRelease)
			}
			if m.ListWidth() != width {
				t.Errorf("ListWidth %d, want %d", m.ListWidth(), width)
			}
			if _, ok := m.PendingListWidth(); ok {
				t.Error("pending width without a drag")
			}
		})
	}
}

// Hiding the list mid-drag (the terminal narrowed) freezes the drag; the
// release still ends it.
func TestDragSurvivesHiddenList(t *testing.T) {
	m := newModel(t, loadDetail(t), 140, 30)
	mouse(&m, 39, 5, tea.MouseButtonLeft, tea.MouseActionPress)
	m.SetSize(50, 30)
	mouse(&m, 20, 5, tea.MouseButtonLeft, tea.MouseActionMotion)
	if strings.Contains(m.View(), "resize list") {
		t.Error("drag note shown while the list is hidden")
	}
	mouse(&m, 20, 5, tea.MouseButtonLeft, tea.MouseActionRelease)
	if m.Dragging() {
		t.Error("release did not end the drag")
	}
	if _, ok := m.PendingListWidth(); ok {
		t.Error("a frozen drag recorded a width")
	}
	m.SetSize(140, 30)
	if m.ListWidth() != 40 {
		t.Errorf("ListWidth = %d, want the automatic 40", m.ListWidth())
	}
}

func TestDragRendering(t *testing.T) {
	t.Run("boxed, no colour", func(t *testing.T) {
		m := newModel(t, loadDetail(t), 140, 12)
		before := m.View()
		mouse(&m, 39, 4, tea.MouseButtonLeft, tea.MouseActionPress)
		ls := lines(m)
		if ls[0] != "resize list: 40 cols · release to keep" {
			t.Errorf("bar line = %q", ls[0])
		}
		for i, l := range ls[1:] {
			want := dragEdge
			switch i {
			case 0:
				want = dragCornerTop
			case len(ls) - 2:
				want = dragCornerBottom
			}
			if got := ansi.Cut(l, 39, 40); got != want {
				t.Errorf("body line %d border = %q, want %q", i, got, want)
			}
			if got := ansi.Cut(l, 40, 41); got != " " {
				t.Errorf("body line %d gap = %q", i, got)
			}
		}
		mouse(&m, 39, 4, tea.MouseButtonLeft, tea.MouseActionRelease)
		if got := m.View(); got != before {
			t.Errorf("view after the drag differs (Ascii output must stay byte-identical):\n%s\nwant\n%s", got, before)
		}
	})
	t.Run("unboxed separator", func(t *testing.T) {
		m := newModel(t, loadDetail(t), 80, 2)
		mouse(&m, 26, 1, tea.MouseButtonLeft, tea.MouseActionPress)
		ls := lines(m)
		if len(ls) != 2 || !strings.HasPrefix(ls[0], "resize list: 26 cols") || ansi.Cut(ls[1], 26, 27) != dragEdge {
			t.Errorf("unboxed drag view:\n%s", strings.Join(ls, "\n"))
		}
	})
	t.Run("accent colour", func(t *testing.T) {
		r := trueColourRenderer()
		m := New(r)
		m.SetSize(140, 12)
		m.SetDetail(loadDetail(t))
		mouse(&m, 39, 4, tea.MouseButtonLeft, tea.MouseActionPress)
		accent := sgrColour(r, theme.New(r).P.Accent)
		got := styledWith(m.View(), accent)
		edges := m.layout().bodyH - 2 // between the two corners
		if strings.Count(got, dragEdge) != edges || !strings.Contains(got, dragCornerTop) || !strings.Contains(got, dragCornerBottom) {
			t.Errorf("accent text %q lacks the heavy border", got)
		}
		checkView(t, "colour drag", m.View(), 140, 12)
	})
}

func TestSetListWidthRebuildsPaneAndInputs(t *testing.T) {
	m := commentModel(t, 140, 30)
	moveTo(t, &m, model.SideNew, 12, "")
	press(t, &m, "C")
	cw := m.comp.ta.Width()
	cur, _ := m.CursorLine()
	m.SetListWidth(80)
	if m.comp == nil || m.comp.ta.Width() >= cw {
		t.Fatalf("composer width %d, was %d", m.comp.ta.Width(), cw)
	}
	if pos, _ := m.CursorLine(); pos != cur || !m.cursorVisible() {
		t.Errorf("cursor %+v (visible %v), was %+v", pos, m.cursorVisible(), cur)
	}
	checkView(t, "composer after resize", m.View(), 140, 30)
	m.SetListWidth(0)
	if m.comp.ta.Width() != cw {
		t.Errorf("composer width %d after the automatic width came back, want %d", m.comp.ta.Width(), cw)
	}
}

// Regression: list clicks, the wheel and by-change rows still map onto the
// right entries with a resized list.
func TestMouseAfterResize(t *testing.T) {
	t.Run("by file", func(t *testing.T) {
		m := newModel(t, loadDetail(t), 140, 10)
		allOff(&m)
		m.SetOrigin(0, 2)
		m.SetListWidth(60)                                               // the pane keeps TestMouse's 77 content columns
		mouse(&m, 70, 5, tea.MouseButtonWheelDown, tea.MouseActionPress) // over the pane
		if m.SelectedFile() != "web/src/api/client.ts" || m.off != wheelStep {
			t.Errorf("pane wheel: file %q off %d", m.SelectedFile(), m.off)
		}
		mouse(&m, 70, 5, tea.MouseButtonWheelUp, tea.MouseActionPress)
		if m.off != 0 {
			t.Errorf("pane wheel up: off %d", m.off)
		}
		mouse(&m, 45, 5, tea.MouseButtonWheelDown, tea.MouseActionPress) // over the wide list
		if m.SelectedFile() != "web/src/api/session.ts" {
			t.Errorf("list wheel: %q", m.SelectedFile())
		}
		mouse(&m, 50, 2+4, tea.MouseButtonLeft, tea.MouseActionPress) // list row 3, right of the old border
		if m.SelectedFile() != "web/legacy/util.ts" {
			t.Errorf("click: %q", m.SelectedFile())
		}
		mouse(&m, 58, 2+1, tea.MouseButtonLeft, tea.MouseActionPress) // last content column
		if m.SelectedFile() != "web/src/api/client.ts" || m.Dragging() {
			t.Errorf("click on the first row: %q (dragging %v)", m.SelectedFile(), m.Dragging())
		}
	})
	t.Run("by change", func(t *testing.T) {
		m := byChangeModel(t, loadGroups(t), 140, 30, true)
		m.SetOrigin(0, 2)
		m.SetListWidth(70)
		click := func(row int) { mouse(&m, 66, 2+row, tea.MouseButtonLeft, tea.MouseActionPress) }
		click(5) // g-apierror's second line
		if groupID(m) != "g-apierror" {
			t.Errorf("click: %q", groupID(m))
		}
		click(10) // ungrouped divider: no change
		if groupID(m) != "g-apierror" {
			t.Errorf("click on the divider: %q", groupID(m))
		}
		click(18) // split suggestion row → the file's group
		if groupID(m) != "g-theme" || curID(m) != "s1" {
			t.Errorf("split row: %q %q", groupID(m), curID(m))
		}
		mouse(&m, 30, 5, tea.MouseButtonWheelUp, tea.MouseActionPress)
		if groupID(m) == "g-theme" {
			t.Errorf("wheel over the list did not move the selection")
		}
	})
}

// The side-by-side threshold still compares the component width; a wide
// list only narrows the split columns.
func TestSplitWithWideList(t *testing.T) {
	m := splitModel(t, loadDetail(t), 200, 30)
	for _, req := range []int{100, 160} {
		m.SetListWidth(req)
		lay := m.layout()
		if !lay.split || !hasKind(m, rowSplit) {
			t.Fatalf("list %d: split %v", req, lay.split)
		}
		if lay.paneW != 200-req-3 {
			t.Errorf("list %d: paneW %d", req, lay.paneW)
		}
		rows := splitRows(t, m)
		if len(rows) == 0 {
			t.Fatalf("list %d: no side-by-side rows:\n%s", req, m.View())
		}
		g := newSplitGeom(lay.paneW, m.p.gutter)
		paired := false
		for _, r := range rows {
			left, right := ansi.Cut(r, 0, g.lw), ansi.Cut(r, g.lw+1, lay.paneW)
			paired = paired || (strings.Contains(left, " - ") && strings.Contains(right, " + "))
		}
		if !paired {
			t.Errorf("list %d: no row pairs a deletion with an addition:\n%s", req, strings.Join(rows, "\n"))
		}
		checkView(t, fmt.Sprintf("split with list %d", req), m.View(), 200, 30)
	}
	m.SetSize(SplitMinWidth-1, 30)
	if m.layout().split {
		t.Error("side-by-side below SplitMinWidth")
	}
	if strings.Contains(m.View(), "resize list") {
		t.Error("drag note without a drag")
	}
}

func TestGoldenListWide(t *testing.T) {
	m := newModel(t, loadDetail(t), 140, 30)
	m.SetListWidth(48)
	if m.ListWidth() != 48 {
		t.Fatalf("ListWidth = %d", m.ListWidth())
	}
	checkGolden(t, "list_wide", m.View()+"\n")
}

// Every geometry the clamp allows renders within bounds, boxed and not.
func TestListWidthViewDimensions(t *testing.T) {
	for _, d := range []proto.Detail{loadDetail(t), loadGroups(t)} {
		for _, size := range [][2]int{{60, 2}, {60, 20}, {80, 2}, {102, 30}, {140, 30}, {200, 30}} {
			for _, req := range []int{16, 30, 61, 999} {
				m := newModel(t, d, size[0], size[1])
				m.SetListWidth(req)
				checkView(t, fmt.Sprintf("%dx%d list %d", size[0], size[1], req), m.View(), size[0], size[1])
			}
		}
	}
}
