package theme

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/fredricw/prt/internal/model"
)

func plain() Theme {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	return New(r)
}

func coloured(p termenv.Profile) Theme {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(p)
	r.SetHasDarkBackground(true)
	return New(r)
}

func TestPlainHasNoEscapes(t *testing.T) {
	th := plain()
	if th.Colour {
		t.Fatal("Ascii renderer reports colour")
	}
	outs := []string{
		th.Box("Queue · ranked", "a\nb", 20, BoxOpt{Focused: true, Right: "x"}),
		th.Divider("NEEDS YOU", 20),
		th.UrgencyBar(2),
		th.Hints([]Hint{{"j/k", "move"}, {"?", "help"}}, 40),
		th.Chips(Chip{ChipWarn, "risk 5"}, Chip{ChipBad, "tests ✗"}),
		th.Tab("2 Code", true),
		th.Attention(model.AttnEyes).Render("■"),
		th.Syntax(SynKeyword).Render("func"),
	}
	for _, s := range outs {
		if strings.Contains(s, "\x1b") {
			t.Errorf("escape code in plain output %q", s)
		}
	}
}

// Every composition kind renders in its own colour in each of the six
// variants (dark and light background × true colour, 256 and 16 colours):
// the coloured bar draws plain █ cells, so two kinds sharing a colour
// cannot be told apart.
func TestKindColoursDistinct(t *testing.T) {
	kinds := []string{"refactor", "feature", "bugfix", "test", "config", "docs", "dependency", "perf", KindNameNoise}
	for _, dark := range []bool{true, false} {
		for _, p := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.ANSI} {
			r := lipgloss.NewRenderer(io.Discard)
			r.SetColorProfile(p)
			r.SetHasDarkBackground(dark)
			th := New(r)
			seen := map[string]string{}
			for _, k := range kinds {
				s := th.Kind(k).Render("█")
				if !strings.Contains(s, "\x1b[") {
					t.Errorf("dark=%v profile %v: %s has no colour", dark, p, k)
				}
				if prev, dup := seen[s]; dup {
					t.Errorf("dark=%v profile %v: %s and %s share a colour", dark, p, prev, k)
				}
				seen[s] = k
			}
		}
	}
}

func TestColourProfilesEmitEscapes(t *testing.T) {
	for _, p := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.ANSI} {
		th := coloured(p)
		if !th.Colour {
			t.Fatalf("profile %v: no colour", p)
		}
		if s := th.UrgencyBar(3); !strings.Contains(s, "\x1b[") {
			t.Errorf("profile %v: urgency bar has no colour: %q", p, s)
		}
	}
}

func TestBoxGeometry(t *testing.T) {
	th := plain()
	tests := []struct {
		name  string
		title string
		body  string
		w     int
		o     BoxOpt
		lines int
		want  []string // exact lines (plain)
	}{
		{
			name: "fit", title: "Risk", body: "HIGH", w: 12,
			want: []string{"┌─ Risk ───┐", "│ HIGH     │", "└──────────┘"},
		},
		{
			name: "height pads and cuts", title: "", body: "a\nb\nc", w: 6, o: BoxOpt{Height: 4},
			want: []string{"┌────┐", "│ a  │", "│ b  │", "└────┘"},
		},
		{
			name: "focused plain uses heavy border", title: "T", body: "x", w: 8, o: BoxOpt{Focused: true},
			want: []string{"┏━ T ━━┓", "┃ x    ┃", "┗━━━━━━┛"},
		},
		{
			name: "long body cut with ellipsis", title: "F", body: "abcdefghij", w: 8,
			want: []string{"┌─ F ──┐", "│ abc… │", "└──────┘"},
		},
		{
			name: "right text", title: "Code", body: "", w: 20, o: BoxOpt{Right: "3 of 7"},
			want: []string{"┌─ Code ── 3 of 7 ─┐", "│                  │", "└──────────────────┘"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Split(th.Box(tc.title, tc.body, tc.w, tc.o), "\n")
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
			for _, l := range got {
				if w := ansi.StringWidth(l); w != tc.w {
					t.Errorf("line %q width %d, want %d", l, w, tc.w)
				}
			}
		})
	}
}

func TestBoxColourWidth(t *testing.T) {
	th := coloured(termenv.TrueColor)
	for _, l := range strings.Split(th.Box("Queue · ranked", "row one\nrow two", 30, BoxOpt{Right: "r"}), "\n") {
		if w := ansi.StringWidth(l); w != 30 {
			t.Errorf("line %q width %d, want 30", l, w)
		}
	}
}

func TestDividerUrgencyTabChips(t *testing.T) {
	th := plain()
	tests := []struct{ got, want string }{
		{th.Divider("NEEDS YOU", 16), "── NEEDS YOU ───"},
		{th.Divider("", 4), "────"},
		{th.Divider("LONG LABEL HERE", 8), "── LONG…"},
		{th.UrgencyBar(0), "▯▯▯▯"},
		{th.UrgencyBar(3), "▮▮▮▯"},
		{th.UrgencyBar(9), "▮▮▮▮"},
		{th.Tab("1 Overview", true), "[1 Overview]"},
		{th.Tab("2 Code", false), " 2 Code "},
		{th.Chips(Chip{ChipPlain, "feature"}, Chip{ChipWarn, "⚠ breaking .86"}), "feature · ⚠ breaking .86"},
	}
	for i, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%d: got %q, want %q", i, tc.got, tc.want)
		}
	}
}

func TestHints(t *testing.T) {
	th := plain()
	hs := []Hint{{"j/k", "move"}, {"1 2 3", "tabs"}, {"?", "help"}}
	if got, want := th.Hints(hs, 80), "j/k move  1 2 3 tabs  ? help"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := th.Hints(hs, 22), "j/k move  1 2 3 tabs …"; got != want {
		t.Errorf("cut: got %q, want %q", got, want)
	}
	b := key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove"))
	off := key.NewBinding(key.WithDisabled())
	if got := HintsOf(b, off); len(got) != 1 || got[0] != (Hint{"x", "remove"}) {
		t.Errorf("HintsOf = %v", got)
	}
}

func TestSpreadTruncateProb(t *testing.T) {
	tests := []struct{ got, want string }{
		{Spread("prt · acme/portal", "synced", 30), "prt · acme/portal       synced"},
		{Spread("left side is long", "right", 12), "left … right"},
		{Spread("x", "toolongright", 5), "tool…"},
		{Truncate("abcdef", 4), "abc…"},
		{Truncate("abc", 0), ""},
		{Prob(0.861), ".86"},
		{Prob(1), "1.00"},
		{Prob(-1), ".00"},
	}
	for i, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%d: got %q, want %q", i, tc.got, tc.want)
		}
	}
}
