package cluster

import (
	"sort"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/shape"
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

// mechanicalGroups forms the mechanical groups. A family is a connected
// component over mechanical links (any source). Each family is split into
// partitions by shape.Key (shape hash plus literal edit signature); every
// partition with at least minCount units becomes a group whose example is
// its lowest-position unit. Family units left out of every group (another
// literal edit in a partition too small, or no shape hash) are listed as
// ejected on the family's first group. No choice of example can make a
// genuine repeat fail: verification never compares against a single unit.
func mechanicalGroups(g *graph, minCount int) []*group {
	var out []*group
	for _, fam := range mechanicalFamilies(g) {
		var formed []*group
		in := map[int]bool{}
		for _, part := range partitions(g, fam) {
			if len(part) < minCount {
				continue
			}
			m := &group{units: part, example: part[0], mechanical: true}
			m.mechLinks = starLinks(g, part)
			for _, u := range part {
				in[u] = true
			}
			formed = append(formed, m)
		}
		if len(formed) == 0 {
			continue
		}
		for _, u := range fam {
			if !in[u] {
				formed[0].ejected = append(formed[0].ejected, u)
			}
		}
		out = append(out, formed...)
	}
	return out
}

// mechanicalFamilies returns the connected components over mechanical
// links: each ascending, ordered by first unit. Units without a mechanical
// link are in no family.
func mechanicalFamilies(g *graph) [][]int {
	var nodes, edges []int
	seen := map[int]bool{}
	for i, l := range g.links {
		if l.l.Kind != model.LinkMechanical {
			continue
		}
		edges = append(edges, i)
		for _, u := range []int{l.from, l.to} {
			if !seen[u] {
				seen[u] = true
				nodes = append(nodes, u)
			}
		}
	}
	return components(g, nodes, edges, map[int]bool{})
}

// partitions splits a family by shape.Key: units without a shape hash are
// left out, each partition is ascending and partitions are ordered by first
// unit.
func partitions(g *graph, fam []int) [][]int {
	idx := map[string]int{}
	var out [][]int
	for _, u := range fam {
		k := shape.Key(g.units[u])
		if k == "" {
			continue
		}
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], u)
	}
	return out
}

// starLinks returns the verified example → member links of a partition
// (example = part[0]): the mechanical link between the two when the input
// has one (example → member preferred), else a code-made mechanical link
// (Source embed, Weight 1), since verification by key is what makes a
// member, not the candidate star that proposed it.
func starLinks(g *graph, part []int) []link {
	ex := part[0]
	byPair := map[[2]int]link{}
	for _, l := range g.links {
		if l.l.Kind != model.LinkMechanical {
			continue
		}
		k := [2]int{l.from, l.to}
		if _, ok := byPair[k]; !ok {
			byPair[k] = l
		}
	}
	out := make([]link, 0, len(part)-1)
	for _, m := range part[1:] {
		if l, ok := byPair[[2]int{ex, m}]; ok {
			out = append(out, l)
			continue
		}
		if l, ok := byPair[[2]int{m, ex}]; ok {
			out = append(out, l)
			continue
		}
		out = append(out, link{from: ex, to: m, l: model.Link{
			From: g.units[ex].ID, To: g.units[m].ID, Kind: model.LinkMechanical, Source: model.LinkEmbed, Weight: 1,
		}})
	}
	sort.Slice(out, func(i, j int) bool { return linkLess(out[i], out[j]) })
	return out
}
