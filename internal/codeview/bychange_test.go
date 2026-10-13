package codeview

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// loadGroups reads the by-change fixture (a fresh copy per call).
func loadGroups(t testing.TB) proto.Detail {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "detail_groups.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var d proto.Detail
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return d
}

// byChange returns a model over the group fixture switched to mode B.
func byChangeModel(t *testing.T, d proto.Detail, w, h int, off bool) Model {
	t.Helper()
	m := newModel(t, d, w, h)
	if off {
		allOff(&m)
	}
	press(t, &m, "g")
	if m.Mode() != ModeChange {
		t.Fatalf("g did not switch to by change (note %q)", m.Note())
	}
	return m
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("View mismatch\n--- got\n%s\n--- want\n%s", got, want)
	}
}

func TestGoldenByChange(t *testing.T) {
	cases := map[string]func(t *testing.T) Model{
		"by_change_default": func(t *testing.T) Model { return byChangeModel(t, loadGroups(t), 120, 30, false) },
		"by_change_mechanical": func(t *testing.T) Model {
			m := byChangeModel(t, loadGroups(t), 120, 30, true)
			press(t, &m, "J", "J", "J")
			return m
		},
		"by_change_narrow": func(t *testing.T) Model { return byChangeModel(t, loadGroups(t), 50, 16, true) },
		"by_change_hidden_group": func(t *testing.T) Model {
			m := byChangeModel(t, loadGroups(t), 120, 30, false)
			press(t, &m, "J")
			return m
		},
		"by_change_labels_missing": func(t *testing.T) Model {
			d := loadGroups(t)
			d.Agg.LabelsMissing = true
			return byChangeModel(t, d, 120, 30, false)
		},
		"by_change_no_embedder": func(t *testing.T) Model {
			return byChangeModel(t, noEmbedder(loadGroups(t)), 120, 30, false)
		},
		"by_file_mechanical_repeat": func(t *testing.T) Model {
			m := newModel(t, loadGroups(t), 120, 30)
			allOff(&m)
			selectFile(t, &m, "web/src/pages/b.tsx")
			return m
		},
		"by_change_no_groups": func(t *testing.T) Model {
			d := loadGroups(t)
			d.Groups = nil
			m := newModel(t, d, 120, 30)
			press(t, &m, "g")
			return m
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			m := build(t)
			checkGolden(t, name, m.View()+"\n")
		})
	}
}

// selectFile presses J (by file) until path is selected.
func selectFile(t *testing.T, m *Model, path string) {
	t.Helper()
	for range m.c.files {
		if m.SelectedFile() == path {
			return
		}
		press(t, m, "J")
	}
	if m.SelectedFile() != path {
		t.Fatalf("file %q not reached (at %q)", path, m.SelectedFile())
	}
}

// Spec Code page mode A: mechanical repeats are collapsed by default and
// `z` expands them; the example unit stays expanded.
func TestByFileCollapsesMechanicalRepeats(t *testing.T) {
	m := newModel(t, loadGroups(t), 120, 30)
	allOff(&m)
	selectFile(t, &m, "web/src/pages/a.tsx")
	lacks(t, ansi.Strip(m.View()), "mechanical repeat")
	selectFile(t, &m, "web/src/pages/b.tsx")
	contains(t, ansi.Strip(m.View()), "mechanical repeat of a.tsx")
	contains(t, ansi.Strip(m.View()), "(×5)")
	press(t, &m, "z")
	lacks(t, ansi.Strip(m.View()), "mechanical repeat")
}

const testEmbedNote = "no embedding model: groups from structural links only"

// noEmbedder marks d as grouped without embeddings (stage 3 skipped).
func noEmbedder(d proto.Detail) proto.Detail {
	g := model.GroupingStats{}
	if d.Agg.Grouping != nil {
		g = *d.Agg.Grouping
	}
	g.EmbedNote = testEmbedNote
	d.Agg.Grouping = &g
	return d
}

// The spec's "skip stage 3 … with a status note" is shown by change (where
// the groups are) and not by file.
func TestEmbedNoteShownByChange(t *testing.T) {
	m := newModel(t, noEmbedder(loadGroups(t)), 120, 30)
	if strings.Contains(ansi.Strip(m.View()), testEmbedNote) {
		t.Error("embed note shown by file")
	}
	press(t, &m, "g")
	if !strings.Contains(ansi.Strip(m.View()), testEmbedNote) {
		t.Errorf("embed note missing by change:\n%s", ansi.Strip(m.View()))
	}
	m = byChangeModel(t, loadGroups(t), 120, 30, false)
	if strings.Contains(ansi.Strip(m.View()), "structural links only") {
		t.Error("embed note shown when stage 3 ran")
	}
}

func curID(m Model) string {
	u, ok := m.CurrentUnit()
	if !ok {
		return ""
	}
	return u.ID
}

func groupID(m Model) string { return m.c.groups[m.sel].g.ID }

func TestModeNoGroups(t *testing.T) {
	d := loadGroups(t)
	d.Groups = nil
	m := newModel(t, d, 120, 30)
	press(t, &m, "g")
	if m.Mode() != ModeFile || m.Note() != "no groups yet" {
		t.Fatalf("mode %v note %q", m.Mode(), m.Note())
	}
	if m.SetMode(ModeChange) {
		t.Error("SetMode(ModeChange) without groups succeeded")
	}
	// Groups arriving for the same item keep by file until g.
	m.SetDetail(loadGroups(t))
	if m.Mode() != ModeFile {
		t.Error("groups switched the mode on their own")
	}
	press(t, &m, "g")
	if m.Mode() != ModeChange {
		t.Error("g did not switch once groups exist")
	}
}

func TestModeRoundTripKeepsUnit(t *testing.T) {
	m := newModel(t, loadGroups(t), 120, 5)
	allOff(&m)
	if !m.FocusUnit("k1") {
		t.Fatal("FocusUnit(k1)")
	}
	press(t, &m, "j", "j") // inside k1, off its header
	_, delta := m.cursor()
	press(t, &m, "g")
	if m.Mode() != ModeChange || curID(m) != "k1" || groupID(m) != "g-user" {
		t.Fatalf("after g: mode %v unit %q group %q", m.Mode(), curID(m), groupID(m))
	}
	if _, d := m.cursor(); d != delta {
		t.Errorf("offset within the unit %d, want %d", d, delta)
	}
	press(t, &m, "g")
	if m.Mode() != ModeFile || curID(m) != "k1" || m.SelectedFile() != "web/src/components/UserCard.tsx" {
		t.Fatalf("after g g: mode %v unit %q file %q", m.Mode(), curID(m), m.SelectedFile())
	}
}

func TestByChangeGroupAndUnitNavigation(t *testing.T) {
	m := byChangeModel(t, loadGroups(t), 120, 30, true)
	var groups []string
	for range 7 {
		press(t, &m, "J")
		groups = append(groups, groupID(m))
	}
	if want := "g-apierror g-cache g-imports g-f g-theme g-save g-save"; strings.Join(groups, " ") != want {
		t.Errorf("J sequence = %q, want %q", strings.Join(groups, " "), want)
	}
	press(t, &m, "K", "K", "K", "K", "K", "K")
	if groupID(m) != "g-user" || curID(m) != "c1" {
		t.Fatalf("K back to %q unit %q", groupID(m), curID(m))
	}
	var units []string
	for range 10 {
		press(t, &m, "n")
		units = append(units, curID(m))
	}
	// m2..m5 are behind the mechanical example m1.
	if want := "k1 t1 c2 e1 p1 m1 m6 s1 l1 l1"; strings.Join(units, " ") != want {
		t.Errorf("n sequence = %q, want %q", strings.Join(units, " "), want)
	}
	if m.Note() == "" {
		t.Error("no note at the last unit")
	}
	press(t, &m, "p", "p")
	if curID(m) != "m6" || groupID(m) != "g-f" {
		t.Errorf("p p = %q in %q", curID(m), groupID(m))
	}

	// Default filters: n skips hidden units and groups.
	m = byChangeModel(t, loadGroups(t), 120, 30, false)
	units = nil
	for range 3 {
		press(t, &m, "n")
		units = append(units, curID(m))
	}
	if want := "k1 p1 p1"; strings.Join(units, " ") != want {
		t.Errorf("filtered n = %q, want %q", strings.Join(units, " "), want)
	}
}

func TestByChangeFocusUnit(t *testing.T) {
	m := byChangeModel(t, loadGroups(t), 120, 30, false)
	if !m.FocusUnit("e1") || groupID(m) != "g-apierror" || curID(m) != "e1" {
		t.Fatalf("FocusUnit(e1): group %q unit %q", groupID(m), curID(m))
	}
	contains(t, m.View(), "moved from web/src/api/client.ts:30")
	if !m.FocusUnit("m3") || groupID(m) != "g-imports" || curID(m) != "m1" {
		t.Errorf("FocusUnit(m3): group %q unit %q, want the mechanical example", groupID(m), curID(m))
	}
	if m.SelectedFile() != "web/src/pages/a.tsx" {
		t.Errorf("SelectedFile = %q", m.SelectedFile())
	}
	if m.FocusUnit("nope") {
		t.Error("FocusUnit of an unknown unit")
	}
}

func TestByChangeExpandNoise(t *testing.T) {
	m := byChangeModel(t, loadGroups(t), 120, 30, true)
	press(t, &m, "J")
	contains(t, m.View(), "▸ moved from web/src/api/client.ts:30 · ApiError · +5 −0 (z to expand)")
	press(t, &m, "z")
	v := m.View()
	lacks(t, v, "(z to expand)")
	contains(t, v, "  moved from web/src/api/client.ts:30")
	press(t, &m, "g") // by file: the group's expansion does not apply
	if !m.collapsed("e1") {
		t.Error("z on a group expanded the unit by file too")
	}
}

func TestMergeGroups(t *testing.T) {
	m := byChangeModel(t, loadGroups(t), 120, 30, true)
	press(t, &m, "M")
	if m.Note() != noteMergeStart {
		t.Errorf("note %q", m.Note())
	}
	if ls := lines(m); !strings.HasPrefix(listPart(ls[3]), "▸*1 Pass request options") || !strings.Contains(ls[1], "marked for me") {
		t.Errorf("mark not drawn: %q / %q", ls[3], ls[1])
	}
	press(t, &m, "M")
	if m.mergeMark != "" || m.Note() != noteMergeCancel {
		t.Errorf("M on the marked group: mark %q note %q", m.mergeMark, m.Note())
	}
	press(t, &m, "M", "esc")
	if m.mergeMark != "" {
		t.Error("esc did not cancel the mark")
	}
	if m.HandleKey(keyMsg("esc")) {
		t.Error("esc consumed without a mark (it must fall through to back)")
	}
	if _, ok := m.PendingEdit(); ok {
		t.Fatal("pending edit after cancelled merges")
	}
	press(t, &m, "M", "J", "M")
	e, ok := m.PendingEdit()
	if !ok || e.Op != model.EditMerge || e.ItemID != "pr:11" || fmt.Sprint(e.Units) != "[c1 k1 t1 c2 e1]" {
		t.Fatalf("merge edit = %+v %v", e, ok)
	}
	if _, ok := m.PendingEdit(); ok {
		t.Error("PendingEdit did not clear")
	}
	if m.mergeMark != "" {
		t.Error("mark kept after the merge")
	}
	// Marking a later group first: units still follow list order.
	press(t, &m, "J", "M", "K", "K", "M")
	if e, _ := m.PendingEdit(); fmt.Sprint(e.Units) != "[c1 k1 t1 p1]" {
		t.Errorf("merge units = %v", e.Units)
	}
	m2 := newModel(t, loadGroups(t), 120, 30)
	press(t, &m2, "M")
	if _, ok := m2.PendingEdit(); ok || !strings.Contains(m2.Note(), "by change only") {
		t.Errorf("M by file: note %q", m2.Note())
	}
}

func TestSplitGroup(t *testing.T) {
	m := byChangeModel(t, loadGroups(t), 120, 30, true)
	split := func() []string {
		t.Helper()
		press(t, &m, "X")
		e, ok := m.PendingEdit()
		if !ok {
			t.Fatalf("no split edit (note %q)", m.Note())
		}
		if e.Op != model.EditSplit || e.ItemID != "pr:11" {
			t.Fatalf("edit %+v", e)
		}
		return e.Units
	}
	if got := split(); fmt.Sprint(got) != "[c1]" {
		t.Errorf("split at the first unit = %v, want [c1]", got)
	}
	press(t, &m, "n")
	if got := split(); fmt.Sprint(got) != "[k1 t1]" {
		t.Errorf("split at k1 = %v", got)
	}
	press(t, &m, "n")
	if got := split(); fmt.Sprint(got) != "[t1]" {
		t.Errorf("split at t1 = %v", got)
	}
	refusals := []struct{ moves, note string }{
		{"JJ", "one-unit"},  // g-cache: a single (user-edited) unit
		{"J", "mechanical"}, // g-imports
		{"J", "ungrouped"},  // g-f
		{"JJ", "ungrouped"}, // g-save
	}
	for _, r := range refusals {
		for _, k := range r.moves {
			press(t, &m, string(k))
		}
		press(t, &m, "X")
		if _, ok := m.PendingEdit(); ok || !strings.Contains(m.Note(), r.note) {
			t.Errorf("X on %s: note %q, want %q and no edit", groupID(m), m.Note(), r.note)
		}
	}
	m2 := newModel(t, loadGroups(t), 120, 30)
	press(t, &m2, "X")
	if _, ok := m2.PendingEdit(); ok || !strings.Contains(m2.Note(), "by change only") {
		t.Errorf("X by file: note %q", m2.Note())
	}
}

func TestByChangeMouse(t *testing.T) {
	m := byChangeModel(t, loadGroups(t), 120, 30, true)
	m.SetOrigin(0, 2)
	click := func(row int) {
		m, _ = m.Update(tea.MouseMsg{X: 5, Y: 2 + row, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	}
	// Body row 0 is the box border, 1 the "by:" line, then two lines per group.
	click(1) // the "by:" line: no change
	if groupID(m) != "g-user" {
		t.Errorf("click on by: %q", groupID(m))
	}
	click(5) // g-apierror's second line
	if groupID(m) != "g-apierror" {
		t.Errorf("click: %q", groupID(m))
	}
	click(10) // ungrouped divider: no change
	if groupID(m) != "g-apierror" {
		t.Errorf("click on the divider: %q", groupID(m))
	}
	ls := lines(m)
	if !strings.HasPrefix(listPart(ls[1]), "by: file [change] · interfaces → call") ||
		!strings.HasPrefix(listPart(ls[10]), "── ungrouped (3)") || !strings.HasPrefix(listPart(ls[17]), "── split suggestion: group 6") ||
		!strings.HasPrefix(listPart(ls[18]), "  split? web/src/styles/theme.css .74") {
		t.Fatalf("list layout:\n%s", m.View())
	}
	click(18) // split suggestion row → the file's group
	if groupID(m) != "g-theme" || curID(m) != "s1" {
		t.Errorf("split row: %q %q", groupID(m), curID(m))
	}
	m, _ = m.Update(tea.MouseMsg{X: 5, Y: 5, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if groupID(m) != "g-save" {
		t.Errorf("wheel: %q", groupID(m))
	}
}

// A regroup (the daemon re-runs clustering after an edit) keeps the cursor
// on the same unit, now in another group.
func TestByChangeReloadKeepsUnit(t *testing.T) {
	m := byChangeModel(t, loadGroups(t), 120, 30, true)
	press(t, &m, "n")
	d := loadGroups(t)
	d.Groups[0].UnitIDs = []string{"c1"}
	d.Groups = append(d.Groups, model.Group{ID: "g-split", UnitIDs: []string{"k1", "t1"}, Name: "UserCard",
		NameSource: model.NamePlaceholder, Order: 1, UserEdited: true})
	d.Groups[1].Order, d.Groups[2].Order, d.Groups[3].Order = 2, 3, 4
	m.SetDetail(d)
	if m.Mode() != ModeChange || curID(m) != "k1" || groupID(m) != "g-split" {
		t.Fatalf("after regroup: mode %v unit %q group %q", m.Mode(), curID(m), groupID(m))
	}
	contains(t, m.View(), "edited by you")
}

func TestByChangeHints(t *testing.T) {
	keysOf := func(bs []string) string { return strings.Join(bs, " ") }
	names := func(m Model) []string {
		var out []string
		for _, b := range m.ShortBindings() {
			out = append(out, b.Help().Key)
		}
		return out
	}
	m := newModel(t, loadGroups(t), 120, 30)
	if got := keysOf(names(m)); strings.Contains(got, "M") || !strings.Contains(got, "g") {
		t.Errorf("by file hints = %q", got)
	}
	press(t, &m, "g")
	if got := keysOf(names(m)); !strings.Contains(got, "M X") || !strings.Contains(got, "g") {
		t.Errorf("by change hints = %q", got)
	}
	var help []string
	for _, row := range m.HelpBindings() {
		for _, b := range row {
			help = append(help, b.Help().Key+"="+b.Help().Desc)
		}
	}
	h := strings.Join(help, ",")
	for _, want := range []string{"g=by file / by change", "M=merge groups", "X=split group", "J=next group", "home=top"} {
		if !strings.Contains(h, want) {
			t.Errorf("help lacks %q: %s", want, h)
		}
	}
}

func TestByChangeContent(t *testing.T) {
	m := byChangeModel(t, loadGroups(t), 160, 40, true)
	v := m.View()
	contains(t, v, "│b2 .82 · risk 4 · auth · error_path · tests: 2 of 3 units · test gap 1 · ⚠ breaking 1 · routing: not routed")
	contains(t, v, "┌─ why these belong together ─")
	contains(t, v, "│ calls  UserCard.tsx UserCard → client.ts getUser")
	contains(t, v, "│ tests  client.test.ts → client.ts getUser")
	contains(t, v, "│ jev caller update .82 · cos .79  UserCard.tsx UserCard → client.test.ts")
	contains(t, v, "── Units · interface first ──")
	contains(t, v, "│1 web/src/api/client.ts@10")
	contains(t, v, "caller · eyes│")
	contains(t, v, "findings: not reviewed (not routed)")
	contains(t, v, "    3u 3f · b2 .82 · not routed")
	contains(t, v, "‹ApiError›")
	press(t, &m, "J")
	contains(t, m.View(), "│ moved  client.ts ApiError → errors.ts ApiError")
	press(t, &m, "J")
	v = m.View()
	contains(t, v, "│b1 .64 · edited by you · risk 3 · tests: 0 of 1 unit · test gap 1 · ? 2 unsure · routing: not routed")
	contains(t, v, "grouped by you (merge or split)")
	press(t, &m, "J")
	v = m.View()
	contains(t, v, "×5 verified identical")
	contains(t, v, "members: web/src/pages/b.tsx · web/src/pages/c.tsx · web/src/pages/d.tsx · web/src/pages/e.tsx")
	contains(t, v, "promoted out: f.tsx → ‹f.tsx›")
	contains(t, v, "same shape ×5 (verified)")
	press(t, &m, "J")
	contains(t, m.View(), "│ungrouped · routing: not routed ") // no GroupAgg row for g-f
}

func TestMemberLine(t *testing.T) {
	paths := []string{"aaaa", "bbbb", "cccc"}
	tests := []struct {
		width int
		want  string
	}{
		{80, "members: aaaa · bbbb · cccc"},
		{25, "members: aaaa +2 more"},
		{5, "members: +3 more"},
	}
	for _, tt := range tests {
		if got := memberLine(paths, tt.width); got != tt.want {
			t.Errorf("memberLine(%d) = %q, want %q", tt.width, got, tt.want)
		}
	}
}

func TestByChangeViewDimensions(t *testing.T) {
	states := map[string]func(m *Model){
		"default": func(*Model) {},
		"all-off": func(m *Model) { allOff(m) },
		"mechanical": func(m *Model) {
			allOff(m)
			m.HandleKey(keyMsg("J"))
			m.HandleKey(keyMsg("J"))
			m.HandleKey(keyMsg("J"))
		},
		"marked":  func(m *Model) { m.HandleKey(keyMsg("M")) },
		"overlay": func(m *Model) { m.HandleKey(keyMsg("L")) },
		"bottom":  func(m *Model) { allOff(m); m.HandleKey(keyMsg("G")) },
	}
	renderers := map[string]*lipgloss.Renderer{"plain": plainRenderer(), "colour": colourRenderer()}
	for name, prep := range states {
		for rname, r := range renderers {
			for _, w := range []int{1, 20, 59, 60, 80, 160} {
				for _, h := range []int{1, 2, 5, 30} {
					m := New(r)
					m.SetSize(w, h)
					m.SetDetail(loadGroups(t))
					m.HandleKey(keyMsg("g"))
					prep(&m)
					checkView(t, fmt.Sprintf("%s/%s/w%d/h%d", name, rname, w, h), m.View(), w, h)
				}
			}
		}
	}
}

// listPart returns the list box content of a view line at width 120.
func listPart(line string) string { return listOf(line) }

func TestByChangeHiddenGroupOneLiner(t *testing.T) {
	m := byChangeModel(t, loadGroups(t), 120, 30, false)
	ls := lines(m)
	if got := listPart(ls[4]); !strings.HasPrefix(got, "  2 ‹ApiError› — 2 units hidden by fi") {
		t.Errorf("hidden one-liner = %q", got)
	}
	press(t, &m, "J")
	if groupID(m) != "g-apierror" {
		t.Fatalf("J onto a hidden group: %q", groupID(m))
	}
	contains(t, m.View(), "2 units hidden by filters (f)")
}
