package pipeview

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
)

// tierBarCells is the width of the tier mix bar.
const tierBarCells = 24

// tierOrder and tierGlyph draw the colour-free tier mix bar.
var (
	tierOrder = []model.Tier{model.TierNone, model.TierFast, model.TierDeep}
	tierGlyph = map[model.Tier]string{model.TierNone: "n", model.TierFast: "f", model.TierDeep: "d"}
)

// statusOrder lists review statuses for the totals' status line.
var statusOrder = []model.ReviewStatus{
	model.ReviewDone, model.ReviewRunning, model.ReviewQueued, model.ReviewFailed,
	model.ReviewSkipped, model.ReviewStaleLabels,
}

// linkOrder lists structural link kinds for the grouping panel.
var linkOrder = []model.LinkKind{
	model.LinkCalls, model.LinkTests, model.LinkImports, model.LinkMoved, model.LinkSameSymbol,
	model.LinkMechanical, model.LinkEmbedNeighbor,
}

// paras wraps each logical line to w cells (continuation lines indented by
// two spaces).
func paras(w int, lines ...string) []string {
	var out []string
	for _, l := range lines {
		out = append(out, wrap(l, w, 2)...)
	}
	return out
}

// pack joins parts with sep into as few lines of w cells as it can,
// starting a new line before a part that does not fit (a part wider than
// w is wrapped on its own).
func pack(w int, sep string, parts ...string) []string {
	var out []string
	cur := ""
	for _, p := range parts {
		switch {
		case cur == "":
			cur = p
		case ansi.StringWidth(cur+sep+p) <= w:
			cur += sep + p
		default:
			out = append(out, wrap(cur, w, 2)...)
			cur = p
		}
	}
	if cur != "" {
		out = append(out, wrap(cur, w, 2)...)
	}
	return out
}

// totalsLines is the totals block under the routing box (v3: tier mix,
// time vs all-deep, cost of cap, jev calls on one line), then the status
// counts and the review note.
func (m *Model) totalsLines(w int) []string {
	var rs *model.ReviewStats
	if m.d.Agg != nil {
		rs = m.d.Agg.Review
	}
	if rs == nil {
		return []string{m.st.dim.Render("totals: no review data yet")}
	}
	sep := m.st.dim.Render("  ·  ")
	mix := m.st.dim.Render("tier mix ") + m.tierBarStyled(rs.Tiers) + " " + m.st.dim.Render(tierLegend(rs.Tiers))
	elapsed := "—"
	if rs.ElapsedMs > 0 {
		elapsed = dur(rs.ElapsedMs)
	}
	timeS := m.st.dim.Render("time ") + m.st.title.Render(elapsed) + m.st.dim.Render(" parallel vs "+estimate(rs.AllDeepEstMs)+" all-deep sequential")
	cost := m.st.dim.Render("cost ") + m.st.title.Render(money(rs.Cost))
	if rs.CapUSD > 0 {
		cost += " of " + money(rs.CapUSD) + " cap"
	} else {
		cost += m.st.dim.Render(" (no cap)")
	}
	if rs.OverCap > 0 {
		cost += m.st.warn.Render(fmt.Sprintf(" · %d queued over cap", rs.OverCap)) + m.st.dim.Render(" ($ raises it)")
	}
	jev := fmt.Sprintf("jev %s · %s", plural(rs.JevCalls, "call", "calls"), money(rs.JevCost))
	if rs.JevCached {
		jev += m.st.dim.Render(" (cached)")
	}
	out := pack(w, sep, mix, timeS, cost, jev)
	var st []string
	for _, s := range statusOrder {
		if n := rs.Status[s]; n > 0 {
			st = append(st, fmt.Sprintf("%s %d", s, n))
		}
	}
	counts := plural(rs.Groups, "group", "groups")
	if len(st) > 0 {
		counts += ": " + strings.Join(st, " · ")
	}
	out = append(out, paras(w, m.st.dim.Render(fmt.Sprintf("%s · escalations %d · calibration samples %d · findings %d",
		counts, rs.Escalations, rs.Calibrations, rs.Findings)))...)
	if rs.Note != "" {
		out = append(out, paras(w, m.st.note.Render("note: "+rs.Note))...)
	}
	return out
}

// tierBarStyled is the tier mix bar: letters without colour (tierBar),
// tier-coloured blocks with colour.
func (m *Model) tierBarStyled(tiers map[model.Tier]int) string {
	if !m.st.th.Colour {
		return tierBar(tiers)
	}
	shares := make([]int, len(tierOrder))
	for i, t := range tierOrder {
		shares[i] = tiers[t]
	}
	var sb strings.Builder
	for i, n := range cellWidths(shares, tierBarCells) {
		sb.WriteString(m.st.th.Tier(tierOrder[i], false).Render(strings.Repeat("█", n)))
	}
	if sb.Len() == 0 {
		return "—"
	}
	return sb.String()
}

// tierBar draws the none/fast/deep shares as letters (n/f/d); "—" when no
// row has a tier.
func tierBar(tiers map[model.Tier]int) string {
	shares := make([]int, len(tierOrder))
	for i, t := range tierOrder {
		shares[i] = tiers[t]
	}
	widths := cellWidths(shares, tierBarCells)
	var sb strings.Builder
	for i, n := range widths {
		sb.WriteString(strings.Repeat(tierGlyph[tierOrder[i]], n))
	}
	if sb.Len() == 0 {
		return "—"
	}
	return sb.String()
}

// tierLegend is "n none 2 · f fast 5 · d deep 3".
func tierLegend(tiers map[model.Tier]int) string {
	parts := make([]string, len(tierOrder))
	for i, t := range tierOrder {
		parts[i] = fmt.Sprintf("%s %s %d", tierGlyph[t], t, tiers[t])
	}
	return strings.Join(parts, " · ")
}

// rulesLines is the rules box from Detail.Routing: one "if … → tier" line
// per rule with its prompts, context and fired rows, then the knobs.
func (m *Model) rulesLines(w int) []string {
	ri := m.d.Routing
	if ri == nil {
		return []string{m.st.dim.Render("no routing rules loaded yet")}
	}
	fired := map[int]int{}
	for _, rw := range m.rows {
		fired[rw.rev.RuleIndex]++
	}
	var out []string
	for _, r := range ri.Rules {
		words := r.Words
		if words == "" {
			words = fmt.Sprintf("if %s → %s", r.When, r.Tier)
		}
		var extra []string
		if len(r.Prompts) > 0 {
			extra = append(extra, "+"+strings.Join(r.Prompts, ",+"))
		}
		if len(r.Context) > 0 {
			extra = append(extra, "ctx "+strings.Join(r.Context, ","))
		}
		extra = append(extra, plural(fired[r.Index], "row", "rows"))
		out = append(out, paras(w, fmt.Sprintf("%d %s", r.Index, words)+m.st.dim.Render("  "+strings.Join(extra, " · ")))...)
		if r.Err != "" {
			out = append(out, paras(w, m.st.warn.Render("  ✗ "+r.Err))...)
		}
	}
	if len(ri.Rules) == 0 {
		out = append(out, m.st.dim.Render("no rules"))
	}
	if n := fired[0]; n > 0 {
		out = append(out, paras(w, m.st.dim.Render("– "+plural(n, "row", "rows")+" decided outside the rules (forced, skipped or labels unknown)"))...)
	}
	out = append(out, paras(w, m.st.dim.Render(fmt.Sprintf("deep_slots %d · cost_cap_usd %s · escalate_below %s · calibration_sample %s",
		ri.DeepSlots, money(ri.CostCapUSD), prob(ri.EscalateBelow), prob(ri.CalibrationSample))))...)
	src := "source " + ri.Source
	if ri.Path != "" && ri.Path != ri.Source {
		src += " · e edits " + ri.Path
	}
	out = append(out, paras(w, m.st.dim.Render(src))...)
	for _, wn := range ri.Warnings {
		out = append(out, paras(w, m.st.warn.Render("warning: "+wn))...)
	}
	if ri.Err != "" {
		out = append(out, paras(w, m.st.warn.Render("reload failed: "+ri.Err+" (previous rules stay active)"))...)
	}
	return out
}

// groupingLines is the grouping box from Agg.Grouping: units → links →
// groups, as in v3.
func (m *Model) groupingLines(w int) []string {
	var g *model.GroupingStats
	if m.d.Agg != nil {
		g = m.d.Agg.Grouping
	}
	if g == nil {
		return []string{m.st.dim.Render("no grouping data yet")}
	}
	structural := "none"
	if s := joinCounts(linkCounts(g.Structural)); s != "" {
		structural = s
	}
	kept := "none"
	if s := joinCounts(byCount(g.JevKept)); s != "" {
		kept = s
	}
	out := paras(w,
		fmt.Sprintf("%d units → ", g.Units)+m.st.title.Render(plural(g.Groups, "group", "groups"))+fmt.Sprintf(" + %d ungrouped", g.Ungrouped),
		m.st.dim.Render("structural "+structural),
		m.st.dim.Render(fmt.Sprintf("embed candidates %d · mechanical candidates %d", g.EmbedCandidates, g.MechanicalCandidates)),
		m.st.dim.Render(fmt.Sprintf("jev asked %s · rejected %d · cut %d", plural(g.PairsAsked, "pair", "pairs"), g.JevRejected, g.CutLinks)),
		m.st.dim.Render("kept "+kept),
		fmt.Sprintf("mechanical %d (%d units collapsed) · ejected %d · user edits %d", g.Mechanical, g.MechanicalCollapsed, g.Ejected, g.UserEdits),
	)
	if g.EmbedNote != "" {
		out = append(out, paras(w, m.st.note.Render("embed: "+g.EmbedNote))...)
	}
	return out
}

// linkCounts lists structural link counts in linkOrder, then other kinds
// by name.
func linkCounts(m map[model.LinkKind]int) []countPair {
	var out []countPair
	seen := map[model.LinkKind]bool{}
	for _, k := range linkOrder {
		seen[k] = true
		if n := m[k]; n > 0 {
			out = append(out, countPair{string(k), n})
		}
	}
	var rest []string
	for k, n := range m {
		if !seen[k] && n > 0 {
			rest = append(rest, string(k))
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		out = append(out, countPair{k, m[model.LinkKind(k)]})
	}
	return out
}

// lastAt formats CalibrationStats.LastAt (RFC3339) like other timestamps;
// an unparsable value is shown as is.
func lastAt(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return clock(t)
}

// urgencyLines is the urgency weights box (spec Ranking: "Weights live in
// prt.toml and are shown on the Pipeline page"): each stored term as words
// weight×value = contribution, then the stored total and "why". It reads
// ItemAgg.UrgencyTerms only; the Overview's `w` overlay shows the same
// terms as a table.
func (m *Model) urgencyLines(w int) []string {
	if m.d.Agg == nil || len(m.d.Agg.UrgencyTerms) == 0 {
		return []string{m.st.dim.Render("no urgency terms yet")}
	}
	parts := make([]string, 0, len(m.d.Agg.UrgencyTerms))
	for _, t := range m.d.Agg.UrgencyTerms {
		parts = append(parts, fmt.Sprintf("%s %.2f×%.2f=%+.2f", t.Words, t.Weight, t.Value, t.Contrib))
	}
	total := fmt.Sprintf("total %+.2f", m.d.Agg.Urgency)
	if m.d.Agg.Why != "" {
		total += " · why: " + m.d.Agg.Why
	}
	return paras(w, strings.Join(parts, " · "), total)
}

// calibrationLines is the calibration box (spec Pipeline page 6, Ranking
// "Calibration metrics"): the last sample's agreement, the fast-tier miss
// rate over 30 days, and the override rate per question with questions
// over FlagAbove flagged as rewording candidates.
func (m *Model) calibrationLines(w int) []string {
	var c *model.CalibrationStats
	if m.d.Agg != nil {
		c = m.d.Agg.Calibration
	}
	if c == nil || c.Samples == 0 {
		sample := "calibration_sample"
		if m.d.Routing != nil {
			sample += " " + prob(m.d.Routing.CalibrationSample)
		}
		out := paras(w, m.st.dim.Render("no sampled double reviews yet ("+sample+")"))
		if c != nil {
			out = append(out, m.overrideLines(c, w)...)
		}
		return out
	}
	var out []string
	if c.LastAgree != nil {
		group := c.LastGroup
		if group == "" {
			group = "a group"
		}
		verdict := m.st.good.Render("agrees")
		if !*c.LastAgree {
			verdict = m.st.warn.Render("disagrees")
		}
		line := "sampled " + group + " to deep too: " + verdict
		if at := lastAt(c.LastAt); at != "" {
			line += m.st.dim.Render(" · " + at)
		}
		out = append(out, paras(w, line)...)
	}
	out = append(out, paras(w, m.st.dim.Render(fmt.Sprintf("fast-tier miss rate %s · 30 days (%d of %s)",
		percent(c.MissRate), c.Misses, plural(c.Samples, "sample", "samples"))))...)
	return append(out, m.overrideLines(c, w)...)
}

// overrideLines is the per-question override table (M5 aggregates: a
// FlagAbove or Questions is set), else the older aggregate's override
// counts by question.
func (m *Model) overrideLines(c *model.CalibrationStats, w int) []string {
	if c.Questions == nil && c.FlagAbove == 0 {
		ov := "none"
		if s := joinCounts(byCount(c.Overrides)); s != "" {
			ov = s
		}
		return paras(w, "label overrides "+ov)
	}
	if len(c.Questions) == 0 {
		return []string{m.st.dim.Render("no overrides in 30 days")}
	}
	qw := 8
	for _, q := range c.Questions {
		qw = max(qw, len([]rune(q.Question)))
	}
	qw = min(qw, max(8, w-30))
	out := []string{m.st.dim.Render(padRight("question", qw) + "  overrides/labels  rate")}
	for _, q := range c.Questions {
		line := padRight(q.Question, qw) + "  " + padLeft(fmt.Sprintf("%d/%d", q.Overrides, q.Labels), 16) + "  " + padLeft(percent(q.Rate), 4)
		if q.Flagged {
			line += " " + m.st.accent.Render("⚑ reword?")
		}
		out = append(out, line)
	}
	if c.FlagAbove > 0 {
		out = append(out, paras(w, m.st.dim.Render("⚑ over "+percent(c.FlagAbove)+" overrides · 30 days"))...)
	}
	return out
}

// percent formats a rate as a whole percentage ("12%").
func percent(r float64) string { return fmt.Sprintf("%d%%", int(math.Round(r*100))) }
