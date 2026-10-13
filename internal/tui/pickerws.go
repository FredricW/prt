package tui

import (
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// The workspace picker (M5.3, DECISIONS "M5.3 lead calls: workspace, picker
// and resizing"): repo groups, each a header row ("▾ acme/portal 6", the
// GitHub status dim on the right) over its section dividers and rows. Groups
// are ordered by their most urgent item, then label; repos without items
// come last and start folded. Inside a group only sections with items get a
// divider. The cursor stops on items and on the headers of folded or empty
// repos (an unfolded header is skipped like a divider).

// repoGroup is one repo's run of items in the arranged list: items
// first..first+n-1 (n 0 for a repo without items).
type repoGroup struct {
	key, label string
	info       proto.RepoInfo
	first, n   int
}

// stop is one row the cursor can stop on: an item (repo "") or a repo
// header (item -1).
type stop struct {
	item int
	repo string
}

// repoStatusWords is the header's dim GitHub status (M5.3).
func repoStatusWords(s proto.RepoStatus) string {
	switch s {
	case proto.RepoGitHub:
		return "gh ✓"
	case proto.RepoGitHubDisabled:
		return "gh off"
	case proto.RepoNotGitHub:
		return "not on GitHub"
	case proto.RepoNoRemote:
		return "no remote"
	case proto.RepoError:
		return "error"
	}
	return ""
}

// repoLabel is what the UI calls a repo: its label, else its key.
func repoLabel(r proto.RepoInfo) string {
	switch {
	case r.Label != "":
		return r.Label
	case r.Key != "":
		return r.Key
	}
	return "–"
}

// arrange sorts items for display: by section and the sort mode
// (sortItems), and in a workspace by repo group first.
func (p picker) arrange(items []model.Item) []model.Item {
	out := sortItems(items, p.sort)
	if !p.multi {
		return out
	}
	rank := map[string]int{}
	for i, k := range groupOrder(p.repos, out) {
		rank[k] = i
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Repo] < rank[out[j].Repo] })
	return out
}

// groupOrder returns the repo keys in display order: repos with items by
// their most urgent item's Urgency (highest first), then label; then the
// repos without items by label. Repos of items the workspace does not list
// are grouped under their key.
func groupOrder(repos []proto.RepoInfo, items []model.Item) []string {
	label := map[string]string{}
	var keys []string
	add := func(key, l string) {
		if _, ok := label[key]; !ok {
			label[key] = l
			keys = append(keys, key)
		}
	}
	for _, r := range repos {
		add(r.Key, repoLabel(r))
	}
	top, count := map[string]float64{}, map[string]int{}
	for _, it := range items {
		add(it.Repo, repoLabel(proto.RepoInfo{Key: it.Repo}))
		if count[it.Repo] == 0 || it.Urgency > top[it.Repo] {
			top[it.Repo] = it.Urgency
		}
		count[it.Repo]++
	}
	sort.SliceStable(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if (count[a] > 0) != (count[b] > 0) {
			return count[a] > 0
		}
		if count[a] > 0 && top[a] != top[b] {
			return top[a] > top[b]
		}
		return label[a] < label[b]
	})
	return keys
}

// groups returns the repo groups in display order. It relies on the items
// being arranged (each repo's items one run, in group order).
func (p picker) groups() []repoGroup {
	info := map[string]proto.RepoInfo{}
	for _, r := range p.repos {
		info[r.Key] = r
	}
	var out []repoGroup
	for i, it := range p.items {
		if n := len(out); n > 0 && out[n-1].key == it.Repo {
			out[n-1].n++
			continue
		}
		out = append(out, repoGroup{key: it.Repo, first: i, n: 1})
	}
	seen := map[string]bool{}
	for _, g := range out {
		seen[g.key] = true
	}
	var empty []repoGroup
	for _, r := range p.repos {
		if !seen[r.Key] {
			seen[r.Key] = true
			empty = append(empty, repoGroup{key: r.Key, first: len(p.items)})
		}
	}
	out = append(out, empty...)
	for i := range out {
		r, ok := info[out[i].key]
		if !ok {
			r = proto.RepoInfo{Key: out[i].key}
		}
		out[i].info, out[i].label = r, repoLabel(r)
	}
	tail := out[len(out)-len(empty):]
	sort.SliceStable(tail, func(i, j int) bool { return tail[i].label < tail[j].label })
	return out
}

// group returns the group of repo key.
func (p picker) group(key string) (repoGroup, bool) {
	for _, g := range p.groups() {
		if g.key == key {
			return g, true
		}
	}
	return repoGroup{}, false
}

// visibleGroups are the groups shown: every group, or the filtered one.
func (p picker) visibleGroups() []repoGroup {
	gs := p.groups()
	if p.filter == "" {
		return gs
	}
	for _, g := range gs {
		if g.key == p.filter {
			return []repoGroup{g}
		}
	}
	return nil
}

// isFolded reports whether g shows only its header: the user's choice, else
// folded when it has no items. Items without a repo key never fold.
func (p picker) isFolded(g repoGroup) bool {
	if g.key == "" {
		return false
	}
	if v, ok := p.folded[g.key]; ok {
		return v
	}
	return g.n == 0
}

// stops returns the rows the cursor can stop on, top to bottom.
func (p picker) stops() []stop {
	if !p.multi {
		out := make([]stop, len(p.items))
		for i := range p.items {
			out[i] = stop{item: i}
		}
		return out
	}
	var out []stop
	for _, g := range p.visibleGroups() {
		if g.key != "" && (p.isFolded(g) || g.n == 0) {
			out = append(out, stop{item: -1, repo: g.key})
			continue
		}
		for i := g.first; i < g.first+g.n; i++ {
			out = append(out, stop{item: i})
		}
	}
	return out
}

// stopIndex is the cursor's position in st, -1 when it is on none.
func (p picker) stopIndex(st []stop) int {
	for i, s := range st {
		if p.header != "" && s.repo == p.header {
			return i
		}
		if p.header == "" && s.repo == "" && s.item == p.cursor {
			return i
		}
	}
	return -1
}

// toStop puts the cursor on stop i (clamped); it reports whether the
// selection changed.
func (p *picker) toStop(i int) bool {
	st := p.stops()
	if len(st) == 0 {
		return false
	}
	before := p.selKey()
	p.setStop(st[max(0, min(i, len(st)-1))])
	p.ensureVisible()
	return p.selKey() != before
}

// setStop puts the cursor on s.
func (p *picker) setStop(s stop) {
	if s.repo != "" {
		p.header = s.repo
		return
	}
	p.header, p.cursor = "", s.item
}

// moveStops moves the cursor delta stops (clamped).
func (p *picker) moveStops(delta int) bool {
	i := max(0, p.stopIndex(p.stops()))
	return p.toStop(i + delta)
}

// moveToItem puts the cursor on item i (clamped), unfolding its repo and
// clearing a filter on another repo so it shows.
func (p *picker) moveToItem(i int) bool {
	if len(p.items) == 0 {
		return p.toStop(0)
	}
	before := p.selKey()
	i = max(0, min(i, len(p.items)-1))
	it := p.items[i]
	if p.filter != "" && it.Repo != p.filter {
		p.filter = ""
	}
	if g, ok := p.group(it.Repo); ok && p.isFolded(g) {
		p.folded = withFold(p.folded, it.Repo, false)
	}
	p.header, p.cursor = "", i
	p.ensureVisible()
	return p.selKey() != before
}

// normalize keeps the cursor on a stop after the list, a fold or the filter
// changed: a header that is no longer a stop (its repo unfolded) gives way
// to the repo's first item; a hidden item to its repo's header; anything
// else to the first stop.
func (p *picker) normalize() {
	if !p.multi {
		p.header = ""
		return
	}
	st := p.stops()
	if len(st) == 0 {
		p.header = ""
		return
	}
	if p.stopIndex(st) >= 0 {
		return
	}
	switch {
	case p.header != "":
		for _, s := range st {
			if s.repo == "" && p.items[s.item].Repo == p.header {
				p.setStop(s)
				return
			}
		}
	case p.cursor >= 0 && p.cursor < len(p.items):
		r := p.items[p.cursor].Repo
		for _, s := range st {
			if s.repo != "" && s.repo == r {
				p.setStop(s)
				return
			}
		}
	}
	p.setStop(st[0])
}

// workspaceLines lays out the visible repo groups: each header, then (when
// unfolded) its non-empty sections.
func (p picker) workspaceLines(st styles, rc rowCtx, w int) []pickerLine {
	var out []pickerLine
	for _, g := range p.visibleGroups() {
		out = append(out, pickerLine{text: p.headerLine(st, g, w), item: -1, repo: g.key, header: true})
		if p.isFolded(g) {
			continue
		}
		out = append(out, p.sectionLines(st, rc, w, g.first, g.first+g.n, true)...)
	}
	return out
}

// headerLine draws a repo header: "▾ acme/portal 6" (▸ when folded) with
// the GitHub status dim at the right end. The header under the cursor has
// the selection background, and without colour brackets around its label
// and count ("▸ [acme/billing-svc 3]"), like the active tab.
func (p picker) headerLine(st styles, g repoGroup, w int) string {
	glyph := "▾ "
	if p.isFolded(g) {
		glyph = "▸ "
	}
	sel := g.key != "" && g.key == p.header
	label, count := g.label, " "+strconv.Itoa(g.n)
	if sel && !st.th.Colour {
		label, count = "["+label, count+"]"
	}
	segs := []seg{{glyph, st.accent}, {label, st.bold}, {count, st.dim}}
	var bg lipgloss.TerminalColor
	if sel {
		bg = st.th.P.SelBg
	}
	right := repoStatusWords(g.info.Status)
	rw := ansi.StringWidth(right)
	if right == "" || w-rw-1 < 8 {
		return renderSegs(segs, w, bg)
	}
	left := renderSegs(segs, w-rw-1, bg)
	gap, rs := st.plain, st.dim
	if bg != nil {
		gap, rs = gap.Background(bg), rs.Background(bg)
	}
	return left + gap.Render(strings.Repeat(" ", max(1, w-ansi.StringWidth(left)-rw))) + rs.Render(right)
}

// setWorkspace turns the repo grouping on (or refreshes the repos); the
// first time, the cursor goes to the top. It reports whether the selection
// changed.
func (p *picker) setWorkspace(repos []proto.RepoInfo) bool {
	before := p.selKey()
	first := !p.multi
	p.multi, p.repos = true, repos
	if p.filter != "" && !slices.ContainsFunc(repos, func(r proto.RepoInfo) bool { return r.Key == p.filter }) {
		p.filter = ""
	}
	p.setItems(p.items)
	if first {
		p.toStop(0)
	}
	p.normalize()
	p.ensureVisible()
	return p.selKey() != before
}

// setFilter shows only repo key ("" every repo); a folded repo with items
// unfolds. The selection stays when it is still shown. It reports whether
// the selection changed.
func (p *picker) setFilter(key string) bool {
	before := p.selKey()
	p.filter = key
	if g, ok := p.group(key); ok && key != "" && g.n > 0 && p.isFolded(g) {
		p.folded = withFold(p.folded, key, false)
	}
	p.normalize()
	p.ensureVisible()
	return p.selKey() != before
}

// toggleFold folds or unfolds repo key; it reports whether the selection
// changed (folding the cursor's repo moves the cursor to its header,
// unfolding the header under the cursor to the repo's first item).
func (p *picker) toggleFold(key string) bool {
	g, ok := p.group(key)
	if !ok || key == "" {
		return false
	}
	before := p.selKey()
	p.folded = withFold(p.folded, key, !p.isFolded(g))
	p.normalize()
	p.ensureVisible()
	return p.selKey() != before
}

// foldAll folds every repo when a shown repo with items is unfolded, else
// unfolds every repo with items (repos without items go back to their
// default, folded). It reports whether the selection changed.
func (p *picker) foldAll() bool {
	fold := false
	for _, g := range p.visibleGroups() {
		if g.key != "" && g.n > 0 && !p.isFolded(g) {
			fold = true
			break
		}
	}
	before := p.selKey()
	next := map[string]bool{}
	for _, g := range p.groups() {
		switch {
		case g.key == "":
		case fold:
			next[g.key] = true
		case g.n > 0:
			next[g.key] = false
		}
	}
	p.folded = next
	p.normalize()
	p.ensureVisible()
	return p.selKey() != before
}

// title is the sidebar box's title: "Queue · ranked", plus " · <label>"
// while a repo filter is on.
func (p picker) title() string {
	if !p.multi || p.filter == "" {
		return pickerTitle
	}
	for _, r := range p.repos {
		if r.Key == p.filter {
			return pickerTitle + " · " + repoLabel(r)
		}
	}
	return pickerTitle + " · " + p.filter
}

// cursorRepo is the repo of the row under the cursor: the header's, else
// the selected item's ("" when none).
func (p picker) cursorRepo() string {
	if p.header != "" {
		return p.header
	}
	if it, ok := p.selected(); ok {
		return it.Repo
	}
	return ""
}

// withFold returns a copy of folded with key set to v.
func withFold(folded map[string]bool, key string, v bool) map[string]bool {
	next := maps.Clone(folded)
	if next == nil {
		next = map[string]bool{}
	}
	next[key] = v
	return next
}
