package judge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
)

// MaxOpenPRTitles is the most open PR titles a branch.state request carries
// (the newest PRs first, as the caller orders them).
const MaxOpenPRTitles = 30

// maxBranchSubjects is the most commit subjects a branch.state request
// carries before the token fit cuts further (head and tail kept).
const maxBranchSubjects = 50

// maxSubjectBytes caps one commit subject or PR title in a request.
const maxSubjectBytes = 300

// BranchStateInput is the input of BranchState (spec catalogue: branch
// scope "state", inputs log, age and open PR titles).
type BranchStateInput struct {
	ItemID string // the branch item's id ("branch:<ref>")
	Ref    string // branch name, sent with the log
	// Subjects are the branch's commit subjects, newest first (the commits
	// ahead of its base).
	Subjects []string
	// AgeDays is the whole days since the head commit; Unpushed the commits
	// not on the upstream (all commits ahead when there is none).
	AgeDays  int
	Unpushed int
	// OpenPRTitles are the repository's open pull request titles; only the
	// first MaxOpenPRTitles are sent.
	OpenPRTitles []string
	Catalogue    questions.Catalogue // the live branch-scope "state" question
}

// branchState is the state of a branch.state request.
type branchState struct {
	Branch   string   `json:"branch"`
	Subjects []string `json:"commitSubjects"` // newest first
	AgeDays  int      `json:"daysSinceLastCommit"`
	Unpushed int      `json:"unpushedCommits"`
	OpenPRs  []string `json:"openPullRequestTitles"`
}

// BranchState asks the choice "state" (WIP, ready for PR, spike, abandoned,
// superseded) about one branch: one request. The label has UnitID
// model.BranchSubject(ItemID), Question model.QState and Ctx =
// BranchCtx(in), so a new commit, another day of age or a changed PR list
// asks again. Like the other judge calls it never fails on a bad answer
// (unknown label, jev.ErrUnavailable reported in Result) and returns only
// context cancellation and programming errors. A deferred or missing
// question asks nothing (empty Result).
func BranchState(ctx context.Context, c jev.Client, in BranchStateInput) (Result, error) {
	return branchStateWith(ctx, c, in, jevLimits())
}

func branchStateWith(ctx context.Context, c jev.Client, in BranchStateInput, lim limits) (Result, error) {
	q, ok := liveQuestion(in.Catalogue, questions.ScopeBranch, model.QState)
	if !ok {
		return Result{}, nil
	}
	binds, _ := bindings(q, questions.Dynamic{})
	if len(binds) == 0 {
		return Result{}, nil
	}
	in = normalizeBranch(in)
	subj := model.BranchSubject(in.ItemID)
	build := func(keep int) any {
		return branchState{
			Branch: in.Ref, Subjects: cutList(in.Subjects, keep), AgeDays: in.AgeDays,
			Unpushed: in.Unpushed, OpenPRs: in.OpenPRTitles,
		}
	}
	req, err := fitRequest(build, len(in.Subjects), binds, lim)
	if err != nil {
		return Result{}, fmt.Errorf("judge: branch %s: %w", subj, err)
	}
	return run(ctx, c, []job{{subject: subj, ctx: BranchCtx(in), req: req, binds: binds}}, nil, 1)
}

// normalizeBranch caps the input's lists and texts as they are sent, so
// BranchCtx hashes exactly what Jev sees.
func normalizeBranch(in BranchStateInput) BranchStateInput {
	subjects := in.Subjects
	if len(subjects) > maxBranchSubjects {
		subjects = subjects[:maxBranchSubjects]
	}
	titles := in.OpenPRTitles
	if len(titles) > MaxOpenPRTitles {
		titles = titles[:MaxOpenPRTitles]
	}
	in.Subjects, in.OpenPRTitles = capTexts(subjects), capTexts(titles)
	in.Ref = capBytes(in.Ref, maxSubjectBytes)
	return in
}

// capTexts returns a copy of xs with every entry trimmed to one line and
// capped at maxSubjectBytes (never nil, so the state always has a list).
func capTexts(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		line, _, _ := strings.Cut(strings.TrimSpace(x), "\n")
		out = append(out, capBytes(line, maxSubjectBytes))
	}
	return out
}

// cutList keeps keep entries of xs: the first ceil(keep/2), an elision
// marker, the last floor(keep/2). keep < 0 or keep >= len(xs) keeps all.
func cutList(xs []string, keep int) []string {
	if keep < 0 || keep >= len(xs) {
		return xs
	}
	head, tail := (keep+1)/2, keep/2
	out := make([]string, 0, keep+1)
	out = append(out, xs[:head]...)
	out = append(out, fmt.Sprintf("… %d commits elided …", len(xs)-keep))
	return append(out, xs[len(xs)-tail:]...)
}

// BranchCtx is the Ctx of a branch.state label: a hash of the inputs as
// they are sent (ref, subjects, age, unpushed count, open PR titles).
func BranchCtx(in BranchStateInput) string {
	in = normalizeBranch(in)
	return model.ContentHash(model.QState, in.Ref, strings.Join(in.Subjects, "\n"),
		strconv.Itoa(in.AgeDays), strconv.Itoa(in.Unpushed), strings.Join(in.OpenPRTitles, "\n"))[:16]
}
