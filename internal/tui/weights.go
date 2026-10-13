package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// weightsText renders the Overview's `w` overlay body: this item's stored
// urgency terms (name, words, value, weight, signed contribution), the
// stored total and the top-three "why". It reads ItemAgg only and computes
// nothing (spec: numbers are computed by aggregate).
func weightsText(d proto.Detail, width int) string {
	why := d.Item.Why
	var terms []model.UrgencyTerm
	total := d.Item.Urgency
	if d.Agg != nil {
		terms, total = d.Agg.UrgencyTerms, d.Agg.Urgency
		if d.Agg.Why != "" {
			why = d.Agg.Why
		}
	}
	if len(terms) == 0 {
		return "no urgency terms yet (the item has not been aggregated)"
	}
	nw, ww := len("term"), len("words")
	for _, t := range terms {
		nw = max(nw, ansi.StringWidth(t.Name))
		ww = max(ww, ansi.StringWidth(t.Words))
	}
	row := func(name, words, value, weight, contrib string) string {
		return padRight(name, nw) + "  " + padRight(words, ww) + "  " +
			padLeft(value, 7) + "  " + padLeft(weight, 7) + "  " + padLeft(contrib, 8)
	}
	lines := []string{row("term", "words", "value", "weight", "contrib")}
	for _, t := range terms {
		lines = append(lines, row(t.Name, t.Words, fmt.Sprintf("%.2f", t.Value), fmt.Sprintf("%.2f", t.Weight),
			fmt.Sprintf("%+.2f", t.Contrib)))
	}
	lines = append(lines, row("total", "", "", "", fmt.Sprintf("%+.2f", total)), "", "why: "+orDash(why))
	for i, l := range lines {
		lines[i] = truncate(l, max(10, width))
	}
	return strings.Join(lines, "\n")
}

// TL;DR texts (spec Overview: the TL;DR is marked ai·from labels).
const (
	tldrMark   = "ai·from labels"
	tldrAbsent = "TL;DR: not generated — "
)

// tldrBlock appends the TL;DR under the title line: the sentence (wrapped)
// with a dim "ai·from labels" after it, or a dim "TL;DR: not generated —
// <reason>" line.
func tldrBlock(p *page, d proto.Detail) {
	if d.Agg == nil || strings.TrimSpace(d.Agg.TLDR) == "" {
		p.dim(tldrAbsent + tldrReason(d))
		return
	}
	ls := strings.Split(ansi.Wrap(strings.TrimSpace(d.Agg.TLDR), max(10, p.width), ""), "\n")
	last := len(ls) - 1
	for _, l := range ls[:last] {
		p.add(strings.TrimRight(l, " "), "")
	}
	end := strings.TrimRight(ls[last], " ")
	if ansi.StringWidth(end)+2+ansi.StringWidth(tldrMark) <= p.width {
		p.add(end+"  "+p.st.dim.Render(tldrMark), "")
		return
	}
	p.add(end, "")
	p.dim(tldrMark)
}

// tldrReason says why no TL;DR exists: labels unavailable (it is written
// from the labelled aggregate), no review model (the review stage reported
// a degradation, e.g. no PRT_ANTHROPIC_API_KEY), or pending.
func tldrReason(d proto.Detail) string {
	switch {
	case d.Agg == nil:
		return "pending"
	case d.Agg.LabelsMissing:
		return "labels unavailable"
	case d.Agg.Review != nil && d.Agg.Review.Note != "":
		return "no review model"
	}
	return "pending"
}
