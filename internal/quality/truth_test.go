package quality

import (
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/testutil"
)

// minimal is the smallest valid ground-truth file.
const minimal = `
item = "pr:7"
title = "t"
patch = "p"
written_blind = true

[summary]
shape = "mixed"
risk_band = "LOW"

[lines_that_matter]
total_changed = 10
must_read = 4
tolerance = 0.5

[fidelity]
applicable = false
`

func TestParseMinimal(t *testing.T) {
	tr, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Item != "pr:7" || tr.LinesThatMatter.MustRead != 4 || tr.Branch != nil {
		t.Errorf("parsed %+v", tr)
	}
}

func TestParseOptionalFields(t *testing.T) {
	src := minimal + `
[[needs_eyes]]
path = "a.go"
lines = [3, 9]
side = "old"
also_at = ["b.go:1-2", "c.go:5"]
depth = "read"
certainty = "firm"
reason = "r"

[[hot_spots]]
rank = 1
path = "a.go"
lines = [3, 3]
risk = 3
fail_mode = "crash"
fail_mode_ok = ["crash", "wrong data"]
risk_tags = ["async/race"]
certainty = "soft"

[[coverage]]
path = "a.go"
lines = [1, 2]
covered = true
by = "x_test.go"

[[comments]]
thread = "T1"
path = "a.go"
line = 4
addressed = false
certainty = "firm"
why = "w"
`
	src = strings.Replace(src, `risk_band = "LOW"`, "risk_band = \"LOW\"\nbreaking_ok = [0, 1]\nneeds_eyes_max = 2", 1)
	src = strings.Replace(src, "applicable = false", "applicable = true\nundocumented_firm = 0", 1)
	tr, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	e := tr.NeedsEyes[0]
	if e.Side != SideOld || len(e.AlsoAt) != 2 || *tr.Summary.NeedsEyesMax != 2 || len(tr.Summary.BreakingOK) != 2 {
		t.Errorf("needs_eyes %+v summary %+v", e, tr.Summary)
	}
	if tr.HotSpots[0].FailModeOK[1] != "wrong data" || !tr.Coverage[0].Covered || tr.Comments[0].Thread != "T1" || *tr.Fidelity.UndocumentedFirm != 0 {
		t.Errorf("parsed %+v", tr)
	}
	rg := regionOf(e.Loc)
	if rg.side != SideOld || len(rg.spans) != 3 || rg.spans[2] != (span{path: "c.go", first: 5, last: 5}) {
		t.Errorf("region %+v", rg)
	}
}

func TestParseBranch(t *testing.T) {
	src := strings.Replace(minimal, `item = "pr:7"`, `item = "branch:spike"`, 1) + `
[branch]
state = "spike"
state_ok = ["spike", "WIP"]
evidence = "e"
suggested_title_like = "x"
`
	tr, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Branch == nil || tr.Branch.State != "spike" || len(tr.Branch.StateOK) != 2 {
		t.Errorf("branch %+v", tr.Branch)
	}
}

func TestParseRejects(t *testing.T) {
	for name, tc := range map[string]struct{ old, new, want string }{
		"unknown top key":     {`title = "t"`, "title = \"t\"\nfoo = 1", "unknown keys: foo"},
		"unknown nested key":  {`must_read = 4`, "must_read = 4\nmust_raed = 3", "lines_that_matter.must_raed"},
		"bad item":            {`item = "pr:7"`, `item = "PR 7"`, "item"},
		"bad shape":           {`shape = "mixed"`, `shape = "messy"`, "summary.shape"},
		"missing risk band":   {`risk_band = "LOW"`, ``, "summary.risk_band"},
		"branch table on pr":  {`[fidelity]`, "[branch]\nstate = \"spike\"\n\n[fidelity]", "[branch] on non-branch item"},
		"unknown entry key":   {`[fidelity]`, "[[needs_eyes]]\npath = \"a\"\nlines = [1, 2]\ncertainty = \"firm\"\nlevel = 3\n\n[fidelity]", "needs_eyes.level"},
		"reversed lines":      {`[fidelity]`, "[[needs_eyes]]\npath = \"a\"\nlines = [5, 2]\ncertainty = \"firm\"\n\n[fidelity]", "lines [5 2]"},
		"zero line":           {`[fidelity]`, "[[needs_eyes]]\npath = \"a\"\nlines = [0, 2]\ncertainty = \"firm\"\n\n[fidelity]", "lines [0 2]"},
		"bad certainty":       {`[fidelity]`, "[[headlines]]\npath = \"a\"\nlines = [1, 2]\ncertainty = \"maybe\"\n\n[fidelity]", "certainty \"maybe\""},
		"bad also_at":         {`[fidelity]`, "[[headlines]]\npath = \"a\"\nlines = [1, 2]\ncertainty = \"firm\"\nalso_at = [\"a.go\"]\n\n[fidelity]", "also_at"},
		"bad side":            {`[fidelity]`, "[[not_headlines]]\npath = \"a\"\nlines = [1, 2]\nside = \"left\"\n\n[fidelity]", "side \"left\""},
		"bad contract":        {`[fidelity]`, "[[headlines]]\npath = \"a\"\nlines = [1, 2]\ncertainty = \"firm\"\ncontract = \"abi\"\n\n[fidelity]", "contract"},
		"bad rank":            {`[fidelity]`, "[[hot_spots]]\npath = \"a\"\nlines = [1, 2]\ncertainty = \"firm\"\nrank = 0\n\n[fidelity]", "rank 0"},
		"bad risk tag":        {`[fidelity]`, "[[hot_spots]]\nrank = 1\npath = \"a\"\nlines = [1, 2]\ncertainty = \"firm\"\nrisk_tags = [\"async\"]\n\n[fidelity]", "risk_tags"},
		"bad bullet status":   {"applicable = false", "applicable = true\nbullets_total = 1\n\n[[fidelity.bullets]]\ntext = \"x\"\nstatus = \"done\"", "status"},
		"bullets_total wrong": {"applicable = false", "applicable = true\nbullets_total = 2\n\n[[fidelity.bullets]]\ntext = \"x\"\nstatus = \"implemented\"", "bullets_total"},
		"bad file kind":       {`[fidelity]`, "[[files]]\npath = \"a\"\nkind = \"misc\"\n\n[fidelity]", "files[0].kind"},
		"bad toml":            {`title = "t"`, `title = `, "decode"},
	} {
		t.Run(name, func(t *testing.T) {
			src := strings.Replace(minimal, tc.old, tc.new, 1)
			_, err := Parse([]byte(src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestRealGroundTruthParses parses every committed ground-truth file
// strictly and checks the README table lists each of them.
func TestRealGroundTruthParses(t *testing.T) {
	dir := testutil.Testdata(t, "groundtruth")
	truths, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	readme := string(testutil.ReadTestdata(t, "groundtruth", "README.md"))
	for i, tr := range truths {
		if !tr.WrittenBlind {
			t.Errorf("%s: written_blind is not true", tr.Item)
		}
		if i > 0 && !itemLess(truths[i-1].Item, tr.Item) {
			t.Errorf("order: %s before %s", truths[i-1].Item, tr.Item)
		}
		if base := tr.path[len(dir)+1:]; !strings.Contains(readme, "`"+base+"`") {
			t.Errorf("README table does not list %s", base)
		}
	}
	if len(truths) < 5 {
		t.Errorf("%d ground-truth files, want at least 5", len(truths))
	}
}

func TestItemLess(t *testing.T) {
	ids := []string{"branch:b", "pr:106", "branch:a", "pr:99", "pr:101"}
	ts := make([]Truth, len(ids))
	for i, id := range ids {
		ts[i].Item = id
	}
	SortTruths(ts)
	var got []string
	for _, tr := range ts {
		got = append(got, tr.Item)
	}
	if strings.Join(got, " ") != "pr:99 pr:101 pr:106 branch:a branch:b" {
		t.Errorf("order = %v", got)
	}
}

func TestLoadDirErrors(t *testing.T) {
	if _, err := LoadDir(t.TempDir()); err == nil {
		t.Error("empty dir: want error")
	}
}
