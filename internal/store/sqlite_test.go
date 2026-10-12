package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestSQLiteRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "prt", "prt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	it := model.Item{ID: model.PRItemID(7), Kind: model.KindPR, Number: 7, Title: "t", Source: model.SourceAdded}
	if err := s.UpsertItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	got, err := s.Item(ctx, it.ID)
	if err != nil || got.Title != "t" {
		t.Fatalf("item = %+v, %v", got, err)
	}
	if _, err := s.Item(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	if err := s.PutFetch(ctx, model.Fetch{ItemID: it.ID, HeadSHA: "abc", Diff: "d"}); err != nil {
		t.Fatal(err)
	}
	f, ok, err := s.Fetch(ctx, it.ID, "abc")
	if err != nil || !ok || f.Diff != "d" {
		t.Fatalf("fetch = %+v %v %v", f, ok, err)
	}

	units := []model.ChangeUnit{{ID: "u2", Order: 1}, {ID: "u1", Order: 0}}
	if err := s.PutUnits(ctx, it.ID, units); err != nil {
		t.Fatal(err)
	}
	us, _ := s.Units(ctx, it.ID)
	if len(us) != 2 || us[0].ID != "u1" {
		t.Fatalf("units = %+v", us)
	}
	if err := s.PutUnits(ctx, it.ID, units[:1]); err != nil {
		t.Fatal(err)
	}
	if us, _ := s.Units(ctx, it.ID); len(us) != 1 {
		t.Fatalf("PutUnits must replace, got %d", len(us))
	}

	if err := s.PutLabels(ctx, []model.Label{{UnitID: "u1", Question: "risk", Value: "3"}, {UnitID: "u9", Question: "risk"}}); err != nil {
		t.Fatal(err)
	}
	ls, _ := s.Labels(ctx, []string{"u1"})
	if len(ls) != 1 || ls[0].Value != "3" {
		t.Fatalf("labels = %+v", ls)
	}

	if err := s.PutEmbedding(ctx, "u1", "fake", []float32{1.5, -2}); err != nil {
		t.Fatal(err)
	}
	v, ok, _ := s.Embedding(ctx, "u1", "fake")
	if !ok || len(v) != 2 || v[0] != 1.5 || v[1] != -2 {
		t.Fatalf("embedding = %v", v)
	}

	if err := s.AppendRunLog(ctx, model.RunLog{ItemID: it.ID, Stage: model.StageParse}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendRunLog(ctx, model.RunLog{ItemID: it.ID, Stage: model.StageLink}); err != nil {
		t.Fatal(err)
	}
	logs, _ := s.RunLogs(ctx, it.ID)
	if len(logs) != 2 || logs[1].Stage != model.StageLink {
		t.Fatalf("runlogs = %+v", logs)
	}

	if err := s.RemoveItem(ctx, it.ID); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.Items(ctx); len(items) != 0 {
		t.Fatalf("items after remove = %+v", items)
	}
	if ls, _ := s.Labels(ctx, []string{"u1"}); len(ls) != 1 {
		t.Fatal("labels are unit-keyed and must survive item removal")
	}
}

func TestReopenKeepsData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "prt.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CachePut(ctx, "ns", "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	b, ok, err := s.CacheGet(ctx, "ns", "k")
	if err != nil || !ok || string(b) != "v" {
		t.Fatalf("cache = %q %v %v", b, ok, err)
	}
}
