package pipeview

import "github.com/charmbracelet/lipgloss"

// styles are built once per renderer. With a no-colour renderer every style
// renders plain text (borders are characters), so goldens are deterministic.
type styles struct {
	title, head, dim, sel, warn, good, note lipgloss.Style
	box, boxSel                             lipgloss.Style
	ai                                      lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	red, green := lipgloss.Color("1"), lipgloss.Color("2")
	faint := r.NewStyle().Faint(true)
	return styles{
		title: r.NewStyle().Bold(true), head: r.NewStyle().Bold(true).Underline(true),
		dim: faint, sel: r.NewStyle().Bold(true), note: faint.Italic(true),
		warn: r.NewStyle().Foreground(red), good: r.NewStyle().Foreground(green),
		box:    r.NewStyle().Border(lipgloss.RoundedBorder()),
		boxSel: r.NewStyle().Border(lipgloss.ThickBorder()).Bold(true),
		ai:     faint.Italic(true),
	}
}
