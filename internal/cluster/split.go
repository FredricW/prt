package cluster

import (
	"sort"

	"github.com/fredricw/prt/internal/model"
)

// pairKey is an unordered unit pair (a < b): every link between the same two
// units is one edge for the structural split.
type pairKey struct{ a, b int }

func keyOf(l link) pairKey {
	if l.from < l.to {
		return pairKey{l.from, l.to}
	}
	return pairKey{l.to, l.from}
}

// kindLooseness ranks link kinds by how loosely they bind review work: a
// test or an import is cut before a call, a call before a move.
func kindLooseness(k model.LinkKind) int {
	switch k {
	case model.LinkTests:
		return 0
	case model.LinkImports:
		return 1
	case model.LinkSameSymbol:
		return 2
	case model.LinkCalls:
		return 3
	case model.LinkMoved:
		return 4
	}
	return 5
}

// structuralSplit splits an oversized part that has no Jev link left
// (Girvan–Newman style): it removes the unit pair with the highest edge
// betweenness, ties to the looser link kind and then the later pair, until
// the part falls apart. A removal that would split off a single unit is put
// back and never tried again, so stars (one helper and its many callers)
// stay whole instead of shedding ungrouped leaves. It returns the pieces
// (ascending, ordered by first unit) and the links crossing them, or nil
// when no split leaves every piece with at least two units.
func structuralSplit(g *graph, part []int, inside []int) ([][]int, []int) {
	byPair := map[pairKey][]int{}
	var pairs []pairKey
	for _, e := range inside {
		k := keyOf(g.links[e])
		if _, ok := byPair[k]; !ok {
			pairs = append(pairs, k)
		}
		byPair[k] = append(byPair[k], e)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].a != pairs[j].a {
			return pairs[i].a < pairs[j].a
		}
		return pairs[i].b < pairs[j].b
	})
	loose := make(map[pairKey]int, len(pairs))
	for _, k := range pairs {
		r := 5
		for _, e := range byPair[k] {
			r = min(r, kindLooseness(g.links[e].l.Kind))
		}
		loose[k] = r
	}
	removed := map[pairKey]bool{}
	protected := map[pairKey]bool{}
	activeEdges := func() []int {
		var out []int
		for _, k := range pairs {
			if !removed[k] {
				out = append(out, byPair[k]...)
			}
		}
		return out
	}
	for {
		eb := betweenness(part, pairs, removed)
		best, found := pairKey{}, false
		for _, k := range pairs {
			if removed[k] || protected[k] {
				continue
			}
			if !found || better(k, best, eb, loose) {
				best, found = k, true
			}
		}
		if !found {
			return nil, nil
		}
		removed[best] = true
		comps := components(g, part, activeEdges(), map[int]bool{})
		if len(comps) == 1 {
			continue
		}
		small := false
		for _, c := range comps {
			if len(c) < 2 {
				small = true
			}
		}
		if small {
			delete(removed, best)
			protected[best] = true
			continue
		}
		piece := map[int]int{}
		for i, c := range comps {
			for _, u := range c {
				piece[u] = i
			}
		}
		var crossing []int
		for _, e := range inside {
			l := g.links[e]
			if piece[l.from] != piece[l.to] {
				crossing = append(crossing, e)
			}
		}
		return comps, crossing
	}
}

// better reports whether pair a is removed before b.
func better(a, b pairKey, eb map[pairKey]float64, loose map[pairKey]int) bool {
	if eb[a] != eb[b] {
		return eb[a] > eb[b]
	}
	if loose[a] != loose[b] {
		return loose[a] < loose[b]
	}
	if a.a != b.a {
		return a.a > b.a
	}
	return a.b > b.b
}

// betweenness is Brandes' unweighted edge betweenness over the part's unit
// pairs that are not removed. Nodes and neighbours are visited in ascending
// order so the float sums are deterministic.
func betweenness(part []int, pairs []pairKey, removed map[pairKey]bool) map[pairKey]float64 {
	adj := make(map[int][]int, len(part))
	for _, k := range pairs {
		if removed[k] {
			continue
		}
		adj[k.a] = append(adj[k.a], k.b)
		adj[k.b] = append(adj[k.b], k.a)
	}
	for _, ns := range adj {
		sort.Ints(ns)
	}
	nodes := append([]int(nil), part...)
	sort.Ints(nodes)
	eb := map[pairKey]float64{}
	for _, s := range nodes {
		sigma := map[int]float64{s: 1}
		dist := map[int]int{s: 0}
		preds := map[int][]int{}
		order := []int{s}
		for i := 0; i < len(order); i++ {
			v := order[i]
			for _, w := range adj[v] {
				if _, seen := dist[w]; !seen {
					dist[w] = dist[v] + 1
					order = append(order, w)
				}
				if dist[w] == dist[v]+1 {
					sigma[w] += sigma[v]
					preds[w] = append(preds[w], v)
				}
			}
		}
		delta := map[int]float64{}
		for i := len(order) - 1; i > 0; i-- {
			w := order[i]
			for _, v := range preds[w] {
				c := sigma[v] / sigma[w] * (1 + delta[w])
				k := pairKey{min(v, w), max(v, w)}
				eb[k] += c
				delta[v] += c
			}
		}
	}
	return eb
}
