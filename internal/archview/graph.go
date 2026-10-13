package archview

import (
	"sort"

	"github.com/fredricw/prt/internal/model"
)

// Graph geometry: module boxes are boxW × boxH cells (border, two content
// lines, border), colGap cells apart; layers are layerGap rows apart (a
// lane row under the importers, where edges leave through a ┬ in the box
// border, and an arrow row over the imports).
const (
	boxW     = 22
	boxH     = 4
	colGap   = 4
	colStep  = boxW + colGap
	layerGap = 2
	rowStep  = boxH + layerGap
	marginX  = 1 // left channel column
)

type point struct{ x, y int }

// rect is a cell rectangle.
type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

func (r rect) center() point { return point{r.x + r.w/2, r.y + r.h/2} }

// shown is the part of the graph the current depth and diff-only show.
type shown struct {
	mods  []model.ArchModule // graph order
	idx   map[string]int
	edges []model.ArchEdge
}

// visible filters g: modules within depth hops (diff-only: touched modules
// that changed, were added or removed) and the edges between them.
func visible(g *model.ArchGraph, depth int, diffOnly bool) shown {
	s := shown{idx: map[string]int{}}
	if g == nil {
		return s
	}
	for _, m := range g.Modules {
		if m.Hop > depth || diffOnly && m.Status == model.ArchContext {
			continue
		}
		s.idx[m.ID] = len(s.mods)
		s.mods = append(s.mods, m)
	}
	for _, e := range g.Edges {
		_, a := s.idx[e.From]
		_, b := s.idx[e.To]
		if a && b && e.From != e.To {
			s.edges = append(s.edges, e)
		}
	}
	return s
}

// layout is where every shown module's box sits on the canvas.
type layout struct {
	boxes  map[string]rect
	layer  map[string]int
	back   map[int]bool // edge index (into shown.edges) drawn upwards (cycle broken)
	layers int
	cols   int
	x0     int // left edge of the (centred) grid
	w, h   int // canvas size used
}

// layoutGraph places s on a canvas of w × h cells: layer = longest path
// from an importer-free module over the shown edges (From above To),
// cycles broken by adding edges in path order and skipping any that would
// close one; boxes in a layer are ordered by the mean column of the
// modules importing them (layer 0 in graph order) and centred. ok is false
// when a layer is wider than the canvas or the layers taller than it.
func layoutGraph(s shown, w, h int) (layout, bool) {
	l := layout{boxes: map[string]rect{}, layer: map[string]int{}, back: map[int]bool{}}
	l.cols = max(0, (w-marginX+colGap)/colStep)
	if len(s.mods) == 0 || l.cols == 0 {
		return l, len(s.mods) == 0
	}
	succ := dag(s, l.back)
	l.layer = longestPath(s, succ)
	rows := orderLayers(s, succ, l.layer)
	l.layers = len(rows)
	for _, r := range rows {
		if len(r) > l.cols {
			return l, false
		}
	}
	if l.layers*rowStep-layerGap > h {
		return l, false
	}
	l.x0 = max(0, (w-(marginX+l.cols*colStep-colGap))/2)
	for li, r := range rows {
		start := (l.cols - len(r)) / 2
		for i, id := range r {
			l.boxes[id] = rect{x: l.x0 + marginX + (start+i)*colStep, y: li * rowStep, w: boxW, h: boxH}
		}
	}
	l.w, l.h = w, l.layers*rowStep-layerGap
	return l, true
}

// dag returns successor lists (module index → imported module indexes)
// with cycle-closing edges left out and recorded in back.
func dag(s shown, back map[int]bool) [][]int {
	succ := make([][]int, len(s.mods))
	for i, e := range s.edges {
		a, b := s.idx[e.From], s.idx[e.To]
		if reaches(succ, b, a) {
			back[i] = true
			continue
		}
		succ[a] = append(succ[a], b)
	}
	return succ
}

// reaches reports whether to is reachable from from.
func reaches(succ [][]int, from, to int) bool {
	seen := make([]bool, len(succ))
	stack := []int{from}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == to {
			return true
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		stack = append(stack, succ[n]...)
	}
	return false
}

// longestPath layers the DAG: a module sits one layer below the deepest
// module importing it.
func longestPath(s shown, succ [][]int) map[string]int {
	indeg := make([]int, len(s.mods))
	for _, ns := range succ {
		for _, n := range ns {
			indeg[n]++
		}
	}
	layer := make([]int, len(s.mods))
	var queue []int
	for i := range s.mods {
		if indeg[i] == 0 {
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, c := range succ[n] {
			layer[c] = max(layer[c], layer[n]+1)
			if indeg[c]--; indeg[c] == 0 {
				queue = append(queue, c)
			}
		}
	}
	out := make(map[string]int, len(s.mods))
	for i, m := range s.mods {
		out[m.ID] = layer[i]
	}
	return out
}

// orderLayers returns the module ids of each layer in column order.
func orderLayers(s shown, succ [][]int, layer map[string]int) [][]string {
	n := 0
	for _, l := range layer {
		n = max(n, l+1)
	}
	byLayer := make([][]int, n)
	for i, m := range s.mods {
		byLayer[layer[m.ID]] = append(byLayer[layer[m.ID]], i)
	}
	pred := make([][]int, len(s.mods))
	for a, ns := range succ {
		for _, b := range ns {
			pred[b] = append(pred[b], a)
		}
	}
	pos := make([]float64, len(s.mods))
	rows := make([][]string, n)
	for li, idxs := range byLayer {
		key := make(map[int]float64, len(idxs))
		for _, i := range idxs {
			key[i] = float64(i) // graph order for modules nothing above imports
			if li > 0 && len(pred[i]) > 0 {
				sum := 0.0
				for _, p := range pred[i] {
					sum += pos[p]
				}
				key[i] = sum / float64(len(pred[i]))
			}
		}
		sort.SliceStable(idxs, func(a, b int) bool {
			if key[idxs[a]] != key[idxs[b]] {
				return key[idxs[a]] < key[idxs[b]]
			}
			return idxs[a] < idxs[b]
		})
		for col, i := range idxs {
			pos[i] = float64(col) - float64(len(idxs)-1)/2
			rows[li] = append(rows[li], s.mods[i].ID)
		}
	}
	return rows
}

// channels are the free columns long edges run down: the left margin and
// the gaps between box columns.
func (l layout) channels() []int {
	out := []int{l.x0}
	for c := 0; c < l.cols; c++ {
		x := l.x0 + marginX + c*colStep + boxW + colGap/2 - 1
		if x < l.w {
			out = append(out, x)
		}
	}
	return out
}

// nearestChannel is the channel column closest to x (leftmost on ties).
func (l layout) nearestChannel(x int) int {
	best := 0
	for _, c := range l.channels() {
		if abs(c-x) < abs(best-x) {
			best = c
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
