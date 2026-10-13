package archview

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/theme"
)

// sty is a colour role of one canvas cell.
type sty int

const (
	stText sty = iota
	stDim
	stLine
	stAdd
	stDel
	stAccent
	stSkim
	stBorder
	stBright
	stBlue
)

// styleOf maps a role to the theme's style.
func styleOf(t theme.Theme, s sty) lipgloss.Style {
	fg := func(c lipgloss.TerminalColor) lipgloss.Style { return t.R.NewStyle().Foreground(c) }
	switch s {
	case stDim:
		return t.Dim
	case stLine:
		return fg(t.P.Line)
	case stAdd:
		return t.Add
	case stDel:
		return t.Del
	case stAccent:
		return t.Accent
	case stSkim:
		return fg(t.P.SkimFg)
	case stBorder:
		return fg(t.P.Border)
	case stBright:
		return t.Bright
	case stBlue:
		return t.Blue
	}
	return t.Text
}

// Line directions of a connector cell.
const (
	dUp uint8 = 1 << iota
	dDown
	dLeft
	dRight
)

// cell is one canvas cell: a fixed rune (text, box, arrow) or a connector
// whose glyph follows from the directions drawn through it.
type cell struct {
	r     rune // fixed rune; 0 for a connector or an empty cell
	st    sty
	mask  uint8 // connector directions
	solid bool  // a non-dashed edge passes (else a dashed one drew the cell)
	prio  int   // style priority of the edges through the cell
}

// canvas is a fixed-size grid the graph is drawn on.
type canvas struct {
	w, h  int
	cells []cell
}

func newCanvas(w, h int) *canvas {
	return &canvas{w: w, h: h, cells: make([]cell, max(0, w*h))}
}

func (c *canvas) in(x, y int) bool { return x >= 0 && y >= 0 && x < c.w && y < c.h }

func (c *canvas) at(x, y int) *cell { return &c.cells[y*c.w+x] }

// empty reports whether (x, y) holds nothing yet.
func (c *canvas) empty(x, y int) bool {
	if !c.in(x, y) {
		return false
	}
	cl := c.at(x, y)
	return cl.r == 0 && cl.mask == 0
}

// set puts a fixed rune at (x, y).
func (c *canvas) set(x, y int, r rune, s sty) {
	if c.in(x, y) {
		*c.at(x, y) = cell{r: r, st: s}
	}
}

// text writes s from (x, y) on, one rune per cell, cut at the canvas edge.
func (c *canvas) text(x, y int, s string, st sty) int {
	for _, r := range s {
		c.set(x, y, r, st)
		x++
	}
	return x
}

// edgeStyle is how one edge is drawn.
type edgeStyle struct {
	st     sty
	prio   int
	dashed bool
}

// link draws a connector step from (x, y) towards direction d (and the
// opposite direction into the neighbour cell).
func (c *canvas) link(x, y int, d uint8, es edgeStyle) {
	nx, ny, back := x, y, uint8(0)
	switch d {
	case dUp:
		ny, back = y-1, dDown
	case dDown:
		ny, back = y+1, dUp
	case dLeft:
		nx, back = x-1, dRight
	case dRight:
		nx, back = x+1, dLeft
	}
	c.mark(x, y, d, es)
	c.mark(nx, ny, back, es)
}

// mark adds direction d to the connector at (x, y); fixed runes win.
func (c *canvas) mark(x, y int, d uint8, es edgeStyle) {
	if !c.in(x, y) {
		return
	}
	cl := c.at(x, y)
	if cl.r != 0 {
		return
	}
	cl.mask |= d
	cl.solid = cl.solid || !es.dashed
	if es.prio >= cl.prio {
		cl.prio, cl.st = es.prio, es.st
	}
}

// path draws a connector through the points (each consecutive pair on one
// row or column).
func (c *canvas) path(es edgeStyle, pts ...point) {
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		for a != b {
			d, next := step(a, b)
			c.link(a.x, a.y, d, es)
			a = next
		}
	}
}

// step is one move from a towards b.
func step(a, b point) (uint8, point) {
	switch {
	case b.y > a.y:
		return dDown, point{a.x, a.y + 1}
	case b.y < a.y:
		return dUp, point{a.x, a.y - 1}
	case b.x > a.x:
		return dRight, point{a.x + 1, a.y}
	}
	return dLeft, point{a.x - 1, a.y}
}

// glyph is a connector's box-drawing rune.
func glyph(mask uint8, dashed bool) rune {
	v, h := mask&(dUp|dDown) != 0, mask&(dLeft|dRight) != 0
	switch mask {
	case dDown | dRight:
		return '┌'
	case dDown | dLeft:
		return '┐'
	case dUp | dRight:
		return '└'
	case dUp | dLeft:
		return '┘'
	case dUp | dDown | dRight:
		return '├'
	case dUp | dDown | dLeft:
		return '┤'
	case dLeft | dRight | dDown:
		return '┬'
	case dLeft | dRight | dUp:
		return '┴'
	case dUp | dDown | dLeft | dRight:
		return '┼'
	}
	switch {
	case v && dashed:
		return '╎'
	case v:
		return '│'
	case h && dashed:
		return '╌'
	case h:
		return '─'
	}
	return ' '
}

// lines renders the canvas, one styled string per row (runs of one style
// rendered together; trailing blanks trimmed).
func (c *canvas) lines(t theme.Theme) []string {
	out := make([]string, c.h)
	for y := 0; y < c.h; y++ {
		var b strings.Builder
		var run []rune
		cur := sty(-1)
		flush := func() {
			if len(run) == 0 {
				return
			}
			s := string(run)
			if strings.TrimSpace(s) == "" {
				b.WriteString(s)
			} else {
				b.WriteString(styleOf(t, cur).Render(s))
			}
			run = run[:0]
		}
		end := c.w
		for end > 0 && c.empty(end-1, y) {
			end--
		}
		for x := 0; x < end; x++ {
			cl := c.at(x, y)
			r, st := cl.r, cl.st
			if r == 0 && cl.mask != 0 {
				r = glyph(cl.mask, !cl.solid)
			}
			if r == 0 {
				r, st = ' ', cur
			}
			if st != cur {
				flush()
				cur = st
			}
			run = append(run, r)
		}
		flush()
		out[y] = b.String()
	}
	return out
}
