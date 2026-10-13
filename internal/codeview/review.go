package codeview

import (
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/comments"
	"github.com/fredricw/prt/internal/diffview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/theme"
)

// Review texts (M3: routing table rows and findings).
const (
	textNotRouted  = "not routed"
	textNoFindings = "findings: none"
	findingGlyph   = "!"
	// glyphStale marks a review whose labels were overridden since it ran
	// (status stale-labels; M5).
	glyphStale = "⚑"
	// barFinding is the by-change findings block's left bar with colour
	// (findingGlyph without).
	barFinding     = "▌"
	noteReReviewOK = "re-review on deep requested"
	reReviewPrefix = "re-review: "
	sevWidth       = len("medium")
	findingIndent  = 2 // inline findings: glyph + space; by change: continuation indent
)

// indexReviews fills c.reviews and c.unitFindings from the routing table.
// A finding names its unit by UnitID; one without a known UnitID is
// anchored to the group's unit whose new-side range covers its path and
// line, else it stays unanchored (shown last, on the group only).
func indexReviews(c *content, rows []model.Review) {
	c.reviews = make(map[string]*model.Review, len(rows))
	c.unitFindings = map[string][]model.Finding{}
	c.unitInline = map[string][]model.Finding{}
	for i := range rows {
		r := &rows[i]
		c.reviews[r.GroupID] = r
		for _, f := range r.Findings {
			if id := c.findingUnit(r.GroupID, f); id != "" {
				c.unitFindings[id] = append(c.unitFindings[id], f)
				if !c.findingDone[comments.FindingKey(c.itemID, r.GroupID, f)] {
					c.unitInline[id] = append(c.unitInline[id], f)
				}
			}
		}
	}
	for _, m := range []map[string][]model.Finding{c.unitFindings, c.unitInline} {
		for id, fs := range m {
			sortFindingsByLine(fs)
			m[id] = fs
		}
	}
}

// findingUnit returns the unit a finding is anchored to, or "".
func (c *content) findingUnit(groupID string, f model.Finding) string {
	if f.UnitID != "" && c.units[f.UnitID] != nil {
		return f.UnitID
	}
	if f.Line <= 0 || c.groupOf == nil {
		return ""
	}
	gi := c.groupIndex(groupID)
	if gi < 0 {
		return ""
	}
	for _, id := range c.groups[gi].units {
		u := c.units[id]
		if u.Path == f.Path && f.Line >= u.Range.New.Start && f.Line < u.Range.New.Start+max(1, u.Range.New.Count) {
			return id
		}
	}
	return ""
}

// sortFindingsByLine orders findings by line, unanchored (line 0) last.
func sortFindingsByLine(fs []model.Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		li, lj := fs[i].Line, fs[j].Line
		if (li > 0) != (lj > 0) {
			return li > 0
		}
		return li < lj
	})
}

// hasFindings reports whether unit id has at least one anchored finding
// (the "has findings" filter).
func (c *content) hasFindings(id string) bool { return len(c.unitFindings[id]) > 0 }

// reviewOf returns the routing row of group ge, nil when it was not routed.
func (c *content) reviewOf(ge *groupEntry) *model.Review { return c.reviews[ge.g.ID] }

// tierText is a row's tier in words: "fast", "deep (forced)", "deep←fast
// .42" for an escalation, "none" and "none (skipped)" when the user
// skipped it.
func tierText(r *model.Review) string {
	switch {
	case r.Tier == model.TierNone && r.UserSkipped:
		return "none (skipped)"
	case r.Tier == "":
		return "—"
	case r.EscalatedOf != nil:
		return string(r.Tier) + "←" + string(*r.EscalatedOf) + " " + ProbText(r.FastConf)
	case r.Forced:
		return string(r.Tier) + " (forced)"
	}
	return string(r.Tier)
}

// overCap reports whether a queued row waits for the cost cap.
func overCap(r *model.Review) bool { return strings.Contains(r.QueueNote, "cap") }

// statusText is a row's status for the list: a done row shows its
// findings count ("2 findings") or "done"; a queued row over the cap
// "queued (over cap)".
func statusText(r *model.Review) string {
	switch r.Status {
	case model.ReviewDone:
		if n := len(r.Findings); n > 0 {
			return plural(n, "finding")
		}
		return "done"
	case model.ReviewQueued:
		if overCap(r) {
			return "queued (over cap)"
		}
		return "queued"
	case model.ReviewStaleLabels:
		return glyphStale + " stale labels"
	case model.ReviewStopped:
		return "stopped" // the user stopped the run (M5.1); not a failure
	case "":
		return "pending"
	}
	return string(r.Status)
}

// reviewMeta is the tier and status part of a by-change list row: "fast ·
// 2 findings", "deep←fast .42 · done", "deep (forced) · running", "none",
// "none (skipped)", "fast · queued (over cap)", "deep · failed", or "not
// routed" without a row.
func reviewMeta(r *model.Review) string {
	if r == nil {
		return textNotRouted
	}
	if r.Tier == model.TierNone {
		return tierText(r)
	}
	return tierText(r) + " · " + statusText(r)
}

// routingChips are the group header's routing chips: "routed: <tier> ·
// <specialist prompts>" (blue; "· ⚑ stale labels" after an override), the
// rule that fired in words and the context kinds attached.
func routingChips(r *model.Review) []chip {
	if r == nil {
		return []chip{{text: "routing: " + textNotRouted}}
	}
	routed := "routed: " + tierText(r)
	for _, p := range r.Prompts {
		routed += " · " + p
	}
	if r.Status == model.ReviewStaleLabels {
		routed += " · " + statusText(r)
	}
	cs := []chip{{text: routed, kind: theme.ChipRoute}}
	if r.RuleWords != "" {
		cs = append(cs, chip{text: "routing: " + r.RuleWords})
	}
	if len(r.Context) > 0 {
		cs = append(cs, chip{text: "context: " + strings.Join(r.Context, ", ")})
	}
	if len(r.ContextNone) > 0 {
		cs = append(cs, chip{text: "no context found: " + strings.Join(r.ContextNone, ", ")})
	}
	return cs
}

// notReviewedWhy is the status inside "not reviewed (…)".
func notReviewedWhy(r *model.Review) string {
	switch {
	case r == nil:
		return textNotRouted
	case r.Tier == model.TierNone && r.UserSkipped:
		return "skipped by you"
	case r.Tier == model.TierNone:
		return "tier none"
	case r.Status == model.ReviewQueued && overCap(r):
		return "queued, over cap"
	}
	return statusText(r)
}

// groupFindings orders a row's findings for the by-change block: anchored
// ones in the group's unit order, then by line; unanchored ones last.
func (c *content) groupFindings(ge *groupEntry, r *model.Review) []model.Finding {
	pos := make(map[string]int, len(ge.units))
	for i, id := range ge.units {
		pos[id] = i
	}
	type keyed struct {
		f    model.Finding
		unit int // position in ge.units, len(ge.units) when unanchored
	}
	ks := make([]keyed, 0, len(r.Findings))
	for _, f := range r.Findings {
		k := keyed{f: f, unit: len(ge.units)}
		if i, ok := pos[c.findingUnit(ge.g.ID, f)]; ok {
			k.unit = i
		}
		ks = append(ks, k)
	}
	sort.SliceStable(ks, func(i, j int) bool {
		if ks[i].unit != ks[j].unit {
			return ks[i].unit < ks[j].unit
		}
		return ks[i].f.Line < ks[j].f.Line
	})
	out := make([]model.Finding, len(ks))
	for i, k := range ks {
		out[i] = k.f
	}
	return out
}

// findingLoc is "path:line", or the path alone when unanchored.
func findingLoc(f model.Finding) string {
	if f.Line <= 0 {
		return f.Path
	}
	return f.Path + ":" + strconv.Itoa(f.Line)
}

// sevText pads a severity to the widest one, so claims line up.
func sevText(s model.Severity) string {
	t := string(s)
	if t == "" {
		t = "?"
	}
	return t + strings.Repeat(" ", max(0, sevWidth-len(t)))
}

// wrapIndent word-wraps s so every line fits width cells once lines after
// the first are indented by indent spaces (all lines wrap at
// width−indent, which keeps the wrap a single pass).
func wrapIndent(s string, width, indent int) []string {
	indent = max(0, min(indent, width/2))
	lines := strings.Split(ansi.Wrap(s, max(4, width-indent), ""), "\n")
	pre := strings.Repeat(" ", indent)
	for i := 1; i < len(lines); i++ {
		lines[i] = pre + strings.TrimLeft(lines[i], " ")
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return lines
}

// addGroupFindings appends the by-change findings block of group ge: each
// finding as "<sev> path:line  claim" (wrapped) plus a dim "fix: …" line;
// "findings: none" for a finished review without findings, "findings: not
// reviewed (<status>)" otherwise; then the calibration result.
func (m *Model) addGroupFindings(p *pane, ge *groupEntry, width int) {
	r := m.c.reviewOf(ge)
	var fs []model.Finding
	if r != nil {
		fs = m.c.groupFindings(ge, r)
	}
	switch {
	case len(fs) > 0:
		head := "findings (" + strconv.Itoa(len(fs)) + ")"
		if r.Status != model.ReviewDone {
			head += " · " + notReviewedWhy(r)
		}
		p.addText(head, textBold)
		for _, f := range fs {
			m.addFindingText(p, r, f, width)
		}
	case r != nil && r.Status == model.ReviewStaleLabels:
		p.addText(textNoFindings+" · "+statusText(r), textDim)
	case r != nil && r.Status == model.ReviewDone && r.Tier != model.TierNone:
		p.addText(textNoFindings, textDim)
	default:
		p.addText("findings: not reviewed ("+notReviewedWhy(r)+")", textDim)
	}
	if r == nil {
		return
	}
	if r.Status != model.ReviewDone && r.QueueNote != "" {
		p.addText("  "+r.QueueNote, textDim)
	}
	if r.Err != "" {
		p.addText("  error: "+r.Err, textDim)
	}
	if cal := r.Calibration; cal != nil {
		p.addText(calibrationText(cal), textDim)
	}
}

// calibrationText is "deep check: agree" or "deep check: N missed".
func calibrationText(c *model.Calibration) string {
	if c.Agree || c.Missed == 0 {
		return "deep check: agree"
	}
	return "deep check: " + strconv.Itoa(c.Missed) + " missed"
}

// addFindingText appends one finding of the by-change block behind a left
// bar (red "▌" with colour, "!" without; design "rv" block): a head
// "<severity> · <tier> · <specialist prompts> · <seconds>" (severity red,
// the rest dim), the location and claim (wrapped), then "fix: …" dim.
func (m *Model) addFindingText(p *pane, r *model.Review, f model.Finding, width int) {
	bar := findingGlyph
	if m.th.Colour {
		bar = barFinding
	}
	bar = m.st.findBar.Render(bar) + " "
	w := max(8, width-2)
	claimSt := m.st.ctx
	if f.Severity == model.SevHigh {
		claimSt = m.st.chipWarn
	}
	head := m.st.findBar.Bold(true).Render(sevWord(f.Severity)) + m.st.sig.Render(" · "+reviewHead(r))
	p.addRaw(fit(bar+head, width))
	for _, l := range wrapIndent(findingLoc(f)+"  "+f.Claim, w, findingIndent) {
		p.addRaw(bar + claimSt.Render(diffview.Clean(l)))
	}
	if f.Fix != "" {
		for _, l := range wrapIndent("fix: "+f.Fix, w, len("fix: ")) {
			p.addRaw(bar + m.st.hidden.Render(diffview.Clean(l)))
		}
	}
}

// reviewHead is the review part of a finding's head, after its severity:
// "deep · security · 7.2s" (tier, specialist prompts, wall time when
// known).
func reviewHead(r *model.Review) string {
	parts := []string{tierText(r)}
	parts = append(parts, r.Prompts...)
	if r.Ms > 0 {
		parts = append(parts, strconv.FormatFloat(float64(r.Ms)/1000, 'f', 1, 64)+"s")
	}
	return strings.Join(parts, " · ")
}

// fline is one row of an inline finding (by file).
type fline struct {
	s    string
	lead bool // first row: drawn with the "!" gutter glyph
	fix  bool // the dim "fix: …" rows
	high bool // severity high (alert style)
}

// addFindingRows appends the inline rows of findings fs to unit ui:
// "! [high] claim" wrapped at the pane width, then "  fix: …".
func (m *Model) addFindingRows(p *pane, ui int32, fs []model.Finding, width int) {
	w := max(8, width-findingIndent)
	for _, f := range fs {
		high := f.Severity == model.SevHigh
		tag := "[" + strings.TrimSpace(sevText(f.Severity)) + "] "
		loc := ""
		if f.Line <= 0 {
			loc = findingLoc(f) + "  "
		}
		for i, l := range wrapIndent(tag+loc+f.Claim, w, len(tag)) {
			p.addFline(ui, fline{s: l, lead: i == 0, high: high})
		}
		if f.Fix != "" {
			for _, l := range wrapIndent("fix: "+f.Fix, w, len("fix: ")) {
				p.addFline(ui, fline{s: l, fix: true})
			}
		}
	}
}

// addFline appends one inline finding row.
func (p *pane) addFline(ui int32, f fline) {
	p.rows = append(p.rows, prow{kind: rowFinding, unit: ui, line: int32(len(p.flines)), pair: -1})
	p.flines = append(p.flines, f)
}

// findingRow renders an inline finding row.
func (m Model) findingRow(f fline, width int) string {
	glyph := " "
	if f.lead {
		st := m.th.Accent
		if f.high {
			st = m.th.Del.Bold(true)
		}
		glyph = st.Render(findingGlyph)
	}
	st := m.st.ctx
	switch {
	case f.fix:
		st = m.st.hidden
	case f.high:
		st = m.st.chipWarn
	}
	return ansi.Truncate(glyph+" "+st.Render(f.s), width, ellipsis)
}

// splitFindings splits a unit's findings into those drawn under one of its
// diff lines (by new-side line number) and the rest, drawn under its header.
func splitFindings(fs []model.Finding, newLines map[int]bool) (map[int][]model.Finding, []model.Finding) {
	at := map[int][]model.Finding{}
	var rest []model.Finding
	for _, f := range fs {
		if f.Line > 0 && newLines[f.Line] {
			at[f.Line] = append(at[f.Line], f)
			continue
		}
		rest = append(rest, f)
	}
	return at, rest
}

// PendingReview returns (once) the group the last R asked to re-review on
// the deep tier; ok is false when there is none. The component cannot run
// commands, so the root model sends client.ReviewAction.
func (m *Model) PendingReview() (string, bool) {
	if m.pendingReview == "" {
		return "", false
	}
	id := m.pendingReview
	m.pendingReview = ""
	return id, true
}

// reReviewKey implements ReReview: by change on the selected group, by file
// on the group of the unit under the cursor. Refused with a note when there
// is no group, the group has no routing row, or it is a mechanical group
// the rules routed to none.
func (m *Model) reReviewKey() {
	gi := -1
	if m.byChange() {
		gi = m.sel
	} else if id := m.cursorUnitID(); id != "" {
		if g, ok := m.c.groupOf[id]; ok {
			gi = g
		}
	}
	if gi < 0 {
		m.note = reReviewPrefix + "no group here (groups arrive with analysis)"
		return
	}
	ge := &m.c.groups[gi]
	r := m.c.reviewOf(ge)
	switch {
	case r == nil:
		m.note = reReviewPrefix + ge.displayName() + " is not routed yet"
		return
	case ge.g.Mechanical && r.Tier == model.TierNone && !r.UserSkipped:
		m.note = reReviewPrefix + "mechanical group routed to none (verified identical repeats)"
		return
	}
	m.pendingReview = ge.g.ID
	m.note = noteReReviewOK
}
