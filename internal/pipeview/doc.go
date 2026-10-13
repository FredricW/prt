package pipeview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/theme"
)

// hit is a clickable region of the page document: a stage box (lines
// y..y1, cells x0..x1) or a routing row.
type hit struct {
	y, y1  int // y1 = 0 means one line
	x0, x1 int
	idx    int
	table  bool
}

// contains reports whether document cell (x, y) is inside h.
func (h hit) contains(x, y int) bool {
	y1 := h.y1
	if y1 == 0 {
		y1 = h.y + 1
	}
	return y >= h.y && y < y1 && x >= h.x0 && x < h.x1
}

// docBuilder collects the page's lines and where the selectable parts are.
type docBuilder struct {
	lines              []string
	hits               []hit
	stripTop, stripEnd int
	logEnd             int // end of the stage log (0 when closed)
	rowTop, rowEnd     []int
}

func (b *docBuilder) add(s ...string) { b.lines = append(b.lines, s...) }
func (b *docBuilder) hit(h hit)       { b.hits = append(b.hits, h) }

// sideMinWidth is the width from which the rules, grouping, calibration
// and urgency boxes sit in a right column beside the routing table (v3
// Pipeline); below it they follow the totals.
const sideMinWidth = 120

// sideWidth is the right column's width at w: a third of the page, 40 to
// 60 cells.
func sideWidth(w int) int { return clampInt(w/3, 40, 60) }

// sideGap separates the left and right columns.
const sideGap = 2

// build lays out the whole page: header, stage strip (and its log), then
// the routing box and totals with the rules, grouping, calibration and
// urgency boxes beside them (or below them on narrower pages).
func (m *Model) build() docBuilder {
	var b docBuilder
	b.add(m.headerLines()...)
	m.buildStrip(&b)
	if m.stageLog {
		m.buildStageLog(&b)
	}
	b.add("")
	m.buildBody(&b)
	return b
}

// buildBody adds the routing box, totals and side boxes.
func (m *Model) buildBody(b *docBuilder) {
	leftW := m.w
	beside := m.w >= sideMinWidth
	sideW := m.w
	if beside {
		sideW = sideWidth(m.w)
		leftW = m.w - sideW - sideGap
	}
	var left docBuilder
	m.buildTable(&left, leftW)
	left.add("")
	left.add(m.totalsLines(leftW)...)
	right := m.sideBoxes(sideW)
	top := len(b.lines)
	for _, h := range left.hits {
		h.y += top
		b.hit(h)
	}
	for i := range left.rowTop {
		b.rowTop = append(b.rowTop, left.rowTop[i]+top)
		b.rowEnd = append(b.rowEnd, left.rowEnd[i]+top)
	}
	if !beside {
		b.add(left.lines...)
		b.add("")
		b.add(right...)
		return
	}
	for i := 0; i < max(len(left.lines), len(right)); i++ {
		l, r := "", ""
		if i < len(left.lines) {
			l = left.lines[i]
		}
		if i < len(right) {
			r = right[i]
		}
		b.add(padRight(l, leftW) + strings.Repeat(" ", sideGap) + r)
	}
}

// sideBoxes are the Rules, Grouping, Calibration and Urgency boxes of
// width w, one blank line apart.
func (m *Model) sideBoxes(w int) []string {
	inner := max(1, w-4)
	var out []string
	add := func(title, right string, lines []string) {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, m.boxLines(title, right, lines, w, false)...)
	}
	add("Rules · prt.routing.toml", "e edit", m.rulesLines(inner))
	add("Grouping", "", m.groupingLines(inner))
	add("Calibration", "", m.calibrationLines(inner))
	add("Urgency", "weights from prt.toml [rank]", m.urgencyLines(inner))
	return out
}

// boxLines draws lines in a titled box of width w.
func (m *Model) boxLines(title, right string, lines []string, w int, focused bool) []string {
	if len(lines) == 0 {
		lines = []string{""}
	}
	return strings.Split(m.st.th.Box(title, strings.Join(lines, "\n"), w, theme.BoxOpt{Right: right, Focused: focused}), "\n")
}

// headerLines are "prt · #N title · pipeline" with the run state on the
// right ("▶ reviewing · 17s elapsed · analysing (review)") and a blank
// line; when both do not fit one line the run state takes the second.
func (m *Model) headerLines() []string {
	it := m.d.Item
	name := it.Ref
	if it.Number > 0 {
		name = fmt.Sprintf("#%d", it.Number)
	}
	if it.Title != "" {
		name += " " + it.Title
	}
	left := m.st.accent.Bold(true).Render("prt") + m.st.dim.Render(" · ") + strings.TrimSpace(name) +
		m.st.dim.Render(" · ") + m.st.title.Render("pipeline")
	right := m.runState()
	if it.State != "" {
		st := string(it.State)
		if it.StateNote != "" {
			st += " (" + it.StateNote + ")"
		}
		if right != "" {
			right += m.st.dim.Render(" · ")
		}
		right += m.st.dim.Render(st)
	}
	if ansi.StringWidth(left)+ansi.StringWidth(right)+2 > m.w {
		return []string{trunc(left, m.w), trunc(right, m.w)}
	}
	return []string{theme.Spread(left, right, m.w), ""}
}

// stageVerb names a running stage in the header ("reviewing").
func stageVerb(s model.Stage) string {
	switch s {
	case model.StageParse:
		return "parsing"
	case model.StageLink:
		return "linking"
	case model.StageEmbed:
		return "embedding"
	case model.StageJev:
		return "labelling"
	case model.StageCluster:
		return "clustering"
	case model.StageRoute:
		return "routing"
	case model.StageReview:
		return "reviewing"
	case model.StageAggregate:
		return "aggregating"
	}
	return string(s)
}

// runState is the current run in words: "▶ reviewing · 17s elapsed" while
// a stage runs, "✗ <stage> failed", "✓ done" or "" before any run.
func (m *Model) runState() string {
	if len(m.run.all) == 0 {
		return ""
	}
	for i := len(stripStages) - 1; i >= 0; i-- {
		rl, ok := m.run.latest[stripStages[i]]
		if !ok || rl.Status != model.StageRunning {
			continue
		}
		s := m.st.blue.Render("▶ " + stageVerb(rl.Stage))
		start := m.run.start
		if start.IsZero() {
			start = rl.StartedAt
		}
		if el := m.elapsed(start); el != "" {
			s += m.st.dim.Render(" · " + el + " elapsed")
		}
		return s
	}
	for _, s := range stripStages {
		if rl, ok := m.run.latest[s]; ok && rl.Status == model.StageFailed {
			return m.st.warn.Render("✗ " + string(s) + " failed")
		}
	}
	if st := m.d.Item.State; st == model.StateAnalysing || st == model.StateFetching {
		return m.st.dim.Render("◌ waiting")
	}
	return m.st.good.Render("✓ done")
}
