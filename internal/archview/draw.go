package archview

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/theme"
)

// seg is a piece of text in one colour role.
type seg struct {
	text string
	st   sty
}

// renderSegs renders segments joined by sep (dim).
func renderSegs(t theme.Theme, segs []seg, sep string) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		parts = append(parts, styleOf(t, s.st).Render(s.text))
	}
	return strings.Join(parts, t.Dim.Render(sep))
}

// statusGlyph is a module's status mark: ● changed, + new, ✕ removed,
// nothing for context.
func statusGlyph(m model.ArchModule) (string, sty) {
	switch m.Status {
	case model.ArchChanged:
		return "●", stAccent
	case model.ArchNew:
		return "+", stAdd
	case model.ArchRemoved:
		return "✕", stDel
	}
	return "", stDim
}

// headSegs is a module's first line: status glyph, ★ when owned, path
// (cut from the left to fit n cells).
func headSegs(m model.ArchModule, n int) []seg {
	var out []seg
	used := 0
	if g, st := statusGlyph(m); g != "" {
		out = append(out, seg{g + " ", st})
		used += 2
	}
	if m.Owned {
		out = append(out, seg{"★ ", stBlue})
		used += 2
	}
	st := stText
	if m.Status == model.ArchContext {
		st = stDim
	}
	return append(out, seg{leftTrunc(m.ID, n-used), st})
}

// leftTrunc keeps the end of s within n cells ("…internal/users").
func leftTrunc(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= n {
		return s
	}
	r := []rune(s)
	return "…" + string(r[len(r)-(n-1):])
}

// factSegs is a module's second line: needs-eyes count or attention word,
// max risk, ✗ untested, ? unsure, +N undoc, ⚠N breaking.
func factSegs(m model.ArchModule) []seg {
	var out []seg
	switch {
	case m.Eyes > 0:
		out = append(out, seg{fmt.Sprintf("%d eyes", m.Eyes), stAccent})
	case m.Attention == model.AttnSkim:
		out = append(out, seg{"skim", stSkim})
	case m.Attention == model.AttnSkip:
		out = append(out, seg{"skip", stDim})
	case m.Units > 0:
		out = append(out, seg{plural(m.Units, "unit", "units"), stDim})
	case m.Status == model.ArchRemoved:
		out = append(out, seg{"removed", stDel})
	default:
		out = append(out, seg{plural(m.FileCount, "file", "files"), stDim})
	}
	if m.RiskMax > 0 {
		st := stDim
		if m.RiskMax >= 4 {
			st = stAccent
		}
		out = append(out, seg{fmt.Sprintf("r%d", m.RiskMax), st})
	}
	if m.Untested > 0 {
		out = append(out, seg{"✗", stDel})
	}
	if m.Unsure > 0 {
		out = append(out, seg{"?", stBlue})
	}
	if m.Undocumented > 0 {
		out = append(out, seg{fmt.Sprintf("+%d undoc", m.Undocumented), stAccent})
	}
	if m.Breaking > 0 {
		out = append(out, seg{fmt.Sprintf("⚠%d", m.Breaking), stAccent})
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// edgeStyleOf is how an edge of status st is drawn.
func edgeStyleOf(st string) edgeStyle {
	switch st {
	case model.ArchNew:
		return edgeStyle{st: stAdd, prio: 3}
	case model.ArchRemoved:
		return edgeStyle{st: stDel, prio: 2, dashed: true}
	}
	return edgeStyle{st: stLine, prio: 1}
}

// attach is one edge end on a box side.
type attach struct {
	edge   int
	otherX int  // centre column of the module at the other end
	from   bool // the importer's end (a stub), else the import's (an arrow)
}

// ends are the attachment columns of every edge: from (stub) and to
// (arrow), spread evenly along the box side they leave or enter.
type ends struct{ from, to map[int]int }

// boxSide is one side of a module box.
type boxSide struct {
	id     string
	bottom bool
}

// spreadEnds spreads edge ends along box sides, ordered by where the other
// end sits: a down edge leaves its importer's bottom and enters the
// import's top; an upward (cycle) edge leaves the top and enters the
// bottom.
func spreadEnds(s shown, l layout) ends {
	sides := map[boxSide][]attach{}
	for i, e := range s.edges {
		a, b := l.boxes[e.From], l.boxes[e.To]
		down := !l.back[i]
		fs, ts := boxSide{e.From, down}, boxSide{e.To, !down}
		sides[fs] = append(sides[fs], attach{i, b.center().x, true})
		sides[ts] = append(sides[ts], attach{i, a.center().x, false})
	}
	out := ends{from: map[int]int{}, to: map[int]int{}}
	for sd, as := range sides {
		sort.SliceStable(as, func(i, j int) bool {
			if as[i].otherX != as[j].otherX {
				return as[i].otherX < as[j].otherX
			}
			return as[i].edge < as[j].edge
		})
		box := l.boxes[sd.id]
		for k, a := range as {
			x := box.x + 1 + (k+1)*(boxW-2)/(len(as)+1)
			if a.from {
				out.from[a.edge] = x
			} else {
				out.to[a.edge] = x
			}
		}
	}
	return out
}

// drawGraph draws the shown modules and edges on a canvas of the layout's
// size; focus is the focused module id.
func drawGraph(s shown, l layout, focus string) *canvas {
	c := newCanvas(l.w, l.h)
	en := spreadEnds(s, l)
	type arrow struct {
		x, y int
		e    model.ArchEdge
	}
	type stub struct {
		x, y int
		up   bool // leaves through the top border (an upward edge)
		id   string
	}
	arrows := make([]arrow, 0, len(s.edges))
	stubs := make([]stub, 0, len(s.edges))
	for i, e := range s.edges {
		a, b := l.boxes[e.From], l.boxes[e.To]
		es := edgeStyleOf(e.Status)
		sx, tx := en.from[i], en.to[i]
		var lane, laneB, ay int
		head := '▼'
		if l.back[i] {
			lane, laneB, ay, head = a.y-1, b.y+boxH+1, b.y+boxH, '▲'
			c.mark(sx, lane, dDown, es)
			stubs = append(stubs, stub{sx, a.y, true, e.From})
		} else {
			lane, laneB, ay = a.y+boxH, b.y-2, b.y-1
			c.mark(sx, lane, dUp, es)
			stubs = append(stubs, stub{sx, a.y + boxH - 1, false, e.From})
		}
		c.path(es, routeEdge(l, point{sx, lane}, lane, laneB, tx, ay)...)
		arrows = append(arrows, arrow{tx, ay, e})
		c.set(tx, ay, head, es.st)
	}
	for _, ar := range arrows {
		edgeMarks(c, ar.x, ar.y, ar.e)
	}
	byID := map[string]model.ArchModule{}
	for _, m := range s.mods {
		byID[m.ID] = m
		drawBox(c, l.boxes[m.ID], m, m.ID == focus)
	}
	for _, st := range stubs {
		r, rs := junction(byID[st.id], st.id == focus, st.up)
		c.set(st.x, st.y, r, rs)
	}
	return c
}

// junction is the border rune where an edge leaves a box (┬ on the bottom
// border, ┴ on the top; heavy for the focused box) in the border's style.
func junction(m model.ArchModule, focused, top bool) (rune, sty) {
	bc, bs := borderOf(m, focused)
	switch {
	case bc[4] == '━' && top:
		return '┻', bs
	case bc[4] == '━':
		return '┳', bs
	case top:
		return '┴', bs
	}
	return '┬', bs
}

// routeEdge is the connector from a stub at s to an arrow at (tx, ay):
// along the lane next to the source, then (for edges spanning more than
// one layer) down or up the nearest channel to the lane next to the
// target.
func routeEdge(l layout, s point, lane, laneB, tx, ay int) []point {
	if lane == laneB {
		return []point{s, {s.x, lane}, {tx, lane}, {tx, ay}}
	}
	ch := l.nearestChannel(tx)
	return []point{s, {s.x, lane}, {ch, lane}, {ch, laneB}, {tx, laneB}, {tx, ay}}
}

// minLabel is the fewest free cells a contract label needs beside its arrow.
const minLabel = 10

// edgeMarks writes an edge's marks next to its arrow, into empty cells
// only: "+" for a new edge, "−" for a removed one, "⚠ <first contract>".
func edgeMarks(c *canvas, x, y int, e model.ArchEdge) {
	switch e.Status {
	case model.ArchNew:
		if c.empty(x-1, y) {
			c.set(x-1, y, '+', stAdd)
		}
	case model.ArchRemoved:
		if c.empty(x-1, y) {
			c.set(x-1, y, '−', stDel)
		}
	}
	if len(e.Contracts) == 0 || !c.empty(x+1, y) || !c.empty(x+2, y) {
		return
	}
	free := 0
	for c.empty(x+1+free, y) && free < boxW {
		free++
	}
	if free < minLabel {
		return
	}
	c.text(x+2, y, theme.Truncate("⚠ "+e.Contracts[0], free-2), stAccent)
}

// boxChars are a box's corner and edge runes: tl tr bl br horizontal vertical.
type boxChars [6]rune

// borderOf is a module box's border: attention colour (eyes accent, skim
// warm, otherwise the plain border), dashed red when removed, heavy and
// bright when focused.
func borderOf(m model.ArchModule, focused bool) (boxChars, sty) {
	switch {
	case focused:
		return boxChars{'┏', '┓', '┗', '┛', '━', '┃'}, stBright
	case m.Status == model.ArchRemoved:
		return boxChars{'┌', '┐', '└', '┘', '╌', '╎'}, stDel
	}
	plain := boxChars{'┌', '┐', '└', '┘', '─', '│'}
	switch m.Attention {
	case model.AttnEyes:
		return plain, stAccent
	case model.AttnSkim:
		return plain, stSkim
	}
	return plain, stBorder
}

// drawBox draws one module box at r.
func drawBox(c *canvas, r rect, m model.ArchModule, focused bool) {
	bc, bs := borderOf(m, focused)
	for y := r.y; y < r.y+r.h; y++ {
		for x := r.x; x < r.x+r.w; x++ {
			c.set(x, y, ' ', stText)
		}
	}
	c.set(r.x, r.y, bc[0], bs)
	c.set(r.x+r.w-1, r.y, bc[1], bs)
	c.set(r.x, r.y+r.h-1, bc[2], bs)
	c.set(r.x+r.w-1, r.y+r.h-1, bc[3], bs)
	for x := r.x + 1; x < r.x+r.w-1; x++ {
		c.set(x, r.y, bc[4], bs)
		c.set(x, r.y+r.h-1, bc[4], bs)
	}
	for y := r.y + 1; y < r.y+r.h-1; y++ {
		c.set(r.x, y, bc[5], bs)
		c.set(r.x+r.w-1, y, bc[5], bs)
	}
	inner := r.w - 4
	writeSegs(c, r.x+2, r.y+1, headSegs(m, inner), "", inner)
	writeSegs(c, r.x+2, r.y+2, factSegs(m), " · ", inner)
}

// writeSegs writes segments joined by sep from (x, y), at most n cells
// (the last segment is cut with "…").
func writeSegs(c *canvas, x, y int, segs []seg, sep string, n int) {
	end := x + n
	for i, s := range segs {
		if i > 0 && sep != "" {
			if x+ansi.StringWidth(sep) >= end {
				return
			}
			x = c.text(x, y, sep, stDim)
		}
		room := end - x
		if room <= 0 {
			return
		}
		x = c.text(x, y, theme.Truncate(s.text, room), s.st)
	}
}
