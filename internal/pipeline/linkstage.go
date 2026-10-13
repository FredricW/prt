package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/link"
	"github.com/fredricw/prt/internal/model"
)

// Notes recorded when stage 3 does not produce embed candidates (spec
// failure handling: "groups come from structural links only, with a status
// note"). They also go into GroupingStats.EmbedNote.
const (
	noteNoEmbedder = "no embedding model: groups from structural links only"
	noteEmbedFmt   = "embeddings unavailable: %s; groups from structural links only"
	noteEmbedErr   = "embeddings failed: %s; groups from structural links only"
)

// skipStage marks the running stage row skipped with a note; runStage keeps
// that status instead of done (the stage ran but had nothing it could do).
func skipStage(rl *model.RunLog, note string) {
	rl.Status, rl.Note = model.StageSkipped, note
}

// linkStage is stage 2: exact structural links between the item's units.
// It returns a non-nil slice on success (empty when nothing links).
func (p *Pipeline) linkStage(ctx context.Context, itemID string, f model.Fetch, in parseOut, rl *model.RunLog) ([]model.Link, error) {
	rl.In = len(in.units)
	ls, err := p.fn.structural(ctx, link.Input{
		ItemID: itemID, BaseSHA: f.BaseSHA, HeadSHA: f.HeadSHA,
		Files: in.files, Units: in.units, Src: p.d.Git, Cache: p.d.Store,
	})
	if err != nil {
		return nil, fmt.Errorf("pipeline: link %s: %w", itemID, err)
	}
	if ls == nil {
		ls = []model.Link{}
	}
	rl.Out = len(ls)
	rl.Note = kindNote(ls)
	return ls, nil
}

// structuralKinds is the display order of structural link kinds.
var structuralKinds = []model.LinkKind{model.LinkCalls, model.LinkTests, model.LinkImports, model.LinkMoved, model.LinkSameSymbol}

// kindNote counts links by kind, e.g. "12 calls · 3 tests · 1 moved".
func kindNote(ls []model.Link) string {
	if len(ls) == 0 {
		return "no structural links"
	}
	counts := countKinds(ls)
	var parts []string
	for _, k := range structuralKinds {
		if n := counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
			delete(counts, k)
		}
	}
	for _, k := range sortedKinds(counts) {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	return strings.Join(parts, " · ")
}

// countKinds counts links by kind (never nil).
func countKinds(ls []model.Link) map[model.LinkKind]int {
	m := map[model.LinkKind]int{}
	for _, l := range ls {
		m[l.Kind]++
	}
	return m
}

// embedOut is the outcome of stage 3: embed-neighbour candidates (nil when
// the stage was skipped or failed), mechanical candidates (always computed:
// they need no vectors) and the skip note ("" when embeddings ran).
type embedOut struct {
	cands []model.Link
	mech  []model.Link
	note  string
}

// embedStage is stage 3. Mechanical candidates are always computed. With
// an embedder, cached vectors come from the store (by unit id and model id)
// and the rest are embedded in one call and stored; kNN candidates follow.
// No embedder or embed.ErrUnavailable skips the stage with a note; any other
// error is returned (soft: the caller continues without candidates).
func (p *Pipeline) embedStage(ctx context.Context, units []model.ChangeUnit, rl *model.RunLog) (embedOut, error) {
	out := embedOut{mech: p.fn.mechanical(units)}
	rl.In = len(units)
	if p.d.Embedder == nil {
		out.note = noteNoEmbedder
		skipStage(rl, out.note)
		return out, nil
	}
	vecs, cached, err := p.unitVectors(ctx, units, rl)
	switch {
	case err != nil && ctx.Err() != nil:
		return out, err
	case errors.Is(err, embed.ErrUnavailable):
		out.note = fmt.Sprintf(noteEmbedFmt, shortReason(err))
		skipStage(rl, out.note)
		return out, nil
	case err != nil:
		out.note = fmt.Sprintf(noteEmbedErr, shortReason(err))
		rl.Note = out.note
		return out, err
	}
	pc := p.d.Config.Pipeline
	out.cands = p.fn.embedCands(units, vecs, link.EmbedOptions{MinCosine: pc.EmbedMinCosine, TopK: pc.EmbedTopK})
	rl.CacheHit = cached == len(units)
	rl.Out = len(out.cands) + len(out.mech)
	rl.Note = fmt.Sprintf("%d of %d cached · %d candidates · %d mechanical", cached, len(units), len(out.cands), len(out.mech))
	return out, nil
}

// unitVectors returns a vector per unit id: stored ones for the embedder's
// model, the rest embedded in one call (adapters batch internally) and
// stored. cached counts the stored hits.
func (p *Pipeline) unitVectors(ctx context.Context, units []model.ChangeUnit, rl *model.RunLog) (map[string][]float32, int, error) {
	m := p.d.Embedder.Model()
	vecs := make(map[string][]float32, len(units))
	var miss []model.ChangeUnit
	for _, u := range units {
		v, ok, err := p.d.Store.Embedding(ctx, u.ID, m)
		if err != nil {
			return nil, 0, fmt.Errorf("pipeline: load embedding: %w", err)
		}
		if ok {
			vecs[u.ID] = v
		} else {
			miss = append(miss, u)
		}
	}
	cached := len(units) - len(miss)
	if len(miss) == 0 {
		return vecs, cached, nil
	}
	texts := make([]string, len(miss))
	for i, u := range miss {
		texts[i] = link.UnitText(u)
	}
	rl.Calls++
	got, err := p.d.Embedder.Embed(ctx, texts)
	if err != nil {
		return nil, cached, fmt.Errorf("pipeline: embed %d units with %s: %w", len(miss), m, err)
	}
	if len(got) != len(miss) {
		return nil, cached, fmt.Errorf("pipeline: embed: %d vectors for %d units", len(got), len(miss))
	}
	for i, u := range miss {
		vecs[u.ID] = got[i]
		if err := p.d.Store.PutEmbedding(ctx, u.ID, m, got[i]); err != nil {
			return nil, cached, fmt.Errorf("pipeline: store embedding: %w", err)
		}
	}
	return vecs, cached, nil
}

// shortReason is an error message without the "pipeline: embed …" and
// "embed: model unavailable" prefixes, cut to 100 runes, for status notes.
func shortReason(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, embed.ErrUnavailable.Error()+": "); i >= 0 {
		s = s[i+len(embed.ErrUnavailable.Error())+2:]
	}
	if r := []rune(s); len(r) > 100 {
		s = string(r[:99]) + "…"
	}
	return s
}
