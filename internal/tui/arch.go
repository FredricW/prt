package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/archview"
	"github.com/fredricw/prt/internal/proto"
)

// archPane is what the root model needs from the Architecture tab (3). The
// real page is *archview.Model; tests substitute a recording stand-in.
type archPane interface {
	SetKeys(k archview.Keys)
	SetSize(w, h int)
	SetOrigin(x, y int)
	SetDetail(d proto.Detail)
	Clear()
	// HandleKey reports whether the key was consumed.
	HandleKey(k tea.KeyMsg) bool
	// Update handles mouse messages.
	Update(msg tea.Msg) tea.Cmd
	// Capturing is true while the module's file list is open.
	Capturing() bool
	// PendingJump returns (once) the file chosen in a module's file list.
	PendingJump() (string, bool)
	HelpBindings() [][]key.Binding
	ShortBindings() []key.Binding
	View() string
}

var _ archPane = (*archview.Model)(nil)

// contextLiner is implemented by a page that writes its own dim context
// for the tab bar's right side.
type contextLiner interface{ ContextLine() string }

// archJump acts on the Architecture tab's pending file jump: switch to the
// Code tab and focus the file's first unit.
func (m *Model) archJump() tea.Cmd {
	path, ok := m.arch.PendingJump()
	if !ok {
		return nil
	}
	if m.detail == nil {
		m.note = "no item loaded"
		return nil
	}
	for _, u := range m.detail.Units {
		if u.Path == path {
			m.jumpToUnit(u.ID)
			return nil
		}
	}
	m.tab = TabCode
	m.note = "no units in " + path
	return nil
}

// archContext is the tab bar's context on the Architecture tab: the page's
// own line, else module counts from the graph.
func (m Model) archContext() string {
	if c, ok := m.arch.(contextLiner); ok {
		if s := c.ContextLine(); s != "" {
			return s
		}
	}
	if m.detail == nil || m.detail.Agg == nil || m.detail.Agg.Arch == nil {
		return ""
	}
	g := m.detail.Agg.Arch
	touched := 0
	for _, mod := range g.Modules {
		if mod.Hop == 0 {
			touched++
		}
	}
	return fmt.Sprintf("%s · %d touched", plural(len(g.Modules), "module"), touched)
}

// codeContext is the tab bar's context on the Code tab ("#482 · showing 7
// of 19 chunks" when the page says, else the chunk count).
func (m Model) codeContext() string {
	if m.detail == nil {
		return ""
	}
	if c, ok := m.code.(contextLiner); ok {
		if s := c.ContextLine(); s != "" {
			return s
		}
	}
	it := m.detail.Item
	return itemShort(it) + " · " + plural(len(m.detail.Units), "chunk")
}

// tabContext is the dim context on the right of the tab bar, after the
// "↻ updating review · <stage>" note while the item updates (M5.2).
func (m Model) tabContext() string {
	if m.detail == nil || m.tab == TabSettings {
		return ""
	}
	var ctx string
	switch m.tab {
	case TabCode:
		ctx = m.codeContext()
	case TabArch:
		ctx = m.archContext()
	case TabPipeline:
		ctx = itemShort(m.detail.Item)
	default:
		ctx = overviewContext(m.detail.Item, m.now())
	}
	note := updatingNote(*m.detail)
	switch {
	case note == "":
		return ctx
	case ctx == "":
		return note
	}
	return note + " · " + ctx
}
