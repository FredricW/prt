package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/model"
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
	// Mode is the reading mode in effect; SetMode switches it and reports
	// false when by change is not available (no groups yet).
	Mode() codeview.Mode
	SetMode(md codeview.Mode) bool
	// PendingEdit returns and clears a merge/split made by the last key.
	PendingEdit() (model.GroupEdit, bool)
	// PendingReview returns and clears the group R asked to re-review on
	// the deep tier.
	PendingReview() (string, bool)
	HelpBindings() [][]key.Binding
	ShortBindings() []key.Binding
	View() string
}

// originSetter is implemented by components that map mouse coordinates
// (codeview, pipeview): the root tells them where the main pane starts.
type originSetter interface{ SetOrigin(x, y int) }

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
func (p *codeviewPane) SetOrigin(x, y int)            { p.m.SetOrigin(x, y) }
func (p *codeviewPane) SetDetail(d proto.Detail)      { p.m.SetDetail(d) }
func (p *codeviewPane) Clear()                        { p.m.Clear() }
func (p *codeviewPane) HandleKey(k tea.KeyMsg) bool   { return p.m.HandleKey(k) }
func (p *codeviewPane) Capturing() bool               { return p.m.Capturing() }
func (p *codeviewPane) FocusUnit(id string) bool      { return p.m.FocusUnit(id) }
func (p *codeviewPane) Mode() codeview.Mode           { return p.m.Mode() }
func (p *codeviewPane) SetMode(md codeview.Mode) bool { return p.m.SetMode(md) }

func (p *codeviewPane) PendingEdit() (model.GroupEdit, bool) { return p.m.PendingEdit() }
func (p *codeviewPane) PendingReview() (string, bool)        { return p.m.PendingReview() }
func (p *codeviewPane) HelpBindings() [][]key.Binding        { return p.m.HelpBindings() }
func (p *codeviewPane) ShortBindings() []key.Binding         { return p.m.ShortBindings() }
func (p *codeviewPane) View() string                         { return p.m.View() }

func (p *codeviewPane) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	p.m, cmd = p.m.Update(msg)
	return cmd
}

// groupEditedMsg answers a daemon EditGroups call.
type groupEditedMsg struct{ err error }

// sendGroupEdit returns a command that sends the Code page's pending merge
// or split to the daemon for the shown item, or nil when there is none.
// The daemon stores the edit and re-runs the item; its patch events reload
// the detail through the usual flow.
func (m Model) sendGroupEdit() tea.Cmd {
	e, ok := m.code.PendingEdit()
	if !ok {
		return nil
	}
	id := m.shownItemID()
	if id == "" {
		return nil
	}
	e.ItemID = id
	c := m.client
	return func() tea.Msg {
		return groupEditedMsg{err: c.EditGroups(context.Background(), proto.GroupEditParams{ItemID: id, Edit: e})}
	}
}

// sendReReview returns a command that asks the daemon to re-review the
// group the Code page's R picked on the deep tier, or nil when there is
// none; it sets the note "re-reviewing on deep…".
func (m *Model) sendReReview() tea.Cmd {
	gid, ok := m.code.PendingReview()
	if !ok {
		return nil
	}
	id := m.shownItemID()
	if id == "" {
		return nil
	}
	m.note = "re-reviewing on deep…"
	return sendReviewAction(m.client, proto.ReviewActionParams{ItemID: id, GroupID: gid, Action: proto.ActionDeep})
}

// onGroupEdited shows the outcome of a merge or split in the status line.
func (m Model) onGroupEdited(msg groupEditedMsg) Model {
	if msg.err != nil {
		m.note = "regroup failed: " + msg.err.Error()
	} else {
		m.note = "regrouping…"
	}
	return m
}
