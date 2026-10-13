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
// Current state: fetch (local git, GitHub diff fallback), parse (hunk
// fallback) and aggregate (line counts, M0 urgency) run; other stages are
// recorded as skipped. TODO(M1–M3).
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
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
	// Emit receives every RunLog row (may be nil). It is never called while
	// ItemLock is held.
	Emit func(model.RunLog)
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
	// ItemLock, when set, is held around every read-modify-write of a stored
	// Item so the pipeline does not lose concurrent updates made by its host
	// (the daemon's state changes and discovery).
	ItemLock sync.Locker
}

// RunOptions tune one run.
type RunOptions struct {
	// Force bypasses the fetch cache (re-fetches the diff even when one is
	// stored for the head SHA).
	Force bool
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
		d.Log = log.New(io.Discard)
	}
	if d.ItemLock == nil {
		d.ItemLock = &sync.Mutex{}
	}
	return &Pipeline{d: d}
}

// Run executes all stages for an item with default options.
func (p *Pipeline) Run(ctx context.Context, it model.Item) error {
	return p.RunWith(ctx, it, RunOptions{})
}

// RunWith executes all stages for an item. It returns the first hard error
// (fetch or parse); soft failures (Jev, embeddings, reviews) are logged in
// RunLog rows and the item still completes.
func (p *Pipeline) RunWith(ctx context.Context, it model.Item, opt RunOptions) error {
	_, err := p.RunHead(ctx, it, opt)
	return err
}

// RunHead is RunWith that also returns the head SHA the run analysed ("" if
// fetch failed), so the caller can tell whether the item moved meanwhile.
func (p *Pipeline) RunHead(ctx context.Context, it model.Item, opt RunOptions) (string, error) {
	f, err := runStage(ctx, p, it.ID, model.StageFetch, func(rl *model.RunLog) (model.Fetch, error) {
		return p.fetch(ctx, it, opt.Force, rl)
	})
	if err != nil {
		return "", err
	}
	return f.HeadSHA, p.runAfterFetch(ctx, it, f)
}

func (p *Pipeline) runAfterFetch(ctx context.Context, it model.Item, f model.Fetch) error {
	files, err := diff.Parse(f.Diff)
	if err != nil {
		p.d.Log.Warn("parse diff failed", "item", it.ID, "err", err)
		return fmt.Errorf("pipeline: parse diff: %w", err)
	}
	units, err := runStage(ctx, p, it.ID, model.StageParse, func(rl *model.RunLog) ([]model.ChangeUnit, error) {
		return p.parse(ctx, it.ID, f, files, rl)
	})
	if err != nil {
		return err
	}
	for _, s := range []model.Stage{model.StageLink, model.StageEmbed, model.StageJev, model.StageCluster, model.StageRoute, model.StageReview} {
		p.emit(model.RunLog{ItemID: it.ID, Stage: s, Status: model.StageSkipped, StartedAt: p.d.Now(), Note: "not implemented yet"})
	}
	_, err = runStage(ctx, p, it.ID, model.StageAggregate, func(rl *model.RunLog) (model.ItemAgg, error) {
		return p.aggregate(ctx, it, f, units, rl)
	})
	return err
}

// parse splits the diff into change units and stores files and units.
func (p *Pipeline) parse(ctx context.Context, itemID string, f model.Fetch, files []diff.File, rl *model.RunLog) ([]model.ChangeUnit, error) {
	us, err := parse.Split(ctx, parse.Input{ItemID: itemID, BaseSHA: f.BaseSHA, HeadSHA: f.HeadSHA, Files: files, Src: p.d.Git})
	if err != nil {
		return nil, fmt.Errorf("pipeline: split %s: %w", itemID, err)
	}
	rl.In, rl.Out = len(files), len(us)
	if err := p.d.Store.PutFiles(ctx, itemID, toFiles(itemID, files)); err != nil {
		return nil, fmt.Errorf("pipeline: store files: %w", err)
	}
	if err := p.d.Store.PutUnits(ctx, itemID, us); err != nil {
		return nil, fmt.Errorf("pipeline: store units: %w", err)
	}
	return us, nil
}

// aggregate computes and stores the ItemAgg, then copies urgency, why and
// line counts onto the stored Item.
func (p *Pipeline) aggregate(ctx context.Context, it model.Item, f model.Fetch, units []model.ChangeUnit, rl *model.RunLog) (model.ItemAgg, error) {
	cur := p.currentItem(ctx, it)
	age, release, ciRed := aggregate.Facts(cur, p.d.Config.GitHub.ReleaseBranches, p.d.Now())
	agg := aggregate.Item(aggregate.Input{
		Item: cur, Units: units, Weights: p.d.Config.Rank, Pipeline: p.d.Config.Pipeline,
		AgeDays: age, Release: release, CIRedAfterApproval: ciRed,
	})
	rl.In = len(units)
	if err := p.d.Store.PutAgg(ctx, agg); err != nil {
		return agg, fmt.Errorf("pipeline: store agg: %w", err)
	}
	return agg, p.applyAgg(ctx, it, f, agg)
}

// currentItem returns the latest stored version of it (metadata may have
// been refreshed by discovery since the run started), or it itself.
func (p *Pipeline) currentItem(ctx context.Context, it model.Item) model.Item {
	if cur, err := p.d.Store.Item(ctx, it.ID); err == nil {
		return cur
	}
	return it
}

// applyAgg writes the aggregate's ranking fields into the stored item. An
// item that is not stored (removed while the run was in flight, or a run of
// an unstored item) is left alone.
func (p *Pipeline) applyAgg(ctx context.Context, it model.Item, f model.Fetch, agg model.ItemAgg) error {
	p.d.ItemLock.Lock()
	defer p.d.ItemLock.Unlock()
	cur, err := p.d.Store.Item(ctx, it.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pipeline: load item %s: %w", it.ID, err)
	}
	if cur.HeadSHA == "" {
		cur.HeadSHA = f.HeadSHA
	}
	if cur.HeadSHA == f.HeadSHA {
		cur.BaseSHA = f.BaseSHA
	}
	cur.Urgency, cur.Why, cur.Minutes = agg.Urgency, agg.Why, agg.Minutes
	cur.Additions, cur.Deletions = agg.Added, agg.Removed
	if err := p.d.Store.UpsertItem(ctx, cur); err != nil {
		return fmt.Errorf("pipeline: store item %s: %w", cur.ID, err)
	}
	return nil
}

// runStage wraps one stage with RunLog bookkeeping and logging.
func runStage[T any](ctx context.Context, p *Pipeline, itemID string, s model.Stage, fn func(*model.RunLog) (T, error)) (T, error) {
	rl := model.RunLog{ItemID: itemID, Stage: s, Status: model.StageRunning, StartedAt: p.d.Now()}
	p.d.Log.Debug("stage start", "item", itemID, "stage", s)
	p.emit(rl)
	v, err := fn(&rl)
	rl.Ms = p.d.Now().Sub(rl.StartedAt).Milliseconds()
	switch {
	case err != nil:
		rl.Status, rl.Err = model.StageFailed, err.Error()
		p.d.Log.Warn("stage failed", "item", itemID, "stage", s, "err", err)
	case rl.CacheHit:
		rl.Status = model.StageCached
	default:
		rl.Status = model.StageDone
	}
	p.d.Log.Debug("stage end", "item", itemID, "stage", s, "status", rl.Status, "ms", rl.Ms, "in", rl.In, "out", rl.Out, "note", rl.Note)
	p.emit(rl)
	if serr := p.d.Store.AppendRunLog(ctx, rl); serr != nil && err == nil {
		err = fmt.Errorf("pipeline: append run log: %w", serr)
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

// fetch resolves base/head and the diff, cached by head SHA. A PR whose head
// is not available locally falls back to GitHub's diff.
func (p *Pipeline) fetch(ctx context.Context, it model.Item, force bool, rl *model.RunLog) (model.Fetch, error) {
	head, err := p.resolveHead(ctx, it, rl)
	if err != nil {
		return model.Fetch{}, err
	}
	if !force {
		if f, ok, err := p.d.Store.Fetch(ctx, it.ID, head); err == nil && ok {
			rl.CacheHit = true
			return f, nil
		}
	}
	base, d, err := p.localDiff(ctx, it, head)
	if err != nil {
		if !p.canUseGitHub(it) {
			return model.Fetch{}, err
		}
		p.d.Log.Debug("local diff unavailable; using GitHub", "item", it.ID, "err", err)
		base, d, err = p.remoteDiff(ctx, it, rl)
		if err != nil {
			return model.Fetch{}, err
		}
	}
	it.HeadSHA, it.BaseSHA = head, base
	f := model.Fetch{ItemID: it.ID, HeadSHA: head, BaseSHA: base, Diff: d, Item: it, At: p.d.Now()}
	if err := p.d.Store.PutFetch(ctx, f); err != nil {
		return f, fmt.Errorf("pipeline: store fetch: %w", err)
	}
	return f, nil
}

func (p *Pipeline) canUseGitHub(it model.Item) bool {
	return it.Kind == model.KindPR && it.Number > 0 && p.d.GH != nil
}

// resolveHead returns the item's head SHA: stored, else the local ref, else
// (PRs) GitHub's view of the PR.
func (p *Pipeline) resolveHead(ctx context.Context, it model.Item, rl *model.RunLog) (string, error) {
	if it.HeadSHA != "" {
		return it.HeadSHA, nil
	}
	sha, err := p.d.Git.ResolveRef(ctx, it.Ref)
	if err == nil {
		return sha, nil
	}
	if !p.canUseGitHub(it) {
		return "", fmt.Errorf("%w: resolve %q: %w", ErrNoDiff, it.Ref, err)
	}
	rl.Calls++
	pr, gerr := p.d.GH.PR(ctx, it.Number)
	if gerr != nil || pr.HeadSHA == "" {
		return "", fmt.Errorf("%w: resolve %q: %w", ErrNoDiff, it.Ref, errors.Join(err, gerr))
	}
	return pr.HeadSHA, nil
}

// localDiff diffs head against its merge base with the item's base ref.
func (p *Pipeline) localDiff(ctx context.Context, it model.Item, head string) (string, string, error) {
	base := it.BaseRef
	if base == "" {
		b, err := p.d.Git.DefaultBranch(ctx)
		if err != nil {
			return "", "", fmt.Errorf("pipeline: default branch: %w", err)
		}
		base = b
	}
	base = gitx.CompareRef(ctx, p.d.Git, base)
	mb, err := p.d.Git.MergeBase(ctx, base, head)
	if err != nil {
		return "", "", fmt.Errorf("%w: merge-base %s %s: %w", ErrNoDiff, base, head, err)
	}
	d, err := p.d.Git.Diff(ctx, mb, head)
	if err != nil {
		return "", "", fmt.Errorf("%w: diff %s..%s: %w", ErrNoDiff, mb, head, err)
	}
	return mb, d, nil
}

// remoteDiff fetches the PR's diff from GitHub; the base SHA comes from the
// PR metadata (falling back to the item's stored base SHA).
func (p *Pipeline) remoteDiff(ctx context.Context, it model.Item, rl *model.RunLog) (string, string, error) {
	rl.Calls++
	d, err := p.d.GH.PRDiff(ctx, it.Number)
	if err != nil {
		return "", "", fmt.Errorf("%w: GitHub diff #%d: %w", ErrNoDiff, it.Number, err)
	}
	base := it.BaseSHA
	rl.Calls++
	if pr, err := p.d.GH.PR(ctx, it.Number); err == nil && pr.BaseSHA != "" {
		base = pr.BaseSHA
	}
	rl.Note = "diff from GitHub"
	return base, d, nil
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
