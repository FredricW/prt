package link

import (
	"context"
	"errors"
	"math/rand"
	"reflect"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// tsScenario is a small TS repo exercising every structural link kind.
func tsScenario() (map[string]string, map[string]string, []model.ChangeUnit) {
	base := map[string]string{
		"src/api.ts":                  "export function fetchData(id: string): string {\n  return id;\n}\n",
		"src/view.ts":                 "import { fetchData } from \"./api\";\n\nexport function show(): string {\n  return fetchData(\"x\");\n}\n",
		"src/other.ts":                "export const z = 1;\n",
		"src/x.ts":                    "export function run(): number {\n  return 1;\n}\n",
		"src/y.ts":                    "export function run(): number {\n  return 3;\n}\n",
		"src/z.ts":                    "export function go(): number {\n  return 0;\n}\n",
		"src/w.ts":                    "import { run } from \"./x\";\n\nexport function use(): number {\n  return 0;\n}\n",
		"src/api.test.ts":             "import { fetchData } from \"./api\";\n\ntest(\"a\", () => fetchData(\"a\"));\n",
		"src/__tests__/misc.test.ts":  "export {};\n",
		"lib/old.ts":                  "export function shared(): number {\n  return  42;\n}\nexport const keep = 1;\n",
		"README.md":                   "# readme\n",
		"src/broken.ts":               "export function (((\n",
		"src/unrelated/deep/thing.ts": "export function thing(): void {}\n",
	}
	head := map[string]string{
		"src/api.ts":                 "export function fetchData(id: string, n: number): string {\n  return id + n;\n}\n",
		"src/view.ts":                "import { fetchData } from \"./api\";\n\nexport function show(): string {\n  return fetchData(\"x\", 1);\n}\n",
		"src/other.ts":               "import { fetchData } from \"./api\";\nexport const z = 1;\n",
		"src/x.ts":                   "export function run(): number {\n  return 2;\n}\n",
		"src/y.ts":                   "export function run(): number {\n  return 4;\n}\n",
		"src/z.ts":                   "export function go(): number {\n  return run();\n}\n",
		"src/w.ts":                   "import { run } from \"./x\";\n\nexport function use(): number {\n  return run();\n}\n",
		"src/api.test.ts":            "import { fetchData } from \"./api\";\n\ntest(\"b\", () => fetchData(\"b\"));\n",
		"src/__tests__/misc.test.ts": "export {};\nshow();\ngo();\n",
		"lib/old.ts":                 "export const keep = 1;\n",
		"lib/new.ts":                 "function shared(): number {\n  return 42;\n}\n",
	}
	units := []model.ChangeUnit{
		mk("api", 0, "src/api.ts", "fetchData", 1, 1,
			"-export function fetchData(id: string): string {", "-  return id;",
			"+export function fetchData(id: string, n: number): string {", "+  return id + n;", " }"),
		mk("view", 1, "src/view.ts", "show", 3, 3,
			" export function show(): string {", "-  return fetchData(\"x\");", "+  return fetchData(\"x\", 1);", " }"),
		mk("other", 2, "src/other.ts", "", 1, 1, "+import { fetchData } from \"./api\";", " export const z = 1;"),
		mk("x", 3, "src/x.ts", "run", 1, 1, " export function run(): number {", "-  return 1;", "+  return 2;", " }"),
		mk("y", 4, "src/y.ts", "run", 1, 1, " export function run(): number {", "-  return 3;", "+  return 4;", " }"),
		mk("z", 5, "src/z.ts", "go", 1, 1, " export function go(): number {", "-  return 0;", "+  return run();", " }"),
		mk("w", 6, "src/w.ts", "use", 3, 3, " export function use(): number {", "-  return 0;", "+  return run();", " }"),
		mk("apitest", 7, "src/api.test.ts", "", 3, 3,
			"-test(\"a\", () => fetchData(\"a\"));", "+test(\"b\", () => fetchData(\"b\"));"),
		mk("misc", 8, "src/__tests__/misc.test.ts", "", 1, 1, " export {};", "+show();", "+go();"),
		mk("old", 9, "lib/old.ts", "shared", 1, 1,
			"-export function shared(): number {", "-  return  42;", "-}", " export const keep = 1;"),
		mk("new", 10, "lib/new.ts", "shared", 0, 1, "+function shared(): number {", "+  return 42;", "+}"),
		mk("mvfrom", 11, "docs/a.md", "", 1, 0, "-one", "-two", "-three"),
		mk("mvto", 12, "docs/b.md", "", 0, 1, "+one", "+two", "+three"),
		mk("s1", 13, "go/a.go", "Store.Get", 10, 10, "-a", "+b"),
		mk("s2", 14, "go/b.go", "Store.Get", 10, 10, "-a", "+b"),
		mk("s3", 15, "go/c.go", "Store.Get", 10, 10, "-a", "+b"),
		mk("s4", 16, "go/c.go", "Store.Get", 40, 40, "-a", "+b"),
		mk("h1", 17, "go/d.go", "Put", 1, 1, "-a", "+b"),
		mk("h2", 18, "go/d.go", "Put", 2, 2, "-c", "+d"),
	}
	units[12].MovedFrom = "mvfrom"
	units[11].Kind, units[12].Kind = model.UnitMoved, model.UnitMoved
	return base, head, units
}

func scenarioFiles() []model.File {
	return []model.File{
		{Path: "lib/old.ts", OldPath: "lib/old.ts"},
		{Path: "lib/new.ts", Added: true},
		{Path: "docs/a.md", OldPath: "docs/a.md", Deleted: true},
		{Path: "docs/b.md", Added: true},
	}
}

func TestStructuralScenario(t *testing.T) {
	ctx := context.Background()
	base, head, units := tsScenario()
	full := []string{
		"calls view->api",
		"imports other->api",
		"same-symbol x->y",
		"calls w->x",
		"calls apitest->api",
		"tests apitest->api",
		"calls misc->view",
		"tests misc->view",
		"calls misc->z",
		"moved old->new",
		"same-symbol old->new",
		"moved mvfrom->mvto",
		"same-symbol s1->s2",
		"same-symbol s1->s3",
		"same-symbol s1->s4",
	}
	noSrc := []string{
		"same-symbol x->y",
		"tests apitest->api",
		"tests misc->view",
		"same-symbol old->new",
		"moved mvfrom->mvto",
		"same-symbol s1->s2",
		"same-symbol s1->s3",
		"same-symbol s1->s4",
	}
	tests := []struct {
		name string
		src  Source
		want []string
	}{
		{"with source", fakeRepo(base, head), full},
		{"nil source", nil, noSrc},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{ItemID: "pr:1", BaseSHA: "base", HeadSHA: "head", Files: scenarioFiles(), Units: units, Src: tc.src}
			links, err := Structural(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			checkLinkInvariants(t, units, links)
			if got := describeLinks(links); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("links:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestStructuralDeterministic(t *testing.T) {
	ctx := context.Background()
	base, head, units := tsScenario()
	in := Input{BaseSHA: "base", HeadSHA: "head", Files: scenarioFiles(), Units: units, Src: fakeRepo(base, head)}
	want, err := Structural(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 5; i++ {
		shuffled := append([]model.ChangeUnit(nil), units...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		in.Units = shuffled
		got, err := Structural(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffle %d: links differ:\n got %q\nwant %q", i, describeLinks(got), describeLinks(want))
		}
	}
}

func TestStructuralCancelled(t *testing.T) {
	base, head, units := tsScenario()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, src := range []Source{nil, fakeRepo(base, head)} {
		_, err := Structural(ctx, Input{BaseSHA: "base", HeadSHA: "head", Units: units, Src: src})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("src %v: err = %v, want context.Canceled", src != nil, err)
		}
	}
}

func TestStructuralIndexCache(t *testing.T) {
	ctx := context.Background()
	base, head, units := tsScenario()
	src := &countingSource{Source: fakeRepo(base, head)}
	cache := &mapCache{}
	in := Input{BaseSHA: "base", HeadSHA: "head", Files: scenarioFiles(), Units: units, Src: src, Cache: cache}
	first, err := Structural(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if src.lists != 1 || cache.puts != 1 {
		t.Fatalf("first run: ListFiles %d, puts %d; want 1, 1", src.lists, cache.puts)
	}
	if _, ok := cache.m[indexNS+"/base:"+indexVersion]; !ok {
		t.Fatalf("index not cached under %s/base:%s; keys %v", indexNS, indexVersion, cache.m)
	}
	second, err := Structural(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if src.lists != 1 {
		t.Errorf("second run called ListFiles again (%d calls)", src.lists)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("cached run differs:\n got %q\nwant %q", describeLinks(second), describeLinks(first))
	}
}

// failingSource fails every call.
type failingSource struct{}

func (failingSource) Show(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("boom")
}

func (failingSource) ListFiles(context.Context, string) ([]string, error) {
	return nil, errors.New("boom")
}

func TestStructuralSourceErrorsDegrade(t *testing.T) {
	ctx := context.Background()
	_, _, units := tsScenario()
	cache := &mapCache{}
	in := Input{BaseSHA: "base", HeadSHA: "head", Files: scenarioFiles(), Units: units, Src: failingSource{}, Cache: cache}
	links, err := Structural(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	noSrc, err := Structural(ctx, Input{Files: scenarioFiles(), Units: units})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(links, noSrc) {
		t.Errorf("failing source: got %q, want the source-free links %q", describeLinks(links), describeLinks(noSrc))
	}
	if cache.puts != 0 {
		t.Errorf("a failed listing was cached")
	}
}

// showFailSource lists files but fails every Show.
type showFailSource struct{ Source }

func (showFailSource) Show(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("transient")
}

// A file read failing while the index is built must not cache that
// partial index for the base SHA.
func TestStructuralShowErrorNotCached(t *testing.T) {
	base, head, units := tsScenario()
	cache := &mapCache{}
	in := Input{BaseSHA: "base", HeadSHA: "head", Files: scenarioFiles(), Units: units, Src: showFailSource{fakeRepo(base, head)}, Cache: cache}
	if _, err := Structural(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if cache.puts != 0 {
		t.Errorf("an index with failed file reads was cached")
	}
}

func TestStructuralLanguages(t *testing.T) {
	ctx := context.Background()
	base := map[string]string{
		"server/users/users.go":      "package users\n\nfunc Get() int {\n\treturn 1\n}\n",
		"server/users/users_test.go": "package users\n\nimport \"testing\"\n\nfunc TestGet(t *testing.T) {\n\t_ = 1\n}\n",
		"server/api/api.go":          "package api\n\nfunc Handle() int {\n\treturn 0\n}\n",
		"scripts/report.py":          "def build():\n    return 1\n",
		"scripts/run.py":             "from report import build\n\n\ndef main():\n    return 0\n",
		"scripts/test_report.py":     "import report\n\n\ndef test_build():\n    assert True\n",
	}
	head := map[string]string{
		"server/users/users.go":      "package users\n\nfunc Get() int {\n\treturn 2\n}\n",
		"server/users/users_test.go": "package users\n\nimport \"testing\"\n\nfunc TestGet(t *testing.T) {\n\t_ = Get()\n}\n",
		"server/api/api.go":          "package api\n\nimport \"example.com/app/server/users\"\n\nfunc Handle() int {\n\treturn users.Get()\n}\n",
		"scripts/report.py":          "def build():\n    return 2\n",
		"scripts/run.py":             "from report import build\n\n\ndef main():\n    return build()\n",
		"scripts/test_report.py":     "import report\n\n\ndef test_build():\n    assert report.build()\n",
	}
	units := []model.ChangeUnit{
		mk("gu", 0, "server/users/users.go", "Get", 3, 3, " func Get() int {", "-\treturn 1", "+\treturn 2", " }"),
		mk("gt", 1, "server/users/users_test.go", "TestGet", 5, 5, " func TestGet(t *testing.T) {", "-\t_ = 1", "+\t_ = Get()", " }"),
		mk("gi", 2, "server/api/api.go", "", 2, 2, " ", "+import \"example.com/app/server/users\"", "+"),
		mk("ga", 3, "server/api/api.go", "Handle", 3, 5, " func Handle() int {", "-\treturn 0", "+\treturn users.Get()", " }"),
		mk("pr", 4, "scripts/report.py", "build", 1, 1, " def build():", "-    return 1", "+    return 2"),
		mk("pm", 5, "scripts/run.py", "main", 4, 4, " def main():", "-    return 0", "+    return build()"),
		mk("pt", 6, "scripts/test_report.py", "test_build", 4, 4, " def test_build():", "-    assert True", "+    assert report.build()"),
	}
	links, err := Structural(ctx, Input{BaseSHA: "base", HeadSHA: "head", Units: units, Src: fakeRepo(base, head)})
	if err != nil {
		t.Fatal(err)
	}
	checkLinkInvariants(t, units, links)
	want := []string{
		"calls gt->gu",
		"tests gt->gu",
		"imports gi->gu",
		"calls ga->gu",
		"calls pm->pr",
		"calls pt->pr",
		"tests pt->pr",
	}
	if got := describeLinks(links); !reflect.DeepEqual(got, want) {
		t.Errorf("links:\n got %q\nwant %q", got, want)
	}
}

func TestTestStemAndScope(t *testing.T) {
	tests := []struct {
		test, code string
		want       bool
	}{
		{"web/src/__tests__/client.test.ts", "web/src/api/client.ts", true},
		{"web/src/__tests__/useProfileCache.test.ts", "web/src/hooks/useProfileCache.ts", true},
		{"web/src/__tests__/client.test.ts", "server/client.ts", false},
		{"web/src/api/client.spec.tsx", "web/src/api/client.tsx", true},
		{"web/src/api/client.test.ts", "web/src/other/client.ts", false},
		{"server/users/users_test.go", "server/users/users.go", true},
		{"server/users/users_test.go", "server/billing/users.go", false},
		{"scripts/test_report.py", "scripts/report.py", true},
		{"scripts/report_test.py", "scripts/report.py", true},
		{"tests/test_report.py", "src/pkg/report.py", true},
	}
	for _, tc := range tests {
		dir, deep := testScope(tc.test)
		got := isTestPath(tc.test) && testStem(tc.test) == stem(tc.code) && inScope(tc.code, dir, deep)
		if got != tc.want {
			t.Errorf("%s ↔ %s: %v, want %v", tc.test, tc.code, got, tc.want)
		}
	}
}

func TestNormaliseDecl(t *testing.T) {
	a := normaliseDecl("export class A {\n\n  x  =  1;\n}")
	b := normaliseDecl("class A {\n  x = 1;\n}\n")
	c := normaliseDecl("export default class A {\n  x = 1;\n}")
	if a != b || b != c {
		t.Errorf("normalised texts differ: %q %q %q", a, b, c)
	}
}
