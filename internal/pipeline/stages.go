package pipeline

import (
	"context"

	"github.com/fredricw/prt/internal/aggregate"
	"github.com/fredricw/prt/internal/arch"
	"github.com/fredricw/prt/internal/cluster"
	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/judge"
	"github.com/fredricw/prt/internal/link"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/route"
)

// stageFuncs are the pure stage implementations the pipeline wires (link,
// cluster, judge, route, review prompts, aggregate). They live on the
// Pipeline rather than in package-level variables so tests can substitute
// any of them per Pipeline without global mutable state; New fills them
// with the real packages.
type stageFuncs struct {
	structural func(context.Context, link.Input) ([]model.Link, error)
	embedCands func([]model.ChangeUnit, map[string][]float32, link.EmbedOptions) []model.Link
	mechanical func([]model.ChangeUnit) []model.Link
	cluster    func(context.Context, cluster.Input) (cluster.Result, error)

	pairs   func(context.Context, jev.Client, judge.PairInput) (judge.Result, error)
	confirm func([]model.Link, []model.Label, float64) ([]model.Link, int)

	names      func(context.Context, jev.Client, judge.NameInput) (judge.Result, error)
	nameCtx    func(string, []string) string
	applyNames func([]model.Group, []model.Label, []string, string) []model.Group

	separatePR func(context.Context, jev.Client, judge.SplitInput) (judge.Result, error)
	splitCtx   func([]judge.GroupSummary) string
	suggestion func(string, []model.ChangeUnit, []model.Group, []model.Label, string) *model.SplitSuggestion

	// Stage 6 (route).
	compile   func(config.Routing) (route.Rules, error)
	route     func(route.Rules, []route.GroupFacts) []route.Decision
	gather    func([]string, model.Group, []model.ChangeUnit, []model.Link, []model.UnitAgg) []model.ContextBlock
	plan      func(route.PlanInput) []route.Planned
	newBudget func(capUSD, spent float64) route.Budget

	// Stage 7 (review) and the prose calls of stages 5 and 8.
	buildReview     func(review.GroupInput) review.Request
	parseReview     func(string, []model.ChangeUnit) ([]model.Finding, string, float64, error)
	buildTLDR       func(model.ItemAgg) review.Request
	cleanTLDR       func(string) string
	buildGroupTitle func(model.Group, []model.ChangeUnit) review.Request
	cleanTitle      func(string) string

	// Stage 8 extras: the suggested PR title of a branch, the branch state
	// question and the Architecture tab's module graph (M5).
	buildPRTitle func([]string, string) review.Request
	cleanPRTitle func(string) string
	branchState  func(context.Context, jev.Client, judge.BranchStateInput) (judge.Result, error)
	archBuild    func(context.Context, arch.Input) (*model.ArchGraph, error)

	aggregate func(aggregate.Input) model.ItemAgg
}

// defaultStages returns the real stage implementations.
func defaultStages() stageFuncs {
	return stageFuncs{
		structural: link.Structural,
		embedCands: link.EmbedCandidates,
		mechanical: link.MechanicalCandidates,
		cluster:    cluster.ClusterContext,
		pairs:      judge.Pairs,
		confirm:    judge.Confirm,
		names:      judge.Names,
		nameCtx:    judge.NameCtx,
		applyNames: judge.ApplyNames,
		separatePR: judge.SeparatePR,
		splitCtx:   judge.SplitCtx,
		suggestion: judge.Suggestion,

		compile:   route.Compile,
		route:     route.Route,
		gather:    route.Gather,
		plan:      route.Plan,
		newBudget: route.NewBudget,

		buildReview:     review.BuildReview,
		parseReview:     review.ParseReview,
		buildTLDR:       review.BuildTLDR,
		cleanTLDR:       review.CleanTLDR,
		buildGroupTitle: review.BuildGroupTitle,
		cleanTitle:      review.CleanTitle,

		buildPRTitle: review.BuildPRTitle,
		cleanPRTitle: review.CleanPRTitle,
		branchState:  judge.BranchState,
		archBuild:    arch.Build,

		aggregate: aggregate.Item,
	}
}
