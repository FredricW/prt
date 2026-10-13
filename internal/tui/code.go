package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/proto"
)

// codePane is what the root model needs from the Code tab. The real pane
// wraps codeview.Model; tests substitute a recording fake, so key routing
// is checked on tui state without depending on codeview's rendering.
type codePane interface {
	SetKeys(k codeview.Keys)
	SetSize(w, h int)
	SetDetail(d proto.Detail)
	Clear()
	// HandleKey reports whether the key was consumed.
	HandleKey(k tea.KeyMsg) bool
	// Update handles non-key messages (mouse).
	Update(msg tea.Msg) tea.Cmd
	// Capturing is true while the filters bar or raw-labels overlay is open:
	// every key except ctrl+c then goes to the pane.
	Capturing() bool
	FocusUnit(unitID string) bool
	HelpBindings() [][]key.Binding
	ShortBindings() []key.Binding
	View() string
}

// codeviewPane adapts codeview.Model (value-returning Update) to codePane.
// The root model holds it by pointer, so copies of the root model share one
// Code page; Bubble Tea only ever uses the latest copy.
type codeviewPane struct{ m codeview.Model }

var _ codePane = (*codeviewPane)(nil)

func newCodePane(r *lipgloss.Renderer) *codeviewPane {
	return &codeviewPane{m: codeview.New(r)}
}

func (p *codeviewPane) SetKeys(k codeview.Keys)       { p.m.SetKeys(k) }
func (p *codeviewPane) SetSize(w, h int)              { p.m.SetSize(w, h) }
func (p *codeviewPane) SetDetail(d proto.Detail)      { p.m.SetDetail(d) }
func (p *codeviewPane) Clear()                        { p.m.Clear() }
func (p *codeviewPane) HandleKey(k tea.KeyMsg) bool   { return p.m.HandleKey(k) }
func (p *codeviewPane) Capturing() bool               { return p.m.Capturing() }
func (p *codeviewPane) FocusUnit(id string) bool      { return p.m.FocusUnit(id) }
func (p *codeviewPane) HelpBindings() [][]key.Binding { return p.m.HelpBindings() }
func (p *codeviewPane) ShortBindings() []key.Binding  { return p.m.ShortBindings() }
func (p *codeviewPane) View() string                  { return p.m.View() }

func (p *codeviewPane) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	p.m, cmd = p.m.Update(msg)
	return cmd
}
