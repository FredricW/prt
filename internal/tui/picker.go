package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/theme"
)

// sections is the picker's display order (spec: Picker, Sections and sort).
var sections = []model.Section{model.SectionNeedsYou, model.SectionYours, model.SectionBranches, model.SectionInvolved, model.SectionAdded}

// hideWhenEmpty lists sections that are not in the spec's table and so get
// no "(0)" divider when empty.
var hideWhenEmpty = map[model.Section]bool{model.SectionInvolved: true}

func sectionRank(s model.Section) int {
	for i, x := range sections {
		if x == s {
			return i
		}
	}
	return len(sections)
}

// sortMode is the picker's order within a section (spec Picker: `s`
// cycles urgency → age → size → relevance).
type sortMode int

const (
	sortUrgency   sortMode = iota // urgency desc
	sortAge                       // oldest first (created, else updated)
	sortSize                      // smallest first (lines changed)
	sortRelevance                 // relevance term desc
	sortModes
)

func (s sortMode) String() string {
	return [...]string{"urgency", "age", "size", "relevance"}[s]
}

// pickerTitle is the sidebar box's title (design: the shared frame).
const pickerTitle = "Queue · ranked"

// picker is the sidebar list: items grouped by section, a cursor over items
// (dividers are never selectable) and a line offset for scrolling. width
// and height are the list's own cells inside the sidebar box.
//
// In a workspace (M5.3, multi) the items are grouped by repo first: each
// repo group starts with a header row and holds its section dividers and
// rows (pickerws.go). The cursor then stops on items and on the headers of
// folded or empty repos.
type picker struct {
	items  []model.Item // (multi: repo group order, then) section order, then the sort mode's key
	sort   sortMode
	cursor int
	offset int // first visible line
	width  int
	height int

	// multi turns the repo grouping on; repos are the workspace's repos in
	// workspace order. folded holds the folds the user chose (a repo absent
	// from it is folded when it has no items); it is replaced, never changed
	// in place, so copies of the model keep their own folds. filter is the
	// repo key shown alone ("" every repo); header is the repo key of the
	// header row under the cursor ("" while the cursor is on an item).
	multi  bool
	repos  []proto.RepoInfo
	folded map[string]bool
	filter string
	header string
}

// cycleSort moves to the next sort mode and re-sorts, keeping the cursor
// on the selected item.
func (p *picker) cycleSort() {
	p.sort = (p.sort + 1) % sortModes
	p.setItems(p.items)
}

// rowCtx is what rendering a row needs besides the item.
type rowCtx struct {
	now    time.Time
	spin   string                 // current spinner frame
	stages map[string]model.Stage // latest stage per item (from stage events)
	// runLogs are the stage rows per item (from stage events), drawn as the
	// one-line stage strip while the item analyses by strip
	// (pipeview.StageStrip; nil draws no strip).
	runLogs map[string][]model.RunLog
	strip   func(rls []model.RunLog, width int) string
	// marked are the multi-selected item ids (space); superseded maps a
	// branch item id to the PR that supersedes it (from its loaded detail).
	marked     map[string]bool
	superseded map[string]int
}

// sortItems orders items by section, then the mode's key (urgency
// descending; oldest first; smallest first; most relevant first), then
// urgency descending, then most recently updated. The sort is stable, so
// an order the daemon already established within equal keys is kept.
func sortItems(items []model.Item, mode sortMode) []model.Item {
	out := append([]model.Item(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := sectionRank(a.Section), sectionRank(b.Section); ra != rb {
			return ra < rb
		}
		switch mode {
		case sortAge:
			if ca, cb := createdAt(a), createdAt(b); !ca.Equal(cb) {
				return ca.Before(cb)
			}
		case sortSize:
			if sa, sb := a.Additions+a.Deletions, b.Additions+b.Deletions; sa != sb {
				return sa < sb
			}
		case sortRelevance:
			if a.Relevance != b.Relevance {
				return a.Relevance > b.Relevance
			}
		}
		if a.Urgency != b.Urgency {
			return a.Urgency > b.Urgency
		}
		return a.UpdatedAt.After(b.UpdatedAt)
	})
	return out
}

// createdAt is when an item was opened (CreatedAt, else UpdatedAt for a
// branch without one).
func createdAt(it model.Item) time.Time {
	if !it.CreatedAt.IsZero() {
		return it.CreatedAt
	}
	return it.UpdatedAt
}

// setItems replaces the items, keeping the cursor on the same item id when
// it still exists (a list that was empty starts at its top). It reports
// whether the selection (item or repo header) changed.
func (p *picker) setItems(items []model.Item) bool {
	before := p.selKey()
	prev, hadPrev := p.selected()
	wasEmpty := len(p.items) == 0
	p.items = p.arrange(items)
	placed := false
	if hadPrev {
		if i := p.indexOf(prev.ID); i >= 0 {
			p.cursor, placed = i, true
		}
	}
	if !placed {
		p.cursor = min(p.cursor, max(0, len(p.items)-1))
	}
	if wasEmpty && len(p.items) > 0 && p.multi {
		p.toStop(0)
	}
	p.normalize()
	p.ensureVisible()
	return p.selKey() != before
}

// selKey identifies the selection: the selected item's id, "\x00" plus the
// repo key on a header, "" for none.
func (p picker) selKey() string {
	if p.header != "" {
		return "\x00" + p.header
	}
	if it, ok := p.selected(); ok {
		return it.ID
	}
	return ""
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

// selected is the item under the cursor; ok is false on a repo header (M5.3)
// and in an empty list.
func (p *picker) selected() (model.Item, bool) {
	if p.header != "" || p.cursor < 0 || p.cursor >= len(p.items) {
		return model.Item{}, false
	}
	return p.items[p.cursor], true
}

// indexOf returns the position of item id, or -1.
func (p *picker) indexOf(id string) int {
	for i, it := range p.items {
		if it.ID == id {
			return i
		}
	}
	return -1
}

// move shifts the cursor by delta items (in a workspace: cursor stops,
// clamped); reports whether it moved.
func (p *picker) move(delta int) bool {
	if p.multi {
		return p.moveStops(delta)
	}
	return p.moveTo(p.cursor + delta)
}

// moveTo puts the cursor on item i (clamped); reports whether it moved. In
// a workspace a hidden item is revealed first (its repo unfolded, a filter
// on another repo cleared).
func (p *picker) moveTo(i int) bool {
	if p.multi {
		return p.moveToItem(i)
	}
	prev := p.cursor
	p.cursor = max(0, min(i, len(p.items)-1))
	p.ensureVisible()
	return p.cursor != prev
}

// first and last put the cursor on the first or last row the cursor can
// stop on (g and G); they report whether it moved.
func (p *picker) first() bool {
	if p.multi {
		return p.toStop(0)
	}
	return p.moveTo(0)
}

func (p *picker) last() bool {
	if p.multi {
		return p.toStop(len(p.stops()) - 1)
	}
	return p.moveTo(len(p.items) - 1)
}

func (p *picker) setSize(w, h int) {
	p.width, p.height = w, h
	p.ensureVisible()
}

// busy reports whether any item is fetching or analysing (spinner needed).
func (p picker) busy() bool {
	return p.busyCount() > 0
}

// busyCount counts items fetching or analysing.
func (p picker) busyCount() int {
	n := 0
	for _, it := range p.items {
		if it.State == model.StateFetching || it.State == model.StateAnalysing {
			n++
		}
	}
	return n
}

// pickerLine is one rendered line; item is the item index or -1. A repo
// header (M5.3) has header set and repo its key.
type pickerLine struct {
	text   string
	item   int
	repo   string
	header bool
}

// lines lays out the whole list (section dividers and rows; in a workspace
// the repo headers with their groups) unscrolled.
func (p picker) lines(st styles, rc rowCtx) []pickerLine {
	w := max(4, p.width)
	if p.multi {
		return p.workspaceLines(st, rc, w)
	}
	return p.sectionLines(st, rc, w, 0, len(p.items), false)
}

// sectionLines lays out items lo..hi-1 under their section dividers. With
// onlyFilled the sections without items get no "(0)" divider (inside a
// workspace repo group, M5.3).
func (p picker) sectionLines(st styles, rc rowCtx, w, lo, hi int, onlyFilled bool) []pickerLine {
	var out []pickerLine
	bySection := map[model.Section][]int{}
	var order []model.Section
	for i := lo; i < hi; i++ {
		s := p.items[i].Section
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
		if len(idx) == 0 && (hideWhenEmpty[s] || onlyFilled) {
			continue
		}
		label := fmt.Sprintf("%s (%d)", strings.ToUpper(sectionTitle(s)), len(idx))
		out = append(out, pickerLine{text: st.th.Divider(label, w), item: -1})
		for _, i := range idx {
			for _, l := range p.row(i, st, rc, w) {
				out = append(out, pickerLine{text: l, item: i})
			}
		}
	}
	return out
}

// sortLabel is the footer's sort indicator ("sort: urgency ▾").
func (p picker) sortLabel() string { return "sort: " + p.sort.String() + " ▾" }

// plural formats n with noun, adding "s" unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// row renders an item's lines (design: urgency bar, bold number and title
// or a blue branch name; the secondary line; a dim "why: …" line on the
// selected item). The selected row carries the "▸" marker and the
// selection background; a multi-selected row a "[x]" mark.
func (p picker) row(i int, st styles, rc rowCtx, w int) []string {
	it := p.items[i]
	th := st.th
	sel := i == p.cursor && p.header == ""
	var bg lipgloss.TerminalColor
	marker := "  "
	if sel {
		marker, bg = "▸ ", th.P.SelBg
	}
	first := []seg{{marker, st.plain}}
	if rc.marked[it.ID] {
		first = append(first, seg{"[x] ", st.accent})
	}
	if sel {
		first = append(first, urgencySegs(st, it.UrgencyQuartile)...)
	} else {
		first = append(first, seg{th.UrgencyBar(it.UrgencyQuartile), st.plain})
	}
	first = append(first, seg{" ", st.plain})
	first = append(first, labelSegs(st, it)...)
	out := []string{renderSegs(first, w, bg)}

	second := append([]seg{{"       ", st.plain}}, joinFacts(st, secondaryFacts(st, it, rc, p.items))...)
	if strip := stageStrip(it, rc, w-9); strip != "" {
		second = []seg{{"       " + strings.TrimSpace(rc.spin+" "+strip), st.dim}}
	}
	out = append(out, renderSegs(second, w, bg))
	if sel && it.Why != "" && usesNeedsYouFacts(it) {
		out = append(out, renderSegs([]seg{{"       ", st.plain}, {"why: " + it.Why, st.dim}}, w, bg))
	}
	return out
}

// urgencySegs is theme.UrgencyBar as runs (same roles: filled cells
// accent, empty ones dim), so the selected row can lay its background
// under the cells too.
func urgencySegs(st styles, q int) []seg {
	q = max(0, min(4, q))
	return []seg{{strings.Repeat("▮", q), st.accent}, {strings.Repeat("▯", 4-q), st.dim}}
}

// labelSegs is the row's label: bold "#N" and the title for a PR, the ref
// in blue for a branch.
func labelSegs(st styles, it model.Item) []seg {
	if it.Kind == model.KindPR {
		return []seg{{fmt.Sprintf("#%d", it.Number), st.bold}, {" " + it.Title, st.plain}}
	}
	return []seg{{it.Ref, st.blue}}
}

// stageStrip is the picker's one-line stage strip for an analysing item
// (spec Pipeline page: "also shown as a one-line status in the picker"),
// "" when the item is not analysing or no stage row arrived yet.
func stageStrip(it model.Item, rc rowCtx, width int) string {
	rows := rc.runLogs[it.ID]
	if it.State != model.StateAnalysing || len(rows) == 0 || rc.strip == nil {
		return ""
	}
	return rc.strip(rows, max(1, width))
}

// upsertRunLog records a stage row: it replaces the row of the same stage,
// and a row that starts a stage (queued or running) drops the rows of every
// later stage, which are about to run again.
func upsertRunLog(rows []model.RunLog, rl model.RunLog) []model.RunLog {
	starting := rl.Status == model.StageQueued || rl.Status == model.StageRunning
	pos := stageIndex(rl.Stage)
	out := make([]model.RunLog, 0, len(rows)+1)
	for _, r := range rows {
		if r.Stage == rl.Stage || (starting && stageIndex(r.Stage) > pos) {
			continue
		}
		out = append(out, r)
	}
	out = append(out, rl)
	sort.SliceStable(out, func(i, j int) bool { return stageIndex(out[i].Stage) < stageIndex(out[j].Stage) })
	return out
}

// stageIndex is s's position in model.Stages (unknown stages last).
func stageIndex(s model.Stage) int {
	for i, x := range model.Stages {
		if x == s {
			return i
		}
	}
	return len(model.Stages)
}

// itemAt returns the item index drawn on row y of the list (0 is its first
// visible line), or -1 for a divider, repo header, blank or out-of-range
// row.
func (p picker) itemAt(y int) int {
	l, ok := p.lineAt(y)
	if !ok {
		return -1
	}
	return l.item
}

// lineAt returns the line drawn on row y of the list; ok is false for a
// blank or out-of-range row.
func (p picker) lineAt(y int) (pickerLine, bool) {
	ls := p.lines(layoutStyles(), rowCtx{})
	n := p.offset + y
	if y < 0 || y >= max(1, p.height) || n < 0 || n >= len(ls) {
		return pickerLine{}, false
	}
	return ls[n], true
}

// cursorLines returns the first and last line of the cursor's row (a repo
// header's line while the cursor is on it).
func (p picker) cursorLines(ls []pickerLine) (int, int) {
	first, last := -1, -1
	for n, l := range ls {
		if p.header != "" && l.header && l.repo == p.header {
			return n, n
		}
		if p.header == "" && l.item == p.cursor {
			if first < 0 {
				first = n
			}
			last = n
		}
	}
	return max(0, first), max(0, last)
}

// ensureVisible scrolls so every line of the cursor row is on screen. The
// layout does not depend on colour or the clock, so plain styles and a zero
// rowCtx are enough.
func (p *picker) ensureVisible() {
	if p.height <= 0 {
		return
	}
	ls := p.lines(layoutStyles(), rowCtx{})
	first, last := p.cursorLines(ls)
	if first < p.offset {
		p.offset = first
	}
	if last >= p.offset+p.height {
		p.offset = last + 1 - p.height
	}
	if first <= 1 {
		p.offset = 0 // keep the first divider visible near the top
	}
	p.offset = max(0, min(p.offset, len(ls)-p.height))
}

// view renders the visible window of the list.
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

// renderSidebar draws the sidebar box (w × h cells, borders included): the
// list window, then the bottom lines (the footer or the search prompt).
func renderSidebar(st styles, p picker, rc rowCtx, bottom []string, w, h int, focused bool) string {
	body := strings.Split(p.view(st, rc), "\n")
	for len(body) < p.height {
		body = append(body, "")
	}
	body = append(body[:p.height], bottom...)
	return st.th.Box(p.title(), strings.Join(body, "\n"), w, theme.BoxOpt{Height: h, Focused: focused})
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

// itemShort is "#N" for a PR, the ref for a branch.
func itemShort(it model.Item) string {
	if it.Kind == model.KindPR {
		return fmt.Sprintf("#%d", it.Number)
	}
	return it.Ref
}

// fact is one " · "-separated part of a secondary line, as styled runs.
type fact []seg

// joinFacts joins facts with a dim " · ".
func joinFacts(st styles, fs []fact) []seg {
	var out []seg
	for i, f := range fs {
		if i > 0 {
			out = append(out, seg{" · ", st.dim})
		}
		out = append(out, f...)
	}
	return out
}

// usesNeedsYouFacts reports whether the item's secondary line is the
// Needs-you kind (Needs you, Involved, PRs in Added).
func usesNeedsYouFacts(it model.Item) bool {
	return it.Section != model.SectionYours && it.Section != model.SectionBranches && it.Kind != model.KindBranch
}

// secondaryLine is the plain text of the secondary line (tests).
func secondaryLine(it model.Item, rc rowCtx, items ...model.Item) string {
	st := layoutStyles()
	return segText(joinFacts(st, secondaryFacts(st, it, rc, items)))
}

// secondaryFacts is the second row: the item state (when not ready), then
// the section-specific facts (spec: Sections and sort; design v2).
func secondaryFacts(st styles, it model.Item, rc rowCtx, items []model.Item) []fact {
	var fs []fact
	if s := stateText(it, rc); s != "" {
		style := st.plain
		if resumable(it) {
			style = st.dim // waiting for the user, not working (M5.1)
		}
		fs = append(fs, fact{{s, style}})
	}
	if it.LabelsUnavailable {
		fs = append(fs, fact{{"labels unavailable", st.dim}})
	}
	switch {
	case it.Section == model.SectionYours:
		fs = append(fs, yoursFacts(st, it, rc.now)...)
	case it.Section == model.SectionBranches, it.Kind == model.KindBranch:
		fs = append(fs, branchFacts(st, it, rc, items)...)
	default: // Needs you, Involved, and PRs in Added
		fs = append(fs, needsYouFacts(st, it)...)
	}
	return fs
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
	case model.StateNotAnalysed:
		return "not analysed · r to run"
	case model.StateStopped:
		if it.StateNote == "" {
			return "stopped · r to resume"
		}
		return "stopped at " + it.StateNote + " · r to resume"
	case model.StateError:
		if it.StateNote == "" {
			return "error"
		}
		return "error: " + it.StateNote
	}
	return ""
}

// needsYouFacts is "⚠1 breaking · 4 eyes · ~12m · CI ✗" (design v2); an
// item without label facts yet shows its author and size instead.
func needsYouFacts(st styles, it model.Item) []fact {
	var fs []fact
	if it.Breaking > 0 {
		fs = append(fs, fact{{fmt.Sprintf("⚠%d", it.Breaking), st.accent}, {" breaking", st.plain}})
	}
	if it.NeedsEyes > 0 {
		fs = append(fs, fact{{fmt.Sprint(it.NeedsEyes), st.accent}, {" eyes", st.plain}})
	}
	if it.Minutes > 0 {
		fs = append(fs, fact{{fmt.Sprintf("~%dm", max(1, int(it.Minutes+0.5))), st.plain}})
	}
	if len(fs) == 0 {
		if it.Author != "" {
			fs = append(fs, fact{{it.Author, st.plain}})
		}
		if it.Additions != 0 || it.Deletions != 0 {
			fs = append(fs, fact{{fmt.Sprintf("+%d", it.Additions), st.add}, {" ", st.plain}, {fmt.Sprintf("−%d", it.Deletions), st.del}})
		}
	}
	if ci := ciText(it.CI); ci != "" {
		fs = append(fs, ciFact(st, it.CI, ci))
	}
	return fs
}

// ciFact colours the CI state (✓ add, ✗ del).
func ciFact(st styles, c model.CIState, text string) fact {
	switch c {
	case model.CIGreen:
		return fact{{text, st.add}}
	case model.CIRed:
		return fact{{text, st.del}}
	}
	return fact{{text, st.plain}}
}

// needEyes formats a needs-eyes count ("1 needs eyes", "5 need eyes").
func needEyes(n int) string {
	if n == 1 {
		return "1 needs eyes"
	}
	return fmt.Sprintf("%d need eyes", n)
}

func yoursFacts(st styles, it model.Item, now time.Time) []fact {
	var fs []fact
	if it.OpenThreads > 0 {
		fs = append(fs, fact{{plural(it.OpenThreads, "open thread"), st.plain}})
	}
	if ci := ciText(it.CI); ci != "" {
		fs = append(fs, ciFact(st, it.CI, ci))
	}
	if it.Draft {
		fs = append(fs, fact{{"draft", st.dim}})
	}
	if a := age(it.UpdatedAt, now); a != "" {
		fs = append(fs, fact{{a, st.plain}})
	}
	return fs
}

// branchFacts is "ready for PR .81 · 3 unpushed · 6d" once the branch state
// is known ("abandoned? .70", "superseded by #12 .77"); before that the M0
// line "3 unpushed · 4 ahead · 2d".
func branchFacts(st styles, it model.Item, rc rowCtx, items []model.Item) []fact {
	var fs []fact
	when := it.UpdatedAt
	if !it.LastCommitAt.IsZero() {
		when = it.LastCommitAt
	}
	if it.BranchState != "" {
		state := branchStateWords(it.BranchState, supersededBy(it, rc, items))
		fs = append(fs, fact{{state, st.plain}, {" " + codeview.ProbText(it.BranchStateP), st.dim}})
		if it.Unpushed > 0 {
			fs = append(fs, fact{{fmt.Sprintf("%d unpushed", it.Unpushed), st.plain}})
		}
	} else {
		if it.Unpushed > 0 {
			fs = append(fs, fact{{fmt.Sprintf("%d unpushed", it.Unpushed), st.plain}})
		}
		if it.Ahead > 0 {
			fs = append(fs, fact{{fmt.Sprintf("%d ahead", it.Ahead), st.plain}})
		}
	}
	if a := age(when, rc.now); a != "" {
		fs = append(fs, fact{{a, st.plain}})
	}
	return fs
}

// supersededBy is the PR number superseding a branch: from its loaded
// detail, else an open PR item of the same repo (M5.3) in the list with the
// branch as its head.
func supersededBy(it model.Item, rc rowCtx, items []model.Item) int {
	if n := rc.superseded[it.ID]; n > 0 {
		return n
	}
	if it.BranchState != model.BranchSuperseded {
		return 0
	}
	for _, o := range items {
		if o.Kind == model.KindPR && o.Ref != "" && o.Ref == it.Ref && o.Repo == it.Repo {
			return o.Number
		}
	}
	return 0
}

// branchStateWords names a branch state the spec's way: "abandoned?" (a
// guess) and "superseded by #N" when the PR is known.
func branchStateWords(state string, by int) string {
	switch state {
	case model.BranchAbandoned:
		return "abandoned?"
	case model.BranchSuperseded:
		if by > 0 {
			return fmt.Sprintf("superseded by #%d", by)
		}
	}
	return state
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
