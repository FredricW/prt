package quality

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/fredricw/prt/internal/testutil"
)

// thresholds is internal/quality/testdata/thresholds.toml.
type thresholds struct {
	Tolerance float64                       `toml:"tolerance"`
	Items     map[string]map[string]float64 `toml:"items"`
}

// headlinesKey is the thresholds key of the headline row count.
const headlinesKey = "headlines"

func loadThresholds(t *testing.T) thresholds {
	t.Helper()
	var th thresholds
	md, err := toml.DecodeFile(filepath.Join("testdata", "thresholds.toml"), &th)
	if err != nil {
		t.Fatal(err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		t.Fatalf("thresholds.toml: unknown keys %v", und)
	}
	return th
}

// TestScoreboardRegression runs the scoreboard offline from the committed
// recordings (strictly: every Jev, embedding and review request must be
// recorded) and checks every item against the committed thresholds.
func TestScoreboardRegression(t *testing.T) {
	truths, err := LoadDir(testutil.Testdata(t, "groundtruth"))
	if err != nil {
		t.Fatal(err)
	}
	env := Env{RepoDir: testutil.FixtureRepo(t), PRsJSON: testutil.FixturePRs(t), Recordings: testutil.Testdata(t, "recordings")}
	board, err := Evaluate(context.Background(), env, truths)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + board.Markdown())
	th := loadThresholds(t)
	for _, tr := range truths {
		if _, ok := th.Items[tr.Item]; !ok {
			t.Errorf("thresholds.toml has no [items.%q]", tr.Item)
		}
	}
	items := make([]string, 0, len(th.Items))
	for item := range th.Items {
		items = append(items, item)
	}
	sort.Strings(items)
	for _, item := range items {
		row, ok := board.Row(item)
		if !ok {
			t.Errorf("%s: in thresholds.toml but not on the board", item)
			continue
		}
		for _, p := range checkRow(row, th.Items[item], th.Tolerance) {
			t.Errorf("%s: %s", item, p)
		}
	}
}

// checkRow returns how a row falls short of its thresholds.
func checkRow(row Row, bounds map[string]float64, tol float64) []string {
	keys := make([]string, 0, len(bounds))
	for k := range bounds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		want := bounds[k]
		if k == headlinesKey {
			if float64(row.Headlines) > want {
				out = append(out, fmt.Sprintf("%d headline rows, want at most %.0f", row.Headlines, want))
			}
			continue
		}
		info, ok := Info(k)
		if !ok {
			out = append(out, "unknown metric "+k)
			continue
		}
		r := row.Ratios[k]
		switch {
		case !r.Applies():
			out = append(out, fmt.Sprintf("%s does not apply any more (threshold %.2f)", k, want))
		case info.Higher && r.Value() < want-tol:
			out = append(out, fmt.Sprintf("%s = %s, want at least %.2f", k, RatioCell(r), want-tol))
		case !info.Higher && r.Value() > want+tol:
			out = append(out, fmt.Sprintf("%s = %s, want at most %.2f", k, RatioCell(r), want+tol))
		}
	}
	return out
}

func TestCheckRow(t *testing.T) {
	row := Row{Headlines: 4, Ratios: map[string]Ratio{MEyesRecall: {2, 3}, MLinesToRead: {1, 2}, MFidelity: {}}}
	got := checkRow(row, map[string]float64{headlinesKey: 3, MEyesRecall: 0.7, MLinesToRead: 0.45, MFidelity: 1, "bogus": 1}, 0.02)
	want := []string{
		"unknown metric bogus",
		"eyes_recall = 2/3 (0.67), want at least 0.68",
		"fidelity does not apply any more (threshold 1.00)",
		"4 headline rows, want at most 3",
		"lines_to_read = 1/2 (0.50), want at most 0.47",
	}
	if len(got) != len(want) {
		t.Fatalf("problems = %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("problem %d = %q, want %q", i, got[i], want[i])
		}
	}
	if p := checkRow(row, map[string]float64{MEyesRecall: 0.68}, 0.02); len(p) != 0 {
		t.Errorf("within tolerance: %q", p)
	}
}
