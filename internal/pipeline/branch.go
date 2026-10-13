package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/fredricw/prt/internal/aggregate"
	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/judge"
	"github.com/fredricw/prt/internal/model"
)

// nsPRTitle holds suggested branch PR titles by review.RequestKey, value
// proseCache (like nsTitle for group titles).
const nsPRTitle = "pr-title"

// Branch block limits.
const (
	// branchCommits is the most commits BranchInfo.Commits lists.
	branchCommits = 20
	// branchSubjects is the most commit subjects sent to branch.state.
	branchSubjects = 50
	// supersedeMinJaccard is the word overlap an open PR title needs with
	// the branch's subjects and ref to be named in SupersededBy.
	supersedeMinJaccard = 0.3
)

// TitleNote values (BranchInfo.TitleNote) when no title is suggested.
const (
	titleNoModel  = "no review model"
	titleNoGroups = "no groups"
)

// branchOut is the branch block (nil for PRs) and the Jev calls it made.
type branchOut struct {
	info  *model.BranchInfo
	calls int
	cost  float64
}

// branchBlock computes the branch state block of stage 8 (spec Overview
// "Branches"; branch items only): git facts (last commit, idle days,
// unpushed commits), the Jev choice branch.state asked once per input set
// (cached on model.BranchSubject with judge.BranchCtx), the abandoned?
// default and SupersededBy. The suggested PR title is added later
// (suggestTitle). Git and store failures degrade the block; only
// cancellation is returned.
func (p *Pipeline) branchBlock(ctx context.Context, r *run) (branchOut, error) {
	cur := p.currentItem(ctx, r.it)
	if cur.Kind != model.KindBranch {
		return branchOut{}, nil
	}
	subjects := p.branchLog(ctx, r.f.BaseSHA, r.f.HeadSHA, branchSubjects)
	lastAt := cur.LastCommitAt
	if lastAt.IsZero() && len(subjects) > 0 && subjects[0].SHA == r.f.HeadSHA {
		lastAt = subjects[0].Date
	}
	idle := aggregate.IdleDays(lastAt, p.d.Now())
	commits, unpushed := p.unpushed(ctx, cur, r.f)
	prs := p.openPRs(ctx)
	in := judge.BranchStateInput{
		ItemID: cur.ID, Ref: cur.Ref, Subjects: commitSubjects(subjects), AgeDays: int(idle),
		Unpushed: unpushed, OpenPRTitles: prTitles(prs), Catalogue: p.catalogue(),
	}
	l, known, calls, cost, err := p.branchLabel(ctx, r, in)
	if err != nil {
		return branchOut{calls: calls, cost: cost}, err
	}
	info := branchInfo(l, known, idle, r.cfg.Pipeline.AbandonedDays)
	info.LastCommitAt, info.Unpushed, info.Commits = lastAt, unpushed, commits
	if info.State == model.BranchSuperseded {
		info.SupersededBy = supersededBy(prs, append(in.Subjects, cur.Ref))
	}
	return branchOut{info: &info, calls: calls, cost: cost}, nil
}

// branchLog is Git.Log(base, head, limit); an error is logged and yields
// no commits.
func (p *Pipeline) branchLog(ctx context.Context, base, head string, limit int) []model.BranchCommit {
	if p.d.Git == nil || base == "" || head == "" {
		return nil
	}
	cs, err := p.d.Git.Log(ctx, base, head, limit)
	if err != nil {
		p.d.Log.Warn("branch log failed", "base", base, "head", head, "err", err)
		return nil
	}
	out := make([]model.BranchCommit, 0, len(cs))
	for _, c := range cs {
		out = append(out, model.BranchCommit{SHA: c.SHA, Subject: c.Subject, Date: c.Date})
	}
	return out
}

// unpushed returns the commits not on the branch's upstream (all commits
// ahead of the merge base when it has none), newest first and at most
// branchCommits, and their count (CountAhead, else the listed commits). A
// remote-tracking ref (Item.RemoteRef) has none: its commits are on the
// remote by definition.
func (p *Pipeline) unpushed(ctx context.Context, it model.Item, f model.Fetch) ([]model.BranchCommit, int) {
	if it.RemoteRef || strings.HasPrefix(it.Ref, "refs/remotes/") {
		return nil, 0
	}
	base := it.Upstream
	if base == "" {
		base = f.BaseSHA
	}
	commits := p.branchLog(ctx, base, f.HeadSHA, branchCommits)
	if p.d.Git == nil || base == "" {
		return commits, len(commits)
	}
	n, err := p.d.Git.CountAhead(ctx, base, f.HeadSHA)
	if err != nil {
		p.d.Log.Debug("count unpushed failed", "base", base, "err", err)
		return commits, len(commits)
	}
	return commits, n
}

// openPRs returns the stored open PR items (previews, and closed or merged
// PRs added from search, excluded; an unknown state counts as open), newest
// number first, at most judge.MaxOpenPRTitles: the open PR titles
// branch.state reads and SupersededBy picks from. A store error is logged
// and yields none.
func (p *Pipeline) openPRs(ctx context.Context) []model.Item {
	items, err := p.d.Store.Items(ctx)
	if err != nil {
		p.d.Log.Warn("load items for open PR titles failed", "err", err)
		return nil
	}
	var out []model.Item
	for _, it := range items {
		closed := it.PRState == "closed" || it.PRState == "merged"
		if it.Kind == model.KindPR && it.Number > 0 && it.Source != model.SourcePreview && !closed {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Number > out[b].Number })
	if len(out) > judge.MaxOpenPRTitles {
		out = out[:judge.MaxOpenPRTitles]
	}
	return out
}

func commitSubjects(cs []model.BranchCommit) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Subject
	}
	return out
}

func prTitles(prs []model.Item) []string {
	out := make([]string, len(prs))
	for i, it := range prs {
		out[i] = it.Title
	}
	return out
}

// branchLabel returns the current branch.state label: the stored one when
// it is known and asked with the same inputs, else Jev's answer (stored
// unless it is unknown and a known answer is stored). known is false when
// no current known answer exists (Jev unavailable, failed or deferred, or
// a FreeOnly run: cache only, nothing asked or written). Only cancellation
// is returned.
func (p *Pipeline) branchLabel(ctx context.Context, r *run, in judge.BranchStateInput) (model.Label, bool, int, float64, error) {
	subj, bctx := model.BranchSubject(in.ItemID), judge.BranchCtx(in)
	stored, err := p.d.Store.Labels(ctx, []string{subj})
	if err != nil {
		p.d.Log.Warn("load branch state label failed", "item", in.ItemID, "err", err)
	}
	if l, ok := stateLabel(stored, bctx); ok {
		return l, true, 0, 0, nil
	}
	if r.m.Jev == nil || r.opt.FreeOnly {
		return model.Label{}, false, 0, 0, nil
	}
	res, err := p.fn.branchState(ctx, r.m.Jev, in)
	if err != nil {
		if ctx.Err() != nil {
			return model.Label{}, false, res.Calls, res.Cost, fmt.Errorf("pipeline: branch state: %w", errors.Join(ctx.Err(), err))
		}
		p.d.Log.Warn("branch state failed; state unknown", "item", in.ItemID, "err", err)
	}
	if put := keepKnownAnswers(res.Labels, stored); len(put) > 0 {
		if err := p.d.Store.PutLabels(ctx, put); err != nil {
			p.d.Log.Warn("store branch state label failed", "item", in.ItemID, "err", err)
		}
	}
	l, ok := stateLabel(res.Labels, bctx)
	return l, ok, res.Calls, res.Cost, nil
}

// stateLabel finds a known branch.state label with Ctx bctx in ls.
func stateLabel(ls []model.Label, bctx string) (model.Label, bool) {
	for _, l := range ls {
		if l.Question == model.QState && l.Line == 0 && l.Ctx == bctx && !model.IsUnknown(l) {
			return l, true
		}
	}
	return model.Label{}, false
}

// branchInfo turns the branch.state answer into the block's state with
// the abandoned? default (DECISIONS "Branch states (M5 lead calls)"): a
// branch idle for at least abandonedDays (≤ 0: config.DefaultAbandonedDays)
// whose Jev state is WIP or spike, or unknown, shows abandoned with Rule
// "no commits for N days" and P = Jev's P(abandoned) (1 when unknown).
// Ready for PR and superseded are never overridden by age.
func branchInfo(l model.Label, known bool, idle float64, abandonedDays int) model.BranchInfo {
	b := model.BranchInfo{IdleDays: int(max(idle, 0))}
	if known {
		b.State, b.P, b.Probs, b.Known = l.Value, l.P, l.Probs, true
	}
	if abandonedDays <= 0 {
		abandonedDays = config.DefaultAbandonedDays
	}
	if idle < float64(abandonedDays) {
		return b
	}
	if known && b.State != model.BranchWIP && b.State != model.BranchSpike {
		return b
	}
	p := 1.0
	if known {
		p = l.Probs[model.BranchAbandoned]
	}
	b.State, b.P, b.Rule = model.BranchAbandoned, p, fmt.Sprintf("no commits for %d days", b.IdleDays)
	return b
}

// AgeBranchInfo re-applies the abandoned? default of branchInfo to a stored
// block at idle days (the time since the head commit, measured now), for
// readers that do not re-run the pipeline: an idle branch has no new head,
// so nothing else would turn its stored WIP into abandoned? once
// abandonedDays pass. Jev's answer (State, P, Probs, Known) is reused; only
// IdleDays, and State/P/Rule when the age rule applies, change. A smaller
// idle than stored (clock skew) leaves b as it is.
func AgeBranchInfo(b model.BranchInfo, idle float64, abandonedDays int) model.BranchInfo {
	if idle < float64(b.IdleDays) {
		return b
	}
	aged := branchInfo(model.Label{Value: b.State, P: b.P, Probs: b.Probs}, b.Known, idle, abandonedDays)
	out := b
	out.IdleDays = aged.IdleDays
	switch {
	case aged.Rule != "":
		out.State, out.P, out.Rule = aged.State, aged.P, aged.Rule
	case b.Rule != "": // already abandoned by age: keep the count current
		out.Rule = fmt.Sprintf("no commits for %d days", out.IdleDays)
	}
	return out
}

// supersededBy returns the number of the open PR whose title shares the
// most words with texts (the branch's commit subjects and ref; Jaccard
// index over words of at least three letters), when that overlap is at
// least supersedeMinJaccard; ties go to the newest PR. 0 when none
// qualifies.
func supersededBy(prs []model.Item, texts []string) int {
	branch := wordSet(texts...)
	best, bestJ := 0, 0.0
	for _, pr := range prs {
		j := jaccard(branch, wordSet(pr.Title))
		if j >= supersedeMinJaccard && (j > bestJ || (j == bestJ && pr.Number > best)) {
			best, bestJ = pr.Number, j
		}
	}
	return best
}

// wordSet is the set of lower-cased words (letters and digits, at least
// three runes) of texts.
func wordSet(texts ...string) map[string]bool {
	out := map[string]bool{}
	for _, t := range texts {
		for _, w := range strings.FieldsFunc(strings.ToLower(t), func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) }) {
			if len([]rune(w)) >= 3 {
				out[w] = true
			}
		}
	}
	return out
}

// jaccard is |a ∩ b| / |a ∪ b| (0 when both are empty).
func jaccard(a, b map[string]bool) float64 {
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// suggestTitle fills the block's suggested PR title (spec Overview
// "Branches": fast LLM, from the group names): review.BuildPRTitle over
// the non-ungrouped group names in review order (all groups when every
// group is ungrouped) and the ref, cached by request key in nsPRTitle and
// served from the cache without a key, like group titles. A forced run or
// FromStage ≤ aggregate asks again. Without groups or a fast reviewer
// TitleNote says why; the call goes in the item's call log ("" row,
// purpose title) and on the aggregate row.
func (p *Pipeline) suggestTitle(ctx context.Context, r *run, groups []model.Group, b *model.BranchInfo, rl *model.RunLog) {
	names := titleGroupNames(groups)
	fast := r.m.Reviewers.Fast
	switch {
	case len(names) == 0:
		b.TitleNote = titleNoGroups
		return
	case fast == nil:
		b.TitleNote = titleNoModel
		return
	}
	bypass := r.opt.Force || r.opt.ignoresCache(model.StageAggregate)
	req := p.fn.buildPRTitle(names, r.it.Ref)
	out := p.prose(ctx, fast, nsPRTitle, req, callMeta{itemID: r.it.ID, tier: model.TierFast, purpose: purposeTitle}, bypass, p.fn.cleanPRTitle)
	if out.notReady {
		b.TitleNote = titleNoModel
		return
	}
	r.calls.add("", out.calls...)
	if !out.cached {
		rl.Calls += len(out.calls)
	}
	rl.Cost += out.spent()
	switch {
	case out.err != nil:
		p.d.Log.Warn("PR title failed", "item", r.it.ID, "err", out.err)
		b.TitleNote = "title failed: " + reasonText(out.err)
	case out.text == "":
		b.TitleNote = "title empty"
	default:
		b.SuggestedTitle, b.TitleKey = out.text, out.key
	}
}

// titleGroupNames are the names a PR title is written from: the groups
// that are not ungrouped singletons, in review order, else every group's.
func titleGroupNames(groups []model.Group) []string {
	var named, all []string
	for _, g := range groups {
		if g.Name == "" {
			continue
		}
		all = append(all, g.Name)
		if !g.Ungrouped {
			named = append(named, g.Name)
		}
	}
	if len(named) > 0 {
		return named
	}
	return all
}

// idleNow is aggregate.IdleDays of it at the pipeline clock (0 for PRs).
func (p *Pipeline) idleNow(it model.Item) float64 {
	if it.Kind != model.KindBranch {
		return 0
	}
	return aggregate.IdleDays(it.LastCommitAt, p.d.Now())
}
