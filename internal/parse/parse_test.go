package parse

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// want describes one expected unit; sig "" means no signature.
type want struct {
	sym    string
	kind   model.UnitKind
	sig    string
	format bool
}

func TestSplitLanguages(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		base  string
		head  string
		hunks [][]want // expected units, grouped by hunk
	}{
		{
			name: "go", path: "server/store.go", base: "go/store.base.go", head: "go/store.head.go",
			hunks: [][]want{
				{{sym: "", kind: model.UnitAdded}},
				{{sym: "UserStore.Get", kind: model.UnitModified,
					sig: "func (s *UserStore) Get(ctx context.Context, id string) (string, error)"}},
				{
					{sym: "helper", kind: model.UnitModified, sig: "func helper(a int) int"},
					{sym: "other", kind: model.UnitModified, sig: "func other(b int) int"},
				},
				{{sym: "Sum", kind: model.UnitModified, sig: "func Sum(xs []int) int", format: true}},
				{{sym: "Max", kind: model.UnitModified, sig: "func Max(xs []int) int"}},
			},
		},
		{
			name: "ts", path: "web/client.ts", base: "ts/client.base.ts", head: "ts/client.head.ts",
			hunks: [][]want{
				{{sym: "", kind: model.UnitAdded}},
				{{sym: "ApiClient.getUser", kind: model.UnitModified,
					sig: "async getUser(id: string, opts: { fresh?: boolean } = {}): Promise<User>"}},
				{
					{sym: "toQuery", kind: model.UnitModified,
						sig: "export const toQuery = (q: Record<string, string>): string =>"},
					{sym: "plain", kind: model.UnitModified, sig: "export function plain(a: number): number"},
				},
			},
		},
		{
			name: "tsx", path: "web/Card.tsx", base: "tsx/card.base.tsx", head: "tsx/card.head.tsx",
			hunks: [][]want{{
				{sym: "Card", kind: model.UnitModified, sig: "export function Card({ name }: Props)"},
				{sym: "Badge", kind: model.UnitModified, sig: "export const Badge = ({ label }: { label: string }) =>"},
			}},
		},
		{
			name: "js", path: "web/widget.js", base: "js/widget.base.js", head: "js/widget.head.js",
			hunks: [][]want{
				{{sym: "", kind: model.UnitModified}},
				{{sym: "Widget.render", kind: model.UnitModified, sig: "render()"}},
			},
		},
		{
			name: "python", path: "scripts/report.py", base: "python/report.base.py", head: "python/report.head.py",
			hunks: [][]want{
				{{sym: "", kind: model.UnitAdded}},
				{
					{sym: "Report.render", kind: model.UnitModified, sig: "def render(self)"},
					{sym: "fetch", kind: model.UnitModified, sig: "async def fetch(client, url)"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, src := build(t, change{path: tt.path, old: fixture(t, tt.base), new: fixture(t, tt.head)})
			units := split(t, files, src)
			groups := checkInvariants(t, files, units)
			if len(groups) != len(tt.hunks) {
				t.Fatalf("got %d hunks %v, want %d", len(groups), symbols(units), len(tt.hunks))
			}
			for h, ws := range tt.hunks {
				got := groups[h]
				if len(got) != len(ws) {
					t.Fatalf("hunk %d: got units %v, want %d", h, symbols(got), len(ws))
				}
				for i, w := range ws {
					u := got[i]
					if u.Symbol != w.sym || u.Kind != w.kind || u.Signature != w.sig || u.Format != w.format {
						t.Errorf("hunk %d unit %d: got {%q %s %q format=%v}, want {%q %s %q format=%v}\n%s",
							h, i, u.Symbol, u.Kind, u.Signature, u.Format, w.sym, w.kind, w.sig, w.format, u.Diff)
					}
				}
			}
			again := split(t, files, src)
			if !reflect.DeepEqual(units, again) {
				t.Error("Split is not deterministic")
			}
		})
	}
}

func TestSplitRanges(t *testing.T) {
	files, src := build(t, change{path: "store.go", old: fixture(t, "go/store.base.go"), new: fixture(t, "go/store.head.go")})
	units := split(t, files, src)
	helper := bySymbol(units, "store.go", "helper")
	other := bySymbol(units, "store.go", "other")
	if len(helper) != 1 || len(other) != 1 {
		t.Fatalf("helper/other units: %v", symbols(units))
	}
	// Hunk @@ -20,11 +21,11 @@: helper keeps leading context and its closing
	// brace; other starts at the blank line before it and keeps the rest.
	wantHelper := model.Range{Old: model.Span{Start: 20, Count: 5}, New: model.Span{Start: 21, Count: 5}}
	wantOther := model.Range{Old: model.Span{Start: 25, Count: 6}, New: model.Span{Start: 26, Count: 6}}
	if helper[0].Range != wantHelper {
		t.Errorf("helper range %+v, want %+v\n%s", helper[0].Range, wantHelper, helper[0].Diff)
	}
	if other[0].Range != wantOther {
		t.Errorf("other range %+v, want %+v\n%s", other[0].Range, wantOther, other[0].Diff)
	}
}

func TestSplitWholeFileKinds(t *testing.T) {
	tests := []struct {
		name string
		c    change
		kind model.UnitKind
	}{
		{"added", change{path: "store.go", new: fixture(t, "go/store.base.go"), added: true}, model.UnitAdded},
		{"deleted", change{path: "store.go", old: fixture(t, "go/store.base.go"), deleted: true}, model.UnitDeleted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, src := build(t, tt.c)
			units := split(t, files, src)
			for _, u := range units {
				if u.Kind != tt.kind {
					t.Errorf("unit %q kind %s, want %s", u.Symbol, u.Kind, tt.kind)
				}
			}
			for _, sym := range []string{"", "UserStore.Get", "helper", "other", "Describe", "Sum", "Max"} {
				if len(bySymbol(units, "store.go", sym)) != 1 {
					t.Errorf("want one unit for %q, got units %v", sym, symbols(units))
				}
			}
			if got := bySymbol(units, "store.go", "UserStore.Get"); len(got) == 1 &&
				got[0].Signature != "func (s *UserStore) Get(id string) (string, error)" {
				t.Errorf("Get signature %q", got[0].Signature)
			}
		})
	}
}

func TestSplitMoved(t *testing.T) {
	files, src := build(t,
		change{path: "a.go", old: fixture(t, "go/moved_a.base.go"), new: fixture(t, "go/moved_a.head.go")},
		change{path: "b.go", new: fixture(t, "go/moved_b.head.go"), added: true},
	)
	units := split(t, files, src)
	from := bySymbol(units, "a.go", "moved")
	to := bySymbol(units, "b.go", "moved")
	if len(from) != 1 || len(to) != 1 {
		t.Fatalf("moved units: %v", symbols(units))
	}
	if from[0].Kind != model.UnitMoved || to[0].Kind != model.UnitMoved {
		t.Errorf("kinds %s → %s, want moved", from[0].Kind, to[0].Kind)
	}
	if to[0].MovedFrom != from[0].ID || from[0].MovedFrom != "" {
		t.Errorf("MovedFrom %q, want %q", to[0].MovedFrom, from[0].ID)
	}
	if pkg := bySymbol(units, "b.go", ""); len(pkg) != 1 || pkg[0].Kind != model.UnitAdded {
		t.Errorf("package clause unit: %+v", pkg)
	}
}

func TestShapeHash(t *testing.T) {
	fn := func(name, arg string) string {
		return "export function " + name + "() {\n  return wait(" + arg + ");\n}\n"
	}
	imp := func(name, from string) string {
		return "import { " + name + " } from \"" + from + "\";\n\nexport const a = 1;\n"
	}
	changes := []change{
		{path: "web/api/a.ts", old: imp("fetchJSON", "./client"), new: imp("requestJSON", "./client")},
		{path: "web/hooks/b.tsx", old: imp("fetchJSON", "../api/client"), new: imp("requestJSON", "../api/client")},
		{path: "web/c.ts", old: fn("f", "100"), new: fn("f", "200")},
		{path: "web/d.ts", old: fn("g", "100"), new: fn("g", "300")},
		{path: "web/e.ts", old: fn("h", "100"), new: fn("h", "200")},
	}
	files, src := build(t, changes...)
	for _, s := range []struct {
		name string
		src  Source
	}{{"tree-sitter", src}, {"fallback tokenizer", nil}} {
		t.Run(s.name, func(t *testing.T) {
			units := split(t, files, s.src)
			if len(units) != len(changes) {
				t.Fatalf("got %d units, want %d", len(units), len(changes))
			}
			hash := map[string]string{}
			for _, u := range units {
				hash[u.Path] = u.ShapeHash
			}
			tests := []struct {
				a, b string
				same bool
			}{
				{"web/api/a.ts", "web/hooks/b.tsx", true}, // same rename, different path literal
				{"web/c.ts", "web/e.ts", true},            // same edit, different function names
				{"web/c.ts", "web/d.ts", false},           // different number literal
				{"web/api/a.ts", "web/c.ts", false},
			}
			for _, tt := range tests {
				if (hash[tt.a] == hash[tt.b]) != tt.same {
					t.Errorf("%s vs %s: same=%v, want %v", tt.a, tt.b, !tt.same, tt.same)
				}
			}
		})
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		old, new string
		format   bool
	}{
		{"ts reindent", "a.ts", "function f() {\n  if (x) {\n    y();\n  }\n}\n", "function f() {\n    if (x) {\n        y();\n    }\n}\n", true},
		{"ts reflow adding a comma", "a.ts", "function f() {\n  g(a, b);\n}\n", "function f() {\n  g(\n    a,\n    b,\n  );\n}\n", false},
		{"ts line join", "a.ts", "function f() {\n  g(\n    a,\n    b\n  );\n}\n", "function f() {\n  g(a, b);\n}\n", true},
		{"comment edit", "a.ts", "function f() {\n  // old note\n  y();\n}\n", "function f() {\n  // new note\n  y();\n}\n", false},
		{"string spacing", "a.ts", "function f() {\n  y(\"a b\");\n}\n", "function f() {\n  y(\"a  b\");\n}\n", false},
		{"python spacing", "a.py", "def f():\n    return x+1\n", "def f():\n    return x + 1\n", true},
		{"python reindent", "a.py", "def f():\n    if a:\n        b()\n    c()\n", "def f():\n    if a:\n        b()\n        c()\n", false},
		{"unknown language", "a.txt", "a  b\n", "a b\n", true},
		{"pure addition", "a.ts", "function f() {\n}\n", "function f() {\n  y();\n}\n", false},
		// Regression: a statement moved past context lines is a reorder, not
		// layout, even though both token streams are equal.
		{"go reordered statement", "a.go",
			"package x\n\nfunc Save() error {\n\tif err := validate(); err != nil {\n\t\treturn err\n\t}\n\twrite()\n\treturn nil\n}\n",
			"package x\n\nfunc Save() error {\n\twrite()\n\tif err := validate(); err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n", false},
		{"ts swapped calls", "a.ts", "function f() {\n  lock();\n  work();\n  unlock();\n}\n", "function f() {\n  unlock();\n  work();\n  lock();\n}\n", false},
		{"js return reflow (ASI)", "a.js", "function f() {\n  return x;\n}\n", "function f() {\n  return\n  x;\n}\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, src := build(t, change{path: tt.path, old: tt.old, new: tt.new})
			units := split(t, files, src)
			if len(units) != 1 {
				t.Fatalf("got %d units %v", len(units), symbols(units))
			}
			if units[0].Format != tt.format {
				t.Errorf("Format = %v, want %v\n%s", units[0].Format, tt.format, units[0].Diff)
			}
		})
	}
}

// Regression: two functions with the same edit gave two units with the same
// id, so the store kept only one of them.
func TestSplitDuplicateBodiesGetUniqueIDs(t *testing.T) {
	body := "\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n"
	fn := func(name string, wrapped bool) string {
		b := body
		if wrapped {
			b = strings.Replace(b, "return err", "return wrap(err)", 1)
		}
		return "func " + name + "() error {\n\terr := do()\n\tx := 1\n\ty := 2\n\tz := 3\n" + b
	}
	gap := "\n// spacer\n// spacer\n// spacer\n// spacer\n// spacer\n// spacer\n\n"
	old := "package x\n\n" + fn("A", false) + gap + fn("B", false)
	head := "package x\n\n" + fn("A", true) + gap + fn("B", true)
	files, src := build(t, change{path: "x.go", old: old, new: head})
	units := split(t, files, src) // checkInvariants requires unique ids
	if len(units) != 2 {
		t.Fatalf("got %d units %v, want 2", len(units), symbols(units))
	}
	if units[0].Diff != units[1].Diff {
		t.Fatalf("fixture should give identical bodies:\n%s\n---\n%s", units[0].Diff, units[1].Diff)
	}
	if units[0].ID != model.UnitID("x.go", units[0].Diff) {
		t.Errorf("first duplicate should keep the content id")
	}
	if units[1].ID != model.DuplicateUnitID(units[0].ID, units[1].Symbol, 2) {
		t.Errorf("second duplicate id = %s", units[1].ID)
	}
}

func TestSplitFallback(t *testing.T) {
	store := change{path: "store.go", old: fixture(t, "go/store.base.go"), new: fixture(t, "go/store.head.go")}
	broken := change{path: "bad.go",
		old: "package x\n\nfunc a( {\n\treturn 1\n}\n",
		new: "package x\n\nfunc a( {\n\treturn 2\n}\n"}
	tests := []struct {
		name    string
		c       change
		src     func(fakeSource) Source
		section bool // symbol is the hunk section (else empty)
	}{
		{"nil source", store, func(fakeSource) Source { return nil }, true},
		{"show error", store, func(fakeSource) Source { return fakeSource{} }, true},
		{"stale source", store, func(s fakeSource) Source {
			s[headRev+":store.go"] = []byte("package store\n")
			return s
		}, true},
		{"syntax error", broken, func(s fakeSource) Source { return s }, true},
		{"markdown", change{path: "README.md",
			old: "# Title\n\nintro\n\n## A\n\none\ntwo\nthree\nfour\nfive\nsix\nseven\n\n## B\n\nend\n",
			new: "# Title\n\nintro text\n\n## A\n\none\ntwo\nthree\nfour\nfive\nsix\nseven\n\n## B\n\nthe end\n"},
			func(s fakeSource) Source { return s }, false},
		{"json", change{path: "package.json",
			old: "{\n  \"name\": \"x\",\n  \"version\": \"1.0.0\"\n}\n",
			new: "{\n  \"name\": \"x\",\n  \"version\": \"1.1.0\"\n}\n"},
			func(s fakeSource) Source { return s }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, src := build(t, tt.c)
			units := split(t, files, tt.src(src))
			hunks := files[0].Hunks
			if len(units) != len(hunks) {
				t.Fatalf("got %d units %v for %d hunks", len(units), symbols(units), len(hunks))
			}
			for i, u := range units {
				wantSym := ""
				if tt.section {
					wantSym = hunks[i].Section
				}
				if u.Symbol != wantSym || u.Signature != "" || u.Diff != hunks[i].Body() {
					t.Errorf("unit %d: symbol %q sig %q, want symbol %q and the whole hunk", i, u.Symbol, u.Signature, wantSym)
				}
			}
		})
	}
}

func TestSplitSkipsBinaryAndCancelled(t *testing.T) {
	files, src := build(t, change{path: "store.go", old: fixture(t, "go/store.base.go"), new: fixture(t, "go/store.head.go")})
	bin := files[0]
	bin.Binary, bin.Hunks = true, nil
	units, err := Split(context.Background(), Input{Files: append(files, bin), Src: src, BaseSHA: baseRev, HeadSHA: headRev})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		if u.Path != "store.go" {
			t.Errorf("unexpected unit for %s", u.Path)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Split(ctx, Input{Files: files, Src: src}); err == nil {
		t.Error("Split on a cancelled context: want error")
	}
}

func TestDetectMoved(t *testing.T) {
	del := func(id, body string) model.ChangeUnit {
		n := strings.Count(body, "\n") + 1
		return model.ChangeUnit{ID: id, Kind: model.UnitDeleted, Dels: n, Diff: prefix("-", body)}
	}
	add := func(id, body string) model.ChangeUnit {
		n := strings.Count(body, "\n") + 1
		return model.ChangeUnit{ID: id, Kind: model.UnitAdded, Adds: n, Diff: prefix("+", body)}
	}
	block := "func f() {\n\treturn 1\n}"
	tests := []struct {
		name  string
		units []model.ChangeUnit
		kinds []model.UnitKind
		from  []string
	}{
		{"pair", []model.ChangeUnit{del("d", block), add("a", block)},
			[]model.UnitKind{model.UnitMoved, model.UnitMoved}, []string{"", "d"}},
		{"whitespace and blank lines ignored", []model.ChangeUnit{del("d", block), add("a", "\nfunc  f() {\n    return 1\n}\n")},
			[]model.UnitKind{model.UnitMoved, model.UnitMoved}, []string{"", "d"}},
		{"added before deleted", []model.ChangeUnit{add("a", block), del("d", block)},
			[]model.UnitKind{model.UnitMoved, model.UnitMoved}, []string{"d", ""}},
		{"too short", []model.ChangeUnit{del("d", "a()\nb()"), add("a", "a()\nb()")},
			[]model.UnitKind{model.UnitDeleted, model.UnitAdded}, []string{"", ""}},
		{"different", []model.ChangeUnit{del("d", block), add("a", "func f() {\n\treturn 2\n}")},
			[]model.UnitKind{model.UnitDeleted, model.UnitAdded}, []string{"", ""}},
		{"matched once", []model.ChangeUnit{del("d1", block), del("d2", block), add("a", block)},
			[]model.UnitKind{model.UnitMoved, model.UnitDeleted, model.UnitMoved}, []string{"", "", "d1"}},
		{"modified units ignored", []model.ChangeUnit{
			{ID: "m", Kind: model.UnitModified, Adds: 3, Dels: 3, Diff: prefix("-", block) + "\n" + prefix("+", block)},
			add("a", block)},
			[]model.UnitKind{model.UnitModified, model.UnitAdded}, []string{"", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := append([]model.ChangeUnit(nil), tt.units...)
			got := DetectMoved(tt.units)
			if !reflect.DeepEqual(in, tt.units) {
				t.Error("DetectMoved modified its input")
			}
			for i, u := range got {
				if u.Kind != tt.kinds[i] || u.MovedFrom != tt.from[i] {
					t.Errorf("unit %s: kind %s from %q, want %s from %q", u.ID, u.Kind, u.MovedFrom, tt.kinds[i], tt.from[i])
				}
			}
		})
	}
}

func prefix(op, body string) string {
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = op + lines[i]
	}
	return strings.Join(lines, "\n")
}

func TestScanLine(t *testing.T) {
	tests := []struct {
		lang, in string
		want     []token
	}{
		{"go", `x := f("a b", 10) // note`, []token{
			{tokIdent, "x"}, {tokOther, ":"}, {tokOther, "="}, {tokIdent, "f"}, {tokOther, "("},
			{tokString, `"a b"`}, {tokOther, ","}, {tokOther, "10"}, {tokOther, ")"}, {tokComment, "// note"}}},
		{"python", `y = 'it\'s' # c`, []token{
			{tokIdent, "y"}, {tokOther, "="}, {tokString, `'it\'s'`}, {tokComment, "# c"}}},
		{"", `"k": 1.2.3 # not a comment`, []token{
			{tokString, `"k"`}, {tokOther, ":"}, {tokOther, "1.2.3"}, {tokOther, "#"}, {tokIdent, "not"},
			{tokIdent, "a"}, {tokIdent, "comment"}}},
		{"ts", `a /* b */ c`, []token{{tokIdent, "a"}, {tokComment, "/* b */"}, {tokIdent, "c"}}},
	}
	for _, tt := range tests {
		if got := scanLine(tt.in, tt.lang); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("scanLine(%q, %q) = %v, want %v", tt.in, tt.lang, got, tt.want)
		}
	}
}
