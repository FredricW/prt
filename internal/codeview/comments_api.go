package codeview

import "github.com/fredricw/prt/internal/model"

// Comments layer contract (M4; DECISIONS "M4 shared contracts"). The Code
// page renders Detail.Comments (drafts, ai-drafts with a dim border, GitHub
// threads with ❱ in the gutter when unresolved), the inline composer, the
// side thread panel and the Comments strip; it never talks to the daemon.
// Every write is handed to the root model through PendingComment, exactly
// like PendingEdit/PendingReview. The signatures below are fixed (the tui
// package is written against them).
//
// Keys for the root model: `a` is consumed only when an ai-draft is under
// the cursor (else HandleKey returns false, so the root's approve runs);
// `d` without an ai-draft shows a note and is consumed; `!` without an
// ai-draft opens the label picker for the cursor's unit and ends in
// CommentOpOverride. The composer, the thread panel and the label picker
// capture every key (Capturing) except ctrl+c. While the strip is focused
// j/k/home/G/enter belong to it; StripJump returns focus to the pane, so
// the root should re-read StripFocused after each key.

// CommentOpKind says what a CommentOp asks the daemon to do.
type CommentOpKind int

// Comment operations.
const (
	CommentOpNone CommentOpKind = iota
	// CommentOpSave upserts the draft Comment (ID "" creates one; Path,
	// Line, Side, StartLine/StartSide, UnitID and Body set) →
	// proto.CommentParams.
	CommentOpSave
	// CommentOpDelete deletes the draft Comment.ID → proto.CommentParams
	// with Delete.
	CommentOpDelete
	// CommentOpAccept accepts the ai-draft CommentID → proto.CommentAccept.
	CommentOpAccept
	// CommentOpDismiss dismisses the ai-draft CommentID; with Override set
	// (`!`) it also files that label override → proto.CommentDismiss.
	CommentOpDismiss
	// CommentOpReply posts Body to thread ThreadID → proto.CommentReply.
	CommentOpReply
	// CommentOpResolve resolves thread ThreadID → proto.CommentResolve.
	CommentOpResolve
	// CommentOpOverride files the label Override alone (`!` on a unit with
	// no ai-draft under the cursor: "mark a label wrong") →
	// daemon.Client.Override.
	CommentOpOverride
)

// CommentOp is one comments-layer write for the root model to send.
type CommentOp struct {
	Kind      CommentOpKind
	Comment   model.Comment
	CommentID string
	ThreadID  string
	Body      string
	Override  *model.Override
}

// PendingComment returns and clears the comment operation made by the last
// key (composer saved, ai-draft accepted, reply sent, ...).
func (m *Model) PendingComment() (CommentOp, bool) {
	if m.pendingComment == nil {
		return CommentOp{}, false
	}
	op := *m.pendingComment
	m.pendingComment = nil
	return op, true
}

// SaveOpenComposer queues the open inline composer's text as a draft save
// (CommentOpSave, read with PendingComment) and closes it, so quitting
// keeps it (spec: quit saves drafts). It reports whether a save was queued:
// false when no composer is open or its body is blank. An unsent thread
// panel reply is not a draft and is not posted on quit.
func (m *Model) SaveOpenComposer() bool {
	if m.comp == nil || tidyBody(m.comp.ta.Value()) == "" {
		return false
	}
	m.saveComposer()
	return m.pendingComment != nil
}

// HasStrip reports whether the Comments strip is shown (the item has at
// least one comment of any kind), so the root's tab cycle includes it.
// The plain diff fallback (no units yet) has no comments layer.
func (m Model) HasStrip() bool { return !m.c.plain() && len(m.c.blocks) > 0 }

// SetStripFocus gives the Comments strip keyboard focus (tab cycle:
// picker → main pane → bottom strip). While focused, j/k move in the strip
// and StripJump jumps to the selected comment. It is ignored (focus off)
// when there is no strip.
func (m *Model) SetStripFocus(on bool) {
	m.stripFocus = on && m.HasStrip()
	m.clampStrip()
	m.clamp() // the strip's height changed the pane's
	m.keepCursorVisible()
}

// StripFocused reports whether the Comments strip has focus.
func (m Model) StripFocused() bool { return m.stripFocus }

// JumpToComment selects the comment block whose comment (a thread's first
// comment) has id and jumps to it exactly as StripJump does. It reports
// whether such a block exists; the snapshot uses it to show comments.
func (m *Model) JumpToComment(id string) bool {
	for i := range m.c.blocks {
		if m.c.blocks[i].c.ID == id {
			m.stripSel = i
			m.clampStrip()
			m.stripJump()
			return true
		}
	}
	return false
}
