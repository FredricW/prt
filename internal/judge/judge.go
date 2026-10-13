// Package judge asks Jev the M2 questions that are not per unit or per line
// (spec "Pipeline internals" stage 4 pairs and stage 5 naming, catalogue
// scopes pair, group and file):
//
//   - Pairs: for each embed/mechanical candidate pair, the Choice
//     "relation" ∈ {same intent, caller update, test for, same pattern,
//     unrelated}; Confirm keeps p ≥ keep_pair_p and not unrelated.
//   - Names: per group, the Choice "name_bullet" over the PR description
//     bullets plus none; ApplyNames turns answers into group names.
//   - SeparatePR: per changed file, the boolean "separate_pr" given the
//     group list; Suggestion turns answers into the split suggestion.
//
// Answers are model.Labels on subjects model.PairSubject,
// model.GroupSubject and model.FileSubject, so the pipeline caches them in
// the label store like unit labels. Like internal/labels, this package does
// no caching itself and never fails on a bad answer: missing or malformed
// answers become unknown labels, and jev.ErrUnavailable is reported in
// Result (with a 3-strike circuit breaker per call of Pairs, Names or
// SeparatePR), not returned.
//
// Requests are deterministic (fixed-order state structs, no floats from
// embeddings), so recordings keyed by jev.RequestKey replay. A request over
// the token limits has its diffs cut to a common per-diff line cap (head and
// tail kept, elision marker between).
package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
)

// Result is the outcome of one batch of Jev calls.
type Result struct {
	// Labels holds one label per asked (subject, question); unknown labels
	// (Value model.LabelUnknown) for calls that failed or answers that were
	// missing or malformed.
	Labels []model.Label
	Calls  int
	Cost   float64
	// Answered and Failed count requests; Unavailable is set when any call
	// failed with jev.ErrUnavailable, Tripped when the circuit breaker (3
	// consecutive ErrUnavailable) stopped further calls.
	Answered, Failed     int
	Unavailable, Tripped bool
}

// Pair is one candidate pair to judge. The order of A and B does not
// matter: requests and subjects use the units sorted by id.
type Pair struct {
	A, B   model.ChangeUnit
	Cosine float64
}

// PairInput is the input of Pairs.
type PairInput struct {
	Pairs       []Pair
	Catalogue   questions.Catalogue // the live pair-scope "relation" question is asked
	Concurrency int                 // parallel requests (default 4)
}

// Pairs asks "relation" for every pair: one request per pair. Labels have
// UnitID model.PairSubject(a, b), Question model.QRelation, Ctx "". Only
// context cancellation (and programming errors such as an over-limit
// question) is returned as an error. Pairs with the same subject are asked
// once.
func Pairs(ctx context.Context, c jev.Client, in PairInput) (Result, error) {
	return pairs(ctx, c, in, jevLimits())
}

func pairs(ctx context.Context, c jev.Client, in PairInput, lim limits) (Result, error) {
	q, ok := liveQuestion(in.Catalogue, questions.ScopePair, model.QRelation)
	if !ok {
		return Result{}, nil
	}
	binds, _ := bindings(q, questions.Dynamic{})
	if len(binds) == 0 {
		return Result{}, nil
	}
	var jobs []job
	seen := map[string]bool{}
	for _, p := range in.Pairs {
		a, b := p.A, p.B
		if b.ID < a.ID {
			a, b = b, a
		}
		subj := model.PairSubject(a.ID, b.ID)
		if seen[subj] {
			continue
		}
		seen[subj] = true
		ds := prepare(a, b)
		build := func(keep int) any { return pairState{A: ds[0].state(keep), B: ds[1].state(keep)} }
		req, err := fitRequest(build, maxLines(ds), binds, lim)
		if err != nil {
			return Result{}, fmt.Errorf("judge: pair %s (%s, %s): %w", subj, a.Path, b.Path, err)
		}
		jobs = append(jobs, job{subject: subj, req: req, binds: binds})
	}
	return run(ctx, c, jobs, nil, in.Concurrency)
}

// DefaultKeepPairP is the keep_pair_p used by Confirm when keepP ≤ 0.
const DefaultKeepPairP = 0.6

// Confirm returns the candidates whose pair has a known relation label
// other than unrelated with P ≥ keepP, as links with Source jev, Relation
// and P set (Kind, From, To and Weight kept), in candidate order; rejected
// counts the judged candidates that were not kept. Candidates with an
// unknown or missing label are neither kept nor rejected.
func Confirm(cands []model.Link, labels []model.Label, keepP float64) (kept []model.Link, rejected int) {
	if keepP <= 0 {
		keepP = DefaultKeepPairP
	}
	rel := map[string]model.Label{}
	for _, l := range labels {
		if l.Question == model.QRelation {
			rel[l.UnitID] = l
		}
	}
	for _, c := range cands {
		l, ok := rel[model.PairSubject(c.From, c.To)]
		if !ok || model.IsUnknown(l) {
			continue
		}
		if l.Value == model.RelUnrelated || l.P < keepP {
			rejected++
			continue
		}
		c.Source, c.Relation, c.P = model.LinkJev, l.Value, l.P
		kept = append(kept, c)
	}
	return kept, rejected
}

// GroupText is one group to name.
type GroupText struct {
	ID    string
	Units []model.ChangeUnit // in review order
}

// NameInput is the input of Names.
type NameInput struct {
	Title       string
	Bullets     []string // model.DescBullets(body)
	Groups      []GroupText
	Catalogue   questions.Catalogue // the live group-scope "name_bullet" question
	Concurrency int
}

// Names asks "name_bullet" for every group: one request per group. Labels
// have UnitID model.GroupSubject(id), Question model.QNameBullet, Ctx =
// NameCtx(title, bullets). With no bullets nothing is asked: every group
// gets a decided "none" label (P 1, Model "prt:no-bullets").
func Names(ctx context.Context, c jev.Client, in NameInput) (Result, error) {
	return names(ctx, c, in, jevLimits())
}

func names(ctx context.Context, c jev.Client, in NameInput, lim limits) (Result, error) {
	q, ok := liveQuestion(in.Catalogue, questions.ScopeGroup, model.QNameBullet)
	if !ok {
		return Result{}, nil
	}
	binds, decided := bindings(q, questions.Dynamic{Bullets: in.Bullets})
	nctx := NameCtx(in.Title, in.Bullets)
	var jobs []job
	var fixed []model.Label
	seen := map[string]bool{}
	for _, g := range in.Groups {
		subj := model.GroupSubject(g.ID)
		if seen[subj] {
			continue
		}
		seen[subj] = true
		for _, wq := range decided {
			fixed = append(fixed, decidedLabel(subj, nctx, wq))
		}
		if len(binds) == 0 {
			continue
		}
		ds := prepare(g.Units...)
		build := func(keep int) any {
			return nameState{PR: titleState{Title: in.Title}, Group: groupUnitState{Units: states(ds, keep)}}
		}
		req, err := fitRequest(build, maxLines(ds), binds, lim)
		if err != nil {
			return Result{}, fmt.Errorf("judge: group %s: %w", subj, err)
		}
		jobs = append(jobs, job{subject: subj, ctx: nctx, req: req, binds: binds})
	}
	return run(ctx, c, jobs, fixed, in.Concurrency)
}

// NameCtx is the Ctx of name_bullet labels: a hash of the PR title and
// bullets, so a stored name is reused only for the same description.
func NameCtx(title string, bullets []string) string {
	return model.ContentHash(model.QNameBullet, title, strings.Join(bullets, "\n"))[:16]
}

// maxNameRunes is the longest group name taken from a bullet.
const maxNameRunes = 60

// ApplyNames sets Name, NameSource, NameP and DescBullet on groups whose
// current (Ctx == ctx) name_bullet label picks a bullet bN with P ≥ 0.5:
// Name = the bullet text cut to 60 runes, DescBullet = the full text,
// NameSource model.NameBullet. Other groups keep their placeholder names.
// Returns a new slice.
func ApplyNames(groups []model.Group, labels []model.Label, bullets []string, ctx string) []model.Group {
	byGroup := map[string]model.Label{}
	for _, l := range labels {
		if l.Question == model.QNameBullet && l.Ctx == ctx {
			byGroup[l.UnitID] = l
		}
	}
	out := append([]model.Group(nil), groups...)
	for i, g := range out {
		l, ok := byGroup[model.GroupSubject(g.ID)]
		if !ok || model.IsUnknown(l) || l.P < 0.5 {
			continue
		}
		n := model.BulletIndex(l.Value)
		if n < 1 || n > len(bullets) {
			continue
		}
		text := bullets[n-1]
		out[i].Name = cutRunes(text, maxNameRunes)
		out[i].DescBullet = text
		out[i].NameSource = model.NameBullet
		out[i].NameP = l.P
	}
	return out
}

// cutRunes cuts s to at most n runes, the last being "…" when cut.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// FileText is one changed file to ask separate_pr about.
type FileText struct {
	Path  string
	Units []model.ChangeUnit
}

// GroupSummary is one row of the group list sent with separate_pr.
type GroupSummary struct {
	ID    string
	Name  string
	Paths []string // files with units in the group, sorted
	Units int
}

// SplitInput is the input of SeparatePR.
type SplitInput struct {
	ItemID      string
	Title       string
	Files       []FileText
	Groups      []GroupSummary // non-ungrouped groups, in review order
	Catalogue   questions.Catalogue
	Concurrency int
}

// SeparatePR asks "separate_pr" for every file: one request per file.
// Labels have UnitID model.FileSubject(itemID, path), Question
// model.QSeparatePR, Ctx = SplitCtx(groups).
func SeparatePR(ctx context.Context, c jev.Client, in SplitInput) (Result, error) {
	return separatePR(ctx, c, in, jevLimits())
}

func separatePR(ctx context.Context, c jev.Client, in SplitInput, lim limits) (Result, error) {
	q, ok := liveQuestion(in.Catalogue, questions.ScopeFile, model.QSeparatePR)
	if !ok {
		return Result{}, nil
	}
	binds, _ := bindings(q, questions.Dynamic{})
	if len(binds) == 0 {
		return Result{}, nil
	}
	sctx := SplitCtx(in.Groups)
	groups := groupStates(in.Groups)
	var jobs []job
	seen := map[string]bool{}
	for _, f := range in.Files {
		subj := model.FileSubject(in.ItemID, f.Path)
		if seen[subj] {
			continue
		}
		seen[subj] = true
		ds := prepare(f.Units...)
		build := func(keep int) any {
			return splitState{File: fileState{Path: f.Path, Units: states(ds, keep)}, Groups: groups}
		}
		req, err := fitRequest(build, maxLines(ds), binds, lim)
		if err != nil {
			return Result{}, fmt.Errorf("judge: file %s: %w", f.Path, err)
		}
		jobs = append(jobs, job{subject: subj, ctx: sctx, req: req, binds: binds})
	}
	return run(ctx, c, jobs, nil, in.Concurrency)
}

// SplitCtx is the Ctx of separate_pr labels: a hash of the group list
// (names, sorted paths and unit counts, in the given order; ids excluded).
func SplitCtx(groups []GroupSummary) string {
	// Marshalling strings, string slices and ints cannot fail.
	b, _ := json.Marshal(groupStates(groups))
	return model.ContentHash(model.QSeparatePR, string(b))[:16]
}

// Suggestion builds the split suggestion from current (Ctx == ctx)
// separate_pr labels: files with P(yes) ≥ 0.5, highest P first (ties by
// path), each with the ids of the groups that have units in it (units give
// each unit's path). Nil when no file qualifies.
func Suggestion(itemID string, units []model.ChangeUnit, groups []model.Group, labels []model.Label, ctx string) *model.SplitSuggestion {
	prefix := model.FileSubject(itemID, "")
	ps := map[string]float64{}
	for _, l := range labels {
		path, ok := strings.CutPrefix(l.UnitID, prefix)
		if !ok || l.Question != model.QSeparatePR || l.Ctx != ctx || model.IsUnknown(l) {
			continue
		}
		if l.P >= 0.5 {
			ps[path] = l.P
		} else {
			delete(ps, path)
		}
	}
	if len(ps) == 0 {
		return nil
	}
	unitPath := make(map[string]string, len(units))
	for _, u := range units {
		unitPath[u.ID] = u.Path
	}
	files := make([]model.SplitFile, 0, len(ps))
	for path, p := range ps {
		files = append(files, model.SplitFile{Path: path, P: p, GroupIDs: groupsIn(path, groups, unitPath)})
	}
	sort.Slice(files, func(a, b int) bool {
		if files[a].P != files[b].P {
			return files[a].P > files[b].P
		}
		return files[a].Path < files[b].Path
	})
	return &model.SplitSuggestion{Files: files}
}

// groupsIn returns the ids of groups with a unit in path, in group order.
func groupsIn(path string, groups []model.Group, unitPath map[string]string) []string {
	ids := []string{}
	for _, g := range groups {
		for _, id := range g.UnitIDs {
			if unitPath[id] == path {
				ids = append(ids, g.ID)
				break
			}
		}
	}
	return ids
}
