package pipeview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
)

const (
	// minBoxInner is the narrowest box content ("cluster ⟳", "aggregate").
	minBoxInner = 9
	// maxBoxInner caps box width on wide terminals.
	maxBoxInner = 14
	// boxLines is a box's height: border, three content lines, border.
	boxLines = 5
)

// cell is the content of one stage box.
type cell struct {
	stage model.Stage
	glyph string
	state string // wall time, elapsed, "queued", "cached", …
	io    string // "14→22", "" when no counts
}

// cell returns the box content of strip stage i from the current run.
func (m *Model) cell(i int) cell {
	s := stripStages[i]
	c := cell{stage: s, glyph: glyph(model.StageQueued), state: "not run"}
	rl, ok := m.run.latest[s]
	if !ok {
		if st := m.d.Item.State; st == model.StateAnalysing || st == model.StateFetching {
			c.state = "queued"
		}
		return c
	}
	c.glyph = glyph(rl.Status)
	switch rl.Status {
	case model.StageDone, model.StageFailed:
		c.state = dur(rl.Ms)
	case model.StageRunning:
		c.state = "running"
		if el := m.elapsed(rl.StartedAt); el != "" {
			c.state = el
		}
	case model.StageCached:
		c.state = "cached"
	case model.StageSkipped:
		c.state = "skipped"
	default:
		c.state = "queued"
	}
	switch {
	case rl.Status == model.StageRunning && rl.Out == 0:
		c.io = fmt.Sprintf("%d→…", rl.In)
	case rl.In > 0 || rl.Out > 0:
		c.io = fmt.Sprintf("%d→%d", rl.In, rl.Out)
	}
	return c
}

// stripLayout is how the eight boxes are arranged at the current width.
type stripLayout struct {
	perRow  int // boxes per row; 0 = compact one line per stage
	inner   int // box content width
	compact bool
}

// layoutStrip fits 8 boxes in one row, else 2 rows of 4, else one line per
// stage.
func layoutStrip(w int) stripLayout {
	for _, per := range []int{8, 4} {
		inner := (w-(per-1))/per - 2
		if inner >= minBoxInner {
			return stripLayout{perRow: per, inner: min(inner, maxBoxInner)}
		}
	}
	return stripLayout{compact: true}
}

// buildStrip adds the stage strip section (fetch prefix line and boxes).
func (m *Model) buildStrip(b *docBuilder) {
	head := m.st.head.Render("Stages")
	if m.run.fetch != nil {
		f := m.run.fetch
		head += m.st.dim.Render(fmt.Sprintf("  run %s · fetch %s %s", clock(f.StartedAt), glyph(f.Status), dur(f.Ms)))
	} else if len(m.run.all) == 0 {
		head += m.st.dim.Render("  no run yet")
	}
	b.add(head)
	focused := m.panel == panelStrip
	lay := layoutStrip(m.w)
	b.stripTop = len(b.lines)
	if lay.compact {
		for i := range stripStages {
			c := m.cell(i)
			mark := "  "
			if focused && i == m.stage {
				mark = "▸ "
			}
			line := fmt.Sprintf("%s%s %s %s %s", mark, stageName(c.stage), c.glyph, c.state, c.io)
			if focused && i == m.stage {
				line = m.st.sel.Render(line)
			}
			b.hit(hit{y: len(b.lines), x0: 0, x1: m.w, idx: i})
			b.add(strings.TrimRight(line, " "))
		}
		b.stripEnd = len(b.lines)
		return
	}
	for start := 0; start < len(stripStages); start += lay.perRow {
		boxes := make([]string, 0, lay.perRow*2)
		y := len(b.lines)
		for i := start; i < start+lay.perRow && i < len(stripStages); i++ {
			if i > start {
				boxes = append(boxes, " ")
			}
			x0 := (i - start) * (lay.inner + 3)
			b.hit(hit{y: y, y1: y + boxLines, x0: x0, x1: x0 + lay.inner + 2, idx: i})
			boxes = append(boxes, m.box(i, lay.inner, focused && i == m.stage))
		}
		b.add(strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, boxes...), "\n")...)
	}
	b.stripEnd = len(b.lines)
}

// box renders stage box i with content width inner.
func (m *Model) box(i, inner int, selected bool) string {
	c := m.cell(i)
	name := stageName(c.stage)
	if ansi.StringWidth(name)+2 > inner {
		name = trunc(name, inner-2)
	}
	lines := []string{
		padRight(name+" "+c.glyph, inner),
		padRight(c.state, inner),
		padRight(c.io, inner),
	}
	st := m.st.box
	if selected {
		st = m.st.boxSel
	}
	return st.Render(strings.Join(lines, "\n"))
}

// buildStageLog adds the selected stage's log below the strip: every row
// of the stage, newest first; "•" marks rows of the current run.
func (m *Model) buildStageLog(b *docBuilder) {
	s := stripStages[m.stage]
	rows := m.run.stageRows(s)
	b.add(m.st.title.Render(fmt.Sprintf("── %s log · %s", s, plural(len(rows), "row", "rows"))) +
		m.st.dim.Render(" · • current run · j/k scroll · r re-run · esc closes ──"))
	if len(rows) == 0 {
		b.add(m.st.dim.Render("  no runs of this stage recorded"))
		b.logEnd = len(b.lines)
		return
	}
	b.add(m.st.dim.Render(fmt.Sprintf("  %-14s %-13s %8s %5s %8s %-5s %s", "time", "status", "wall", "calls", "cost", "cache", "note")))
	off := clampInt(m.logOff, 0, max(0, len(rows)-maxLogRows))
	for _, rl := range rows[off:min(len(rows), off+maxLogRows)] {
		mark := " "
		if m.run.inRun(rl) {
			mark = "•"
		}
		cache := "–"
		if rl.CacheHit {
			cache = "hit"
		}
		wall := dur(rl.Ms)
		if rl.Status == model.StageRunning || rl.Status == model.StageQueued {
			wall = "—"
		}
		line := fmt.Sprintf("%s %-14s %-13s %8s %5d %8s %-5s ", mark, clock(rl.StartedAt), rl.Status, wall, rl.Calls, money(rl.Cost), cache)
		note := rl.Note
		if rl.Err != "" {
			note = strings.TrimSpace(note + " " + m.st.warn.Render("✗ "+rl.Err))
		}
		b.add(line + note)
	}
	if more := len(rows) - off - maxLogRows; more > 0 {
		b.add(m.st.dim.Render(fmt.Sprintf("  … %d older (j)", more)))
	}
	b.logEnd = len(b.lines)
}

// maxLogRows is how many stage-log rows show at once.
const maxLogRows = 8

// StageStrip renders the one-line stage strip for the picker's status
// line while an item is analysing (spec: "The stage strip is also shown as
// a one-line status in the picker"), from the item's RunLogs: the latest
// row per stage of the current run, "parse ✓ link ✓ embed ✓ jev ⟳ cluster
// · route · review · agg ·". When that does not fit width it falls back to
// three-letter names, then to glyphs only, then truncates. width <= 0 means
// no limit; no rows give "".
func StageStrip(rls []model.RunLog, width int) string {
	if len(rls) == 0 {
		return ""
	}
	run := currentRun(rls)
	forms := []func(model.Stage, string) string{
		func(s model.Stage, g string) string { return stageName(s) + " " + g },
		func(s model.Stage, g string) string { return trunc3(stageName(s)) + " " + g },
		func(_ model.Stage, g string) string { return g },
	}
	seps := []string{" ", " ", ""}
	var out string
	for fi, form := range forms {
		parts := make([]string, len(stripStages))
		for i, s := range stripStages {
			g := glyph(model.StageQueued)
			if rl, ok := run.latest[s]; ok {
				g = glyph(rl.Status)
			}
			parts[i] = form(s, g)
		}
		out = strings.Join(parts, seps[fi])
		if width <= 0 || ansi.StringWidth(out) <= width {
			return out
		}
	}
	return trunc(out, width)
}

// trunc3 shortens a stage name to three letters.
func trunc3(s string) string {
	if len(s) > 3 {
		return s[:3]
	}
	return s
}
