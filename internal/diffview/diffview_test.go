package diffview

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/golden")

var fixtures = []string{"add", "modify", "delete", "rename", "binary", "noeol"}

func plainRenderer() *lipgloss.Renderer { return lipgloss.NewRenderer(io.Discard) }

func colourRenderer() *lipgloss.Renderer {
	r := lipgloss.NewRenderer(io.Discard, termenv.WithProfile(termenv.ANSI256))
	r.SetColorProfile(termenv.ANSI256)
	return r
}

func loadFixture(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".diff"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func newModel(r *lipgloss.Renderer, raw string, w, h int) Model {
	m := New(r)
	m.SetSize(w, h)
	m.SetDiff(raw)
	return m
}

// kinds encodes the row layout as one letter per row.
func kinds(m Model) string {
	letters := map[rowKind]byte{kindFile: 'F', kindHunk: 'H', kindLine: 'L', kindNoEOL: 'N', kindRaw: 'R'}
	var b strings.Builder
	for _, r := range m.rows {
		b.WriteByte(letters[r.kind])
	}
	return b.String()
}

// genDiff builds a synthetic diff of nFiles files with perFile lines each:
// one context line then alternating -/+ pairs.
func genDiff(nFiles, perFile int) string {
	var b strings.Builder
	for f := range nFiles {
		p := fmt.Sprintf("dir/file%03d.go", f)
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n", p, p, p, p)
		fmt.Fprintf(&b, "@@ -1,%d +1,%d @@ func F%d()\n", perFile, perFile, f)
		for i := range perFile {
			switch {
			case i == 0:
				fmt.Fprintf(&b, " context line %d\n", i)
			case i%2 == 1:
				fmt.Fprintf(&b, "-\tvalue := compute(%d, old)\n", i)
			default:
				fmt.Fprintf(&b, "+\tvalue := compute(%d, new)\n", i)
			}
		}
	}
	return b.String()
}

func TestRows(t *testing.T) {
	tests := []struct {
		name      string
		kinds     string
		files     int
		titleHas  []string
		firstPath string
	}{
		{"add", "FHLLL", 1, []string{"hello.go (added)", "+3 −0"}, "hello.go"},
		{"modify", "FHLLLLLLLHLLLL", 1, []string{"calc/calc.go", "+2 −1"}, "calc/calc.go"},
		{"delete", "FHLL", 1, []string{"old.txt (deleted)", "+0 −2"}, "old.txt"},
		{"rename", "FHLLL", 1, []string{"pkg/a.go → pkg/b.go (renamed 90%)", "+1 −1"}, "pkg/b.go"},
		{"binary", "FFHLL", 2, []string{"logo.png (binary)"}, "logo.png"},
		{"noeol", "FHLNLN", 1, []string{"VERSION", "+1 −1"}, "VERSION"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(plainRenderer(), loadFixture(t, tt.name), 80, 10)
			if got := kinds(m); got != tt.kinds {
				t.Errorf("kinds = %q, want %q", got, tt.kinds)
			}
			if m.RowCount() != len(tt.kinds) {
				t.Errorf("RowCount = %d, want %d", m.RowCount(), len(tt.kinds))
			}
			if len(m.Files()) != tt.files || len(m.heads) != tt.files {
				t.Errorf("files = %d heads = %d, want %d", len(m.Files()), len(m.heads), tt.files)
			}
			title := fileTitle(m.files[0], m.meta[0])
			for _, s := range tt.titleHas {
				if !strings.Contains(title, s) {
					t.Errorf("title %q lacks %q", title, s)
				}
			}
			if got := m.CurrentFile(); got != tt.firstPath {
				t.Errorf("CurrentFile = %q, want %q", got, tt.firstPath)
			}
		})
	}
}

func TestPairsLinked(t *testing.T) {
	m := newModel(plainRenderer(), loadFixture(t, "modify"), 80, 10)
	// rows: 0 F, 1 H, 2 ctx, 3 ctx, 4 del, 5 add
	if m.rows[4].pair != 5 || m.rows[5].pair != 4 {
		t.Errorf("pair = %d/%d, want 5/4", m.rows[4].pair, m.rows[5].pair)
	}
	if m.rows[2].pair != -1 {
		t.Errorf("context pair = %d, want -1", m.rows[2].pair)
	}
	if m.gutter != 2 {
		t.Errorf("gutter = %d, want 2", m.gutter)
	}
}

func TestRawFallback(t *testing.T) {
	tests := []struct{ name, raw string }{
		{"no git header", "just some text\nmore text\n"},
		{"bad hunk header", "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ nonsense\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(plainRenderer(), tt.raw, 40, 5)
			if !strings.HasPrefix(kinds(m), "RR") || strings.Trim(kinds(m), "R") != "" {
				t.Errorf("kinds = %q, want raw rows", kinds(m))
			}
			if m.CurrentFile() != "" {
				t.Errorf("CurrentFile = %q, want empty", m.CurrentFile())
			}
			if p, n := m.Line(); p != "" || n != 0 {
				t.Errorf("Line = %q,%d", p, n)
			}
			if first := strings.Split(m.View(), "\n")[0]; first != strings.Split(tt.raw, "\n")[0] {
				t.Errorf("first line = %q", first)
			}
		})
	}
}

func TestEmpty(t *testing.T) {
	m := newModel(plainRenderer(), "", 30, 3)
	want := "(no diff yet)\n\n"
	if got := m.View(); got != want {
		t.Errorf("View = %q, want %q", got, want)
	}
	if m.CurrentFile() != "" || m.RowCount() != 0 {
		t.Error("empty model reports content")
	}
	m.ScrollBy(5)
	m.NextFile()
	m.PrevFile()
	if m.Offset() != 0 {
		t.Errorf("offset = %d", m.Offset())
	}
}

func TestGolden(t *testing.T) {
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			m := New(plainRenderer())
			m.SetDiff(loadFixture(t, name))
			m.SetSize(60, m.RowCount()+2)
			got := m.View() + "\n"
			path := filepath.Join("testdata", "golden", name+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
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
		})
	}
}

func TestViewDimensions(t *testing.T) {
	tricky := "diff --git a/w.txt b/w.txt\n--- a/w.txt\n+++ b/w.txt\n@@ -1,2 +1,2 @@ 日本語のセクション\n" +
		"-\t日本語テキスト🙂 wide and long " + strings.Repeat("x", 300) + "\n" +
		"+\t日本語テキスト🙃 wide and long " + strings.Repeat("y", 300) + "\n" +
		" ctl \x1b[31mred\x07 bell\n"
	sources := map[string]string{"tricky": tricky, "synthetic": genDiff(5, 20), "raw": "plain\ttext 日本語\n\x1b[2Jboom\n"}
	for _, name := range fixtures {
		sources[name] = loadFixture(t, name)
	}
	renderers := map[string]*lipgloss.Renderer{"plain": plainRenderer(), "colour": colourRenderer()}
	for sname, src := range sources {
		for rname, r := range renderers {
			for _, w := range []int{1, 2, 5, 13, 40, 200} {
				for _, h := range []int{1, 3, 50} {
					m := newModel(r, src, w, h)
					for _, off := range []int{0, 1, 1000} {
						m.GotoTop()
						m.ScrollBy(off)
						checkView(t, fmt.Sprintf("%s/%s/w%d/h%d/off%d", sname, rname, w, h, off), m.View(), w, h)
					}
				}
			}
		}
	}
}

func checkView(t *testing.T, label, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != h {
		t.Errorf("%s: %d lines, want %d", label, len(lines), h)
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got > w {
			t.Errorf("%s: line %d width %d > %d: %q", label, i, got, w, l)
		}
		if strings.ContainsAny(ansi.Strip(l), "\x1b\x07\t") {
			t.Errorf("%s: line %d has control characters: %q", label, i, l)
		}
	}
}

func TestTruncationEllipsis(t *testing.T) {
	m := newModel(plainRenderer(), genDiff(1, 3), 20, 5)
	lines := strings.Split(m.View(), "\n")
	// row 2 is the context line: "1 1   context line 0" is exactly 20 cells.
	if lines[2] != "1 1   context line 0" {
		t.Errorf("line 2 = %q", lines[2])
	}
	// row 3 is a deletion with an expanded tab, longer than 20 cells.
	if !strings.HasSuffix(lines[3], "…") || ansi.StringWidth(lines[3]) != 20 {
		t.Errorf("line 3 = %q, want truncated to 20 with ellipsis", lines[3])
	}
	if !strings.Contains(lines[3], "-     value") {
		t.Errorf("line 3 = %q, want tab expanded to 4 spaces", lines[3])
	}
}

func TestScrollClamp(t *testing.T) {
	m := newModel(plainRenderer(), genDiff(3, 10), 40, 5) // 3*(1+1+10) = 36 rows
	if m.RowCount() != 36 {
		t.Fatalf("RowCount = %d", m.RowCount())
	}
	m.ScrollBy(-3)
	if m.Offset() != 0 {
		t.Errorf("offset after scrolling up at top = %d", m.Offset())
	}
	m.ScrollBy(1000)
	if m.Offset() != 31 {
		t.Errorf("offset after scrolling past bottom = %d, want 31", m.Offset())
	}
	m.GotoTop()
	m.GotoBottom()
	if m.Offset() != 31 {
		t.Errorf("GotoBottom offset = %d, want 31", m.Offset())
	}
	m.SetSize(40, 30)
	if m.Offset() != 6 {
		t.Errorf("SetSize clamps offset: got %d, want 6", m.Offset())
	}
	m.SetSize(40, 100)
	m.ScrollBy(1)
	if m.Offset() != 0 {
		t.Errorf("scroll down with everything visible moved to %d", m.Offset())
	}
}

func TestFileNavigation(t *testing.T) {
	m := newModel(plainRenderer(), genDiff(3, 10), 40, 5) // headers at 0, 12, 24
	steps := []struct {
		do     func(*Model)
		offset int
		file   string
	}{
		{(*Model).NextFile, 12, "dir/file001.go"},
		{(*Model).NextFile, 24, "dir/file002.go"},
		{(*Model).NextFile, 24, "dir/file002.go"}, // last file stays on top (overscroll)
		{func(m *Model) { m.ScrollBy(2) }, 26, "dir/file002.go"},
		{(*Model).PrevFile, 24, "dir/file002.go"}, // inside a file: back to its header
		{(*Model).PrevFile, 12, "dir/file001.go"},
		{func(m *Model) { m.ScrollBy(-1) }, 11, "dir/file000.go"},
		{(*Model).PrevFile, 0, "dir/file000.go"},
		{(*Model).PrevFile, 0, "dir/file000.go"},
	}
	for i, s := range steps {
		s.do(&m)
		if m.Offset() != s.offset || m.CurrentFile() != s.file {
			t.Fatalf("step %d: offset %d file %q, want %d %q", i, m.Offset(), m.CurrentFile(), s.offset, s.file)
		}
	}
	lines := strings.Split(m.View(), "\n")
	if !strings.HasPrefix(lines[0], " dir/file000.go") {
		t.Errorf("top row = %q, want file header", lines[0])
	}
}

func TestLine(t *testing.T) {
	m := newModel(plainRenderer(), loadFixture(t, "modify"), 80, 3)
	tests := []struct {
		offset int
		line   int
	}{
		{0, 0},   // file header
		{1, 8},   // hunk header: new start
		{2, 8},   // context
		{4, 9},   // deletion: nearest preceding new-side line
		{5, 10},  // addition
		{9, 20},  // second hunk header
		{13, 23}, // last addition
	}
	for _, tt := range tests {
		m.GotoTop()
		m.offset = tt.offset
		p, n := m.Line()
		if p != "calc/calc.go" || n != tt.line {
			t.Errorf("offset %d: Line = %q,%d want calc/calc.go,%d", tt.offset, p, n, tt.line)
		}
	}
	d := newModel(plainRenderer(), loadFixture(t, "delete"), 80, 3)
	d.offset = 2
	if p, n := d.Line(); p != "old.txt" || n != 1 {
		t.Errorf("deleted file Line = %q,%d want old.txt,1", p, n)
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestUpdate(t *testing.T) {
	steps := []struct {
		msg    tea.Msg
		offset int
	}{
		{key("j"), 1},
		{key("down"), 2},
		{key("k"), 1},
		{key("up"), 0},
		{key("ctrl+d"), 5},
		{key("ctrl+u"), 0},
		{key("pgdown"), 10},
		{key("space"), 20},
		{key("pgup"), 10},
		{key("G"), 26},
		{key("g"), 0},
		{key("end"), 26},
		{key("home"), 0},
		{key("]"), 12},
		{key("["), 0},
		{tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}, 3},
		{tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}, 6},
		{tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress}, 3},
		{tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}, 3},
		{key("x"), 3},
		{tea.WindowSizeMsg{Width: 1, Height: 1}, 3},
	}
	m := newModel(plainRenderer(), genDiff(3, 10), 40, 10)
	for i, s := range steps {
		var cmd tea.Cmd
		m, cmd = m.Update(s.msg)
		if cmd != nil {
			t.Errorf("step %d: unexpected cmd", i)
		}
		if m.Offset() != s.offset {
			t.Fatalf("step %d (%v): offset %d, want %d", i, s.msg, m.Offset(), s.offset)
		}
	}
}

func TestIntraLineHighlight(t *testing.T) {
	m := newModel(colourRenderer(), loadFixture(t, "modify"), 80, 20)
	view := m.View()
	added := m.st.addEmph.Render(" + 0")
	if !strings.Contains(view, added) {
		t.Errorf("view lacks emphasised addition %q:\n%q", added, view)
	}
	if !strings.Contains(view, m.st.add.Render("    return a + b")) {
		t.Errorf("view lacks unchanged prefix in base style")
	}
	if spans := m.cache.spans[4]; len(spans) != 0 {
		t.Errorf("deleted line spans = %v, want none (pure insertion)", spans)
	}
	if spans := m.cache.spans[5]; len(spans) != 1 {
		t.Errorf("added line spans = %v, want one", spans)
	}
	r := newModel(colourRenderer(), loadFixture(t, "rename"), 80, 20)
	rv := r.View()
	if !strings.Contains(rv, r.st.delEmph.Render("a")) || !strings.Contains(rv, r.st.addEmph.Render("b")) {
		t.Errorf("rename view lacks emphasised spans:\n%q", rv)
	}
}

func TestLongPairSkipsIntraLine(t *testing.T) {
	long := strings.Repeat("a", maxIntraBytes)
	raw := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-" + long + "\n+" + long + "b\n"
	m := newModel(colourRenderer(), raw, 40, 5)
	_ = m.View()
	if s, ok := m.cache.spans[3]; !ok || s != nil {
		t.Errorf("spans = %v (cached %v), want cached nil", s, ok)
	}
}

func TestLazyRendering(t *testing.T) {
	m := newModel(plainRenderer(), genDiff(200, 25), 120, 40)
	if m.RowCount() != 200*(25+2) {
		t.Fatalf("RowCount = %d", m.RowCount())
	}
	_ = m.View()
	if m.cache.renders != 40 {
		t.Errorf("first View rendered %d rows, want 40", m.cache.renders)
	}
	_ = m.View()
	if m.cache.renders != 40 {
		t.Errorf("second View rendered %d rows, want cached", m.cache.renders)
	}
	m.ScrollBy(1)
	_ = m.View()
	if m.cache.renders != 41 {
		t.Errorf("scroll by one rendered %d rows total, want 41", m.cache.renders)
	}
	if len(m.cache.spans) > 2*40 {
		t.Errorf("intra-line spans computed for %d rows, want only visible pairs", len(m.cache.spans))
	}
	m.GotoBottom()
	_ = m.View()
	if m.cache.renders != 81 {
		t.Errorf("jump to bottom rendered %d rows total, want 81", m.cache.renders)
	}
	m.SetSize(100, 40)
	_ = m.View()
	if m.cache.renders != 121 {
		t.Errorf("resize re-rendered %d rows total, want 121", m.cache.renders)
	}
}

func TestClean(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a\tb", "a    b"},
		{"\x1b[31m", "?[31m"},
		{"bell\x07\r", "bell??"},
		{"\u0085x", "?x"},
		{"日本 ok", "日本 ok"},
	}
	for _, tt := range tests {
		if got := clean(tt.in); got != tt.want {
			t.Errorf("clean(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClip(t *testing.T) {
	s, cut := clip("日本語", 4) // 3 bytes per rune: cut back to a rune boundary
	if s != "日" || !cut {
		t.Errorf("clip = %q,%v", s, cut)
	}
	if s, cut := clip("abc", 3); s != "abc" || cut {
		t.Errorf("clip = %q,%v", s, cut)
	}
}

func BenchmarkSetDiff5000(b *testing.B) {
	raw := genDiff(200, 25)
	m := New(plainRenderer())
	b.ReportAllocs()
	for b.Loop() {
		m.SetDiff(raw)
	}
}

func BenchmarkView5000(b *testing.B) {
	m := newModel(colourRenderer(), genDiff(200, 25), 120, 50)
	b.ReportAllocs()
	for b.Loop() {
		m.cache.resetLines() // measure rendering, not cache hits
		m.ScrollBy(7)
		if m.Offset() == m.maxOffset() {
			m.GotoTop()
		}
		_ = m.View()
	}
}
