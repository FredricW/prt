package tui

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// Comment-layer messages (M4): answers of the daemon calls the Code page's
// comment operations, the submit and create-PR forms and MarkViewed make.
type (
	commentDoneMsg struct {
		op  codeview.CommentOp
		err error
	}
	submittedMsg struct {
		res proto.SubmitResult
		err error
	}
	prCreatedMsg struct {
		res proto.CreatePRResult
		err error
	}
	viewedMsg struct{ err error }
)

// sendComment drains the Code page's pending comment operation and returns
// the daemon call for it, or nil when there is none. The daemon's patch
// events then reload the detail through the usual flow.
func (m *Model) sendComment() tea.Cmd {
	op, ok := m.code.PendingComment()
	if !ok || op.Kind == codeview.CommentOpNone {
		return nil
	}
	id := m.shownItemID()
	if id == "" {
		m.note = "no item selected"
		return nil
	}
	cmd, err := commentCmd(m.client, id, op)
	if err != nil {
		m.note = "comment failed: " + err.Error()
		return nil
	}
	if op.Override != nil {
		// The overridden label changes: fetch the unit's labels afresh.
		m.dropUnitLabels(id, op.Override.UnitID)
	}
	return cmd
}

// commentCmd maps a CommentOp onto its daemon.Client call for item itemID.
func commentCmd(c daemon.Client, itemID string, op codeview.CommentOp) (tea.Cmd, error) {
	ctx := context.Background()
	done := func(err error) tea.Msg { return commentDoneMsg{op: op, err: err} }
	switch op.Kind {
	case codeview.CommentOpSave, codeview.CommentOpDelete:
		p := proto.CommentParams{ItemID: itemID, Comment: op.Comment, Delete: op.Kind == codeview.CommentOpDelete}
		if p.Comment.ItemID == "" {
			p.Comment.ItemID = itemID
		}
		return func() tea.Msg {
			_, err := c.SaveComment(ctx, p)
			return done(err)
		}, nil
	case codeview.CommentOpAccept:
		return actionCmd(c, proto.CommentActionParams{ItemID: itemID, Action: proto.CommentAccept, CommentID: op.CommentID}, done), nil
	case codeview.CommentOpDismiss:
		return actionCmd(c, proto.CommentActionParams{ItemID: itemID, Action: proto.CommentDismiss,
			CommentID: op.CommentID, Override: op.Override}, done), nil
	case codeview.CommentOpReply:
		return actionCmd(c, proto.CommentActionParams{ItemID: itemID, Action: proto.CommentReply,
			ThreadID: op.ThreadID, Body: op.Body}, done), nil
	case codeview.CommentOpResolve:
		return actionCmd(c, proto.CommentActionParams{ItemID: itemID, Action: proto.CommentResolve, ThreadID: op.ThreadID}, done), nil
	case codeview.CommentOpOverride:
		if op.Override == nil {
			return nil, errors.New("override without a label")
		}
		p := proto.OverrideParams{ItemID: itemID, Override: *op.Override}
		return func() tea.Msg { return done(c.Override(ctx, p)) }, nil
	}
	return nil, fmt.Errorf("unknown comment operation %d", op.Kind)
}

// actionCmd calls CommentAction with p.
func actionCmd(c daemon.Client, p proto.CommentActionParams, done func(error) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		_, err := c.CommentAction(context.Background(), p)
		return done(err)
	}
}

// commentNote is the status note for a finished comment operation.
func commentNote(msg commentDoneMsg) string {
	if msg.err != nil {
		return "comment failed: " + msg.err.Error()
	}
	switch msg.op.Kind {
	case codeview.CommentOpSave:
		return "draft saved"
	case codeview.CommentOpDelete:
		return "draft deleted"
	case codeview.CommentOpAccept:
		return "finding accepted"
	case codeview.CommentOpDismiss:
		if msg.op.Override != nil {
			return "finding dismissed; label override filed"
		}
		return "finding dismissed"
	case codeview.CommentOpReply:
		return "reply posted"
	case codeview.CommentOpResolve:
		return "thread resolved"
	case codeview.CommentOpOverride:
		return "label override filed"
	}
	return ""
}

// onCommentMsg handles the answers of the comment-layer daemon calls; ok is
// false for any other message.
func (m Model) onCommentMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case commentDoneMsg:
		m.note = commentNote(msg)
	case submittedMsg:
		if msg.err != nil {
			m.note = "submit failed: " + msg.err.Error()
		} else {
			m.note = fmt.Sprintf("review submitted (%s)", plural(len(msg.res.Posted), "comment"))
		}
	case prCreatedMsg:
		return m.onPRCreated(msg)
	case viewedMsg:
		if msg.err != nil {
			m.note = "mark viewed: " + msg.err.Error()
		}
	default:
		return m, nil, false
	}
	return m, nil, true
}

// onPRCreated reports a created PR and moves the picker to its item as soon
// as the item list has it.
func (m Model) onPRCreated(msg prCreatedMsg) (Model, tea.Cmd, bool) {
	if msg.err != nil {
		m.note = "create PR failed: " + msg.err.Error()
		return m, nil, true
	}
	m.note = fmt.Sprintf("PR #%d created (%s attached)", msg.res.Item.Number, plural(msg.res.Attached, "comment"))
	m.selectAfter = msg.res.Item.ID
	if m.takeSelectAfter() {
		return m, m.selectionChanged(), true
	}
	return m, m.queueReload(), true
}

// takeSelectAfter moves the picker cursor to the item a create-PR asked
// for once it is listed; it reports whether the cursor moved.
func (m *Model) takeSelectAfter() bool {
	if m.selectAfter == "" {
		return false
	}
	for i, it := range m.picker.items {
		if it.ID == m.selectAfter {
			m.selectAfter = ""
			return m.picker.moveTo(i)
		}
	}
	return false
}

// markViewed records, once per (item, analysed head) per session, that the
// item's detail is shown in the focused main pane (Overview or Code tab),
// so "Since you looked" compares the next push against it (DECISIONS M4).
func (m *Model) markViewed() tea.Cmd {
	if m.detail == nil || m.showHelp || m.form != nil || m.focus == FocusPicker {
		return nil
	}
	if m.tab != TabOverview && m.tab != TabCode {
		return nil
	}
	it := m.detail.Item
	if it.AnalysedSHA == "" {
		return nil
	}
	k := it.ID + "@" + it.AnalysedSHA
	if m.viewed[k] {
		return nil
	}
	m.viewed[k] = true
	c, p := m.client, proto.ViewedParams{ItemID: it.ID, HeadSHA: it.AnalysedSHA}
	return func() tea.Msg { return viewedMsg{err: c.MarkViewed(context.Background(), p)} }
}

// stripShown reports whether the Code page's Comments strip is on screen,
// i.e. part of the tab cycle.
func (m Model) stripShown() bool { return m.tab == TabCode && m.code.HasStrip() }

// cycleFocus moves focus dir steps along picker → main pane → bottom strip
// (the strip only while it is shown).
func (m *Model) cycleFocus(dir int) {
	order := []Focus{FocusPicker, FocusMain}
	if m.stripShown() {
		order = append(order, FocusStrip)
	}
	i := 0
	for j, f := range order {
		if f == m.focus {
			i = j
		}
	}
	m.focus = order[(i+dir+len(order))%len(order)]
}

// syncFocus keeps the Code page's strip focus in step with the root's:
// focus on a strip that is no longer shown falls back to the main pane.
func (m *Model) syncFocus() {
	if m.focus == FocusStrip && !m.stripShown() {
		m.focus = FocusMain
	}
	if want := m.focus == FocusStrip; m.code.StripFocused() != want {
		m.code.SetStripFocus(want)
	}
}

// draftCount counts an item's user drafts: sendable ones and outdated ones
// (skipped by a submission).
type draftCount struct{ sendable, outdated int }

func countDrafts(cs []model.Comment) draftCount {
	var n draftCount
	for _, c := range cs {
		if c.Source != model.CommentUser || c.State != model.CommentDraft {
			continue
		}
		if c.Outdated {
			n.outdated++
		} else {
			n.sendable++
		}
	}
	return n
}

func (d draftCount) total() int { return d.sendable + d.outdated }

// draftsFor counts the drafts of item id when its detail is loaded.
func (m Model) draftsFor(id string) draftCount {
	if m.detail == nil || m.detail.Item.ID != id {
		return draftCount{}
	}
	return countDrafts(m.detail.Comments)
}

// batch is tea.Batch that returns nil for no command and the command itself
// for one, so single commands (tea.Quit) stay recognisable.
func batch(cmds ...tea.Cmd) tea.Cmd {
	var out []tea.Cmd
	for _, c := range cmds {
		if c != nil {
			out = append(out, c)
		}
	}
	switch len(out) {
	case 0:
		return nil
	case 1:
		return out[0]
	}
	return tea.Batch(out...)
}
