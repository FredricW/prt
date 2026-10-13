package cluster

import (
	"sort"

	"github.com/fredricw/prt/internal/model"
)

// link is a model.Link whose endpoints are unit positions in graph.units.
type link struct {
	from, to int
	l        model.Link
}

// graph is an item's units in review position (Order, ties by id) and its
// usable links, sorted by (From position, To position, Kind, Source, …).
type graph struct {
	units []model.ChangeUnit
	pos   map[string]int
	links []link
}

// newGraph sorts the units and keeps the links whose endpoints are two
// different units of the item. Embed-source links other than mechanical
// candidates (unconfirmed embed neighbours) are dropped; exact duplicates
// are kept once.
func newGraph(units []model.ChangeUnit, links []model.Link) *graph {
	sorted := append([]model.ChangeUnit(nil), units...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Order != sorted[j].Order {
			return sorted[i].Order < sorted[j].Order
		}
		return sorted[i].ID < sorted[j].ID
	})
	g := &graph{pos: make(map[string]int, len(sorted))}
	for _, u := range sorted {
		if _, dup := g.pos[u.ID]; dup {
			continue
		}
		g.pos[u.ID] = len(g.units)
		g.units = append(g.units, u)
	}
	for _, l := range links {
		from, okF := g.pos[l.From]
		to, okT := g.pos[l.To]
		if !okF || !okT || from == to || unconfirmedEmbed(l) {
			continue
		}
		g.links = append(g.links, link{from: from, to: to, l: l})
	}
	sort.Slice(g.links, func(i, j int) bool { return linkLess(g.links[i], g.links[j]) })
	g.links = dedupe(g.links)
	return g
}

// unconfirmedEmbed reports an embed candidate that is not a mechanical
// candidate: only Jev confirmation makes such a link usable.
func unconfirmedEmbed(l model.Link) bool {
	return l.Source == model.LinkEmbed && l.Kind != model.LinkMechanical
}

// linkLess is the total order of links: endpoints by position, then every
// remaining field, so sorting never depends on input order.
func linkLess(a, b link) bool {
	switch {
	case a.from != b.from:
		return a.from < b.from
	case a.to != b.to:
		return a.to < b.to
	case a.l.Kind != b.l.Kind:
		return a.l.Kind < b.l.Kind
	case a.l.Source != b.l.Source:
		return a.l.Source < b.l.Source
	case a.l.Weight != b.l.Weight:
		return a.l.Weight < b.l.Weight
	case a.l.P != b.l.P:
		return a.l.P < b.l.P
	}
	return a.l.Relation < b.l.Relation
}

// dedupe drops adjacent identical links from a sorted slice.
func dedupe(ls []link) []link {
	out := ls[:0]
	for i, l := range ls {
		if i > 0 && l.l == ls[i-1].l {
			continue
		}
		out = append(out, l)
	}
	return out
}

// graphEdge reports whether a link binds units in the general graph:
// structural and Jev-confirmed links (mechanical candidates from embed are
// handled by the mechanical step only).
func graphEdge(l link) bool {
	return l.l.Source != model.LinkEmbed
}

// splitComponents computes the connected components of the units outside
// mechanical groups over structural and Jev links, then splits components
// larger than maxUnits by cutting their weakest Jev links. It returns the
// parts (each ascending, ordered by first unit) and the cut links' indexes
// in g.links.
func splitComponents(g *graph, inMech []bool, maxUnits int) ([][]int, map[int]bool) {
	var nodes, edges []int
	for u := range g.units {
		if !inMech[u] {
			nodes = append(nodes, u)
		}
	}
	for i, l := range g.links {
		if graphEdge(l) && !inMech[l.from] && !inMech[l.to] {
			edges = append(edges, i)
		}
	}
	cut := map[int]bool{}
	var parts [][]int
	for _, c := range components(g, nodes, edges, cut) {
		parts = append(parts, refine(g, c, edges, cut, maxUnits)...)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i][0] < parts[j][0] })
	return parts, cut
}

// refine splits an oversized part by cutting its weakest Jev link until
// every piece fits. A piece with no Jev link left is split at its
// structural bridges (structuralSplit); one that has none stays whole.
func refine(g *graph, part []int, edges []int, cut map[int]bool, maxUnits int) [][]int {
	if len(part) <= maxUnits {
		return [][]int{part}
	}
	inside := insideEdges(g, part, edges, cut)
	w, ok := weakestJev(g, inside)
	if !ok {
		pieces, crossing := structuralSplit(g, part, inside)
		if pieces == nil {
			return [][]int{part}
		}
		for _, e := range crossing {
			cut[e] = true
		}
		var out [][]int
		for _, c := range pieces {
			out = append(out, refine(g, c, inside, cut, maxUnits)...)
		}
		return out
	}
	cut[w] = true
	var out [][]int
	for _, c := range components(g, part, inside, cut) {
		out = append(out, refine(g, c, inside, cut, maxUnits)...)
	}
	return out
}

// insideEdges returns the uncut edges whose endpoints are both in part.
func insideEdges(g *graph, part []int, edges []int, cut map[int]bool) []int {
	in := make(map[int]bool, len(part))
	for _, u := range part {
		in[u] = true
	}
	var out []int
	for _, e := range edges {
		l := g.links[e]
		if !cut[e] && in[l.from] && in[l.to] {
			out = append(out, e)
		}
	}
	return out
}

// weakestJev picks the Jev link to cut first: lowest Weight, then lowest P,
// then the last by (From Order, To Order) and the rest of the link order.
func weakestJev(g *graph, edges []int) (int, bool) {
	best, found := -1, false
	for _, e := range edges {
		if g.links[e].l.Source != model.LinkJev {
			continue
		}
		if !found || weaker(g.links[e], g.links[best]) {
			best, found = e, true
		}
	}
	return best, found
}

// weaker reports whether a should be cut before b.
func weaker(a, b link) bool {
	if a.l.Weight != b.l.Weight {
		return a.l.Weight < b.l.Weight
	}
	if a.l.P != b.l.P {
		return a.l.P < b.l.P
	}
	return linkLess(b, a)
}

// components is a deterministic union-find over nodes and the uncut edges:
// each component is ascending and components are ordered by first node.
func components(g *graph, nodes []int, edges []int, cut map[int]bool) [][]int {
	parent := make(map[int]int, len(nodes))
	for _, u := range nodes {
		parent[u] = u
	}
	find := func(u int) int {
		for parent[u] != u {
			parent[u] = parent[parent[u]]
			u = parent[u]
		}
		return u
	}
	for _, e := range edges {
		if cut[e] {
			continue
		}
		l := g.links[e]
		if _, ok := parent[l.from]; !ok {
			continue
		}
		if _, ok := parent[l.to]; !ok {
			continue
		}
		a, b := find(l.from), find(l.to)
		switch {
		case a < b:
			parent[b] = a
		case b < a:
			parent[a] = b
		}
	}
	sorted := append([]int(nil), nodes...)
	sort.Ints(sorted)
	byRoot := map[int][]int{}
	var roots []int
	for _, u := range sorted {
		r := find(u)
		if _, seen := byRoot[r]; !seen {
			roots = append(roots, r)
		}
		byRoot[r] = append(byRoot[r], u)
	}
	out := make([][]int, 0, len(roots))
	for _, r := range roots {
		out = append(out, byRoot[r])
	}
	return out
}
