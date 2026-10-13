// Package labels is the "labels" half of pipeline stage 4 (spec: "Pipeline
// internals" stage 4 and "Jev question catalogue"). For each change unit it
// sends one Jev request carrying the live unit-scope questions, and for the
// unit's added lines one request per batch of up to LineBatch lines carrying
// the live line-scope questions; it maps the answers to model.Label values
// following the conventions in internal/model/labels.go.
//
// It does no I/O except through the jev.Client passed in, holds no cache
// (the pipeline passes only units that need labels) and is deterministic:
// the same Input always produces byte-identical jev.Requests (so recordings
// keyed by jev.RequestKey replay) and labels in a stable order.
//
// Requests: the state is a State (JSON object: unit path, symbol,
// signature, git change kind and diff, plus only the inputs the request's
// questions declare). Wire question names are the catalogue id,
// "<id>__<sub>" for "N ×k" sub-questions, and "<name>__lNN" for line
// questions (NN = the line's slot in state.lines). A request that would
// exceed the token limits first has its diff truncated (head and tail kept)
// and then its questions split across several requests.
package labels

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
)

// DefaultLineBatch is the number of added lines per line request when
// Input.LineBatch is unset (spec: "batched 20 lines per call").
const DefaultLineBatch = 20

// Context is the per-item information attached to unit requests (only the
// parts a question's `inputs` ask for).
type Context struct {
	ItemID  string
	Title   string
	Bullets []string // model.DescBullets(item.Body), offered as desc_bullet options
	Role    string   // config user.role
	Owns    []string // config user.owns globs
	// TestsTouched lists the paths of changed test files in the item (input
	// "changed test units" of the covered question).
	TestsTouched []string
}

// Input is everything one labelling run needs.
type Input struct {
	Ctx       Context
	Units     []model.ChangeUnit // units to label, in item order
	Catalogue questions.Catalogue
	// LineBatch is the number of added lines per line-question request
	// (config pipeline.line_batch, default 20); <= 0 means 20.
	LineBatch int
	// Concurrency bounds parallel requests (config pipeline.concurrency);
	// <= 0 means 1. Output order never depends on it.
	Concurrency int
	// MaxStatePlusQuestion and MaxRequestTokens override the Jev token
	// limits (state + longest question, whole request); <= 0 means
	// jev.MaxStatePlusQuestion / jev.MaxRequestTokens. Tests use tiny values.
	MaxStatePlusQuestion int
	MaxRequestTokens     int
}

// Result is the outcome of a run.
type Result struct {
	// Labels for every input unit: unit-scope labels (one per live question,
	// one per sub-question of N ×k rows) and line-scope labels per added
	// line (Line = the line's 1-based position among the unit's added
	// lines, see model.AddedLineNumbers). Labels of questions with
	// item-level inputs carry Ctx = InputHash. When Jev is unavailable the affected units get LabelUnknown
	// labels for every live question (confidence 0). Order: units in input
	// order; within a unit, unit labels by question id, then line labels by
	// line and question id.
	Labels []model.Label
	// Calls is the number of Evaluate calls made; Cost the summed
	// providerMetadata.gateway.cost in USD.
	Calls int
	Cost  float64
	// Unavailable is true when any call failed with jev.ErrUnavailable.
	Unavailable bool
}

// Run labels in.Units with c. It returns an error only for failures that are
// bugs or test misconfiguration (for example jev.ErrNoRecording from Replay,
// a non-retryable 4xx, or a request that cannot be made to fit the token
// limits); jev.ErrUnavailable is not an error: those units get unknown
// labels and Result.Unavailable is set. With an error, Result still holds
// the labels of the calls that completed.
func Run(ctx context.Context, c jev.Client, in Input) (Result, error) {
	in, lim := normalise(in)
	p, err := buildPlan(in, lim)
	if err != nil {
		return Result{}, err
	}
	outs := execute(ctx, c, p.jobs, in.Concurrency)
	res := assemble(in.Units, p, outs)
	hashes := InputHashes(in.Catalogue, in.Ctx)
	for i := range res.Labels {
		res.Labels[i].Ctx = hashes[res.Labels[i].Question]
	}
	if err := firstError(in.Units, p.jobs, outs); err != nil {
		return res, err
	}
	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("labels: %w", err)
	}
	return res, nil
}

// normalise applies defaults.
func normalise(in Input) (Input, limits) {
	if in.LineBatch <= 0 {
		in.LineBatch = DefaultLineBatch
	}
	in.Concurrency = max(1, in.Concurrency)
	lim := limits{statePlusQuestion: in.MaxStatePlusQuestion, request: in.MaxRequestTokens}
	if lim.statePlusQuestion <= 0 {
		lim.statePlusQuestion = jev.MaxStatePlusQuestion
	}
	if lim.request <= 0 {
		lim.request = jev.MaxRequestTokens
	}
	return in, lim
}

// outcome is the result of one job; done is false when it was never sent.
// skipped marks a job not sent because the circuit breaker tripped (Jev
// treated as unavailable for it).
type outcome struct {
	resp    jev.Response
	err     error
	done    bool
	skipped bool
}

// breakerAfter is the number of consecutive jev.ErrUnavailable failures
// after which no new request is sent in a run: the rest of the item's
// questions become unknown at once instead of each waiting out the HTTP
// timeout against an unreachable endpoint.
const breakerAfter = 3

// execute runs jobs with at most conc in flight. After a fatal error (not
// jev.ErrUnavailable) or cancellation no new job starts; after breakerAfter
// consecutive ErrUnavailable failures the remaining jobs are skipped.
func execute(ctx context.Context, c jev.Client, jobs []job, conc int) []outcome {
	outs := make([]outcome, len(jobs))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var failed atomic.Bool
	var unavailable atomic.Int32 // consecutive ErrUnavailable failures
	for i := range jobs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil || failed.Load() {
			break
		}
		if unavailable.Load() >= breakerAfter {
			<-sem
			outs[i] = outcome{skipped: true}
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			resp, err := c.Evaluate(ctx, jobs[i].req)
			outs[i] = outcome{resp: resp, err: err, done: true}
			switch {
			case err == nil:
				unavailable.Store(0)
			case errors.Is(err, jev.ErrUnavailable):
				unavailable.Add(1)
			default:
				failed.Store(true)
			}
		}(i)
	}
	wg.Wait()
	return outs
}

// assemble turns outcomes into the ordered Result.
func assemble(units []model.ChangeUnit, p plan, outs []outcome) Result {
	var res Result
	perUnit := make([][]model.Label, len(units))
	for i := range units {
		perUnit[i] = append(perUnit[i], p.fixed[i]...)
	}
	for k, j := range p.jobs {
		o := outs[k]
		if o.skipped {
			res.Unavailable = true
			for _, b := range j.binds {
				perUnit[j.unit] = append(perUnit[j.unit], unknownLabel(units[j.unit].ID, b.line, b.wq))
			}
			continue
		}
		if !o.done {
			continue
		}
		res.Calls++
		unitID := units[j.unit].ID
		switch {
		case o.err == nil:
			res.Cost += o.resp.CostUSD()
			perUnit[j.unit] = append(perUnit[j.unit], decodeAll(unitID, j, o.resp)...)
		case errors.Is(o.err, jev.ErrUnavailable):
			res.Unavailable = true
			for _, b := range j.binds {
				perUnit[j.unit] = append(perUnit[j.unit], unknownLabel(unitID, b.line, b.wq))
			}
		}
	}
	for _, ls := range perUnit {
		sort.SliceStable(ls, func(a, b int) bool {
			if ls[a].Line != ls[b].Line {
				return ls[a].Line < ls[b].Line
			}
			return ls[a].Question < ls[b].Question
		})
		res.Labels = append(res.Labels, ls...)
	}
	return res
}

// decodeAll maps every binding of j to a label from resp.
func decodeAll(unitID string, j job, resp jev.Response) []model.Label {
	modelID := resp.Model
	if modelID == "" {
		modelID = jev.Model
	}
	out := make([]model.Label, 0, len(j.binds))
	for _, b := range j.binds {
		a, ok := resp.Answers[b.name]
		out = append(out, decode(unitID, b, a, ok, modelID))
	}
	return out
}

// firstError returns the fatal error of the lowest-indexed job, naming the
// unit and the request key so a missing recording is easy to find.
func firstError(units []model.ChangeUnit, jobs []job, outs []outcome) error {
	for k, o := range outs {
		if !o.done || o.err == nil || errors.Is(o.err, jev.ErrUnavailable) {
			continue
		}
		u := units[jobs[k].unit]
		key, kerr := jev.RequestKey(jobs[k].req)
		if kerr != nil {
			key = "?"
		}
		return fmt.Errorf("labels: unit %s (%s): request %s: %w", u.Path, u.ID, key, o.err)
	}
	return nil
}
