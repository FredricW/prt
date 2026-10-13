package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// M5.2 review fixes: headline candidates, the full-list overlay's block
// keys, labels after a head move and after a jump, the git-facts Files
// order.

// tea130x40 sizes a model wide enough for the two-column Overview.
var tea130x40 = tea.WindowSizeMsg{Width: 130, Height: 40}

// Headline candidates below .60 are rows of the full list, anchored to
// their units; the summary's "+N below .60 (unsure)" line opens that list.
func TestHeadlineCandidatesListed(t *testing.T) {
	d := richDetail(t)
	d.Agg.HeadlineCandidates = []model.Headline{
		{UnitID: "u-devices", Path: "web/src/devices.ts", Line: 4, Kind: "env/flag", Text: "DEVICE_LIMIT", P: 0.55},
		{UnitID: "u-format", Path: "web/src/format.ts", Line: 9, Kind: "breaking", Text: "formatDate()", P: 0.52},
	}
	d.Agg.HeadlinesUnsure = 2
	r := plainRenderer()
	st := newStyles(r)
	p := overviewPage(st, newMarkdown(r), d, fixedNow, 90)
	more := -1
	for i, l := range p.lines {
		if strings.Contains(l, "+2 below .60 (unsure)") {
			more = i
		}
	}
	if more < 0 || len(p.mores[more]) == 0 || p.mores[more][0].block != blockHeadlines {
		t.Fatalf("the unsure line does not open the Headlines list (line %d, mores %+v)", more, p.mores[more])
	}
	if hl := section(strings.Join(p.lines, "\n"), blockHeadlines); strings.Contains(hl, "DEVICE_LIMIT") {
		t.Errorf("a candidate shows in the summary:\n%s", hl)
	}
	full, ok := fullBlock(st, d, defaultOvOpts(), blockHeadlines, 86)
	if !ok {
		t.Fatal("no Headlines list")
	}
	want := map[string]string{"DEVICE_LIMIT": "u-devices", "formatDate()": "u-format"}
	for i, l := range full.lines {
		for text, unit := range want {
			if !strings.Contains(l, text) {
				continue
			}
			if !strings.HasPrefix(l, "? ") || full.anchors[i] != unit {
				t.Errorf("candidate row %q: want a ? row anchored to %s, anchor %q", l, unit, full.anchors[i])
			}
			delete(want, text)
		}
	}
	if len(want) > 0 {
		t.Errorf("full list lacks candidates %v:\n%s", want, strings.Join(full.lines, "\n"))
	}
	// No listed row: the empty state does not contradict the unsure line.
	d.Agg.Headlines = nil
	hl := section(renderAt(d, 90), blockHeadlines)
	if !strings.Contains(hl, "no contract changes at .60 or above") || !strings.Contains(hl, "+2 below .60 (unsure)") {
		t.Errorf("empty state with candidates:\n%s", hl)
	}
	d.Agg.HeadlineCandidates, d.Agg.HeadlinesUnsure = nil, 0
	if hl := section(renderAt(d, 90), blockHeadlines); !strings.Contains(hl, "no contract changes") || strings.Contains(hl, "below .60") {
		t.Errorf("empty state:\n%s", hl)
	}
}

// J/K in a full list skip boxes without a list; with none left the list
// and the focus stay, so the overlay always shows the focused block.
func TestListBlockKeysSkipBoxesWithoutList(t *testing.T) {
	m, _ := blockModel(t, nil)
	last := m.overview.order[len(m.overview.order)-1]
	if last != blockDescription {
		t.Fatalf("reading order %v: want Description last", m.overview.order)
	}
	m.overview.focus = blockSince
	m.openBlock(blockSince)
	m = press(t, m, "J")
	if l := m.overview.list; l == nil || l.block != blockSince || m.overview.focus != blockSince || !m.overview.raw {
		t.Fatalf("J past the last list: focus %q list %+v", m.overview.focus, l)
	}
	if !strings.Contains(m.note, "no other list") {
		t.Errorf("note %q", m.note)
	}
	m = press(t, m, "K")
	if l := m.overview.list; l == nil || l.block != m.overview.focus || l.block == blockSince {
		t.Fatalf("K: focus %q list %+v", m.overview.focus, l)
	}

	// The git-facts page: J from Files skips the note boxes to API endpoints.
	d := richDetail(t)
	d.Agg.LabelsMissing = true
	f := newFake(d.Item)
	f.details[d.Item.ID] = d
	m2, _ := withFakePane(newTestModel(t, f, Options{}))
	m2 = update(t, m2, tea130x40)
	m2 = feed[detailMsg](t, m2, m2.selectionChanged())
	m2 = press(t, m2, "enter")
	m2.overview.focus = blockFiles
	m2.openBlock(blockFiles)
	m2 = press(t, m2, "J")
	if l := m2.overview.list; l == nil || l.block != blockAPI || m2.overview.focus != blockAPI {
		t.Fatalf("J from Files: focus %q list %+v", m2.overview.focus, l)
	}
}

// A Labels answer for a head no longer shown is not lost: the Code page
// stops waiting and the unit is fetched again for the shown head.
func TestStaleLabelsAnswerAsksAgain(t *testing.T) {
	f := fullFake(t)
	d := f.details["pr:1"]
	d.Units[0].Diff = "+a\n+b\n"
	d.Agg.Units[0].Behaviour, d.Agg.Units[0].BehaviourP = true, 0.9
	f.details["pr:1"] = d
	m := loaded(t, f)
	m = press(t, m, "2", "enter")
	m, labelsCmd := updateCmd(t, m, keyMsg("L"))
	if labelsCmd == nil || !strings.Contains(m.View(), "loading labels…") {
		t.Fatalf("L did not start a Labels call:\n%s", m.View())
	}
	// A run on a new head finishes while the call is in flight.
	it := m.detail.Item
	it.AnalysedSHA = "a2"
	d.Item = it
	f.details["pr:1"] = d
	m = update(t, m, eventMsg{Type: proto.EventItem, ItemID: it.ID, Item: &it})
	// The a1 answer lands after the head moved.
	var next = labelsCmd
	for range 4 {
		var got bool
		for _, msg := range run(next) {
			if _, ok := msg.(labelsMsg); ok {
				m, next = updateCmd(t, m, msg)
				got = true
			}
		}
		if !got {
			break
		}
	}
	// The reload applies.
	m, cmd := updateCmd(t, m, reloadTickMsg{gen: m.loads.gen, at: time.Now()})
	m = feedAll[detailMsg](t, m, cmd)
	if len(f.labelCalls) != 2 {
		t.Fatalf("Labels calls %d, want 2 (asked again for a2)", len(f.labelCalls))
	}
	if v := m.View(); strings.Contains(v, "loading labels…") || !strings.Contains(v, "breaking") {
		t.Errorf("overlay stuck:\n%s", v)
	}
	// L again on the same unit is served from the cache.
	m = press(t, m, "esc")
	_, _ = updateCmd(t, m, keyMsg("L"))
	if len(f.labelCalls) != 2 {
		t.Errorf("cached labels fetched again: %d calls", len(f.labelCalls))
	}
}

// A jump from the Overview to Code (enter, a click) collects the label
// requests the Code page made for the target.
func TestJumpFromOverviewCollectsCodeLabels(t *testing.T) {
	f := fullFake(t)
	m, p := withFakePane(loaded(t, f))
	m = update(t, m, tea130x40)
	m = press(t, m, "enter")
	m.overview.vp.SetYOffset(lineOf(t, m, "Headlines"))
	p.itemID, p.wantLabels = "pr:1", []string{"u-devices"}
	m, cmd := updateCmd(t, m, keyMsg("enter"))
	if m.tab != TabCode || len(p.focused) != 1 {
		t.Fatalf("enter did not jump: tab %v focused %v", m.tab, p.focused)
	}
	m = feedAll[labelsMsg](t, m, cmd)
	if len(f.labelCalls) != 1 || len(p.setLabels) != 1 {
		t.Fatalf("enter: Labels calls %d, handed over %v", len(f.labelCalls), p.setLabels)
	}
	// A click on a headline row.
	m = press(t, m, "1")
	m.overview.vp.GotoTop()
	sw, _, _ := m.dims()
	x, y := -1, -1
	for i, l := range strings.Split(m.overview.view(), "\n") {
		if c := strings.Index(l, "refresh(token)"); c >= 0 {
			x, y = sw+2+ansi.StringWidth(l[:c])+1, i+2
			break
		}
	}
	if y < 0 {
		t.Fatalf("no headline row in view:\n%s", m.overview.view())
	}
	p.wantLabels = []string{"u-server"}
	m, cmd = updateCmd(t, m, mouseClick(x, y))
	if m.tab != TabCode {
		t.Fatalf("click did not jump")
	}
	_ = feedAll[labelsMsg](t, m, cmd)
	if len(f.labelCalls) != 2 {
		t.Errorf("click: Labels calls %d, want 2", len(f.labelCalls))
	}
}

// The git-facts Files list is ordered by importance (owned first, then
// changed lines) and each row opens its file's first unit.
func TestGitFactsFilesOrderedAndAnchored(t *testing.T) {
	d := richDetail(t)
	d.Agg.LabelsMissing = true
	d.Files = []model.File{
		{Path: "small-owned.go", Adds: 1, Owned: true},
		{Path: "big-other.go", Adds: 90, Dels: 10},
		{Path: "big-owned.go", Adds: 40, Owned: true},
	}
	d.Units = append(d.Units,
		model.ChangeUnit{ID: "u-big-owned", Path: "big-owned.go"},
		model.ChangeUnit{ID: "u-big-other", Path: "big-other.go"},
	)
	r := plainRenderer()
	p := overviewPage(newStyles(r), newMarkdown(r), d, fixedNow, 90)
	text := strings.Join(p.lines, "\n")
	files := section(text, blockFiles)
	a, b, c := strings.Index(files, "big-owned.go"), strings.Index(files, "small-owned.go"), strings.Index(files, "big-other.go")
	if a < 0 || a >= b || b >= c {
		t.Errorf("Files order:\n%s", files)
	}
	for i, l := range p.lines {
		switch {
		case strings.Contains(l, "big-owned.go") && p.anchors[i] != "u-big-owned":
			t.Errorf("big-owned.go row anchored to %q", p.anchors[i])
		case strings.Contains(l, "big-other.go") && p.anchors[i] != "u-big-other":
			t.Errorf("big-other.go row anchored to %q", p.anchors[i])
		}
	}
	if fmt.Sprint(len(gitFiles(d))) != "3" {
		t.Errorf("gitFiles: %+v", gitFiles(d))
	}
}
