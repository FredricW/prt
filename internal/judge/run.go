package judge

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

// defaultConcurrency is the number of requests in flight when the input
// leaves Concurrency unset.
const defaultConcurrency = 4

// breakerAfter is the number of consecutive jev.ErrUnavailable failures
// after which no new request is sent in a batch (as in internal/labels).
const breakerAfter = 3

// binding ties a wire question name in a request to the label it produces.
type binding struct {
	name string
	wq   questions.WireQuestion
}

// job is one Evaluate call about one subject.
type job struct {
	subject string // Label.UnitID
	ctx     string // Label.Ctx
	req     jev.Request
	binds   []binding
}

// outcome is the result of one job; done is false when it was never sent,
// skipped when the circuit breaker held it back.
type outcome struct {
	resp    jev.Response
	err     error
	done    bool
	skipped bool
}

// liveQuestion returns the live question id of scope s in cat.
func liveQuestion(cat questions.Catalogue, s questions.Scope, id string) (questions.Question, bool) {
	for _, q := range cat.Live(s) {
		if q.ID == id {
			return q, true
		}
	}
	return questions.Question{}, false
}

// bindings expands q: answerable wire questions become bindings (sorted by
// name), the others (a bullet choice without bullets) are decided locally.
func bindings(q questions.Question, dyn questions.Dynamic) (binds []binding, decided []questions.WireQuestion) {
	for _, wq := range q.Expand(dyn) {
		if !wq.Answerable() {
			decided = append(decided, wq)
			continue
		}
		binds = append(binds, binding{name: wq.Name, wq: wq})
	}
	sort.Slice(binds, func(a, b int) bool { return binds[a].name < binds[b].name })
	return binds, decided
}

// conc applies the default concurrency.
func conc(n int) int {
	if n <= 0 {
		return defaultConcurrency
	}
	return n
}

// run sends jobs with at most n in flight and assembles the Result, adding
// the locally decided labels fixed. The error is ctx's when cancelled, else
// the first fatal (non-ErrUnavailable) call error; Result then holds the
// labels received.
func run(ctx context.Context, c jev.Client, jobs []job, fixed []model.Label, n int) (Result, error) {
	outs := execute(ctx, c, jobs, conc(n))
	res := assemble(jobs, outs)
	res.Labels = append(res.Labels, fixed...)
	sortLabels(res.Labels)
	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("judge: %w", err)
	}
	return res, firstError(jobs, outs)
}

// execute runs jobs with at most n in flight. After a fatal error or
// cancellation no new job starts; after breakerAfter consecutive
// ErrUnavailable failures the remaining jobs are skipped. Any success
// resets the count.
func execute(ctx context.Context, c jev.Client, jobs []job, n int) []outcome {
	outs := make([]outcome, len(jobs))
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	var failed atomic.Bool
	var unavailable atomic.Int32
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

// assemble turns outcomes into a Result (labels unsorted).
func assemble(jobs []job, outs []outcome) Result {
	var res Result
	for k, j := range jobs {
		o := outs[k]
		switch {
		case o.skipped:
			res.Unavailable, res.Tripped = true, true
			res.Failed++
			res.Labels = append(res.Labels, unknownLabels(j)...)
		case !o.done:
		case o.err == nil:
			res.Calls++
			res.Answered++
			res.Cost += o.resp.CostUSD()
			res.Labels = append(res.Labels, decodeAll(j, o.resp)...)
		case errors.Is(o.err, jev.ErrUnavailable):
			res.Calls++
			res.Unavailable = true
			res.Failed++
			res.Labels = append(res.Labels, unknownLabels(j)...)
		default:
			res.Calls++
		}
	}
	return res
}

// sortLabels orders labels by subject, then question.
func sortLabels(ls []model.Label) {
	sort.SliceStable(ls, func(a, b int) bool {
		if ls[a].UnitID != ls[b].UnitID {
			return ls[a].UnitID < ls[b].UnitID
		}
		return ls[a].Question < ls[b].Question
	})
}

// firstError returns the fatal error of the lowest-indexed job, naming the
// subject and the request key.
func firstError(jobs []job, outs []outcome) error {
	for k, o := range outs {
		if !o.done || o.err == nil || errors.Is(o.err, jev.ErrUnavailable) {
			continue
		}
		key, kerr := jev.RequestKey(jobs[k].req)
		if kerr != nil {
			key = "?"
		}
		return fmt.Errorf("judge: %s: request %s: %w", jobs[k].subject, key, o.err)
	}
	return nil
}

// unknownLabels are the placeholders for every question of j.
func unknownLabels(j job) []model.Label {
	out := make([]model.Label, 0, len(j.binds))
	for _, b := range j.binds {
		out = append(out, unknownLabel(j.subject, j.ctx, b.wq))
	}
	return out
}

// decodeAll maps every binding of j to a label from resp.
func decodeAll(j job, resp jev.Response) []model.Label {
	modelID := resp.Model
	if modelID == "" {
		modelID = jev.Model
	}
	out := make([]model.Label, 0, len(j.binds))
	for _, b := range j.binds {
		a, ok := resp.Answers[b.name]
		out = append(out, decode(j.subject, j.ctx, b.wq, a, ok, modelID))
	}
	return out
}
