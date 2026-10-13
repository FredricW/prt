package cluster

import (
	"context"
	"sort"

	"github.com/fredricw/prt/internal/model"
)

// pairKey is an unordered pair of local unit indexes (a < b): every link
// between the same two units is one edge for the structural split.
type pairKey struct{ a, b int }

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

// recomputeDivisor bounds the betweenness recomputations of one structural
// split: betweenness is recomputed after every 1 + pairs/recomputeDivisor
// removals (every removal for parts under recomputeDivisor pairs, the exact
// Girvan–Newman order), so a split costs O(recomputeDivisor·V·E) plus a
// cheap connectivity check per removal instead of O(V·E²).
const recomputeDivisor = 64

// structuralSplit splits an oversized part that has no Jev link left
// (Girvan–Newman style): it removes the unit pair with the highest edge
// betweenness, ties to the looser link kind and then the later pair, until
// the part falls apart. A removal that would split off a single unit is put
// back and never tried again, so stars (one helper and its many callers)
// stay whole instead of shedding ungrouped leaves. It returns the pieces
// (ascending, ordered by first unit) and the links crossing them, or nil
// when no split leaves every piece with at least two units or ctx is done.
func structuralSplit(ctx context.Context, g *graph, part []int, inside []int) ([][]int, []int) {
	nodes := append([]int(nil), part...)
	sort.Ints(nodes)
	local := make(map[int]int, len(nodes))
	for i, u := range nodes {
		local[u] = i
	}
	byPair := map[pairKey][]int{}
	var pairs []pairKey
	for _, e := range inside {
		l := g.links[e]
		k := pairKey{min(local[l.from], local[l.to]), max(local[l.from], local[l.to])}
		if _, ok := byPair[k]; !ok {
			pairs = append(pairs, k)
		}
		byPair[k] = append(byPair[k], e)
	}
	// Local indexes follow ascending positions, so this is the
	// (From position, To position) order.
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].a != pairs[j].a {
			return pairs[i].a < pairs[j].a
		}
		return pairs[i].b < pairs[j].b
	})
	loose := make([]int, len(pairs))
	for i, k := range pairs {
		r := 5
		for _, e := range byPair[k] {
			r = min(r, kindLooseness(g.links[e].l.Kind))
		}
		loose[i] = r
	}
	removed := make([]bool, len(pairs))
	protected := make([]bool, len(pairs))
	every := 1 + len(pairs)/recomputeDivisor
	var eb []float64
	since := every
	for {
		if ctx.Err() != nil {
			return nil, nil
		}
		if since >= every {
			eb = betweenness(len(nodes), pairs, removed)
			since = 0
		}
		best := -1
		for i := range pairs {
			if removed[i] || protected[i] {
				continue
			}
			if best < 0 || better(i, best, pairs, eb, loose) {
				best = i
			}
		}
		if best < 0 {
			return nil, nil
		}
		removed[best] = true
		since++
		comps := localComponents(len(nodes), pairs, removed)
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
			// Put back: the graph is as before, so eb is still current.
			removed[best] = false
			protected[best] = true
			since--
			continue
		}
		piece := make(map[int]int, len(nodes))
		out := make([][]int, len(comps))
		for i, c := range comps {
			for _, x := range c {
				piece[nodes[x]] = i
				out[i] = append(out[i], nodes[x])
			}
		}
		var crossing []int
		for _, e := range inside {
			l := g.links[e]
			if piece[l.from] != piece[l.to] {
				crossing = append(crossing, e)
			}
		}
		return out, crossing
	}
}

// better reports whether pair a is removed before b (indexes into pairs).
func better(a, b int, pairs []pairKey, eb []float64, loose []int) bool {
	if eb[a] != eb[b] {
		return eb[a] > eb[b]
	}
	if loose[a] != loose[b] {
		return loose[a] < loose[b]
	}
	if pairs[a].a != pairs[b].a {
		return pairs[a].a > pairs[b].a
	}
	return pairs[a].b > pairs[b].b
}

// arc is one direction of a unit pair in the adjacency of betweenness.
type arc struct{ to, pair int }

// adjacency lists the pairs not removed per local node, neighbours
// ascending.
func adjacency(n int, pairs []pairKey, removed []bool) [][]arc {
	adj := make([][]arc, n)
	for i, k := range pairs {
		if removed[i] {
			continue
		}
		adj[k.a] = append(adj[k.a], arc{k.b, i})
		adj[k.b] = append(adj[k.b], arc{k.a, i})
	}
	for _, ns := range adj {
		sort.Slice(ns, func(i, j int) bool { return ns[i].to < ns[j].to })
	}
	return adj
}

// localComponents returns the connected components over local nodes 0..n−1
// and the pairs not removed: each ascending, ordered by first node.
func localComponents(n int, pairs []pairKey, removed []bool) [][]int {
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(u int) int {
		for parent[u] != u {
			parent[u] = parent[parent[u]]
			u = parent[u]
		}
		return u
	}
	for i, k := range pairs {
		if removed[i] {
			continue
		}
		a, b := find(k.a), find(k.b)
		switch {
		case a < b:
			parent[b] = a
		case b < a:
			parent[a] = b
		}
	}
	idx := make([]int, n)
	var out [][]int
	for u := range n {
		r := find(u)
		if r == u {
			idx[u] = len(out)
			out = append(out, nil)
		}
		out[idx[r]] = append(out[idx[r]], u)
	}
	return out
}

// betweenness is Brandes' unweighted edge betweenness over local nodes
// 0..n−1 and the pairs not removed, indexed like pairs. Nodes and
// neighbours are visited in ascending order so the float sums are
// deterministic.
func betweenness(n int, pairs []pairKey, removed []bool) []float64 {
	adj := adjacency(n, pairs, removed)
	eb := make([]float64, len(pairs))
	sigma := make([]float64, n)
	dist := make([]int, n)
	delta := make([]float64, n)
	preds := make([][]arc, n)
	order := make([]int, 0, n)
	for s := range n {
		for i := range n {
			sigma[i], dist[i], delta[i], preds[i] = 0, -1, 0, preds[i][:0]
		}
		sigma[s], dist[s] = 1, 0
		order = append(order[:0], s)
		for i := 0; i < len(order); i++ {
			v := order[i]
			for _, a := range adj[v] {
				w := a.to
				if dist[w] < 0 {
					dist[w] = dist[v] + 1
					order = append(order, w)
				}
				if dist[w] == dist[v]+1 {
					sigma[w] += sigma[v]
					preds[w] = append(preds[w], arc{v, a.pair})
				}
			}
		}
		for i := len(order) - 1; i > 0; i-- {
			w := order[i]
			for _, p := range preds[w] {
				c := sigma[p.to] / sigma[w] * (1 + delta[w])
				eb[p.pair] += c
				delta[p.to] += c
			}
		}
	}
	return eb
}
