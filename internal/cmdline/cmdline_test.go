package cmdline

import (
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
)

func TestParse(t *testing.T) {
	cases := []struct {
		line    string
		want    Command
		wantErr string
	}{
		{line: "add #482", want: Command{Kind: Add, Arg: "#482"}},
		{line: ":add feat/x", want: Command{Kind: Add, Arg: "feat/x"}},
		{line: "  :add   migrate session  ", want: Command{Kind: Add, Arg: "migrate session"}},
		{line: "add", wantErr: ":add needs a PR or ref"},
		{line: "add   ", wantErr: ":add needs a PR or ref"},
		{line: "tier deep", want: Command{Kind: Tier, Tier: "deep"}},
		{line: "tier none", want: Command{Kind: Tier, Tier: "none"}},
		{line: ":tier RULES", want: Command{Kind: Tier, Tier: "rules"}},
		{line: "tier fast", wantErr: "fast is chosen by the rules; use :tier rules"},
		{line: "tier", wantErr: ":tier takes deep, none or rules"},
		{line: "tier turbo", wantErr: ":tier takes deep, none or rules"},
		{line: "cap 1.00", want: Command{Kind: Cap, CapUSD: 1}},
		{line: "cap $0.5", want: Command{Kind: Cap, CapUSD: 0.5}},
		{line: "cap default", want: Command{Kind: Cap, Arg: "default"}},
		{line: "cap 0", wantErr: ":cap takes an amount"},
		{line: "cap -1", wantErr: ":cap takes an amount"},
		{line: "cap lots", wantErr: ":cap takes an amount"},
		{line: "cap", wantErr: ":cap takes an amount"},
		{line: "cap NaN", wantErr: ":cap takes an amount"},
		{line: "q", want: Command{Kind: Quit}},
		{line: ":quit", want: Command{Kind: Quit}},
		{line: "q!", want: Command{Kind: Quit}},
		{line: "q now", wantErr: ":q takes no argument"},
		{line: "foo", wantErr: "unknown command :foo — try :add, :auto, :cap, :repo, :resume, :settings, :stop, :tier, :q"},
		{line: ":", wantErr: "empty command — try :add, :auto, :cap, :repo, :resume, :settings, :stop, :tier, :q"},
		{line: "", wantErr: "empty command"},
		// M5.1
		{line: "auto manual", want: Command{Kind: Auto, Strategy: model.AutoManual}},
		{line: "auto needs-you", want: Command{Kind: Auto, Strategy: model.AutoNeedsYou}},
		{line: ":auto Needs-You+Yours", want: Command{Kind: Auto, Strategy: model.AutoNeedsYouYours}},
		{line: "auto ALL", want: Command{Kind: Auto, Strategy: model.AutoAll}},
		{line: "auto", wantErr: ":auto takes manual, needs-you, needs-you+yours or all"},
		{line: "auto some", wantErr: ":auto takes manual, needs-you, needs-you+yours or all"},
		{line: "stop", want: Command{Kind: Stop}},
		{line: "stop all", want: Command{Kind: Stop, All: true}},
		{line: ":stop ALL", want: Command{Kind: Stop, All: true}},
		{line: "stop now", wantErr: ":stop takes no argument or all"},
		{line: "resume", want: Command{Kind: Resume}},
		{line: "resume all", want: Command{Kind: Resume, All: true}},
		{line: "resume #4", wantErr: ":resume takes no argument or all"},
		{line: "settings", want: Command{Kind: Settings}},
		{line: "settings repo", wantErr: ":settings takes no argument"},
		// M5.3
		{line: "repo billing-svc", want: Command{Kind: Repo, Arg: "billing-svc"}},
		{line: ":repo acme/billing-svc", want: Command{Kind: Repo, Arg: "acme/billing-svc"}},
		{line: "  :repo   services/api  ", want: Command{Kind: Repo, Arg: "services/api"}},
		{line: "repo Billing-Svc", want: Command{Kind: Repo, Arg: "Billing-Svc"}}, // as typed; the root resolves it
		{line: "repo all", want: Command{Kind: Repo}},
		{line: "repo ALL", want: Command{Kind: Repo}},
		{line: ":repo All", want: Command{Kind: Repo}},
		{line: "repo", want: Command{Kind: Repo}},
		{line: "repo allx", want: Command{Kind: Repo, Arg: "allx"}},
		{line: "add acme/billing-svc#12", want: Command{Kind: Add, Arg: "acme/billing-svc#12"}},
		{line: ":add api:feat/x", want: Command{Kind: Add, Arg: "api:feat/x"}},
		{line: "add billing-svc#12", want: Command{Kind: Add, Arg: "billing-svc#12"}},
		{line: "add", wantErr: "qualified by repo: :add acme/api#12 or :add api:feat/x"},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			got, err := Parse(tc.line)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Parse(%q) = %+v, %v; want error %q", tc.line, got, err, tc.wantErr)
				}
				if got != (Command{}) {
					t.Errorf("command with error = %+v", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("Parse(%q) = %+v, %v; want %+v", tc.line, got, err, tc.want)
			}
		})
	}
}

func plain() *lipgloss.Renderer { return lipgloss.NewRenderer(io.Discard) }

func typeLine(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func press(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func TestModelSubmit(t *testing.T) {
	m := New(plain())
	if m.Active() || m.View() != "" {
		t.Fatal("new model is open")
	}
	m.Open()
	typeLine(&m, "tier deep")
	if got := m.View(); got != ":tier deep█" {
		t.Errorf("view = %q", got)
	}
	m.Update(press(tea.KeyEnter))
	if m.Active() {
		t.Error("enter did not close")
	}
	s, ok := m.PendingCommand()
	if !ok || s.Line != "tier deep" || s.Cmd != (Command{Kind: Tier, Tier: "deep"}) || s.Err != nil {
		t.Fatalf("submitted = %+v, %v", s, ok)
	}
	if _, ok := m.PendingCommand(); ok {
		t.Error("PendingCommand not cleared")
	}

	m.Open()
	typeLine(&m, "nope")
	m.Update(press(tea.KeyEnter))
	s, ok = m.PendingCommand()
	if !ok || s.Err == nil || s.Cmd != (Command{}) || s.Line != "nope" {
		t.Errorf("bad line submitted = %+v, %v", s, ok)
	}

	// An empty line closes without submitting.
	m.Open()
	m.Update(press(tea.KeyEnter))
	if _, ok := m.PendingCommand(); ok || m.Active() {
		t.Error("empty line submitted or left open")
	}
}

func TestModelEsc(t *testing.T) {
	m := New(plain())
	m.Open()
	typeLine(&m, "q")
	m.Update(press(tea.KeyEsc))
	if m.Active() {
		t.Error("esc did not close")
	}
	if _, ok := m.PendingCommand(); ok {
		t.Error("esc submitted")
	}
	// Reopening starts empty.
	m.Open()
	if got := m.View(); got != ":█" {
		t.Errorf("reopened view = %q", got)
	}
	// Keys while closed are ignored.
	m.Close()
	typeLine(&m, "x")
	m.Open()
	if got := m.View(); got != ":█" {
		t.Errorf("closed model took keys: %q", got)
	}
}

func TestModelHistory(t *testing.T) {
	m := New(plain())
	for _, l := range []string{"add #1", "cap 2", "cap 2", "q"} {
		m.Open()
		typeLine(&m, l)
		m.Update(press(tea.KeyEnter))
		m.PendingCommand()
	}
	m.Open()
	typeLine(&m, "ti")
	steps := []struct {
		k    tea.KeyType
		want string
	}{
		{tea.KeyUp, ":q█"},
		{tea.KeyUp, ":cap 2█"}, // the repeated line is kept once
		{tea.KeyUp, ":add #1█"},
		{tea.KeyUp, ":add #1█"}, // oldest: stays
		{tea.KeyDown, ":cap 2█"},
		{tea.KeyDown, ":q█"},
		{tea.KeyDown, ":ti█"}, // back to the line being typed
		{tea.KeyDown, ":ti█"},
	}
	for i, s := range steps {
		m.Update(press(s.k))
		if got := m.View(); got != s.want {
			t.Fatalf("step %d: view = %q, want %q", i, got, s.want)
		}
	}
}

func TestHistoryCap(t *testing.T) {
	m := New(plain())
	for i := range maxHistory + 10 {
		m.remember(strings.Repeat("x", i+1))
	}
	if len(m.history) != maxHistory || m.history[0] != strings.Repeat("x", 11) {
		t.Errorf("history len %d, first %q", len(m.history), m.history[0])
	}
}

func TestComplete(t *testing.T) {
	all := []string{"add", "auto", "tier", "cap", "stop", "resume", "repo", "settings", "q"}
	cases := []struct {
		line, want string
		cands      []string
	}{
		{"", "", all},
		{"a", "a", []string{"add", "auto"}},
		{"au", "auto ", []string{"auto"}},
		{":au", ":auto ", []string{"auto"}},
		{"s", "s", []string{"stop", "settings"}},
		{"se", "settings ", []string{"settings"}},
		{"auto ", "auto ", []string{"manual", "needs-you", "needs-you+yours", "all"}},
		{"auto n", "auto needs-you", []string{"needs-you", "needs-you+yours"}},
		{"auto needs-you+", "auto needs-you+yours", []string{"needs-you+yours"}},
		{"auto M", "auto manual", []string{"manual"}},
		{"stop a", "stop all", []string{"all"}},
		{"resume ", "resume all", []string{"all"}},
		{"tier d", "tier deep", []string{"deep"}},
		{"tier ", "tier ", []string{"deep", "none", "rules"}},
		{"cap d", "cap default", []string{"default"}},
		{"cap 1", "cap 1", nil},
		{"add #4", "add #4", nil},
		{"settings x", "settings x", nil},
		{"zz", "zz", nil},
		{"zz a", "zz a", nil},
		// M5.3: without repos :repo completes "all" only.
		{"re", "re", []string{"resume", "repo"}},
		{"rep", "repo ", []string{"repo"}},
		{"repo ", "repo all", []string{"all"}},
		{"repo b", "repo b", nil},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			got, cands := Complete(tc.line)
			if got != tc.want || strings.Join(cands, ",") != strings.Join(tc.cands, ",") {
				t.Errorf("Complete(%q) = %q, %v; want %q, %v", tc.line, got, cands, tc.want, tc.cands)
			}
		})
	}
}

func TestCompleteRepos(t *testing.T) {
	// Keys and labels as the root passes them: a non-GitHub repo's label is
	// its key, so names repeat.
	repos := []string{"billing-svc", "acme/billing-svc", "portal", "acme/portal", "tools", "tools", " "}
	cases := []struct {
		line, want string
		cands      []string
	}{
		{"repo ", "repo ", []string{"billing-svc", "acme/billing-svc", "portal", "acme/portal", "tools", "all"}},
		{"repo b", "repo billing-svc", []string{"billing-svc"}},
		{":repo B", ":repo billing-svc", []string{"billing-svc"}},
		{"repo acme/", "repo acme/", []string{"acme/billing-svc", "acme/portal"}},
		{"repo ACME/P", "repo acme/portal", []string{"acme/portal"}},
		{"repo a", "repo a", []string{"acme/billing-svc", "acme/portal", "all"}},
		{"repo al", "repo all", []string{"all"}},
		{"repo t", "repo tools", []string{"tools"}},
		{"repo x", "repo x", nil},
		{"stop ", "stop all", []string{"all"}}, // other commands ignore the repos
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			got, cands := complete(tc.line, repos)
			if got != tc.want || strings.Join(cands, ",") != strings.Join(tc.cands, ",") {
				t.Errorf("complete(%q) = %q, %v; want %q, %v", tc.line, got, cands, tc.want, tc.cands)
			}
		})
	}
	// Candidates that differ in case keep the typed prefix.
	if got, _ := complete("repo a", []string{"Alpha", "api"}); got != "repo A" {
		t.Errorf("mixed-case prefix = %q, want %q", got, "repo A")
	}
}

func TestModelTabCompletesRepos(t *testing.T) {
	m := New(plain())
	m.SetWidth(80)
	m.Open()
	typeLine(&m, "repo b")
	m.Update(press(tea.KeyTab))
	if got := m.View(); got != ":repo b█" {
		t.Fatalf("without repos :repo b<tab> view = %q", got)
	}

	m.SetRepos([]string{"billing-svc", "acme/billing-svc", "acme/portal"})
	m.Open()
	typeLine(&m, "repo b")
	m.Update(press(tea.KeyTab))
	if got := m.View(); got != ":repo billing-svc█" {
		t.Fatalf(":repo b<tab> view = %q", got)
	}
	m.Update(press(tea.KeyEnter))
	if s, ok := m.PendingCommand(); !ok || s.Cmd != (Command{Kind: Repo, Arg: "billing-svc"}) {
		t.Errorf("submitted %+v", s)
	}

	m.Open()
	typeLine(&m, "repo acme")
	m.Update(press(tea.KeyTab))
	if got := m.View(); !strings.HasPrefix(got, ":repo acme/█") || !strings.Contains(got, "acme/billing-svc · acme/portal") {
		t.Fatalf(":repo acme<tab> view = %q", got)
	}
	m.Update(press(tea.KeyTab))
	m.Update(press(tea.KeyTab))
	if got := m.View(); !strings.HasPrefix(got, ":repo acme/portal█") {
		t.Errorf("cycled view = %q", got)
	}

	// The repos are per model: another model and the static Complete do
	// not see them.
	if _, cands := Complete("repo b"); cands != nil {
		t.Errorf("Complete saw repos: %v", cands)
	}
	m.SetRepos(nil)
	m.Open()
	typeLine(&m, "repo ")
	m.Update(press(tea.KeyTab))
	if got := m.View(); got != ":repo all█" {
		t.Errorf("after SetRepos(nil) view = %q", got)
	}
}

func TestSetReposCopies(t *testing.T) {
	names := []string{"api", "web"}
	m := New(plain())
	m.SetRepos(names)
	names[0] = "changed"
	if _, cands := complete("repo a", m.repos); strings.Join(cands, ",") != "api,all" {
		t.Errorf("candidates %v: SetRepos kept the caller's slice", cands)
	}
}

func TestHelpCoversEveryCommand(t *testing.T) {
	h := Help()
	var usages []string
	for _, e := range h {
		if e.Usage == "" || e.Desc == "" {
			t.Errorf("incomplete entry %+v", e)
		}
		usages = append(usages, e.Usage)
	}
	joined := strings.Join(usages, "\n")
	for _, c := range commands() {
		if !strings.Contains(joined, ":"+c.name) {
			t.Errorf("Help misses :%s", c.name)
		}
		if _, ok := lookup(c.name); !ok {
			t.Errorf("lookup(%q) failed", c.name)
		}
	}
	want := []HelpEntry{
		{":auto manual|needs-you|needs-you+yours|all", "set the auto-review strategy (saved to .git/prt/prt.toml)", false},
		{":stop [all]", "stop the selected item's analysis (all: every item)", false},
		{":resume [all]", "resume the selected item from its last finished stage (all: every stopped item)", false},
		{":settings", "open the settings page (also ,)", false},
		{":repo <name>|all", "show one repo of the workspace (all: every repo)", true},
		{":add #482 | :add feat/x", "add a PR or branch and analyse it now", false},
		{":add acme/api#12 | :add api:feat/x", "add from one repo of the workspace (owner/name#N, repo#N, repo:branch)", true},
	}
	var order, wsOnly []string
	for _, e := range h {
		order = append(order, strings.Fields(e.Usage)[0])
		if e.Workspace {
			wsOnly = append(wsOnly, e.Usage)
		}
	}
	if got := strings.Join(order, " "); got != ":add :add :auto :tier :cap :stop :resume :repo :settings :q" {
		t.Errorf("Help order = %s", got)
	}
	// A single repo's overlay leaves out exactly the workspace forms (M5.3).
	if got := strings.Join(wsOnly, " ; "); got != ":add acme/api#12 | :add api:feat/x ; :repo <name>|all" {
		t.Errorf("workspace-only entries = %s", got)
	}
	for _, w := range want {
		found := false
		for _, e := range h {
			found = found || e == w
		}
		if !found {
			t.Errorf("Help lacks %+v", w)
		}
	}
}

func TestModelTabCompletes(t *testing.T) {
	m := New(plain())
	m.Open()
	typeLine(&m, "au")
	m.Update(press(tea.KeyTab))
	if got := m.View(); got != ":auto █" {
		t.Fatalf("after :au<tab> view = %q", got)
	}
	typeLine(&m, "a")
	m.Update(press(tea.KeyTab))
	if got := m.View(); got != ":auto all█" {
		t.Fatalf("after a<tab> view = %q", got)
	}
	m.Update(press(tea.KeyEnter))
	s, ok := m.PendingCommand()
	if !ok || s.Cmd != (Command{Kind: Auto, Strategy: model.AutoAll}) {
		t.Errorf("submitted %+v", s)
	}
}

func TestModelTabCycles(t *testing.T) {
	m := New(plain())
	m.SetWidth(40)
	m.Open()
	typeLine(&m, "a")
	m.Update(press(tea.KeyTab))
	v := m.View()
	if !strings.HasPrefix(v, ":a█") || !strings.HasSuffix(v, "add · auto") {
		t.Fatalf("ambiguous view = %q", v)
	}
	if w := ansi.StringWidth(v); w != 40 {
		t.Errorf("view width %d, want 40", w)
	}
	steps := []struct {
		k    tea.KeyType
		want string
	}{
		{tea.KeyTab, ":add█"},
		{tea.KeyTab, ":auto█"},
		{tea.KeyTab, ":add█"}, // wraps
		{tea.KeyShiftTab, ":auto█"},
		{tea.KeyShiftTab, ":add█"},
	}
	for i, s := range steps {
		m.Update(press(s.k))
		if got := m.View(); !strings.HasPrefix(got, s.want) || !strings.Contains(got, "add · auto") {
			t.Fatalf("step %d: view = %q, want prefix %q and candidates", i, got, s.want)
		}
	}
	// Typing ends the cycle and hides the candidates.
	typeLine(&m, " ")
	if got := m.View(); got != ":add █" {
		t.Errorf("after typing view = %q", got)
	}
	// Shift+tab first walks back from the common prefix.
	m.SetWidth(80)
	m.Open()
	typeLine(&m, "auto n")
	m.Update(press(tea.KeyTab))
	if got := m.View(); !strings.HasPrefix(got, ":auto needs-you█") || !strings.Contains(got, "needs-you · needs-you+yours") {
		t.Fatalf("auto n<tab> view = %q", got)
	}
	m.Update(press(tea.KeyShiftTab))
	if got := m.View(); !strings.HasPrefix(got, ":auto needs-you+yours█") {
		t.Errorf("shift+tab view = %q", got)
	}
	// No candidates: tab does nothing.
	m.Open()
	typeLine(&m, "zz")
	m.Update(press(tea.KeyTab))
	if got := m.View(); got != ":zz█" {
		t.Errorf("zz<tab> view = %q", got)
	}
}

func TestCandidatesCutToWidth(t *testing.T) {
	m := New(plain())
	m.SetWidth(20)
	m.Open()
	m.Update(press(tea.KeyTab)) // every command
	v := m.View()
	if w := ansi.StringWidth(v); w > 20 {
		t.Errorf("view %q is %d wide", v, w)
	}
	if !strings.HasPrefix(v, ":█") || !strings.Contains(v, "add · auto") || !strings.HasSuffix(v, "…") {
		t.Errorf("view %q", v)
	}
	// Too narrow: candidates dropped, prompt kept.
	m.SetWidth(5)
	if v := m.View(); v != ":█" {
		t.Errorf("narrow view %q", v)
	}
}

func TestViewWidth(t *testing.T) {
	m := New(plain())
	m.SetWidth(10)
	m.Open()
	typeLine(&m, "add feat/a-very-long-branch")
	got := m.View()
	if w := lipgloss.Width(got); w > 10 {
		t.Errorf("view %q is %d wide", got, w)
	}
	if !strings.HasSuffix(got, "█") || !strings.HasPrefix(got, ":") {
		t.Errorf("view %q lost the prompt or cursor", got)
	}
}
