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

// tableCols fits the columns to width w; longest is the widest group name,
// which the name column takes when it can (up to 3/5 of the flexible
// width), leaving the rest to the rule.
func tableCols(w, longest int) cols {
	c := cols{tier: 15, status: 12, time: 6, find: 4, cost: 7}
	if w >= wideTable {
		c.wide, c.ctx, c.calib = true, 13, 10
		fixed := 2 + c.tier + c.ctx + c.status + c.time + c.find + c.cost + c.calib + 8
		flex := max(w-fixed, 24)
		c.name = clampInt(longest, 12, max(12, flex*3/5))
		c.rule = flex - c.name
		return c
	}
	fixed := 2 + c.tier + c.status + c.time + c.find + c.cost + 5
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

// statusText is the status column ("running 12s" with a clock).
func (m *Model) statusText(r model.Review) string {
	switch r.Status {
	case "":
		return "—"
	case model.ReviewRunning:
		if el := m.elapsed(r.StartedAt); el != "" {
			return "running " + el
		}
	}
	return string(r.Status)
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

// buildTable adds the routing table section.
func (m *Model) buildTable(b *docBuilder) {
	b.add(m.st.head.Render("Routing") + m.st.dim.Render("  D deep · s skip · c call log · r re-review"))
	b.rowTop = make([]int, len(m.rows))
	b.rowEnd = make([]int, len(m.rows))
	if len(m.rows) == 0 {
		b.add(m.st.dim.Render("  no routing decisions yet (stage 6 has not run for this item)"))
		return
	}
	longest := 0
	for _, rw := range m.rows {
		longest = max(longest, ansi.StringWidth(rw.nameText()))
	}
	c := tableCols(m.w, longest)
	b.add(m.st.dim.Render(m.tableHeader(c)))
	for i, rw := range m.rows {
		b.rowTop[i] = len(b.lines)
		m.addRow(b, c, i, rw)
		b.rowEnd[i] = len(b.lines)
		for y := b.rowTop[i]; y < b.rowEnd[i]; y++ {
			b.hit(hit{y: y, x0: 0, x1: m.w, idx: i, table: true})
		}
	}
}

// tableHeader is the dim column header line.
func (m *Model) tableHeader(c cols) string {
	parts := []string{"  " + padRight("group", c.name), padRight("tier", c.tier)}
	if c.wide {
		parts = append(parts, padRight("rule", c.rule), padRight("context", c.ctx))
	}
	parts = append(parts, padRight("status", c.status), padLeft("time", c.time), padLeft("find", c.find), padLeft("cost", c.cost))
	if c.wide {
		parts = append(parts, "calib")
	}
	return strings.Join(parts, " ")
}

// addRow adds one routing row: the main line, on narrow widths a dim line
// with rule, context and calibration, and a note line for a queue note or
// an error.
func (m *Model) addRow(b *docBuilder, c cols, i int, rw row) {
	r := rw.rev
	mark := "  "
	if i == m.row {
		mark = "▹ "
		if m.panel == panelTable {
			mark = "▸ "
		}
	}
	name := padRight(rw.nameText(), c.name)
	if i == m.row && m.panel == panelTable {
		name = m.st.sel.Render(name)
	}
	parts := []string{mark + name, padRight(tierText(r), c.tier)}
	if c.wide {
		parts = append(parts, m.st.dim.Render(padRight(ruleText(r), c.rule)), padRight(ctxList(r.Context), c.ctx))
	}
	status := padRight(m.statusText(r), c.status)
	if r.Status == model.ReviewFailed {
		status = m.st.warn.Render(status)
	}
	parts = append(parts, status, padLeft(timeText(r), c.time), padLeft(findText(r), c.find), padLeft(costText(r), c.cost))
	if c.wide {
		parts = append(parts, m.calibStyled(r))
	}
	b.add(strings.TrimRight(strings.Join(parts, " "), " "))
	if !c.wide {
		var sub []string
		if rt := ruleText(r); rt != "" {
			sub = append(sub, rt)
		}
		if len(r.Context) > 0 {
			sub = append(sub, "ctx "+ctxList(r.Context))
		}
		line := m.st.dim.Render("    " + strings.Join(sub, " · "))
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
		b.add(m.st.dim.Render("    ↳ " + r.QueueNote))
	}
	if r.Err != "" {
		b.add(m.st.warn.Render("    ↳ error: " + r.Err))
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
