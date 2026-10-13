package tui

import (
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/fredricw/prt/internal/theme"
)

// styles are built from a renderer so snapshots render without colour. No
// meaning is carried by colour alone: the urgency bar, cursor marker and
// states are all readable as plain text. Every colour comes from the shared
// palette (internal/theme, DECISIONS "Design canvas adopted (M5)").
type styles struct {
	th                                theme.Theme
	title, dim, cursor, section       lipgloss.Style
	helpBox, prompt, plain            lipgloss.Style
	add, del, accent, blue, bold, sel lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	th := theme.New(r)
	return styles{
		th:      th,
		title:   th.Title,
		dim:     th.Dim,
		cursor:  th.Selected,
		section: th.Bold,
		helpBox: r.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(th.P.Border).Padding(0, 1),
		prompt:  th.Accent.Bold(true),
		plain:   r.NewStyle(),
		add:     th.Add,
		del:     th.Del,
		accent:  th.Accent,
		blue:    th.Blue,
		bold:    th.Bold,
		sel:     th.Selected,
	}
}

// layoutStyles are colour-free styles for measuring layouts (line counts
// and positions do not depend on colour).
func layoutStyles() styles { return newStyles(lipgloss.NewRenderer(io.Discard)) }

// helpStyles returns bubbles/help styles bound to r (the defaults are bound
// to the global renderer and would leak colour into snapshots).
func helpStyles(r *lipgloss.Renderer) help.Styles {
	th := theme.New(r)
	key := th.Bright
	desc := th.Dim
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

// seg is one styled run of a line (picker rows): plain text and the style
// it is drawn in.
type seg struct {
	text  string
	style lipgloss.Style
}

// renderSegs draws segs cut to w cells (an ellipsis marks the cut). With
// bg set (the selected row) every run also gets the selection background
// and the line is padded to w, so the highlight spans the row.
func renderSegs(segs []seg, w int, bg lipgloss.TerminalColor) string {
	var b strings.Builder
	used := 0
	for _, s := range segs {
		st := s.style
		if bg != nil {
			st = st.Background(bg)
		}
		tw := ansi.StringWidth(s.text)
		if used+tw > w {
			b.WriteString(st.Render(ansi.Truncate(s.text, max(0, w-used), "…")))
			used = w
			break
		}
		b.WriteString(st.Render(s.text))
		used += tw
	}
	if bg != nil && used < w && len(segs) > 0 {
		b.WriteString(segs[0].style.UnsetForeground().Background(bg).Render(strings.Repeat(" ", w-used)))
	}
	return b.String()
}

// segText is the plain text of segs (tests and width checks).
func segText(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.text)
	}
	return b.String()
}
