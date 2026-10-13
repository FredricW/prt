package store

import (
	"context"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

func TestItemReviewsReplaceAndRemove(t *testing.T) {
	ctx := context.Background()
	s, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id := model.PRItemID(1)
	if err := s.UpsertItem(ctx, model.Item{ID: id, Kind: model.KindPR, Number: 1}); err != nil {
		t.Fatal(err)
	}
	if rs, err := s.ItemReviews(ctx, id); err != nil || rs != nil {
		t.Fatalf("empty: %v %v", rs, err)
	}
	first := []model.Review{{GroupID: "g1", ItemID: id, Tier: model.TierFast}, {GroupID: "g2", ItemID: id, Tier: model.TierNone}}
	if err := s.PutItemReviews(ctx, id, first); err != nil {
		t.Fatal(err)
	}
	if err := s.PutItemReviews(ctx, id, first[:1]); err != nil {
		t.Fatal(err)
	}
	rs, err := s.ItemReviews(ctx, id)
	if err != nil || len(rs) != 1 || rs[0].GroupID != "g1" {
		t.Fatalf("replace: %+v %v", rs, err)
	}
	if err := s.RemoveItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if rs, err := s.ItemReviews(ctx, id); err != nil || rs != nil {
		t.Fatalf("after remove: %v %v", rs, err)
	}
}

func TestCalibrationsSince(t *testing.T) {
	ctx := context.Background()
	s, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i, d := range []int{0, 20, 40} {
		c := model.CalibrationSample{ItemID: "pr:1", GroupID: string(rune('a' + i)), At: t0.AddDate(0, 0, d), Missed: i, Agree: i == 0}
		if err := s.AppendCalibration(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Calibrations(ctx, t0.AddDate(0, 0, 10))
	if err != nil || len(got) != 2 || got[0].GroupID != "b" || got[1].GroupID != "c" {
		t.Fatalf("since: %+v %v", got, err)
	}
}
