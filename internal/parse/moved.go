package parse

import (
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// minMovedLines is the number of non-blank lines a block needs before an
// identical delete/add pair counts as a move.
const minMovedLines = 3

// DetectMoved marks moved code: a pure-deleted unit and a pure-added unit
// whose changed lines, with +/- stripped and whitespace normalised (blank
// lines ignored), are identical and have at least three non-blank lines are
// both set to Kind moved, and the added one gets MovedFrom = the deleted
// unit's ID. Each unit is matched at most once: deleted units are taken in
// slice order and paired with the earliest unmatched added unit. The input
// slice is not modified; a new slice is returned.
func DetectMoved(units []model.ChangeUnit) []model.ChangeUnit {
	out := append([]model.ChangeUnit(nil), units...)
	added := map[string][]int{}
	for i, u := range out {
		if u.Kind != model.UnitAdded || u.Dels != 0 {
			continue
		}
		if k, ok := movedKey(u.Diff, '+'); ok {
			added[k] = append(added[k], i)
		}
	}
	if len(added) == 0 {
		return out
	}
	for i := range out {
		u := &out[i]
		if u.Kind != model.UnitDeleted || u.Adds != 0 {
			continue
		}
		k, ok := movedKey(u.Diff, '-')
		if !ok || len(added[k]) == 0 {
			continue
		}
		j := added[k][0]
		added[k] = added[k][1:]
		u.Kind = model.UnitMoved
		out[j].Kind = model.UnitMoved
		out[j].MovedFrom = u.ID
	}
	return out
}

// movedKey returns the normalised changed lines (those starting with op) of
// a unit diff, or ok=false when fewer than minMovedLines are non-blank.
func movedKey(unitDiff string, op byte) (string, bool) {
	var lines []string
	for _, l := range strings.Split(unitDiff, "\n") {
		if len(l) == 0 || l[0] != op {
			continue
		}
		if n := strings.Join(strings.Fields(l[1:]), " "); n != "" {
			lines = append(lines, n)
		}
	}
	if len(lines) < minMovedLines {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}
