package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fredricw/prt/internal/comments"
	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/judge"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/parse"
)

// nsSinceUnits caches the unit ids parsed from a "Since you looked" base
// head, keyed itemID|baseSHA (JSON []string), so re-runs do not re-parse
// the base.
const nsSinceUnits = "since-units"

// Since-you-looked notes.
const (
	noteLabelsUnavailable = "labels unavailable"
	// beforeContext is how many lines either side of a comment's line go
	// into its comment_addressed request.
	beforeContext = 10
)

// sinceOut is the outcome of sinceLooked: the block (nil without a base)
// and the Jev calls and cost it made.
type sinceOut struct {
	since *model.SinceLooked
	calls int
	cost  float64
}

// sinceLooked computes the Overview's "Since you looked" block in stage 8
// (spec Overview 9; DECISIONS "M4 shared contracts"): the base head from
// the item's view mark or, for PRs, the viewer's latest review commit
// (comments.SinceBase); the current units that did not exist at the base;
// and comment_addressed for the viewer's earlier comments. Every failure is
// soft (a note on the block); only cancellation is returned.
func (p *Pipeline) sinceLooked(ctx context.Context, r *run, units []model.ChangeUnit) (sinceOut, error) {
	cur := p.currentItem(ctx, r.it)
	if cur.Kind != model.KindPR {
		cur.ViewerReviewedSHA = "" // branches: the view mark only
	}
	mark, ok, err := p.d.Store.ViewMark(ctx, cur.ID)
	if err != nil {
		p.d.Log.Warn("load view mark failed", "item", cur.ID, "err", err)
		ok = false
	}
	base, source := comments.SinceBase(cur, r.f.HeadSHA, mark, ok)
	if base == "" {
		return sinceOut{}, nil
	}
	s := &model.SinceLooked{BaseSHA: base, HeadSHA: r.f.HeadSHA, Source: source, ChangedUnits: []string{}}
	var notes []string
	local := p.haveCommit(ctx, base)
	if local {
		ids, err := p.sinceBaseUnits(ctx, cur, base)
		switch {
		case ctx.Err() != nil:
			return sinceOut{}, ctx.Err()
		case err != nil:
			p.d.Log.Warn("parse since-you-looked base failed", "item", cur.ID, "base", base, "err", err)
			notes = append(notes, fmt.Sprintf("base head %s could not be parsed", shortSHA(base)))
		default:
			s.ChangedUnits = comments.ChangedUnits(unitsWithIDs(ids), units)
		}
	} else {
		notes = append(notes, fmt.Sprintf("base head %s not available locally", shortSHA(base)))
	}
	ad, err := p.addressedComments(ctx, cur, r.f.HeadSHA, base, local, units)
	if err != nil {
		return sinceOut{}, err
	}
	s.Comments = ad.rows
	if ad.note != "" {
		notes = append(notes, ad.note)
	}
	s.Note = strings.Join(notes, "; ")
	return sinceOut{since: s, calls: ad.calls, cost: ad.cost}, nil
}

// haveCommit reports whether sha resolves in the local repository.
func (p *Pipeline) haveCommit(ctx context.Context, sha string) bool {
	if p.d.Git == nil {
		return false
	}
	_, err := p.d.Git.ResolveRef(ctx, sha)
	return err == nil
}

// shortSHA is the first 7 characters of a SHA.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// unitsWithIDs returns placeholder units carrying only ids.
func unitsWithIDs(ids []string) []model.ChangeUnit {
	out := make([]model.ChangeUnit, len(ids))
	for i, id := range ids {
		out[i].ID = id
	}
	return out
}

// sinceBaseUnits returns the unit ids of the item analysed at head base,
// parsed the way stage 1 parses (merge base with the item's base ref, git
// diff, diff.Parse, parse.Split), cached in nsSinceUnits.
func (p *Pipeline) sinceBaseUnits(ctx context.Context, it model.Item, base string) ([]string, error) {
	key := it.ID + "|" + base
	if b, ok, err := p.d.Store.CacheGet(ctx, nsSinceUnits, key); err == nil && ok {
		var ids []string
		if err := json.Unmarshal(b, &ids); err == nil {
			return ids, nil
		}
	}
	mb, d, err := p.localDiff(ctx, it, base)
	if err != nil {
		return nil, err
	}
	files, err := diff.Parse(d)
	if err != nil {
		return nil, fmt.Errorf("pipeline: parse base diff: %w", err)
	}
	us, err := parse.Split(ctx, parse.Input{ItemID: it.ID, BaseSHA: mb, HeadSHA: base, Files: files, Src: p.d.Git})
	if err != nil {
		return nil, fmt.Errorf("pipeline: split base %s: %w", shortSHA(base), err)
	}
	ids := unitIDs(us)
	if b, err := json.Marshal(ids); err == nil {
		if err := p.d.Store.CachePut(ctx, nsSinceUnits, key, b); err != nil {
			p.d.Log.Warn("cache since-you-looked base units failed", "item", it.ID, "err", err)
		}
	}
	return ids, nil
}

// addressedOut is the outcome of addressedComments.
type addressedOut struct {
	rows  []model.AddressedComment
	calls int
	cost  float64
	note  string
}

// addressedComments asks comment_addressed for each of the viewer's earlier
// top-level comments (comments.Previous) that has no current stored answer,
// stores the answers on model.CommentSubject (never replacing a known
// answer with an unknown one) and returns one row per comment in path/line
// order. Without Jev, or when it fails, rows are unknown and the note says
// "labels unavailable". Only cancellation is returned.
func (p *Pipeline) addressedComments(ctx context.Context, it model.Item, head, base string, local bool, units []model.ChangeUnit) (addressedOut, error) {
	viewer := p.viewer(ctx)
	if viewer == "" {
		return addressedOut{}, nil
	}
	stored, err := p.d.Store.Comments(ctx, it.ID)
	if err != nil {
		p.d.Log.Warn("load comments failed; no since-you-looked comments", "item", it.ID, "err", err)
		return addressedOut{note: "comments unavailable"}, nil
	}
	prev := comments.Previous(stored, viewer, head)
	if len(prev) == 0 {
		return addressedOut{}, nil
	}
	cases := p.commentCases(ctx, prev, head, base, local, units)
	ans, err := p.addressedLabels(ctx, it.ID, cases)
	if err != nil {
		return addressedOut{}, err
	}
	out := addressedOut{calls: ans.calls, cost: ans.cost}
	unknown := false
	for i, c := range prev {
		row := model.AddressedComment{
			CommentID: c.ID, UnitID: c.UnitID, Path: c.Path, Line: c.Line,
			Excerpt: comments.Excerpt(c.Body), Resolved: c.State == model.CommentResolved,
		}
		if l, ok := ans.byID[cases[i].ID]; ok {
			row.Known, row.P, row.Addressed = true, l.P, l.P >= 0.5
		} else {
			unknown = true
		}
		out.rows = append(out.rows, row)
	}
	if unknown {
		out.note = noteLabelsUnavailable
	}
	return out, nil
}

// commentCases builds the comment_addressed cases for cs: the code around
// each comment's line on the commit it was made on (else the base) and the
// diff of its file from that commit to head, so a fix pushed before the
// Since base still counts (DECISIONS "comment_addressed diffs from the
// comment's commit"); from base to head when the comment's commit is not
// local; and, when neither is, the current diff of the unit the comment is
// anchored to. One git diff per distinct start commit.
func (p *Pipeline) commentCases(ctx context.Context, cs []model.Comment, head, base string, local bool, units []model.ChangeUnit) []judge.CommentCase {
	diffs := map[string]map[string]string{} // by start commit; nil: unavailable
	diffFrom := func(from string) map[string]string {
		if m, ok := diffs[from]; ok {
			return m
		}
		var m map[string]string
		if (from == base && local) || (from != base && p.haveCommit(ctx, from)) {
			m = p.fileDiffs(ctx, from, head)
		}
		diffs[from] = m
		return m
	}
	byID := unitsByID(units)
	out := make([]judge.CommentCase, len(cs))
	for i, c := range cs {
		cc := judge.CommentCase{ID: c.ID, Path: c.Path, Line: c.Line, Side: c.Side, Body: c.Body}
		cc.Before = p.codeAround(ctx, []string{c.CommitSHA, base}, c.Path, c.Line)
		var fd map[string]string
		if c.CommitSHA != "" && c.CommitSHA != head {
			fd = diffFrom(c.CommitSHA)
		}
		if fd == nil {
			fd = diffFrom(base)
		}
		if fd != nil {
			cc.Diff = fd[c.Path]
		} else if u, ok := byID[c.UnitID]; ok {
			cc.Diff = u.Diff
		}
		out[i] = cc
	}
	return out
}

// fileDiffs returns the base..head diff split per file (keyed by new and
// old path), or nil when it cannot be computed.
func (p *Pipeline) fileDiffs(ctx context.Context, base, head string) map[string]string {
	d, err := p.d.Git.Diff(ctx, base, head)
	if err != nil {
		p.d.Log.Warn("since-you-looked diff failed", "base", base, "head", head, "err", err)
		return nil
	}
	files, err := diff.Parse(d)
	if err != nil {
		p.d.Log.Warn("since-you-looked diff unparsable", "base", base, "head", head, "err", err)
		return nil
	}
	out := make(map[string]string, len(files))
	for _, f := range files {
		text := renderHunks(f.Hunks)
		for _, path := range []string{f.OldPath, f.NewPath} {
			if path != "" {
				out[path] = text
			}
		}
	}
	return out
}

// renderHunks renders hunks in unified form (header line, then body).
func renderHunks(hs []diff.Hunk) string {
	var b strings.Builder
	for _, h := range hs {
		b.WriteString(h.Header())
		b.WriteByte('\n')
		if body := h.Body(); body != "" {
			b.WriteString(body)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// codeAround returns up to beforeContext numbered lines ("<n>: <text>")
// either side of line in path at the first rev that has the file, or ""
// when none does.
func (p *Pipeline) codeAround(ctx context.Context, revs []string, path string, line int) string {
	if p.d.Git == nil || line <= 0 {
		return ""
	}
	for _, rev := range revs {
		if rev == "" {
			continue
		}
		b, err := p.d.Git.Show(ctx, rev, path)
		if err != nil {
			continue
		}
		return numberedWindow(string(b), line, beforeContext)
	}
	return ""
}

// numberedWindow returns lines line−n … line+n of text, each prefixed with
// its 1-based number, or "" when line is past the end.
func numberedWindow(text string, line, n int) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if line > len(lines) {
		return ""
	}
	var b strings.Builder
	for i := max(1, line-n); i <= min(len(lines), line+n); i++ {
		fmt.Fprintf(&b, "%d: %s\n", i, lines[i-1])
	}
	return b.String()
}

// addressedLabelsOut holds the current known comment_addressed answers by
// comment id, and the Jev calls made for them.
type addressedLabelsOut struct {
	byID  map[string]model.Label
	calls int
	cost  float64
}

// addressedLabels returns the current known comment_addressed label of each
// case: stored ones whose Ctx matches judge.AddressedCtx, and fresh answers
// from Jev for the rest. Fresh labels are stored, except unknown ones that
// would replace a known stored answer. Only cancellation is returned.
func (p *Pipeline) addressedLabels(ctx context.Context, itemID string, cases []judge.CommentCase) (addressedLabelsOut, error) {
	out := addressedLabelsOut{byID: map[string]model.Label{}}
	subjects := make([]string, len(cases))
	for i, cc := range cases {
		subjects[i] = model.CommentSubject(itemID, cc.ID)
	}
	stored, err := p.d.Store.Labels(ctx, subjects)
	if err != nil {
		p.d.Log.Warn("load comment labels failed", "item", itemID, "err", err)
	}
	have := addressedBySubject(stored)
	var need []judge.CommentCase
	for i, cc := range cases {
		if l, ok := have[subjects[i]]; ok && !model.IsUnknown(l) && l.Ctx == judge.AddressedCtx(cc) {
			out.byID[cc.ID] = l
			continue
		}
		need = append(need, cc)
	}
	if len(need) == 0 || p.d.Jev == nil {
		return out, nil
	}
	res, err := judge.CommentAddressed(ctx, p.d.Jev, judge.AddressedInput{
		ItemID: itemID, Cases: need, Catalogue: p.catalogue(), Concurrency: p.concurrency(),
	})
	out.calls, out.cost = res.Calls, res.Cost
	if err != nil {
		if ctx.Err() != nil {
			return out, fmt.Errorf("pipeline: comment_addressed: %w", errors.Join(ctx.Err(), err))
		}
		p.d.Log.Warn("comment_addressed failed; answers unknown", "item", itemID, "err", err)
	}
	fresh := addressedBySubject(res.Labels)
	for _, cc := range need {
		if l, ok := fresh[model.CommentSubject(itemID, cc.ID)]; ok && !model.IsUnknown(l) && l.Ctx == judge.AddressedCtx(cc) {
			out.byID[cc.ID] = l
		}
	}
	if put := keepKnownAnswers(res.Labels, stored); len(put) > 0 {
		if err := p.d.Store.PutLabels(ctx, put); err != nil {
			p.d.Log.Warn("store comment labels failed", "item", itemID, "err", err)
		}
	}
	return out, nil
}

// addressedBySubject indexes the item-level comment_addressed labels of ls
// by subject.
func addressedBySubject(ls []model.Label) map[string]model.Label {
	out := map[string]model.Label{}
	for _, l := range ls {
		if l.Question == model.QCommentAddressed && l.Line == 0 {
			out[l.UnitID] = l
		}
	}
	return out
}

// keepKnownAnswers returns fresh without the unknown labels whose subject
// and question already have a known stored answer (stage 4's rule: an
// outage never erases an earlier answer).
func keepKnownAnswers(fresh, stored []model.Label) []model.Label {
	type key struct{ subject, q string }
	known := map[key]bool{}
	for _, l := range stored {
		if !model.IsUnknown(l) {
			known[key{l.UnitID, l.Question}] = true
		}
	}
	out := make([]model.Label, 0, len(fresh))
	for _, l := range fresh {
		if model.IsUnknown(l) && known[key{l.UnitID, l.Question}] {
			continue
		}
		out = append(out, l)
	}
	return out
}
