package surface

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestBuildEmptyIsNonNil(t *testing.T) {
	for _, in := range []Input{{}, {Units: []model.ChangeUnit{{}}}, {Files: []model.File{{Path: "x.go"}}}} {
		cs := Build(context.Background(), in)
		if cs == nil || cs.API == nil || cs.DB == nil || cs.Flags == nil {
			t.Fatalf("Build(%+v) = %+v, want non-nil lists", in, cs)
		}
		data, err := json.Marshal(cs)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"api":[]`) || !strings.Contains(string(data), `"db":[]`) || !strings.Contains(string(data), `"flags":[]`) {
			t.Errorf("json = %s, want empty lists, not null", data)
		}
	}
}

func TestFinishSortsAndDedups(t *testing.T) {
	es := []model.SurfaceEntry{
		{Change: model.SurfaceAdded, What: whatRoute, Name: "GET /b", Path: "b.go", Line: 1, Confidence: 0.9},
		{Change: model.SurfaceAdded, What: whatRoute, Name: "GET /a", Path: "a.go", Line: 9, Confidence: 0.97},
		{Change: model.SurfaceChanged, What: whatRoute, Name: "GET /c", Path: "c.go", Line: 1, Confidence: 0.5},
		{Change: model.SurfaceRemoved, What: whatRoute, Name: "GET /d", Path: "d.go", Line: 1, Confidence: 0.3},
		{Change: model.SurfaceAdded, What: whatRoute, Name: "GET /e", Path: "a.go", Line: 2, Confidence: 0.9},
		// Duplicates of "GET /a": the most confident stays, the others are named.
		{Change: model.SurfaceAdded, What: whatRoute, Name: "GET /a", Path: "z.go", Line: 3, Confidence: 0.8},
		{Change: model.SurfaceAdded, What: whatRoute, Name: "GET /a", Path: "y.go", Line: 4, Confidence: 0.8},
		{Change: model.SurfaceAdded, What: whatRoute, Name: "GET /a", Path: "a.go", Line: 9, Confidence: 0.6},
		// Same name, other change: kept apart.
		{Change: model.SurfaceRemoved, What: whatRoute, Name: "GET /a", Path: "a.go", Line: 9, Confidence: 0.97, Side: "old"},
		{Change: model.SurfaceAdded, What: whatEnv, Name: "X", Path: "a.go", Line: 1, Confidence: 0.333333},
	}
	got := finish(es)
	var names []string
	for _, e := range got {
		names = append(names, e.Change+" "+e.Name)
	}
	want := []string{"removed GET /a", "removed GET /d", "changed GET /c", "added GET /a", "added GET /e", "added GET /b", "added X"}
	if !slices.Equal(names, want) {
		t.Errorf("order = %q\nwant  %q", names, want)
	}
	if got[3].Path != "a.go" || got[3].Detail != "also at y.go:4 and 1 more" {
		t.Errorf("dedup kept %+v, want a.go:9 with the other places", got[3])
	}
	if got[6].Confidence != 0.33 {
		t.Errorf("confidence = %v, want rounded to .33", got[6].Confidence)
	}
	if finish(nil) == nil {
		t.Error("finish(nil) = nil, want empty")
	}
}

func TestOtherPlaces(t *testing.T) {
	best := model.SurfaceEntry{Path: "a.go", Line: 1}
	if got := otherPlaces(best, []model.SurfaceEntry{{Path: "a.go", Line: 1}}); got != "" {
		t.Errorf("same place: %q, want none", got)
	}
	if got := otherPlaces(best, []model.SurfaceEntry{{Path: "b.go", Line: 2}}); got != "also at b.go:2" {
		t.Errorf("one other: %q", got)
	}
}

// TestDeterministic checks the result does not depend on the order of the
// units and files, nor on the run.
func TestDeterministic(t *testing.T) {
	in := fixtureInput(t, "06-user-preferences-api.patch")
	first := build(context.Background(), in)
	rev := in
	rev.Units, rev.Files = slices.Clone(in.Units), slices.Clone(in.Files)
	slices.Reverse(rev.Units)
	slices.Reverse(rev.Files)
	for range 3 {
		if got := build(context.Background(), rev); !reflect.DeepEqual(got, first) {
			t.Fatalf("reordered input gave\n%v\nwant\n%v", allEntries(got), allEntries(first))
		}
	}
}

// TestTestFilesExcluded checks units in test files and fixtures add nothing,
// by path or by the file's role.
func TestTestFilesExcluded(t *testing.T) {
	text := `diff --git a/server/api_test.go b/server/api_test.go
--- a/server/api_test.go
+++ b/server/api_test.go
@@ -1,2 +1,3 @@ func TestAPI(t *testing.T) {
 	mux := http.NewServeMux()
+	mux.HandleFunc("GET /fake", fake)
diff --git a/web/src/__tests__/flags.test.ts b/web/src/__tests__/flags.test.ts
--- a/web/src/__tests__/flags.test.ts
+++ b/web/src/__tests__/flags.test.ts
@@ -1,1 +1,2 @@
 it("x", () => {
+  expect(isEnabled("test-only")).toBe(true);
diff --git a/testdata/migrations/0001.sql b/testdata/migrations/0001.sql
--- a/testdata/migrations/0001.sql
+++ b/testdata/migrations/0001.sql
@@ -1,1 +1,2 @@
 -- fixture
+CREATE TABLE fixture_only (id INT);
diff --git a/server/helpers.go b/server/helpers.go
--- a/server/helpers.go
+++ b/server/helpers.go
@@ -1,1 +1,2 @@
 func helper() {
+	addr := os.Getenv("ROLE_TEST")
`
	in := patchInput(t, text)
	for i := range in.Files {
		if in.Files[i].Path == "server/helpers.go" {
			in.Files[i].Role = model.RoleTest
		}
	}
	for _, u := range in.Units {
		in.Labels = append(in.Labels, contract(u.ID, "api shape", 0.95))
	}
	assertEntries(t, build(context.Background(), in), nil)
}

func TestBounds(t *testing.T) {
	var b strings.Builder
	b.WriteString(" func main() {\n")
	for range maxUnitLines + 10 {
		b.WriteString("+\tx++\n")
	}
	b.WriteString("+\taddr := os.Getenv(\"BEYOND_THE_CAP\")\n")
	big := testUnit("main.go", "main", b.String(), 1, 1)
	minified := testUnit("web/app.min.js", "", "+"+strings.Repeat("a;", maxLineLen)+`app.get("/hidden", h);`, 1, 1)
	cs := build(context.Background(), Input{Units: []model.ChangeUnit{big, minified}})
	assertEntries(t, cs, nil)
	if !strings.Contains(cs.Note, "large diff") {
		t.Errorf("note = %q, want the cap mentioned", cs.Note)
	}
}

func TestCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cs := Build(ctx, fixtureInput(t, "06-user-preferences-api.patch"))
	if cs.API == nil || cs.DB == nil || cs.Flags == nil || !strings.Contains(cs.Note, "stopped") {
		t.Errorf("cancelled build = %+v, want non-nil lists and a note", cs)
	}
}

// panicSource panics on every read, to exercise Build's guard.
type panicSource struct{}

func (panicSource) Show(context.Context, string, string) ([]byte, error) { panic("boom") }

// errSource fails every read.
type errSource struct{}

func (errSource) Show(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("not readable")
}

func TestBuildNeverPanics(t *testing.T) {
	in := fixtureInput(t, "06-user-preferences-api.patch")
	in.Src = panicSource{}
	cs := Build(context.Background(), in)
	if cs == nil || cs.API == nil || cs.Note == "" {
		t.Errorf("Build with a panicking source = %+v, want an empty surface with a note", cs)
	}
}

func TestUnreadableSource(t *testing.T) {
	in := fixtureInput(t, "06-user-preferences-api.patch")
	in.Src = errSource{}
	cs := build(context.Background(), in)
	find(t, cs, "added route: GET /api/v1/users/{id}/preferences")
	if !strings.Contains(cs.Note, "not readable") {
		t.Errorf("note = %q, want unreadable files mentioned", cs.Note)
	}
}

// TestGarbage feeds malformed units through Build: nothing may panic and
// the result stays well-formed.
func TestGarbage(t *testing.T) {
	diffs := []string{
		"", "\x00\xff\xfe", "@@", "@@ -1 +1 @@\n+", "\\ No newline", "+\n-\n \n+\n",
		"+\"unterminated", "+`", "+/*", "+'''", `+r.Get("`, "+app.get('", "+CREATE TABLE", "+ALTER TABLE x\n+ADD",
		"+migrations.AddField(", "+op.add_column(", "+\t)\n+)\n+(", "+@app.route(", "+process.env.", "+os.Getenv(\"",
		"+" + strings.Repeat("(", 5000), strings.Repeat("+r.Get(\"/x\", h)\n", 50),
	}
	paths := []string{"", "/", "x.go", "a/b.ts", "c.py", "m/migrations/0001.py", "db/schema.sql", "s.prisma", ".env",
		"app/api/route.ts", "pages/api/x.js", "u/urls.py", "db/migrate/1.rb", "settings.py", "models.py", "ünï/cødé.go"}
	src := mapSource{revBase: {}, revHead: {}}
	for _, p := range paths {
		src[revBase][p], src[revHead][p] = "garbage\n\x00\n(((", ")))\n}{\n"
	}
	for _, p := range paths {
		for i, d := range diffs {
			for _, s := range []Source{nil, src} {
				u := testUnit(p, "", d, i-3, -i)
				cs := build(context.Background(), Input{Units: []model.ChangeUnit{u, u}, Src: s, BaseSHA: revBase, HeadSHA: revHead,
					Labels: []model.Label{contract(u.ID, "api shape", 0.9)}})
				checkWellFormed(t, cs)
			}
		}
	}
}

// checkWellFormed asserts the contract's invariants on a result.
func checkWellFormed(t *testing.T, cs *model.ChangeSurface) {
	t.Helper()
	if cs == nil || cs.API == nil || cs.DB == nil || cs.Flags == nil {
		t.Fatalf("surface = %+v, want non-nil lists", cs)
	}
	for _, es := range [][]model.SurfaceEntry{cs.API, cs.DB, cs.Flags} {
		if !slices.IsSortedFunc(es, compareImportance) {
			t.Fatalf("entries not sorted: %v", es)
		}
		for _, e := range es {
			if e.Confidence < 0 || e.Confidence > 1 || e.Name == "" || e.Change == "" {
				t.Fatalf("malformed entry %+v", e)
			}
		}
	}
}
