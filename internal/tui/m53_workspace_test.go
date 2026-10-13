package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/cmdline"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeview"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/settingsview"
)

// ---- fixtures -------------------------------------------------------------------

// wsItems are two repos' items: both have a PR #7; each has a superseded
// feat/session branch, but only portal has a PR on that ref; billing-svc
// has a not-analysed branch.
func wsItems() []model.Item {
	return []model.Item{
		{ID: "portal::pr:7", Repo: "portal", Kind: model.KindPR, Number: 7, Title: "Add JWT session", Section: model.SectionNeedsYou,
			Author: "alice", CI: model.CIRed, Why: "asked you · CI red", Urgency: 9, UrgencyQuartile: 4, Breaking: 1, NeedsEyes: 3,
			Minutes: 12, State: model.StateReady, UpdatedAt: ago(26 * time.Hour), URL: "https://github.com/acme/portal/pull/7", HeadSHA: "p7"},
		{ID: "portal::pr:12", Repo: "portal", Kind: model.KindPR, Number: 12, Title: "Session rework", Ref: "feat/session",
			Section: model.SectionYours, Urgency: 2, UrgencyQuartile: 2, State: model.StateReady, UpdatedAt: ago(5 * time.Hour), HeadSHA: "p12"},
		{ID: "portal::branch:feat/session", Repo: "portal", Kind: model.KindBranch, Ref: "feat/session", Section: model.SectionBranches,
			BranchState: model.BranchSuperseded, BranchStateP: 0.77, Urgency: 1, UrgencyQuartile: 1, Unpushed: 2, State: model.StateReady,
			LastCommitAt: ago(6 * 24 * time.Hour), HeadSHA: "pb"},
		{ID: "billing-svc::pr:7", Repo: "billing-svc", Kind: model.KindPR, Number: 7, Title: "Retry invoice sync",
			Section: model.SectionNeedsYou, Author: "bob", Why: "asked you · age", Urgency: 5, UrgencyQuartile: 3, NeedsEyes: 2,
			Minutes: 8, State: model.StateReady, UpdatedAt: ago(3 * time.Hour), HeadSHA: "b7"},
		{ID: "billing-svc::branch:feat/session", Repo: "billing-svc", Kind: model.KindBranch, Ref: "feat/session",
			Section: model.SectionBranches, BranchState: model.BranchSuperseded, BranchStateP: 0.66, Urgency: 0.5, UrgencyQuartile: 1,
			State: model.StateReady, LastCommitAt: ago(2 * 24 * time.Hour), HeadSHA: "bb"},
		{ID: "billing-svc::branch:wip/x", Repo: "billing-svc", Kind: model.KindBranch, Ref: "wip/x", Section: model.SectionBranches,
			Urgency: 0.2, State: model.StateNotAnalysed, Ahead: 1, LastCommitAt: ago(4 * time.Hour), HeadSHA: "bw"},
	}
}

// wsInfo is a three-repo workspace: billing-svc (GitHub disabled), portal
// (on GitHub) and tools (no remote, no items).
func wsInfo() proto.WorkspaceInfo {
	return proto.WorkspaceInfo{Multi: true, Root: "/home/u/work", SpentUSD: 0.124, Concurrency: 2, Repos: []proto.RepoInfo{
		{Key: "billing-svc", Label: "acme/billing-svc", Root: "/home/u/work/billing-svc", GHRepo: "acme/billing-svc",
			Status: proto.RepoGitHubDisabled, Note: "GitHub disabled (no token); local branches only", Role: "backend", SpentUSD: 0.004},
		{Key: "portal", Label: "acme/portal", Root: "/home/u/work/portal", GHRepo: "acme/portal", Status: proto.RepoGitHub,
			Role: "frontend", SpentUSD: 0.12},
		{Key: "tools", Label: "tools", Root: "/home/u/work/tools", Status: proto.RepoNoRemote, Note: "no origin remote"},
	}}
}

// wsFake is a fake daemon serving the workspace.
func wsFake() *fakeClient {
	f := newFake(wsItems()...)
	f.workspace = wsInfo()
	return f
}

// wsModelWith returns a w × h model on f after the Workspace answer, with
// the selected item's detail and repo settings loaded; $HOME is /home/u.
func wsModelWith(t *testing.T, f *fakeClient, w, h int, o Options) Model {
	t.Helper()
	t.Setenv("HOME", "/home/u")
	m := newTestModel(t, f, o)
	m = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	msgs := run(m.loadWorkspace())
	if len(msgs) != 1 {
		t.Fatalf("workspace load: %v", msgs)
	}
	m, cmd := updateCmd(t, m, msgs[0])
	m = settle(t, m, cmd)
	// newTestModel dropped the first selection's load.
	return settle(t, m, m.selectionChanged())
}

func wsModel(t *testing.T, w, h int) (Model, *fakeClient) {
	t.Helper()
	f := wsFake()
	return wsModelWith(t, f, w, h, Options{}), f
}

// pickerText is the visible picker lines (colour-free).
func pickerText(m Model) string {
	return ansi.Strip(m.picker.view(m.st, m.rowCtx()))
}

// ---- grouping and goldens ---------------------------------------------------------

func TestWorkspacePickerGolden(t *testing.T) {
	tests := []struct {
		name string
		w, h int
		keys []string
	}{
		{"picker_workspace_80", 80, 24, nil},
		{"picker_workspace_160", 160, 30, nil},
		{"picker_workspace_folded", 100, 24, []string{"z"}},
		{"picker_workspace_filtered", 100, 24, []string{"F"}},
		{"picker_workspace_billing", 100, 24, []string{"j", "j", "j"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := wsModel(t, tt.w, tt.h)
			for _, k := range tt.keys {
				m = pressRun(t, m, k)
			}
			m.note = ""
			got := m.View()
			if strings.Contains(got, "\x1b[") {
				t.Fatal("escape codes in a plain view")
			}
			lines := strings.Split(got, "\n")
			if len(lines) != tt.h {
				t.Errorf("%d lines, want %d", len(lines), tt.h)
			}
			for _, l := range lines {
				if n := ansi.StringWidth(l); n > tt.w {
					t.Errorf("line wider than %d: %q", tt.w, l)
				}
			}
			golden(t, tt.name, got)
		})
	}
}

func TestWorkspaceGroupsAndHeaders(t *testing.T) {
	m, _ := wsModel(t, 120, 40)
	text := pickerText(m)
	var heads []string
	for _, l := range m.picker.lines(m.st, m.rowCtx()) {
		if l.header {
			heads = append(heads, strings.Join(strings.Fields(ansi.Strip(l.text)), " "))
		}
	}
	want := []string{"▾ acme/portal 3 gh ✓", "▾ acme/billing-svc 3 gh off", "▸ tools 0 no remote"}
	if fmt.Sprint(heads) != fmt.Sprint(want) {
		t.Errorf("headers %q, want %q (most urgent repo first, the empty one last and folded)\n%s", heads, want, text)
	}
	// Inside a repo only the sections with items get a divider.
	if strings.Contains(text, "(0)") {
		t.Errorf("a repo shows an empty section:\n%s", text)
	}
	if n := strings.Count(text, "#7 "); n != 2 {
		t.Errorf("both repos' #7 should be listed, got %d:\n%s", n, text)
	}
	// supersededBy only matches PRs of the same repo.
	if !strings.Contains(text, "superseded by #12") || strings.Count(text, "superseded by") != 1 {
		t.Errorf("superseded lines:\n%s", text)
	}
	if got := selectedID(m); got != "portal::pr:7" {
		t.Errorf("the cursor starts on %s, want the top item", got)
	}
	if !strings.Contains(text, "why: asked you · CI red") {
		t.Errorf("no why-line on the selected item:\n%s", text)
	}
}

func TestRepoStatusWords(t *testing.T) {
	for s, want := range map[proto.RepoStatus]string{
		proto.RepoGitHub: "gh ✓", proto.RepoGitHubDisabled: "gh off", proto.RepoNotGitHub: "not on GitHub",
		proto.RepoNoRemote: "no remote", proto.RepoError: "error", "": "",
	} {
		if got := repoStatusWords(s); got != want {
			t.Errorf("%q: %q, want %q", s, got, want)
		}
	}
}

// The urgency bar shows Item.UrgencyQuartile as given (the workspace client
// ranks across repos).
func TestWorkspaceUrgencyBarAsGiven(t *testing.T) {
	m, _ := wsModel(t, 120, 40)
	text := pickerText(m)
	for _, want := range []string{"▮▮▮▮ #7 Add JWT session", "▮▮▮▯ #7 Retry invoice sync", "▮▮▯▯ #12 Session rework"} {
		if !strings.Contains(text, want) {
			t.Errorf("picker lacks %q:\n%s", want, text)
		}
	}
}

// ---- keys -------------------------------------------------------------------------

func TestWorkspaceNavigationStops(t *testing.T) {
	m, _ := wsModel(t, 120, 40)
	steps := []struct{ key, want string }{
		{"k", "portal::pr:7"},
		{"j", "portal::pr:12"},
		{"j", "portal::branch:feat/session"},
		{"j", "billing-svc::pr:7"},
		{"j", "billing-svc::branch:feat/session"},
		{"j", "billing-svc::branch:wip/x"},
		{"j", "header:tools"}, // the folded empty repo is a stop
		{"j", "header:tools"}, // clamps
		{"g", "portal::pr:7"},
		{"G", "header:tools"},
	}
	for i, s := range steps {
		m = update(t, m, keyMsg(s.key))
		got := selectedID(m)
		if m.picker.header != "" {
			got = "header:" + m.picker.header
		}
		if got != s.want {
			t.Fatalf("step %d (%s): %s, want %s", i, s.key, got, s.want)
		}
	}
}

func TestFoldKeys(t *testing.T) {
	m, f := wsModel(t, 120, 40)
	m = press(t, m, "j") // portal::pr:12
	before := len(f.detailParams)
	m, cmd := updateCmd(t, m, keyMsg("z"))
	if m.picker.header != "portal" {
		t.Fatalf("z: cursor header %q, want portal", m.picker.header)
	}
	if _, ok := m.picker.selected(); ok {
		t.Error("selected() is true on a repo header")
	}
	m = settle(t, m, cmd)
	if len(f.detailParams) != before || m.detail != nil {
		t.Errorf("a header loaded a Detail: %v", f.detailParams[before:])
	}
	text := pickerText(m)
	if !strings.Contains(text, "▸ [acme/portal 3]") || strings.Contains(text, "Session rework") {
		t.Errorf("folded portal:\n%s", text)
	}
	// The repo card.
	v := m.View()
	for _, want := range []string{"acme/portal", "~/work/portal", "gh ✓", "2 PRs · 1 branch — Needs you 1 · Yours 1 · Branches 1 · Added 0",
		"$0.12 this session", "enter or z unfolds"} {
		if !strings.Contains(v, want) {
			t.Errorf("repo card lacks %q:\n%s", want, v)
		}
	}
	// enter on the folded header unfolds it, onto the repo's first item.
	m, cmd = updateCmd(t, m, keyMsg("enter"))
	if got := selectedID(m); got != "portal::pr:7" || m.focus != FocusPicker {
		t.Fatalf("enter on the header: selected %s focus %v", got, m.focus)
	}
	m = settle(t, m, cmd)
	if m.detail == nil || m.detail.Item.ID != "portal::pr:7" {
		t.Error("unfolding did not load the item")
	}
	// Z folds every repo (cursor on its repo's header), Z again unfolds.
	m = press(t, m, "Z")
	if m.picker.header != "portal" || len(m.picker.stops()) != 3 {
		t.Errorf("Z: header %q stops %v", m.picker.header, m.picker.stops())
	}
	m = press(t, m, "Z")
	if got := selectedID(m); got != "portal::pr:7" || len(m.picker.stops()) != 7 {
		t.Errorf("Z again: selected %s stops %d", got, len(m.picker.stops()))
	}
	// z on the empty repo's header unfolds it; it stays a header stop.
	m = press(t, m, "G", "z")
	if m.picker.header != "tools" || !strings.Contains(pickerText(m), "▾ [tools 0]") {
		t.Errorf("unfolded empty repo:\n%s", pickerText(m))
	}
	if v := m.View(); !strings.Contains(v, "no items in this repo") {
		t.Errorf("empty repo card:\n%s", v)
	}
}

func TestHeaderClickToggles(t *testing.T) {
	m, _ := wsModel(t, 120, 40)
	// Row 2 is the list's first line: portal's header.
	m = update(t, m, mouseClick(3, 2))
	if !strings.HasPrefix(pickerText(m), "▸ [acme/portal 3]") || m.picker.header != "portal" {
		t.Fatalf("click on the header did not fold it:\n%s", pickerText(m))
	}
	m = update(t, m, mouseClick(3, 2))
	if !strings.HasPrefix(pickerText(m), "▾ acme/portal 3") || selectedID(m) != "portal::pr:7" {
		t.Errorf("second click did not unfold it:\n%s", pickerText(m))
	}
	// A click on billing-svc's header folds it while the cursor stays.
	text := strings.Split(pickerText(m), "\n")
	row := -1
	for i, l := range text {
		if strings.HasPrefix(l, "▾ acme/billing-svc") {
			row = i
		}
	}
	m = update(t, m, mouseClick(5, row+2))
	if selectedID(m) != "portal::pr:7" || !strings.Contains(pickerText(m), "▸ acme/billing-svc 3") {
		t.Errorf("folding another repo moved the cursor or did not fold:\n%s", pickerText(m))
	}
}

func TestRepoFilterKey(t *testing.T) {
	m, _ := wsModel(t, 120, 40)
	var got []string
	for range 4 {
		m = press(t, m, "F")
		got = append(got, m.picker.filter+"|"+m.note)
	}
	want := []string{"billing-svc|repo: acme/billing-svc", "portal|repo: acme/portal", "tools|repo: tools", "|repo: all"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("F cycle %q, want %q", got, want)
	}
	m = press(t, m, "F")
	v := m.View()
	text := pickerText(m)
	if strings.Contains(text, "acme/portal") || !strings.Contains(text, "acme/billing-svc") {
		t.Errorf("filtered picker:\n%s", text)
	}
	if !strings.Contains(v, "Queue · ranked · acme/bill") { // cut to the 36-column box
		t.Errorf("box title:\n%s", v)
	}
	if got := m.picker.title(); got != "Queue · ranked · acme/billing-svc" {
		t.Errorf("title %q", got)
	}
	if h := m.viewHeader(); !strings.Contains(h, "repo: acme/billing-svc") || !strings.Contains(h, "role: backend") {
		t.Errorf("header %q", h)
	}
	if got := selectedID(m); got != "billing-svc::pr:7" {
		t.Errorf("filter moved the cursor to %s", got)
	}
}

func TestSingleRepoWorkspaceKeysDoNothing(t *testing.T) {
	m := newTestModel(t, newFake(threeItems()...), Options{})
	before := m.View()
	for _, k := range []string{"z", "Z", "F"} {
		m = press(t, m, k)
		if m.note != "" || m.picker.filter != "" || m.picker.header != "" {
			t.Errorf("%s in a single repo: note %q", k, m.note)
		}
	}
	if m.View() != before {
		t.Error("z/Z/F changed the single-repo screen")
	}
	m, _ = withFakeCmd(m, map[string]cmdline.Submitted{"repo x": {Cmd: cmdline.Command{Kind: cmdline.Repo, Arg: "x"}}})
	m = press(t, m, ":")
	m = typeKeys(m, "repo x")
	m = update(t, m, keyMsg("enter"))
	if m.note != "not a workspace" {
		t.Errorf(":repo outside a workspace: %q", m.note)
	}
}

func TestRepoCommand(t *testing.T) {
	m, _ := wsModel(t, 120, 40)
	next := map[string]cmdline.Submitted{}
	for _, arg := range []string{"billing-svc", "acme/portal", "ACME/Portal", "", "nope", "tools"} {
		next["repo "+arg] = cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Repo, Arg: arg}}
	}
	m, c := withFakeCmd(m, next)
	m = update(t, m, workspaceMsg{info: wsInfo()}) // SetRepos on the fake line
	if fmt.Sprint(c.repos) != "[billing-svc acme/billing-svc portal acme/portal tools]" {
		t.Errorf("SetRepos %v", c.repos)
	}
	runLine := func(m Model, line string) Model {
		m = press(t, m, ":")
		m = typeKeys(m, line)
		m, cmd := updateCmd(t, m, keyMsg("enter"))
		return settle(t, m, cmd)
	}
	for _, tc := range []struct{ line, filter, note string }{
		{"repo billing-svc", "billing-svc", "repo: acme/billing-svc"},
		{"repo acme/portal", "portal", "repo: acme/portal"},
		{"repo ACME/Portal", "portal", "repo: acme/portal"},
		{"repo ", "", "repo: all"},
		{"repo nope", "", `unknown repo "nope" — repos: acme/billing-svc, acme/portal, tools`},
		{"repo tools", "tools", "repo: tools"},
	} {
		m = runLine(m, tc.line)
		if m.picker.filter != tc.filter || m.note != tc.note {
			t.Errorf(":%s → filter %q note %q, want %q %q", tc.line, m.picker.filter, m.note, tc.filter, tc.note)
		}
	}
	// The filter on the empty repo shows its header (the card).
	if m.picker.header != "tools" {
		t.Errorf(":repo tools: cursor %q", m.picker.header)
	}
}

func TestResolveRepoAmbiguousBaseName(t *testing.T) {
	m := Model{ws: proto.WorkspaceInfo{Multi: true, Repos: []proto.RepoInfo{
		{Key: "a/api", Label: "acme/api"}, {Key: "b/api", Label: "beta/api"}, {Key: "web", Label: "acme/web"},
	}}}
	if k, err := m.resolveRepo("web"); err != nil || k != "web" {
		t.Errorf("web → %q %v", k, err)
	}
	if _, err := m.resolveRepo("api"); err == nil || err.Error() != `repo "api" is ambiguous: acme/api, beta/api` {
		t.Errorf("api → %v", err)
	}
	if k, err := m.resolveRepo("beta/api"); err != nil || k != "b/api" {
		t.Errorf("beta/api → %q %v", k, err)
	}
}

// Two clones of one GitHub repo: their shared GitHub name is ambiguous (as
// workspace.Resolve says), their keys and the labels the workspace made
// unique are not.
func TestResolveRepoDuplicateGitHubRepo(t *testing.T) {
	m := Model{ws: proto.WorkspaceInfo{Multi: true, Repos: []proto.RepoInfo{
		{Key: "portal", Label: "acme/portal (portal)", GHRepo: "acme/portal"},
		{Key: "portal-2", Label: "acme/portal (portal-2)", GHRepo: "acme/portal"},
	}}}
	for _, arg := range []string{"acme/portal", "ACME/portal"} {
		want := fmt.Sprintf("repo %q is ambiguous: acme/portal (portal), acme/portal (portal-2)", arg)
		if _, err := m.resolveRepo(arg); err == nil || err.Error() != want {
			t.Errorf("%s → %v, want %q", arg, err, want)
		}
	}
	for arg, key := range map[string]string{"portal": "portal", "portal-2": "portal-2", "acme/portal (portal-2)": "portal-2"} {
		if k, err := m.resolveRepo(arg); err != nil || k != key {
			t.Errorf("%s → %q %v, want %q", arg, k, err, key)
		}
	}
}

func TestWorkspaceKeysRebindableAndInHelp(t *testing.T) {
	km, err := KeyMapFromConfig(map[string]string{"fold_repo": "ctrl+f", "fold_all": "alt+z", "repo_filter": "R",
		"narrower": "alt+left", "wider": "alt+right"})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(km.FoldRepo.Keys(), km.FoldAll.Keys(), km.RepoFilter.Keys(), km.Narrower.Keys(), km.Wider.Keys())
	if got != "[ctrl+f] [alt+z] [R] [alt+left] [alt+right]" {
		t.Errorf("rebound %s", got)
	}
	acts := " " + strings.Join(KeyActions(), " ") + " "
	for _, a := range []string{"fold_repo", "fold_all", "repo_filter", "narrower", "wider"} {
		if !strings.Contains(acts, " "+a+" ") {
			t.Errorf("KeyActions lacks %s", a)
		}
	}
	f := wsFake()
	m := wsModelWith(t, f, 120, 40, Options{Keys: &km})
	m = press(t, m, "z") // the Code page's key, not the picker's any more
	if m.picker.header != "" {
		t.Error("z still folds after rebinding")
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	if m.picker.header != "portal" {
		t.Errorf("ctrl+f did not fold: %q", m.picker.header)
	}
	m = update(t, m, keyMsg("?"))
	v := m.View()
	for _, want := range []string{"ctrl+f", "fold repo", "alt+z", "fold all repos", "filter by repo", "alt+left", "narrower pane"} {
		if !strings.Contains(v, want) {
			t.Errorf("help lacks %q:\n%s", want, v)
		}
	}
}

// In a workspace the picker scope adds z Z F (and every scope < >) without a
// clash; z stays the Code page's expand key there.
func TestNoKeyClashWorkspaceScopes(t *testing.T) {
	m, _ := wsModel(t, 120, 40)
	for _, sc := range []struct {
		focus Focus
		tab   Tab
		keys  []string
	}{
		{FocusPicker, TabOverview, []string{"z", "Z", "F", "<", ">"}},
		{FocusMain, TabOverview, []string{"<", ">"}},
		{FocusMain, TabCode, []string{"<", ">", "z"}},
		{FocusMain, TabPipeline, []string{"<", ">"}},
		{FocusMain, TabArch, []string{"<", ">"}},
	} {
		m.focus, m.tab = sc.focus, sc.tab
		name, groups := m.helpGroups()
		owner := map[string]string{}
		for _, g := range groups {
			for _, b := range g {
				if !b.Enabled() {
					continue
				}
				for _, k := range b.Keys() {
					if prev, ok := owner[k]; ok && prev != b.Help().Desc && !helpExceptions[k] {
						t.Errorf("%s: %q bound to %q and %q", name, k, prev, b.Help().Desc)
					}
					owner[k] = b.Help().Desc
				}
			}
		}
		for _, k := range sc.keys {
			if owner[k] == "" {
				t.Errorf("%s: %q not in help", name, k)
			}
		}
		if sc.tab == TabCode && owner["z"] != "expand noise" {
			t.Errorf("code: z is %q", owner["z"])
		}
	}
}

// In a workspace the command list adds `:repo` and the repo-qualified
// `:add` forms that a single repo's overlay leaves out.
func TestWorkspaceHelpListsRepoCommands(t *testing.T) {
	m, _ := wsModel(t, 200, 120)
	v := press(t, m, "?").View()
	for _, e := range cmdline.Help() {
		if !strings.Contains(v, e.Usage) {
			t.Errorf("workspace help lacks command %q", e.Usage)
		}
	}
}

// ---- header -----------------------------------------------------------------------

func TestWorkspaceHeader(t *testing.T) {
	m, f := wsModel(t, 200, 30)
	h := m.viewHeader()
	if !strings.HasPrefix(h, "prt · ~/work · 3 repos · 3 PRs · 3 branches · role: frontend") {
		t.Errorf("header %q", h)
	}
	if !strings.HasSuffix(h, "$0.12 spent") {
		t.Errorf("spend %q", h)
	}
	m = press(t, m, "j", "j", "j") // billing-svc::pr:7
	if h := m.viewHeader(); !strings.Contains(h, "role: backend") {
		t.Errorf("role of billing-svc %q", h)
	}
	m = press(t, m, "G") // tools' header: no role
	if h := m.viewHeader(); strings.Contains(h, "role:") {
		t.Errorf("tools has no role: %q", h)
	}
	// Below a cent.
	f.workspace.SpentUSD = 0.004
	m = settle(t, m, m.loadWorkspace())
	if h := m.viewHeader(); !strings.HasSuffix(h, "<$0.01 spent") {
		t.Errorf("tiny spend %q", h)
	}
	// A live role change of a repo (its settings) wins over the workspace's.
	f.repoSettings = map[string]proto.SettingsInfo{"portal": {Values: proto.SettingsValues{Role: "platform"}}}
	m = press(t, m, "g")
	m, cmd := updateCmd(t, m, eventMsg(proto.Event{Type: proto.EventSettings, Repo: "portal"}))
	m = settle(t, m, cmd)
	if h := m.viewHeader(); !strings.Contains(h, "role: platform") {
		t.Errorf("role after EventSettings %q", h)
	}
}

func TestWorkspaceNotesNameTheRepo(t *testing.T) {
	m, _ := wsModel(t, 120, 40)
	m = press(t, m, "j", "j", "j") // billing-svc::pr:7
	m = press(t, m, "r")
	if m.note != "refreshing acme/billing-svc #7 Retry invoice sync" {
		t.Errorf("refresh note %q", m.note)
	}
	m = press(t, m, "ctrl+x")
	if m.note != "stopping acme/billing-svc #7…" {
		t.Errorf("stop note %q", m.note)
	}
}

// ---- per-repo context ---------------------------------------------------------------

func TestWorkspaceSettingsCarryRepo(t *testing.T) {
	f := wsFake()
	f.ctl.settings = settingsInfo()
	m := wsModelWith(t, f, 120, 40, Options{})
	m, s := withFakeSettings(m)
	m = press(t, m, "j", "j", "j") // billing-svc::pr:7
	m = pressRun(t, m, ",")
	last := f.ctl.settingsReqs[len(f.ctl.settingsReqs)-1]
	if last != (proto.SettingsParams{Repo: "billing-svc"}) {
		t.Fatalf("settings read %+v", last)
	}
	s.acts["enter"] = settingsview.Action{Kind: settingsview.ActSave, Scope: proto.ScopeRepo, Values: proto.SettingsValues{AutoReview: "all"}}
	m, cmd := updateCmd(t, m, keyMsg("enter"))
	m = settle(t, m, cmd)
	if len(f.ctl.saves) != 1 || f.ctl.saves[0].Repo != "billing-svc" {
		t.Errorf("saves %+v", f.ctl.saves)
	}
	if got := m.strategyFor("billing-svc"); got != "all" {
		t.Errorf("billing-svc strategy after the save %q", got)
	}
	s.acts["e"] = settingsview.Action{Kind: settingsview.ActEditRouting}
	f.routing = proto.RoutingInfo{}
	edit := func(_ string, done func(error) tea.Msg) tea.Cmd { return func() tea.Msg { return done(nil) } }
	m.opts.Edit = edit
	m, cmd = updateCmd(t, m, keyMsg("e"))
	m = settle(t, m, cmd)
	if n := len(f.routingCalls); n == 0 || f.routingCalls[n-1] != (proto.RoutingParams{Reload: true, Repo: "billing-svc"}) {
		t.Errorf("routing calls %+v", f.routingCalls)
	}
	if last := f.ctl.settingsReqs[len(f.ctl.settingsReqs)-1]; last.Repo != "billing-svc" {
		t.Errorf("ensure read %+v", last)
	}
}

func TestWorkspaceRoutingCarriesRepo(t *testing.T) {
	f := wsFake()
	f.routing = proto.RoutingInfo{Path: "/home/u/work/portal/.git/prt/prt.routing.toml"}
	edit := func(_ string, done func(error) tea.Msg) tea.Cmd { return func() tea.Msg { return done(nil) } }
	m := wsModelWith(t, f, 120, 40, Options{Edit: edit})
	m, p := withFakePipe(m)
	m = press(t, m, "P")
	p.actions["e"] = pipeview.Action{Kind: pipeview.ActEditRules}
	m, cmd := updateCmd(t, m, keyMsg("e"))
	settle(t, m, cmd)
	want := []proto.RoutingParams{{Ensure: true, Repo: "portal"}, {Reload: true, Repo: "portal"}}
	if fmt.Sprint(f.routingCalls) != fmt.Sprint(want) {
		t.Errorf("routing calls %+v, want %+v", f.routingCalls, want)
	}
}

func TestWorkspaceAutoCommand(t *testing.T) {
	f := wsFake()
	f.ctl.settings = settingsInfo()
	m := wsModelWith(t, f, 120, 40, Options{})
	m = press(t, m, "j", "j", "j") // billing-svc::pr:7
	m, cmd := m.runCommand(cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Auto, Strategy: model.AutoManual}})
	m = settle(t, m, cmd)
	if len(f.ctl.saves) != 1 || f.ctl.saves[0].Repo != "billing-svc" || f.ctl.saves[0].Scope != proto.ScopeRepo {
		t.Fatalf("saves %+v", f.ctl.saves)
	}
	if last := f.ctl.settingsReqs[len(f.ctl.settingsReqs)-1]; last.Repo != "billing-svc" {
		t.Errorf("read %+v", last)
	}
	if m.note != "auto-review: manual (saved to acme/billing-svc)" {
		t.Errorf("note %q", m.note)
	}
	if m.strategyFor("billing-svc") != "manual" {
		t.Errorf("cached strategy %q", m.strategyFor("billing-svc"))
	}
}

func TestWorkspaceStrategyPerRepo(t *testing.T) {
	f := wsFake()
	f.repoSettings = map[string]proto.SettingsInfo{
		"portal":      {Values: proto.SettingsValues{AutoReview: "all"}},
		"billing-svc": {Values: proto.SettingsValues{AutoReview: "manual"}},
	}
	f.ctl.settings.Values.AutoReview = "needs-you" // the first repo's, from Settings{}
	m := wsModelWith(t, f, 160, 60, Options{})
	m.picker.moveTo(m.picker.indexOf("billing-svc::branch:wip/x"))
	m = settle(t, m, m.selectionChanged())
	v := m.View()
	if !strings.Contains(v, "auto-review: manual — runs nothing until you ask") {
		t.Errorf("billing-svc strategy line missing:\n%s", v)
	}
	var repos []string
	for _, p := range f.ctl.settingsReqs {
		repos = append(repos, p.Repo)
	}
	if !strings.Contains(strings.Join(repos, ","), "billing-svc") {
		t.Errorf("settings reads %q", repos)
	}
}

func TestWorkspaceSearchAndAdd(t *testing.T) {
	m, f := wsModel(t, 120, 40)
	m, s := withFakeSearch(m)
	m = press(t, m, "F") // billing-svc
	m = press(t, m, "/")
	m = typeKeys(m, "7")
	if len(f.searches) == 0 || f.searches[0].Repo != "billing-svc" {
		t.Fatalf("searches %+v", f.searches)
	}
	s.Close()
	m = press(t, m, "esc") // closes nothing; back to the picker
	next := map[string]cmdline.Submitted{}
	for _, q := range []string{"482", "#482", "feat/x", "acme/portal#3", "portal:feat/y"} {
		next["add "+q] = cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Add, Arg: q}}
	}
	m, _ = withFakeCmd(m, next)
	for _, q := range []string{"482", "#482", "feat/x", "acme/portal#3", "portal:feat/y"} {
		m = press(t, m, ":")
		m = typeKeys(m, "add "+q)
		var cmd tea.Cmd
		m, cmd = updateCmd(t, m, keyMsg("enter"))
		m = settle(t, m, cmd)
	}
	want := "[billing-svc#482 billing-svc#482 billing-svc:feat/x acme/portal#3 portal:feat/y]"
	if fmt.Sprint(f.adds) != want {
		t.Errorf("adds %v, want %s", f.adds, want)
	}
	// Without a filter an unqualified :add is kept as typed.
	m = press(t, m, "F", "F", "F") // portal, tools, all
	m = press(t, m, ":")
	m = typeKeys(m, "add 482")
	m, cmd := updateCmd(t, m, keyMsg("enter"))
	settle(t, m, cmd)
	if f.adds[len(f.adds)-1] != "482" {
		t.Errorf("unfiltered add %q", f.adds[len(f.adds)-1])
	}
}

func TestWorkspaceEditOpensUnderRepoRoot(t *testing.T) {
	var edits []string
	o := Options{Root: "/elsewhere", EditFile: func(path string, _ int) tea.Cmd { edits = append(edits, path); return nil }}
	m := wsModelWith(t, wsFake(), 120, 40, o)
	m, p := withFakePane(m)
	m = press(t, m, "j", "j", "j") // billing-svc::pr:7
	m = feed[detailMsg](t, m, m.selectionChanged())
	p.unit, p.unitOK = model.ChangeUnit{ID: "u", Path: "web/a.ts"}, true
	m = press(t, m, "2", "enter")
	_ = pressRun(t, m, "e")
	if fmt.Sprint(edits) != fmt.Sprint([]string{filepath.Join("/home/u/work/billing-svc", "web/a.ts")}) {
		t.Errorf("e opened %v", edits)
	}
}

func TestWorkspaceLogNotes(t *testing.T) {
	m, f := wsModel(t, 200, 30)
	calls := f.workspaceCalls
	m, cmd := updateCmd(t, m, eventMsg(proto.Event{Type: proto.EventLog, Repo: "billing-svc", Note: "GitHub disabled (no token); local branches only"}))
	if m.logNote != "" {
		t.Errorf("a status note was copied: %q", m.logNote)
	}
	m = settle(t, m, cmd)
	if f.workspaceCalls <= calls {
		t.Error("an EventLog with a repo did not reload the workspace")
	}
	for _, tc := range []struct{ repo, note, want string }{
		{"billing-svc", "offline: GitHub disabled; local branches only", ""},
		{"tools", "no origin remote", ""},
		{"portal", "GitHub unavailable: rate limited", "acme/portal: GitHub unavailable: rate limited"},
		{"billing-svc", "no default branch; local branches skipped", "acme/billing-svc: no default branch; local branches skipped"},
	} {
		m.logNote = ""
		m = update(t, m, eventMsg(proto.Event{Type: proto.EventLog, Repo: tc.repo, Note: tc.note}))
		if m.logNote != tc.want {
			t.Errorf("%s %q → %q, want %q", tc.repo, tc.note, m.logNote, tc.want)
		}
	}
	// A note without a repo is copied as before.
	m = update(t, m, eventMsg(proto.Event{Type: proto.EventLog, Note: "Jev: replay"}))
	if m.logNote != "Jev: replay" || !strings.HasSuffix(m.viewHeader(), "Jev: replay") {
		t.Errorf("plain note %q", m.logNote)
	}
}

func TestWorkspaceReloads(t *testing.T) {
	m, f := wsModel(t, 120, 40)
	calls := f.workspaceCalls
	m, cmd := updateCmd(t, m, eventMsg(proto.Event{Type: proto.EventSettings}))
	m = settle(t, m, cmd)
	if f.workspaceCalls != calls+1 {
		t.Errorf("EventSettings: workspace calls %d → %d", calls, f.workspaceCalls)
	}
	_, cmd = updateCmd(t, m, refreshTickMsg{})
	for _, msg := range run(cmd) {
		_ = msg
	}
	if f.workspaceCalls != calls+2 {
		t.Errorf("refresh tick: workspace calls %d", f.workspaceCalls)
	}
	// Init asks too.
	n := f.workspaceCalls
	run(NewWithOptions(f, plainRenderer(), Options{}).Init())
	if f.workspaceCalls != n+1 {
		t.Errorf("Init: workspace calls %d → %d", n, f.workspaceCalls)
	}
	// A failed reload keeps the workspace.
	f.workspaceErr = errors.New("gone")
	m = settle(t, m, m.loadWorkspace())
	if !m.ws.Multi || !m.picker.multi {
		t.Error("a failed reload dropped the workspace")
	}
}

// Discovery notes and repos that could not be opened reach the user: the
// status line names what an answer brings that the last did not (repeats
// stay quiet), an unopened repo is a header with the error status, and the
// repo card lists the workspace's notes.
func TestWorkspaceNotesShown(t *testing.T) {
	f := wsFake()
	f.workspace.Notes = []string{"symlink loop at links/loop (back to the root)"}
	f.workspace.Repos = append(f.workspace.Repos, proto.RepoInfo{Key: "broken", Label: "broken", Root: "/home/u/work/broken",
		Status: proto.RepoError, Note: "config: prt.toml: toml: line 1: expected ']'"})
	t.Setenv("HOME", "/home/u")
	m := newTestModel(t, f, Options{})
	m = update(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m, cmd := updateCmd(t, m, run(m.loadWorkspace())[0])
	want := "workspace: broken: config: prt.toml: toml: line 1: expected ']' · symlink loop at links/loop (back to the root)"
	if m.note != want {
		t.Errorf("note %q, want %q", m.note, want)
	}
	m = settle(t, m, cmd)
	m.note = ""
	m = settle(t, m, m.loadWorkspace())
	if m.note != "" {
		t.Errorf("a repeated answer noted again: %q", m.note)
	}
	f.workspace.Notes = append(f.workspace.Notes, "bad exclude glob \"[x\" ignored")
	m = settle(t, m, m.loadWorkspace())
	if m.note != `workspace: bad exclude glob "[x" ignored` {
		t.Errorf("new note %q", m.note)
	}
	if !strings.Contains(pickerText(m), "broken 0") || !strings.Contains(pickerText(m), "error") {
		t.Errorf("the unopened repo is not listed:\n%s", pickerText(m))
	}
	card := ansi.Strip(m.viewRepoCard("broken", 80))
	for _, s := range []string{"error — config: prt.toml", "workspace notes", "· symlink loop at links/loop", `· bad exclude glob "[x" ignored`} {
		if !strings.Contains(card, s) {
			t.Errorf("repo card lacks %q:\n%s", s, card)
		}
	}
}

// A single-repo daemon (Multi false) changes nothing.
func TestSingleRepoWorkspaceAnswer(t *testing.T) {
	f := newFake(threeItems()...)
	f.workspace = proto.WorkspaceInfo{Root: "/repo", Repos: []proto.RepoInfo{{Label: "o/r", Root: "/repo"}}}
	m := newTestModel(t, f, Options{Repo: "o/r"})
	before := m.View()
	m = settle(t, m, m.loadWorkspace())
	if m.picker.multi || m.View() != before {
		t.Errorf("a single-repo answer changed the screen:\n%s", m.View())
	}
}

// ---- snapshot ---------------------------------------------------------------------

func TestWorkspaceSnapshot(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	f := wsFake()
	opts := Options{Now: func() time.Time { return fixedNow }}
	var a, b bytes.Buffer
	if err := SnapshotWithOptions(context.Background(), f, &a, 120, 30, 1, opts); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotWithOptions(context.Background(), f, &b, 120, 30, 1, opts); err != nil {
		t.Fatal(err)
	}
	s := a.String()
	if s != b.String() {
		t.Error("workspace snapshot is not byte-stable")
	}
	if strings.Contains(s, "\x1b[") {
		t.Error("escape codes in the snapshot")
	}
	for _, want := range []string{
		"=== screen: picker + overview · portal::pr:7 ===", "=== screen: picker + overview · billing-svc::pr:7 ===",
		"=== screen: workspace · folded ===", "=== screen: workspace · filter portal ===", "=== screen: help · picker ===",
		"prt · ~/work · 3 repos · 3 PRs · 3 branches", "▸ [acme/portal 3]", "Queue · ranked · acme/portal", "fold repo",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("snapshot lacks %q", want)
		}
	}
	// maxItems is per repo.
	if strings.Contains(s, "portal::pr:12 ===") || strings.Contains(s, "billing-svc::branch") {
		t.Error("more than one item per repo")
	}
	if strings.Index(s, "overview · portal::pr:7") > strings.Index(s, "overview · billing-svc::pr:7") {
		t.Error("repos not in picker order")
	}
}

// ---- helpers ----------------------------------------------------------------------

func TestQualifyAddAndTildePath(t *testing.T) {
	m := Model{ws: proto.WorkspaceInfo{Multi: true}}
	m.picker.multi, m.picker.filter = true, "api"
	for q, want := range map[string]string{
		"482": "api#482", "#482": "api#482", " 7 ": "api#7", "feat/x": "api:feat/x", "acme/web#3": "acme/web#3",
		"web:feat/y": "web:feat/y", "": "",
	} {
		if got := m.qualifyAdd(q); got != want {
			t.Errorf("qualifyAdd(%q) = %q, want %q", q, got, want)
		}
	}
	for _, tc := range []struct{ p, home, want string }{
		{"/home/u/work", "/home/u", "~/work"}, {"/home/u", "/home/u", "~"}, {"/home/uv/x", "/home/u", "/home/uv/x"},
		{"/srv/w", "", "/srv/w"}, {"", "/home/u", "–"},
	} {
		if got := tildePath(tc.p, tc.home); got != tc.want {
			t.Errorf("tildePath(%q, %q) = %q, want %q", tc.p, tc.home, got, tc.want)
		}
	}
	for usd, want := range map[float64]string{0.004: "<$0.01 spent", 0.01: "$0.01 spent", 1.234: "$1.23 spent"} {
		if got := spentText(usd); got != want {
			t.Errorf("spentText(%v) = %q", usd, got)
		}
	}
}

// keyBinding is a compile-time check that the new bindings are key.Binding.
var _ = []key.Binding{DefaultKeyMap().FoldRepo, DefaultKeyMap().Narrower}
