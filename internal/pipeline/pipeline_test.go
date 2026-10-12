package pipeline

import (
	"context"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/store"
)

const branchDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 package a
+var X = 1
 func F() {}
`

func TestRunBranchOffline(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	g := &gitx.Fake{
		SHAs:       map[string]string{"feat/x": "h1"},
		MergeBases: map[string]string{"main..h1": "b1"},
		Diffs:      map[string]string{"b1..h1": branchDiff},
	}
	var events []model.RunLog
	p := New(Deps{Git: g, Store: st, Config: config.Defaults(), Emit: func(r model.RunLog) { events = append(events, r) }})
	it := model.Item{ID: model.BranchItemID("feat/x"), Kind: model.KindBranch, Ref: "feat/x"}
	if err := p.Run(ctx, it); err != nil {
		t.Fatal(err)
	}
	units, _ := st.Units(ctx, it.ID)
	if len(units) != 1 || units[0].Adds != 1 {
		t.Fatalf("units = %+v", units)
	}
	agg, ok, _ := st.Agg(ctx, it.ID)
	if !ok || agg.Added != 1 {
		t.Fatalf("agg = %+v", agg)
	}
	if len(events) == 0 {
		t.Fatal("no run log events emitted")
	}

	// Second run hits the fetch cache.
	events = nil
	if err := p.Run(ctx, it); err != nil {
		t.Fatal(err)
	}
	if events[1].Stage != model.StageFetch || events[1].Status != model.StageCached {
		t.Fatalf("want cached fetch, got %+v", events[1])
	}
}
