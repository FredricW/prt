package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/uistate"
)

// Resizable sidebars (M5.3, DECISIONS "M5.3 lead calls: workspace, picker
// and resizing"). The picker's right border column (and the gap after it)
// is a drag handle: a left press there starts a drag, motion with the
// button held moves the border, release ends it and saves the widths. The
// Code page list's border is codeview's own handle; the root routes every
// mouse message to the page while it drags and saves the width it reports.
// `<` and `>` move the focused pane's border by 2 columns (the Code list
// while the Code tab has focus, else the picker). Widths are kept as the
// user left them and clamped when applied; 0 is automatic.

const (
	// sideMin and sideMax bound a user-sized picker; mainKeep is what the
	// box's border, the gap and a 40-column main pane need, so the picker
	// is at most width − 42 (below sideMin the automatic width applies).
	sideMin  = 20
	sideMax  = 80
	mainKeep = 42
	// sideStep is how far one `<` / `>` moves the picker's border.
	sideStep = 2
)

// layoutSavedMsg answers Options.SaveLayout.
type layoutSavedMsg struct{ err error }

// sidebarWidth adapts the picker to narrow terminals: the automatic width.
func sidebarWidth(total int) int {
	if total >= 100 {
		return 36
	}
	return max(16, min(36, total*2/5))
}

// sideWidth is the picker's width now: the user's, clamped, else automatic.
func (m Model) sideWidth() int { return m.clampSide(m.sideUser) }

// sideLimit is the widest the picker may be at this terminal width.
func (m Model) sideLimit() int { return min(sideMax, m.width-mainKeep) }

// sideResizable reports whether the terminal leaves room to resize the
// picker (below sideMin+mainKeep columns the width is automatic).
func (m Model) sideResizable() bool { return m.sideLimit() >= sideMin }

// clampSide clamps a requested picker width (0: automatic) to
// [sideMin, min(sideMax, width − mainKeep)]; the automatic width when the
// terminal is too narrow for that.
func (m Model) clampSide(w int) int {
	if w <= 0 || !m.sideResizable() {
		return sidebarWidth(m.width)
	}
	return max(sideMin, min(m.sideLimit(), w))
}

// applyLayout takes the saved widths (Options.Layout).
func (m *Model) applyLayout(l uistate.Layout) {
	m.sideUser, m.codeListUser = max(0, l.SidebarWidth), max(0, l.CodeListWidth)
	if m.codeListUser > 0 {
		m.code.SetListWidth(m.codeListUser)
	}
}

// relayout lays the screen out again after a width change: picker and
// prompt, the pages' sizes and origins, the Overview's wrapping (keeping
// its scroll position, so a drag does not jump to the top), the form.
func (m *Model) relayout() {
	y := m.overview.vp.YOffset
	m.layout()
	m.overview.vp.SetYOffset(y)
	m.sizeForm()
}

// splitNote follows a width change on the Code tab with side-by-side
// requested: it says whether it is in effect (layoutNote).
func (m *Model) splitNote() {
	if m.tab == TabCode && m.code.DiffLayout() == codeview.LayoutSplit {
		m.layoutNote()
	}
}

// startsSideDrag reports whether msg is a left press on the picker's right
// border or the gap after it (body rows only), on a terminal wide enough to
// resize it.
func (m Model) startsSideDrag(msg tea.MouseMsg) bool {
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress || !m.sideResizable() {
		return false
	}
	sw, _, bh := m.dims()
	return msg.Y >= 1 && msg.Y <= bh && (msg.X == sw || msg.X == sw+1)
}

// sideDragMouse takes every mouse message while the picker's border is
// dragged: motion with the left button moves the border to the pointer's
// column, a release ends the drag and saves the widths; anything else is
// swallowed.
func (m Model) sideDragMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Action == tea.MouseActionMotion && msg.Button == tea.MouseButtonLeft:
		m.dragMoved = true
		m.resizeSide(msg.X)
	case msg.Action == tea.MouseActionRelease:
		if m.dragMoved {
			m.resizeSide(msg.X)
		}
		m.sideDrag, m.dragMoved = false, false
		m.splitNote()
		return m, m.saveLayout()
	}
	return m, nil
}

// resizeSide puts the picker's right border at column x (clamped).
func (m *Model) resizeSide(x int) {
	if !m.sideResizable() {
		return
	}
	m.sideUser = m.clampSide(max(1, x))
	m.relayout()
}

// codeMouse hands a mouse message to the Code page (which starts and runs
// its own list drag) and saves the list width a finished drag reports.
func (m Model) codeMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	cmd := m.code.Update(msg)
	var save tea.Cmd
	if w, ok := m.code.PendingListWidth(); ok {
		m.codeListUser = w
		save = m.saveLayout()
	}
	return m, batch(cmd, m.codeLabels(), save)
}

// resizeKey is `<` (narrower) and `>` (wider): the Code page list while the
// Code tab has focus, else the picker. Each press saves the widths.
func (m Model) resizeKey(wider bool) (Model, tea.Cmd) {
	dir := -1
	if wider {
		dir = 1
	}
	if m.tab == TabCode && m.focus != FocusPicker {
		cur := m.code.ListWidth()
		if cur <= 0 {
			m.note = "list hidden below 60 columns"
			return m, nil
		}
		want := cur + dir*codeview.ListWidthStep
		m.code.SetListWidth(want)
		m.codeListUser = want
		if w := m.code.ListWidth(); w > 0 {
			m.codeListUser = w
		}
		m.note = fmt.Sprintf("code list: %d cols", m.codeListUser)
		return m, m.saveLayout()
	}
	if !m.sideResizable() {
		m.note = fmt.Sprintf("the picker's width is automatic below %d columns", sideMin+mainKeep)
		return m, nil
	}
	m.sideUser = m.clampSide(m.sideWidth() + dir*sideStep)
	m.relayout()
	m.note = fmt.Sprintf("picker: %d cols", m.sideWidth())
	m.splitNote()
	return m, m.saveLayout()
}

// saveLayout persists both widths through Options.SaveLayout (nil: none).
func (m Model) saveLayout() tea.Cmd {
	save := m.opts.SaveLayout
	if save == nil {
		return nil
	}
	l := uistate.Layout{SidebarWidth: m.sideUser, CodeListWidth: m.codeListUser}
	return func() tea.Msg { return layoutSavedMsg{err: save(l)} }
}

// heavyBorder redraws the sidebar box's right border column heavy (┓ ┃ ┛)
// in the accent colour while it is dragged.
func (m Model) heavyBorder(side string, sw int) string {
	lines := strings.Split(side, "\n")
	for i, l := range lines {
		g := "┃"
		switch i {
		case 0:
			g = "┓"
		case len(lines) - 1:
			g = "┛"
		}
		t := ansi.Truncate(l, sw, "")
		lines[i] = t + strings.Repeat(" ", max(0, sw-ansi.StringWidth(t))) + m.st.accent.Render(g)
	}
	return strings.Join(lines, "\n")
}

// dragStatus is the status line during a resize drag.
func (m Model) dragStatus() string {
	if m.code.Dragging() {
		return fmt.Sprintf("resize code list: %d cols · release to keep", m.code.ListWidth())
	}
	return fmt.Sprintf("resize picker: %d cols · release to keep", m.sideWidth())
}
