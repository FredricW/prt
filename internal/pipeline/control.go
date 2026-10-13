package pipeline

import (
	"errors"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/review"
)

// ErrStopped is the context cancel cause the daemon uses when the user
// stops an item's analysis (M5.1; context.WithCancelCause(ctx) then
// cancel(ErrStopped)). A run whose context.Cause(ctx) is ErrStopped marks
// the stage it was in model.StageStopped (not failed), in-flight review
// rows model.ReviewStopped (not failed, not retried, not cached), stores
// nothing half-done, and returns an error wrapping ErrStopped so the host
// can tell a stop from a failure or a shutdown.
var ErrStopped = errors.New("stopped by user")

// Models are the model-backed clients a run may use. Any may be nil (the
// pipeline degrades as with nil Deps fields).
type Models struct {
	Jev       jev.Client
	Embedder  embed.Embedder
	Reviewers review.Tiers
}

// Config returns the configuration runs started now use (Deps.Config, or
// the last SetConfig).
//
// M5.1 shared contract; the pipeline work package makes every stage read
// the configuration and models through Config and the model accessors
// (one snapshot per run), so SetConfig and SetModels are safe while runs
// are in flight.
func (p *Pipeline) Config() config.Config {
	p.liveMu.RLock()
	defer p.liveMu.RUnlock()
	return p.d.Config
}

// SetConfig replaces the prt.toml configuration for runs started from now
// on (the settings page applies changes live; M5.1). Runs in flight keep
// the snapshot they started with.
func (p *Pipeline) SetConfig(c config.Config) {
	p.liveMu.Lock()
	defer p.liveMu.Unlock()
	p.d.Config = c
}

// SetModels replaces the model clients for runs started from now on (the
// settings page changed fast, deep or embed; M5.1).
func (p *Pipeline) SetModels(m Models) {
	p.liveMu.Lock()
	defer p.liveMu.Unlock()
	p.d.Jev, p.d.Embedder, p.d.Reviewers = m.Jev, m.Embedder, m.Reviewers
}

// CurrentModels returns the model clients runs started now use.
func (p *Pipeline) CurrentModels() Models {
	p.liveMu.RLock()
	defer p.liveMu.RUnlock()
	return Models{Jev: p.d.Jev, Embedder: p.d.Embedder, Reviewers: p.d.Reviewers}
}
