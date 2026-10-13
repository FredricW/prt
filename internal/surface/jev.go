package surface

import (
	"github.com/fredricw/prt/internal/model"
)

// jevMinP is the probability a contract label needs to add or confirm an
// entry (the M5.2 headline threshold: claims below it are unsure).
const jevMinP = 0.60

// Contract label values that map to a block (catalogue question
// `contract`).
var contractAreas = map[string]string{
	"api shape":        model.SurfaceAPI,
	"schema/migration": model.SurfaceDB,
	"env/flag":         model.SurfaceFlag,
}

// mergeJev applies the units' contract labels: a code entry in a unit whose
// label names its area (P ≥ jevMinP) becomes code+jev with confidence
// max(code, P); a unit so labelled with no code entry in that area gets a
// Jev-only entry ("contract change", confidence P), unless the unit is in a
// test file, is pure formatting or is noise by its aggregate (formatting,
// generated, moved).
func (b *builder) mergeJev(api, db, flags []model.SurfaceEntry) ([]model.SurfaceEntry, []model.SurfaceEntry, []model.SurfaceEntry) {
	labels := contractLabels(b.in.Labels)
	if len(labels) == 0 {
		return api, db, flags
	}
	noise := map[string]bool{}
	for _, a := range b.in.Aggs {
		noise[a.UnitID] = a.Noise
	}
	lists := map[string]*[]model.SurfaceEntry{model.SurfaceAPI: &api, model.SurfaceDB: &db, model.SurfaceFlag: &flags}
	has := map[string]map[string]bool{} // area → unit ids with a code entry
	for area, es := range lists {
		has[area] = map[string]bool{}
		for i := range *es {
			e := &(*es)[i]
			has[area][e.UnitID] = true
			if l, ok := labels[e.UnitID]; ok && contractAreas[l.Value] == area {
				e.Source, e.Confidence = model.SurfaceFromBoth, max(e.Confidence, l.P)
			}
		}
	}
	for _, v := range b.views {
		l, ok := labels[v.u.ID]
		area := contractAreas[l.Value]
		if !ok || area == "" || v.f.test || v.u.Format || noise[v.u.ID] || has[area][v.u.ID] {
			continue
		}
		if e, ok := jevEntry(v, area, l.P); ok {
			*lists[area] = append(*lists[area], e)
			has[area][v.u.ID] = true
		}
	}
	return api, db, flags
}

// contractLabels returns each unit's contract label with P ≥ jevMinP
// (unknown answers left out; the most probable when a unit has several).
func contractLabels(ls []model.Label) map[string]model.Label {
	out := map[string]model.Label{}
	for _, l := range ls {
		if l.Question != model.QContract || l.Line != 0 || model.IsUnknown(l) || l.P < jevMinP {
			continue
		}
		if cur, ok := out[l.UnitID]; !ok || l.P > cur.P {
			out[l.UnitID] = l
		}
	}
	return out
}

// jevEntry is the Jev-only entry of a unit: named by its symbol, else its
// signature, else its path, at its first changed line; ok is false for a
// unit with none of them (nothing to show).
func jevEntry(v *view, area string, p float64) (model.SurfaceEntry, bool) {
	name := v.u.Symbol
	if name == "" {
		name = v.u.Signature
	}
	if name == "" {
		name = v.u.Path
	}
	if name == "" {
		return model.SurfaceEntry{}, false
	}
	e := newEntry(area, model.SurfaceChanged, whatContract, name, firstChange(v), p, "")
	e.Source = model.SurfaceFromJev
	return e, true
}
