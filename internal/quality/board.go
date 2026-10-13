package quality

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Board is the scoreboard: one row per ground-truth item, in item order,
// plus a totals row that pools every ratio (sums of numerators over sums
// of denominators, items where a metric does not apply left out) and sums
// the headline counts.
type Board struct {
	Rows   []Row `json:"items"`
	Totals Row   `json:"totals"`
	// Notes are remarks about the run (e.g. tolerated review misses),
	// printed under the table.
	Notes []string `json:"notes,omitempty"`
}

// NewBoard builds a board from rows (kept in the given order) and computes
// the totals row.
func NewBoard(rows []Row) Board {
	b := Board{Rows: rows, Totals: Row{Item: "totals", Ratios: map[string]Ratio{}}}
	for _, r := range rows {
		b.Totals.Headlines += r.Headlines
		b.Totals.HeadlinesUnsure += r.HeadlinesUnsure
		for _, m := range Metrics {
			if v := r.Ratios[m.Key]; v.Applies() {
				b.Totals.Ratios[m.Key] = b.Totals.Ratios[m.Key].add(v)
			}
		}
	}
	return b
}

// Row returns the row of an item (ok false when absent).
func (b Board) Row(item string) (Row, bool) {
	for _, r := range b.Rows {
		if r.Item == item {
			return r, true
		}
	}
	return Row{}, false
}

// JSON renders the board as indented JSON (deterministic: rows in order,
// metric maps sorted by key).
func (b Board) JSON() ([]byte, error) {
	out, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("quality: encode board: %w", err)
	}
	return append(out, '\n'), nil
}

// Markdown renders the board as a markdown table: one row per item and a
// totals row. A ratio cell reads "num/den (value)", "—" when the metric
// does not apply; the headline cell is "rows (+unsure left out)"; the
// must_read cell adds the eyes lines and the accepted range.
func (b Board) Markdown() string {
	var sb strings.Builder
	head := []string{"Item", "Headlines"}
	for _, m := range Metrics {
		head = append(head, m.Title)
	}
	sb.WriteString("| " + strings.Join(head, " | ") + " |\n")
	sb.WriteString("|" + strings.Repeat(" --- |", len(head)) + "\n")
	for _, r := range b.Rows {
		sb.WriteString(markdownRow(r, r.Item, true))
	}
	sb.WriteString(markdownRow(b.Totals, "**totals**", false))
	for _, n := range b.Notes {
		sb.WriteString("\nNote: " + n + "\n")
	}
	return sb.String()
}

// markdownRow renders one table row.
func markdownRow(r Row, label string, withRange bool) string {
	cells := []string{label, headlineCell(r)}
	for _, m := range Metrics {
		c := RatioCell(r.Ratios[m.Key])
		if m.Key == MMustRead && withRange && r.Ratios[MMustRead].Applies() {
			c = fmt.Sprintf("%s: %d in %d–%d", yesNo(r.Ratios[MMustRead].Num == 1), r.Ratios[MLinesToRead].Num, r.MustReadRange[0], r.MustReadRange[1])
		}
		cells = append(cells, c)
	}
	return "| " + strings.Join(cells, " | ") + " |\n"
}

// headlineCell is "rows" or "rows (+n unsure)".
func headlineCell(r Row) string {
	if r.HeadlinesUnsure > 0 {
		return fmt.Sprintf("%d (+%d unsure)", r.Headlines, r.HeadlinesUnsure)
	}
	return fmt.Sprint(r.Headlines)
}

// RatioCell renders a ratio as "num/den (0.00)", or "—" when it does not
// apply.
func RatioCell(r Ratio) string {
	if !r.Applies() {
		return "—"
	}
	return fmt.Sprintf("%d/%d (%.2f)", r.Num, r.Den, r.Value())
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
