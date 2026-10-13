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

// CommentCase is one earlier review comment to ask comment_addressed about
// (spec catalogue: item scope, inputs "comment + new diff").
type CommentCase struct {
	ID   string // Comment.ID
	Path string
	Line int
	Side model.Side
	Body string
	// Before is the code around the comment's line on the commit it was
	// made on (up to 10 lines either side, numbered), "" when unavailable.
	Before string
	// Diff is the change to Path from the base head to the analysed head
	// (unified diff); when the base head is not available locally, the
	// current diff of the unit the comment is anchored to.
	Diff string
}

// AddressedInput is the input of CommentAddressed.
type AddressedInput struct {
	ItemID      string
	Cases       []CommentCase
	Catalogue   questions.Catalogue // the live item-scope "comment_addressed" question
	Concurrency int
}

// commentState is the state of a comment_addressed request.
type commentState struct {
	Comment commentText `json:"comment"`
	// Before is the commented code when the comment was written.
	Before string `json:"codeWhenCommented,omitempty"`
	// Diff is what the latest push changed in the file.
	Diff string `json:"latestPushDiff"`
}

type commentText struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"` // "old" or "new"
	Body string `json:"body"`
}

// maxCommentBytes caps a comment body in a request.
const maxCommentBytes = 4000

// CommentAddressed asks the boolean comment_addressed for every case: one
// request per comment. Labels have UnitID model.CommentSubject(itemID, id),
// Question model.QCommentAddressed, Ctx = AddressedCtx(case). Like the
// other judge calls it never fails on a bad answer (unknown labels) and
// returns only context cancellation and programming errors. Cases with the
// same id are asked once.
func CommentAddressed(ctx context.Context, c jev.Client, in AddressedInput) (Result, error) {
	return commentAddressed(ctx, c, in, jevLimits())
}

func commentAddressed(ctx context.Context, c jev.Client, in AddressedInput, lim limits) (Result, error) {
	q, ok := liveQuestion(in.Catalogue, questions.ScopeItem, model.QCommentAddressed)
	if !ok {
		return Result{}, nil
	}
	binds, _ := bindings(q, questions.Dynamic{})
	if len(binds) == 0 {
		return Result{}, nil
	}
	var jobs []job
	seen := map[string]bool{}
	for _, cc := range in.Cases {
		subj := model.CommentSubject(in.ItemID, cc.ID)
		if seen[subj] {
			continue
		}
		seen[subj] = true
		before, diff := capLines(cc.Before), capLines(cc.Diff)
		text := commentText{Path: cc.Path, Line: cc.Line, Side: sideName(cc.Side), Body: capBytes(cc.Body, maxCommentBytes)}
		build := func(keep int) any {
			st := commentState{Comment: text, Diff: cutDiff(diff, keep)}
			if len(before) > 0 && before[0] != "" {
				st.Before = cutDiff(before, keep)
			}
			return st
		}
		req, err := fitRequest(build, max(len(before), len(diff)), binds, lim)
		if err != nil {
			return Result{}, fmt.Errorf("judge: comment %s: %w", subj, err)
		}
		jobs = append(jobs, job{subject: subj, ctx: AddressedCtx(cc), req: req, binds: binds})
	}
	return run(ctx, c, jobs, nil, in.Concurrency)
}

// AddressedCtx is the Ctx of a comment_addressed label: a hash of the
// comment body, path, line, side, Before and Diff, so a new push (a new
// Diff) asks again and an unchanged one reuses the stored answer.
func AddressedCtx(cc CommentCase) string {
	return model.ContentHash(model.QCommentAddressed, cc.Path, strconv.Itoa(cc.Line), sideName(cc.Side), cc.Body, cc.Before, cc.Diff)[:16]
}

// sideName is "old" for SideOld, else "new".
func sideName(s model.Side) string {
	if s == model.SideOld {
		return string(model.SideOld)
	}
	return string(model.SideNew)
}

// capLines splits s into lines, each capped at maxLineBytes.
func capLines(s string) []string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = capLine(l)
	}
	return lines
}

// capBytes cuts s to at most n bytes on a rune boundary, ending in "…".
func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s[:n])
	return string(r[:len(r)-1]) + "…"
}
