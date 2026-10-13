package pipeview

import (
	"fmt"
	"strings"
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

// build lays out the whole page: header, stage strip (and its log),
// routing table, totals, rules, grouping and calibration.
func (m *Model) build() docBuilder {
	var b docBuilder
	b.add(m.headerLine())
	b.add("")
	m.buildStrip(&b)
	if m.stageLog {
		m.buildStageLog(&b)
	}
	sections := []func(*docBuilder){m.buildTable, m.buildTotals, m.buildRules, m.buildGrouping, m.buildCalibration}
	for _, s := range sections {
		b.add("")
		s(&b)
	}
	return b
}

// headerLine names the item and its state.
func (m *Model) headerLine() string {
	it := m.d.Item
	name := it.Ref
	if it.Number > 0 {
		name = fmt.Sprintf("#%d", it.Number)
	}
	if it.Title != "" {
		name += " " + it.Title
	}
	line := m.st.title.Render("Pipeline · " + strings.TrimSpace(name))
	if it.State != "" {
		st := string(it.State)
		if it.StateNote != "" {
			st += " (" + it.StateNote + ")"
		}
		line += m.st.dim.Render(" · " + st)
	}
	return line
}
