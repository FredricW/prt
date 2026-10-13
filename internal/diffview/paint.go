package diffview

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/highlight"
	"github.com/fredricw/prt/internal/parse"
	"github.com/fredricw/prt/internal/theme"
)

// maxSyntaxBytes skips syntax colour for very long lines (minified files),
// where a parse per line is slow and the colour is noise.
const maxSyntaxBytes = 4096

// Syntax classes in the order of Painter's style table (0 is plain).
var classes = []string{"", theme.SynKeyword, theme.SynString, theme.SynComment, theme.SynNumber, theme.SynType, theme.SynFunction}

// numClasses is len(classes).
const numClasses = 7

func classIndex(c string) uint8 {
	for i, s := range classes {
		if s == c {
			return uint8(i)
		}
	}
	return 0
}

// opIndex maps a diff op to a row of Painter's style tables.
func opIndex(op diff.Op) int {
	switch op {
	case diff.Add:
		return 1
	case diff.Del:
		return 2
	}
	return 0
}

// Painter styles the text of diff lines for one renderer, shared by the
// plain viewer and the Code page (spec Diff rendering): the line's colour
// by op (the design's add/del line backgrounds), syntax classes from
// tree-sitter (highlight) as foregrounds over it, and the changed
// characters of a paired line on the emphasis background. Without colour it
// returns the cleaned text untouched, so colour-free output is identical
// with or without syntax and intra-line information.
type Painter struct {
	th  theme.Theme
	hl  *highlight.Highlighter
	op  [3]lipgloss.Style                // base style by opIndex
	seg [3][numClasses][2]lipgloss.Style // by op, class, emphasis
}

// NewPainter binds the theme to r; hl caches syntax spans (nil: a private
// cache).
func NewPainter(r *lipgloss.Renderer, hl *highlight.Highlighter) *Painter {
	if hl == nil {
		hl = highlight.New(0, 0)
	}
	th := theme.New(r)
	p := &Painter{th: th, hl: hl}
	ns := r.NewStyle
	p.op[0] = ns().Foreground(th.P.CtxFg)
	p.op[1] = ns().Background(th.P.AddLineBg).Foreground(th.P.AddLineFg)
	p.op[2] = ns().Background(th.P.DelLineBg).Foreground(th.P.DelLineFg)
	emph := [3]lipgloss.TerminalColor{nil, th.P.AddEmphBg, th.P.DelEmphBg}
	for o := range p.op {
		for c, name := range classes {
			s := p.op[o]
			if name != "" {
				s = th.Syntax(name).Inherit(p.op[o])
			}
			p.seg[o][c][0] = s
			p.seg[o][c][1] = s
			if emph[o] != nil {
				p.seg[o][c][1] = s.Background(emph[o]).Bold(true)
			}
		}
	}
	return p
}

// Colour reports whether the painter emits colour.
func (p *Painter) Colour() bool { return p.th.Colour }

// Theme is the painter's theme.
func (p *Painter) Theme() theme.Theme { return p.th }

// Base is the line style of op (the "+ " cell and background padding).
func (p *Painter) Base(op diff.Op) lipgloss.Style { return p.op[opIndex(op)] }

// Lang is the highlight language of a path ("" when unknown): the file's
// Lang when the caller has one, else from the extension.
func Lang(lang, path string) string {
	if lang != "" {
		return lang
	}
	return parse.Lang(path)
}

// Text is one diff line ready to draw: Clean is its sanitised text (tabs
// expanded, control characters replaced, clipped to the byte limit; Cut
// says it was clipped), runs its styled pieces.
type Text struct {
	Clean string
	Cut   bool
	p     *Painter
	op    int
	runs  []run // a partition of Clean (colour only)
}

// run is one styled piece of Text.Clean ending at byte end.
type run struct {
	end  int
	cls  uint8
	emph bool
}

// Prepare cleans raw (clipped to limit bytes) and, with colour, splits it
// into runs by syntax class (lang "" is plain) and the intra-line spans
// (byte offsets in raw).
func (p *Painter) Prepare(lang string, op diff.Op, raw string, intra []diff.Span, limit int) Text {
	text, cut := clip(raw, limit)
	t := Text{Cut: cut, p: p, op: opIndex(op)}
	if !p.th.Colour {
		t.Clean = clean(text)
		return t
	}
	cls := make([]uint8, len(text))
	emph := make([]bool, len(text))
	if lang != "" && len(raw) <= maxSyntaxBytes {
		for _, s := range p.hl.Line(lang, raw) {
			for i := s.Start; i < s.End && i < len(text); i++ {
				cls[i] = classIndex(s.Class)
			}
		}
	}
	for _, s := range intra {
		for i := max(0, s.Start); i < s.End && i < len(text); i++ {
			emph[i] = true
		}
	}
	t.Clean, t.runs = cleanRuns(text, cls, emph)
	return t
}

// cleanRuns cleans text rune by rune (as clean does) and records the runs
// of equal class and emphasis over the cleaned bytes.
func cleanRuns(text string, cls []uint8, emph []bool) (string, []run) {
	var b strings.Builder
	b.Grow(len(text))
	var runs []run
	for i, r := range text {
		switch {
		case r == '\t':
			b.WriteString(tabSpaces)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			b.WriteByte('?')
		default:
			b.WriteRune(r) // an invalid byte becomes U+FFFD, as with strings.Map
		}
		c, e := cls[i], emph[i]
		if n := len(runs); n > 0 && runs[n-1].cls == c && runs[n-1].emph == e {
			runs[n-1].end = b.Len()
			continue
		}
		runs = append(runs, run{end: b.Len(), cls: c, emph: e})
	}
	return b.String(), runs
}

// Render returns Clean[from:to] styled (plain without colour). from and to
// must be rune boundaries of Clean.
func (t Text) Render(from, to int) string {
	from, to = max(0, from), min(to, len(t.Clean))
	if from >= to {
		return ""
	}
	if t.p == nil || !t.p.th.Colour {
		return t.Clean[from:to]
	}
	var b strings.Builder
	start := 0
	for _, r := range t.runs {
		a, e := max(start, from), min(r.end, to)
		if a < e {
			b.WriteString(t.p.seg[t.op][r.cls][boolIndex(r.emph)].Render(t.Clean[a:e]))
		}
		start = r.end
		if start >= to {
			break
		}
	}
	return b.String()
}

// All renders the whole text.
func (t Text) All() string { return t.Render(0, len(t.Clean)) }

// WrapBounds splits s (cleaned text) into pieces of at most width cells and
// returns the start byte of each piece: [0] for a line that fits (or is
// empty). A rune wider than width gets a piece of its own.
func WrapBounds(s string, width int) []int {
	out := []int{0}
	if width < 1 {
		return out
	}
	used := 0
	for i, r := range s {
		w := 1
		if r >= 0x80 {
			w = ansi.StringWidth(string(r))
		}
		if used > 0 && used+w > width {
			out = append(out, i)
			used = 0
		}
		used += w
	}
	return out
}

func boolIndex(b bool) int {
	if b {
		return 1
	}
	return 0
}
