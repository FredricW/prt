package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline/pipelinetest"
)

// fixtureRun runs the fixture PRs and, with --branches, then the
// fixture's local branches (pipelinetest.BranchRefs) in the same store.
// The branch outcomes are nil without --branches.
func fixtureRun(ctx context.Context, cfg config, repo, prs string, jc jev.Client, opts []pipelinetest.Option) (map[int]pipelinetest.Outcome, map[string]pipelinetest.Outcome, error) {
	if !cfg.branches {
		outs, err := pipelinetest.FixtureRun(ctx, repo, prs, jc, opts...)
		return outs, nil, err
	}
	return pipelinetest.FixtureRunWithBranches(ctx, repo, prs, jc, opts...)
}

// checkRun is checkOutcomes over the PRs plus a failed-stage check of the
// branches.
func checkRun(outs map[int]pipelinetest.Outcome, branches map[string]pipelinetest.Outcome) error {
	err := checkOutcomes(outs)
	var ps []string
	for _, ref := range pipelinetest.BranchRefs {
		o, ok := branches[ref]
		if !ok {
			continue
		}
		for _, rl := range o.Failed() {
			ps = append(ps, fmt.Sprintf("%s: stage %s failed: %s", ref, rl.Stage, rl.Err))
		}
	}
	if len(ps) > 0 {
		err = errors.Join(err, errors.New(strings.Join(ps, "\n")))
	}
	return err
}

// warnBranchExpectations prints how the branch outcomes deviate from
// pipelinetest.BranchExpectations (asserted by the branch replay test);
// nothing without --branches.
func warnBranchExpectations(branches map[string]pipelinetest.Outcome, stdout io.Writer) {
	if branches == nil {
		return
	}
	ps := pipelinetest.BranchExpectations(branches)
	if len(ps) == 0 {
		fmt.Fprintln(stdout, "branch expectations: all hold")
		return
	}
	fmt.Fprintf(stdout, "WARNING: %d branch expectations do not hold with these recordings:\n", len(ps))
	for _, p := range ps {
		fmt.Fprintln(stdout, "  -", p)
	}
}

// isBranchState reports whether req asks branch.state (M5: one request per
// branch item).
func isBranchState(req jev.Request) bool {
	for name := range req.Questions {
		if base, _, _ := strings.Cut(name, "__"); base == model.QState {
			return true
		}
	}
	return false
}

// m5Summary renders the branch.state request counts.
func (c *counter) m5Summary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.branch
	return fmt.Sprintf("M5 requests: branch %s %d (%d recorded, %d to record) — one per branch item (--branches)",
		model.QState, b.requests, b.recorded, b.requests-b.recorded)
}
