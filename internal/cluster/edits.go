package cluster

import (
	"sort"

	"github.com/fredricw/prt/internal/model"
)

// applyEdits applies the user's merge and split edits in order and returns
// the groups plus the number of edits applied. Unit ids not in the item
// (rewritten by a push) are dropped from an edit; a split left with no unit,
// a merge left with fewer than two (it would only tear one unit out of its
// group, the opposite of a merge), a reset (the store has already cleared
// earlier edits) or an unknown op is skipped and not counted. Both merge and
// split move the edit's units out of their groups into one new user-edited
// group; they differ only in intent.
func applyEdits(g *graph, groups []*group, edits []model.GroupEdit, minCount int) ([]*group, int) {
	applied := 0
	for _, e := range edits {
		if e.Op != model.EditMerge && e.Op != model.EditSplit {
			continue
		}
		units := editUnits(g, e.Units)
		if len(units) == 0 || (e.Op == model.EditMerge && len(units) < 2) {
			continue
		}
		groups = moveUnits(groups, units, minCount)
		applied++
	}
	return groups, applied
}

// editUnits maps an edit's unit ids to positions, ascending, without
// unknown ids or duplicates.
func editUnits(g *graph, ids []string) []int {
	seen := map[int]bool{}
	var out []int
	for _, id := range ids {
		if p, ok := g.pos[id]; ok && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Ints(out)
	return out
}

// moveUnits takes units out of their groups and appends them as one new
// user-edited group. Groups that lose members keep the rest; empty groups
// are dropped.
func moveUnits(groups []*group, units []int, minCount int) []*group {
	move := make(map[int]bool, len(units))
	for _, u := range units {
		move[u] = true
	}
	out := make([]*group, 0, len(groups)+1)
	for _, gr := range groups {
		if removeUnits(gr, move, minCount) && len(gr.units) == 0 {
			continue
		}
		out = append(out, gr)
	}
	return append(out, &group{units: units, example: -1, userEdited: true})
}

// removeUnits drops the units in move from gr and reports whether any was
// removed. A mechanical group stays mechanical only while it keeps at least
// minCount verified members; when its example leaves, the first remaining
// member becomes the example and the old example's star links go.
func removeUnits(gr *group, move map[int]bool, minCount int) bool {
	kept := gr.units[:0:0]
	for _, u := range gr.units {
		if !move[u] {
			kept = append(kept, u)
		}
	}
	if len(kept) == len(gr.units) {
		return false
	}
	gr.units = kept
	gr.userEdited = true
	if !gr.mechanical {
		return true
	}
	if len(kept) < minCount {
		gr.mechanical, gr.example, gr.mechLinks, gr.ejected = false, -1, nil, nil
		return true
	}
	in := make(map[int]bool, len(kept))
	for _, u := range kept {
		in[u] = true
	}
	if !in[gr.example] {
		gr.example = kept[0]
	}
	links := gr.mechLinks[:0:0]
	for _, l := range gr.mechLinks {
		if (l.from == gr.example && in[l.to]) || (l.to == gr.example && in[l.from]) {
			links = append(links, l)
		}
	}
	gr.mechLinks = links
	return true
}
