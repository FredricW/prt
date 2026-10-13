package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/cmdline"
)

// helpGroups returns the bindings of the focused pane (spec: `?` lists the
// bindings for the focused pane).
func (m Model) helpGroups() (string, [][]key.Binding) {
	if m.focus == FocusPicker {
		return "picker", m.keys.pickerHelp(m.ws.Multi)
	}
	switch m.tab {
	case TabCode:
		return "code", m.keys.codeHelp(m.code.HelpBindings())
	case TabPipeline:
		return "pipeline", m.keys.pipelineHelp(m.pipe.HelpBindings())
	case TabArch:
		return "architecture", m.keys.archHelp(m.arch.HelpBindings())
	}
	return "overview", m.keys.overviewHelp()
}

// viewHelp renders the `?` overlay shown in place of the main pane: the
// focused pane's keys, the mouse resize (M5.3), then every `:` command
// (M5.1).
func (m Model) viewHelp(w int) string {
	name, groups := m.helpGroups()
	inner := max(10, w-4)
	body := m.st.title.Render("Keys · "+name) + "\n\n" + m.helpColumns(groups, inner) + "\n" +
		m.st.dim.Render(ansi.Wordwrap(m.resizeHelp(), inner, "")) + "\n\n" +
		m.st.title.Render("Commands") + "\n" + m.commandHelp(inner) + "\n\n" +
		m.st.dim.Render("? or esc closes")
	return m.st.helpBox.Render(body)
}

// resizeHelp is the help overlay's line on resizing (M5.3).
func (m Model) resizeHelp() string {
	k := m.keys
	return "mouse: drag the picker's or the Code list's right border to resize; " +
		k.Narrower.Help().Key + " " + k.Wider.Help().Key + " resize the focused pane"
}

// commandHelp lists cmdline.Help() as "usage  description" rows, the
// descriptions wrapped to width under their own column (below the usage
// when the pane is too narrow for two columns). Outside a workspace the
// workspace-only forms (`:repo`, repo-qualified `:add`) are left out, so a
// single repo's overlay reads as before M5.3.
func (m Model) commandHelp(width int) string {
	var entries []cmdline.HelpEntry
	for _, e := range cmdline.Help() {
		if !e.Workspace || m.ws.Multi {
			entries = append(entries, e)
		}
	}
	col := 0
	for _, e := range entries {
		col = max(col, ansi.StringWidth(e.Usage))
	}
	col = min(col, width/2)
	descW := width - col - 2
	var lines []string
	for _, e := range entries {
		usage := m.st.bold.Render(truncate(e.Usage, width))
		if descW < 12 || ansi.StringWidth(e.Usage) > col {
			lines = append(lines, usage)
			for _, l := range strings.Split(ansi.Wordwrap(e.Desc, max(1, width-2), ""), "\n") {
				lines = append(lines, "  "+m.st.dim.Render(l))
			}
			continue
		}
		desc := strings.Split(ansi.Wordwrap(e.Desc, descW, ""), "\n")
		lines = append(lines, padRight(usage, col)+"  "+m.st.dim.Render(desc[0]))
		for _, l := range desc[1:] {
			lines = append(lines, strings.Repeat(" ", col+2)+m.st.dim.Render(l))
		}
	}
	return strings.Join(lines, "\n")
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
