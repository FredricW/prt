package codeview

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/diffview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/theme"
)

// Fixed by-change texts.
const (
	// textWhy titles the links box (lower case as before M5, which other
	// packages' snapshot tests match; the design capitalises it).
	textWhy         = "why these belong together"
	textUnits       = "Units · interface first"
	textPlaceholder = "placeholder name (code-derived; no description bullet names this group)"
	textAITitle     = "ai· title written by the fast model (no description bullet names this group)"
	// boxMinW is the narrowest pane that draws inner boxes (why, mechanical
	// preview); narrower panes get a bold title line instead.
	boxMinW = narrowBelow
)

// buildGroupPane lays out group gi (design v3/Groups): the name with the
// bullet it implements, header chips (routing in blue), the stale-labels
// note, the "Why these belong together" box, the visible units in review
// order under "Units · interface first" (a mechanical group: the example,
// then the "Group N · mechanical preview" box), then the group's review
// findings.
func (m *Model) buildGroupPane(gi int) *pane {
	if gi < 0 || gi >= len(m.c.groups) {
		return &pane{}
	}
	ge := &m.c.groups[gi]
	p := m.newPane(ge.paneUnits())
	w := m.layout().paneW
	r := m.c.reviewOf(ge)
	p.addRaw(fit(m.groupName(ge), w))
	p.gchips = append(m.c.groupChips(ge), routingChips(r)...)
	p.gchipRows = wrapChips(p.gchips, w, 0, m.th.Colour)
	for k := range p.gchipRows {
		p.rows = append(p.rows, prow{kind: rowGroupHead, line: int32(k), pair: -1})
	}
	if r != nil && r.Status == model.ReviewStaleLabels && r.StaleNote != "" {
		for _, l := range wrapIndent(glyphStale+" "+r.StaleNote, w, 2) {
			p.addText(l, textDim)
		}
	}
	switch {
	case ge.aiTitle():
		p.addText(textAITitle, textDim)
	case ge.placeholder():
		p.addText(textPlaceholder, textDim)
	}
	p.addBlank()
	why := m.c.whyLines(ge)
	if len(why) == 0 {
		why = []string{whyNone(ge)}
	}
	m.addBox(p, textWhy, textWhy, why, w)
	p.addBlank()
	p.addRaw(m.th.Divider(textUnits, w))
	maxNo := 0
	if ge.mechanical() {
		maxNo = m.addMechanical(p, gi, w)
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
	p.setGutter(maxNo)
	return p
}

// groupName is the pane's first line: the group's name (bold) and, for a
// bullet-named group, ` — implements "<bullet>"` (dim).
func (m *Model) groupName(ge *groupEntry) string {
	s := m.th.Title.Render(diffview.Clean(ge.displayName()))
	if b := strings.TrimSpace(ge.g.DescBullet); b != "" && ge.bulletNamed() && b != strings.TrimSpace(ge.g.Name) {
		s += m.st.sig.Render(` — implements "` + diffview.Clean(b) + `"`)
	}
	return s
}

// addBox appends lines inside a titled box (theme.Box) as pre-rendered
// rows; below boxMinW columns it is a bold title line and the lines
// indented by two cells instead.
func (m *Model) addBox(p *pane, title, plainTitle string, lines []string, w int) {
	if w < boxMinW {
		p.addText(plainTitle, textBold)
		for _, s := range lines {
			p.addText("  "+s, textPlain)
		}
		return
	}
	clean := make([]string, len(lines))
	for i, s := range lines {
		clean[i] = diffview.Clean(s)
	}
	p.addRaw(strings.Split(m.th.Box(title, strings.Join(clean, "\n"), w, theme.BoxOpt{}), "\n")...)
}

// unitTitle is a unit's first row by change (design "Units" box): "n
// path@line" on the left, "role · attention" on the right.
func (m *Model) unitTitle(id string, w int) string {
	u := m.c.units[id]
	n := 0
	if gi, ok := m.c.groupOf[id]; ok {
		for i, uid := range m.c.groups[gi].units {
			if uid == id {
				n = i + 1
			}
		}
	}
	left := m.st.sig.Render(strconv.Itoa(n)) + " " + m.th.Bold.Render(diffview.Clean(u.Path)+"@"+strconv.Itoa(unitStart(u)))
	var right []string
	if role := m.c.unitRole(id); role != "" {
		right = append(right, role)
	}
	if a := m.c.aggs[id]; a != nil && a.Attention != "" && m.c.labelled {
		right = append(right, string(a.Attention))
	}
	return theme.Spread(left, m.st.sig.Render(strings.Join(right, " · ")), w)
}

// unitRole is a unit's place in the review order: "interface" (its
// interface label says yes), "test" (its file's role is test), else
// "caller"; "" without labels, and "" (or "test") while the unit's labels
// are being fetched (M5.2), so it never claims "caller" too early.
func (c *content) unitRole(id string) string {
	if !c.labelled {
		return ""
	}
	u := c.units[id]
	if !c.labelsKnown(id) {
		if c.roles[u.Path] == model.RoleTest {
			return "test"
		}
		return ""
	}
	for _, l := range c.labels {
		if l.UnitID == id && l.Line == 0 && l.Question == model.QInterface && currentValue(l) == model.LabelYes {
			return "interface"
		}
	}
	if c.roles[u.Path] == model.RoleTest {
		return "test"
	}
	return "caller"
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

// addMechanical renders mechanical group gi: the example unit, then the
// "Group N · mechanical preview" box with the verified count, a compact
// member list and the units promoted out of the pattern. It returns the
// example's largest line number.
func (m *Model) addMechanical(p *pane, gi, width int) int {
	ge := &m.c.groups[gi]
	ex, maxNo := ge.example(), 0
	if m.visible(ex) {
		maxNo = m.addUnit(p, ex)
	} else {
		p.addNote(hiddenText(m.hiddenIn(ge.units)))
	}
	inner := width
	if width >= boxMinW {
		inner = width - 4
	}
	lines := []string{"×" + strconv.Itoa(ge.count()) + " verified identical"}
	var paths []string
	for _, id := range ge.units {
		if id != ex {
			paths = append(paths, diffview.Clean(m.c.units[id].Path))
		}
	}
	if len(paths) > 0 {
		lines = append(lines, memberLine(paths, inner))
	}
	for _, id := range ge.g.Ejected {
		s := "promoted out: " + m.c.unitLabel(id)
		if gj, ok := m.c.groupOf[id]; ok {
			s += " → " + m.c.groups[gj].displayName()
		}
		lines = append(lines, s)
	}
	m.addBox(p, "Group "+strconv.Itoa(gi+1)+" · mechanical preview", "mechanical preview", lines, width)
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
	lineByHead      lineKind = iota // "by: file [change] · interfaces → callers → tests"
	lineGroup                       // cursor, merge mark, number and group name
	lineGroupMeta                   // counts, bullet, tier and status
	lineGroupHidden                 // dim one-liner: every unit hidden by filters
	lineUngrouped                   // "── ungrouped (N) ──" divider
	lineSplitHead                   // "── split suggestion: groups … ──" divider
	lineSplit                       // one split-suggestion file
)

// listLine is one line of the by-change list; idx is the group index, or
// the split file index for lineSplit.
type listLine struct {
	kind lineKind
	idx  int
}

// groupLines lays out the by-change list: the "by:" line, groups in order
// (two lines each, one when filters hide all its units), the ungrouped
// divider before the first ungrouped group, then the split suggestion.
func (m *Model) groupLines() []listLine {
	out := make([]listLine, 0, 2*len(m.c.groups)+len(m.c.split)+3)
	out = append(out, listLine{kind: lineByHead})
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
// the ungrouped divider or the "by:" line right above it) are visible.
func (m *Model) ensureGroupVisible() {
	h := m.layout().listH
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
	if first > 0 && (lines[first-1].kind == lineUngrouped || lines[first-1].kind == lineByHead) {
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

// groupListLines renders the by-change list, listH lines padded to listIn.
func (m Model) groupListLines(lay layout) []string {
	out := make([]string, lay.listH)
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
			s = m.groupListLine(lines[li], lay.listIn, ungrouped)
		}
		out[i] = pad(s, lay.listIn)
	}
	return out
}

// groupListLine renders one list line at width w.
func (m Model) groupListLine(l listLine, w, ungrouped int) string {
	rule := func(s string) string {
		return m.st.divider.Render(ansi.Truncate(s+" "+strings.Repeat("─", w), w, ""))
	}
	switch l.kind {
	case lineByHead:
		return fit(m.byHead(), w)
	case lineUngrouped:
		return rule("── ungrouped (" + strconv.Itoa(ungrouped) + ")")
	case lineSplitHead:
		return rule("── " + m.splitHead())
	case lineSplit:
		sf := m.c.split[l.idx]
		prob := " " + ProbText(sf.P)
		path := leftTrunc(diffview.Clean(sf.Path), max(1, w-len("  split? ")-len(prob)))
		return ansi.Truncate(m.st.listItem.Render("  split? "+path)+m.st.prob.Render(prob), w, ellipsis)
	case lineGroupMeta:
		return fit("    "+m.groupMetaStyled(&m.c.groups[l.idx]), w)
	case lineGroupHidden:
		ge := &m.c.groups[l.idx]
		s := m.groupMarker(l.idx) + strconv.Itoa(l.idx+1) + " " + ge.displayName() + " — " +
			strconv.Itoa(len(ge.units)) + " " + pluralWord(len(ge.units), "unit") + " hidden by filters"
		return m.st.hidden.Render(fit(diffview.Clean(s), w))
	}
	ge := &m.c.groups[l.idx]
	st := m.th.Bright
	switch {
	case l.idx == m.sel:
		st = m.st.listSel
	case ge.placeholder():
		st = m.st.placeholder
	}
	num := m.st.sig
	if l.idx == m.sel {
		num = m.th.Accent
	}
	marker := m.groupMarker(l.idx)
	return fit(st.Render(marker)+num.Render(strconv.Itoa(l.idx+1))+st.Render(" "+diffview.Clean(ge.displayName())), w)
}

// byHead is the list's first line: "by: file [change] · interfaces →
// callers → tests" (with colour the active mode is a filled blue chip).
func (m Model) byHead() string {
	tail := m.st.sig.Render(" · interfaces → callers → tests")
	if !m.th.Colour {
		return "by: file [change]" + tail
	}
	on := m.r.NewStyle().Background(m.th.P.Blue).Foreground(m.th.P.Bg)
	return m.st.sig.Render("by: ") + m.st.sig.Render("file ") + on.Render(" change ") + tail
}

// splitHead is the split suggestion's divider text: "split suggestion:
// groups 4 · 6", naming the groups the suggested files jump to.
func (m Model) splitHead() string {
	seen := map[int]bool{}
	var nums []string
	for _, sf := range m.c.split {
		if gi := m.c.splitTarget(sf); gi >= 0 && !seen[gi] {
			seen[gi] = true
			nums = append(nums, strconv.Itoa(gi+1))
		}
	}
	if len(nums) == 0 {
		return "split suggestion:"
	}
	return "split suggestion: " + pluralWord(len(nums), "group") + " " + strings.Join(nums, " · ")
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
// and status, "deep · ⚑ stale labels" after an override).
func (m Model) groupMeta(ge *groupEntry) string {
	return strings.Join([]string{m.groupFacts(ge), reviewMeta(m.c.reviewOf(ge))}, " · ")
}

// groupFacts is the counts and bullet part of groupMeta.
func (m Model) groupFacts(ge *groupEntry) string {
	parts := []string{strconv.Itoa(len(ge.units)) + "u " + strconv.Itoa(ge.files) + "f"}
	if ge.mechanical() {
		parts[0] = "×" + strconv.Itoa(ge.count()) + " " + strconv.Itoa(ge.files) + "f"
	}
	if ge.bulletNamed() {
		parts = append(parts, ge.bullet+" "+ProbText(ge.g.NameP))
	}
	return strings.Join(parts, " · ")
}

// groupMetaStyled is groupMeta drawn dim with the tier in its colour
// (v3/Pipeline: fast green, deep accent, escalated warm); a stale review
// stays dim.
func (m Model) groupMetaStyled(ge *groupEntry) string {
	r := m.c.reviewOf(ge)
	tier := m.st.sig
	if r != nil && r.Status != model.ReviewStaleLabels {
		tier = m.th.Tier(r.Tier, r.EscalatedOf != nil)
	}
	return m.st.sig.Render(diffview.Clean(m.groupFacts(ge))+" · ") + tier.Render(diffview.Clean(reviewMeta(r)))
}

func pluralWord(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// groupTitle is the pane title by change: "name  3 units · 2 files" and
// "marked for merge"; right is "group i of n" in a box, and without the
// list the title starts with "i/n" (group names are long, so a trailing
// position would usually be cut off).
func (m Model) groupTitle(lay layout) (string, string) {
	ge := &m.c.groups[m.sel]
	s := ""
	if lay.listW == 0 {
		s += strconv.Itoa(m.sel+1) + "/" + strconv.Itoa(len(m.c.groups)) + " "
	}
	s += diffview.Clean(ge.displayName()) + "  " + ge.countText()
	if m.mergeMark != "" && ge.g.ID == m.mergeMark {
		s += "  * marked for merge"
	}
	if lay.boxed {
		return s, "group " + strconv.Itoa(m.sel+1) + " of " + strconv.Itoa(len(m.c.groups))
	}
	return s, ""
}

// groupHeadText renders row k of the group's chip strip.
func (m Model) groupHeadText(k int) string {
	span := m.p.gchipRows[k]
	return m.chipsText(m.p.gchips[span[0]:span[1]])
}

// textRow renders a rowText line.
func (m Model) textRow(t ptext, width int) string {
	st := m.st.ctx
	switch t.st {
	case textRaw:
		return ansi.Truncate(t.s, width, ellipsis)
	case textBold:
		st = m.st.header
	case textDim:
		st = m.st.hidden
	case textWarn:
		st = m.st.chipWarn
	}
	return st.Render(fit(diffview.Clean(t.s), width))
}
