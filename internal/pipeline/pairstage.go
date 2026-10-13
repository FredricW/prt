package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/fredricw/prt/internal/judge"
	"github.com/fredricw/prt/internal/model"
)

// pairOut is the outcome of the pair pass of stage 4.
type pairOut struct {
	// confirmed are the candidates Jev kept (Source jev); rejected counts
	// judged candidates that were not kept; asked counts distinct pairs
	// judged (cached or not).
	confirmed       []model.Link
	rejected, asked int
}

// jevStage is stage 4: the label pass and the pair pass run concurrently
// ("two Jev passes, parallelised") and share one RunLog row: calls and cost
// add up, the pair note is appended to the label note. Only the label pass
// decides failure and LabelsMissing; a pair-pass failure leaves the pairs
// unknown (no confirmed links) and is noted.
func (p *Pipeline) jevStage(ctx context.Context, it model.Item, in parseOut, structural []model.Link, em embedOut, rl *model.RunLog) (jevOut, error) {
	var (
		wg         sync.WaitGroup
		lrl, prl   model.RunLog
		jo         jevOut
		po         pairOut
		lerr, perr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		jo, lerr = p.labelStage(ctx, it, in.files, in.units, &lrl)
	}()
	go func() {
		defer wg.Done()
		po, perr = p.pairStage(ctx, in.units, structural, em, &prl)
	}()
	wg.Wait()
	rl.In, rl.Out = lrl.In, lrl.Out+prl.Out
	rl.Calls, rl.Cost = lrl.Calls+prl.Calls, lrl.Cost+prl.Cost
	rl.CacheHit = lrl.CacheHit && prl.CacheHit
	rl.Note = joinNotes(lrl.Note, prl.Note)
	jo.pairs = po
	if perr != nil && ctx.Err() != nil {
		return jo, errors.Join(lerr, perr)
	}
	return jo, lerr
}

// pairStage judges candidate pairs: every embed-neighbour candidate, plus
// mechanical candidates of shape classes smaller than mechanical_min,
// minus pairs that already have a structural link. Stored relation labels
// that are known are cache hits; the rest are asked (when Jev is set) and
// stored, never replacing a known label with an unknown one. Errors other
// than cancellation are soft and only noted.
func (p *Pipeline) pairStage(ctx context.Context, units []model.ChangeUnit, structural []model.Link, em embedOut, rl *model.RunLog) (pairOut, error) {
	cands := pairCandidates(units, structural, em.cands, em.mech, p.d.Config.Pipeline.MechanicalMin)
	pairs := judgePairs(units, cands)
	out := pairOut{asked: len(pairs)}
	rl.Out = len(pairs)
	if len(pairs) == 0 {
		rl.CacheHit = true
		return out, nil
	}
	subjects := make([]string, len(pairs))
	for i, pr := range pairs {
		subjects[i] = model.PairSubject(pr.A.ID, pr.B.ID)
	}
	stored, err := p.labelsFor(ctx, subjects, model.QRelation)
	if err != nil {
		rl.Note = "pairs: " + err.Error()
		return out, err
	}
	known := knownSubjects(stored, nil)
	var need []judge.Pair
	for i, pr := range pairs {
		if !known[subjects[i]] {
			need = append(need, pr)
		}
	}
	rl.Note = fmt.Sprintf("pairs: %d asked, %d cached", len(pairs), len(pairs)-len(need))
	rl.CacheHit = len(need) == 0
	fresh, note, err := p.askPairs(ctx, need, stored, rl)
	rl.Note += note
	if err != nil && ctx.Err() != nil {
		return out, err
	}
	if err != nil {
		p.d.Log.Warn("pair pass failed; pairs stay unknown", "err", err)
		rl.Note += " (failed: " + err.Error() + ")"
	}
	out.confirmed, out.rejected = p.fn.confirm(cands, mergeLabels(stored, fresh), p.d.Config.Pipeline.KeepPairP)
	return out, nil
}

// askPairs asks Jev about need and stores the answers (unknowns only where
// no known label is stored). It returns the stored answers and a note
// suffix.
func (p *Pipeline) askPairs(ctx context.Context, need []judge.Pair, stored []model.Label, rl *model.RunLog) ([]model.Label, string, error) {
	if len(need) == 0 {
		return nil, "", nil
	}
	if p.d.Jev == nil {
		return nil, " (Jev unavailable)", nil
	}
	res, err := p.fn.pairs(ctx, p.d.Jev, judge.PairInput{Pairs: need, Catalogue: p.catalogue(), Concurrency: p.d.Config.Pipeline.Concurrency})
	rl.Calls, rl.Cost = rl.Calls+res.Calls, rl.Cost+res.Cost
	note := failedNote(res)
	fresh := keepKnown(res.Labels, stored)
	if serr := p.putLabels(ctx, fresh); serr != nil {
		err = errors.Join(err, serr)
	}
	return fresh, note, err
}

// failedNote is " (N failed)" when some Jev calls of res failed.
func failedNote(res judge.Result) string {
	if !res.Unavailable {
		return ""
	}
	if res.Tripped {
		return fmt.Sprintf(" (%d failed; Jev unavailable)", res.Failed)
	}
	return fmt.Sprintf(" (%d failed)", res.Failed)
}

// pairCandidates selects the candidate links to judge: all embed-neighbour
// candidates and the mechanical candidates whose shape class has fewer than
// mechMin members (larger classes are verified by code in cluster), minus
// any pair with a structural link in either direction ("structural links
// are never sent"). Candidate order is kept.
func pairCandidates(units []model.ChangeUnit, structural, embedCands, mech []model.Link, mechMin int) []model.Link {
	linked := map[string]bool{}
	for _, l := range structural {
		linked[model.PairSubject(l.From, l.To)] = true
	}
	shape := map[string]string{}
	classSize := map[string]int{}
	for _, u := range units {
		shape[u.ID] = u.ShapeHash
		if u.ShapeHash != "" {
			classSize[u.ShapeHash]++
		}
	}
	var out []model.Link
	for _, c := range embedCands {
		if !linked[model.PairSubject(c.From, c.To)] {
			out = append(out, c)
		}
	}
	for _, c := range mech {
		if classSize[shape[c.From]] < mechMin && !linked[model.PairSubject(c.From, c.To)] {
			out = append(out, c)
		}
	}
	return out
}

// judgePairs turns candidate links into distinct judge pairs sorted by pair
// subject (deterministic request order). Links whose units are not in units
// are skipped; a pair proposed twice keeps its highest weight.
func judgePairs(units []model.ChangeUnit, cands []model.Link) []judge.Pair {
	byID := unitsByID(units)
	bySubject := map[string]judge.Pair{}
	for _, c := range cands {
		a, okA := byID[c.From]
		b, okB := byID[c.To]
		if !okA || !okB || a.ID == b.ID {
			continue
		}
		if b.ID < a.ID {
			a, b = b, a
		}
		s := model.PairSubject(a.ID, b.ID)
		if prev, ok := bySubject[s]; !ok || c.Weight > prev.Cosine {
			bySubject[s] = judge.Pair{A: a, B: b, Cosine: c.Weight}
		}
	}
	keys := make([]string, 0, len(bySubject))
	for k := range bySubject {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]judge.Pair, len(keys))
	for i, k := range keys {
		out[i] = bySubject[k]
	}
	return out
}
