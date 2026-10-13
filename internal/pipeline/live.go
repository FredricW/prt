package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/labels"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
)

// noteFreeOnly is why a model-backed call was refused in a FreeOnly run.
const noteFreeOnly = "free-only run (outside the auto_review strategy)"

// noteStopped is the note of a stage row the user stopped.
const noteStopped = "stopped by user"

// snapshot is the configuration and the model clients one run uses, taken
// once when the run starts (Config, CurrentModels), so SetConfig and
// SetModels never change a run in flight. Under RunOptions.FreeOnly the
// models are guards that refuse every call (see freeOnlyModels).
type snapshot struct {
	cfg config.Config
	m   Models
}

// snapshot takes the run's configuration and models.
func (p *Pipeline) snapshot(opt RunOptions) snapshot {
	s := snapshot{cfg: p.Config(), m: p.CurrentModels()}
	if opt.FreeOnly {
		s.m = freeOnlyModels(s.m)
	}
	return s
}

// concurrency is pipeline.concurrency (default 4).
func (s snapshot) concurrency() int {
	if n := s.cfg.Pipeline.Concurrency; n > 0 {
		return n
	}
	return 4
}

// tierModel is the model spec of a tier: its reviewer's, else the
// configured one (estimates and request keys need a name even when no
// reviewer runs).
func (s snapshot) tierModel(t model.Tier) string {
	if rev := s.m.Reviewers.For(t); rev != nil {
		return rev.Model()
	}
	if t == model.TierDeep {
		return s.cfg.Models.Deep
	}
	return s.cfg.Models.Fast
}

// labelContext is the per-item context sent with unit questions.
func (s snapshot) labelContext(it model.Item, files []model.File) labels.Context {
	return labels.Context{
		ItemID:       it.ID,
		Title:        it.Title,
		Bullets:      model.DescBullets(it.Body),
		Role:         s.cfg.User.Role,
		Owns:         s.cfg.User.Owns,
		TestsTouched: testsTouched(files),
	}
}

// freeOnlyModels replaces every model client of m with a guard that keeps
// the client's model name (cache keys and request keys stay the same, so
// cached answers are still found) but fails every call without contacting
// anything. nil clients stay nil.
func freeOnlyModels(m Models) Models {
	out := Models{}
	if m.Jev != nil {
		out.Jev = freeJev{}
	}
	if m.Embedder != nil {
		out.Embedder = freeEmbedder{model: m.Embedder.Model()}
	}
	if m.Reviewers.Fast != nil {
		out.Reviewers.Fast = freeReviewer{model: m.Reviewers.Fast.Model()}
	}
	if m.Reviewers.Deep != nil {
		out.Reviewers.Deep = freeReviewer{model: m.Reviewers.Deep.Model()}
	}
	return out
}

// freeJev is the Jev client of a FreeOnly run: every call fails with
// jev.ErrUnavailable.
type freeJev struct{}

// Evaluate implements jev.Client without calling anything.
func (freeJev) Evaluate(context.Context, jev.Request) (jev.Response, error) {
	return jev.Response{}, fmt.Errorf("%w: %s", jev.ErrUnavailable, noteFreeOnly)
}

// freeEmbedder is the embedder of a FreeOnly run: Embed fails with
// embed.ErrUnavailable; Model is the guarded embedder's.
type freeEmbedder struct{ model string }

// Embed implements embed.Embedder without calling anything.
func (freeEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, fmt.Errorf("%w: %s", embed.ErrUnavailable, noteFreeOnly)
}

// Model implements embed.Embedder.
func (f freeEmbedder) Model() string { return f.model }

// freeReviewer is a reviewer of a FreeOnly run: it is never ready (so prose
// is served from its cache only) and Review fails with
// review.ErrUnavailable; Model is the guarded reviewer's.
type freeReviewer struct{ model string }

// Review implements review.Reviewer without calling anything.
func (freeReviewer) Review(context.Context, review.Request) (review.Result, error) {
	return review.Result{}, errFreeOnlyReview
}

// Model implements review.Reviewer.
func (f freeReviewer) Model() string { return f.model }

// Ready implements review.Checker: a FreeOnly run never calls a reviewer.
func (freeReviewer) Ready() error { return errFreeOnlyReview }

var errFreeOnlyReview = fmt.Errorf("%w: %s", review.ErrUnavailable, noteFreeOnly)

// stopped reports whether ctx was cancelled by a user stop (cause
// ErrStopped), as opposed to a shutdown or an item removal.
func stopped(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), ErrStopped)
}

// stopErr is the error a stopped run returns: it wraps the cancel cause, so
// errors.Is(err, ErrStopped) holds.
func stopErr(ctx context.Context, s model.Stage) error {
	return fmt.Errorf("pipeline: %s: %w", s, context.Cause(ctx))
}

// knownOnly returns the labels of ls that are known answers.
func knownOnly(ls []model.Label) []model.Label {
	out := make([]model.Label, 0, len(ls))
	for _, l := range ls {
		if !model.IsUnknown(l) {
			out = append(out, l)
		}
	}
	return out
}

// putAnswered stores the answers of the Jev calls that completed before a
// stop (known labels only, so nothing unanswered is written as unknown),
// with a context the stop does not cancel. Store errors are only logged.
func (p *Pipeline) putAnswered(ctx context.Context, ls []model.Label) {
	known := knownOnly(ls)
	if len(known) == 0 {
		return
	}
	if err := p.d.Store.PutLabels(context.WithoutCancel(ctx), known); err != nil {
		p.d.Log.Warn("store labels after stop failed", "err", err)
	}
}
