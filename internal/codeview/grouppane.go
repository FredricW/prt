package codeview

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/diffview"
)

// Fixed by-change texts.
const (
	textWhy         = "why these belong together"
	textPlaceholder = "│ placeholder name (code-derived; no description bullet names this group)"
	textAITitle     = "│ ai· title written by the fast model (no description bullet names this group)"
)

// buildGroupPane lays out group gi: header chips, the "why these belong
// together" block, the visible units in review order (a mechanical group:
// the example, "×N verified identical", members and promoted-out units),
// then the group's review findings.
func (m *Model) buildGroupPane(gi int) *pane {
	p := &pane{}
	if gi < 0 || gi >= len(m.c.groups) {
		return p
	}
	ge := &m.c.groups[gi]
	w := m.layout().paneW
	p.gchips = append(groupChips(ge, m.c.labelled), routingChips(m.c.reviewOf(ge))...)
	p.gchipRows = wrapChips(p.gchips, w)
	for k := range p.gchipRows {
		p.rows = append(p.rows, prow{kind: rowGroupHead, line: int32(k), pair: -1})
	}
	switch {
	case ge.aiTitle():
		p.addText(textAITitle, textDim)
	case ge.placeholder():
		p.addText(textPlaceholder, textDim)
	}
	p.addBlank()
	p.addText(textWhy, textBold)
	why := m.c.whyLines(ge)
	if len(why) == 0 {
		why = []string{whyNone(ge)}
	}
	for _, s := range why {
		p.addText("  "+s, textPlain)
	}
	p.addBlank()
	maxNo := 0
	if ge.mechanical() {
		maxNo = m.addMechanical(p, ge, w)
	} else {
		for _, id := range ge.units {
			if m.visible(id) {
				maxNo = max(maxNo, m.addUnit(p, id))
			}
		}
		if hidden := m.hiddenIn(ge.units); hidden > 0 {
			p.addNote(hiddenText(hidden))
			p.addBlank()
		}
	}
	m.addGroupFindings(p, ge, w)
	p.gutter = len(strconv.Itoa(maxNo))
	return p
}

// whyNone explains a group without links.
func whyNone(ge *groupEntry) string {
	switch {
	case ge.g.Ungrouped:
		return "no kept link binds this unit to another (ungrouped)"
	case ge.g.UserEdited:
		return "grouped by you (merge or split)"
	}
	return "no links recorded"
}

// addMechanical renders a mechanical group: the example unit, the verified
// count, a compact member list and the units promoted out of the pattern.
// It returns the example's largest line number.
func (m *Model) addMechanical(p *pane, ge *groupEntry, width int) int {
	ex, maxNo := ge.example(), 0
	if m.visible(ex) {
		maxNo = m.addUnit(p, ex)
	} else {
		p.addNote(hiddenText(m.hiddenIn(ge.units)))
	}
	p.addText("×"+strconv.Itoa(ge.count())+" verified identical", textBold)
	var paths []string
	for _, id := range ge.units {
		if id != ex {
			paths = append(paths, diffview.Clean(m.c.units[id].Path))
		}
	}
	if len(paths) > 0 {
		p.addText(memberLine(paths, width), textDim)
	}
	for _, id := range ge.g.Ejected {
		s := "promoted out: " + m.c.unitLabel(id)
		if gi, ok := m.c.groupOf[id]; ok {
			s += " → " + m.c.groups[gi].displayName()
		}
		p.addText(s, textDim)
	}
	p.addBlank()
	return maxNo
}

// memberLine is "members: a · b · c +K more", keeping as many paths as fit
// in width (at least none).
func memberLine(paths []string, width int) string {
	for n := len(paths); n >= 0; n-- {
		s := "members: " + strings.Join(paths[:n], " · ")
		if n < len(paths) {
			if n > 0 {
				s += " "
			}
			s += "+" + strconv.Itoa(len(paths)-n) + " more"
		}
		if n == 0 || ansi.StringWidth(s) <= width {
			return s
		}
	}
	return ""
}

// lineKind is what one by-change list line shows.
type lineKind uint8

const (
	lineGroup       lineKind = iota // cursor, merge mark and group name
	lineGroupMeta                   // counts, bullet, tier and status
	lineGroupHidden                 // dim one-liner: every unit hidden by filters
	lineUngrouped                   // "── ungrouped (N) ──" divider
	lineSplitHead                   // "── split suggestion ──" divider
	lineSplit                       // one split-suggestion file
)

// listLine is one line of the by-change list; idx is the group index, or
// the split file index for lineSplit.
type listLine struct {
	kind lineKind
	idx  int
}

// groupLines lays out the by-change list: groups in order (two lines each,
// one when filters hide all its units), the ungrouped divider before the
// first ungrouped group, then the split suggestion.
func (m *Model) groupLines() []listLine {
	out := make([]listLine, 0, 2*len(m.c.groups)+len(m.c.split)+2)
	first := m.c.firstUngrouped()
	for gi := range m.c.groups {
		if gi == first {
			out = append(out, listLine{kind: lineUngrouped})
		}
		if m.allHidden(gi) {
			out = append(out, listLine{kind: lineGroupHidden, idx: gi})
			continue
		}
		out = append(out, listLine{kind: lineGroup, idx: gi}, listLine{kind: lineGroupMeta, idx: gi})
	}
	if len(m.c.split) > 0 {
		out = append(out, listLine{kind: lineSplitHead})
		for i := range m.c.split {
			out = append(out, listLine{kind: lineSplit, idx: i})
		}
	}
	return out
}

// allHidden reports whether filters hide every unit of group gi.
func (m *Model) allHidden(gi int) bool {
	units := m.c.groups[gi].units
	return len(units) > 0 && m.hiddenIn(units) == len(units)
}

// isGroupLine reports whether l belongs to a group entry.
func (l listLine) isGroupLine() bool {
	return l.kind == lineGroup || l.kind == lineGroupMeta || l.kind == lineGroupHidden
}

// ensureGroupVisible scrolls the list so the selected group's lines (and
// the ungrouped divider right above it) are visible.
func (m *Model) ensureGroupVisible() {
	h := m.layout().bodyH
	lines := m.groupLines()
	if h <= 0 || len(lines) == 0 {
		m.listOff = 0
		return
	}
	first, last := 0, 0
	found := false
	for i, l := range lines {
		if l.isGroupLine() && l.idx == m.sel {
			if !found {
				first, found = i, true
			}
			last = i
		}
	}
	if first > 0 && lines[first-1].kind == lineUngrouped {
		first--
	}
	if first < m.listOff {
		m.listOff = first
	}
	if last >= m.listOff+h {
		m.listOff = last - h + 1
	}
	m.listOff = clamp(m.listOff, 0, max(0, len(lines)-h))
}

// clickGroupList acts on list line i: a group line selects the group, a
// split line jumps to its file's first group.
func (m *Model) clickGroupList(i int) {
	lines := m.groupLines()
	if i < 0 || i >= len(lines) {
		return
	}
	switch l := lines[i]; {
	case l.isGroupLine():
		m.selectEntry(l.idx)
	case l.kind == lineSplit:
		m.jumpSplit(l.idx)
	}
}

// jumpSplit selects the first group with units in split file i.
func (m *Model) jumpSplit(i int) {
	if i < 0 || i >= len(m.c.split) {
		return
	}
	gi := m.c.splitTarget(m.c.split[i])
	if gi < 0 {
		m.note = "split: no group has units in " + diffview.Clean(m.c.split[i].Path)
		return
	}
	m.selectEntry(gi)
	m.ensureListVisible()
}

// groupListLines renders the by-change list, bodyH lines padded to listW.
func (m Model) groupListLines(lay layout) []string {
	out := make([]string, lay.bodyH)
	if lay.listW == 0 {
		return out
	}
	lines := m.groupLines()
	ungrouped := 0
	for i := range m.c.groups {
		if m.c.groups[i].g.Ungrouped {
			ungrouped++
		}
	}
	for i := range out {
		s := ""
		if li := m.listOff + i; li < len(lines) {
			s = m.groupListLine(lines[li], lay.listW, ungrouped)
		}
		out[i] = pad(s, lay.listW)
	}
	return out
}

// groupListLine renders one list line at width w.
func (m Model) groupListLine(l listLine, w, ungrouped int) string {
	rule := func(s string) string {
		return m.st.divider.Render(ansi.Truncate(s+" "+strings.Repeat("─", w), w, ""))
	}
	switch l.kind {
	case lineUngrouped:
		return rule("── ungrouped (" + strconv.Itoa(ungrouped) + ")")
	case lineSplitHead:
		return rule("── split suggestion")
	case lineSplit:
		sf := m.c.split[l.idx]
		prob := " " + ProbText(sf.P)
		path := leftTrunc(diffview.Clean(sf.Path), max(1, w-len("  split? ")-len(prob)))
		return ansi.Truncate(m.st.listItem.Render("  split? "+path)+m.st.prob.Render(prob), w, ellipsis)
	case lineGroupMeta:
		return m.st.sig.Render(fit("    "+m.groupMeta(&m.c.groups[l.idx]), w))
	case lineGroupHidden:
		ge := &m.c.groups[l.idx]
		s := m.groupMarker(l.idx) + ge.displayName() + " — " + strconv.Itoa(len(ge.units)) + " " +
			pluralWord(len(ge.units), "unit") + " hidden by filters"
		return m.st.hidden.Render(fit(diffview.Clean(s), w))
	}
	ge := &m.c.groups[l.idx]
	st := m.st.listItem
	switch {
	case l.idx == m.sel:
		st = m.st.listSel
	case ge.placeholder():
		st = m.st.placeholder
	}
	return st.Render(fit(m.groupMarker(l.idx)+diffview.Clean(ge.displayName()), w))
}

// groupMarker is the two-cell row prefix: "▸" on the cursor row, "*" on the
// group marked for merge.
func (m Model) groupMarker(gi int) string {
	cur, mark := " ", " "
	if gi == m.sel {
		cur = "▸"
	}
	if m.mergeMark != "" && m.c.groups[gi].g.ID == m.mergeMark {
		mark = "*"
	}
	return cur + mark
}

// groupMeta is a list row's second line: "3u 2f · b2 .82 · fast · 2
// findings" ("×5 5f" for a mechanical group; see reviewMeta for the tier
// and status).
func (m Model) groupMeta(ge *groupEntry) string {
	parts := []string{strconv.Itoa(len(ge.units)) + "u " + strconv.Itoa(ge.files) + "f"}
	if ge.mechanical() {
		parts[0] = "×" + strconv.Itoa(ge.count()) + " " + strconv.Itoa(ge.files) + "f"
	}
	if ge.bulletNamed() {
		parts = append(parts, ge.bullet+" "+ProbText(ge.g.NameP))
	}
	return strings.Join(append(parts, reviewMeta(m.c.reviewOf(ge))), " · ")
}

func pluralWord(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// groupTitle is the pane title by change: " name  3 units · 2 files" and
// "marked for merge"; when the list is hidden it starts with "i/n" (group
// names are long, so a trailing position would usually be cut off).
func (m Model) groupTitle(lay layout) string {
	ge := &m.c.groups[m.sel]
	s := " "
	if lay.listW == 0 {
		s += strconv.Itoa(m.sel+1) + "/" + strconv.Itoa(len(m.c.groups)) + " "
	}
	s += diffview.Clean(ge.displayName()) + "  " + ge.countText()
	if m.mergeMark != "" && ge.g.ID == m.mergeMark {
		s += "  * marked for merge"
	}
	return s
}

// groupHeadText renders row k of the group's chip strip.
func (m Model) groupHeadText(k int) string {
	span := m.p.gchipRows[k]
	cs := m.p.gchips[span[0]:span[1]]
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		st := m.st.header
		if c.warn {
			st = m.st.chipWarn
		}
		s := st.Render(diffview.Clean(c.text))
		if c.prob != "" {
			s += " " + m.st.prob.Render(c.prob)
		}
		parts = append(parts, s)
	}
	prefix := "┌ "
	if k > 0 {
		prefix = "│ "
	}
	return m.st.header.Render(prefix) + strings.Join(parts, m.st.sig.Render(chipSep))
}

// textRow renders a rowText line.
func (m Model) textRow(t ptext, width int) string {
	st := m.st.ctx
	switch t.st {
	case textBold:
		st = m.st.header
	case textDim:
		st = m.st.hidden
	case textWarn:
		st = m.st.chipWarn
	}
	return st.Render(fit(diffview.Clean(t.s), width))
}
