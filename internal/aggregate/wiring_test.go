package aggregate

import (
	"reflect"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

// wiringInput is one bullet ("Invalidate the profile cache on logout") and
// three units: a new helper, a modified logout handler and a test.
func wiringInput(links []model.Link, claims ...string) Input {
	units := []model.ChangeUnit{
		{ID: "h", Path: "web/cache.ts", Symbol: "clearProfileCache", Kind: model.UnitAdded, Adds: 5, Order: 0},
		{ID: "c", Path: "web/logout.ts", Symbol: "logout", Kind: model.UnitModified, Adds: 1, Dels: 1, Order: 1},
		{ID: "t", Path: "web/cache.test.ts", Symbol: "it", Kind: model.UnitAdded, Adds: 4, Order: 2},
		{ID: "n", Path: "web/config.ts", Kind: model.UnitAdded, Adds: 2, Order: 3},
		{ID: "mv", Path: "web/util.ts", Symbol: "moved", Kind: model.UnitMoved, Adds: 3, Order: 4},
	}
	var labels []model.Label
	for _, id := range claims {
		labels = append(labels, model.Label{UnitID: id, Question: model.QDescBullet, Value: model.BulletValue(1), P: 0.8, Confidence: 0.8})
	}
	return Input{
		Item:    model.Item{ID: "pr:1", Body: "- Invalidate the profile cache on logout"},
		Units:   units,
		Labels:  labels,
		Links:   links,
		Weights: config.Defaults().Rank, Pipeline: config.Defaults().Pipeline,
	}
}

func calls(from, to string) model.Link {
	return model.Link{From: from, To: to, Kind: model.LinkCalls, Weight: 1, Source: model.LinkStructural}
}

func TestFidelityNeedsWiredUnits(t *testing.T) {
	imports := model.Link{From: "c", To: "h", Kind: model.LinkImports, Weight: 1, Source: model.LinkStructural}
	tests := []struct {
		name        string
		links       []model.Link
		claims      []string
		implemented bool
	}{
		{"unwired helper", []model.Link{}, []string{"h"}, false},
		{"helper called by a non-test unit", []model.Link{calls("c", "h")}, []string{"h"}, true},
		{"helper imported by a non-test unit", []model.Link{imports}, []string{"h"}, true},
		{"helper called only from a test", []model.Link{calls("t", "h")}, []string{"h"}, false},
		{"caller outside the item", []model.Link{calls("zz", "h")}, []string{"h"}, false},
		{"tests link does not wire", []model.Link{{From: "c", To: "h", Kind: model.LinkTests, Source: model.LinkStructural}}, []string{"h"}, false},
		{"links nil keeps M1", nil, []string{"h"}, true},
		{"modified unit counts", []model.Link{}, []string{"c"}, true},
		{"test unit counts", []model.Link{}, []string{"t"}, true},
		{"unit without symbol counts", []model.Link{}, []string{"n"}, true},
		{"unwired moved-in unit", []model.Link{}, []string{"mv"}, false},
		{"one wired claimer is enough", []model.Link{}, []string{"h", "c"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agg := Item(wiringInput(tc.links, tc.claims...))
			if len(agg.Bullets) != 1 {
				t.Fatalf("bullets = %+v", agg.Bullets)
			}
			b := agg.Bullets[0]
			if b.Implemented != tc.implemented {
				t.Errorf("implemented = %v, want %v", b.Implemented, tc.implemented)
			}
			if !reflect.DeepEqual(b.UnitIDs, tc.claims) {
				t.Errorf("unit ids = %v, want every claimer %v", b.UnitIDs, tc.claims)
			}
			want := 0.0
			if tc.implemented {
				want = 1
			}
			if !near(agg.Fidelity, want) {
				t.Errorf("fidelity = %v, want %v", agg.Fidelity, want)
			}
		})
	}
}

func TestMechanicalCountFive(t *testing.T) {
	var units []model.ChangeUnit
	for i, id := range []string{"m1", "m2", "m3", "m4", "m5", "x"} {
		units = append(units, model.ChangeUnit{ID: id, Path: "src/a.go", Kind: model.UnitModified, Adds: 2, Dels: 1, Order: i})
	}
	units[5].Adds = 4
	ids := []string{"m3", "m1", "m2", "m4", "m5"}
	group := model.Group{ID: model.GroupID(ids), UnitIDs: ids, Mechanical: true, ExampleUnit: "m3", Count: 5}
	in := Input{
		Item: model.Item{ID: "pr:1"}, Units: units, Groups: []model.Group{group},
		Weights: config.Defaults().Rank, Pipeline: config.Defaults().Pipeline,
	}
	agg := Item(in)
	if agg.EffectiveLines != 3+5 {
		t.Errorf("effective lines = %d, want 8 (example once plus x)", agg.EffectiveLines)
	}
	for _, id := range []string{"m1", "m2", "m3", "m4", "m5"} {
		want := 0
		if id == "m3" {
			want = 3
		}
		if got := unitByID(t, agg, id).EffectiveLines; got != want {
			t.Errorf("%s effective lines = %d, want %d", id, got, want)
		}
	}
	if len(agg.Groups) != 1 || agg.Groups[0].EffectiveLines != 3 {
		t.Errorf("group agg = %+v", agg.Groups)
	}
}
