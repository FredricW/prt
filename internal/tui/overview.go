package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// fileStat is one row of the git-facts file list.
type fileStat struct {
	path       string
	adds, dels int
}

// markdown renders PR descriptions with glamour. Without colour (snapshots,
// pipes) it uses the "notty" style so output is plain and deterministic.
type markdown struct {
	style string
	r     *lipgloss.Renderer
	cache map[int]*glamour.TermRenderer // by wrap width
}

func newMarkdown(r *lipgloss.Renderer) *markdown {
	style := "notty"
	if hasColour(r) {
		style = "light"
		if r.HasDarkBackground() {
			style = "dark"
		}
	}
	return &markdown{style: style, r: r, cache: map[int]*glamour.TermRenderer{}}
}

// render returns body as wrapped terminal text; on a glamour error it falls
// back to the raw body.
func (md *markdown) render(body string, width int) string {
	width = max(20, width)
	tr, ok := md.cache[width]
	if !ok {
		var err error
		tr, err = glamour.NewTermRenderer(
			glamour.WithStandardStyle(md.style),
			glamour.WithColorProfile(md.r.ColorProfile()),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return body
		}
		md.cache[width] = tr
	}
	out, err := tr.Render(body)
	if err != nil {
		return body
	}
	return strings.Trim(trimRight(out), "\n")
}

// overview is the Overview tab: a scrolling page plus, per rendered line,
// the unit id behind it (anchors) so `enter` and `L` know which unit the
// block at the viewport top refers to. raw is the `L` overlay.
type overview struct {
	vp      viewport.Model
	anchors []string        // per content line; "" when the line has no unit
	cells   map[int]cellRow // per content line with unit cells (minimap)

	raw   bool // an overlay is open (raw labels, or urgency weights)
	rawVP viewport.Model
	// weights marks the open overlay as the urgency weights (`w`), which
	// `w` closes; otherwise it is the raw labels (`L`).
	weights bool
}

func newOverview() overview {
	return overview{vp: viewport.New(80, 20), rawVP: viewport.New(80, 20)}
}

func (o *overview) setSize(w, h int) {
	o.vp.Width, o.vp.Height = max(1, w), max(1, h)
	o.rawVP.Width, o.rawVP.Height = max(1, w), max(1, h)
}

// setContent replaces the page and its anchors and scrolls to the top.
func (o *overview) setContent(s string, anchors []string) {
	o.vp.SetContent(s)
	o.anchors, o.cells = anchors, nil
	o.vp.GotoTop()
}

// setPage is setContent for a rendered page, keeping its cell runs.
func (o *overview) setPage(p *page) {
	o.setContent(strings.Join(p.lines, "\n"), p.anchors)
	o.cells = p.cells
}

// clickAt returns the unit behind a click on content line i at display
// column x: the cell's unit on a minimap row, else the line's anchor.
func (o overview) clickAt(i, x int) (string, bool) {
	if c, ok := o.cells[i]; ok {
		if id, ok := c.at(x); ok {
			return id, true
		}
	}
	return o.anchorAt(i)
}

// anchor returns the unit behind the first anchored line at or after the
// viewport top.
func (o overview) anchor() (string, bool) {
	return o.anchorFrom(o.vp.YOffset)
}

// anchorFrom returns the first anchor at or after content line i.
func (o overview) anchorFrom(i int) (string, bool) {
	for i = max(0, i); i < len(o.anchors); i++ {
		if o.anchors[i] != "" {
			return o.anchors[i], true
		}
	}
	return "", false
}

// anchorAt returns the anchor of content line i only.
func (o overview) anchorAt(i int) (string, bool) {
	if i < 0 || i >= len(o.anchors) || o.anchors[i] == "" {
		return "", false
	}
	return o.anchors[i], true
}

// openRaw shows the raw-labels overlay with body.
func (o *overview) openRaw(body string) {
	o.raw, o.weights = true, false
	o.rawVP.SetContent(body)
	o.rawVP.GotoTop()
}

// openWeights shows the urgency-weights overlay with body.
func (o *overview) openWeights(body string) {
	o.openRaw(body)
	o.weights = true
}

func (o *overview) closeRaw() { o.raw, o.weights = false, false }

func (o overview) view() string {
	if o.raw {
		return o.rawVP.View()
	}
	return o.vp.View()
}

// titleLine is the spec's title line: number, title, author, age, base ← head.
func titleLine(it model.Item, now time.Time) string {
	label := itemLabel(it)
	if it.Kind == model.KindBranch && it.Title != "" && it.Title != it.Ref {
		label += " — " + it.Title
	}
	parts := []string{label}
	if it.Author != "" {
		parts = append(parts, it.Author)
	}
	if a := age(it.UpdatedAt, now); a != "" {
		parts = append(parts, a)
	}
	parts = append(parts, orDash(it.BaseRef)+" ← "+orDash(it.Ref))
	return strings.Join(parts, " · ")
}

// totals prefers the aggregate's counts, then the item's, then the files.
func totals(d proto.Detail, files []fileStat) (int, int) {
	if d.Agg != nil && (d.Agg.Added != 0 || d.Agg.Removed != 0) {
		return d.Agg.Added, d.Agg.Removed
	}
	if d.Item.Additions != 0 || d.Item.Deletions != 0 {
		return d.Item.Additions, d.Item.Deletions
	}
	var a, r int
	for _, f := range files {
		a, r = a+f.adds, r+f.dels
	}
	return a, r
}

// statsFromDetail returns per-file counts from Detail.Files.
func statsFromDetail(fs []model.File) []fileStat {
	out := make([]fileStat, 0, len(fs))
	for _, f := range fs {
		p := f.Path
		if f.Renamed && f.OldPath != "" {
			p = f.OldPath + " → " + f.Path
		}
		out = append(out, fileStat{path: p, adds: f.Adds, dels: f.Dels})
	}
	return out
}

func padLeft(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return strings.Repeat(" ", n-w) + s
	}
	return s
}

func padRight(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}
