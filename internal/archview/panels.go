package archview

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/theme"
)

// hit is a clickable body region: a module (file < 0) or a file-list row.
type hit struct {
	r    rect
	id   string
	file int
}

// frame is the rendered body (everything under the header line) and where
// its parts are.
type frame struct {
	lines []string
	hits  []hit
	// centers are module positions for focus movement (body cells); order
	// is the order modules are tried in (graph order).
	centers map[string]point
	order   []string
	// focusTop and focusEnd are the body lines of the focused module.
	focusTop, focusEnd int
	graph              bool // drawn as a graph (else the list)
}

// part is a rendered column: its lines and hits relative to its top-left.
type part struct {
	lines              []string
	hits               []hit
	centers            map[string]point
	focusTop, focusEnd int
	graph              bool
}

// render lays out the body: the graph (or list, or file list) box on the
// left and the side column beside it from SideMinWidth, else below it.
func (m *Model) render() frame {
	if g := m.graph(); g == nil || len(g.Modules) == 0 {
		return frame{}
	}
	s := m.shown()
	bh := m.bodyHeight()
	beside := m.w >= SideMinWidth
	leftW := m.w
	if beside {
		leftW = m.w - sideW - 1
	}
	var left part
	switch {
	case m.files:
		left = m.filesBox(leftW, bh)
	default:
		var ok bool
		if left, ok = m.graphBox(s, leftW, bh); !ok {
			left = m.listBox(s, leftW, bh)
		}
	}
	f := frame{hits: left.hits, centers: left.centers, focusTop: left.focusTop, focusEnd: left.focusEnd, graph: left.graph}
	for _, mod := range s.mods {
		f.order = append(f.order, mod.ID)
	}
	if !beside {
		f.lines = append(left.lines, "")
		f.lines = append(f.lines, m.side(s, m.w)...)
		return f
	}
	right := m.side(s, sideW)
	n := max(len(left.lines), len(right))
	for i := 0; i < n; i++ {
		l, r := "", ""
		if i < len(left.lines) {
			l = left.lines[i]
		}
		if i < len(right) {
			r = right[i]
		}
		f.lines = append(f.lines, padTo(l, leftW)+" "+r)
	}
	return f
}

// padTo pads s with spaces to w cells.
func padTo(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}

// header is the line above the body: counts on the left, the item and the
// depth on the right ("#101 · modules touched + 1 hop").
func (m *Model) header() string {
	s := m.shown()
	left := m.th.Dim.Render(fmt.Sprintf("%s · %s",
		plural(len(s.mods), "module", "modules"), plural(len(s.edges), "edge", "edges")))
	if m.diffOnly {
		left += m.th.Dim.Render(" · ") + m.th.Accent.Render("diff-only")
	}
	name := m.d.Item.Ref
	if m.d.Item.Number > 0 {
		name = fmt.Sprintf("#%d", m.d.Item.Number)
	}
	right := depthLabel(m.depth)
	if name != "" {
		right = name + " · " + right
	}
	return theme.Spread(left, m.th.Dim.Render(right), m.w)
}

// depthLabel is "modules touched", "modules touched + 1 hop", "… + 2 hops".
func depthLabel(d int) string {
	switch d {
	case 0:
		return "modules touched"
	case 1:
		return "modules touched + 1 hop"
	}
	return fmt.Sprintf("modules touched + %d hops", d)
}

// boxTitle is the graph box's title.
func (m *Model) boxTitle() string { return fmt.Sprintf("Module graph · depth %d", m.depth) }

// boxRight is the graph box's top-right note.
func (m *Model) boxRight() string {
	if m.diffOnly {
		return "diff-only"
	}
	return ""
}

// graphBox draws the layered graph in a box w × h; ok is false when it
// does not fit (the list is drawn instead).
func (m *Model) graphBox(s shown, w, h int) (part, bool) {
	cw, ch := w-4, h-2
	if cw < boxW+2 || ch < boxH || len(s.mods) == 0 {
		return part{}, false
	}
	l, ok := layoutGraph(s, cw, ch)
	if !ok {
		return part{}, false
	}
	dy := 0
	if l.h+1 <= ch {
		dy = 1 // a blank row under the title
	}
	body := make([]string, dy, ch)
	body = append(body, drawGraph(s, l, m.focus).lines(m.th)...)
	p := part{centers: map[string]point{}, graph: true}
	p.lines = strings.Split(m.th.Box(m.boxTitle(), strings.Join(body, "\n"), w, theme.BoxOpt{Height: h, Right: m.boxRight()}), "\n")
	for id, r := range l.boxes {
		sr := rect{x: r.x + 2, y: r.y + 1 + dy, w: r.w, h: r.h}
		p.hits = append(p.hits, hit{r: sr, id: id, file: -1})
		p.centers[id] = sr.center()
		if id == m.focus {
			p.focusTop, p.focusEnd = sr.y, sr.y+sr.h
		}
	}
	sort.Slice(p.hits, func(i, j int) bool { return p.hits[i].id < p.hits[j].id })
	return p, true
}

// listBox is the fallback: modules grouped by hop with their two lines,
// then the edges as "A → B (new)".
func (m *Model) listBox(s shown, w, h int) part {
	inner := max(1, w-4)
	p := part{centers: map[string]point{}}
	var lines []string
	add := func(l string) { lines = append(lines, l) }
	maxHop := 0
	for _, mod := range s.mods {
		maxHop = max(maxHop, mod.Hop)
	}
	for hop := 0; hop <= maxHop; hop++ {
		first := true
		for _, mod := range s.mods {
			if mod.Hop != hop {
				continue
			}
			if first {
				add(m.th.Divider(fmt.Sprintf("hop %d", hop), inner))
				first = false
			}
			top := len(lines)
			mark := "  "
			if mod.ID == m.focus {
				mark = m.th.Accent.Render("▸ ")
			}
			head := renderSegs(m.th, headSegs(mod, inner-2), "")
			if mod.ID == m.focus {
				head = m.th.Bold.Render(head)
			}
			add(mark + head)
			add("    " + renderSegs(m.th, factSegs(mod), " · "))
			r := rect{x: 0, y: top + 1, w: w, h: 2}
			p.hits = append(p.hits, hit{r: r, id: mod.ID, file: -1})
			p.centers[mod.ID] = point{0, top + 1}
			if mod.ID == m.focus {
				p.focusTop, p.focusEnd = top+1, top+3
			}
		}
	}
	add(m.th.Divider("edges", inner))
	for _, e := range s.edges {
		add(m.edgeLine(e))
	}
	if len(s.edges) == 0 {
		add(m.th.Dim.Render("  no import edges between the shown modules"))
	}
	p.lines = strings.Split(m.th.Box(m.boxTitle(), strings.Join(lines, "\n"), w, theme.BoxOpt{Height: max(h, len(lines)+2), Right: m.boxRight()}), "\n")
	return p
}

// edgeLine is one edge of the list: "  web/a → web/b (new) · 3 imports · ⚠ getUser".
func (m *Model) edgeLine(e model.ArchEdge) string {
	text := e.From + " → " + e.To
	st := m.th.Text
	switch e.Status {
	case model.ArchNew:
		text += " (new)"
		st = m.th.Add
	case model.ArchRemoved:
		text += " (removed)"
		st = m.th.Del
	}
	line := "  " + st.Render(text) + m.th.Dim.Render(" · "+plural(e.Imports, "import", "imports"))
	if len(e.Contracts) > 0 {
		line += m.th.Dim.Render(" · ") + m.th.Accent.Render("⚠ "+e.Contracts[0])
	}
	return line
}

// filesBox is the focused module's file list.
func (m *Model) filesBox(w, h int) part {
	mod, _ := m.focused()
	inner := max(1, w-4)
	p := part{centers: map[string]point{}}
	var lines []string
	if len(m.fileRows) == 0 {
		lines = append(lines, m.th.Dim.Render("no changed files in this module (context)"))
	}
	for i, r := range m.fileRows {
		mark := "  "
		if i == m.fileSel {
			mark = m.th.Accent.Render("▸ ")
		}
		counts := m.th.Add.Render(fmt.Sprintf("+%d", r.adds)) + " " + m.th.Del.Render(fmt.Sprintf("−%d", r.dels))
		if r.status != "" {
			counts += m.th.Dim.Render(" " + r.status)
		}
		name := theme.Truncate(r.path, max(1, inner-2-ansi.StringWidth(counts)-2))
		if i == m.fileSel {
			name = m.th.Bold.Render(name)
		}
		lines = append(lines, theme.Spread(mark+name, counts, inner))
		p.hits = append(p.hits, hit{r: rect{x: 0, y: i + 1, w: w, h: 1}, file: i})
	}
	lines = append(lines, "", m.th.Dim.Render("enter opens in Code · j/k move · esc closes"))
	title := "Files in " + mod.ID
	p.lines = strings.Split(m.th.Box(title, strings.Join(lines, "\n"), w, theme.BoxOpt{Height: max(h, len(lines)+2), Focused: true}), "\n")
	return p
}

// side is the right column: Per module, Contracts crossing edges, Net
// effect, Legend and the footer.
func (m *Model) side(s shown, w int) []string {
	inner := max(1, w-4)
	var out []string
	box := func(title string, lines []string) {
		out = append(out, strings.Split(m.th.Box(title, strings.Join(lines, "\n"), w, theme.BoxOpt{}), "\n")...)
	}
	box("Per module", m.perModule(s, inner))
	box("Contracts crossing edges", m.contracts(s, inner))
	box("Net effect", m.netEffect(inner))
	box("Legend", m.legend())
	out = append(out, m.th.Dim.Render(theme.Truncate(" Graph from import analysis.", w)))
	return out
}

// perModule is the table of touched modules: module · units · risk ·
// tests · you.
func (m *Model) perModule(s shown, inner int) []string {
	nameW := max(4, inner-21)
	row := func(name, units, risk, tests, you string) string {
		return padTo(name, nameW) + " " + padLeftTo(units, 5) + " " + padLeftTo(risk, 4) + " " + padLeftTo(tests, 5) + " " + padLeftTo(you, 3)
	}
	lines := []string{m.th.Dim.Render(row("module", "units", "risk", "tests", "you"))}
	for _, mod := range s.mods {
		if mod.Hop > 0 {
			continue
		}
		name := theme.Truncate(shortName(mod.ID), nameW)
		switch {
		case mod.Status == model.ArchRemoved:
			name = m.th.Dim.Render(name)
		case mod.Owned:
			name = m.th.Bright.Render(name)
		}
		risk := "–"
		if mod.RiskMax > 0 {
			risk = fmt.Sprint(mod.RiskMax)
			if mod.RiskMax >= 4 {
				risk = m.th.Accent.Render(risk)
			}
		}
		you := ""
		if mod.Owned {
			you = m.th.Blue.Render("★")
		}
		lines = append(lines, row(name, fmt.Sprint(mod.Units), risk, m.testsCell(mod), you))
	}
	if len(lines) == 1 {
		lines = append(lines, m.th.Dim.Render("no touched modules shown"))
	}
	return lines
}

// testsCell is ✗ untested, ? unsure, ✓ covered, – no units.
func (m *Model) testsCell(mod model.ArchModule) string {
	switch {
	case mod.Untested > 0:
		return m.th.Del.Render("✗")
	case mod.Unsure > 0:
		return m.th.Blue.Render("?")
	case mod.Units > 0:
		return m.th.Add.Render("✓")
	}
	return "–"
}

// padLeftTo right-aligns s in w cells.
func padLeftTo(s string, w int) string {
	return strings.Repeat(" ", max(0, w-ansi.StringWidth(s))) + s
}

// shortName abbreviates a module path: the first letter of its top
// directory and its last segment ("web/src/auth" → "w/auth").
func shortName(id string) string {
	parts := strings.Split(id, "/")
	if len(parts) < 2 {
		return id
	}
	first := []rune(parts[0])
	return string(first[0]) + "/" + parts[len(parts)-1]
}

// contracts lists contract changes crossing shown edges, by the module
// that changed them: "⚠ w/api → w/comp, w/hooks" and the contracts.
func (m *Model) contracts(s shown, inner int) []string {
	type group struct {
		importers []string
		texts     []string
	}
	groups := map[string]*group{}
	var order []string
	for _, e := range s.edges {
		if len(e.Contracts) == 0 {
			continue
		}
		g := groups[e.To]
		if g == nil {
			g = &group{texts: e.Contracts}
			groups[e.To] = g
			order = append(order, e.To)
		}
		g.importers = append(g.importers, shortName(e.From))
	}
	sort.Strings(order)
	var lines []string
	for _, to := range order {
		g := groups[to]
		lines = append(lines, theme.Truncate(m.th.Accent.Render("⚠")+" "+shortName(to)+" → "+strings.Join(g.importers, ", "), inner))
		for _, t := range g.texts {
			lines = append(lines, m.th.Dim.Render(theme.Truncate("  "+t, inner)))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, m.th.Dim.Render("none"))
	}
	return lines
}

// netEffect is ArchGraph.NetEffect, then the note of a degraded graph.
func (m *Model) netEffect(inner int) []string {
	g := m.graph()
	var lines []string
	for _, s := range g.NetEffect {
		lines = append(lines, wrap(s, inner)...)
	}
	if g.Note != "" {
		for _, l := range wrap(g.Note, inner) {
			lines = append(lines, m.th.Dim.Render(l))
		}
	}
	if g.Truncated {
		lines = append(lines, m.th.Dim.Render(theme.Truncate("repo index truncated", inner)))
	}
	if len(lines) == 0 {
		lines = append(lines, m.th.Dim.Render("no import changes"))
	}
	return lines
}

// wrap word-wraps plain text to w cells.
func wrap(s string, w int) []string {
	return strings.Split(ansi.Wrap(s, w, ""), "\n")
}

// legend explains the glyphs and line styles.
func (m *Model) legend() []string {
	t := m.th
	return []string{
		t.Accent.Render("●") + " changed  " + t.Blue.Render("★") + " yours",
		t.Add.Render("+") + " new module / edge",
		t.Del.Render("✕ ╌") + " removed  " + t.R.NewStyle().Foreground(t.P.Line).Render("──") + " unchanged",
	}
}
