package gh

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/fredricw/prt/internal/model"
)

// Fake is an in-memory Client for tests and `--fixtures` runs.
//
// SearchPRs returns, in number order, every PR whose number, title, head ref
// or author contains each non-qualifier search term. The state qualifiers
// is:open, is:closed (closed or merged, as on GitHub) and is:merged filter on
// PR.State, where an empty State counts as open; involves:@me (or
// involves:<login>) keeps PRs the user authored, is a requested reviewer
// of, or has viewer activity on; every other qualifier (repo:, is:pr, ...)
// is ignored. All methods are safe for
// concurrent use once the Fake is shared; set fields before sharing it.
type Fake struct {
	mu sync.Mutex

	Login     string
	PRs       map[int]PR
	Diffs     map[int]string
	ThreadMap map[int][]Thread
	Owners    string
	Err       error // when set, every call returns it

	// Repo is "owner/name" (Fixture.Repo); it builds CreatePR URLs
	// ("fake/fake" when empty).
	Repo string
	// HeadSHAs maps a branch ref to the head SHA CreatePR reports for a PR
	// with that head (optional; lets callers fake pushed heads).
	HeadSHAs map[string]string

	// Writes are recorded, never sent: Submitted and Payloads (the exact
	// REST body, see BuildReviewRequest) with PayloadPRs (PR numbers),
	// Replies, Resolved thread ids and Created PRs.
	Submitted  []ReviewSubmission
	Payloads   []ReviewRequest
	PayloadPRs []int
	Replies    []FakeReply
	Created    []NewPR
	Resolved   []string
}

var _ Client = (*Fake)(nil)

// Viewer implements Client.
func (f *Fake) Viewer(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Login, f.Err
}

// SearchPRs implements Client.
func (f *Fake) SearchPRs(_ context.Context, query string, limit int) ([]PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	q := parseFakeQuery(query)
	who := q.involves
	if who == "@me" {
		who = strings.ToLower(f.Login)
	}
	var out []PR
	for _, n := range sortedKeys(f.PRs) {
		p := f.PRs[n]
		if !stateMatches(p.State, q.state) || !matches(p, q.terms) {
			continue
		}
		if q.involves != "" && !involves(p, who, strings.EqualFold(who, f.Login)) {
			continue
		}
		out = append(out, p)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// ListOpenPRs implements Client: open PRs (empty State counts as open) in
// number order, with whatever Files the fixture gives them.
func (f *Fake) ListOpenPRs(_ context.Context, limit int) ([]PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	var out []PR
	for _, n := range sortedKeys(f.PRs) {
		p := f.PRs[n]
		if !stateMatches(p.State, "open") {
			continue
		}
		out = append(out, p)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// fakeQuery is a parsed search query.
type fakeQuery struct {
	terms    []string // lower-cased free text
	state    string   // last is:open/closed/merged ("" when absent)
	involves string   // involves:<login> (@me resolved by the caller), "" when absent
}

// parseFakeQuery splits a search query into its free-text terms and the
// qualifiers the fake honours.
func parseFakeQuery(query string) fakeQuery {
	var q fakeQuery
	for _, w := range strings.Fields(query) {
		lw := strings.ToLower(w)
		switch {
		case lw == "is:open" || lw == "is:closed" || lw == "is:merged":
			q.state = strings.TrimPrefix(lw, "is:")
		case strings.HasPrefix(lw, "involves:"):
			q.involves = strings.TrimPrefix(lw, "involves:")
		case strings.Contains(lw, ":"):
			// other qualifiers are ignored
		default:
			q.terms = append(q.terms, lw)
		}
	}
	return q
}

// involves mirrors GitHub's involves: qualifier with the facts a fake PR
// has: author, requested reviewer, or any recorded viewer activity (the
// viewer-* flags describe login == the fake's Login).
func involves(p PR, login string, isViewer bool) bool {
	if strings.EqualFold(p.Author, login) || slices.ContainsFunc(p.Reviewers, func(r string) bool { return strings.EqualFold(r, login) }) {
		return true
	}
	return isViewer && (p.ReviewRequested || p.ViewerApproved || p.ViewerCommented || p.ViewerAddressed || p.ViewerReviewedSHA != "")
}

// stateMatches applies an is: state qualifier to a PR state.
func stateMatches(prState, want string) bool {
	s := strings.ToLower(prState)
	if s == "" {
		s = "open"
	}
	switch want {
	case "":
		return true
	case "closed":
		return s == "closed" || s == "merged"
	default:
		return s == want
	}
}

func sortedKeys(m map[int]PR) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func maxKey(m map[int]PR) int {
	hi := 0
	for k := range m {
		hi = max(hi, k)
	}
	return hi
}

func matches(p PR, terms []string) bool {
	hay := strings.ToLower(fmt.Sprintf("%d %s %s %s", p.Number, p.Title, p.HeadRef, p.Author))
	for _, t := range terms {
		if !strings.Contains(hay, strings.TrimPrefix(t, "#")) {
			return false
		}
	}
	return true
}

// PR implements Client.
func (f *Fake) PR(_ context.Context, number int) (PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return PR{}, f.Err
	}
	p, ok := f.PRs[number]
	if !ok {
		return PR{}, fmt.Errorf("gh fake: PR #%d: %w", number, ErrPRNotFound)
	}
	return p, nil
}

// PRDiff implements Client.
func (f *Fake) PRDiff(_ context.Context, number int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return "", f.Err
	}
	d, ok := f.Diffs[number]
	if !ok {
		return "", fmt.Errorf("gh fake: no diff for #%d", number)
	}
	return d, nil
}

// Threads implements Client. It returns deep copies, so callers may mutate
// the result without changing the fake.
func (f *Fake) Threads(_ context.Context, number int) ([]Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	ts := f.ThreadMap[number]
	if ts == nil {
		return nil, nil
	}
	out := make([]Thread, len(ts))
	for i, t := range ts {
		t.Comments = slices.Clone(t.Comments)
		out[i] = t
	}
	return out, nil
}

// repoName is the fake's "owner/name".
func (f *Fake) repoName() string {
	if f.Repo == "" {
		return "fake/fake"
	}
	return f.Repo
}

// SubmitReview implements Client. It validates and records the submission
// (Submitted) and its exact REST body (Payloads, with the PR number in
// PayloadPRs); nothing leaves the process. Review ids are 1, 2, ...; the
// i-th comment of review n gets GHID 1000·n + i.
func (f *Fake) SubmitReview(_ context.Context, number int, r ReviewSubmission) (SubmittedReview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return SubmittedReview{}, f.Err
	}
	req, err := BuildReviewRequest(r)
	if err != nil {
		return SubmittedReview{}, err
	}
	f.Submitted = append(f.Submitted, r)
	f.Payloads = append(f.Payloads, req)
	f.PayloadPRs = append(f.PayloadPRs, number)
	n := int64(len(f.Submitted))
	out := SubmittedReview{ID: n, URL: fmt.Sprintf("https://github.com/%s/pull/%d#pullrequestreview-%d", f.repoName(), number, n)}
	for i, c := range r.Comments {
		c.GHID = 1000*n + int64(i)
		c.ReviewID = n
		c.State = model.CommentPosted
		out.Comments = append(out.Comments, c)
	}
	return out, nil
}

// Reply implements Client: the reply (GHID 900000+n, ID gh-<GHID>) is
// recorded in Replies and, when the thread is known, positioned like the
// thread's comments (ItemID, Path, Line, Side, InReplyTo the first comment)
// and appended to it in ThreadMap. An empty body is an error, as on API.
func (f *Fake) Reply(_ context.Context, threadID, body string) (model.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return model.Comment{}, f.Err
	}
	if strings.TrimSpace(body) == "" {
		return model.Comment{}, fmt.Errorf("gh fake: reply to %s: empty body", threadID)
	}
	f.Replies = append(f.Replies, FakeReply{ThreadID: threadID, Body: body})
	ghID := int64(900000 + len(f.Replies))
	c := model.Comment{
		ID: CommentID(ghID), Body: body, ThreadID: threadID, Author: f.Login,
		Source: model.CommentGitHub, State: model.CommentPosted, GHID: ghID,
	}
	for n, ts := range f.ThreadMap {
		for i, t := range ts {
			if t.ID != threadID {
				continue
			}
			c = normaliseComment(c, t, n)
			if len(t.Comments) > 0 {
				c.InReplyTo = t.Comments[0].GHID
			}
			f.ThreadMap[n][i].Comments = append(f.ThreadMap[n][i].Comments, c)
		}
	}
	return c, nil
}

// FakeReply is one recorded Fake.Reply call.
type FakeReply struct {
	ThreadID string
	Body     string
}

// ResolveThread implements Client: records the id in Resolved and marks
// the thread (and its comments) resolved in ThreadMap.
func (f *Fake) ResolveThread(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	f.Resolved = append(f.Resolved, id)
	for n, ts := range f.ThreadMap {
		for i, t := range ts {
			if t.ID != id {
				continue
			}
			f.ThreadMap[n][i].Resolved = true
			for j := range t.Comments {
				f.ThreadMap[n][i].Comments[j].State = model.CommentResolved
			}
		}
	}
	return nil
}

// CreatePR implements Client. Like API it rejects a NewPR without title,
// head or base (ErrBadPR, nothing recorded). The new PR is numbered after
// the highest known one, authored by Login, with URL
// https://github.com/<Repo>/pull/<n> and HeadSHA from HeadSHAs[head].
func (f *Fake) CreatePR(_ context.Context, p NewPR) (PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return PR{}, f.Err
	}
	if err := validateNewPR(p); err != nil {
		return PR{}, err
	}
	f.Created = append(f.Created, p)
	n := maxKey(f.PRs) + 1
	pr := PR{
		Number: n, Title: p.Title, Body: p.Body, Author: f.Login, HeadRef: p.Head, HeadSHA: f.HeadSHAs[p.Head],
		BaseRef: p.Base, Draft: p.Draft, State: "open", CI: model.CIUnknown,
		URL: fmt.Sprintf("https://github.com/%s/pull/%d", f.repoName(), n),
	}
	if f.PRs == nil {
		f.PRs = map[int]PR{}
	}
	f.PRs[n] = pr
	return pr, nil
}

// CODEOWNERS implements Client.
func (f *Fake) CODEOWNERS(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Owners, f.Err
}
