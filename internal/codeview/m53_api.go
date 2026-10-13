package codeview

import (
	"strconv"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// M5.3 shared contract: a resizable file/group list (DECISIONS "M5.3 shared
// contracts"). The user drags the list's right border with the mouse (left
// press on the border column, motion with the button held, release), or the
// root model calls SetListWidth for its `<` / `>` keys while the Code page
// has focus. The root reads PendingListWidth after a drag to persist the
// width (internal/uistate) and routes every mouse message to the page while
// Dragging reports true, even when the pointer leaves the page.
//
// The lead fixed these signatures; the components work package filled in
// the bodies.
//
// Geometry (DECISIONS "M5.3 lead calls", Resizing): the list is listMin
// (16) to page width − paneKeepW (40) columns, hidden below narrowBelow
// (60). The drag handle is the list box's right border column and the gap
// after it (boxed: x = listW−1 or listW), or the separator column (unboxed:
// x = listW), on any body row.

// ListWidthStep is how many columns one `<` / `>` press moves the list
// border (the root's keys use it).
const ListWidthStep = 2

// paneKeepW is the least width the diff pane side keeps (the gap, the pane
// box and its content) when the user widens the list.
const paneKeepW = 40

// Drag glyphs: the list's right border is drawn heavy while it is dragged
// (┒ and ┚ join the heavy edge to the light top and bottom borders).
const (
	dragEdge         = "┃"
	dragCornerTop    = "┒"
	dragCornerBottom = "┚"
)

// listResize is the M5.3 state of Model.
type listResize struct {
	// user is the width the user chose (outer columns); 0 is automatic.
	user int
	// dragging is set between a left press on the list border and the
	// release; pending holds a finished resize for PendingListWidth.
	dragging bool
	pending  int
	hasPend  bool
	// grab is the pointer's column minus the list width at the press (-1
	// on a box border, 0 on the gap or the unboxed separator), so the
	// border keeps its place under the pointer; start is user at the press
	// (a drag that changed nothing records no pending width).
	grab  int
	start int
}

// SetListWidth sets the list's outer width in columns; 0 restores the
// automatic width (a third of the page, 16–40 columns). The width is
// clamped when applied (at least 16 columns, and the diff pane keeps at
// least 40), so a value saved on a wider terminal is safe; the stored
// request is kept, so widening the terminal again restores it. When the
// pane's width changes the page is re-laid out like SetSize: rows are
// rebuilt keeping the current unit in place, the cursor stays in view and
// an open composer or thread panel follows the new width.
func (m *Model) SetListWidth(w int) {
	old := m.layout()
	m.listResize.user = max(0, w)
	m.relayout(old)
}

// ListWidth is the list's current outer width in columns after clamping
// (0 while the list is hidden below narrowBelow columns).
func (m *Model) ListWidth() int { return m.layout().listW }

// Dragging reports whether a drag of the list border is in progress.
func (m Model) Dragging() bool { return m.listResize.dragging }

// PendingListWidth returns, once, the width a finished drag left the list
// at (the root persists it); ok is false when no drag finished since the
// last call. `<` / `>` go through SetListWidth and do not set it. A press
// and release that did not move the border records nothing, so an
// automatic width stays automatic.
func (m *Model) PendingListWidth() (w int, ok bool) {
	if !m.listResize.hasPend {
		return 0, false
	}
	m.listResize.hasPend = false
	return m.listResize.pending, true
}

// listWidthFor is the list's outer width on a page w columns wide (at or
// above narrowBelow): the automatic width when user is 0, else user
// clamped to listMin..w−paneKeepW.
func listWidthFor(w, user int) int {
	if user <= 0 {
		return max(listMin, min(listMax, w/3))
	}
	return clamp(user, listMin, max(listMin, w-paneKeepW))
}

// relayout follows a geometry change from old (SetSize, SetListWidth, a
// drag): rows are rebuilt when the pane width or the side-by-side state
// changed, else only the scroll offsets are clamped; the composer and
// thread panel inputs follow the width and the cursor stays in view.
func (m *Model) relayout(old layout) {
	if now := m.layout(); now.paneW != old.paneW || now.split != old.split {
		m.rebuildKeep() // header chips, comment bodies and split columns follow the pane width
	} else {
		m.clamp()
	}
	if m.comp != nil {
		m.comp.ta.SetWidth(max(4, m.layout().paneW-blockIndent))
	}
	if m.panel != nil {
		_, cw := m.panelGeom()
		m.panel.ta.SetWidth(max(4, cw))
	}
	m.keepCursorVisible()
}

// dragAllowed reports whether a press may start a drag: the list is shown
// over units (not the plain diff fallback) and no composer, thread panel or
// label picker holds the page.
func (m *Model) dragAllowed(lay layout) bool {
	return lay.listW > 0 && !m.c.plain() && m.comp == nil && m.panel == nil && m.picker == nil
}

// onListBorder reports whether the page cell (x, y) is the list's drag
// handle: the box's right border column or the gap after it (boxed), the
// separator column (unboxed), on a body row.
func onListBorder(lay layout, x, y int) bool {
	if lay.listW == 0 || y < lay.top || y >= lay.top+lay.bodyH {
		return false
	}
	if lay.boxed {
		return x == lay.listW-1 || x == lay.listW
	}
	return x == lay.listW
}

// startDrag begins a drag grabbed at page column x.
func (m *Model) startDrag(lay layout, x int) {
	m.listResize.dragging = true
	m.listResize.grab = x - lay.listW
	m.listResize.start = m.listResize.user
	m.clamp() // the bar line with the drag note may have appeared
	m.keepCursorVisible()
}

// dragMouse handles a mouse message during a drag at page column x:
// motion with the left button moves the border, a release (or motion with
// no button, a release the terminal did not report) ends the drag. A left
// press also ends it (its release was lost) and reports false, so the
// caller handles the press as usual; any other message is swallowed.
func (m *Model) dragMouse(mm tea.MouseMsg, x int) bool {
	switch {
	case mm.Action == tea.MouseActionMotion && mm.Button == tea.MouseButtonLeft:
		m.dragTo(x)
	case mm.Action == tea.MouseActionRelease,
		mm.Action == tea.MouseActionMotion && mm.Button == tea.MouseButtonNone:
		m.endDrag()
	case mm.Action == tea.MouseActionPress && mm.Button == tea.MouseButtonLeft:
		m.endDrag()
		return false
	}
	return true
}

// dragTo moves the border so it stays under the pointer at page column x,
// clamped like SetListWidth. Nothing moves while the list is hidden.
func (m *Model) dragTo(x int) {
	old := m.layout()
	if old.listW == 0 {
		return
	}
	// At or left of the page edge the list is at its minimum (a width ≤ 0
	// would read as automatic).
	w := listWidthFor(m.w, max(listMin, x-m.listResize.grab))
	if w == old.listW {
		return // e.g. moving along the border: an automatic width stays automatic
	}
	m.listResize.user = w
	m.relayout(old)
}

// endDrag ends the drag; a drag that changed the width leaves it for
// PendingListWidth.
func (m *Model) endDrag() {
	m.listResize.dragging = false
	if m.listResize.user != m.listResize.start {
		m.listResize.pending, m.listResize.hasPend = m.listResize.user, true
	}
	m.clamp() // the drag note's bar line goes
	m.keepCursorVisible()
}

// dragShown reports whether the drag note takes the bar line: a drag is in
// progress and the list is shown (the page is at least narrowBelow wide).
// layout() reads it, so it cannot ask layout() itself.
func (m *Model) dragShown() bool { return m.listResize.dragging && m.w >= narrowBelow }

// dragNote is the bar line during a drag: "resize list: N cols · release
// to keep".
func dragNote(listW int) string {
	return "resize list: " + strconv.Itoa(listW) + " cols · release to keep"
}

// heavyBorder redraws the list box's right border column (the last cell of
// each box line) heavy in the accent colour while a drag is in progress.
func (m Model) heavyBorder(box []string, listW int) []string {
	if !m.listResize.dragging {
		return box
	}
	out := make([]string, len(box))
	for i, l := range box {
		g := dragEdge
		switch i {
		case 0:
			g = dragCornerTop
		case len(box) - 1:
			g = dragCornerBottom
		}
		out[i] = ansi.Truncate(l, listW-1, "") + m.th.Accent.Render(g)
	}
	return out
}

// separator is the column between the list and the pane when they are not
// boxed: blank, or the heavy accent edge during a drag.
func (m Model) separator() string {
	if m.listResize.dragging {
		return m.th.Accent.Render(dragEdge)
	}
	return " "
}
