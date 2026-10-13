package labels

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
)

// binding ties a wire question name in a request to the label it produces.
type binding struct {
	name string
	wq   questions.WireQuestion
	line int // new-side line number for line questions, 0 for unit questions
}

// job is one Evaluate call.
type job struct {
	unit  int // index into Input.Units
	req   jev.Request
	binds []binding // sorted by name
}

// limits are the token limits in force for a run.
type limits struct {
	statePlusQuestion int
	request           int
}

// plan is every request of a run plus the labels decided without Jev.
type plan struct {
	jobs []job
	// fixed holds labels decided without asking (a bullet question when the
	// PR has no bullets), per unit index.
	fixed [][]model.Label
}

// lineNote is appended to a line question's instructions to pin the line.
const lineNote = "\nAnswer only about the added line in state.lines with slot %q (new-side line %d)."

// buildPlan builds the requests for in, in deterministic order: per unit,
// its unit request(s), then its line batches.
func buildPlan(in Input, lim limits) (plan, error) {
	p := plan{fixed: make([][]model.Label, len(in.Units))}
	unitQs := in.Catalogue.Live(questions.ScopeUnit)
	lineQs := in.Catalogue.Live(questions.ScopeLine)
	dyn := questions.Dynamic{Bullets: in.Ctx.Bullets}
	for i, u := range in.Units {
		binds, fixed := unitBindings(u.ID, unitQs, dyn)
		p.fixed[i] = fixed
		if len(binds) > 0 {
			jobs, err := pack(i, baseState(u, in.Ctx, unitQs), binds, lim)
			if err != nil {
				return plan{}, fmt.Errorf("labels: unit %s (%s): %w", u.Path, u.ID, err)
			}
			p.jobs = append(p.jobs, jobs...)
		}
		if len(lineQs) == 0 {
			continue
		}
		added := addedLines(u)
		for start := 0; start < len(added); start += in.LineBatch {
			batch := added[start:min(start+in.LineBatch, len(added))]
			st, binds := lineRequest(u, in.Ctx, lineQs, dyn, batch, in.LineBatch)
			jobs, err := pack(i, st, binds, lim)
			if err != nil {
				return plan{}, fmt.Errorf("labels: unit %s (%s) lines %d-%d: %w",
					u.Path, u.ID, batch[0].line, batch[len(batch)-1].line, err)
			}
			p.jobs = append(p.jobs, jobs...)
		}
	}
	return p, nil
}

// unitBindings expands the unit questions; questions that need no call
// (see questions.WireQuestion.Answerable) become fixed labels.
func unitBindings(unitID string, qs []questions.Question, dyn questions.Dynamic) ([]binding, []model.Label) {
	var binds []binding
	var fixed []model.Label
	for _, q := range qs {
		for _, wq := range q.Expand(dyn) {
			if !wq.Answerable() {
				fixed = append(fixed, decidedLabel(unitID, 0, wq))
				continue
			}
			binds = append(binds, binding{name: wq.Name, wq: wq})
		}
	}
	return binds, fixed
}

// lineRequest builds the state and bindings for one batch of added lines.
func lineRequest(u model.ChangeUnit, c Context, qs []questions.Question, dyn questions.Dynamic,
	batch []addedLine, batchSize int) (State, []binding) {
	st := baseState(u, c, qs)
	var binds []binding
	for j, al := range batch {
		slot := slotName(j, batchSize)
		st.Lines = append(st.Lines, LineState{Slot: slot, Line: al.line, Text: al.text})
		for _, q := range qs {
			for _, wq := range q.Expand(dyn) {
				if !wq.Answerable() {
					continue
				}
				wq.Name += questions.WireSep + slot
				wq.Q.Instructions += fmt.Sprintf(lineNote, slot, al.line)
				binds = append(binds, binding{name: wq.Name, wq: wq, line: al.line})
			}
		}
	}
	return st, binds
}

// pack fits st and binds into requests: it truncates the diff until the
// state plus the longest question fits limits.statePlusQuestion, then splits
// the questions (in name order) so every request fits limits.request.
func pack(unit int, st State, binds []binding, lim limits) ([]job, error) {
	sort.Slice(binds, func(a, b int) bool { return binds[a].name < binds[b].name })
	entries := make([]int, len(binds)) // byte length of `"name":{...}`
	longest := 0
	for k, b := range binds {
		q, err := json.Marshal(b.wq.Q)
		if err != nil {
			return nil, fmt.Errorf("encode question %s: %w", b.name, err)
		}
		entries[k] = len(b.name) + 3 + len(q)
		longest = max(longest, jev.EstimateTokensLen(entries[k]))
	}
	st, err := fitState(st, longest, lim.statePlusQuestion)
	if err != nil {
		return nil, err
	}
	base, err := json.Marshal(jev.Request{Model: jev.Model, State: st, Questions: map[string]jev.Question{}})
	if err != nil {
		return nil, fmt.Errorf("encode state: %w", err)
	}
	var jobs []job
	var cur []binding
	size := len(base)
	flush := func() {
		if len(cur) > 0 {
			jobs = append(jobs, newJob(unit, st, cur))
		}
		cur, size = nil, len(base)
	}
	for k, b := range binds {
		add := entries[k]
		if len(cur) > 0 {
			add++ // comma
		}
		if len(cur) > 0 && jev.EstimateTokensLen(size+add) > lim.request {
			flush()
			add = entries[k]
		}
		if jev.EstimateTokensLen(size+add) > lim.request {
			return nil, fmt.Errorf("question %s does not fit the %d-token request limit (~%d tokens)",
				b.name, lim.request, jev.EstimateTokensLen(size+add))
		}
		cur = append(cur, b)
		size += add
	}
	flush()
	return jobs, nil
}

func newJob(unit int, st State, binds []binding) job {
	qs := make(map[string]jev.Question, len(binds))
	for _, b := range binds {
		qs[b.name] = b.wq.Q
	}
	return job{unit: unit, req: jev.Request{Model: jev.Model, State: st, Questions: qs}, binds: binds}
}

// fitState returns st with its diff truncated (head and tail kept) so that
// the state plus a question of qTokens fits limit; an error when even an
// empty diff does not fit.
func fitState(st State, qTokens, limit int) (State, error) {
	fits := func(s State) (bool, error) {
		b, err := json.Marshal(s)
		if err != nil {
			return false, fmt.Errorf("encode state: %w", err)
		}
		return jev.EstimateTokensLen(len(b))+qTokens <= limit, nil
	}
	ok, err := fits(st)
	if err != nil || ok {
		return st, err
	}
	lines := strings.Split(st.Unit.Diff, "\n")
	with := func(keep int) State {
		s := st
		s.Unit.Diff = truncateDiff(lines, keep)
		return s
	}
	// Largest keep in [0, len(lines)) that fits; fits(keep) is monotone.
	lo, hi := -1, len(lines)
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		ok, err := fits(with(mid))
		if err != nil {
			return st, err
		}
		if ok {
			lo = mid
		} else {
			hi = mid
		}
	}
	if lo < 0 {
		return st, fmt.Errorf("state does not fit the %d-token state+question limit even with an empty diff", limit)
	}
	return with(lo), nil
}
