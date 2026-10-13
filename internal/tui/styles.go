package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// styles are built from a renderer so snapshots render without colour. No
// meaning is carried by colour alone: the urgency bar, cursor marker and
// states are all readable as plain text.
type styles struct {
	title, dim, cursor, section, emptySection, tabOn, tabOff, tabLater lipgloss.Style
	sidebar, helpBox, prompt                                           lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	return styles{
		title:        r.NewStyle().Bold(true),
		dim:          r.NewStyle().Faint(true),
		cursor:       r.NewStyle().Bold(true),
		section:      r.NewStyle().Bold(true),
		emptySection: r.NewStyle().Faint(true),
		tabOn:        r.NewStyle().Bold(true).Underline(true),
		tabOff:       r.NewStyle(),
		tabLater:     r.NewStyle().Faint(true),
		sidebar:      r.NewStyle().Border(lipgloss.NormalBorder(), false, true, false, false),
		helpBox:      r.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1),
		prompt:       r.NewStyle().Bold(true),
	}
}

// helpStyles returns bubbles/help styles bound to r (the defaults are bound
// to the global renderer and would leak colour into snapshots).
func helpStyles(r *lipgloss.Renderer) help.Styles {
	key := r.NewStyle().Bold(true)
	desc := r.NewStyle().Faint(true)
	return help.Styles{
		Ellipsis:       desc,
		ShortKey:       key,
		ShortDesc:      desc,
		ShortSeparator: desc,
		FullKey:        key,
		FullDesc:       desc,
		FullSeparator:  desc,
	}
}

// hasColour reports whether r emits colour (false for snapshots and pipes).
func hasColour(r *lipgloss.Renderer) bool { return r.ColorProfile() != termenv.Ascii }

// truncate shortens s to at most n terminal cells, ending in "…" when cut.
// It is ANSI- and wide-character aware.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…")
}

// fit makes s exactly h lines, each truncated to w cells (short blocks are
// padded with empty lines) so panes never wrap or overflow.
func fit(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		lines[i] = truncate(l, w)
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// trimRight strips trailing spaces from every line (glamour pads to the wrap
// width, which would make snapshots noisy).
func trimRight(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}
