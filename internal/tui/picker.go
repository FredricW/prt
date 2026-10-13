package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fredricw/prt/internal/model"
)

// sections is the picker's display order (spec: Picker, Sections and sort).
var sections = []model.Section{model.SectionNeedsYou, model.SectionYours, model.SectionBranches, model.SectionInvolved, model.SectionAdded}

// hideWhenEmpty lists sections that are not in the spec's table and so get
// no "(0)" header when empty.
var hideWhenEmpty = map[model.Section]bool{model.SectionInvolved: true}

func sectionRank(s model.Section) int {
	for i, x := range sections {
		if x == s {
			return i
		}
	}
	return len(sections)
}

// picker is the sidebar: items grouped by section, a cursor over items
// (headers are never selectable) and a line offset for scrolling.
type picker struct {
	items  []model.Item // section order, then urgency desc
	cursor int
	offset int // first visible line
	width  int
	height int
}

// rowCtx is what rendering a row needs besides the item.
type rowCtx struct {
	now    time.Time
	spin   string                 // current spinner frame
	stages map[string]model.Stage // latest stage per item (from stage events)
}

// sortItems orders items by section, then urgency descending, then most
// recently updated. The sort is stable, so an order the daemon already
// established within equal keys is kept.
func sortItems(items []model.Item) []model.Item {
	out := append([]model.Item(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := sectionRank(a.Section), sectionRank(b.Section); ra != rb {
			return ra < rb
		}
		if a.Urgency != b.Urgency {
			return a.Urgency > b.Urgency
		}
		return a.UpdatedAt.After(b.UpdatedAt)
	})
	return out
}

// setItems replaces the items, keeping the cursor on the same item id when
// it still exists. It reports whether the selected id changed.
func (p *picker) setItems(items []model.Item) bool {
	prev, hadPrev := p.selected()
	p.items = sortItems(items)
	if hadPrev {
		for i, it := range p.items {
			if it.ID == prev.ID {
				p.cursor = i
				p.ensureVisible()
				return false
			}
		}
	}
	p.cursor = min(p.cursor, max(0, len(p.items)-1))
	p.ensureVisible()
	cur, ok := p.selected()
	return ok != hadPrev || cur.ID != prev.ID
}

// patch updates one item's state in place (from an item event) so the
// spinner shows before the next Items() round trip.
func (p *picker) patch(it model.Item) {
	for i := range p.items {
		if p.items[i].ID == it.ID {
			p.items[i].State, p.items[i].StateNote = it.State, it.StateNote
			return
		}
	}
}

func (p *picker) selected() (model.Item, bool) {
	if p.cursor < 0 || p.cursor >= len(p.items) {
		return model.Item{}, false
	}
	return p.items[p.cursor], true
}

// move shifts the cursor by delta items (clamped); reports whether it moved.
func (p *picker) move(delta int) bool {
	return p.moveTo(p.cursor + delta)
}

// moveTo puts the cursor on item i (clamped); reports whether it moved.
func (p *picker) moveTo(i int) bool {
	prev := p.cursor
	p.cursor = max(0, min(i, len(p.items)-1))
	p.ensureVisible()
	return p.cursor != prev
}

func (p *picker) setSize(w, h int) {
	p.width, p.height = w, h
	p.ensureVisible()
}

// busy reports whether any item is fetching or analysing (spinner needed).
func (p picker) busy() bool {
	for _, it := range p.items {
		if it.State == model.StateFetching || it.State == model.StateAnalysing {
			return true
		}
	}
	return false
}

// pickerLine is one rendered line; item is the item index or -1.
type pickerLine struct {
	text string
	item int
}

// lines lays out the whole picker (header, sections, rows) unscrolled.
func (p picker) lines(st styles, rc rowCtx) []pickerLine {
	w := max(4, p.width)
	out := []pickerLine{{text: st.title.Render(truncate(p.header(), w)), item: -1}}
	bySection := map[model.Section][]int{}
	var order []model.Section
	for i, it := range p.items {
		s := it.Section
		if sectionRank(s) == len(sections) {
			s = "other"
		}
		if _, seen := bySection[s]; !seen && sectionRank(s) == len(sections) {
			order = append(order, s)
		}
		bySection[s] = append(bySection[s], i)
	}
	for _, s := range append(append([]model.Section(nil), sections...), order...) {
		idx := bySection[s]
		head := fmt.Sprintf("%s (%d)", sectionTitle(s), len(idx))
		if len(idx) == 0 && hideWhenEmpty[s] {
			continue
		}
		if len(idx) == 0 {
			out = append(out, pickerLine{text: st.emptySection.Render(truncate(head, w)), item: -1})
			continue
		}
		out = append(out, pickerLine{text: st.section.Render(truncate(head, w)), item: -1})
		for _, i := range idx {
			r1, r2 := p.row(i, st, rc, w)
			out = append(out, pickerLine{text: r1, item: i}, pickerLine{text: r2, item: i})
		}
	}
	return out
}

func (p picker) header() string {
	if len(p.items) == 0 {
		return "prt · no items yet"
	}
	return "prt · " + plural(len(p.items), "item")
}

// plural formats n with noun, adding "s" unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// row renders an item's two lines: cursor marker, urgency bar and label;
// then the dim secondary line.
func (p picker) row(i int, st styles, rc rowCtx, w int) (string, string) {
	it := p.items[i]
	marker := "  "
	if i == p.cursor {
		marker = "▸ "
	}
	first := truncate(marker+urgencyBar(it.UrgencyQuartile)+" "+itemLabel(it), w)
	if i == p.cursor {
		first = st.cursor.Render(first)
	}
	second := st.dim.Render(truncate("       "+secondaryLine(it, rc), w))
	return first, second
}

// itemAt returns the item index drawn on screen row y of the picker (0 is
// its first visible line), or -1 for a header, blank or out-of-range row.
func (p picker) itemAt(y int) int {
	ls := p.lines(styles{}, rowCtx{})
	n := p.offset + y
	if y < 0 || y >= max(1, p.height) || n < 0 || n >= len(ls) {
		return -1
	}
	return ls[n].item
}

// cursorLine returns the index of the cursor's first line in lines().
func (p picker) cursorLine(ls []pickerLine) int {
	for n, l := range ls {
		if l.item == p.cursor {
			return n
		}
	}
	return 0
}

// ensureVisible scrolls so both lines of the cursor row are on screen. The
// layout does not depend on styles or text, so a zero rowCtx is enough.
func (p *picker) ensureVisible() {
	if p.height <= 0 {
		return
	}
	ls := p.lines(styles{}, rowCtx{})
	c := p.cursorLine(ls)
	if c < p.offset {
		p.offset = c
	}
	if c+1 >= p.offset+p.height {
		p.offset = c + 2 - p.height
	}
	if c <= 1 {
		p.offset = 0 // keep the title visible near the top
	}
	p.offset = max(0, min(p.offset, len(ls)-p.height))
}

// view renders the visible window of the picker.
func (p picker) view(st styles, rc rowCtx) string {
	ls := p.lines(st, rc)
	start := max(0, min(p.offset, len(ls)))
	end := min(len(ls), start+max(1, p.height))
	texts := make([]string, 0, end-start)
	for _, l := range ls[start:end] {
		texts = append(texts, l.text)
	}
	return strings.Join(texts, "\n")
}

// urgencyBar draws four cells, q of them filled (spec: Urgency bar).
func urgencyBar(q int) string {
	q = max(0, min(4, q))
	return strings.Repeat("▮", q) + strings.Repeat("▯", 4-q)
}

func sectionTitle(s model.Section) string {
	switch s {
	case model.SectionNeedsYou:
		return "Needs you"
	case model.SectionYours:
		return "Yours"
	case model.SectionBranches:
		return "Branches"
	case model.SectionInvolved:
		return "Involved"
	case model.SectionAdded:
		return "Added"
	}
	return "Other"
}

func itemLabel(it model.Item) string {
	if it.Kind == model.KindPR {
		return fmt.Sprintf("#%d %s", it.Number, it.Title)
	}
	return it.Ref
}

// secondaryLine is the dim second row: the item state (when not ready),
// then the section-specific facts (spec: Sections and sort).
func secondaryLine(it model.Item, rc rowCtx) string {
	var parts []string
	if s := stateText(it, rc); s != "" {
		parts = append(parts, s)
	}
	switch {
	case it.Section == model.SectionYours:
		parts = append(parts, yoursFacts(it, rc.now)...)
	case it.Section == model.SectionBranches, it.Kind == model.KindBranch:
		parts = append(parts, branchFacts(it, rc.now)...)
	default: // Needs you, and PRs in Added
		parts = append(parts, needsYouFacts(it)...)
	}
	return strings.Join(parts, " · ")
}

// stateText renders the analysis state; "" when ready or unknown.
func stateText(it model.Item, rc rowCtx) string {
	switch it.State {
	case model.StateFetching:
		return strings.TrimSpace(rc.spin + " fetching")
	case model.StateAnalysing:
		stage := it.StateNote
		if stage == "" {
			stage = string(rc.stages[it.ID])
		}
		return strings.TrimSpace(rc.spin + " " + strings.TrimSpace("analysing "+stage))
	case model.StateStale:
		return "stale · r to refresh"
	case model.StateError:
		if it.StateNote == "" {
			return "error"
		}
		return "error: " + it.StateNote
	}
	return ""
}

func needsYouFacts(it model.Item) []string {
	var parts []string
	if it.Author != "" {
		parts = append(parts, it.Author)
	}
	if it.Additions != 0 || it.Deletions != 0 {
		parts = append(parts, fmt.Sprintf("+%d −%d", it.Additions, it.Deletions))
	}
	if ci := ciText(it.CI); ci != "" {
		parts = append(parts, ci)
	}
	if it.Minutes > 0 {
		parts = append(parts, fmt.Sprintf("~%d min", max(1, int(it.Minutes+0.5))))
	}
	if it.Why != "" {
		parts = append(parts, it.Why)
	}
	return parts
}

func yoursFacts(it model.Item, now time.Time) []string {
	var parts []string
	if it.OpenThreads > 0 {
		parts = append(parts, plural(it.OpenThreads, "open thread"))
	}
	if ci := ciText(it.CI); ci != "" {
		parts = append(parts, ci)
	}
	if it.Draft {
		parts = append(parts, "draft")
	}
	if a := age(it.UpdatedAt, now); a != "" {
		parts = append(parts, a)
	}
	return parts
}

func branchFacts(it model.Item, now time.Time) []string {
	var parts []string
	if it.Unpushed > 0 {
		parts = append(parts, fmt.Sprintf("%d unpushed", it.Unpushed))
	}
	if it.Ahead > 0 {
		parts = append(parts, fmt.Sprintf("%d ahead", it.Ahead))
	}
	if a := age(it.UpdatedAt, now); a != "" {
		parts = append(parts, a)
	}
	return parts
}

func ciText(c model.CIState) string {
	switch c {
	case model.CIGreen:
		return "CI ✓"
	case model.CIRed:
		return "CI ✗"
	case model.CIPending:
		return "CI …"
	}
	return ""
}

// age formats now−t as 5m, 3h or 2d; "" for a zero time.
func age(t, now time.Time) string {
	if t.IsZero() || now.IsZero() {
		return ""
	}
	d := max(0, now.Sub(t))
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}
