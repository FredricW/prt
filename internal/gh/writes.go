package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/go-github/v66/github"
	"github.com/shurcooL/githubv4"

	"github.com/fredricw/prt/internal/model"
)

// ErrRejected is wrapped when GitHub refuses a write with 422 Unprocessable
// Entity (for example a comment line outside the diff, or a PR that already
// exists); the error text carries GitHub's message, cut to rejectMsgMax bytes.
var ErrRejected = errors.New("gh: rejected by GitHub")

// ErrBadPR is wrapped by CreatePR when the NewPR lacks a title, head or base.
var ErrBadPR = errors.New("gh: invalid pull request")

// rejectMsgMax caps GitHub's 422 message in ErrRejected errors.
const rejectMsgMax = 300

// rejected maps a go-github 422 error onto ErrRejected with GitHub's
// message; every other error is returned unchanged.
func rejected(err error) error {
	var er *github.ErrorResponse
	if !errors.As(err, &er) || er.Response == nil || er.Response.StatusCode != http.StatusUnprocessableEntity {
		return err
	}
	parts := []string{er.Message}
	for _, e := range er.Errors {
		if e.Message != "" {
			parts = append(parts, e.Message)
		} else {
			parts = append(parts, e.Error())
		}
	}
	return fmt.Errorf("%w: %s", ErrRejected, truncateUTF8(strings.Join(parts, ": "), rejectMsgMax))
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// reviewResponse is the part of GitHub's review object prt reads.
type reviewResponse struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
}

// SubmitReview implements Client. It sends exactly BuildReviewRequest's body
// (an invalid submission returns its ErrBadReview error and sends nothing),
// then lists the review's comments to learn their GitHub ids. A failed
// follow-up listing is not fatal: the comments then keep GHID 0.
func (a *API) SubmitReview(ctx context.Context, number int, r ReviewSubmission) (SubmittedReview, error) {
	body, err := BuildReviewRequest(r)
	if err != nil {
		return SubmittedReview{}, err
	}
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", a.Repo.Owner, a.Repo.Name, number)
	req, err := a.REST.NewRequest(http.MethodPost, path, body)
	if err != nil {
		return SubmittedReview{}, fmt.Errorf("gh: submit review on #%d: %w", number, err)
	}
	var resp reviewResponse
	if _, err := a.REST.Do(ctx, req, &resp); err != nil {
		return SubmittedReview{}, fmt.Errorf("gh: submit review on #%d: %w", number, rejected(err))
	}
	out := SubmittedReview{ID: resp.ID, URL: resp.HTMLURL, Comments: make([]model.Comment, len(r.Comments))}
	for i, c := range r.Comments {
		c.State, c.ReviewID, c.GHID = model.CommentPosted, resp.ID, 0
		out.Comments[i] = c
	}
	if len(r.Comments) == 0 {
		return out, nil
	}
	// The review is posted either way; ids are best effort (GHID stays 0).
	if posted, err := a.reviewComments(ctx, number, resp.ID); err == nil {
		assignGHIDs(out.Comments, posted)
	}
	return out, nil
}

// reviewComments lists every comment of one review (all pages).
func (a *API) reviewComments(ctx context.Context, number int, reviewID int64) ([]*github.PullRequestComment, error) {
	opts := &github.ListOptions{PerPage: 100}
	var out []*github.PullRequestComment
	for {
		page, resp, err := a.REST.PullRequests.ListReviewComments(ctx, a.Repo.Owner, a.Repo.Name, number, reviewID, opts)
		if err != nil {
			return nil, fmt.Errorf("gh: comments of review %d on #%d: %w", reviewID, number, err)
		}
		out = append(out, page...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// assignGHIDs sets GHID on each submitted comment from GitHub's listing:
// first by path + line + body, then the still unmatched comments take the
// unused listed ones in order. Comments left over keep GHID 0.
func assignGHIDs(submitted []model.Comment, posted []*github.PullRequestComment) {
	used := make([]bool, len(posted))
	matched := make([]bool, len(submitted))
	for i, c := range submitted {
		for j, p := range posted {
			if !used[j] && p.GetPath() == c.Path && p.GetLine() == c.Line && p.GetBody() == c.Body {
				submitted[i].GHID, used[j], matched[i] = p.GetID(), true, true
				break
			}
		}
	}
	j := 0
	for i := range submitted {
		if matched[i] {
			continue
		}
		for j < len(posted) && used[j] {
			j++
		}
		if j == len(posted) {
			return
		}
		submitted[i].GHID, used[j] = posted[j].GetID(), true
	}
}

// replyMutation is addPullRequestReviewThreadReply with the new comment.
type replyMutation struct {
	AddPullRequestReviewThreadReply struct {
		Comment struct {
			reviewCommentNode
			Path         string
			Line         *int
			OriginalLine *int
		}
	} `graphql:"addPullRequestReviewThreadReply(input: $input)"`
}

// Reply implements Client. The returned comment has Source github, State
// posted, ID gh-<databaseId>, ThreadID, Path and Line (originalLine with
// Outdated when the thread is outdated). ItemID and Side are left for the
// caller to copy from the thread (the mutation payload has no diff side).
func (a *API) Reply(ctx context.Context, threadID, body string) (model.Comment, error) {
	if strings.TrimSpace(body) == "" {
		return model.Comment{}, fmt.Errorf("gh: reply to %s: empty body", threadID)
	}
	var m replyMutation
	input := githubv4.AddPullRequestReviewThreadReplyInput{
		PullRequestReviewThreadID: githubv4.ID(threadID),
		Body:                      githubv4.String(body),
	}
	if err := a.GQL.Mutate(ctx, &m, input, nil); err != nil {
		return model.Comment{}, fmt.Errorf("gh: reply to %s: %w", threadID, err)
	}
	n := m.AddPullRequestReviewThreadReply.Comment
	if n.DatabaseID == 0 {
		return model.Comment{}, fmt.Errorf("gh: reply to %s: no comment returned", threadID)
	}
	c := commentFromNode(n.reviewCommentNode)
	c.ThreadID, c.Path, c.Line = threadID, n.Path, intOr0(n.Line)
	if c.Line == 0 {
		c.Line, c.Outdated = intOr0(n.OriginalLine), true
	}
	return c, nil
}

// resolveMutation is resolveReviewThread with the thread's new state.
type resolveMutation struct {
	ResolveReviewThread struct {
		Thread struct {
			ID         string
			IsResolved bool
		}
	} `graphql:"resolveReviewThread(input: $input)"`
}

// ResolveThread implements Client; it fails when GitHub reports the thread
// still unresolved.
func (a *API) ResolveThread(ctx context.Context, threadID string) error {
	var m resolveMutation
	input := githubv4.ResolveReviewThreadInput{ThreadID: githubv4.ID(threadID)}
	if err := a.GQL.Mutate(ctx, &m, input, nil); err != nil {
		return fmt.Errorf("gh: resolve thread %s: %w", threadID, err)
	}
	if !m.ResolveReviewThread.Thread.IsResolved {
		return fmt.Errorf("gh: resolve thread %s: GitHub reports it still unresolved", threadID)
	}
	return nil
}

// validateNewPR checks the fields GitHub requires to open a PR.
func validateNewPR(p NewPR) error {
	switch {
	case strings.TrimSpace(p.Title) == "":
		return fmt.Errorf("%w: no title", ErrBadPR)
	case strings.TrimSpace(p.Head) == "":
		return fmt.Errorf("%w: no head branch", ErrBadPR)
	case strings.TrimSpace(p.Base) == "":
		return fmt.Errorf("%w: no base branch", ErrBadPR)
	}
	return nil
}

// CreatePR implements Client (REST POST /repos/{o}/{r}/pulls with title,
// head, base, body and draft). An incomplete NewPR returns ErrBadPR before
// any request; a 422 (e.g. a PR for head already exists) is ErrRejected.
func (a *API) CreatePR(ctx context.Context, p NewPR) (PR, error) {
	if err := validateNewPR(p); err != nil {
		return PR{}, err
	}
	in := &github.NewPullRequest{
		Title: github.String(p.Title),
		Head:  github.String(p.Head),
		Base:  github.String(p.Base),
		Body:  github.String(p.Body),
		Draft: github.Bool(p.Draft),
	}
	pr, _, err := a.REST.PullRequests.Create(ctx, a.Repo.Owner, a.Repo.Name, in)
	if err != nil {
		return PR{}, fmt.Errorf("gh: create PR %s → %s: %w", p.Head, p.Base, rejected(err))
	}
	return prFromREST(pr), nil
}

// prFromREST maps a REST pull request onto PR with the fields PR() fills
// from GraphQL (CI is unknown until the next metadata refresh).
func prFromREST(pr *github.PullRequest) PR {
	out := PR{
		Number:       pr.GetNumber(),
		Title:        pr.GetTitle(),
		Body:         pr.GetBody(),
		Author:       pr.GetUser().GetLogin(),
		State:        strings.ToLower(pr.GetState()),
		Draft:        pr.GetDraft(),
		HeadRef:      pr.GetHead().GetRef(),
		HeadSHA:      pr.GetHead().GetSHA(),
		BaseRef:      pr.GetBase().GetRef(),
		BaseSHA:      pr.GetBase().GetSHA(),
		URL:          pr.GetHTMLURL(),
		Additions:    pr.GetAdditions(),
		Deletions:    pr.GetDeletions(),
		ChangedFiles: pr.GetChangedFiles(),
		CreatedAt:    pr.GetCreatedAt().Time,
		UpdatedAt:    pr.GetUpdatedAt().Time,
		CI:           model.CIUnknown,
	}
	if pr.GetMerged() {
		out.State = "merged"
	}
	if hr, br := pr.GetHead().GetRepo(), pr.GetBase().GetRepo(); hr != nil && br != nil {
		out.CrossRepo = hr.GetFullName() != br.GetFullName()
	}
	for _, l := range pr.Labels {
		out.Labels = append(out.Labels, l.GetName())
	}
	return out
}
