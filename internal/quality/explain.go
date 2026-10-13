package quality

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Explain renders a diagnostic listing of one item's scoring: every unit
// with its attention, kind, depth, risk and breaking/contract labels and
// whether it overlaps a needs_eyes entry; every headline row and hot spot
// with its match; and the firm entries the pipeline missed. It is for
// tuning (tools/quality -explain), not part of the scoreboard.
func Explain(t Truth, o Outcome) string {
	ix := newIndex(o)
	labels := unitLabelIndex(o.Labels)
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", t.Item)
	explainUnits(&b, t, o, ix, labels)
	explainHeadlines(&b, t, o, ix)
	explainHotSpots(&b, t, o, ix)
	explainMissed(&b, t, o, ix)
	return b.String()
}

// truthClaim names what a headline entry claims: "breaking, <contract>" or
// the contract alone.
func truthClaim(e TruthHeadline) string {
	if e.Breaking {
		return "breaking, " + e.Contract
	}
	return e.Contract
}

// unitLabelIndex maps unit id → question → unit-scope label.
func unitLabelIndex(ls []model.Label) map[string]map[string]model.Label {
	out := map[string]map[string]model.Label{}
	for _, l := range ls {
		if l.Line > 0 {
			continue
		}
		if out[l.UnitID] == nil {
			out[l.UnitID] = map[string]model.Label{}
		}
		out[l.UnitID][l.Question] = l
	}
	return out
}

// spanText renders a unit's changed spans ("new 12-19 old 10-15").
func spanText(s unitSpans) string {
	var parts []string
	if s.hasNew {
		parts = append(parts, fmt.Sprintf("new %d-%d", s.new.first, s.new.last))
	}
	if s.hasOld {
		parts = append(parts, fmt.Sprintf("old %d-%d", s.old.first, s.old.last))
	}
	return strings.Join(parts, " ")
}

// labelText renders a label as "value .pp".
func labelText(ls map[string]model.Label, q string) string {
	l, ok := ls[q]
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%s %.2f", l.Value, l.P)
}

func explainUnits(b *strings.Builder, t Truth, o Outcome, ix index, labels map[string]map[string]model.Label) {
	regions := make([]region, len(t.NeedsEyes))
	for i, e := range t.NeedsEyes {
		regions[i] = regionOf(e.Loc)
	}
	fmt.Fprintf(b, "units (%d; * = overlaps a needs_eyes entry):\n", len(o.Agg.Units))
	for _, a := range o.Agg.Units {
		u := ix.units[a.UnitID]
		s := ix.spans[a.UnitID]
		mark := " "
		if overlapsAny(s, regions) {
			mark = "*"
		}
		ls := labels[a.UnitID]
		unsure := ""
		if a.Unsure {
			unsure = fmt.Sprintf(" unsure(min %.2f)", a.MinConfidence)
		}
		fmt.Fprintf(b, "  %s %-4s %s %s %s +%d-%d | kind %s | depth %s | risk %s | beh %s | brk %s | contract %s | covered %s%s\n",
			mark, a.Attention, u.Path, spanText(s), u.Symbol, u.Adds, u.Dels,
			labelText(ls, model.QKind), labelText(ls, model.QDepth), labelText(ls, model.QRisk),
			labelText(ls, model.QBehaviour), labelText(ls, model.QBreaking), labelText(ls, model.QContract),
			labelText(ls, model.QCovered), unsure)
	}
	b.WriteString("\n")
}

// explainHeadlines lists the headline rows tagged ok (matches an entry),
// FP(kind) (on an entry's lines with another claim), FP(not) (on a
// not_headlines entry) or FP, then the candidates left out below the
// threshold.
func explainHeadlines(b *strings.Builder, t Truth, o Outcome, ix index) {
	regions := make([]region, len(t.Headlines))
	for i, e := range t.Headlines {
		regions[i] = regionOf(e.Loc)
	}
	nots := make([]region, len(t.NotHeadlines))
	for i, e := range t.NotHeadlines {
		nots[i] = regionOf(e.Loc)
	}
	fmt.Fprintf(b, "headlines (%d, %d unsure left out):\n", len(o.Agg.Headlines), o.Agg.HeadlinesUnsure)
	for _, h := range o.Agg.Headlines {
		s := headlineSpans(h, ix)
		tag := "FP"
		switch {
		case matchesAnyHeadline(h, s, t.Headlines):
			tag = "ok"
		case overlapsAny(s, regions):
			tag = "FP(kind)"
		case overlapsAny(s, nots):
			tag = "FP(not)"
		}
		fmt.Fprintf(b, "  %-8s %-16s %s:%d %s P %.2f\n", tag, h.Kind, h.Path, h.Line, h.Text, h.P)
	}
	for _, h := range o.Agg.HeadlineCandidates {
		fmt.Fprintf(b, "  %-8s %-16s %s:%d %s P %.2f\n", "unsure", h.Kind, h.Path, h.Line, h.Text, h.P)
	}
	b.WriteString("\n")
}

func explainHotSpots(b *strings.Builder, t Truth, o Outcome, ix index) {
	ranked := make([]TruthHotSpot, 0, len(t.HotSpots))
	ranked = append(ranked, t.HotSpots...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Rank < ranked[j].Rank })
	b.WriteString("hot spots:\n")
	for i, h := range o.Agg.HotSpots {
		s := ix.spans[h.UnitID]
		hit := "-"
		for _, e := range ranked {
			if s.overlaps(regionOf(e.Loc)) {
				hit = fmt.Sprintf("truth rank %d", e.Rank)
				break
			}
		}
		fmt.Fprintf(b, "  %d. %s:%d score %.2f %q (%s)\n", i+1, h.Path, h.Line, h.Score, h.Reason, hit)
	}
	b.WriteString("\n")
}

func explainMissed(b *strings.Builder, t Truth, o Outcome, ix index) {
	eyes := ix.spansFor(ix.eyes)
	b.WriteString("missed firm entries:\n")
	for _, e := range t.NeedsEyes {
		if e.Certainty == Firm && !anyOverlap(eyes, regionOf(e.Loc)) {
			fmt.Fprintf(b, "  needs_eyes %s %v %s\n", e.Path, e.Lines, e.Symbol)
		}
	}
	for _, e := range t.Headlines {
		if e.Certainty != Firm {
			continue
		}
		found := false
		for _, h := range o.Agg.Headlines {
			if headlineMatches(h, headlineSpans(h, ix), e) {
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(b, "  headline %s %v %s (%s)\n", e.Path, e.Lines, e.Symbol, truthClaim(e))
		}
	}
	und := ix.spansFor(o.Agg.Undocumented)
	for _, u := range t.Fidelity.Undocumented {
		if u.Certainty == Firm && !anyOverlap(und, regionOf(u.Loc)) {
			fmt.Fprintf(b, "  undocumented %s %v %s\n", u.Path, u.Lines, u.Symbol)
		}
	}
	for i, bt := range t.Fidelity.Bullets {
		if pb, ok := findBullet(o.Agg.Bullets, bt.Text, i); ok && bt.Status != BulletPartial && pb.Implemented != (bt.Status == BulletImplemented) {
			fmt.Fprintf(b, "  bullet %q: pipeline implemented=%v by %v, truth %s\n", bt.Text, pb.Implemented, pb.UnitIDs, bt.Status)
		}
	}
	b.WriteString("\n")
}
