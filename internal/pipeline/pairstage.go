package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/judge"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/shape"
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
func (p *Pipeline) jevStage(ctx context.Context, r *run, it model.Item, in parseOut, structural []model.Link, em embedOut, rl *model.RunLog) (jevOut, error) {
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
		jo, lerr = p.labelStage(ctx, r, it, in.files, in.units, &lrl)
	}()
	go func() {
		defer wg.Done()
		po, perr = p.pairStage(ctx, r, in.units, structural, em, &prl)
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
// the mechanical candidates cluster does not verify by code (see
// pairCandidates), minus pairs that already have a structural link. Stored relation labels
// that are known are cache hits; the rest are asked (when Jev is set) and
// stored, never replacing a known label with an unknown one. Errors other
// than cancellation are soft and only noted.
func (p *Pipeline) pairStage(ctx context.Context, r *run, units []model.ChangeUnit, structural []model.Link, em embedOut, rl *model.RunLog) (pairOut, error) {
	pc := r.cfg.Pipeline
	cands := pairCandidates(units, structural, em.cands, em.mech, pc.MechanicalMin)
	pairs := judgePairs(units, cands)
	perUnit := pc.MaxPairsPerUnit
	if perUnit <= 0 {
		perUnit = config.DefaultMaxPairsPerUnit
	}
	pairs, cands, dropped := capPairs(pairs, cands, perUnit*len(units))
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
	if dropped > 0 {
		rl.Note += fmt.Sprintf(" (%d over the cap not asked)", dropped)
	}
	rl.CacheHit = len(need) == 0
	fresh, note, err := p.askPairs(ctx, r, need, stored, rl)
	rl.Note += note
	if err != nil && ctx.Err() != nil {
		return out, err
	}
	if err != nil {
		p.d.Log.Warn("pair pass failed; pairs stay unknown", "err", err)
		rl.Note += " (failed: " + err.Error() + ")"
	}
	out.confirmed, out.rejected = p.fn.confirm(cands, mergeLabels(stored, fresh), pc.KeepPairP)
	return out, nil
}

// askPairs asks Jev about need and stores the answers (unknowns only where
// no known label is stored). It returns the stored answers and a note
// suffix. On a user stop only the known answers are stored.
func (p *Pipeline) askPairs(ctx context.Context, r *run, need []judge.Pair, stored []model.Label, rl *model.RunLog) ([]model.Label, string, error) {
	if len(need) == 0 {
		return nil, "", nil
	}
	if r.m.Jev == nil {
		return nil, " (Jev unavailable)", nil
	}
	res, err := p.fn.pairs(ctx, r.m.Jev, judge.PairInput{Pairs: need, Catalogue: p.catalogue(), Concurrency: r.cfg.Pipeline.Concurrency})
	rl.Calls, rl.Cost = rl.Calls+res.Calls, rl.Cost+res.Cost
	if stopped(ctx) {
		p.putAnswered(ctx, res.Labels)
		return nil, "", stopErr(ctx, model.StageJev)
	}
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
// candidates and every mechanical candidate except those inside one
// shape.Key partition (same shape hash and literal edit) of at least mechMin
// units — those are verified by code in cluster. Pairs across partitions
// (same shape, another literal edit) and pairs in smaller partitions go to
// Jev. Any pair with a structural link in either direction is left out
// ("structural links are never sent"). Candidate order is kept.
func pairCandidates(units []model.ChangeUnit, structural, embedCands, mech []model.Link, mechMin int) []model.Link {
	linked := map[string]bool{}
	for _, l := range structural {
		linked[model.PairSubject(l.From, l.To)] = true
	}
	key := map[string]string{}
	partSize := map[string]int{}
	for _, u := range units {
		k := shape.Key(u)
		key[u.ID] = k
		if k != "" {
			partSize[k]++
		}
	}
	var out []model.Link
	for _, c := range embedCands {
		if !linked[model.PairSubject(c.From, c.To)] {
			out = append(out, c)
		}
	}
	for _, c := range mech {
		k := key[c.From]
		verified := k != "" && k == key[c.To] && partSize[k] >= mechMin
		if !verified && !linked[model.PairSubject(c.From, c.To)] {
			out = append(out, c)
		}
	}
	return out
}

// capPairs keeps at most limit pairs: the highest cosine first (mechanical
// candidates have weight 1, so they go first), ties by pair subject. Kept
// pairs stay sorted by subject; cands is filtered to the kept pairs so a
// dropped pair cannot be confirmed by an old stored label. It returns the
// number of pairs dropped.
func capPairs(pairs []judge.Pair, cands []model.Link, limit int) ([]judge.Pair, []model.Link, int) {
	if limit < 0 || len(pairs) <= limit {
		return pairs, cands, 0
	}
	ranked := append([]judge.Pair(nil), pairs...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Cosine != ranked[j].Cosine {
			return ranked[i].Cosine > ranked[j].Cosine
		}
		return model.PairSubject(ranked[i].A.ID, ranked[i].B.ID) < model.PairSubject(ranked[j].A.ID, ranked[j].B.ID)
	})
	keep := map[string]bool{}
	for _, pr := range ranked[:limit] {
		keep[model.PairSubject(pr.A.ID, pr.B.ID)] = true
	}
	var kp []judge.Pair
	for _, pr := range pairs {
		if keep[model.PairSubject(pr.A.ID, pr.B.ID)] {
			kp = append(kp, pr)
		}
	}
	var kc []model.Link
	for _, c := range cands {
		if keep[model.PairSubject(c.From, c.To)] {
			kc = append(kc, c)
		}
	}
	return kp, kc, len(pairs) - len(kp)
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
