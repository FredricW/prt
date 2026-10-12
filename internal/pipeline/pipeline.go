// Package pipeline orchestrates the per-item stages (fetch, then 1–8:
// parse, link, embed, jev, cluster, route, review, aggregate). Each stage is
// a function from inputs to a stored output keyed by a hash of those inputs,
// so a rebase only re-runs what changed. Every stage emits a RunLog row
// through Deps.Emit as it starts and finishes, which feeds the Pipeline page
// and the log file.
//
// External calls go only through the interfaces in Deps, so the whole
// pipeline runs offline in tests with fakes or recordings.
//
// Current state: fetch, parse (hunk fallback) and aggregate (line counts) run;
// other stages are recorded as skipped. TODO(M1–M3).
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/charmbracelet/log"

	"github.com/fredricw/prt/internal/aggregate"
	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/parse"
	"github.com/fredricw/prt/internal/questions"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/store"
)

// Deps are the pipeline's collaborators. GH, Jev and Embedder may be nil:
// the pipeline then degrades as the spec's failure-handling section says.
type Deps struct {
	Git       gitx.Git
	GH        gh.Client
	Jev       jev.Client
	Embedder  embed.Embedder
	Reviewers review.Tiers
	Store     store.Store
	Config    config.Config
	Routing   config.Routing
	Questions questions.Catalogue
	Log       *log.Logger
	// Emit receives every RunLog row (may be nil).
	Emit func(model.RunLog)
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

// Pipeline runs stages for items.
type Pipeline struct {
	d Deps
}

// New builds a Pipeline.
func New(d Deps) *Pipeline {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = log.New(nil)
	}
	return &Pipeline{d: d}
}

// Run executes all stages for an item. It returns the first hard error
// (fetch or parse); soft failures (Jev, embeddings, reviews) are logged in
// RunLog rows and the item still completes.
func (p *Pipeline) Run(ctx context.Context, it model.Item) error {
	f, err := runStage(ctx, p, it.ID, model.StageFetch, func(rl *model.RunLog) (model.Fetch, error) {
		return p.fetch(ctx, it, rl)
	})
	if err != nil {
		return err
	}
	files, err := diff.Parse(f.Diff)
	if err != nil {
		return fmt.Errorf("pipeline: parse diff: %w", err)
	}
	units, err := runStage(ctx, p, it.ID, model.StageParse, func(rl *model.RunLog) ([]model.ChangeUnit, error) {
		us, err := parse.Split(ctx, parse.Input{ItemID: it.ID, BaseSHA: f.BaseSHA, HeadSHA: f.HeadSHA, Files: files, Src: p.d.Git})
		if err != nil {
			return nil, err
		}
		rl.In, rl.Out = len(files), len(us)
		if err := p.d.Store.PutFiles(ctx, it.ID, toFiles(it.ID, files)); err != nil {
			return nil, err
		}
		return us, p.d.Store.PutUnits(ctx, it.ID, us)
	})
	if err != nil {
		return err
	}
	for _, s := range []model.Stage{model.StageLink, model.StageEmbed, model.StageJev, model.StageCluster, model.StageRoute, model.StageReview} {
		p.emit(model.RunLog{ItemID: it.ID, Stage: s, Status: model.StageSkipped, StartedAt: p.d.Now(), Note: "not implemented yet"})
	}
	_, err = runStage(ctx, p, it.ID, model.StageAggregate, func(rl *model.RunLog) (model.ItemAgg, error) {
		agg := aggregate.Item(aggregate.Input{Item: f.Item, Units: units, Weights: p.d.Config.Rank, Pipeline: p.d.Config.Pipeline})
		rl.In = len(units)
		return agg, p.d.Store.PutAgg(ctx, agg)
	})
	return err
}

// runStage wraps one stage with RunLog bookkeeping.
func runStage[T any](ctx context.Context, p *Pipeline, itemID string, s model.Stage, fn func(*model.RunLog) (T, error)) (T, error) {
	rl := model.RunLog{ItemID: itemID, Stage: s, Status: model.StageRunning, StartedAt: p.d.Now()}
	p.emit(rl)
	v, err := fn(&rl)
	rl.Ms = p.d.Now().Sub(rl.StartedAt).Milliseconds()
	switch {
	case err != nil:
		rl.Status, rl.Err = model.StageFailed, err.Error()
	case rl.CacheHit:
		rl.Status = model.StageCached
	default:
		rl.Status = model.StageDone
	}
	p.emit(rl)
	if serr := p.d.Store.AppendRunLog(ctx, rl); serr != nil && err == nil {
		err = serr
	}
	return v, err
}

func (p *Pipeline) emit(rl model.RunLog) {
	if p.d.Emit != nil {
		p.d.Emit(rl)
	}
}

// ErrNoDiff is returned when neither local git nor GitHub can provide a diff.
var ErrNoDiff = errors.New("pipeline: no diff available")

// fetch resolves base/head and the diff, cached by head SHA.
func (p *Pipeline) fetch(ctx context.Context, it model.Item, rl *model.RunLog) (model.Fetch, error) {
	head := it.HeadSHA
	if head == "" {
		sha, err := p.d.Git.ResolveRef(ctx, it.Ref)
		if err != nil {
			return model.Fetch{}, fmt.Errorf("%w: %w", ErrNoDiff, err)
		}
		head = sha
	}
	if f, ok, err := p.d.Store.Fetch(ctx, it.ID, head); err == nil && ok {
		rl.CacheHit = true
		return f, nil
	}
	base := it.BaseRef
	if base == "" {
		b, err := p.d.Git.DefaultBranch(ctx)
		if err != nil {
			return model.Fetch{}, err
		}
		base = b
	}
	mb, err := p.d.Git.MergeBase(ctx, base, head)
	if err != nil {
		return model.Fetch{}, fmt.Errorf("%w: %w", ErrNoDiff, err)
	}
	d, err := p.d.Git.Diff(ctx, mb, head)
	if err != nil {
		return model.Fetch{}, fmt.Errorf("%w: %w", ErrNoDiff, err)
	}
	it.HeadSHA, it.BaseSHA = head, mb
	f := model.Fetch{ItemID: it.ID, HeadSHA: head, BaseSHA: mb, Diff: d, Item: it, At: p.d.Now()}
	return f, p.d.Store.PutFetch(ctx, f)
}

func toFiles(itemID string, fs []diff.File) []model.File {
	out := make([]model.File, 0, len(fs))
	for _, f := range fs {
		adds, dels := f.Counts()
		out = append(out, model.File{
			ItemID: itemID, Path: f.Path(), OldPath: f.OldPath, Added: f.Added, Deleted: f.Deleted,
			Renamed: f.Renamed, Binary: f.Binary, Lang: parse.Lang(f.Path()), Adds: adds, Dels: dels,
		})
	}
	return out
}
