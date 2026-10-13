package codeview

import (
	"strconv"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/comments"
	"github.com/fredricw/prt/internal/diffview"
	"github.com/fredricw/prt/internal/model"
)

// stripRows is the most comment rows the focused strip shows at once.
const stripRows = 6

// stripWant is the number of rows the Comments strip asks for: none
// without comments, the summary line, plus up to stripRows rows when
// focused.
func (m *Model) stripWant() int {
	switch {
	case !m.HasStrip():
		return 0
	case !m.stripFocus:
		return 1
	}
	return 1 + min(stripRows, len(m.c.blocks))
}

// stripSummary is "Comments N · d drafts · p pending · q posted · a ai ·
// t threads (o open)": one entry per user comment, ai-draft and thread.
func (m *Model) stripSummary() string {
	var drafts, pending, posted, ai, threads, open int
	for i := range m.c.blocks {
		b := &m.c.blocks[i]
		switch {
		case b.kind == blockAI:
			ai++
		case b.kind == blockThread:
			threads++
			if !b.resolved() {
				open++
			}
		case b.c.State == model.CommentPending:
			pending++
		case b.c.State == model.CommentPosted:
			posted++
		default:
			drafts++
		}
	}
	return "Comments " + strconv.Itoa(len(m.c.blocks)) + " · " + plural(drafts, "draft") + " · " +
		strconv.Itoa(pending) + " pending · " + strconv.Itoa(posted) + " posted · " + strconv.Itoa(ai) + " ai · " +
		plural(threads, "thread") + " (" + strconv.Itoa(open) + " open)"
}

// stripLines renders the strip in h rows: the summary, then (focused) the
// comment rows around the selection.
func (m Model) stripLines(h int) []string {
	out := make([]string, h)
	head := m.stripSummary()
	if m.stripFocus {
		head += " — j/k select · " + m.keys.StripJump.Help().Key + " jumps"
	} else {
		head += " (tab focuses)"
	}
	out[0] = m.st.strip.Render(fit(head, m.w))
	for i := 1; i < h; i++ {
		bi := m.stripOff + i - 1
		if bi >= len(m.c.blocks) {
			break
		}
		marker := "  "
		if bi == m.stripSel {
			marker = "▸ "
		}
		out[i] = fit(marker+diffview.Clean(stripEntry(&m.c.blocks[bi])), m.w)
	}
	return out
}

// stripEntry is one strip row: kind, path:line, excerpt.
func stripEntry(b *cblock) string {
	kind := stateWord(b.c.State)
	switch b.kind {
	case blockAI:
		kind = "ai " + sevWord(b.c.Severity)
	case blockThread:
		kind = "thread"
		if b.resolved() {
			kind = "resolved"
		}
	}
	loc := b.c.Path + ":" + commentLines(b.c)
	if b.c.Outdated {
		loc += " (outdated)"
	}
	return pad(kind, 10) + " " + loc + "  " + comments.Excerpt(b.c.Body)
}

// stripKey handles a key while the strip has focus: j/k (and the top and
// bottom keys) select, StripJump jumps. Other keys are not the strip's.
func (m *Model) stripKey(k tea.KeyMsg) bool {
	kk, n := m.keys, len(m.c.blocks)
	switch {
	case key.Matches(k, kk.Down):
		m.stripSel = min(m.stripSel+1, n-1)
	case key.Matches(k, kk.Up):
		m.stripSel = max(m.stripSel-1, 0)
	case key.Matches(k, kk.Top):
		m.stripSel = 0
	case key.Matches(k, kk.Bottom):
		m.stripSel = n - 1
	case key.Matches(k, kk.StripJump):
		m.stripJump()
		return true
	default:
		return false
	}
	m.clampStrip()
	return true
}

// clampStrip keeps the selection in range and in view.
func (m *Model) clampStrip() {
	n := len(m.c.blocks)
	m.stripSel = clamp(m.stripSel, 0, max(0, n-1))
	if m.stripSel < m.stripOff {
		m.stripOff = m.stripSel
	}
	if m.stripSel >= m.stripOff+stripRows {
		m.stripOff = m.stripSel - stripRows + 1
	}
	m.stripOff = clamp(m.stripOff, 0, max(0, n-stripRows))
}

// stripJump focuses the selected comment's unit and puts the cursor on its
// line (on the block itself when it sits under the unit header); focus
// returns to the pane.
func (m *Model) stripJump() {
	if m.stripSel >= len(m.c.blocks) {
		return
	}
	b := m.c.blocks[m.stripSel]
	if b.unit == "" || !m.FocusUnit(b.unit) {
		m.note = "comment is not on a unit of this diff: " + b.c.Path + ":" + commentLines(b.c)
		return
	}
	k := curKey{unit: b.unit, line: b.line}
	if b.line < 0 {
		k.block = b.id
	}
	if r := m.rowOfKey(k); r >= 0 {
		m.setCursorRow(r)
		m.ensureRowVisible(r)
	}
	m.stripFocus = false
	m.note = "jumped to " + diffview.Clean(b.c.Path) + ":" + commentLines(b.c)
}
