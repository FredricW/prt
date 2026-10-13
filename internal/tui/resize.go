package tui

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

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

// mainLimit is the widest the picker may be while the main pane keeps 40
// columns: min(sideMax, width − mainKeep).
func (m Model) mainLimit() int { return min(sideMax, m.width-mainKeep) }

// sideLimit is the widest the picker may be at this terminal width:
// mainLimit, but never narrower than the automatic width (on 62–69 columns
// the automatic width is the wider, and `>` or a drag from it must not
// shrink the picker).
func (m Model) sideLimit() int { return max(m.mainLimit(), sidebarWidth(m.width)) }

// sideResizable reports whether the terminal leaves room to resize the
// picker (below sideMin+mainKeep columns the width is automatic).
func (m Model) sideResizable() bool { return m.mainLimit() >= sideMin }

// clampSide clamps a requested picker width (0: automatic) to
// [sideMin, sideLimit]; the automatic width when the terminal is too
// narrow to resize.
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
// column, a release ends the drag and saves the widths. A lost release
// (the button let go outside the window, under tmux) is recovered as the
// Code list's drag does: a left press ends the drag where it is (saved) and
// is then handled as a press, and motion with no button ends it too.
// Anything else (the wheel) is swallowed.
func (m Model) sideDragMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Action == tea.MouseActionMotion && msg.Button == tea.MouseButtonLeft:
		m.dragMoved = true
		m.resizeSide(msg.X)
	case msg.Action == tea.MouseActionRelease:
		if m.dragMoved {
			m.resizeSide(msg.X)
		}
		return m.endSideDrag()
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		ended, save := m.endSideDrag()
		next, cmd := ended.(Model).handleMouse(msg)
		return next, batch(save, cmd)
	case msg.Action == tea.MouseActionMotion && msg.Button == tea.MouseButtonNone:
		return m.endSideDrag()
	}
	return m, nil
}

// endSideDrag ends a picker drag at the current width and saves the widths.
func (m Model) endSideDrag() (tea.Model, tea.Cmd) {
	m.sideDrag, m.dragMoved = false, false
	m.splitNote()
	return m, m.saveLayout()
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

// layoutSaves orders the layout saves. Bubble Tea runs every command on its
// own goroutine, so the saves of a burst of `<`/`>` presses (key repeat)
// run concurrently and could land in any order. Each save command carries
// the generation it was made with (in Update order); writes are serialised
// and a command older than the last one written does nothing, so the file
// always ends with the newest widths.
type layoutSaves struct {
	gen     atomic.Uint64 // the last generation handed out
	mu      sync.Mutex
	written uint64 // the generation last written (guarded by mu)
}

// write saves l as generation g unless a newer generation was written.
func (s *layoutSaves) write(g uint64, l uistate.Layout, save func(uistate.Layout) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g <= s.written {
		return nil
	}
	s.written = g
	return save(l)
}

// pending reports whether a save was asked for and not written yet.
func (s *layoutSaves) pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written < s.gen.Load()
}

// layoutState is the widths to persist.
func (m Model) layoutState() uistate.Layout {
	return uistate.Layout{SidebarWidth: m.sideUser, CodeListWidth: m.codeListUser}
}

// saveLayout persists both widths through Options.SaveLayout (nil: none),
// ordered by layoutSaves.
func (m Model) saveLayout() tea.Cmd {
	save, s := m.opts.SaveLayout, m.saves
	if save == nil {
		return nil
	}
	l := m.layoutState()
	if s == nil {
		return func() tea.Msg { return layoutSavedMsg{err: save(l)} }
	}
	g := s.gen.Add(1)
	return func() tea.Msg { return layoutSavedMsg{err: s.write(g, l, save)} }
}

// FlushLayout writes the current widths now when a save is still pending
// (Bubble Tea does not wait for running commands when the program ends, so
// a resize just before quitting could be lost or half written); call it
// on the final model after the program returns. Nothing is written when
// every save already landed or without Options.SaveLayout.
func (m Model) FlushLayout() error {
	save, s := m.opts.SaveLayout, m.saves
	if save == nil || s == nil || !s.pending() {
		return nil
	}
	return s.write(s.gen.Add(1), m.layoutState(), save)
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
