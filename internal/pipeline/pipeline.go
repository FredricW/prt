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
// Current state (M3): fetch (local git, GitHub diff fallback), parse (with
// file ownership from user.owns and CODEOWNERS), link (structural links),
// embed (vectors cached by unit id and model; kNN and mechanical
// candidates), jev (labels cached by unit id, and pair relations cached on
// pair subjects, in parallel), cluster (groups, Jev names, split
// suggestion, fast-LLM titles for unnamed groups), route (prt.routing.toml
// rules, user overrides, context, cost-capped plan), review (fast/deep
// calls with retry, escalation and calibration) and aggregate (with the
// TL;DR, review totals and calibration panel) run. Failures after parse are
// soft: the item still aggregates (Jev down: unknown labels and
// LabelsMissing; no embeddings: groups from structural links only, with a
// note; no review key: rows skipped with a note).
//
// A stage function marks its row skipped (ran, but had nothing it could do,
// e.g. no embedding model) with skipStage; runStage keeps that status.
//
// Cache namespaces (Store.CacheGet/CachePut) owned by this package:
// nsOverride (per-group D/s overrides), nsCallLog (raw LLM calls per
// routing row), nsTitle (group titles by request key) and nsTLDR (TL;DR
// text by request key).
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
	"github.com/fredricw/prt/internal/labels"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/parse"
	"github.com/fredricw/prt/internal/questions"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/route"
	"github.com/fredricw/prt/internal/store"
)

// Deps are the pipeline's collaborators. GH, Jev, Embedder and the
// reviewers may be nil: the pipeline then degrades as the spec's
// failure-handling section says.
type Deps struct {
	Git       gitx.Git
	GH        gh.Client
	Jev       jev.Client
	Embedder  embed.Embedder
	Reviewers review.Tiers
	Store     store.Store
	Config    config.Config
	// Routing is prt.routing.toml; a value without rules (the zero value)
	// means config.DefaultRouting(). SetRouting swaps it at runtime.
	Routing config.Routing
	// ConfigDirs are the config lookup directories (config.Dirs: the repo's
	// .git/prt first). The pipeline does not read them; the daemon uses
	// them to find, write (Ensure) and reload prt.routing.toml. May be nil.
	ConfigDirs []string
	Questions  questions.Catalogue
	Log        *log.Logger
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
	// stored for the head SHA) and regenerates the TL;DR (spec Overview:
	// "`r` forces it").
	Force bool
	// FromStage, when set, makes that stage and every later one ignore its
	// cache (Pipeline page `r`): fetch re-fetches, cluster re-asks group
	// titles, review re-calls every routed group and aggregate regenerates
	// the TL;DR. Parse, link and route have no cache; embeddings, Jev
	// labels, pairs, names and split answers are content-keyed and kept
	// (DECISIONS "Pipeline stages 6–8 (M3)").
	FromStage model.Stage
	// KeepReviewed is set by a routing-rules reload (spec: "saving re-routes
	// unreviewed groups"): a group whose stored routing row is done keeps
	// that row's decision (tier, rule, prompts, context) instead of being
	// routed by the new rules, unless the user overrode it (D/s) or the run
	// ignores the review cache. Queued, failed and skipped rows are routed
	// again.
	KeepReviewed bool
}

// ignoresCache reports whether stage s must not use its cache in this run.
func (o RunOptions) ignoresCache(s model.Stage) bool {
	if o.FromStage == "" {
		return false
	}
	return stageIndex(s) >= stageIndex(o.FromStage)
}

// stageIndex is s's position in model.Stages (len(model.Stages) when
// unknown, so an unknown FromStage ignores nothing).
func stageIndex(s model.Stage) int {
	for i, st := range model.Stages {
		if st == s {
			return i
		}
	}
	return len(model.Stages)
}

// ValidStage reports whether s names a pipeline stage.
func ValidStage(s model.Stage) bool { return stageIndex(s) < len(model.Stages) }

// Pipeline runs stages for items.
type Pipeline struct {
	d  Deps
	fn stageFuncs // stage implementations (substitutable in tests)

	mu    sync.Mutex
	login string // cached GitHub viewer login (see viewer)

	// emitMu serialises Deps.Emit: the review stage reports progress from
	// several goroutines.
	emitMu sync.Mutex

	rulesMu sync.RWMutex
	rules   route.Rules // compiled Deps.Routing (see SetRouting)
}

// New builds a Pipeline. The routing rules are compiled once here; when
// they do not compile, the built-in rules are used and a warning logged
// (SetRouting reports such errors to the caller instead).
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
	if len(d.Routing.Rules) == 0 {
		d.Routing = config.DefaultRouting()
	}
	p := &Pipeline{d: d, fn: defaultStages()}
	p.rules = p.initialRules(d.Routing)
	return p
}

// run is the state one RunHead call shares between the stages after fetch.
type run struct {
	it    model.Item
	f     model.Fetch
	opt   RunOptions
	calls *callLog
	// titleCost is what the current groups' titles cost when they were
	// asked (cache hits included); the route plan counts it against the
	// item's cap.
	titleCost float64
	// jevCalls and jevCost are this run's stage-4 totals (ReviewStats).
	jevCalls int
	jevCost  float64
}

// analysis is what stages 1–5 produced for the later stages.
type analysis struct {
	parsed parseOut
	jo     jevOut
	gro    groupOut
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
		return p.fetch(ctx, it, opt.Force || opt.ignoresCache(model.StageFetch), rl)
	})
	if err != nil {
		return "", err
	}
	r := &run{it: it, f: f, opt: opt, calls: newCallLog()}
	return f.HeadSHA, p.runAfterFetch(ctx, r)
}

func (p *Pipeline) runAfterFetch(ctx context.Context, r *run) error {
	it, f := r.it, r.f
	files, err := diff.Parse(f.Diff)
	if err != nil {
		p.d.Log.Warn("parse diff failed", "item", it.ID, "err", err)
		return fmt.Errorf("pipeline: parse diff: %w", err)
	}
	parsed, err := runStage(ctx, p, it.ID, model.StageParse, func(rl *model.RunLog) (parseOut, error) {
		return p.parse(ctx, it.ID, f, files, rl)
	})
	if err != nil {
		return err
	}
	in := clusterIn{parsed: parsed}
	in.structural, err = runStage(ctx, p, it.ID, model.StageLink, func(rl *model.RunLog) ([]model.Link, error) {
		return p.linkStage(ctx, it.ID, f, parsed, rl)
	})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		p.d.Log.Warn("structural links failed; continuing without them", "item", it.ID, "err", err)
		in.structural = nil
	}
	in.em, err = runStage(ctx, p, it.ID, model.StageEmbed, func(rl *model.RunLog) (embedOut, error) {
		return p.embedStage(ctx, parsed.units, rl)
	})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		p.d.Log.Warn("embeddings failed; groups from structural links only", "item", it.ID, "err", err)
	}
	jo, err := runStage(ctx, p, it.ID, model.StageJev, func(rl *model.RunLog) (jevOut, error) {
		out, err := p.jevStage(ctx, p.currentItem(ctx, it), parsed, in.structural, in.em, rl)
		r.jevCalls, r.jevCost = rl.Calls, rl.Cost
		return out, err
	})
	if err != nil {
		// Soft failure (spec: failure handling): the RunLog row carries the
		// error and the item still aggregates with unknown labels.
		if ctx.Err() != nil {
			return err
		}
		p.d.Log.Warn("labels failed; continuing with unknown labels", "item", it.ID, "err", err)
		jo.missing = true
	}
	in.pairs = jo.pairs
	gro, err := runStage(ctx, p, it.ID, model.StageCluster, func(rl *model.RunLog) (groupOut, error) {
		return p.clusterStage(ctx, r, p.currentItem(ctx, it), in, rl)
	})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		p.d.Log.Warn("clustering failed; no groups", "item", it.ID, "err", err)
		gro = groupOut{links: in.keptLinks(), noStructural: in.structural == nil}
		p.clearGroups(ctx, it.ID, gro.links)
	}
	return p.runReviewStages(ctx, r, analysis{parsed: parsed, jo: jo, gro: gro})
}

// runReviewStages runs stages 6–8. Route and review failures are soft: the
// item still aggregates (with whatever rows exist).
func (p *Pipeline) runReviewStages(ctx context.Context, r *run, a analysis) error {
	itemID := r.it.ID
	ro, err := runStage(ctx, p, itemID, model.StageRoute, func(rl *model.RunLog) (*routeOut, error) {
		return p.routeStage(ctx, r, a, rl)
	})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		p.d.Log.Warn("routing failed; no reviews", "item", itemID, "err", err)
		ro = &routeOut{failed: true, capUSD: capFor(p.currentItem(ctx, r.it), p.Routing().Config)}
		if serr := p.d.Store.PutItemReviews(ctx, itemID, nil); serr != nil {
			p.d.Log.Warn("clear routing rows failed", "item", itemID, "err", serr)
		}
	}
	rv, err := runStage(ctx, p, itemID, model.StageReview, func(rl *model.RunLog) (reviewOut, error) {
		return p.reviewStage(ctx, r, ro, rl)
	})
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		p.d.Log.Warn("reviews failed; aggregating what landed", "item", itemID, "err", err)
	}
	_, err = runStage(ctx, p, itemID, model.StageAggregate, func(rl *model.RunLog) (model.ItemAgg, error) {
		return p.aggregate(ctx, r, a, ro, rv, rl)
	})
	return err
}

// clearGroups replaces the item's stored groups (none) and links (this
// run's) after stage 5 failed, so Detail does not serve the previous head's
// groups next to an aggregate without grouping. A store error here is only
// logged: the stage already failed.
func (p *Pipeline) clearGroups(ctx context.Context, itemID string, links []model.Link) {
	if err := p.d.Store.PutGroups(ctx, itemID, nil); err != nil {
		p.d.Log.Warn("clear stale groups failed", "item", itemID, "err", err)
	}
	if err := p.d.Store.PutLinks(ctx, itemID, links); err != nil {
		p.d.Log.Warn("store links failed", "item", itemID, "err", err)
	}
}

// parseOut is the output of stage 1: the item's files (with ownership) and
// change units.
type parseOut struct {
	files []model.File
	units []model.ChangeUnit
}

// parse splits the diff into change units, marks owned files and stores
// files and units.
func (p *Pipeline) parse(ctx context.Context, itemID string, f model.Fetch, files []diff.File, rl *model.RunLog) (parseOut, error) {
	us, err := parse.Split(ctx, parse.Input{ItemID: itemID, BaseSHA: f.BaseSHA, HeadSHA: f.HeadSHA, Files: files, Src: p.d.Git})
	if err != nil {
		return parseOut{}, fmt.Errorf("pipeline: split %s: %w", itemID, err)
	}
	rl.In, rl.Out = len(files), len(us)
	fs := toFiles(itemID, files)
	markOwned(fs, p.ownershipFor(ctx, f.HeadSHA))
	if err := p.d.Store.PutFiles(ctx, itemID, fs); err != nil {
		return parseOut{}, fmt.Errorf("pipeline: store files: %w", err)
	}
	if err := p.d.Store.PutUnits(ctx, itemID, us); err != nil {
		return parseOut{}, fmt.Errorf("pipeline: store units: %w", err)
	}
	return parseOut{files: fs, units: us}, nil
}

// aggInput is aggregate.Input for the item's current stored metadata, its
// stored labels (current Ctx, overrides applied), groups and links; Reviews
// are left to the caller.
func (p *Pipeline) aggInput(ctx context.Context, r *run, a analysis) (aggregate.Input, error) {
	cur := p.currentItem(ctx, r.it)
	ls, err := p.itemLabels(ctx, cur, a.parsed.files, a.parsed.units)
	if err != nil {
		return aggregate.Input{}, err
	}
	age, release, ciRed := aggregate.Facts(cur, p.d.Config.GitHub.ReleaseBranches, p.d.Now())
	return aggregate.Input{
		Item: cur, Files: a.parsed.files, Units: a.parsed.units, Labels: ls,
		Groups: a.gro.groups, Links: a.gro.aggLinks(),
		Weights: p.d.Config.Rank, Pipeline: p.d.Config.Pipeline,
		AgeDays: age, Release: release, CIRedAfterApproval: ciRed,
		Owns: p.d.Config.User.Owns, LabelsMissing: a.jo.missing,
	}, nil
}

// aggregate is stage 8: it computes the ItemAgg from files, units, groups,
// links, the stored labels (overrides applied) and the routing rows, adds
// the grouping record and split suggestion from stage 5, the TL;DR, the
// review totals and the calibration panel, stores it with the raw call
// log, then copies the picker fields onto the stored Item.
func (p *Pipeline) aggregate(ctx context.Context, r *run, a analysis, ro *routeOut, rv reviewOut, rl *model.RunLog) (model.ItemAgg, error) {
	in, err := p.aggInput(ctx, r, a)
	if err != nil {
		return model.ItemAgg{}, err
	}
	rows := rv.rows
	if rows == nil {
		rows = ro.rows
	}
	in.Reviews = rows
	agg := p.fn.aggregate(in)
	agg.Grouping, agg.Split = a.gro.stats, a.gro.split
	rl.Note = p.tldr(ctx, r, &agg, rl)
	agg.Review = p.reviewStats(r, a.gro.groups, rows, ro, rv)
	if r.jevCalls == 0 && !a.jo.missing {
		p.carryJevStats(ctx, r.it.ID, agg.Review)
	}
	agg.Calibration = p.calibrationStats(ctx)
	rl.In, rl.Out = len(a.parsed.units), len(in.Labels)
	if err := r.calls.flush(ctx, p.d.Store, r.it.ID, groupIDs(a.gro.groups, "")); err != nil {
		p.d.Log.Warn("store call log failed", "item", r.it.ID, "err", err)
	}
	if err := p.d.Store.PutAgg(ctx, agg); err != nil {
		return agg, fmt.Errorf("pipeline: store agg: %w", err)
	}
	return agg, p.applyAgg(ctx, r.it, r.f, agg)
}

// carryJevStats fills s's Jev totals from the item's stored aggregate when
// this run's Jev stage made no call (every label cached): the page then
// shows what the labels in use cost when they were asked, marked cached.
func (p *Pipeline) carryJevStats(ctx context.Context, itemID string, s *model.ReviewStats) {
	prev, ok, err := p.d.Store.Agg(ctx, itemID)
	if err != nil || !ok || prev.Review == nil || (prev.Review.JevCalls == 0 && !prev.Review.JevCached) {
		return
	}
	s.JevCalls, s.JevCost, s.JevCached = prev.Review.JevCalls, prev.Review.JevCost, true
}

// groupIDs returns the ids of groups followed by extra.
func groupIDs(groups []model.Group, extra ...string) []string {
	out := make([]string, 0, len(groups)+len(extra))
	for _, g := range groups {
		out = append(out, g.ID)
	}
	return append(out, extra...)
}

// itemLabels loads every stored label of the item's units and of the item
// itself (model.ItemSubject), with labels asked under other item-level
// inputs turned unknown (currentLabels) and the user's overrides applied.
func (p *Pipeline) itemLabels(ctx context.Context, it model.Item, files []model.File, units []model.ChangeUnit) ([]model.Label, error) {
	itemID := it.ID
	ids := append(unitIDs(units), model.ItemSubject(itemID))
	ls, err := p.d.Store.Labels(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("pipeline: load labels: %w", err)
	}
	ls = currentLabels(ls, labels.InputHashes(p.catalogue(), p.labelContext(it, files)))
	ovs, err := p.d.Store.Overrides(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("pipeline: load overrides: %w", err)
	}
	return applyOverrides(ls, ovs), nil
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
	cur.LabelsUnavailable, cur.NeedsEyes = agg.LabelsMissing, agg.UnitsEyes
	cur.Relevance = 0
	for _, t := range agg.UrgencyTerms {
		if t.Name == "w_relevance" {
			cur.Relevance = t.Value
		}
	}
	cur.Breaking = 0
	for _, h := range agg.Headlines {
		if h.Kind == HeadlineBreaking {
			cur.Breaking++
		}
	}
	if err := p.d.Store.UpsertItem(ctx, cur); err != nil {
		return fmt.Errorf("pipeline: store item %s: %w", cur.ID, err)
	}
	return nil
}

// runStage wraps one stage with RunLog bookkeeping and logging. A stage
// that returns an error is failed (with Err); one that called skipStage is
// skipped (with its note); otherwise it is done, or skipped-cache when it
// set CacheHit.
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
	case rl.Status == model.StageSkipped:
		rl.CacheHit = false
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
		p.emitMu.Lock()
		defer p.emitMu.Unlock()
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
