package pipeview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
)

const (
	// minBoxInner is the narrowest box content ("◌ cluster").
	minBoxInner = 9
	// maxBoxInner caps box width on wide terminals.
	maxBoxInner = 16
	// arrowSep is the "→" separator between boxes when it fits.
	arrowSep = " → "
)

// cell is the content of one stage box.
type cell struct {
	stage  model.Stage
	status model.StageStatus // "" when the stage has no row in the current run
	state  string            // wall time, elapsed, "queued", "cached", …
	io     string            // "14→22", "" when no counts
}

// cell returns the box content of strip stage i from the current run.
func (m *Model) cell(i int) cell {
	s := stripStages[i]
	c := cell{stage: s, state: "not run"}
	rl, ok := m.run.latest[s]
	if !ok {
		if st := m.d.Item.State; st == model.StateAnalysing || st == model.StateFetching {
			c.state = "queued"
		}
		return c
	}
	c.status = rl.Status
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
	perRow  int  // boxes per row
	inner   int  // box content width
	arrows  bool // "→" between boxes (else one space)
	compact bool // one line per stage
}

// sep is the separator between two boxes of a row.
func (l stripLayout) sep() string {
	if l.arrows {
		return arrowSep
	}
	return " "
}

// layoutStrip fits 8 boxes in one row (with arrows when they fit), else 2
// rows of 4, else one line per stage.
func layoutStrip(w int) stripLayout {
	for _, per := range []int{8, 4} {
		for _, arrows := range []bool{true, false} {
			sep := 1
			if arrows {
				sep = ansi.StringWidth(arrowSep)
			}
			inner := (w-(per-1)*sep)/per - 2
			if inner >= minBoxInner {
				return stripLayout{perRow: per, inner: min(inner, maxBoxInner), arrows: arrows}
			}
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
			line := fmt.Sprintf("%s%s %s %s %s", mark, boxGlyph(c.status), stageName(c.stage), c.state, c.io)
			if focused && i == m.stage {
				line = m.st.sel.Render(line)
			}
			b.hit(hit{y: len(b.lines), x0: 0, x1: m.w, idx: i})
			b.add(strings.TrimRight(line, " "))
		}
		b.stripEnd = len(b.lines)
		return
	}
	sepW := ansi.StringWidth(lay.sep())
	for start := 0; start < len(stripStages); start += lay.perRow {
		end := min(start+lay.perRow, len(stripStages))
		cells := make([]cell, 0, end-start)
		for i := start; i < end; i++ {
			cells = append(cells, m.cell(i))
		}
		rows := boxContent(cells, lay.inner)
		height := len(rows[0]) + 2
		y := len(b.lines)
		boxes := make([][]string, len(cells))
		for k, c := range cells {
			i := start + k
			x0 := k * (lay.inner + 2 + sepW)
			b.hit(hit{y: y, y1: y + height, x0: x0, x1: x0 + lay.inner + 2, idx: i})
			boxes[k] = m.stageBox(c, rows[k], lay.inner, focused && i == m.stage)
		}
		b.add(joinBoxes(boxes, lay, m.st.dim)...)
	}
	b.stripEnd = len(b.lines)
}

// boxGlyph is a stage status's mark in the strip boxes (v3 Pipeline): done
// ✓, running ▶, queued ◌, failed ✗, cached ≡, skipped –.
func boxGlyph(s model.StageStatus) string {
	switch s {
	case model.StageRunning:
		return "▶"
	case model.StageQueued, "":
		return "◌"
	}
	return glyph(s)
}

// boxContent is each box's content lines: "✓ parse", then "0→24 · 120 ms"
// when every box of the row fits that, else the counts and the time on two
// lines.
func boxContent(cells []cell, inner int) [][]string {
	joined := true
	for _, c := range cells {
		if ansi.StringWidth(joinIO(c)) > inner {
			joined = false
		}
	}
	out := make([][]string, len(cells))
	for i, c := range cells {
		first := trunc(boxGlyph(c.status)+" "+stageName(c.stage), inner)
		if joined {
			out[i] = []string{first, joinIO(c)}
			continue
		}
		out[i] = []string{first, trunc(c.io, inner), trunc(c.state, inner)}
	}
	return out
}

// joinIO is "24→12 · 2.3 s" (the time alone without counts).
func joinIO(c cell) string {
	if c.io == "" {
		return c.state
	}
	return c.io + " · " + c.state
}

// stageBox draws one stage box: plain border when done or cached, blue
// when running, red when failed, dashed when queued or not run; heavy and
// accent when selected.
func (m *Model) stageBox(c cell, lines []string, inner int, selected bool) []string {
	th := m.st.th
	tl, tr, bl, br, hz, vt := "┌", "┐", "└", "┘", "─", "│"
	bs := th.R.NewStyle().Foreground(th.P.Border)
	switch c.status {
	case model.StageRunning:
		bs = th.Blue
	case model.StageFailed:
		bs = th.Del
	case model.StageQueued, "":
		hz, vt = "╌", "╎"
	}
	if selected {
		tl, tr, bl, br, hz, vt = "┏", "┓", "┗", "┛", "━", "┃"
		bs = th.Accent
	}
	out := []string{bs.Render(tl + strings.Repeat(hz, inner) + tr)}
	for i, l := range lines {
		out = append(out, bs.Render(vt)+m.stageText(c, i, padRight(l, inner))+bs.Render(vt))
	}
	return append(out, bs.Render(bl+strings.Repeat(hz, inner)+br))
}

// stageText styles a box's content line: the first by status, the rest dim.
func (m *Model) stageText(c cell, i int, s string) string {
	if i > 0 {
		return m.st.dim.Render(s)
	}
	switch c.status {
	case model.StageDone:
		return m.st.good.Render(s)
	case model.StageRunning:
		return m.st.blue.Render(s)
	case model.StageFailed:
		return m.st.warn.Render(s)
	}
	return m.st.dim.Render(s)
}

// joinBoxes puts a row of boxes side by side with the layout's separator
// ("→" on the box's middle line).
func joinBoxes(boxes [][]string, lay stripLayout, dim lipgloss.Style) []string {
	if len(boxes) == 0 {
		return nil
	}
	h := len(boxes[0])
	out := make([]string, h)
	blank := strings.Repeat(" ", ansi.StringWidth(lay.sep()))
	for y := 0; y < h; y++ {
		var sb strings.Builder
		for k, bx := range boxes {
			if k > 0 {
				if lay.arrows && y == h/2 {
					sb.WriteString(dim.Render(lay.sep()))
				} else {
					sb.WriteString(blank)
				}
			}
			sb.WriteString(bx[y])
		}
		out[y] = sb.String()
	}
	return out
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
