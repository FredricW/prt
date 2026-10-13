package aggregate

import (
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// rankedFinding is a finding with its position, for deterministic ordering.
type rankedFinding struct {
	f   model.Finding
	row int // index of the routing row
	pos int // index within the row's findings
}

// severityRank orders severities: high > medium > low > anything else.
func severityRank(s model.Severity) int {
	switch s {
	case model.SevHigh:
		return 3
	case model.SevMedium:
		return 2
	case model.SevLow:
		return 1
	}
	return 0
}

// findingReasons returns, per unit id, the hot-spot reason taken from the
// unit's highest-severity review finding: the first five words of its claim.
// Ties go to the earliest routing row, then the lowest line, then the
// finding's position in the row. Findings without a unit id or with an
// empty claim are ignored.
func findingReasons(reviews []model.Review) map[string]string {
	best := map[string]rankedFinding{}
	for ri, r := range reviews {
		for fi, f := range r.Findings {
			if f.UnitID == "" || strings.TrimSpace(f.Claim) == "" {
				continue
			}
			c := rankedFinding{f: f, row: ri, pos: fi}
			if cur, ok := best[f.UnitID]; !ok || findingBefore(c, cur) {
				best[f.UnitID] = c
			}
		}
	}
	out := make(map[string]string, len(best))
	for id, c := range best {
		out[id] = fiveWords(c.f.Claim)
	}
	return out
}

// findingBefore reports whether a outranks b as a unit's hot-spot reason.
func findingBefore(a, b rankedFinding) bool {
	if sa, sb := severityRank(a.f.Severity), severityRank(b.f.Severity); sa != sb {
		return sa > sb
	}
	if a.row != b.row {
		return a.row < b.row
	}
	if a.f.Line != b.f.Line {
		return a.f.Line < b.f.Line
	}
	return a.pos < b.pos
}

// fiveWords keeps the first maxReasonWords words of s, without a trailing
// comma, semicolon or colon on the last word.
func fiveWords(s string) string {
	words := strings.Fields(s)
	if len(words) > maxReasonWords {
		words = words[:maxReasonWords]
	}
	return strings.TrimRight(strings.Join(words, " "), ",;:")
}

// groupFindings counts findings per group id on the routing rows; when a
// group has more than one row, the first one counts.
func groupFindings(reviews []model.Review) map[string]int {
	out := make(map[string]int, len(reviews))
	for _, r := range reviews {
		if _, seen := out[r.GroupID]; !seen {
			out[r.GroupID] = len(r.Findings)
		}
	}
	return out
}

// Group kinds that are not Jev kind values.
const (
	groupKindMoved = "moved"
	groupKindMixed = "mixed"
)

// unitKindKey is a unit's kind for the group uniformity test: "moved" for
// git-moved units, "formatting" for parse-formatting units or the
// formatting kind, else the Jev kind; "unknown" when the kind is unknown
// or was never asked.
func unitKindKey(f unitFacts) string {
	switch {
	case f.unit.Kind == model.UnitMoved:
		return groupKindMoved
	case f.unit.Format || f.agg.Kind == model.KindFormatting:
		return model.KindFormatting
	case f.agg.Kind == "" || f.agg.Kind == model.LabelUnknown:
		return unknownKind
	}
	return f.agg.Kind
}

// groupKind is the kind every member shares, "unknown" when any member's
// kind is unknown (or the group has no known members), else "mixed".
func groupKind(members []unitFacts) string {
	if len(members) == 0 {
		return unknownKind
	}
	kinds := map[string]bool{}
	for _, f := range members {
		k := unitKindKey(f)
		if k == unknownKind {
			return unknownKind
		}
		kinds[k] = true
	}
	if len(kinds) == 1 {
		return unitKindKey(members[0])
	}
	return groupKindMixed
}

// groupHotSpot is the max model.UnitHotSpot over the members (0 if none).
func groupHotSpot(members []unitFacts) float64 {
	best := 0.0
	for _, f := range members {
		best = max(best, model.UnitHotSpot(f.agg))
	}
	return best
}
