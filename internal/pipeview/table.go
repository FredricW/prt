package pipeview

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
)

// wideTable is the width from which the routing table fits on one line per
// row; below it rule, context and calibration go on a second, dim line.
const wideTable = 110

// cols are the routing table's column widths.
type cols struct {
	wide                                bool
	name, tier, rule, ctx, status, time int
	find, cost, calib                   int
}

// lead is the width of a row's selection mark and "#" column ("▸  3 ").
const lead = 2 + 2 + 1

// tableCols fits the columns to width w; longest is the widest group name,
// which the name column takes when it can (up to 3/5 of the flexible
// width), leaving the rest to the "why" (rule) column.
func tableCols(w, longest int) cols {
	c := cols{tier: 15, status: 14, time: 6, find: 4, cost: 7}
	if w >= wideTable {
		c.wide, c.ctx, c.calib = true, 13, 10
		fixed := lead + c.tier + c.ctx + c.status + c.time + c.find + c.cost + c.calib + 8
		flex := max(w-fixed, 24)
		c.name = clampInt(longest, 12, max(12, flex*3/5))
		c.rule = flex - c.name
		return c
	}
	fixed := lead + c.tier + c.status + c.time + c.find + c.cost + 5
	c.name = max(w-fixed, 8)
	return c
}

// tierText is the tier column: "fast → deep .42", "deep (forced)",
// "none (skipped)" or the tier.
func tierText(r model.Review) string {
	switch {
	case r.Forced:
		return "deep (forced)"
	case r.UserSkipped:
		return "none (skipped)"
	case r.EscalatedOf != nil:
		return fmt.Sprintf("%s → %s %s", *r.EscalatedOf, r.Tier, prob(r.FastConf))
	case r.Tier == "":
		return "—"
	}
	return string(r.Tier)
}

// statusText is the status column with its glyph ("✓ done", "▶ running
// 12s" with a clock, "◌ queued", "✗ failed", "– skipped", "stale labels").
func (m *Model) statusText(r model.Review) string {
	switch r.Status {
	case "":
		return "—"
	case model.ReviewRunning:
		if el := m.elapsed(r.StartedAt); el != "" {
			return "▶ running " + el
		}
		return "▶ running"
	case model.ReviewDone:
		return "✓ done"
	case model.ReviewQueued:
		return "◌ queued"
	case model.ReviewFailed:
		return "✗ failed"
	case model.ReviewSkipped:
		return "– skipped"
	case model.ReviewStaleLabels:
		return "stale labels"
	}
	return string(r.Status)
}

// statusStyled colours the status cell: done green, running blue, failed
// red, queued, skipped and stale labels dim.
func (m *Model) statusStyled(r model.Review, s string) string {
	switch r.Status {
	case model.ReviewDone:
		return m.st.good.Render(s)
	case model.ReviewRunning:
		return m.st.blue.Render(s)
	case model.ReviewFailed:
		return m.st.warn.Render(s)
	case model.ReviewQueued, model.ReviewSkipped, model.ReviewStaleLabels:
		return m.st.dim.Render(s)
	}
	return s
}

// tierStyled colours the tier cell (theme.Tier: none dim, fast green, deep
// accent, fast → deep warm).
func (m *Model) tierStyled(r model.Review, s string) string {
	tier := r.Tier
	if r.UserSkipped {
		tier = model.TierNone
	}
	return m.st.th.Tier(tier, r.EscalatedOf != nil).Render(s)
}

// timeText is the time column: wall time once there is one.
func timeText(r model.Review) string {
	if r.Ms > 0 {
		return dur(r.Ms)
	}
	return "—"
}

// findText is the findings count of a finished row ("—" before).
func findText(r model.Review) string {
	if r.Status == model.ReviewDone || r.Status == model.ReviewStaleLabels || len(r.Findings) > 0 {
		return strconv.Itoa(len(r.Findings))
	}
	return "—"
}

// costText is the row cost.
func costText(r model.Review) string {
	if r.Cost > 0 {
		return money(r.Cost)
	}
	return "—"
}

// calibText is the calibration marker: "✓ agree" or "✗ missed N".
func calibText(r model.Review) string {
	switch {
	case r.Calibration == nil:
		return ""
	case r.Calibration.Agree:
		return "✓ agree"
	}
	return fmt.Sprintf("✗ missed %d", r.Calibration.Missed)
}

// ruleText is the rule in words (the `when` text when no words were kept).
func ruleText(r model.Review) string {
	if r.RuleWords != "" {
		return r.RuleWords
	}
	return r.Rule
}

// nameText is the group name: "ai·" marks a model-written title,
// ‹…› a placeholder.
func (rw row) nameText() string {
	switch {
	case rw.ai:
		return "ai·" + rw.name
	case rw.placeholder:
		return "‹" + rw.name + "›"
	}
	return rw.name
}

// buildTable adds the routing table in a titled box of width w ("Routing ·
// one call per group"; heavy when the selection is in it and colour is
// off). Row positions are recorded relative to b.
func (m *Model) buildTable(b *docBuilder, w int) {
	inner := max(1, w-4)
	var t docBuilder
	t.rowTop = make([]int, len(m.rows))
	t.rowEnd = make([]int, len(m.rows))
	if len(m.rows) == 0 {
		t.add(m.st.dim.Render("no routing decisions yet (stage 6 has not run for this item)"))
	} else {
		longest := 0
		for _, rw := range m.rows {
			longest = max(longest, ansi.StringWidth(rw.nameText()))
		}
		c := tableCols(inner, longest)
		t.add(m.st.dim.Render(m.tableHeader(c)))
		for i, rw := range m.rows {
			t.rowTop[i] = len(t.lines)
			m.addRow(&t, c, i, rw)
			t.rowEnd[i] = len(t.lines)
		}
	}
	top := len(b.lines) + 1 // the box's top border
	b.add(m.boxLines("Routing · one call per group", "D deep · s skip · c call log", t.lines, w, m.panel == panelTable && len(m.rows) > 0)...)
	b.rowTop, b.rowEnd = make([]int, len(m.rows)), make([]int, len(m.rows))
	for i := range m.rows {
		b.rowTop[i], b.rowEnd[i] = t.rowTop[i]+top, t.rowEnd[i]+top
		for y := b.rowTop[i]; y < b.rowEnd[i]; y++ {
			b.hit(hit{y: y, x0: 0, x1: w, idx: i, table: true})
		}
	}
}

// tableHeader is the dim column header line.
func (m *Model) tableHeader(c cols) string {
	parts := []string{"   # " + padRight("group", c.name), padRight("tier", c.tier)}
	if c.wide {
		parts = append(parts, padRight("why", c.rule), padRight("context", c.ctx))
	}
	parts = append(parts, padRight("status", c.status), padLeft("time", c.time), padLeft("find", c.find), padLeft("cost", c.cost))
	if c.wide {
		parts = append(parts, "calib")
	}
	return strings.Join(parts, " ")
}

// addRow adds one routing row: the main line, on narrow widths a dim line
// with the rule, context and calibration, and a note line for a queue
// note, a stale-labels note or an error.
func (m *Model) addRow(b *docBuilder, c cols, i int, rw row) {
	r := rw.rev
	mark := "  "
	if i == m.row {
		mark = "▹ "
		if m.panel == panelTable {
			mark = "▸ "
		}
	}
	num := m.st.dim.Render(padLeft(fmt.Sprint(i+1), 2))
	name := padRight(rw.nameText(), c.name)
	if i == m.row && m.panel == panelTable {
		name = m.st.sel.Render(name)
	}
	parts := []string{mark + num + " " + name, m.tierStyled(r, padRight(tierText(r), c.tier))}
	if c.wide {
		parts = append(parts, m.st.dim.Render(padRight(ruleText(r), c.rule)), padRight(ctxCell(r), c.ctx))
	}
	parts = append(parts, m.statusStyled(r, padRight(m.statusText(r), c.status)), padLeft(timeText(r), c.time),
		padLeft(findText(r), c.find), padLeft(costText(r), c.cost))
	if c.wide {
		parts = append(parts, m.calibStyled(r))
	}
	b.add(strings.TrimRight(strings.Join(parts, " "), " "))
	if !c.wide {
		var sub []string
		if rt := ruleText(r); rt != "" {
			sub = append(sub, rt)
		}
		if len(r.Context)+len(r.ContextNone) > 0 {
			sub = append(sub, "ctx "+ctxCell(r))
		}
		line := m.st.dim.Render("       " + strings.Join(sub, " · "))
		if ct := calibText(r); ct != "" {
			if len(sub) > 0 {
				line += m.st.dim.Render(" · ")
			}
			line += m.calibStyled(r)
		}
		if len(sub) > 0 || r.Calibration != nil {
			b.add(line)
		}
	}
	if r.QueueNote != "" {
		b.add(m.st.dim.Render("       ↳ " + r.QueueNote))
	}
	if r.Status == model.ReviewStaleLabels && r.StaleNote != "" {
		b.add(m.st.dim.Render("       ↳ " + r.StaleNote))
	}
	if r.Err != "" {
		b.add(m.st.warn.Render("       ↳ error: " + r.Err))
	}
}

// calibStyled is the calibration marker, red when the sample missed.
func (m *Model) calibStyled(r model.Review) string {
	ct := calibText(r)
	if r.Calibration != nil && !r.Calibration.Agree {
		return m.st.warn.Render(ct)
	}
	return ct
}
