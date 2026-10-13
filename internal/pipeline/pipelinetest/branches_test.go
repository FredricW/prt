package pipelinetest_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/pipeline/pipelinetest"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/testutil"
)

// runBranches runs the fixture PRs, then the fixture branches, in f.
func runBranches(t *testing.T, f *pipelinetest.Fixture) map[string]pipelinetest.Outcome {
	t.Helper()
	ctx := context.Background()
	if _, err := f.RunAll(ctx, pipeline.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	outs, err := f.RunBranches(ctx, pipeline.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return outs
}

// TestRecordedBranchReplay runs the local branch 05-spike-dark-mode
// strictly from the Jev and embedding recordings (no reviewers): the
// branch block has a known Jev state from the option set, copied onto the
// item, and the title note says there is no review model.
func TestRecordedBranchReplay(t *testing.T) {
	f := openFixture(t, review.Tiers{}, nil)
	outs := runBranches(t, f)
	states := []string{model.BranchWIP, model.BranchReady, model.BranchSpike, model.BranchAbandoned, model.BranchSuperseded}
	for _, ref := range pipelinetest.BranchRefs {
		o := outs[ref]
		for _, rl := range o.Failed() {
			t.Errorf("%s: stage %s failed: %s", ref, rl.Stage, rl.Err)
		}
		b := o.Agg.Branch
		if b == nil {
			t.Fatalf("%s: no branch block", ref)
		}
		if !b.Known || !slices.Contains(states, b.State) || b.P <= 0 {
			t.Errorf("%s: state %+v", ref, b)
		}
		if o.Item.Kind != model.KindBranch || o.Item.BranchState != b.State || o.Item.BranchStateP != b.P {
			t.Errorf("%s: item %q %.2f, block %q %.2f", ref, o.Item.BranchState, o.Item.BranchStateP, b.State, b.P)
		}
		if b.Unpushed != 2 || len(b.Commits) != 2 || b.LastCommitAt.IsZero() || b.TitleNote != "no review model" {
			t.Errorf("%s: git facts or title note %+v", ref, b)
		}
		if b.State == model.BranchSuperseded && b.SupersededBy == 0 {
			t.Logf("%s: superseded without a matching open PR title", ref)
		}
		t.Logf("%s: %s %.2f (%s)", ref, b.State, b.P, b.Rule)
	}
}

// TestRecordedBranchReviewReplay adds the recorded reviews (PRs first, then
// the branch), strictly: every review request of the PRs and of the branch
// — its group title, fast review, TL;DR and suggested PR title, recorded
// in M5.2 once the fast review's output cap was raised (it used to stop at
// max_tokens before its JSON) — has a recording, and the branch block
// holds (BranchExpectations: a known state, a suggested PR title of at
// most 72 characters).
func TestRecordedBranchReviewReplay(t *testing.T) {
	dir := testutil.RecordingsDir(t, "review")
	cfg := config.Defaults()
	fast := &reviewMisses{next: &review.Replay{Name: cfg.Models.Fast, Dir: dir}}
	deep := &reviewMisses{next: &review.Replay{Name: cfg.Models.Deep, Dir: dir}}
	f := openFixture(t, review.Tiers{Fast: fast, Deep: deep}, nil)
	ctx := context.Background()
	if _, err := f.RunAll(ctx, pipeline.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if misses := append(fast.list(), deep.list()...); len(misses) > 0 {
		t.Fatalf("%d PR review requests have no recording in %s: %v", len(misses), dir, misses)
	}
	outs, err := f.RunBranches(ctx, pipeline.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if misses := append(fast.list(), deep.list()...); len(misses) > 0 {
		t.Fatalf("%d branch review requests have no recording (re-record with `make record-review`); first miss: %s",
			len(misses), strings.SplitN(misses[0], "\n", 2)[0])
	}
	reportAll(t, pipelinetest.BranchExpectations(outs))
}
