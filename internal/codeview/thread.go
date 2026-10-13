package codeview

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/diffview"
	"github.com/fredricw/prt/internal/model"
)

const (
	// panelFullBelow is the width under which the thread panel covers the
	// whole body instead of its right 40%.
	panelFullBelow = 100
	// panelMinW is the narrowest side panel.
	panelMinW = 30
	// replyHeight is the reply textarea's height in rows.
	replyHeight = 3
)

// threadPanel is the side thread panel (t): every comment of one thread
// and a reply textarea. off scrolls the comments.
type threadPanel struct {
	block string
	ta    textarea.Model
	off   int
}

// panelGeom returns the panel's width and its content width (the side
// panel draws a "│ " border; the full-width one does not).
func (m *Model) panelGeom() (pw, cw int) {
	if m.w < panelFullBelow {
		return m.w, m.w
	}
	pw = min(m.w, max(panelMinW, m.w*2/5))
	return pw, max(1, pw-2)
}

// openPanel opens the thread panel on block b.
func (m *Model) openPanel(b *cblock) {
	_, cw := m.panelGeom()
	m.vis, m.visRow = nil, -1
	m.panel = &threadPanel{block: b.id, ta: m.newTextarea(cw, replyHeight, "")}
}

// panelBlock returns the panel's thread, nil when it is gone.
func (m *Model) panelBlock() *cblock {
	if bi := m.c.blockIndex(m.panel.block); bi >= 0 {
		return &m.c.blocks[bi]
	}
	return nil
}

// panelKey handles a key while the panel is open: SaveDraft replies,
// Resolve resolves, Close closes, page keys scroll the comments; ctrl+c
// falls through; the rest goes to the reply textarea.
func (m *Model) panelKey(k tea.KeyMsg) bool {
	kk := m.keys
	b := m.panelBlock()
	switch {
	case key.Matches(k, kk.Close) || b == nil:
		m.panel = nil
	case key.Matches(k, kk.SaveDraft):
		m.reply(b)
	case key.Matches(k, kk.Resolve):
		m.resolve(b)
	case k.String() == "pgdown": // not the PageDown binding: space must type
		_, cw := m.panelGeom()
		m.panel.off = min(m.panel.off+max(1, m.layout().bodyH/2), max(0, len(m.threadText(b, cw))-1))
	case k.String() == "pgup":
		m.panel.off = max(0, m.panel.off-max(1, m.layout().bodyH/2))
	case k.String() == "ctrl+c":
		return false
	default:
		m.panel.ta, _ = m.panel.ta.Update(k)
	}
	return true
}

// reply queues the textarea as a reply to b (a note for a blank body).
func (m *Model) reply(b *cblock) {
	body := tidyBody(m.panel.ta.Value())
	switch {
	case body == "":
		m.note = "reply: empty reply not sent"
	case b.c.ThreadID == "":
		m.note = "reply: this comment has no GitHub thread id"
	default:
		m.pendingComment = &CommentOp{Kind: CommentOpReply, ThreadID: b.c.ThreadID, Body: body}
		m.panel.ta.Reset()
		m.note = "reply sent"
	}
}

// resolve queues resolving thread b.
func (m *Model) resolve(b *cblock) {
	switch {
	case b.resolved():
		m.note = "resolve: thread already resolved"
	case b.c.ThreadID == "":
		m.note = "resolve: this comment has no GitHub thread id"
	default:
		m.pendingComment = &CommentOp{Kind: CommentOpResolve, ThreadID: b.c.ThreadID}
		m.note = "resolve requested"
	}
}

// panelOverlay draws the panel over the body lines: over their right part
// (about 40%), or over all of them below panelFullBelow columns.
func (m Model) panelOverlay(body []string) []string {
	pw, cw := m.panelGeom()
	left := m.w - pw
	lines := m.panelLines(cw, len(body))
	for i := range body {
		s := ""
		if left > 0 {
			s = pad(ansi.Truncate(body[i], left, ""), left) + m.st.cDim.Render("│ ")
		}
		body[i] = s + lines[i]
	}
	return body
}

// panelLines renders the panel at content width cw, exactly h lines: the
// title, the thread's comments (scrolled), then the reply hint and the
// textarea at the bottom.
func (m Model) panelLines(cw, h int) []string {
	out := make([]string, h)
	b := m.panelBlock()
	if h == 0 || b == nil {
		return out
	}
	title := " thread · " + diffview.Clean(b.c.Path) + ":" + commentLines(b.c) + " · " + threadState(b)
	out[0] = m.st.panelTitle.Render(pad(fit(title, cw), cw))
	bottom := []string{m.st.cDim.Render(fit("reply · "+m.keys.SaveDraft.Help().Key+" send · "+
		m.keys.Resolve.Help().Key+" resolve · "+m.keys.Close.Help().Key+" close", cw))}
	bottom = append(bottom, strings.Split(m.panel.ta.View(), "\n")...)
	midH := max(0, h-1-len(bottom))
	content := m.threadText(b, cw)
	off := min(m.panel.off, max(0, len(content)-midH))
	for i := 0; i < midH && off+i < len(content); i++ {
		out[1+i] = content[off+i]
	}
	for i, l := range bottom {
		if r := 1 + midH + i; r < h {
			out[r] = l
		}
	}
	for j := range out {
		out[j] = pad(fit(out[j], cw), cw)
	}
	return out
}

// threadState is "open" or "resolved", with the comment count.
func threadState(b *cblock) string {
	s := "open"
	if b.resolved() {
		s = "resolved"
	}
	return s + " · " + plural(1+len(b.replies), "comment")
}

// threadText renders every comment of thread b: "author · date" then the
// Glamour body, a blank line between comments.
func (m Model) threadText(b *cblock, cw int) []string {
	var out []string
	for i, c := range append([]model.Comment{b.c}, b.replies...) {
		if i > 0 {
			out = append(out, "")
		}
		head := authorWord(c.Author)
		if !c.CreatedAt.IsZero() {
			head += " · " + c.CreatedAt.UTC().Format("2006-01-02 15:04")
		}
		out = append(out, m.st.cHead.Render(fit(diffview.Clean(head), cw)))
		for _, l := range m.md.lines(c.Body, cw-2) {
			out = append(out, "  "+l)
		}
	}
	return out
}
