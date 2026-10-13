package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// fileStat is one row of the overview file list.
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

// overview is the Overview tab. M0 is a placeholder: title line, facts,
// file list and the description; label-driven blocks arrive in M1.
type overview struct {
	vp viewport.Model
}

func newOverview() overview {
	return overview{vp: viewport.New(80, 20)}
}

func (o *overview) setSize(w, h int) {
	o.vp.Width, o.vp.Height = max(1, w), max(1, h)
}

// setContent replaces the text and scrolls to the top.
func (o *overview) setContent(s string) {
	o.vp.SetContent(s)
	o.vp.GotoTop()
}

func (o overview) view() string { return o.vp.View() }

// overviewText builds the placeholder overview for d.
func overviewText(st styles, md *markdown, d proto.Detail, files []fileStat, now time.Time, width int) string {
	it := d.Item
	var b strings.Builder
	b.WriteString(st.title.Render(titleLine(it, now)) + "\n")
	b.WriteString(factsLine(it) + "\n")
	b.WriteString(listLine("GitHub labels", it.Labels) + "\n")
	b.WriteString(listLine("Reviewers", it.Reviewers) + "\n")
	adds, dels := totals(d, files)
	fmt.Fprintf(&b, "+%d −%d · %s\n", adds, dels, plural(len(files), "file"))
	b.WriteString(st.dim.Render("labels arrive in M1") + "\n\n")
	b.WriteString(st.section.Render("Files") + "\n")
	if len(files) == 0 {
		b.WriteString(st.dim.Render("no files yet") + "\n")
	}
	for _, f := range files {
		fmt.Fprintf(&b, "  %s  %s\n", padLeft(fmt.Sprintf("+%d −%d", f.adds, f.dels), 11), f.path)
	}
	b.WriteString("\n" + st.section.Render("Description") + "\n")
	if strings.TrimSpace(it.Body) == "" {
		b.WriteString(st.dim.Render("no description"))
	} else {
		b.WriteString(md.render(it.Body, width))
	}
	return b.String()
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

func factsLine(it model.Item) string {
	parts := []string{"CI " + orDash(string(it.CI))}
	if it.Kind == model.KindPR && it.Draft {
		parts = append(parts, "draft")
	}
	if it.State != "" {
		parts = append(parts, string(it.State))
	}
	return strings.Join(parts, " · ")
}

func listLine(name string, xs []string) string {
	if len(xs) == 0 {
		return name + ": none"
	}
	return name + ": " + strings.Join(xs, ", ")
}

// totals prefers the item's counts, then the aggregate, then the files.
func totals(d proto.Detail, files []fileStat) (int, int) {
	if d.Item.Additions != 0 || d.Item.Deletions != 0 {
		return d.Item.Additions, d.Item.Deletions
	}
	if d.Agg != nil && (d.Agg.Added != 0 || d.Agg.Removed != 0) {
		return d.Agg.Added, d.Agg.Removed
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

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}
