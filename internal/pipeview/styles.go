package pipeview

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/theme"
)

// styles are built once per renderer from the shared theme. With a
// no-colour renderer every style renders plain text (borders are
// characters), so goldens are deterministic.
type styles struct {
	th                                      theme.Theme
	title, head, dim, sel, warn, good, note lipgloss.Style
	ai, blue, accent                        lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	th := theme.New(r)
	return styles{
		th: th, title: th.Title, head: th.Title, dim: th.Dim, sel: th.Bold,
		warn: th.Del, good: th.Add, note: th.Dim.Italic(true), ai: th.Dim.Italic(true),
		blue: th.Blue, accent: th.Accent,
	}
}
