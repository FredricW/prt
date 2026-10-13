// Package cluster is pipeline stage 5: connected components over structural
// and Jev-confirmed links, split of components larger than max_group_units by
// cutting the weakest Jev-confirmed (embed-derived) links, mechanical groups
// (count ≥ mechanical_min, members verified against the example's shape hash,
// non-matching members ejected), user merge/split edits, placeholder names
// and review order (interface units first, then callers, then tests).
//
// Jev naming (name_bullet) is done by the pipeline through internal/judge
// after clustering; this package only sets placeholder names, so it stays
// pure: no I/O, deterministic output.
package cluster

import "github.com/fredricw/prt/internal/model"

// Defaults applied when an Options field is ≤ 0.
const (
	DefaultMaxGroupUnits = 8
	DefaultMechanicalMin = 3
)

// Options are the clustering knobs from prt.toml [pipeline].
type Options struct {
	MaxGroupUnits int // max_group_units (default 8)
	MechanicalMin int // mechanical_min (default 3)
}

// withDefaults fills unset (≤ 0) knobs with their defaults.
func (o Options) withDefaults() Options {
	if o.MaxGroupUnits <= 0 {
		o.MaxGroupUnits = DefaultMaxGroupUnits
	}
	if o.MechanicalMin <= 0 {
		o.MechanicalMin = DefaultMechanicalMin
	}
	return o
}

// Input is everything stage 5 needs for one item.
type Input struct {
	ItemID string
	Units  []model.ChangeUnit
	// Links are the item's kept links: structural (Source structural),
	// Jev-confirmed (Source jev) and mechanical candidates (Source embed,
	// Kind mechanical). Unconfirmed embed-neighbour candidates (Source
	// embed, Kind embed-neighbour) are ignored.
	Links []model.Link
	// Labels are the units' labels (overrides applied); only the unit-scope
	// "interface" label is read (P(yes) ≥ 0.5 marks an interface unit).
	Labels []model.Label
	// Edits are the user's stored edits, applied in order after clustering.
	Edits   []model.GroupEdit
	Options Options
}

// Result is the stage output.
type Result struct {
	// Groups are in review order with Order = index: groups with an
	// interface unit, then other code groups, then test-only groups, then
	// mechanical groups, then ungrouped singletons. Every unit of the item
	// is in exactly one group. Names are placeholders (NameSource
	// model.NamePlaceholder).
	Groups []model.Group
	// Stats fills the cluster part of model.GroupingStats: Units, CutLinks,
	// Groups, Mechanical, MechanicalCollapsed, Ejected, Ungrouped,
	// UserEdits. The pipeline fills the link and Jev counts.
	Stats model.GroupingStats
}

// Cluster groups an item's units using its links: mechanical groups first,
// then connected components over structural and Jev-confirmed links (with
// oversized components split at their weakest Jev links), then the user's
// edits. It is pure and deterministic: the result depends only on unit
// Order (ties by id) and link content, never on input or map order.
func Cluster(in Input) Result {
	opts := in.Options.withDefaults()
	g := newGraph(in.Units, in.Links)

	mech := mechanicalGroups(g, opts.MechanicalMin)
	inMech := make([]bool, len(g.units))
	for _, m := range mech {
		for _, u := range m.units {
			inMech[u] = true
		}
	}

	parts, cut := splitComponents(g, inMech, opts.MaxGroupUnits)
	groups := make([]*group, 0, len(mech)+len(parts))
	groups = append(groups, mech...)
	for _, p := range parts {
		groups = append(groups, &group{units: p, example: -1})
	}

	groups, applied := applyEdits(g, groups, in.Edits, opts.MechanicalMin)

	b := builder{g: g, cut: cut, iface: interfaceLabels(in.Labels)}
	out := b.build(in.ItemID, groups)
	stats := statsOf(out)
	stats.Units = len(g.units)
	stats.CutLinks = len(cut)
	stats.UserEdits = applied
	return Result{Groups: out, Stats: stats}
}

// statsOf counts the groups of a result.
func statsOf(groups []model.Group) model.GroupingStats {
	var s model.GroupingStats
	for _, gr := range groups {
		s.Ejected += len(gr.Ejected)
		switch {
		case gr.Ungrouped:
			s.Ungrouped++
			continue
		case gr.Mechanical:
			s.Mechanical++
			s.MechanicalCollapsed += gr.Count - 1
		}
		s.Groups++
	}
	return s
}
