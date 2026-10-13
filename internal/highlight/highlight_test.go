package highlight

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/")

// render lists a source's spans one per line: "L<n> <class> <text>".
func render(src string, ls [][]Span) string {
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	var b strings.Builder
	for i, spans := range ls {
		for _, s := range spans {
			fmt.Fprintf(&b, "L%d %-8s %q\n", i+1, s.Class, lines[i][s.Start:s.End])
		}
	}
	return b.String()
}

func TestLinesGolden(t *testing.T) {
	cases := []struct{ name, lang, src string }{
		{"go", "go", "package main\n\nimport \"fmt\"\n\n// Greet says hi.\nfunc Greet(n int) string {\n\tif n > 0 {\n\t\treturn fmt.Sprintf(\"hi %d\", n)\n\t}\n\treturn \"\"\n}\n\ntype T struct{ x float64 }\n\nconst c = 1.5\nvar ok = true\n"},
		{"ts", "ts", "// fetch json\nexport async function fetchJSON(url: string): Promise<Res> {\n  const res = await fetch(url, { n: 42 });\n  return res.json();\n}\nclass Store {}\n"},
		{"tsx", "tsx", "export function Card({ id }: Props) {\n  return <div className=\"card\">{id}</div>;\n}\n"},
		{"js", "js", "/* block */\nconst x = require('x');\nfunction f(a) { return a ? null : 3; }\nnew Foo(`t`);\n"},
		{"python", "python", "# comment\nimport os\n\nclass A:\n    def run(self, n=3):\n        return os.path.join('a', \"b\") if n else None\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render(tc.src, Lines(tc.lang, []byte(tc.src)))
			path := filepath.Join("testdata", tc.name+".golden")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
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
				t.Errorf("spans\n--- got\n%s--- want\n%s", got, want)
			}
		})
	}
}

func TestUnknownLanguageIsPlain(t *testing.T) {
	for _, lang := range []string{"", "rust", "markdown"} {
		if got := Lines(lang, []byte("fn main() {}\n")); got != nil {
			t.Errorf("Lines(%q) = %v, want nil", lang, got)
		}
		if got := Line(lang, "fn main() {}"); got != nil {
			t.Errorf("Line(%q) = %v, want nil", lang, got)
		}
	}
	if got := Lines("go", nil); got != nil {
		t.Errorf("empty source = %v", got)
	}
}

func TestLineAlone(t *testing.T) {
	tests := []struct {
		lang, text string
		want       []string // "class:text" in order (a subset check)
	}{
		{"go", "\treturn fmt.Sprintf(\"x\", 1)", []string{"keyword:return", "function:Sprintf", "string:\"x\"", "number:1"}},
		{"ts", "  if (!res.ok) throw new Error(`HTTP`);", []string{"keyword:if", "keyword:throw", "keyword:new", "string:`HTTP`"}},
		{"python", "def run(self):  # go", []string{"keyword:def", "function:run", "comment:# go"}},
		{"go", "func (s *S) Get(id string) error {", []string{"keyword:func", "type:S", "function:Get", "type:string", "type:error"}},
	}
	for _, tt := range tests {
		var got []string
		for _, s := range Line(tt.lang, tt.text) {
			got = append(got, s.Class+":"+tt.text[s.Start:s.End])
		}
		j := 0
		for _, g := range got {
			if j < len(tt.want) && g == tt.want[j] {
				j++
			}
		}
		if j != len(tt.want) {
			t.Errorf("Line(%s, %q) = %v, want in order %v", tt.lang, tt.text, got, tt.want)
		}
	}
}

func TestMultiByteOffsets(t *testing.T) {
	text := `	s := "日本語🙂" + x // 註解 é`
	spans := Line("go", text)
	if len(spans) == 0 {
		t.Fatal("no spans")
	}
	var classes []string
	for _, s := range spans {
		if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
			t.Fatalf("span %+v out of range", s)
		}
		seg := text[s.Start:s.End]
		if !utf8.ValidString(seg) {
			t.Errorf("span %+v cuts a rune: %q", s, seg)
		}
		classes = append(classes, s.Class+":"+seg)
	}
	want := []string{`string:"日本語🙂"`, "comment:// 註解 é"}
	for _, w := range want {
		if !strings.Contains(strings.Join(classes, "|"), w) {
			t.Errorf("spans %v lack %q", classes, w)
		}
	}
}

func TestMultiLineSpansSplit(t *testing.T) {
	src := "/* a\n   b */\nconst s = `x\ny`;\n"
	ls := Lines("js", []byte(src))
	if len(ls) != 4 {
		t.Fatalf("%d lines", len(ls))
	}
	if len(ls[0]) != 1 || ls[0][0].Class != "comment" || ls[1][0].Class != "comment" {
		t.Errorf("block comment not split per line: %v", ls[:2])
	}
	if s := ls[3][0]; s.Class != "string" || s.Start != 0 || s.End != 2 {
		t.Errorf("template string tail = %+v", s)
	}
}

func TestHighlighterCache(t *testing.T) {
	h := New(2, 2)
	src := []byte("package p\nfunc f() {}\n")
	a := h.Lines("go", src)
	if len(a) != 2 || h.files.len() != 1 {
		t.Fatalf("lines %v cached %d", a, h.files.len())
	}
	h.Lines("go", []byte("package q\n"))
	h.Lines("go", []byte("package r\n"))
	if h.files.len() != 2 {
		t.Errorf("file cache not bounded: %d", h.files.len())
	}
	h.Lines("rust", src)
	if h.files.len() != 2 {
		t.Error("unknown language cached")
	}
	for _, s := range []string{"return 1", "return 2", "return 3", "return 1"} {
		if len(h.Line("go", s)) == 0 {
			t.Errorf("Line(%q) empty", s)
		}
	}
	if h.lines.len() != 2 {
		t.Errorf("line cache not bounded: %d", h.lines.len())
	}
}

func BenchmarkLine(b *testing.B) {
	for b.Loop() {
		_ = Line("ts", `  const res = await fetch(url, { ...opts, credentials: "include" });`)
	}
}
