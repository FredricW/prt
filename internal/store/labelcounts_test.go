package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestLabelCounts(t *testing.T) {
	ctx := context.Background()
	s, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	got, err := s.LabelCounts(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty store: %v %v", got, err)
	}
	ls := []model.Label{
		{UnitID: "u1", Question: model.QRisk, Value: "4"},
		{UnitID: "u2", Question: model.QRisk, Value: model.LabelUnknown},
		{UnitID: "u1", Question: model.QKind, Value: "feature"},
		{UnitID: "u1", Question: "suspicious", Line: 1, Value: "2"},
		{UnitID: "u1", Question: "suspicious", Line: 2, Value: "1"},
		{UnitID: model.PairSubject("a", "b"), Question: model.QRelation, Value: "same intent"},
		{UnitID: model.BranchSubject("branch:x"), Question: model.QState, Value: model.BranchWIP},
		{UnitID: "u1", Question: model.QRisk, Value: "3"}, // replaces u1's risk
	}
	if err := s.PutLabels(ctx, ls); err != nil {
		t.Fatal(err)
	}
	// Other kinds are not counted.
	if err := s.PutOverride(ctx, model.Override{UnitID: "u1", Question: model.QRisk, Value: "2"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.LabelCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{model.QRisk: 2, model.QKind: 1, "suspicious": 2, model.QRelation: 1, model.QState: 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
}
