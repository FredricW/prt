package daemon

import (
	"context"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/store"
)

func TestDiscoverAndRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	g := &gitx.Fake{
		RefList:    []gitx.Ref{{Name: "main", SHA: "b1"}, {Name: "feat/x", SHA: "h1", Subject: "add x"}, {Name: "merged", SHA: "b1"}},
		Ahead:      map[string]int{"main..feat/x": 1},
		MergeBases: map[string]string{"main..h1": "b1"},
		Diffs:      map[string]string{"b1..h1": "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1,2 @@\n x\n+y\n"},
	}
	c := NewInProcess(pipeline.Deps{Git: g, Store: st, Config: config.Defaults()})
	defer func() { _ = c.Close() }()

	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	items, _ := c.Items(ctx)
	if len(items) != 1 || items[0].ID != model.BranchItemID("feat/x") || items[0].Title != "add x" {
		t.Fatalf("items = %+v", items)
	}
	if err := c.RunSync(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	det, err := c.Detail(ctx, items[0].ID)
	if err != nil || len(det.Units) != 1 || det.Diff == "" || det.Agg == nil || det.Item.State != model.StateReady {
		t.Fatalf("detail = %+v, %v", det, err)
	}
}
