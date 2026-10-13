package codeview

import (
	"strconv"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/model"
)

// rowKind is what one right-pane row shows.
type rowKind uint8

const (
	rowHeader    rowKind = iota // chips strip of a unit
	rowSig                      // symbol/signature, range and counts
	rowMoved                    // "moved from <path>:<line>"
	rowLine                     // one diff line
	rowNoEOL                    // "\ No newline at end of file"
	rowCollapsed                // one-line noise summary
	rowNote                     // dim note (hidden units, empty file)
	rowBlank                    // gap between units
	rowGroupHead                // chips strip of a group (by change)
	rowText                     // a styled text line (by change: why block, counts)
)

// textStyle picks the style of a rowText line.
type textStyle uint8

const (
	textPlain textStyle = iota
	textBold
	textDim
)

// ptext is the content of one rowText line.
type ptext struct {
	s  string
	st textStyle
}

// prow is one right-pane row; it holds indices, strings are made on render.
type prow struct {
	kind rowKind
	unit int32 // index into pane.units
	line int32 // index into paneUnit.lines; note index for rowNote
	pair int32 // row index of the paired -/+ line, -1 when unpaired
}

// paneUnit is one visible unit of the selected file.
type paneUnit struct {
	u     *model.ChangeUnit
	a     *model.UnitAgg // nil without an aggregate
	lines []diff.Line    // nil when collapsed
	marks map[int]model.LineMark
	chips []chip
	// chipRows splits chips into header rows that fit the pane width:
	// chipRows[k] = [start, end) indices into chips for header row k.
	chipRows [][2]int
}

// chipSep separates chips in a header row.
const chipSep = " · "

// wrapChips splits chips greedily into rows of at most width cells, each
// row starting with a two-cell prefix ("┌ " then "│ "). A chip wider than
// a row gets a row of its own (and is truncated on render).
func wrapChips(cs []chip, width int) [][2]int {
	var rows [][2]int
	start, used := 0, 2
	for i, c := range cs {
		w := ansi.StringWidth(c.label())
		if i > start && used+len([]rune(chipSep))+w > width {
			rows = append(rows, [2]int{start, i})
			start, used = i, 2
		}
		if i > start {
			used += len([]rune(chipSep))
		}
		used += w
	}
	return append(rows, [2]int{start, len(cs)})
}

// pane is the row index of the selected file: built once per selection,
// filter or expansion change, rendered lazily.
type pane struct {
	rows   []prow
	units  []paneUnit
	heads  []int // header row of each unit (same order as units)
	notes  []string
	gutter int // digits of the widest line number

	// By change: the group header's chips and their rows, and text lines.
	gchips    []chip
	gchipRows [][2]int
	texts     []ptext
}

func (p *pane) addText(s string, st textStyle) {
	p.rows = append(p.rows, prow{kind: rowText, line: int32(len(p.texts)), pair: -1})
	p.texts = append(p.texts, ptext{s: s, st: st})
}

func (p *pane) addBlank() { p.rows = append(p.rows, prow{kind: rowBlank, pair: -1}) }

// buildPane lays out file fi's visible units in line order.
func (m *Model) buildPane(fi int) *pane {
	p := &pane{}
	if fi < 0 || fi >= len(m.c.files) {
		return p
	}
	f := m.c.files[fi]
	hidden, maxNo := 0, 0
	for _, id := range f.units {
		if !m.visible(id) {
			hidden++
			continue
		}
		maxNo = max(maxNo, m.addUnit(p, id))
	}
	switch {
	case len(f.units) == 0:
		p.addNote("no change units in this file (binary or empty)")
	case hidden > 0: // after the visible units, or alone when all are hidden
		p.addNote(hiddenText(hidden))
	}
	p.gutter = len(strconv.Itoa(maxNo))
	return p
}

func hiddenText(n int) string {
	if n == 1 {
		return "1 unit hidden by filters (f)"
	}
	return strconv.Itoa(n) + " units hidden by filters (f)"
}

func (p *pane) addNote(s string) {
	p.rows = append(p.rows, prow{kind: rowNote, line: int32(len(p.notes)), pair: -1})
	p.notes = append(p.notes, s)
}

// addUnit appends unit id's rows and returns its largest line number.
func (m *Model) addUnit(p *pane, id string) int {
	u := m.c.units[id]
	pu := paneUnit{u: u, a: m.c.aggs[id]}
	ui := int32(len(p.units))
	p.heads = append(p.heads, len(p.rows))
	if m.collapsed(id) {
		p.units = append(p.units, pu)
		p.rows = append(p.rows, prow{kind: rowCollapsed, unit: ui, pair: -1}, prow{kind: rowBlank, pair: -1})
		return 0
	}
	pu.lines = unitLines(u)
	if m.c.labelled && pu.a != nil && len(pu.a.Lines) > 0 {
		pu.marks = make(map[int]model.LineMark, len(pu.a.Lines))
		for _, lm := range pu.a.Lines {
			pu.marks[lm.Line] = lm
		}
	}
	pu.chips = unitChips(u, pu.a, m.c.labelled)
	pu.chipRows = wrapChips(pu.chips, m.layout().paneW)
	p.units = append(p.units, pu)
	for k := range pu.chipRows {
		p.rows = append(p.rows, prow{kind: rowHeader, unit: ui, line: int32(k), pair: -1})
	}
	p.rows = append(p.rows, prow{kind: rowSig, unit: ui, pair: -1})
	if u.MovedFrom != "" {
		p.rows = append(p.rows, prow{kind: rowMoved, unit: ui, pair: -1})
	}
	lineRow := make([]int32, len(pu.lines))
	maxNo := 0
	for li, l := range pu.lines {
		lineRow[li] = int32(len(p.rows))
		p.rows = append(p.rows, prow{kind: rowLine, unit: ui, line: int32(li), pair: -1})
		if l.NoEOL {
			p.rows = append(p.rows, prow{kind: rowNoEOL, unit: ui, line: int32(li), pair: -1})
		}
		maxNo = max(maxNo, l.OldNo, l.NewNo)
	}
	for _, pr := range diff.Pair(diff.Hunk{Lines: pu.lines}) {
		a, b := lineRow[pr[0]], lineRow[pr[1]]
		p.rows[a].pair, p.rows[b].pair = b, a
	}
	p.rows = append(p.rows, prow{kind: rowBlank, pair: -1})
	return maxNo
}

// unitAt returns the index (into p.units) of the unit whose header is at
// or above row r; the first unit when every header is below; -1 if none.
func (p *pane) unitAt(r int) int {
	if len(p.heads) == 0 {
		return -1
	}
	best := 0
	for i, h := range p.heads {
		if h > r {
			break
		}
		best = i
	}
	return best
}

// headOf returns the header row of unit id, or -1.
func (p *pane) headOf(id string) int {
	for i, pu := range p.units {
		if pu.u.ID == id {
			return p.heads[i]
		}
	}
	return -1
}
