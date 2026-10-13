package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/store"
)

func newStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func fetchEvent(t *testing.T, events []model.RunLog) model.RunLog {
	t.Helper()
	for _, e := range events {
		if e.Stage == model.StageFetch && e.Status != model.StageRunning {
			return e
		}
	}
	t.Fatalf("no finished fetch event in %+v", events)
	return model.RunLog{}
}

func TestFetchPRDiffFallbackAndCache(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	// Head SHA "remote1" is not known locally: no merge base, no diff.
	g := &gitx.Fake{}
	fake := &gh.Fake{
		PRs:   map[int]gh.PR{7: {Number: 7, HeadRef: "feat/remote", HeadSHA: "remote1", BaseRef: "main", BaseSHA: "base9"}},
		Diffs: map[int]string{7: branchDiff},
	}
	var events []model.RunLog
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	p := New(Deps{Git: g, GH: fake, Store: st, Config: config.Defaults(),
		Now: func() time.Time { return now }, Emit: func(r model.RunLog) { events = append(events, r) }})
	it := model.Item{ID: model.PRItemID(7), Kind: model.KindPR, Number: 7, Ref: "feat/remote", BaseRef: "main",
		HeadSHA: "remote1", ReviewRequested: true, CreatedAt: now.Add(-24 * time.Hour)}
	if err := st.UpsertItem(ctx, it); err != nil {
		t.Fatal(err)
	}

	if err := p.Run(ctx, it); err != nil {
		t.Fatal(err)
	}
	fe := fetchEvent(t, events)
	if fe.Status != model.StageDone || fe.CacheHit || fe.Note != "diff from GitHub" {
		t.Fatalf("first fetch = %+v", fe)
	}
	f, ok, err := st.Fetch(ctx, it.ID, "remote1")
	if err != nil || !ok || f.BaseSHA != "base9" || f.Diff != branchDiff {
		t.Fatalf("stored fetch = %+v ok=%v err=%v", f, ok, err)
	}
	got, err := st.Item(ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Additions != 1 || got.Deletions != 0 || got.Urgency <= 0 || got.Why == "" || got.BaseSHA != "base9" {
		t.Fatalf("item not updated from agg: %+v", got)
	}

	// Second run: cache hit by head SHA, GitHub not asked again.
	fake.Diffs = nil
	events = nil
	if err := p.Run(ctx, it); err != nil {
		t.Fatal(err)
	}
	if fe := fetchEvent(t, events); !fe.CacheHit || fe.Status != model.StageCached {
		t.Fatalf("second fetch = %+v", fe)
	}

	// Force bypasses the cache: with the diff gone from GitHub it now fails.
	events = nil
	if err := p.RunWith(ctx, it, RunOptions{Force: true}); err == nil {
		t.Fatal("forced run should re-fetch and fail without a diff")
	}
	if fe := fetchEvent(t, events); fe.CacheHit || fe.Status != model.StageFailed {
		t.Fatalf("forced fetch = %+v", fe)
	}
}

func TestFetchForceRefetchesLocal(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	g := &gitx.Fake{
		MergeBases: map[string]string{"main..h1": "b1"},
		Diffs:      map[string]string{"b1..h1": branchDiff},
	}
	var events []model.RunLog
	p := New(Deps{Git: g, Store: st, Config: config.Defaults(), Emit: func(r model.RunLog) { events = append(events, r) }})
	it := model.Item{ID: model.BranchItemID("feat/x"), Kind: model.KindBranch, Ref: "feat/x", HeadSHA: "h1"}
	for i, force := range []bool{false, false, true} {
		events = nil
		if err := p.RunWith(ctx, it, RunOptions{Force: force}); err != nil {
			t.Fatal(err)
		}
		wantHit := i == 1
		if fe := fetchEvent(t, events); fe.CacheHit != wantHit {
			t.Fatalf("run %d (force=%v): cacheHit = %v", i, force, fe.CacheHit)
		}
	}
}

func TestFetchNoDiffWithoutGitHub(t *testing.T) {
	ctx := context.Background()
	p := New(Deps{Git: &gitx.Fake{}, Store: newStore(t), Config: config.Defaults()})
	it := model.Item{ID: model.PRItemID(9), Kind: model.KindPR, Number: 9, Ref: "gone"}
	if err := p.Run(ctx, it); err == nil {
		t.Fatal("want error")
	}
}
