package codeview

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/diffview"
	"github.com/fredricw/prt/internal/model"
)

// Comment block texts.
const (
	// blockIndent is the prefix of a comment row: the gutter mark cell and
	// the cursor cell.
	blockIndent = 2
	aiHint      = "a accept · d dismiss · ! override"
	glyphThread = "❱"
	glyphDraft  = "✎"
)

// cline is one rendered row of an inline comment block.
type cline struct {
	s     string // styled text after the two-cell prefix, at most the pane width − 2
	block int    // index into content.blocks
	head  bool   // the block's first row (a cursor target)
}

// addBlockRows appends the rows of comment blocks bis to unit ui.
func (m *Model) addBlockRows(p *pane, ui int32, bis []int, width int) {
	w := max(8, width-blockIndent)
	for _, bi := range bis {
		for i, s := range m.blockLines(&m.c.blocks[bi], w) {
			p.rows = append(p.rows, prow{kind: rowComment, unit: ui, line: int32(len(p.clines)), pair: -1})
			p.clines = append(p.clines, cline{s: s, block: bi, head: i == 0})
		}
	}
}

// blockLines renders one comment block at width w.
func (m *Model) blockLines(b *cblock, w int) []string {
	switch b.kind {
	case blockAI:
		return m.aiLines(b, w)
	case blockThread:
		return m.threadBlockLines(b, w)
	}
	return m.userLines(b, w)
}

// userLines renders a user comment: "✎ draft · you" (pending, posted),
// the range and position notes, then the Glamour body behind a bar.
func (m *Model) userLines(b *cblock, w int) []string {
	head := glyphDraft + " " + stateWord(b.c.State) + " · you" + rangeSuffix(b.c) + placeSuffix(b)
	out := []string{m.st.cHead.Render(fit(diffview.Clean(head), w))}
	return append(out, m.barred(b.c.Body, w)...)
}

// aiLines renders an ai-draft in a dim rounded border: the severity in the
// top edge, the Glamour body, then the a/d/! hint.
func (m *Model) aiLines(b *cblock, w int) []string {
	inner := max(4, w-4)
	label := "╭─ ai-draft · " + sevWord(b.c.Severity) + placeSuffix(b) + " "
	top := fit(label, w-1)
	top += strings.Repeat("─", max(0, w-1-ansi.StringWidth(top))) + "╮"
	out := []string{m.st.cDim.Render(top)}
	row := func(s string) string {
		return m.st.cDim.Render("│ ") + pad(fit(s, inner), inner) + m.st.cDim.Render(" │")
	}
	for _, l := range m.md.lines(b.c.Body, inner) {
		out = append(out, row(l))
	}
	out = append(out, row(m.st.cDim.Render(aiHint)))
	return append(out, m.st.cDim.Render("╰"+strings.Repeat("─", max(0, w-2))+"╯"))
}

// threadBlockLines renders a GitHub thread: "❱ thread · author · n replies
// · open" and the first comment's body; a resolved thread is one dim,
// collapsed line.
func (m *Model) threadBlockLines(b *cblock, w int) []string {
	state := "open"
	if b.resolved() {
		state = "resolved"
	}
	head := glyphThread + " thread · " + authorWord(b.c.Author) + " · " + repliesText(len(b.replies)) + " · " + state + placeSuffix(b)
	if b.resolved() {
		return []string{m.st.cDim.Render(fit(diffview.Clean(head+" (t opens)"), w))}
	}
	out := []string{m.st.cHead.Render(fit(diffview.Clean(head), w))}
	return append(out, m.barred(b.c.Body, w)...)
}

// barred renders a markdown body behind a dim "│ " bar.
func (m *Model) barred(body string, w int) []string {
	ls := m.md.lines(body, w-2)
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, m.st.cDim.Render("│ ")+l)
	}
	return out
}

// stateWord is a user comment's lifecycle in words.
func stateWord(s model.CommentState) string {
	if s == "" {
		return string(model.CommentDraft)
	}
	return string(s)
}

// sevWord is a severity, "?" when unset.
func sevWord(s model.Severity) string {
	if s == "" {
		return "?"
	}
	return string(s)
}

// authorWord is a comment author, "unknown" when unset.
func authorWord(a string) string {
	if a == "" {
		return "unknown"
	}
	return a
}

// repliesText is "1 reply" / "N replies".
func repliesText(n int) string {
	if n == 1 {
		return "1 reply"
	}
	return strconv.Itoa(n) + " replies"
}

// commentLines is a comment's lines: "L12", "L12–14", with "old " for
// the old side.
func commentLines(c model.Comment) string {
	prefix := "L"
	if c.Side == model.SideOld {
		prefix = "old L"
	}
	if c.StartLine > 0 && c.StartLine != c.Line {
		return prefix + strconv.Itoa(c.StartLine) + "–" + strconv.Itoa(c.Line)
	}
	return prefix + strconv.Itoa(c.Line)
}

// rangeSuffix is " · L12–14" for a multi-line comment, else "".
func rangeSuffix(c model.Comment) string {
	if c.StartLine > 0 && c.StartLine != c.Line {
		return " · " + commentLines(c)
	}
	return ""
}

// placeSuffix notes a block drawn under its unit header: " · outdated
// (L40)" for an outdated comment, " · L40" otherwise; "" under its line.
func placeSuffix(b *cblock) string {
	switch {
	case b.line >= 0:
		return ""
	case b.c.Outdated:
		return " · outdated (" + commentLines(b.c) + ")"
	case b.c.Line > 0:
		return " · " + commentLines(b.c)
	}
	return ""
}

// commentRow renders an inline comment row with its cursor cell.
func (m Model) commentRow(cl cline, deco, width int) string {
	return ansi.Truncate(" "+decoGlyph(deco)+cl.s, width, ellipsis)
}
