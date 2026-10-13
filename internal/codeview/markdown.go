package codeview

import (
	"strings"

	"github.com/charmbracelet/glamour"
	gstyles "github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/fredricw/prt/internal/diffview"
)

const (
	// minMarkdownWidth is the narrowest wrap width handed to Glamour.
	minMarkdownWidth = 12
	// maxMarkdownCache bounds the rendered-body cache; it is cleared when
	// full.
	maxMarkdownCache = 512
)

// mdKey identifies one rendered body.
type mdKey struct {
	body  string
	width int
}

// markdown renders comment bodies with Glamour, word-wrapped to a width,
// and caches the result by (body, width). Without colour (snapshots, tests,
// pipes) it uses the ASCII "notty" style, so output is deterministic;
// otherwise the dark or light style. Document margins are dropped: the
// comment blocks draw their own borders.
type markdown struct {
	colour bool
	cfg    func() glamour.TermRendererOption
	r      *lipgloss.Renderer
	trs    map[int]*glamour.TermRenderer // by wrap width
	cache  map[mdKey][]string
}

func newMarkdown(r *lipgloss.Renderer) *markdown {
	md := &markdown{r: r, trs: map[int]*glamour.TermRenderer{}, cache: map[mdKey][]string{}}
	md.colour = r.ColorProfile() != termenv.Ascii
	zero := uint(0)
	cfg := gstyles.NoTTYStyleConfig
	if md.colour {
		cfg = gstyles.LightStyleConfig
		if r.HasDarkBackground() {
			cfg = gstyles.DarkStyleConfig
		}
	}
	cfg.Document.Margin = &zero
	cfg.Document.BlockPrefix, cfg.Document.BlockSuffix = "", ""
	md.cfg = func() glamour.TermRendererOption { return glamour.WithStyles(cfg) }
	return md
}

// lines renders body wrapped at width cells and returns its lines without
// leading or trailing blank lines and without trailing padding. Control
// characters in the body are made visible first (diffview.Clean, line by
// line), so a comment cannot move the cursor or recolour the screen. On a
// Glamour error the cleaned body is returned as is.
func (md *markdown) lines(body string, width int) []string {
	width = max(minMarkdownWidth, width)
	k := mdKey{body: body, width: width}
	if ls, ok := md.cache[k]; ok {
		return ls
	}
	src := cleanLines(body)
	out := src
	if tr := md.renderer(width); tr != nil {
		if s, err := tr.Render(src); err == nil {
			out = s
		}
	}
	ls := strings.Split(out, "\n")
	for i, l := range ls {
		ls[i] = strings.TrimRight(l, " ")
	}
	ls = trimBlank(ls)
	if len(md.cache) >= maxMarkdownCache {
		md.cache = map[mdKey][]string{}
	}
	md.cache[k] = ls
	return ls
}

// renderer returns the Glamour renderer for width, nil when it cannot be
// built.
func (md *markdown) renderer(width int) *glamour.TermRenderer {
	if tr, ok := md.trs[width]; ok {
		return tr
	}
	tr, err := glamour.NewTermRenderer(md.cfg(), glamour.WithColorProfile(md.r.ColorProfile()), glamour.WithWordWrap(width))
	if err != nil {
		return nil
	}
	md.trs[width] = tr
	return tr
}

// cleanLines applies diffview.Clean to every line of s.
func cleanLines(s string) string {
	ls := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range ls {
		ls[i] = diffview.Clean(l)
	}
	return strings.Join(ls, "\n")
}

// trimBlank drops leading and trailing lines that are blank once escape
// codes are ignored.
func trimBlank(ls []string) []string {
	blank := func(s string) bool { return strings.TrimSpace(ansi.Strip(s)) == "" }
	for len(ls) > 0 && blank(ls[0]) {
		ls = ls[1:]
	}
	for len(ls) > 0 && blank(ls[len(ls)-1]) {
		ls = ls[:len(ls)-1]
	}
	return ls
}
