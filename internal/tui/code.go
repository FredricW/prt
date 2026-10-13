package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/diffview"
)

// code is the Code tab: in M0 the plain unified diff viewer.
type code struct {
	dv     diffview.Model
	height int
}

func newCode(r *lipgloss.Renderer) code {
	return code{dv: diffview.New(r), height: 20}
}

func (c *code) setSize(w, h int) {
	c.height = max(1, h)
	c.dv.SetSize(w, c.height)
}

func (c *code) setDiff(raw string) { c.dv.SetDiff(raw) }

// files returns per-file counts parsed from the diff (fallback for the
// overview when the pipeline has not stored files yet).
func (c code) files() []fileStat {
	fs := c.dv.Files()
	out := make([]fileStat, 0, len(fs))
	for _, f := range fs {
		a, d := f.Counts()
		p := f.Path()
		if f.Renamed && f.OldPath != "" && f.NewPath != "" {
			p = f.OldPath + " → " + f.NewPath
		}
		out = append(out, fileStat{path: p, adds: a, dels: d})
	}
	return out
}

// handleKey maps the (rebindable) key map onto the viewer's API. Mouse and
// other messages go to the viewer's own Update.
func (c *code) handleKey(km KeyMap, msg tea.KeyMsg) {
	switch {
	case key.Matches(msg, km.Down):
		c.dv.ScrollBy(1)
	case key.Matches(msg, km.Up):
		c.dv.ScrollBy(-1)
	case key.Matches(msg, km.PageDown):
		c.dv.ScrollBy(max(1, c.height))
	case key.Matches(msg, km.PageUp):
		c.dv.ScrollBy(-max(1, c.height))
	case key.Matches(msg, km.HalfPageDown):
		c.dv.ScrollBy(max(1, c.height/2))
	case key.Matches(msg, km.HalfPageUp):
		c.dv.ScrollBy(-max(1, c.height/2))
	case key.Matches(msg, km.Top):
		c.dv.GotoTop()
	case key.Matches(msg, km.Bottom):
		c.dv.GotoBottom()
	case key.Matches(msg, km.NextFile):
		c.dv.NextFile()
	case key.Matches(msg, km.PrevFile):
		c.dv.PrevFile()
	}
}

// update forwards non-key messages (mouse wheel) to the viewer.
func (c *code) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	c.dv, cmd = c.dv.Update(msg)
	return cmd
}

func (c code) view() string { return c.dv.View() }
