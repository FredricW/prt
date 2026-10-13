package cmdline

import (
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
		{line: "foo", wantErr: "unknown command :foo — try :add, :tier, :cap, :q"},
		{line: ":", wantErr: "empty command"},
		{line: "", wantErr: "empty command"},
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
