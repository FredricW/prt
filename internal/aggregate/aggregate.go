// Package aggregate is pipeline stage 8: plain arithmetic over labels,
// reviews and git facts (spec: "Ranking and aggregation"). It computes
// UnitAgg, GroupAgg, ItemAgg and the urgency score with per-term
// contributions. No I/O, no model calls; the TL;DR is filled in by the
// pipeline from the returned record.
//
// TODO(M1): composition, headlines, risk, test gap, attention/minutes.
// TODO(M3): hot spots with findings, urgency with weights and "why".
package aggregate

import (
	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

// Input is everything aggregate reads.
type Input struct {
	Item               model.Item
	Files              []model.File
	Units              []model.ChangeUnit
	Labels             []model.Label // with overrides already applied
	Groups             []model.Group
	Reviews            []model.Review
	Weights            config.Rank
	Pipeline           config.Pipeline
	AgeDays            float64
	Release            bool // base is a release branch
	CIRedAfterApproval bool
}

// Item computes the overview record. Placeholder: line counts only.
func Item(in Input) model.ItemAgg {
	agg := model.ItemAgg{ItemID: in.Item.ID, UnitsTotal: len(in.Units)}
	for _, u := range in.Units {
		agg.Added += u.Adds
		agg.Removed += u.Dels
	}
	return agg
}
