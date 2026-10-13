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
	rowFinding                  // one row of an inline review finding (by file)
	rowComment                  // one row of an inline comment block
	rowSplit                    // one visual row of a side-by-side line pair
)

// textStyle picks the style of a rowText line.
type textStyle uint8

const (
	textPlain textStyle = iota
	textBold
	textDim
	textWarn // alert style (high-severity findings)
	textRaw  // already styled and fitted to the pane width when built
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
	line int32 // index into paneUnit.lines; note index for rowNote; pane.splits index for rowSplit
	// pair is the row index of the paired -/+ line (-1 when unpaired); for
	// rowSplit it is the visual row within the pair (0 first, then the
	// wrapped continuation rows).
	pair int32
}

// srow is one side-by-side line pair: indices into the unit's lines of the
// old (left) and new (right) line, -1 for a blank side; a context line has
// left == right. parts is its number of visual rows (wrapped).
type srow struct {
	left, right int
	parts       int
}

// paired reports whether the pair is a deletion and an addition (intra-line
// highlighting applies).
func (s srow) paired() bool { return s.left >= 0 && s.right >= 0 && s.left != s.right }

// splitGeom is the side-by-side column layout of a pane: column widths
// (left, a one-cell separator, right) and their text widths.
type splitGeom struct {
	lw, rw, lt, rt int
}

// splitGutter is the gutter of a side-by-side column: mark (new side
// only), cursor, line number, space, op, space.
func splitGutter(digits int) int { return 2 + digits + 3 }

// newSplitGeom lays out two columns in width w for line numbers of digits.
func newSplitGeom(w, digits int) splitGeom {
	lw := (w - 1) / 2
	rw := w - 1 - lw
	g := splitGutter(digits)
	return splitGeom{lw: lw, rw: rw, lt: max(4, lw-g), rt: max(4, rw-g)}
}

// paneUnit is one visible unit of the selected file.
type paneUnit struct {
	u     *model.ChangeUnit
	a     *model.UnitAgg // nil without an aggregate
	lang  string         // syntax language ("" plain)
	lines []diff.Line    // nil when collapsed
	marks map[int]model.LineMark
	// threads marks the line indices with an unresolved GitHub thread (❱).
	threads map[int]bool
	chips   []chip
	// chipRows splits chips into header rows that fit the pane width:
	// chipRows[k] = [start, end) indices into chips for header row k.
	chipRows [][2]int
}

// wrapChips splits chips greedily into rows of at most width cells, each
// row starting with a prefix of prefix cells. Chips are measured as
// theme.Chips draws them: joined by " · " without colour, padded by a cell
// on each side and one space apart with colour. A chip wider than a row
// gets a row of its own (and is truncated on render).
func wrapChips(cs []chip, width, prefix int, colour bool) [][2]int {
	sep, padW := 3, 0
	if colour {
		sep, padW = 1, 2
	}
	var rows [][2]int
	start, used := 0, prefix
	for i, c := range cs {
		w := ansi.StringWidth(c.label()) + padW
		if i > start && used+sep+w > width {
			rows = append(rows, [2]int{start, i})
			start, used = i, prefix
		}
		if i > start {
			used += sep
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
	// flines are the inline finding rows (by file).
	flines []fline
	// clines are the inline comment rows.
	clines []cline

	// Side-by-side (split): the line pairs, the column layout, and the
	// line-number digits fixed before the units are laid out (wrapping
	// depends on them).
	split  bool
	geom   splitGeom
	splits []srow
}

func (p *pane) addText(s string, st textStyle) {
	p.rows = append(p.rows, prow{kind: rowText, line: int32(len(p.texts)), pair: -1})
	p.texts = append(p.texts, ptext{s: s, st: st})
}

// addRaw appends already styled lines (each fitted to the pane width).
func (p *pane) addRaw(lines ...string) {
	for _, s := range lines {
		p.addText(s, textRaw)
	}
}

func (p *pane) addBlank() { p.rows = append(p.rows, prow{kind: rowBlank, pair: -1}) }

// newPane starts the pane of the units ids; side-by-side it fixes the
// line-number digits and the column layout first.
func (m *Model) newPane(ids []string) *pane {
	p := &pane{split: m.splitOn()}
	if p.split {
		maxNo := 0
		for _, id := range ids {
			if !m.visible(id) || m.collapsed(id) {
				continue
			}
			for _, l := range m.c.linesOf(id) {
				maxNo = max(maxNo, l.OldNo, l.NewNo)
			}
		}
		p.gutter = len(strconv.Itoa(maxNo))
		p.geom = newSplitGeom(m.layout().paneW, p.gutter)
	}
	return p
}

// setGutter records the widest line number once the units are laid out
// (side-by-side fixed it up front).
func (p *pane) setGutter(maxNo int) {
	if !p.split {
		p.gutter = len(strconv.Itoa(maxNo))
	}
}

// buildPane lays out file fi's visible units in line order.
func (m *Model) buildPane(fi int) *pane {
	if fi < 0 || fi >= len(m.c.files) {
		return &pane{}
	}
	f := m.c.files[fi]
	p := m.newPane(f.units)
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
	p.setGutter(maxNo)
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
	pu.lines = m.c.linesOf(id)
	pu.lang = m.c.langOf(u.Path)
	if m.c.labelled && pu.a != nil && len(pu.a.Lines) > 0 {
		pu.marks = make(map[int]model.LineMark, len(pu.a.Lines))
		for _, lm := range pu.a.Lines {
			pu.marks[lm.Line] = lm
		}
	}
	w := m.layout().paneW
	pu.threads = m.c.threadLines(id)
	pu.chips = unitChips(u, pu.a, m.c.labelled)
	pu.chipRows = wrapChips(pu.chips, w, ansi.StringWidth(headPrefix(u)), m.th.Colour)
	p.units = append(p.units, pu)
	if m.byChange() {
		p.addRaw(m.unitTitle(id, w))
	}
	for k := range pu.chipRows {
		p.rows = append(p.rows, prow{kind: rowHeader, unit: ui, line: int32(k), pair: -1})
	}
	p.rows = append(p.rows, prow{kind: rowSig, unit: ui, pair: -1})
	if u.MovedFrom != "" {
		p.rows = append(p.rows, prow{kind: rowMoved, unit: ui, pair: -1})
	}
	atLine, rest := m.inlineFindings(id, pu.lines)
	m.addFindingRows(p, ui, rest, w)
	m.addBlockRows(p, ui, m.c.blocksAt(id, -1), w)
	if p.split {
		maxNo := m.addSplitLines(p, ui, atLine, w)
		p.rows = append(p.rows, prow{kind: rowBlank, pair: -1})
		return maxNo
	}
	lineRow := make([]int32, len(pu.lines))
	maxNo := 0
	for li, l := range pu.lines {
		lineRow[li] = int32(len(p.rows))
		p.rows = append(p.rows, prow{kind: rowLine, unit: ui, line: int32(li), pair: -1})
		if l.NoEOL {
			p.rows = append(p.rows, prow{kind: rowNoEOL, unit: ui, line: int32(li), pair: -1})
		}
		if l.Op != diff.Del && atLine[l.NewNo] != nil {
			m.addFindingRows(p, ui, atLine[l.NewNo], w)
		}
		m.addBlockRows(p, ui, m.c.blocksAt(id, li), w)
		maxNo = max(maxNo, l.OldNo, l.NewNo)
	}
	for _, pr := range diff.Pair(diff.Hunk{Lines: pu.lines}) {
		a, b := lineRow[pr[0]], lineRow[pr[1]]
		p.rows[a].pair, p.rows[b].pair = b, a
	}
	p.rows = append(p.rows, prow{kind: rowBlank, pair: -1})
	return maxNo
}

// inlineFindings returns unit id's findings to draw inline (by file only;
// by change they are listed after the group's units): those under a new-side
// line of lines, and the rest, drawn under the unit's header. Findings shown
// as comments or marked accepted/dismissed are left out (content.unitInline).
func (m *Model) inlineFindings(id string, lines []diff.Line) (map[int][]model.Finding, []model.Finding) {
	fs := m.c.unitInline[id]
	if m.byChange() || len(fs) == 0 {
		return nil, nil
	}
	newLines := make(map[int]bool, len(lines))
	for _, l := range lines {
		if l.Op != diff.Del {
			newLines[l.NewNo] = true
		}
	}
	return splitFindings(fs, newLines)
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

// unitSpan returns the first and last rows of unit id (its header through
// the row before the next unit's header), or -1, -1.
func (p *pane) unitSpan(id string) (int, int) {
	for i, pu := range p.units {
		if pu.u.ID != id {
			continue
		}
		end := len(p.rows) - 1
		if i+1 < len(p.heads) {
			end = p.heads[i+1] - 1
		}
		return p.heads[i], end
	}
	return -1, -1
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
