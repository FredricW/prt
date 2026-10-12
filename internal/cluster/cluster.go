// Package cluster is pipeline stage 5: connected components over structural
// and Jev-confirmed links, split of components larger than max_group_units by
// cutting the weakest embed links, mechanical groups (count ≥ mechanical_min,
// members verified against the example's shape hash, non-matching members
// ejected), and review order (interface units first, then callers, then tests).
// Naming (Jev name_bullet choice, else a fast-LLM three-word title) is done by
// the pipeline, not here, so this package stays pure.
//
// TODO(M2): implement Cluster. The placeholder puts every unit in its own group.
package cluster

import "github.com/fredricw/prt/internal/model"

// Options are the clustering knobs from prt.toml [pipeline].
type Options struct {
	MaxGroupUnits int
	MechanicalMin int
}

// Cluster groups units using links. Labels are passed for ordering (the
// "interface" label). Placeholder: one group per unit.
func Cluster(itemID string, units []model.ChangeUnit, _ []model.Link, _ []model.Label, _ Options) []model.Group {
	groups := make([]model.Group, 0, len(units))
	for i, u := range units {
		ids := []string{u.ID}
		groups = append(groups, model.Group{
			ID: model.GroupID(ids), ItemID: itemID, UnitIDs: ids, Name: u.Path, Order: i,
		})
	}
	return groups
}
