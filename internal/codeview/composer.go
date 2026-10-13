package codeview

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/diffview"
	"github.com/fredricw/prt/internal/model"
)

const (
	// composerHeight is the composer's textarea height in rows.
	composerHeight  = 4
	notePlaceCursor = "put the cursor on a diff line (j/k)"
)

// composer is the inline comment composer (C): a textarea drawn under the
// row of key (the cursor line, or the end of a V selection). base is the
// draft being written: ID set when an existing draft is edited.
type composer struct {
	ta    textarea.Model
	base  model.Comment
	key   curKey
	row   int // pane row of key (the end of its block); -1 when not drawn
	title string
}

// newTextarea builds a focused textarea of width cells and height rows
// holding value, styled with the component's renderer and a static cursor
// so renders are deterministic.
func (m *Model) newTextarea(width, height int, value string) textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = "┃ "
	ta.Placeholder = "markdown"
	ta.CharLimit = 0
	st := textarea.Style{
		Base: m.r.NewStyle(), CursorLine: m.r.NewStyle(), Text: m.r.NewStyle(),
		Placeholder: m.st.cDim, Prompt: m.st.cDim, EndOfBuffer: m.st.cDim,
		LineNumber: m.st.cDim, CursorLineNumber: m.st.cDim,
	}
	ta.FocusedStyle, ta.BlurredStyle = st, st
	ta.Cursor.Style = m.r.NewStyle().Reverse(true)
	ta.Cursor.TextStyle = m.r.NewStyle()
	_ = ta.Cursor.SetMode(cursor.CursorStatic)
	ta.SetWidth(max(4, width))
	ta.SetHeight(height)
	ta.SetValue(value)
	_ = ta.Focus()
	return ta
}

// commentKey handles the comment keys outside the composer, panel and
// picker: C, V, d, ! and t (a is acceptKey). It reports whether k was one.
func (m *Model) commentKey(k tea.KeyMsg) bool {
	kk := m.keys
	switch {
	case key.Matches(k, kk.Comment):
		m.openComposer()
	case key.Matches(k, kk.Visual):
		m.startVisual()
	case key.Matches(k, kk.Dismiss):
		m.dismissKey()
	case key.Matches(k, kk.OverrideLabel):
		m.overrideKey()
	case key.Matches(k, kk.Thread):
		m.threadKey()
	default:
		return false
	}
	return true
}

// openComposer implements C on the cursor row: on one of the user's drafts
// (its block, or a line carrying a single-line draft) it edits that draft,
// otherwise it starts a new comment on the line.
func (m *Model) openComposer() {
	if !m.cursorVisible() {
		m.note = "comment: " + notePlaceCursor
		return
	}
	if m.cur.block != "" {
		if bi := m.c.blockIndex(m.cur.block); bi >= 0 && m.c.blocks[bi].editable() {
			m.startComposer(m.c.blocks[bi].c, m.cur)
			return
		}
	}
	pos, ok := m.posOf(m.cur)
	if !ok {
		m.note = "comment: " + notePlaceCursor
		return
	}
	base := model.Comment{ItemID: m.c.itemID, UnitID: pos.UnitID, Path: pos.Path, Line: pos.Line, Side: pos.Side}
	if d := m.draftAt(base); d != nil {
		base = *d
	}
	m.startComposer(base, m.cur)
}

// draftAt returns the user's editable draft on exactly want's lines and
// sides (single line: no StartLine, or StartLine == Line), or nil.
func (m *Model) draftAt(want model.Comment) *model.Comment {
	single := func(c model.Comment) bool { return c.StartLine == 0 || c.StartLine == c.Line }
	for i := range m.c.blocks {
		b := &m.c.blocks[i]
		c := b.c
		if !b.editable() || c.Path != want.Path || c.Line != want.Line || sideOf(c.Side) != sideOf(want.Side) {
			continue
		}
		if single(want) && single(c) {
			return &b.c
		}
		if !single(want) && c.StartLine == want.StartLine && sideOf(startSide(c)) == sideOf(startSide(want)) {
			return &b.c
		}
	}
	return nil
}

// sideOf normalises a side ("" is new).
func sideOf(s model.Side) model.Side {
	if s == model.SideOld {
		return model.SideOld
	}
	return model.SideNew
}

// startSide is the side of a comment's StartLine ("" = Side).
func startSide(c model.Comment) model.Side {
	if c.StartSide == "" {
		return c.Side
	}
	return c.StartSide
}

// startComposer opens the composer for base under the row of k.
func (m *Model) startComposer(base model.Comment, k curKey) {
	verb := "new comment"
	if base.ID != "" {
		verb = "edit draft"
	}
	title := glyphDraft + " " + verb + " · " + diffview.Clean(base.Path) + ":" + commentLines(base) +
		" · " + m.keys.SaveDraft.Help().Key + " save · " + m.keys.Close.Help().Key + " discard"
	m.vis, m.visRow = nil, -1
	m.comp = &composer{ta: m.newTextarea(m.layout().paneW-blockIndent, composerHeight, base.Body), base: base, key: k, title: title}
	m.comp.row = m.anchorRow(k)
	m.showComposer()
}

// anchorRow returns the row a composer keyed by k is drawn under: the line
// row, or the last row of a comment block; -1 when k is not drawn.
func (m *Model) anchorRow(k curKey) int {
	r := m.rowOfKey(k)
	if r >= 0 && m.p.rows[r].kind == rowSplit {
		for r+1 < len(m.p.rows) && m.p.rows[r+1].kind == rowSplit && m.p.rows[r+1].pair > 0 {
			r++ // under the pair's last wrapped row
		}
		return r
	}
	if r < 0 || k.block == "" {
		return r
	}
	for r+1 < len(m.p.rows) && m.p.rows[r+1].kind == rowComment && !m.p.clines[m.p.rows[r+1].line].head {
		r++
	}
	return r
}

// showComposer scrolls so the anchor row and the composer below it are in
// view (overscrolling past the bottom when needed).
func (m *Model) showComposer() {
	if m.comp == nil || m.comp.row < 0 {
		return
	}
	viewH := max(1, m.layout().viewH)
	if need := m.comp.row + 1 + composerHeight + 1 - viewH; m.off < need {
		m.off = min(m.comp.row, need)
	}
	if m.comp.row < m.off {
		m.off = m.comp.row
	}
}

// composerKey handles a key while the composer is open: SaveDraft saves,
// Close discards, ctrl+c falls through, everything else edits.
func (m *Model) composerKey(k tea.KeyMsg) bool {
	switch {
	case key.Matches(k, m.keys.SaveDraft):
		m.saveComposer()
	case key.Matches(k, m.keys.Close):
		m.comp = nil
		m.note = "comment discarded"
	case k.String() == "ctrl+c":
		return false
	default:
		m.comp.ta, _ = m.comp.ta.Update(k)
	}
	return true
}

// saveComposer closes the composer and queues its write: a non-blank body
// saves the draft; a blank body deletes an existing draft and is a no-op
// (with a note) for a new one.
func (m *Model) saveComposer() {
	body := tidyBody(m.comp.ta.Value())
	base := m.comp.base
	m.comp = nil
	switch {
	case body == "" && base.ID == "":
		m.note = "empty comment not saved"
	case body == "":
		m.pendingComment = &CommentOp{Kind: CommentOpDelete, Comment: base}
		m.note = "draft deleted (empty body)"
	default:
		base.Body = body
		m.pendingComment = &CommentOp{Kind: CommentOpSave, Comment: base}
		m.note = "draft saved"
	}
}

// tidyBody drops leading blank lines and trailing whitespace; "" for a
// blank body.
func tidyBody(s string) string {
	s = strings.TrimRight(s, " \t\r\n")
	for strings.HasPrefix(s, "\n") || strings.HasPrefix(s, "\r\n") {
		s = strings.TrimPrefix(strings.TrimPrefix(s, "\r"), "\n")
	}
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return s
}

// composerLines renders the open composer: its title, then the textarea.
func (m Model) composerLines(width int) []string {
	out := []string{m.st.cHead.Render(fit("  "+m.comp.title, width))}
	for _, l := range strings.Split(m.comp.ta.View(), "\n") {
		out = append(out, fit("  "+l, width)) // under the two-cell comment prefix
	}
	return out
}

// startVisual implements V: a line selection anchored at the cursor line.
func (m *Model) startVisual() {
	if !m.cursorVisible() || m.cur.block != "" {
		m.note = "select: " + notePlaceCursor
		return
	}
	k := m.cur
	m.vis, m.visRow = &k, m.curRow
	m.note = "select: j/k extend · C comment · esc cancels"
}

// visualKey handles a key during a V selection: j/k extend it (within the
// anchor's unit), C comments on it, Close or V cancels; any other key ends
// the selection and is handled as usual (it reports false then).
func (m *Model) visualKey(k tea.KeyMsg) bool {
	kk := m.keys
	switch {
	case key.Matches(k, kk.Down):
		m.visualMove(1)
	case key.Matches(k, kk.Up):
		m.visualMove(-1)
	case key.Matches(k, kk.Comment):
		m.openRangeComposer()
	case key.Matches(k, kk.Close), key.Matches(k, kk.Visual):
		m.vis, m.visRow = nil, -1
		m.note = "selection cancelled"
	default:
		m.vis, m.visRow = nil, -1
		return false
	}
	return true
}

// visualMove moves the selection's free end to the next (d > 0) or
// previous diff line of the anchor's unit.
func (m *Model) visualMove(d int) {
	lo, hi := m.p.unitSpan(m.vis.unit)
	for i := m.curRow + d; i >= lo && i <= hi && i >= 0; i += d {
		if m.p.isLineRow(i) {
			m.setCursorRow(i)
			m.ensureRowVisible(i)
			return
		}
	}
	m.note = "select: a selection stays within one unit"
}

// inSelection reports whether row i lies between the selection's anchor
// and the cursor.
func (m Model) inSelection(i int) bool {
	if m.visRow < 0 || m.curRow < 0 {
		return false
	}
	return i >= min(m.visRow, m.curRow) && i <= max(m.visRow, m.curRow)
}

// openRangeComposer implements C during a V selection: the comment spans
// the top row (StartLine/StartSide) to the bottom row (Line/Side); a
// one-line selection is a single-line comment.
func (m *Model) openRangeComposer() {
	a, b := min(m.visRow, m.curRow), max(m.visRow, m.curRow)
	m.vis, m.visRow = nil, -1
	if a < 0 {
		m.note = "comment: " + notePlaceCursor
		return
	}
	end, ok1 := m.posOf(m.keyOfRow(b))
	start, ok2 := m.posOf(m.keyOfRow(a))
	if !ok1 || !ok2 {
		m.note = "comment: " + notePlaceCursor
		return
	}
	base := model.Comment{ItemID: m.c.itemID, UnitID: end.UnitID, Path: end.Path, Line: end.Line, Side: end.Side}
	if a != b {
		base.StartLine, base.StartSide = start.Line, start.Side
	}
	if d := m.draftAt(base); d != nil {
		base = *d
	}
	m.setCursorRow(b)
	m.startComposer(base, m.cur)
}
