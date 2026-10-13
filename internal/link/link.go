// Package link is pipeline stage 2 (structural links) and the candidate half
// of stage 3 (embedding kNN and shape-hash mechanical candidates):
//
//   - Structural: call edges between changed symbols (tree-sitter queries +
//     a symbol index of the repo cached per base SHA), imports, test-to-code
//     by filename and symbol name, moved code (identical normalised bodies
//     deleted in one place and added in another), same-symbol edits across
//     files. Source = structural, Weight = 1.
//   - EmbedCandidates: kNN over units of the same item, cosine ≥
//     MinCosine or the top K, whichever yields more. Source = embed,
//     Kind = embed-neighbour, Weight = cosine.
//   - MechanicalCandidates: units with identical shape hashes, linked
//     directly without embedding. Source = embed, Kind = mechanical.
//
// Link directions follow model.Link's doc comment. Every function is
// deterministic: output order does not depend on map iteration.
package link

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/shape"
)

// Source provides file contents and listings at a revision (gitx.Git
// satisfies it).
type Source interface {
	Show(ctx context.Context, rev, path string) ([]byte, error)
	ListFiles(ctx context.Context, rev string) ([]string, error)
}

// Cache persists the repo symbol index per base SHA (store.Store satisfies
// it). Namespace "symindex", key = base SHA + index format version.
type Cache interface {
	CacheGet(ctx context.Context, ns, key string) ([]byte, bool, error)
	CachePut(ctx context.Context, ns, key string, val []byte) error
}

// Input is everything stage 2 needs for one item.
type Input struct {
	ItemID  string
	BaseSHA string
	HeadSHA string
	Files   []model.File
	Units   []model.ChangeUnit // as produced by parse.Split (Order, Symbol, MovedFrom, ShapeHash set)
	// Src may be nil: then only links derivable from the units themselves
	// (moved, same-symbol, test-to-code by name) are produced.
	Src Source
	// Cache may be nil: the symbol index is then rebuilt every run.
	Cache Cache
}

// Structural returns the exact links between the item's units, deduplicated
// (one link per (From, To, Kind)) and sorted by (From order, To order, Kind).
// Only context cancellation is an error; source problems degrade to fewer
// links.
func Structural(ctx context.Context, in Input) ([]model.Link, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("link: %w", err)
	}
	units := sortedUnits(in.Units)
	ls := newLinkSet(units)
	movedLinks(units, ls)
	sameSymbolLinks(units, ls)
	testLinks(units, ls)
	if in.Src != nil && len(units) > 0 {
		r, err := newRun(ctx, in, units)
		if err != nil {
			return nil, err
		}
		r.symbolMoves(ls)
		if err := r.calls(ctx, ls); err != nil {
			return nil, err
		}
		if err := r.imports(ctx, ls); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("link: %w", err)
	}
	return ls.sorted(), nil
}

// EmbedOptions are the kNN knobs from prt.toml [pipeline].
type EmbedOptions struct {
	MinCosine float64 // embed_min_cosine (default 0.72)
	TopK      int     // embed_top_k (default 3)
}

// EmbedCandidates returns embed-neighbour candidate links from unit vectors
// (keyed by unit id; units without a vector are skipped). Per unit, its
// neighbours are every other unit with cosine ≥ MinCosine, or its TopK
// nearest when that is more. Each unordered pair appears once, From = the
// lower-Order unit, Weight = cosine. Pairs whose units share a non-empty
// ShapeHash are left out before ranking (they are mechanical candidates).
// Zero options take the defaults (0.72, 3).
func EmbedCandidates(units []model.ChangeUnit, vecs map[string][]float32, o EmbedOptions) []model.Link {
	if o.MinCosine <= 0 {
		o.MinCosine = DefaultMinCosine
	}
	if o.TopK <= 0 {
		o.TopK = DefaultTopK
	}
	var us []model.ChangeUnit
	for _, u := range sortedUnits(units) {
		if usable(vecs[u.ID]) {
			us = append(us, u)
		}
	}
	ls := newLinkSet(us)
	for i, u := range us {
		for _, n := range neighbours(us, vecs, i, o) {
			from, to := u.ID, us[n.idx].ID
			if us[n.idx].Order < u.Order {
				from, to = to, from
			}
			ls.add(from, to, model.LinkEmbedNeighbor, model.LinkEmbed, n.cos)
		}
	}
	return ls.sorted()
}

// Embedding kNN defaults (spec stage 3; prt.toml [pipeline]).
const (
	DefaultMinCosine = 0.72
	DefaultTopK      = 3
)

// neighbour is one candidate neighbour of a unit.
type neighbour struct {
	idx int
	cos float64
}

// neighbours returns unit i's neighbours among us: everyone with cosine ≥
// MinCosine, or the TopK nearest when that is more. Ties rank by Order.
func neighbours(us []model.ChangeUnit, vecs map[string][]float32, i int, o EmbedOptions) []neighbour {
	var cand []neighbour
	for j, v := range us {
		if j == i || sameShape(us[i], v) {
			continue
		}
		c, ok := cosine(vecs[us[i].ID], vecs[v.ID])
		if !ok {
			continue
		}
		cand = append(cand, neighbour{idx: j, cos: c})
	}
	sort.SliceStable(cand, func(a, b int) bool {
		if cand[a].cos != cand[b].cos {
			return cand[a].cos > cand[b].cos
		}
		return us[cand[a].idx].Order < us[cand[b].idx].Order
	})
	above := 0
	for above < len(cand) && cand[above].cos >= o.MinCosine {
		above++
	}
	return cand[:min(len(cand), max(above, o.TopK))]
}

// sameShape reports whether two units share a non-empty shape hash.
func sameShape(a, b model.ChangeUnit) bool {
	return a.ShapeHash != "" && a.ShapeHash == b.ShapeHash
}

// usable reports whether v is a non-empty, non-zero vector.
func usable(v []float32) bool {
	for _, x := range v {
		if x != 0 {
			return true
		}
	}
	return false
}

// cosine is the cosine similarity of a and b (as embed.Cosine); ok is false
// when the lengths differ or either vector is zero.
func cosine(a, b []float32) (float64, bool) {
	if len(a) != len(b) {
		return 0, false
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0, false
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), true
}

// MechanicalCandidates links units with identical, non-empty shape hashes,
// Kind mechanical, Source embed, Weight 1. A shape class is partitioned by
// shape.Key (shape hash plus literal edit signature): each partition of ≥ 2
// units gets a star from its root (lowest Order) to its other members, and
// the class root links to the root of every other partition, so units that
// share a shape but make another literal edit stay candidates (for Jev)
// without being compared only against one example.
func MechanicalCandidates(units []model.ChangeUnit) []model.Link {
	us := sortedUnits(units)
	ls := newLinkSet(us)
	classRoot := map[string]string{}
	partRoot := map[string]string{}
	for _, u := range us {
		if u.ShapeHash == "" {
			continue
		}
		k := shape.Key(u)
		root, ok := partRoot[k]
		if ok {
			ls.add(root, u.ID, model.LinkMechanical, model.LinkEmbed, 1)
			continue
		}
		partRoot[k] = u.ID
		if cr, ok := classRoot[u.ShapeHash]; ok {
			ls.add(cr, u.ID, model.LinkMechanical, model.LinkEmbed, 1)
			continue
		}
		classRoot[u.ShapeHash] = u.ID
	}
	return ls.sorted()
}

// UnitText is the text embedded per unit (spec stage 3): path, signature
// and the diff's added and removed lines (prefixes kept, context lines
// dropped), joined by newlines. Embedding recordings are keyed by this text,
// so changing it invalidates testdata/recordings/embed.
func UnitText(u model.ChangeUnit) string {
	var b strings.Builder
	b.WriteString(u.Path)
	b.WriteByte('\n')
	b.WriteString(u.Signature)
	for _, l := range strings.Split(u.Diff, "\n") {
		if strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
			b.WriteByte('\n')
			b.WriteString(l)
		}
	}
	return b.String()
}
