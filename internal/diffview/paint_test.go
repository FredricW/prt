package diffview

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/diff"
)

func TestWrapBounds(t *testing.T) {
	tests := []struct {
		s     string
		width int
		want  []int
	}{
		{"", 4, []int{0}},
		{"abcd", 4, []int{0}},
		{"abcdefghij", 4, []int{0, 4, 8}},
		{"日本語x", 4, []int{0, 6}},
		{"ab日本", 3, []int{0, 2, 5}}, // a wide rune never straddles a cut
		{"abc", 0, []int{0}},
		{"日", 1, []int{0}}, // wider than the piece: a piece of its own
	}
	for _, tt := range tests {
		got := WrapBounds(tt.s, tt.width)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("WrapBounds(%q, %d) = %v, want %v", tt.s, tt.width, got, tt.want)
		}
		for i, b := range got {
			end := len(tt.s)
			if i+1 < len(got) {
				end = got[i+1]
			}
			if w := ansi.StringWidth(tt.s[b:end]); tt.width > 1 && w > tt.width {
				t.Errorf("piece %q is %d cells wide", tt.s[b:end], w)
			}
		}
	}
}

// The colour path cleans text exactly like clean, so wrapping and widths
// agree in both renderers.
func TestPrepareCleansLikeClean(t *testing.T) {
	inputs := []string{"\tx := \"日本\" // \x1b[2J", "bell\x07\r", "\u0085x\xffy", "plain"}
	for _, p := range []*Painter{NewPainter(plainRenderer(), nil), NewPainter(trueColourRenderer(), nil)} {
		for _, in := range inputs {
			tx := p.Prepare("go", diff.Add, in, []diff.Span{{Start: 1, End: 3}}, 1000)
			if tx.Clean != clean(in) {
				t.Errorf("colour %v: Clean = %q, want %q", p.Colour(), tx.Clean, clean(in))
			}
			if got := ansi.Strip(tx.All()); got != clean(in) {
				t.Errorf("colour %v: rendered %q, want %q", p.Colour(), got, clean(in))
			}
			if !p.Colour() && strings.Contains(tx.All(), "\x1b") {
				t.Errorf("plain painter emitted escapes: %q", tx.All())
			}
		}
	}
}
