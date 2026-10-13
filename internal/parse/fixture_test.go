package parse

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/testutil"
)

// fixturePR is the subset of testdata/fixtures/prs.json this test reads.
type fixturePR struct {
	Number  int    `json:"number"`
	HeadRef string `json:"headRef"`
	BaseRef string `json:"baseRef"`
}

func TestSplitFixturePRs(t *testing.T) {
	ctx := context.Background()
	var doc struct {
		PRs []fixturePR `json:"prs"`
	}
	if err := json.Unmarshal(testutil.ReadTestdata(t, "fixtures", "prs.json"), &doc); err != nil {
		t.Fatal(err)
	}
	repo := testutil.FixtureRepo(t)
	g, err := gitx.Open(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[int]func(*testing.T, []model.ChangeUnit){
		101: check101, 102: check102, 103: check103, 104: check104,
	}
	for _, pr := range doc.PRs {
		t.Run(pr.HeadRef, func(t *testing.T) {
			files, in := fixtureInput(ctx, t, g, pr)
			units, err := Split(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			checkInvariants(t, files, units)
			for _, f := range files {
				if Lang(f.Path()) != "" && !f.Binary && !newFileCtx(ctx, in, f).ok {
					t.Errorf("%s fell back to hunk-level units", f.Path())
				}
			}
			again, err := Split(ctx, in)
			if err != nil || !reflect.DeepEqual(units, again) {
				t.Errorf("Split is not deterministic (err %v)", err)
			}
			seen := map[string]bool{}
			for _, u := range units {
				if seen[u.ID] {
					t.Errorf("duplicate unit id %s (%s %q)", u.ID, u.Path, u.Symbol)
				}
				seen[u.ID] = true
			}
			if check := checks[pr.Number]; check != nil {
				check(t, units)
			}
		})
	}
	for n := range checks {
		found := false
		for _, pr := range doc.PRs {
			found = found || pr.Number == n
		}
		if !found {
			t.Errorf("fixture PR #%d missing from prs.json", n)
		}
	}
}

func fixtureInput(ctx context.Context, t *testing.T, g gitx.Git, pr fixturePR) ([]diff.File, Input) {
	t.Helper()
	base := pr.BaseRef
	if base == "" {
		base = "main"
	}
	mb, err := g.MergeBase(ctx, base, pr.HeadRef)
	if err != nil {
		t.Fatal(err)
	}
	head, err := g.ResolveRef(ctx, pr.HeadRef)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := g.Diff(ctx, mb, head)
	if err != nil {
		t.Fatal(err)
	}
	files, err := diff.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return files, Input{ItemID: model.PRItemID(pr.Number), BaseSHA: mb, HeadSHA: head, Files: files, Src: g}
}

func check101(t *testing.T, units []model.ChangeUnit) {
	for _, u := range units {
		if u.Path == "web/src/api/client.ts" && strings.Contains(u.Symbol, "getUser") &&
			strings.Contains(u.Signature, "opts: GetUserOptions") {
			return
		}
	}
	t.Errorf("#101: no getUser unit with the new signature in client.ts; units: %v", describe(units, "web/src/api/client.ts"))
}

func check102(t *testing.T, units []model.ChangeUnit) {
	count := map[string]int{}
	best := 0
	for _, u := range units {
		count[u.ShapeHash]++
		best = max(best, count[u.ShapeHash])
	}
	if best < 5 {
		t.Errorf("#102: largest shape group has %d units, want >= 5", best)
	}
}

func check103(t *testing.T, units []model.ChangeUnit) {
	if len(units) == 0 {
		t.Fatal("#103: no units")
	}
	for _, u := range units {
		if u.Kind != model.UnitDeleted && u.Kind != model.UnitMoved {
			t.Errorf("#103: %s %q kind %s, want deleted", u.Path, u.Symbol, u.Kind)
		}
	}
}

func check104(t *testing.T, units []model.ChangeUnit) {
	for _, p := range []string{"server/go.mod", "web/package.json"} {
		found := false
		for _, u := range units {
			if u.Path != p {
				continue
			}
			found = true
			if u.Symbol != "" {
				t.Errorf("#104: %s unit has symbol %q", p, u.Symbol)
			}
		}
		if !found {
			t.Errorf("#104: no units for %s", p)
		}
	}
}

func describe(units []model.ChangeUnit, path string) []string {
	var out []string
	for _, u := range units {
		if u.Path == path {
			out = append(out, u.Symbol+" | "+u.Signature)
		}
	}
	return out
}
