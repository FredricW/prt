package store

import (
	"context"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

func TestFindingAndViewMarks(t *testing.T) {
	ctx := context.Background()
	s, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

	if _, ok, err := s.ViewMark(ctx, "pr:1"); err != nil || ok {
		t.Fatalf("empty view mark: ok %v err %v", ok, err)
	}
	for _, v := range []model.ViewMark{
		{ItemID: "pr:1", HeadSHA: "a", At: at},
		{ItemID: "pr:1", HeadSHA: "b", PrevHeadSHA: "a", At: at.Add(time.Hour)},
		{ItemID: "pr:2", HeadSHA: "x", At: at},
	} {
		if err := s.PutViewMark(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if v, ok, err := s.ViewMark(ctx, "pr:1"); err != nil || !ok || v.HeadSHA != "b" || v.PrevHeadSHA != "a" {
		t.Errorf("view mark = %+v ok %v err %v, want head b prev a", v, ok, err)
	}

	marks := []model.FindingMark{
		{ItemID: "pr:1", FindingKey: "k1", State: model.FindingDismissed, At: at},
		{ItemID: "pr:1", FindingKey: "k2", State: model.FindingAccepted, CommentID: "c1", At: at},
		{ItemID: "pr:1", FindingKey: "k1", State: model.FindingDismissed, Override: &model.Override{UnitID: "u", Question: "risk", Value: "1"}, At: at},
		{ItemID: "pr:2", FindingKey: "k1", State: model.FindingAccepted, At: at},
	}
	for _, m := range marks {
		if err := s.PutFindingMark(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.FindingMarks(ctx, "pr:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].FindingKey != "k1" || got[0].Override == nil || got[1].CommentID != "c1" {
		t.Errorf("finding marks = %+v, want k1 (with override) and k2", got)
	}
	// Removing an item keeps its marks (like comments).
	if err := s.RemoveItem(ctx, "pr:1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FindingMarks(ctx, "pr:1"); len(got) != 2 {
		t.Errorf("marks after RemoveItem = %d, want 2", len(got))
	}
}
