package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden")

// golden compares got with testdata/name.golden (rewritten with -update).
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if string(want) != got {
		t.Errorf("%s mismatch (run with -update)\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func ago(d time.Duration) time.Time { return fixedNow.Add(-d) }

// pickerItems covers every section, quartiles 0–4 and every state.
func pickerItems() []model.Item {
	return []model.Item{
		{ID: "pr:482", Kind: model.KindPR, Number: 482, Title: "Rework session refresh and add device list", Section: model.SectionNeedsYou,
			Author: "alice", Additions: 312, Deletions: 88, CI: model.CIRed, Why: "asked you · CI red · age", Urgency: 9, UrgencyQuartile: 4,
			State: model.StateReady, UpdatedAt: ago(26 * time.Hour)},
		{ID: "pr:490", Kind: model.KindPR, Number: 490, Title: "Rename fetchUser to loadUser", Section: model.SectionNeedsYou,
			Author: "bob", Additions: 40, Deletions: 40, CI: model.CIGreen, Urgency: 5, UrgencyQuartile: 3,
			State: model.StateAnalysing, StateNote: "parse", UpdatedAt: ago(3 * time.Hour)},
		{ID: "pr:491", Kind: model.KindPR, Number: 491, Title: "Drop legacy exporter", Section: model.SectionYours,
			CI: model.CIPending, Draft: true, Urgency: 2, UrgencyQuartile: 2, State: model.StateStale, UpdatedAt: ago(5 * 24 * time.Hour)},
		{ID: "pr:493", Kind: model.KindPR, Number: 493, Title: "Bump lodash", Section: model.SectionYours,
			Urgency: 1, UrgencyQuartile: 1, State: model.StateFetching, UpdatedAt: ago(30 * time.Minute)},
		{ID: "branch:spike/graph", Kind: model.KindBranch, Ref: "spike/graph", Title: "try graph layout", Section: model.SectionBranches,
			Unpushed: 3, Ahead: 4, UrgencyQuartile: 0, State: model.StateError, StateNote: "diff: exit 128", UpdatedAt: ago(2 * 24 * time.Hour)},
		{ID: "branch:feat/a-very-long-branch-name-that-will-not-fit-in-the-sidebar", Kind: model.KindBranch,
			Ref: "feat/a-very-long-branch-name-that-will-not-fit-in-the-sidebar", Section: model.SectionBranches,
			Ahead: 1, UrgencyQuartile: 1, Urgency: 0.5, UpdatedAt: ago(5 * time.Hour)},
		{ID: "pr:12", Kind: model.KindPR, Number: 12, Title: "Old context PR", Section: model.SectionAdded,
			Author: "carol", CI: model.CIGreen, UrgencyQuartile: 0, State: model.StateReady},
	}
}

func renderPicker(items []model.Item, cursor, w, h int) string {
	p := picker{}
	p.setSize(w, h)
	p.setItems(items)
	p.moveTo(cursor)
	return p.view(newStyles(plainRenderer()), rowCtx{now: fixedNow, spin: "*"})
}

func TestPickerGolden(t *testing.T) {
	tests := []struct {
		name   string
		items  []model.Item
		cursor int
		w, h   int
	}{
		{"picker_all_sections", pickerItems(), 0, 40, 40},
		{"picker_narrow_scrolled", pickerItems(), 6, 24, 8},
		{"picker_empty", nil, 0, 36, 10},
		{"picker_branches_only", pickerItems()[4:6], 1, 36, 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderPicker(tt.items, tt.cursor, tt.w, tt.h)
			if strings.Contains(got, "\x1b[") {
				t.Fatal("picker output contains ANSI escapes")
			}
			for _, l := range strings.Split(got, "\n") {
				if n := len([]rune(l)); n > tt.w {
					t.Errorf("line wider than %d: %q", tt.w, l)
				}
			}
			golden(t, tt.name, got)
		})
	}
}

func TestPickerSortsBySectionThenUrgency(t *testing.T) {
	items := []model.Item{
		{ID: "b1", Section: model.SectionBranches, Urgency: 1},
		{ID: "n1", Section: model.SectionNeedsYou, Urgency: 1},
		{ID: "n2", Section: model.SectionNeedsYou, Urgency: 3},
		{ID: "a1", Section: model.SectionAdded},
		{ID: "y1", Section: model.SectionYours, UpdatedAt: ago(time.Hour)},
		{ID: "y2", Section: model.SectionYours, UpdatedAt: ago(time.Minute)},
	}
	var got []string
	for _, it := range sortItems(items) {
		got = append(got, it.ID)
	}
	if want := "n2 n1 y2 y1 b1 a1"; strings.Join(got, " ") != want {
		t.Errorf("order = %v, want %s", got, want)
	}
}

func TestUrgencyBar(t *testing.T) {
	tests := []struct {
		q    int
		want string
	}{
		{-1, "▯▯▯▯"}, {0, "▯▯▯▯"}, {1, "▮▯▯▯"}, {2, "▮▮▯▯"}, {3, "▮▮▮▯"}, {4, "▮▮▮▮"}, {9, "▮▮▮▮"},
	}
	for _, tt := range tests {
		if got := urgencyBar(tt.q); got != tt.want {
			t.Errorf("urgencyBar(%d) = %q, want %q", tt.q, got, tt.want)
		}
	}
}

func TestSecondaryLine(t *testing.T) {
	rc := rowCtx{now: fixedNow, spin: "*", stages: map[string]model.Stage{"pr:2": model.StageEmbed}}
	tests := []struct {
		name string
		it   model.Item
		want string
	}{
		{"needs you", model.Item{Section: model.SectionNeedsYou, Kind: model.KindPR, Author: "alice", Additions: 3, Deletions: 1, CI: model.CIRed, Why: "asked you · age"},
			"alice · +3 −1 · CI ✗ · asked you · age"},
		{"needs you minutes", model.Item{Section: model.SectionNeedsYou, Kind: model.KindPR, Author: "alice", Minutes: 39.8, Why: "asked you"},
			"alice · ~40 min · asked you"},
		{"needs you tiny", model.Item{Section: model.SectionNeedsYou, Kind: model.KindPR, Minutes: 0.2}, "~1 min"},
		{"yours", model.Item{Section: model.SectionYours, Kind: model.KindPR, CI: model.CIGreen, Draft: true, UpdatedAt: ago(3 * 24 * time.Hour)},
			"CI ✓ · draft · 3d"},
		{"yours threads", model.Item{Section: model.SectionYours, Kind: model.KindPR, OpenThreads: 2, CI: model.CIGreen},
			"2 open threads · CI ✓"},
		{"involved", model.Item{Section: model.SectionInvolved, Kind: model.KindPR, Author: "carol"}, "carol"},
		{"branch", model.Item{Section: model.SectionBranches, Kind: model.KindBranch, Unpushed: 2, Ahead: 5, UpdatedAt: ago(5 * time.Hour)},
			"2 unpushed · 5 ahead · 5h"},
		{"added pr", model.Item{Section: model.SectionAdded, Kind: model.KindPR, Author: "bob"}, "bob"},
		{"added branch", model.Item{Section: model.SectionAdded, Kind: model.KindBranch, Ahead: 1, UpdatedAt: ago(10 * time.Minute)}, "1 ahead · 10m"},
		{"fetching", model.Item{Section: model.SectionNeedsYou, State: model.StateFetching, Author: "a"}, "* fetching · a"},
		{"analysing note", model.Item{Section: model.SectionNeedsYou, State: model.StateAnalysing, StateNote: "parse"}, "* analysing parse"},
		{"analysing from event", model.Item{ID: "pr:2", Section: model.SectionNeedsYou, State: model.StateAnalysing}, "* analysing embed"},
		{"stale", model.Item{Section: model.SectionYours, State: model.StateStale}, "stale · r to refresh"},
		{"error", model.Item{Section: model.SectionBranches, Kind: model.KindBranch, State: model.StateError, StateNote: "boom"}, "error: boom"},
		{"ready", model.Item{Section: model.SectionYours, State: model.StateReady, CI: model.CIPending}, "CI …"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := secondaryLine(tt.it, rc); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAge(t *testing.T) {
	tests := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, ""}, {ago(0), "0m"}, {ago(59 * time.Minute), "59m"}, {ago(2 * time.Hour), "2h"},
		{ago(49 * time.Hour), "2d"}, {fixedNow.Add(time.Hour), "0m"},
	}
	for _, tt := range tests {
		if got := age(tt.t, fixedNow); got != tt.want {
			t.Errorf("age(%v) = %q, want %q", tt.t, got, tt.want)
		}
	}
}
