package pipelinetest

import (
	"context"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/review"
)

// BranchRefs are the fixture's local branches without a PR that the
// recordings cover (M5: branch states), run after the PRs in the same
// store so branch.state sees #101–#104 as the open PR titles.
var BranchRefs = []string{"05-spike-dark-mode"}

// fixtureBase is the fixture repo's default branch.
const fixtureBase = "main"

// FixtureRunWithBranches is FixtureRun followed by every branch of
// BranchRefs in the same environment.
func FixtureRunWithBranches(ctx context.Context, repoDir, prsJSON string, jc jev.Client, opts ...Option) (map[int]Outcome, map[string]Outcome, error) {
	f, err := Open(ctx, repoDir, prsJSON, jc, opts...)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	prs, err := f.RunAll(ctx, pipeline.RunOptions{})
	if err != nil {
		return prs, nil, err
	}
	branches, err := f.RunBranches(ctx, pipeline.RunOptions{})
	return prs, branches, err
}

// RunBranches runs every branch of BranchRefs in order.
func (f *Fixture) RunBranches(ctx context.Context, opt pipeline.RunOptions) (map[string]Outcome, error) {
	out := make(map[string]Outcome, len(BranchRefs))
	for _, ref := range BranchRefs {
		oc, err := f.RunBranch(ctx, ref, opt)
		if err != nil {
			return out, err
		}
		out[ref] = oc
	}
	return out, nil
}

// RunBranch runs one local branch of the fixture repo as a branch item,
// stored on the first run the way discovery stores it (head, subject,
// author, upstream, last commit date, ahead and unpushed counts from git).
func (f *Fixture) RunBranch(ctx context.Context, ref string, opt pipeline.RunOptions) (Outcome, error) {
	id := model.BranchItemID(ref)
	it, err := f.Store.Item(ctx, id)
	if err != nil {
		if it, err = f.branchItem(ctx, ref); err != nil {
			return Outcome{}, err
		}
		if err := f.Store.UpsertItem(ctx, it); err != nil {
			return Outcome{}, fmt.Errorf("pipelinetest: store %s: %w", ref, err)
		}
	}
	if err := f.Pipeline.RunWith(ctx, it, opt); err != nil {
		return Outcome{}, fmt.Errorf("pipelinetest: run %s: %w", ref, err)
	}
	return collect(ctx, f.Store, id)
}

// branchItem builds the item of a local fixture branch from its ref.
func (f *Fixture) branchItem(ctx context.Context, ref string) (model.Item, error) {
	refs, err := f.git.Refs(ctx, false)
	if err != nil {
		return model.Item{}, fmt.Errorf("pipelinetest: refs: %w", err)
	}
	i := slices.IndexFunc(refs, func(r gitx.Ref) bool { return r.Name == ref })
	if i < 0 {
		return model.Item{}, fmt.Errorf("pipelinetest: no local branch %q in the fixture repo", ref)
	}
	r := refs[i]
	ahead, err := f.git.CountAhead(ctx, fixtureBase, r.Name)
	if err != nil {
		return model.Item{}, fmt.Errorf("pipelinetest: count ahead %s: %w", ref, err)
	}
	unpushed := ahead
	if r.Upstream != "" {
		if unpushed, err = f.git.CountAhead(ctx, r.Upstream, r.Name); err != nil {
			return model.Item{}, fmt.Errorf("pipelinetest: count unpushed %s: %w", ref, err)
		}
	}
	return model.Item{
		ID: model.BranchItemID(ref), Kind: model.KindBranch, Ref: r.Name, BaseRef: fixtureBase, HeadSHA: r.SHA,
		Title: r.Subject, Author: r.Author, Source: model.SourceRanked, Section: model.SectionBranches,
		UpdatedAt: r.CommitDate, LastCommitAt: r.CommitDate, Upstream: r.Upstream, Ahead: ahead, Unpushed: unpushed,
	}, nil
}

// branchStates are the options of the catalogue's branch.state question.
var branchStates = []string{model.BranchWIP, model.BranchReady, model.BranchSpike, model.BranchAbandoned, model.BranchSuperseded}

// BranchExpectations lists how the branch outcomes deviate from what the
// branch replay test asserts (empty when all hold): every BranchRefs
// branch has a branch block with a known Jev state from the option set, a
// non-empty one-line suggested PR title of at most 72 characters, and the
// state and probability copied onto the stored item.
func BranchExpectations(outs map[string]Outcome) []string {
	var ps []string
	for _, ref := range BranchRefs {
		o, ok := outs[ref]
		if !ok {
			ps = append(ps, ref+": not run")
			continue
		}
		ps = append(ps, branchProblems(ref, o)...)
	}
	return ps
}

func branchProblems(ref string, o Outcome) []string {
	var ps []string
	for _, rl := range o.Failed() {
		ps = append(ps, fmt.Sprintf("%s: stage %s failed: %s", ref, rl.Stage, rl.Err))
	}
	b := o.Agg.Branch
	if b == nil {
		return append(ps, ref+": no branch block")
	}
	if !b.Known {
		ps = append(ps, ref+": branch state unknown (Jev answer missing)")
	}
	if !slices.Contains(branchStates, b.State) {
		ps = append(ps, fmt.Sprintf("%s: state %q not a branch.state option", ref, b.State))
	}
	if b.SuggestedTitle == "" || utf8.RuneCountInString(b.SuggestedTitle) > review.MaxPRTitle {
		ps = append(ps, fmt.Sprintf("%s: suggested title %q (note %q)", ref, b.SuggestedTitle, b.TitleNote))
	}
	if o.Item.BranchState != b.State || o.Item.BranchStateP != b.P {
		ps = append(ps, fmt.Sprintf("%s: item state %q %.2f, block %q %.2f", ref, o.Item.BranchState, o.Item.BranchStateP, b.State, b.P))
	}
	return ps
}
