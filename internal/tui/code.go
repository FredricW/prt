package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

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
	// PendingComment returns and clears the comments-layer write made by the
	// last key (draft saved, ai-draft accepted, reply, …).
	PendingComment() (codeview.CommentOp, bool)
	// SaveOpenComposer queues an open composer's non-blank text as a draft
	// save (handed out by PendingComment); used on quit.
	SaveOpenComposer() bool
	// HasStrip reports whether the Comments strip is shown, so the root's
	// tab cycle includes it; SetStripFocus and StripFocused move and report
	// keyboard focus on it.
	HasStrip() bool
	SetStripFocus(on bool)
	StripFocused() bool
	HelpBindings() [][]key.Binding
	ShortBindings() []key.Binding
	View() string

	// M5. PendingOpen returns and clears the e/o request of the last key;
	// DiffLayout and SetDiffLayout read and request unified / side-by-side;
	// CurrentUnit is the unit under the cursor (its group is the focused
	// group by change, for :tier); Note is the page's one-shot note (a
	// degraded side-by-side says so).
	PendingOpen() (codeview.OpenRequest, bool)
	DiffLayout() codeview.Layout
	SetDiffLayout(l codeview.Layout) bool
	CurrentUnit() (model.ChangeUnit, bool)
	Note() string
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
func (p *codeviewPane) HasStrip() bool                       { return p.m.HasStrip() }
func (p *codeviewPane) SetStripFocus(on bool)                { p.m.SetStripFocus(on) }
func (p *codeviewPane) StripFocused() bool                   { return p.m.StripFocused() }

func (p *codeviewPane) JumpToComment(id string) bool               { return p.m.JumpToComment(id) }
func (p *codeviewPane) PendingComment() (codeview.CommentOp, bool) { return p.m.PendingComment() }
func (p *codeviewPane) SaveOpenComposer() bool                     { return p.m.SaveOpenComposer() }
func (p *codeviewPane) HelpBindings() [][]key.Binding              { return p.m.HelpBindings() }
func (p *codeviewPane) ShortBindings() []key.Binding               { return p.m.ShortBindings() }
func (p *codeviewPane) View() string                               { return p.m.View() }

func (p *codeviewPane) PendingOpen() (codeview.OpenRequest, bool) { return p.m.PendingOpen() }
func (p *codeviewPane) DiffLayout() codeview.Layout               { return p.m.DiffLayout() }
func (p *codeviewPane) SetDiffLayout(l codeview.Layout) bool      { return p.m.SetDiffLayout(l) }
func (p *codeviewPane) CurrentUnit() (model.ChangeUnit, bool)     { return p.m.CurrentUnit() }
func (p *codeviewPane) Note() string                              { return p.m.Note() }

// ContextLine forwards the page's tab-bar context ("#482 · showing 7 of 19
// chunks") when codeview provides one; "" otherwise.
func (p *codeviewPane) ContextLine() string {
	if c, ok := any(&p.m).(contextLiner); ok {
		return c.ContextLine()
	}
	return ""
}

func (p *codeviewPane) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	p.m, cmd = p.m.Update(msg)
	return cmd
}

// Code page open messages (M5): the browser opened for `o`, the editor
// exited for `e`.
type (
	unitOpenedMsg struct {
		where string
		err   error
	}
	fileEditedMsg struct{ err error }
)

// EditFileFunc opens path at line in an editor; the default runs $EDITOR
// (fallback vi) as `$EDITOR +<line> <path>` through tea.ExecProcess.
type EditFileFunc func(path string, line int) tea.Cmd

// execEditFile is the default EditFileFunc.
func execEditFile(path string, line int) tea.Cmd {
	args := strings.Fields(os.Getenv("EDITOR"))
	if len(args) == 0 {
		args = []string{"vi"}
	}
	if line > 0 {
		args = append(args, "+"+strconv.Itoa(line))
	}
	c := exec.CommandContext(context.Background(), args[0], append(args[1:], path)...)
	return tea.ExecProcess(c, func(err error) tea.Msg { return fileEditedMsg{err: err} })
}

// codeOpenCmd acts on the Code page's pending e/o request: o opens the
// item's files on GitHub (the item URL when it is not a PR), e opens the
// file at the line in $EDITOR (Options.EditFile), under Options.Root.
func (m *Model) codeOpenCmd() tea.Cmd {
	req, ok := m.code.PendingOpen()
	if !ok {
		return nil
	}
	where := req.Path
	if req.Line > 0 {
		where = fmt.Sprintf("%s:%d", req.Path, req.Line)
	}
	if req.Browser {
		return m.openOnGitHub(where)
	}
	path := req.Path
	if m.opts.Root != "" && !filepath.IsAbs(path) {
		path = filepath.Join(m.opts.Root, path)
	}
	edit := m.opts.EditFile
	if edit == nil {
		edit = execEditFile
	}
	m.note = "opening " + where + " in $EDITOR…"
	return edit(path, req.Line)
}

// openOnGitHub opens the shown item's files page for o on the Code page.
func (m *Model) openOnGitHub(where string) tea.Cmd {
	it, ok := m.shownItem()
	switch {
	case !ok:
		m.note = "no item selected"
		return nil
	case it.URL == "":
		m.note = "no GitHub URL for " + itemLabel(it)
		return nil
	case m.opts.Open == nil:
		m.note = "no opener configured"
		return nil
	}
	url := it.URL
	if it.Kind == model.KindPR {
		url = strings.TrimRight(url, "/") + "/files"
	}
	open := m.opts.Open
	return func() tea.Msg { return unitOpenedMsg{where: where, err: open(url)} }
}

// shownItem is the item the main pane shows (loaded detail, else the
// picker selection).
func (m Model) shownItem() (model.Item, bool) {
	if m.detail != nil {
		return m.detail.Item, true
	}
	return m.picker.selected()
}

// layoutNote says which diff layout `u` left in effect; a side-by-side
// request below codeview.SplitMinWidth columns renders unified and says so
// (the page's own note wins when it has one).
func (m *Model) layoutNote() {
	if n := m.code.Note(); n != "" {
		m.note = n
		return
	}
	l := m.code.DiffLayout()
	if _, mw, _ := m.dims(); l == codeview.LayoutSplit && mw < codeview.SplitMinWidth {
		m.note = fmt.Sprintf("side-by-side needs a %d-column terminal (pane has %d) — showing unified", codeview.SplitMinTerminal, mw)
		return
	}
	m.note = "diff: " + l.String()
}

// focusedGroup is the group :tier acts on: the Code page's group by change
// (the cursor unit's), or the Pipeline page's selected row.
func (m Model) focusedGroup() (string, bool) {
	switch {
	case m.tab == TabPipeline:
		return m.pipe.SelectedGroup()
	case m.tab == TabCode && m.code.Mode() == codeview.ModeChange && m.detail != nil:
		u, ok := m.code.CurrentUnit()
		if !ok {
			return "", false
		}
		for _, g := range m.detail.Groups {
			for _, id := range g.UnitIDs {
				if id == u.ID {
					return g.ID, true
				}
			}
		}
	}
	return "", false
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
