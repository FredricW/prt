package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

// helpGroups returns the bindings of the focused pane (spec: `?` lists the
// bindings for the focused pane).
func (m Model) helpGroups() (string, [][]key.Binding) {
	if m.focus == FocusPicker {
		return "picker", m.keys.pickerHelp()
	}
	if m.tab == TabCode {
		return "code", m.keys.codeHelp()
	}
	return "overview", m.keys.overviewHelp()
}

// viewHelp renders the `?` overlay shown in place of the main pane.
func (m Model) viewHelp(w int) string {
	name, groups := m.helpGroups()
	body := m.st.title.Render("Keys · "+name) + "\n\n" + m.helpColumns(groups, max(10, w-4)) + "\n\n" +
		m.st.dim.Render("? or esc closes")
	return m.st.helpBox.Render(body)
}

// helpColumns renders groups as columns, wrapping onto further rows of
// columns when they do not fit in width (bubbles/help would cut them off).
func (m Model) helpColumns(groups [][]key.Binding, width int) string {
	h := m.help
	h.Width = 0 // measure without truncation
	var rows []string
	var cur [][]key.Binding
	for _, g := range groups {
		next := append(append([][]key.Binding(nil), cur...), g)
		if len(cur) > 0 && lipgloss.Width(h.FullHelpView(next)) > width {
			rows = append(rows, h.FullHelpView(cur))
			next = [][]key.Binding{g}
		}
		cur = next
	}
	if len(cur) > 0 {
		rows = append(rows, h.FullHelpView(cur))
	}
	return strings.Join(rows, "\n\n")
}
