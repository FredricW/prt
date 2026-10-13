package codeview

import (
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// An overridden label shows the override and Jev's original answer.
func TestRawLabelsTextOverride(t *testing.T) {
	high := "high"
	u := model.ChangeUnit{ID: "u1", Path: "a.go"}
	ls := []model.Label{
		{UnitID: "u1", Question: "risk", Value: "high", Overridden: &high, Original: "low", P: 1, Confidence: 1},
		{UnitID: "u1", Question: "kind", Value: "refactor", Overridden: ptr("refactor"), P: 1, Confidence: 1},
	}
	got := RawLabelsText(u, ls, 120)
	if !strings.Contains(got, "high ! (was low)") {
		t.Errorf("override text missing:\n%s", got)
	}
	if !strings.Contains(got, "refactor ! (was unknown)") {
		t.Errorf("override without an original answer:\n%s", got)
	}
	if v := currentValue(ls[0]); v != "high" {
		t.Errorf("currentValue = %q", v)
	}
}

func ptr(s string) *string { return &s }
