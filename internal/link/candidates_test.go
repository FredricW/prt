package link

import (
	"math"
	"reflect"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func vecUnits(n int) []model.ChangeUnit {
	ids := []string{"a", "b", "c", "d", "e", "f"}
	out := make([]model.ChangeUnit, n)
	for i := range out {
		out[i] = model.ChangeUnit{ID: ids[i], Order: i}
	}
	return out
}

func TestEmbedCandidates(t *testing.T) {
	vecs := map[string][]float32{
		"a": {1, 0},
		"b": {0.99, 0.14},
		"c": {0.98, 0.2},
		"d": {0, 1},
		// "e" has no vector
		"f": {0, 0},
	}
	units := vecUnits(6)
	shaped := vecUnits(6)
	shaped[0].ShapeHash, shaped[1].ShapeHash = "h", "h"
	shaped[2].ShapeHash = "other"
	tests := []struct {
		name  string
		units []model.ChangeUnit
		o     EmbedOptions
		want  []string
	}{
		{
			// a, b, c are mutually above the threshold; d has no neighbour
			// above it and takes its single nearest (c).
			name: "threshold wins", units: units, o: EmbedOptions{MinCosine: 0.9, TopK: 1},
			want: []string{"a->b", "a->c", "b->c", "c->d"},
		},
		{
			// nobody is above 0.9999, so every unit takes its top 3.
			name: "topK wins", units: units, o: EmbedOptions{MinCosine: 0.9999, TopK: 3},
			want: []string{"a->b", "a->c", "a->d", "b->c", "b->d", "c->d"},
		},
		{
			// a and b share a shape hash: excluded before ranking, so a's
			// and b's nearest other neighbour is taken instead.
			name: "shape hash excluded", units: shaped, o: EmbedOptions{MinCosine: 0.9999, TopK: 1},
			want: []string{"a->c", "b->c", "c->d"},
		},
		{
			name: "defaults", units: units, o: EmbedOptions{},
			want: []string{"a->b", "a->c", "a->d", "b->c", "b->d", "c->d"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			links := EmbedCandidates(tc.units, vecs, tc.o)
			var got []string
			for _, l := range links {
				got = append(got, l.From+"->"+l.To)
				if l.Kind != model.LinkEmbedNeighbor || l.Source != model.LinkEmbed {
					t.Errorf("%v: wrong kind/source", l)
				}
				c, _ := cosine(vecs[l.From], vecs[l.To])
				if math.Abs(l.Weight-c) > 1e-12 {
					t.Errorf("%v: weight %v, want cosine %v", l, l.Weight, c)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			again := EmbedCandidates(reverse(tc.units), vecs, tc.o)
			if !reflect.DeepEqual(links, again) {
				t.Errorf("not deterministic under input order")
			}
		})
	}
}

func TestEmbedCandidatesEdgeCases(t *testing.T) {
	units := vecUnits(3)
	if got := EmbedCandidates(units, nil, EmbedOptions{}); len(got) != 0 {
		t.Errorf("no vectors: got %v", got)
	}
	mismatched := map[string][]float32{"a": {1, 0}, "b": {1, 0, 0}}
	if got := EmbedCandidates(units, mismatched, EmbedOptions{}); len(got) != 0 {
		t.Errorf("mismatched dims: got %v", got)
	}
	ties := map[string][]float32{"a": {1, 0}, "b": {1, 0}, "c": {1, 0}}
	got := EmbedCandidates(units, ties, EmbedOptions{MinCosine: 0.99, TopK: 1})
	if len(got) != 3 {
		t.Errorf("identical vectors: want all 3 pairs once, got %v", got)
	}
}

func reverse(units []model.ChangeUnit) []model.ChangeUnit {
	out := make([]model.ChangeUnit, len(units))
	for i, u := range units {
		out[len(units)-1-i] = u
	}
	return out
}

func TestMechanicalCandidates(t *testing.T) {
	units := []model.ChangeUnit{
		{ID: "u3", Order: 3, ShapeHash: "h1"},
		{ID: "u1", Order: 1, ShapeHash: "h1"},
		{ID: "u5", Order: 5, ShapeHash: "h1"},
		{ID: "u2", Order: 2, ShapeHash: "h2"},
		{ID: "u0", Order: 0},
		{ID: "u4", Order: 4},
		{ID: "u6", Order: 6, ShapeHash: "h3"},
		{ID: "u7", Order: 7, ShapeHash: "h3"},
	}
	links := MechanicalCandidates(units)
	var got []string
	for _, l := range links {
		got = append(got, l.From+"->"+l.To)
		if l.Kind != model.LinkMechanical || l.Source != model.LinkEmbed || l.Weight != 1 {
			t.Errorf("%v: wrong kind/source/weight", l)
		}
	}
	want := []string{"u1->u3", "u1->u5", "u6->u7"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if !reflect.DeepEqual(links, MechanicalCandidates(reverse(units))) {
		t.Errorf("not deterministic under input order")
	}
}

// A shape class whose lowest-Order unit makes another literal edit: each
// literal edit gets its own star, and the class root links to the other
// partition roots (for Jev).
func TestMechanicalCandidatesPartitions(t *testing.T) {
	odd := "-\tfoo(x)\n+\tbar(x)\n"
	ren := "-\tgetUser(id)\n+\tfetchUser(id)\n"
	other := "-\tgetA(id)\n+\tfetchA(id)\n"
	units := []model.ChangeUnit{
		{ID: "a", Order: 0, ShapeHash: "s", Diff: odd},
		{ID: "b", Order: 1, ShapeHash: "s", Diff: ren},
		{ID: "c", Order: 2, ShapeHash: "s", Diff: ren},
		{ID: "d", Order: 3, ShapeHash: "s", Diff: other},
		{ID: "e", Order: 4, ShapeHash: "s", Diff: ren},
		{ID: "f", Order: 5, ShapeHash: "s", Diff: other},
	}
	var got []string
	for _, l := range MechanicalCandidates(units) {
		got = append(got, l.From+"->"+l.To)
	}
	want := []string{"a->b", "a->d", "b->c", "b->e", "d->f"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
