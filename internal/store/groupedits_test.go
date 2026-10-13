package store

import (
	"context"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

func TestGroupEdits(t *testing.T) {
	ctx := context.Background()
	s, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	it := model.Item{ID: model.PRItemID(1), Kind: model.KindPR, Number: 1}
	if err := s.UpsertItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	edits := []model.GroupEdit{
		{ItemID: it.ID, Op: model.EditMerge, Units: []string{"a", "b"}, At: at},
		{ItemID: it.ID, Op: model.EditSplit, Units: []string{"b"}, At: at},
		{ItemID: "pr:2", Op: model.EditSplit, Units: []string{"x"}, At: at},
	}
	for i := 0; i < 11; i++ { // more than 10 to check ordering past one digit
		edits = append(edits, model.GroupEdit{ItemID: it.ID, Op: model.EditMerge, Units: []string{"m", string(rune('a' + i))}, At: at})
	}
	for _, e := range edits {
		if err := s.AppendGroupEdit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GroupEdits(ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 13 || got[0].Op != model.EditMerge || got[1].Op != model.EditSplit || got[12].Units[1] != "k" {
		t.Fatalf("edits = %+v", got)
	}

	if err := s.AppendGroupEdit(ctx, model.GroupEdit{ItemID: it.ID, Op: model.EditReset, At: at}); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GroupEdits(ctx, it.ID); err != nil || len(got) != 0 {
		t.Fatalf("after reset = %+v, %v", got, err)
	}
	if other, err := s.GroupEdits(ctx, "pr:2"); err != nil || len(other) != 1 {
		t.Fatalf("other item = %+v, %v", other, err)
	}

	if err := s.AppendGroupEdit(ctx, edits[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveItem(ctx, it.ID); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GroupEdits(ctx, it.ID); err != nil || len(got) != 0 {
		t.Fatalf("after remove = %+v, %v", got, err)
	}
}
