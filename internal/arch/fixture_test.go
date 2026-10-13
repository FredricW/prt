package arch

import (
	"context"
	"slices"
	"testing"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/testutil"
)

// TestBuildFixturePR101 builds the graph of fixture PR #101
// (testdata/fixtures/prs/01-user-profile-refactor.patch) over the generated
// fixture repo.
func TestBuildFixturePR101(t *testing.T) {
	ctx := context.Background()
	g, err := gitx.Open(ctx, testutil.FixtureRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	const headRef = "01-user-profile-refactor"
	base, err := g.MergeBase(ctx, "main", headRef)
	if err != nil {
		t.Fatal(err)
	}
	head, err := g.ResolveRef(ctx, headRef)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := g.Diff(ctx, base, head)
	if err != nil {
		t.Fatal(err)
	}
	dfs, err := diff.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]model.File, 0, len(dfs))
	for _, f := range dfs {
		files = append(files, model.File{Path: f.Path(), OldPath: f.OldPath, Added: f.Added, Deleted: f.Deleted, Renamed: f.Renamed})
	}
	graph, err := Build(ctx, Input{ItemID: "pr:101", BaseSHA: base, HeadSHA: head, Files: files, Src: g, MaxHop: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("modules %v", moduleList(graph))
	t.Logf("edges %v", edgeList(graph))
	t.Logf("net effect %q note %q", graph.NetEffect, graph.Note)
	touched := map[string]model.ArchModule{}
	for _, m := range graph.Modules {
		if m.Hop == 0 {
			touched[m.ID] = m
		}
	}
	for _, id := range []string{"web/src/api", "web/src/components", "web/src/hooks", "server/internal/users"} {
		if _, ok := touched[id]; !ok {
			t.Errorf("module %s not touched", id)
		}
	}
	if m := touched["web/src/api"]; !slices.Contains(m.Files, "web/src/api/errors.ts") || m.Status != model.ArchChanged {
		t.Errorf("web/src/api %+v", m)
	}
	edges := map[string]string{}
	for _, e := range graph.Edges {
		edges[e.From+"→"+e.To] = e.Status
	}
	if st := edges["web/src/components→web/src/api"]; st != model.ArchSame {
		t.Errorf("components → api edge %q", st)
	}
	if st := edges["web/src/components→web/src/hooks"]; st == "" {
		t.Errorf("no components → hooks edge")
	}
	if graph.Note != "" {
		t.Errorf("note %q", graph.Note)
	}
	if m, ok := moduleByID(graph, "web/src"); !ok || m.Hop != 1 || m.Status != model.ArchContext {
		t.Errorf("App.tsx's module web/src is not a hop-1 context module: %+v", m)
	}
}
