package link

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/parse"
	"github.com/fredricw/prt/internal/testutil"
)

// fixturePR is the subset of testdata/fixtures/prs.json this test reads.
type fixturePR struct {
	Number  int    `json:"number"`
	HeadRef string `json:"headRef"`
	BaseRef string `json:"baseRef"`
}

// fixtureItem is one fixture PR run through parse and Structural.
type fixtureItem struct {
	units []model.ChangeUnit
	links []model.Link
	byID  map[string]model.ChangeUnit
}

func TestStructuralFixturePRs(t *testing.T) {
	ctx := context.Background()
	var doc struct {
		PRs []fixturePR `json:"prs"`
	}
	if err := json.Unmarshal(testutil.ReadTestdata(t, "fixtures", "prs.json"), &doc); err != nil {
		t.Fatal(err)
	}
	g, err := gitx.Open(ctx, testutil.FixtureRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	checks := map[int]func(*testing.T, fixtureItem){
		101: check101, 102: check102, 103: func(*testing.T, fixtureItem) {}, 104: check104,
	}
	cache := &mapCache{} // shared: the PRs share a base, so the index is built once
	for _, pr := range doc.PRs {
		check := checks[pr.Number]
		if check == nil {
			continue
		}
		t.Run(pr.HeadRef, func(t *testing.T) {
			in := fixtureInput(ctx, t, g, pr)
			in.Cache = cache
			links, err := Structural(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			checkLinkInvariants(t, in.Units, links)
			again, err := Structural(ctx, in)
			if err != nil || !reflect.DeepEqual(links, again) {
				t.Errorf("Structural is not deterministic (err %v)", err)
			}
			it := fixtureItem{units: in.Units, links: links, byID: map[string]model.ChangeUnit{}}
			for _, u := range in.Units {
				it.byID[u.ID] = u
			}
			check(t, it)
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

// fixtureInput diffs a fixture PR against its merge base and splits it.
func fixtureInput(ctx context.Context, t *testing.T, g gitx.Git, pr fixturePR) Input {
	t.Helper()
	baseRef := pr.BaseRef
	if baseRef == "" {
		baseRef = "main"
	}
	base, err := g.MergeBase(ctx, baseRef, pr.HeadRef)
	if err != nil {
		t.Fatal(err)
	}
	head, err := g.ResolveRef(ctx, pr.HeadRef)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := g.Diff(ctx, base, head)
	if err != nil {
		t.Fatal(err)
	}
	files, err := diff.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	itemID := model.PRItemID(pr.Number)
	// Split already applies parse.DetectMoved.
	units, err := parse.Split(ctx, parse.Input{ItemID: itemID, BaseSHA: base, HeadSHA: head, Files: files, Src: g})
	if err != nil {
		t.Fatal(err)
	}
	mf := make([]model.File, 0, len(files))
	for _, f := range files {
		mf = append(mf, model.File{
			ItemID: itemID, Path: f.Path(), OldPath: f.OldPath, Added: f.Added, Deleted: f.Deleted,
			Renamed: f.Renamed, Binary: f.Binary, Lang: parse.Lang(f.Path()),
		})
	}
	return Input{ItemID: itemID, BaseSHA: base, HeadSHA: head, Files: mf, Units: units, Src: g}
}

// has reports whether a link of kind runs from a unit matching from to a
// unit matching to.
func (it fixtureItem) has(kind model.LinkKind, from, to func(model.ChangeUnit) bool) bool {
	for _, l := range it.links {
		if l.Kind == kind && from(it.byID[l.From]) && to(it.byID[l.To]) {
			return true
		}
	}
	return false
}

func inFile(p string) func(model.ChangeUnit) bool {
	return func(u model.ChangeUnit) bool { return u.Path == p }
}

func symbolIn(p, sym string) func(model.ChangeUnit) bool {
	return func(u model.ChangeUnit) bool { return u.Path == p && strings.Contains(u.Symbol, sym) }
}

func check101(t *testing.T, it fixtureItem) {
	const client, errorsTS = "web/src/api/client.ts", "web/src/api/errors.ts"
	if !it.has(model.LinkMoved, inFile(client), symbolIn(errorsTS, "ApiError")) {
		t.Errorf("#101: no moved link client.ts → errors.ts ApiError")
	}
	// parse pairs only identical pure-delete/pure-add units; authHeader
	// gained `export` on the way, so only the symbol-level detection finds it.
	if !it.has(model.LinkMoved, symbolIn(client, "authHeader"), symbolIn(errorsTS, "authHeader")) {
		t.Errorf("#101: no moved link client.ts authHeader → errors.ts authHeader")
	}
	callers := map[string]bool{}
	for _, l := range it.links {
		from, to := it.byID[l.From], it.byID[l.To]
		if l.Kind == model.LinkCalls && to.Path == client && strings.Contains(to.Symbol, "getUser") && from.Path != client {
			callers[from.ID] = true
		}
	}
	if len(callers) < 3 {
		t.Errorf("#101: %d caller units in other files link to client.ts getUser, want ≥ 3", len(callers))
	}
	if !it.has(model.LinkTests, inFile("web/src/__tests__/useProfileCache.test.ts"), inFile("web/src/hooks/useProfileCache.ts")) {
		t.Errorf("#101: no tests link useProfileCache.test.ts → useProfileCache.ts")
	}
	if !it.has(model.LinkTests, inFile("web/src/__tests__/client.test.ts"), inFile(client)) {
		t.Errorf("#101: no tests link client.test.ts → client.ts")
	}
}

func check102(t *testing.T, it fixtureItem) {
	def := symbolIn("web/src/api/client.ts", "requestJSON")
	callers := map[string]bool{}
	for _, l := range it.links {
		from := it.byID[l.From]
		if (l.Kind == model.LinkCalls || l.Kind == model.LinkImports) && def(it.byID[l.To]) && from.Path != "web/src/api/client.ts" {
			callers[from.Path] = true
		}
	}
	if len(callers) < 15 {
		t.Errorf("#102: %d files link to the requestJSON definition, want ≥ 15: %v", len(callers), callers)
	}
	stars := map[string]int{}
	for _, l := range MechanicalCandidates(it.units) {
		stars[l.From]++
	}
	best := 0
	for _, n := range stars {
		best = max(best, n)
	}
	if best < 4 {
		t.Errorf("#102: largest mechanical star has %d links, want ≥ 4", best)
	}
}

func check104(t *testing.T, it fixtureItem) {
	for _, l := range it.links {
		if l.Kind == model.LinkCalls {
			t.Errorf("#104: unexpected calls link %s → %s", it.byID[l.From].Path, it.byID[l.To].Path)
		}
	}
}
