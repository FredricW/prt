package pipeview

import (
	"fmt"
	"sort"
	"strings"
	"time"

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

// addPara adds a logical line wrapped to the page width.
func (m *Model) addPara(b *docBuilder, s string) { b.add(wrap(s, m.w, 4)...) }

// buildTotals adds the totals section from Agg.Review.
func (m *Model) buildTotals(b *docBuilder) {
	b.add(m.st.head.Render("Totals"))
	var rs *model.ReviewStats
	if m.d.Agg != nil {
		rs = m.d.Agg.Review
	}
	if rs == nil {
		b.add(m.st.dim.Render("  no review data yet"))
		return
	}
	m.addPara(b, "  tier mix  "+tierBar(rs.Tiers)+"  "+m.st.dim.Render(tierLegend(rs.Tiers)))
	var st []string
	for _, s := range statusOrder {
		if n := rs.Status[s]; n > 0 {
			st = append(st, fmt.Sprintf("%s %d", s, n))
		}
	}
	if len(st) > 0 {
		m.addPara(b, "  "+plural(rs.Groups, "group", "groups")+": "+strings.Join(st, " · "))
	}
	elapsed := "—"
	if rs.ElapsedMs > 0 {
		elapsed = dur(rs.ElapsedMs)
	}
	m.addPara(b, fmt.Sprintf("  reviews %s · all-deep sequential %s", elapsed, estimate(rs.AllDeepEstMs)))
	cost := "  cost " + money(rs.Cost)
	if rs.CapUSD > 0 {
		cost += " of " + money(rs.CapUSD) + " cap"
	} else {
		cost += " (no cap)"
	}
	if rs.OverCap > 0 {
		cost += m.st.warn.Render(fmt.Sprintf(" · %d queued over cap", rs.OverCap)) + m.st.dim.Render(" ($ raises it)")
	}
	m.addPara(b, cost)
	jevLine := fmt.Sprintf("  jev %s · %s", plural(rs.JevCalls, "call", "calls"), money(rs.JevCost))
	if rs.JevCached {
		jevLine += m.st.dim.Render(" (cached)")
	}
	m.addPara(b, jevLine)
	m.addPara(b, m.st.dim.Render(fmt.Sprintf("  escalations %d · calibration samples %d · findings %d", rs.Escalations, rs.Calibrations, rs.Findings)))
	if rs.Note != "" {
		m.addPara(b, m.st.note.Render("  note: "+rs.Note))
	}
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

// buildRules adds the rules panel from Detail.Routing.
func (m *Model) buildRules(b *docBuilder) {
	b.add(m.st.head.Render("Rules") + m.st.dim.Render("  e edit"))
	ri := m.d.Routing
	if ri == nil {
		b.add(m.st.dim.Render("  no routing rules loaded yet"))
		return
	}
	fired := map[int]int{}
	for _, rw := range m.rows {
		fired[rw.rev.RuleIndex]++
	}
	for _, r := range ri.Rules {
		words := r.Words
		if words == "" {
			words = fmt.Sprintf("if %s → %s", r.When, r.Tier)
		}
		line := fmt.Sprintf("  %d %s", r.Index, words)
		var extra []string
		if len(r.Prompts) > 0 {
			extra = append(extra, "+"+strings.Join(r.Prompts, ",+"))
		}
		if len(r.Context) > 0 {
			extra = append(extra, "ctx "+strings.Join(r.Context, ","))
		}
		extra = append(extra, plural(fired[r.Index], "row", "rows"))
		m.addPara(b, line+m.st.dim.Render("  "+strings.Join(extra, " · ")))
		if r.Err != "" {
			m.addPara(b, m.st.warn.Render("    ✗ "+r.Err))
		}
	}
	if len(ri.Rules) == 0 {
		b.add(m.st.dim.Render("  no rules"))
	}
	if n := fired[0]; n > 0 {
		m.addPara(b, m.st.dim.Render("  – "+plural(n, "row", "rows")+" decided outside the rules (forced, skipped or labels unknown)"))
	}
	m.addPara(b, m.st.dim.Render(fmt.Sprintf("  deep_slots %d · cost_cap_usd %s · escalate_below %s · calibration_sample %s",
		ri.DeepSlots, money(ri.CostCapUSD), prob(ri.EscalateBelow), prob(ri.CalibrationSample))))
	src := "  source " + ri.Source
	if ri.Path != "" && ri.Path != ri.Source {
		src += " · e edits " + ri.Path
	}
	m.addPara(b, m.st.dim.Render(src))
	for _, w := range ri.Warnings {
		m.addPara(b, m.st.warn.Render("  warning: "+w))
	}
	if ri.Err != "" {
		m.addPara(b, m.st.warn.Render("  reload failed: "+ri.Err+" (previous rules stay active)"))
	}
}

// buildGrouping adds the grouping panel from Agg.Grouping.
func (m *Model) buildGrouping(b *docBuilder) {
	b.add(m.st.head.Render("Grouping"))
	var g *model.GroupingStats
	if m.d.Agg != nil {
		g = m.d.Agg.Grouping
	}
	if g == nil {
		b.add(m.st.dim.Render("  no grouping data yet"))
		return
	}
	structural := "none"
	if s := joinCounts(linkCounts(g.Structural)); s != "" {
		structural = s
	}
	m.addPara(b, fmt.Sprintf("  units %d → structural %s", g.Units, structural))
	m.addPara(b, fmt.Sprintf("    → embed candidates %d · mechanical candidates %d", g.EmbedCandidates, g.MechanicalCandidates))
	kept := "none"
	if s := joinCounts(byCount(g.JevKept)); s != "" {
		kept = s
	}
	m.addPara(b, fmt.Sprintf("    → jev asked %s: kept %s · rejected %d · cut %d", plural(g.PairsAsked, "pair", "pairs"), kept, g.JevRejected, g.CutLinks))
	m.addPara(b, fmt.Sprintf("    → groups %d · mechanical %d (%d units collapsed) · ejected %d · ungrouped %d · user edits %d",
		g.Groups, g.Mechanical, g.MechanicalCollapsed, g.Ejected, g.Ungrouped, g.UserEdits))
	if g.EmbedNote != "" {
		m.addPara(b, m.st.note.Render("  embed: "+g.EmbedNote))
	}
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

// buildUrgency adds the urgency weights panel (spec Ranking: "Weights live
// in prt.toml and are shown on the Pipeline page"): each stored term as
// words weight×value = contribution, then the stored total and "why". It
// reads ItemAgg.UrgencyTerms only; the Overview's `w` overlay shows the same
// terms as a table.
func (m *Model) buildUrgency(b *docBuilder) {
	b.add(m.st.head.Render("Urgency") + m.st.dim.Render("  weights from prt.toml [rank]"))
	if m.d.Agg == nil || len(m.d.Agg.UrgencyTerms) == 0 {
		b.add(m.st.dim.Render("  no urgency terms yet"))
		return
	}
	parts := make([]string, 0, len(m.d.Agg.UrgencyTerms))
	for _, t := range m.d.Agg.UrgencyTerms {
		parts = append(parts, fmt.Sprintf("%s %.2f×%.2f=%+.2f", t.Words, t.Weight, t.Value, t.Contrib))
	}
	m.addPara(b, "  "+strings.Join(parts, " · "))
	total := fmt.Sprintf("  total %+.2f", m.d.Agg.Urgency)
	if m.d.Agg.Why != "" {
		total += " · why: " + m.d.Agg.Why
	}
	m.addPara(b, total)
}

// buildCalibration adds the calibration panel from Agg.Calibration.
func (m *Model) buildCalibration(b *docBuilder) {
	b.add(m.st.head.Render("Calibration"))
	var c *model.CalibrationStats
	if m.d.Agg != nil {
		c = m.d.Agg.Calibration
	}
	if c == nil || c.Samples == 0 {
		sample := "calibration_sample"
		if m.d.Routing != nil {
			sample += " " + prob(m.d.Routing.CalibrationSample)
		}
		b.add(m.st.dim.Render("  no sampled double reviews yet (" + sample + ")"))
	} else {
		line := fmt.Sprintf("  fast-tier miss rate %s (%d of %s, 30 days)", prob(c.MissRate), c.Misses, plural(c.Samples, "sample", "samples"))
		if c.LastAgree != nil {
			last := m.st.good.Render("agree ✓")
			if !*c.LastAgree {
				last = m.st.warn.Render("disagree ✗")
			}
			line += " · last sample " + last
			if at := lastAt(c.LastAt); at != "" {
				line += m.st.dim.Render(" " + at)
			}
		}
		m.addPara(b, line)
	}
	if c == nil {
		return
	}
	ov := "none"
	if s := joinCounts(byCount(c.Overrides)); s != "" {
		ov = s
	}
	m.addPara(b, "  label overrides "+ov)
}
