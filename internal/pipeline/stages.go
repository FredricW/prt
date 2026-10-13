package pipeline

import (
	"context"

	"github.com/fredricw/prt/internal/aggregate"
	"github.com/fredricw/prt/internal/cluster"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/judge"
	"github.com/fredricw/prt/internal/link"
	"github.com/fredricw/prt/internal/model"
)

// stageFuncs are the pure stage implementations the pipeline wires (link,
// cluster, judge, aggregate). They live on the Pipeline rather than in
// package-level variables so tests can substitute any of them per Pipeline
// without global mutable state; New fills them with the real packages.
type stageFuncs struct {
	structural func(context.Context, link.Input) ([]model.Link, error)
	embedCands func([]model.ChangeUnit, map[string][]float32, link.EmbedOptions) []model.Link
	mechanical func([]model.ChangeUnit) []model.Link
	cluster    func(cluster.Input) cluster.Result

	pairs   func(context.Context, jev.Client, judge.PairInput) (judge.Result, error)
	confirm func([]model.Link, []model.Label, float64) ([]model.Link, int)

	names      func(context.Context, jev.Client, judge.NameInput) (judge.Result, error)
	nameCtx    func(string, []string) string
	applyNames func([]model.Group, []model.Label, []string, string) []model.Group

	separatePR func(context.Context, jev.Client, judge.SplitInput) (judge.Result, error)
	splitCtx   func([]judge.GroupSummary) string
	suggestion func(string, []model.ChangeUnit, []model.Group, []model.Label, string) *model.SplitSuggestion

	aggregate func(aggregate.Input) model.ItemAgg
}

// defaultStages returns the real stage implementations.
func defaultStages() stageFuncs {
	return stageFuncs{
		structural: link.Structural,
		embedCands: link.EmbedCandidates,
		mechanical: link.MechanicalCandidates,
		cluster:    cluster.Cluster,
		pairs:      judge.Pairs,
		confirm:    judge.Confirm,
		names:      judge.Names,
		nameCtx:    judge.NameCtx,
		applyNames: judge.ApplyNames,
		separatePR: judge.SeparatePR,
		splitCtx:   judge.SplitCtx,
		suggestion: judge.Suggestion,
		aggregate:  aggregate.Item,
	}
}
