package cluster

import (
	"sort"

	"github.com/fredricw/prt/internal/model"
)

// group is a group under construction: unit positions plus the facts that
// survive user edits.
type group struct {
	units      []int // ascending positions
	mechanical bool
	example    int    // mechanical: example position; −1 otherwise
	mechLinks  []link // mechanical: the verified example → member links
	ejected    []int  // mechanical: members whose shape did not match
	userEdited bool
}

// mechanicalGroups forms a group per mechanical class (mechanical links
// grouped by From, the example) whose verified members — the example and
// the targets with the example's non-empty shape hash — number at least
// minCount. Classes are taken in example order; a unit already in an
// earlier mechanical group is neither a member nor ejected.
func mechanicalGroups(g *graph, minCount int) []*group {
	taken := make([]bool, len(g.units))
	var out []*group
	for _, class := range mechanicalClasses(g) {
		example := class[0].from
		if taken[example] {
			continue
		}
		m := verify(g, example, class, taken)
		if len(m.units) < minCount {
			continue
		}
		for _, u := range m.units {
			taken[u] = true
		}
		out = append(out, m)
	}
	return out
}

// mechanicalClasses returns the mechanical links grouped by From, in From
// order; g.links is sorted by From so each class is a contiguous run.
func mechanicalClasses(g *graph) [][]link {
	var out [][]link
	for _, l := range g.links {
		if l.l.Kind != model.LinkMechanical {
			continue
		}
		if n := len(out); n > 0 && out[n-1][0].from == l.from {
			out[n-1] = append(out[n-1], l)
			continue
		}
		out = append(out, []link{l})
	}
	return out
}

// verify splits a class into verified members (example first in units,
// which stays ascending) and ejected targets.
func verify(g *graph, example int, class []link, taken []bool) *group {
	shape := g.units[example].ShapeHash
	m := &group{example: example, mechanical: true}
	seen := map[int]bool{}
	members := []int{}
	if shape != "" {
		members = append(members, example)
	}
	for _, l := range class {
		if taken[l.to] || seen[l.to] {
			continue
		}
		seen[l.to] = true
		if shape != "" && g.units[l.to].ShapeHash == shape {
			members = append(members, l.to)
			m.mechLinks = append(m.mechLinks, l)
			continue
		}
		m.ejected = append(m.ejected, l.to)
	}
	sort.Ints(members)
	sort.Ints(m.ejected)
	m.units = members
	return m
}
