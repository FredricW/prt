package workspace

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
	"github.com/fredricw/prt/internal/testutil"
)

// realMember builds an in-process client over a freshly generated fixture
// repo, its own in-memory store and the offline GitHub fixture (no
// network), sharing pool and ledger.
func realMember(t *testing.T, key string, pool *daemon.Pool, ledger *daemon.Ledger) Member {
	t.Helper()
	ctx := context.Background()
	repo := testutil.FixtureRepo(t)
	g, err := gitx.Open(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fake, err := gh.LoadFixture(testutil.FixturePRs(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Pipeline.AutoReview = string(model.AutoManual)
	cfg.User.Role = "role-" + key
	info := Describe(Repo{Key: key, Root: repo, GitDir: filepath.Join(repo, ".git")}, "acme/"+key, "", true, "fixtures")
	c := daemon.NewInProcessWith(pipeline.Deps{Git: g, Store: st, Config: cfg, GH: fake}, daemon.Options{Pool: pool, Ledger: ledger, Repo: info})
	t.Cleanup(func() { _ = c.Close() })
	return Member{Info: info, Client: c}
}

func TestWorkspaceOverRealClients(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx := context.Background()
	pool, ledger := daemon.NewPool(2), daemon.NewLedger()
	b := realMember(t, "b", pool, ledger)
	a := realMember(t, "a", pool, ledger)
	c := New(t.TempDir(), []Member{b, a}, ClientOptions{Pool: pool, Ledger: ledger, Notes: []string{"note"}})
	t.Cleanup(func() { _ = c.Close() })

	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := c.Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]model.Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	for _, id := range []string{"a::pr:101", "b::pr:101"} {
		it, ok := byID[id]
		if !ok {
			t.Fatalf("missing %s in %d items", id, len(items))
		}
		if repo, _ := model.SplitRepoItemID(id); it.Repo != repo {
			t.Errorf("%s: repo %q", id, it.Repo)
		}
	}
	if len(items)%2 != 0 {
		t.Errorf("the two identical repos list %d items", len(items))
	}

	det, err := c.DetailOf(ctx, proto.DetailParams{ItemID: "b::pr:101", NoLabels: true})
	if err != nil {
		t.Fatal(err)
	}
	if det.Item.ID != "b::pr:101" || det.Item.Repo != "b" || det.Item.Number != 101 {
		t.Errorf("detail item = %s %q #%d", det.Item.ID, det.Item.Repo, det.Item.Number)
	}

	if _, err := c.Stop(ctx, proto.StopParams{All: true}); err != nil {
		t.Errorf("stop all: %v", err)
	}

	r, err := c.Search(ctx, proto.SearchParams{Query: "#101", Source: proto.SearchOpen})
	if err != nil {
		t.Fatal(err)
	}
	var queries []string
	for _, h := range r.Hits {
		queries = append(queries, h.Query)
	}
	if !slices.Equal(queries, []string{"a#101", "b#101"}) {
		t.Errorf("search hits = %v (note %q)", queries, r.Note)
	}

	ws, err := c.Workspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ws.Multi || len(ws.Repos) != 2 || ws.Concurrency != 2 || ws.Repos[0].Key != "a" || ws.Repos[1].Label != "acme/b" {
		t.Errorf("workspace = %+v", ws)
	}
	if ws.Repos[0].Status != proto.RepoGitHub || ws.Repos[0].Role != "role-a" || ws.Repos[1].Role != "role-b" {
		t.Errorf("repo a = %+v", ws.Repos[0])
	}
}
