package surface

import (
	"context"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// testUnit builds a unit from diff lines numbered from oldStart/newStart.
func testUnit(p, symbol, diffText string, oldStart, newStart int) model.ChangeUnit {
	u := model.ChangeUnit{Path: p, FileID: p, Symbol: symbol, Diff: diffText}
	u.Range.Old.Start, u.Range.New.Start = oldStart, newStart
	u.ID = model.UnitID(p, diffText)
	return u
}

// contract returns a contract label of unit id.
func contract(id, value string, p float64) model.Label {
	return model.Label{UnitID: id, Question: model.QContract, Value: value, P: p, Confidence: p, Model: "typesafe-ai/jev"}
}

func TestJevMerge(t *testing.T) {
	flagUnit := testUnit("web/src/a.ts", "load", " function load() {\n+  if (isEnabled(\"x-flag\")) {}\n }", 10, 10)
	dbUnit := testUnit("server/store.go", "Store.Migrate", " func (s *Store) Migrate() {\n+\ts.upgrade(3)\n }", 20, 20)
	sigUnit := testUnit("server/api.go", "", " func a() {\n+\tb()\n }", 5, 5)
	sigUnit.Signature = "func a()"
	delUnit := testUnit("server/old.go", "", "-func legacy() {}\n-", 40, 39)
	lowUnit := testUnit("server/low.go", "low", "+x := 1", 1, 1)
	testFile := testUnit("server/api_test.go", "TestA", "+\tcall()", 3, 3)
	fmtUnit := testUnit("server/fmt.go", "f", "-a  :=1\n+a := 1", 2, 2)
	fmtUnit.Format = true
	genUnit := testUnit("server/gen.pb.go", "Register", "+\tx := 1", 7, 7)
	in := Input{
		Units: []model.ChangeUnit{flagUnit, dbUnit, sigUnit, delUnit, lowUnit, testFile, fmtUnit, genUnit},
		Aggs:  []model.UnitAgg{{UnitID: genUnit.ID, Noise: true, NoiseReason: model.NoiseGenerated}, {UnitID: dbUnit.ID}},
		Labels: []model.Label{
			contract(genUnit.ID, "api shape", 0.9),
			contract(flagUnit.ID, "env/flag", 0.99),
			contract(dbUnit.ID, "schema/migration", 0.7),
			contract(sigUnit.ID, "api shape", 0.65),
			contract(delUnit.ID, "api shape", 0.8),
			contract(lowUnit.ID, "api shape", 0.59),
			contract(testFile.ID, "api shape", 0.9),
			contract(fmtUnit.ID, "env/flag", 0.9),
			{UnitID: dbUnit.ID, Question: model.QRisk, Value: "4", P: 0.9},
		},
	}
	cs := build(context.Background(), in)

	flag := find(t, cs, "added flag: x-flag")
	if flag.Source != model.SurfaceFromBoth || flag.Confidence != 0.99 {
		t.Errorf("flag = %+v, want code+jev at max(code, P) = .99", flag)
	}
	db := find(t, cs, "changed contract change: Store.Migrate")
	if db.Area != model.SurfaceDB || db.Source != model.SurfaceFromJev || db.Confidence != 0.7 ||
		db.Path != "server/store.go" || db.Line != 21 || db.Side != "" || db.UnitID != dbUnit.ID || db.Framework != "" {
		t.Errorf("db = %+v, want a Jev-only DB entry at store.go:21 with P .70", db)
	}
	if e := find(t, cs, "changed contract change: func a()"); e.Area != model.SurfaceAPI {
		t.Errorf("signature-named entry = %+v, want API", e)
	}
	del := find(t, cs, "changed contract change: server/old.go")
	if del.Side != "old" || del.Line != 40 {
		t.Errorf("deletion = %+v, want old side line 40", del)
	}
	if len(cs.API) != 2 || len(cs.DB) != 1 || len(cs.Flags) != 1 {
		t.Errorf("api/db/flags = %d/%d/%d, want 2/1/1 (low P, test file, formatting and noise units add nothing):\n%v",
			len(cs.API), len(cs.DB), len(cs.Flags), allEntries(cs))
	}
	if rows := Rows(cs.DB); rows[0].Text != "contract change: Store.Migrate" {
		t.Errorf("row text = %q", rows[0].Text)
	}
}

// TestJevMergeOtherArea checks a contract label confirms only entries of
// its own area, and adds its area's entry next to a code entry of another.
func TestJevMergeOtherArea(t *testing.T) {
	u := testUnit("server/main.go", "main", " func main() {\n+\taddr := os.Getenv(\"ADDR\")\n }", 1, 1)
	cs := build(context.Background(), Input{Units: []model.ChangeUnit{u}, Labels: []model.Label{contract(u.ID, "api shape", 0.8)}})
	if e := find(t, cs, "added env: ADDR"); e.Source != model.SurfaceFromCode {
		t.Errorf("env = %+v, want code only", e)
	}
	find(t, cs, "changed contract change: main")
}

// TestJevMergeNoLabels checks unknown and absent labels leave code
// detection alone.
func TestJevMergeNoLabels(t *testing.T) {
	u := testUnit("server/main.go", "main", " func main() {\n+\taddr := os.Getenv(\"ADDR\")\n }", 1, 1)
	unknown := model.Label{UnitID: u.ID, Question: model.QContract, Value: model.LabelUnknown}
	for _, labels := range [][]model.Label{nil, {unknown}} {
		cs := build(context.Background(), Input{Units: []model.ChangeUnit{u}, Labels: labels})
		assertEntries(t, cs, []string{"added env: ADDR"})
		if cs.Flags[0].Source != model.SurfaceFromCode {
			t.Errorf("source = %s, want code", cs.Flags[0].Source)
		}
	}
}
