// Package theme is prt's shared look (docs/design/README.md): one palette of
// colour roles taken from the design canvas, adaptive to dark and light
// terminals and degrading to 256 and 16 colours, plus the small drawing
// pieces every screen reuses (titled boxes, section dividers, the urgency
// bar, chips, tabs, the bottom key hint bar).
//
// Every piece is built on a *lipgloss.Renderer, so a renderer bound to a
// non-TTY (snapshots, pipes, tests) produces plain text: no escape codes,
// and every meaning carried by colour is also carried by a glyph or word.
// Package owners (codeview, pipeview, archview, searchview, tui) use the
// roles here instead of their own hex values. Fixed by the M5 lead; work
// packages may not change it (add local styles in their own package
// instead and report what they would add).
package theme

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/fredricw/prt/internal/model"
)

// colour builds an adaptive role: dark truecolor/256/16 and light
// truecolor/256/16 values.
func colour(dHex, d256, d16, lHex, l256, l16 string) lipgloss.CompleteAdaptiveColor {
	return lipgloss.CompleteAdaptiveColor{
		Dark:  lipgloss.CompleteColor{TrueColor: dHex, ANSI256: d256, ANSI: d16},
		Light: lipgloss.CompleteColor{TrueColor: lHex, ANSI256: l256, ANSI: l16},
	}
}

// Palette holds the colour roles. Dark values are the design canvas's
// (bg #0e1014, text #d4d4cf, dim #8b929c, accent #f0a63a, blue #79b8ff,
// add #7ee2a8, del #ff9b9b, borders #2b313c); light values are chosen to
// keep the same roles readable on a light background.
type Palette struct {
	Bg, Text, Dim, Bright, Accent, Blue, Add, Del lipgloss.TerminalColor
	// Border is box borders, Line graph edges, Faint the "·" gutter.
	Border, Line, Faint lipgloss.TerminalColor
	// SelBg is the selected row's background.
	SelBg lipgloss.TerminalColor
	// Chips: plain, warn (risk, breaking), bad (tests ✗), info (unsure,
	// routing).
	ChipBg, ChipFg, WarnBg, WarnFg, BadBg, BadFg, InfoBg, InfoFg lipgloss.TerminalColor
	// Diff lines: added, deleted, context foreground.
	AddLineBg, AddLineFg, DelLineBg, DelLineFg, CtxFg lipgloss.TerminalColor
	// Intra-line emphasis backgrounds for the changed characters of a
	// paired added/deleted line.
	AddEmphBg, DelEmphBg lipgloss.TerminalColor
	// Attention: skim text and cells, skip cells (eyes uses Accent).
	SkimFg, SkimCell, SkipCell lipgloss.TerminalColor
	// Suspicious gutter shades (score 3–4 and 5).
	Susp1Bg, Susp1Fg, Susp2Bg, Susp2Fg lipgloss.TerminalColor
	// Key hint caps.
	KeyBg, KeyFg lipgloss.TerminalColor
	// Syntax roles (tree-sitter capture classes).
	SynKeyword, SynString, SynComment, SynNumber, SynType, SynFunction lipgloss.TerminalColor
}

// Default returns the design palette.
func Default() Palette {
	return Palette{
		Bg:          colour("#0e1014", "233", "0", "#ffffff", "231", "15"),
		Text:        colour("#d4d4cf", "252", "7", "#24292f", "235", "0"),
		Dim:         colour("#8b929c", "246", "8", "#6e7781", "243", "8"),
		Bright:      colour("#f2f2ec", "255", "15", "#1f2328", "234", "0"),
		Accent:      colour("#f0a63a", "214", "11", "#b35900", "130", "3"),
		Blue:        colour("#79b8ff", "111", "12", "#0969da", "26", "4"),
		Add:         colour("#7ee2a8", "115", "10", "#1a7f37", "28", "2"),
		Del:         colour("#ff9b9b", "210", "9", "#cf222e", "160", "1"),
		Border:      colour("#2b313c", "237", "8", "#d0d7de", "252", "7"),
		Line:        colour("#5b6371", "241", "8", "#8c959f", "245", "8"),
		Faint:       colour("#3e4550", "239", "8", "#afb8c1", "250", "7"),
		SelBg:       colour("#1b2130", "235", "0", "#ddf4ff", "195", "7"),
		ChipBg:      colour("#232933", "236", "0", "#eaeef2", "255", "7"),
		ChipFg:      colour("#e6e6e0", "254", "15", "#24292f", "235", "0"),
		WarnBg:      colour("#4a3410", "58", "3", "#fff1c2", "230", "11"),
		WarnFg:      colour("#ffd28f", "222", "0", "#7d4e00", "94", "0"),
		BadBg:       colour("#3a1a1c", "52", "1", "#ffebe9", "224", "9"),
		BadFg:       colour("#ffb3b3", "217", "15", "#a40e26", "124", "0"),
		InfoBg:      colour("#16263a", "17", "4", "#ddf4ff", "195", "14"),
		InfoFg:      colour("#a8d1ff", "153", "15", "#0550ae", "25", "0"),
		AddLineBg:   colour("#12261c", "22", "0", "#dafbe1", "194", "15"),
		AddLineFg:   colour("#9ff0c0", "157", "10", "#116329", "22", "2"),
		DelLineBg:   colour("#2a1517", "52", "0", "#ffebe9", "224", "15"),
		DelLineFg:   colour("#ffb3b3", "217", "9", "#82071e", "88", "1"),
		CtxFg:       colour("#b4b8bf", "250", "7", "#57606a", "241", "8"),
		AddEmphBg:   colour("#1f5236", "29", "2", "#aceebb", "157", "10"),
		DelEmphBg:   colour("#6b2a2e", "88", "1", "#ffcecb", "217", "9"),
		SkimFg:      colour("#a78445", "137", "3", "#9a6700", "136", "3"),
		SkimCell:    colour("#6b5426", "94", "3", "#d4a72c", "178", "3"),
		SkipCell:    colour("#2e343e", "237", "8", "#d0d7de", "252", "7"),
		Susp1Bg:     colour("#3a2a14", "58", "3", "#fff8c5", "230", "11"),
		Susp1Fg:     colour("#f0c27a", "222", "0", "#7d4e00", "94", "0"),
		Susp2Bg:     colour("#6b4614", "94", "11", "#ffd8b5", "223", "11"),
		Susp2Fg:     colour("#ffd28f", "222", "0", "#953800", "130", "0"),
		KeyBg:       colour("#2b313c", "237", "8", "#eaeef2", "255", "7"),
		KeyFg:       colour("#e6e6e0", "254", "15", "#24292f", "235", "0"),
		SynKeyword:  colour("#79b8ff", "111", "12", "#cf222e", "160", "1"),
		SynString:   colour("#e6c07b", "180", "3", "#0a3069", "17", "4"),
		SynComment:  colour("#8b929c", "246", "8", "#6e7781", "243", "8"),
		SynNumber:   colour("#f0a63a", "214", "11", "#0550ae", "25", "4"),
		SynType:     colour("#a8d1ff", "153", "14", "#953800", "130", "3"),
		SynFunction: colour("#f2f2ec", "255", "15", "#8250df", "98", "5"),
	}
}

// Theme is the palette bound to a renderer, with the common styles.
type Theme struct {
	R *lipgloss.Renderer
	P Palette
	// Colour is false when the renderer emits no colour (snapshots, pipes,
	// NO_COLOR): pieces then fall back to glyphs and plain separators.
	Colour bool

	Text, Dim, Bright, Bold, Accent, Blue, Add, Del lipgloss.Style
	// Title is a block or page title; Selected the selected row (background
	// plus bold; plain text without colour, so callers keep a "▸" marker).
	Title, Selected lipgloss.Style
}

// New binds the default palette to r (nil: lipgloss's default renderer).
func New(r *lipgloss.Renderer) Theme {
	if r == nil {
		r = lipgloss.DefaultRenderer()
	}
	p := Default()
	fg := func(c lipgloss.TerminalColor) lipgloss.Style { return r.NewStyle().Foreground(c) }
	return Theme{
		R: r, P: p, Colour: r.ColorProfile() != termenv.Ascii,
		Text: fg(p.Text), Dim: fg(p.Dim), Bright: fg(p.Bright).Bold(true), Bold: r.NewStyle().Bold(true),
		Accent: fg(p.Accent), Blue: fg(p.Blue), Add: fg(p.Add), Del: fg(p.Del),
		Title:    fg(p.Bright).Bold(true),
		Selected: r.NewStyle().Background(p.SelBg).Bold(true),
	}
}

// BoxOpt tunes Box.
type BoxOpt struct {
	// Height is the total height including borders (0: fit the body).
	Height int
	// Focused draws the border in the accent colour and, without colour,
	// with a heavy line (┏━┓) so focus stays visible.
	Focused bool
	// Right is dim text set into the top border at the right end
	// ("group 3 of 7").
	Right string
	// NoPad drops the one-cell padding inside the side borders.
	NoPad bool
}

// Box draws body inside a border of total width w whose title is set into
// the top edge (the design's "boxes with titled borders"):
//
//	┌─ Queue · ranked ─────┐
//	│ body                 │
//	└──────────────────────┘
//
// Body lines longer than the inside are cut with "…"; shorter ones are
// padded. w below 4 returns the body unchanged.
func (t Theme) Box(title, body string, w int, o BoxOpt) string {
	if w < 4 {
		return body
	}
	tl, tr, bl, br, hz, vt := "┌", "┐", "└", "┘", "─", "│"
	if o.Focused && !t.Colour {
		tl, tr, bl, br, hz, vt = "┏", "┓", "┗", "┛", "━", "┃"
	}
	bs := t.R.NewStyle().Foreground(t.P.Border)
	if o.Focused {
		bs = t.R.NewStyle().Foreground(t.P.Accent)
	}
	pad := 1
	if o.NoPad {
		pad = 0
	}
	inner := w - 2 - 2*pad

	// Top edge: ┌─ title ── right ─┐
	top := hz
	if title != "" {
		top += " " + t.Dim.Render(Truncate(title, max(1, w-6))) + " "
	}
	right := ""
	if o.Right != "" {
		right = " " + t.Dim.Render(o.Right) + " " + hz
	}
	fill := w - 2 - ansi.StringWidth(top) - ansi.StringWidth(right)
	if fill < 0 && right != "" {
		right, fill = "", w-2-ansi.StringWidth(top)
	}
	if fill < 0 {
		top, fill = Truncate(top, w-2), 0
	}
	topLine := bs.Render(tl) + styleEdges(bs, top, hz) + bs.Render(strings.Repeat(hz, fill)) + styleEdges(bs, right, hz) + bs.Render(tr)

	lines := strings.Split(body, "\n")
	if o.Height > 0 {
		n := max(0, o.Height-2)
		if len(lines) > n {
			lines = lines[:n]
		}
		for len(lines) < n {
			lines = append(lines, "")
		}
	}
	var b strings.Builder
	b.WriteString(topLine)
	sp := strings.Repeat(" ", pad)
	for _, l := range lines {
		l = Truncate(l, inner)
		b.WriteString("\n" + bs.Render(vt) + sp + l + strings.Repeat(" ", max(0, inner-ansi.StringWidth(l))) + sp + bs.Render(vt))
	}
	b.WriteString("\n" + bs.Render(bl+strings.Repeat(hz, w-2)+br))
	return b.String()
}

// styleEdges renders the border glyphs at the ends of s (a title segment
// like "─ title " or " right ─") in the border style, leaving the already
// styled text in between alone.
func styleEdges(bs lipgloss.Style, s, hz string) string {
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, hz) {
		return bs.Render(hz) + s[len(hz):]
	}
	if strings.HasSuffix(s, hz) {
		return s[:len(s)-len(hz)] + bs.Render(hz)
	}
	return s
}

// Divider is a dim section divider of width w: "── NEEDS YOU ─────".
func (t Theme) Divider(label string, w int) string {
	if w <= 0 {
		return ""
	}
	s := "──"
	if label != "" {
		s += " " + label + " "
	}
	if n := w - ansi.StringWidth(s); n > 0 {
		s += strings.Repeat("─", n)
	}
	return t.Dim.Render(Truncate(s, w))
}

// UrgencyBar draws the picker's four-cell urgency bar: q filled cells "▮"
// (accent) then empty cells "▯" (dim); q is clamped to 0–4.
func (t Theme) UrgencyBar(q int) string {
	q = max(0, min(4, q))
	return t.Accent.Render(strings.Repeat("▮", q)) + t.Dim.Render(strings.Repeat("▯", 4-q))
}

// Hint is one key + action pair of the bottom key hint bar.
type Hint struct{ Key, Desc string }

// HintsOf turns enabled bindings into hints (their help key and text).
func HintsOf(bs ...key.Binding) []Hint {
	out := make([]Hint, 0, len(bs))
	for _, b := range bs {
		if !b.Enabled() || b.Help().Key == "" {
			continue
		}
		out = append(out, Hint{Key: b.Help().Key, Desc: b.Help().Desc})
	}
	return out
}

// Hints renders the bottom key hint bar ("j/k move  1 2 3 tabs  ? help"):
// keys as caps (inverse in colour, bold-free plain text without), pairs
// separated by two spaces, cut at a whole pair to fit w (an ellipsis marks
// the cut).
func (t Theme) Hints(hs []Hint, w int) string {
	ks := t.R.NewStyle().Background(t.P.KeyBg).Foreground(t.P.KeyFg)
	var b strings.Builder
	used := 0
	for i, h := range hs {
		cell := h.Key
		if t.Colour {
			cell = ks.Render(" " + h.Key + " ")
		}
		part := cell + " " + t.Dim.Render(h.Desc)
		sep := ""
		if i > 0 {
			sep = "  "
		}
		pw := ansi.StringWidth(sep + part)
		if w > 0 && used+pw > w {
			if used+2 <= w {
				b.WriteString(t.Dim.Render(" …"))
			}
			break
		}
		b.WriteString(sep + part)
		used += pw
	}
	return b.String()
}

// ChipKind selects a chip's colours.
type ChipKind int

// Chip kinds (design: plain, warm for risk, red for missing tests, blue
// outline for unsure, blue for routing).
const (
	ChipPlain ChipKind = iota
	ChipWarn
	ChipBad
	ChipUnsure
	ChipRoute
)

// Chip is one label chip.
type Chip struct {
	Kind ChipKind
	Text string
}

// Chips renders chips in a row: with colour each is padded on its own
// background, one space apart; without colour they are joined by " · "
// (the plain layout the Code page goldens use).
func (t Theme) Chips(cs ...Chip) string {
	if !t.Colour {
		parts := make([]string, 0, len(cs))
		for _, c := range cs {
			parts = append(parts, c.Text)
		}
		return strings.Join(parts, " · ")
	}
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, t.chipStyle(c.Kind).Render(" "+c.Text+" "))
	}
	return strings.Join(parts, " ")
}

func (t Theme) chipStyle(k ChipKind) lipgloss.Style {
	s := t.R.NewStyle()
	switch k {
	case ChipWarn:
		return s.Background(t.P.WarnBg).Foreground(t.P.WarnFg)
	case ChipBad:
		return s.Background(t.P.BadBg).Foreground(t.P.BadFg)
	case ChipUnsure:
		return s.Foreground(t.P.InfoFg).Underline(true)
	case ChipRoute:
		return s.Background(t.P.InfoBg).Foreground(t.P.InfoFg)
	}
	return s.Background(t.P.ChipBg).Foreground(t.P.ChipFg)
}

// Tab renders one tab label: the active one inverted on the accent colour
// (design), others dim. Without colour the active tab is wrapped in
// brackets ("[2 Code]") so it stays visible.
func (t Theme) Tab(label string, on bool) string {
	if !on {
		return t.Dim.Render(" " + label + " ")
	}
	if !t.Colour {
		return "[" + label + "]"
	}
	return t.R.NewStyle().Background(t.P.Accent).Foreground(t.P.Bg).Bold(true).Render(" " + label + " ")
}

// Attention returns the style of an attention level (eyes accent, skim
// warm, skip faint) for minimap cells and module boxes.
func (t Theme) Attention(a model.Attention) lipgloss.Style {
	switch a {
	case model.AttnEyes:
		return t.Accent
	case model.AttnSkim:
		return t.R.NewStyle().Foreground(t.P.SkimFg)
	case model.AttnSkip:
		return t.R.NewStyle().Foreground(t.P.Line)
	}
	return t.Dim
}

// Tier returns the routing tier's style (v3 Pipeline: none dim, fast green,
// deep accent, escalated warm).
func (t Theme) Tier(tier model.Tier, escalated bool) lipgloss.Style {
	switch {
	case escalated:
		return t.R.NewStyle().Foreground(t.P.WarnFg)
	case tier == model.TierDeep:
		return t.Accent
	case tier == model.TierFast:
		return t.Add
	}
	return t.Dim
}

// Suspicious returns the gutter style of a suspicious mark by score
// (1..5): ≥ 4.5 the strong shade, ≥ 3 the light one, else the accent.
func (t Theme) Suspicious(score float64) lipgloss.Style {
	switch {
	case score >= 4.5:
		return t.R.NewStyle().Background(t.P.Susp2Bg).Foreground(t.P.Susp2Fg)
	case score >= 3:
		return t.R.NewStyle().Background(t.P.Susp1Bg).Foreground(t.P.Susp1Fg)
	}
	return t.Accent
}

// Syntax classes (tree-sitter capture classes the highlighter emits).
const (
	SynKeyword  = "keyword"
	SynString   = "string"
	SynComment  = "comment"
	SynNumber   = "number"
	SynType     = "type"
	SynFunction = "function"
)

// Syntax returns the foreground style of a syntax class; an unknown class
// (or "") is the plain text style. Callers layer it over a diff line's
// background.
func (t Theme) Syntax(class string) lipgloss.Style {
	s := t.R.NewStyle()
	switch class {
	case SynKeyword:
		return s.Foreground(t.P.SynKeyword)
	case SynString:
		return s.Foreground(t.P.SynString)
	case SynComment:
		return s.Foreground(t.P.SynComment).Italic(true)
	case SynNumber:
		return s.Foreground(t.P.SynNumber)
	case SynType:
		return s.Foreground(t.P.SynType)
	case SynFunction:
		return s.Foreground(t.P.SynFunction)
	}
	return s
}

// Spread puts left and right on one line of width w with right aligned to
// the end (left is cut first when both do not fit).
func Spread(left, right string, w int) string {
	rw := ansi.StringWidth(right)
	if rw >= w {
		return Truncate(right, w)
	}
	left = Truncate(left, max(0, w-rw-1))
	gap := w - ansi.StringWidth(left) - rw
	return left + strings.Repeat(" ", max(1, gap)) + right
}

// Truncate cuts s to at most n cells, ending in "…" when cut (ANSI and
// wide-character aware).
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…")
}

// Prob formats a probability the spec's way: two decimals without the
// leading zero (".86"; "1.00" for 1).
func Prob(p float64) string {
	s := fmt.Sprintf("%.2f", max(0, min(1, p)))
	return strings.TrimPrefix(s, "0")
}
