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

	Submitted []ReviewSubmission
	Created   []NewPR
	Resolved  []string
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
		return PR{}, fmt.Errorf("gh fake: PR #%d not found", number)
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

// Threads implements Client.
func (f *Fake) Threads(_ context.Context, number int) ([]Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ThreadMap[number], f.Err
}

// SubmitReview implements Client.
func (f *Fake) SubmitReview(_ context.Context, _ int, r ReviewSubmission) ([]model.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	f.Submitted = append(f.Submitted, r)
	out := make([]model.Comment, len(r.Comments))
	for i, c := range r.Comments {
		c.GHID = int64(1000*len(f.Submitted) + i)
		c.State = model.CommentPosted
		out[i] = c
	}
	return out, nil
}

// Reply implements Client.
func (f *Fake) Reply(_ context.Context, _ int, inReplyTo int64, body string) (model.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return model.Comment{Body: body, InReplyTo: inReplyTo, Source: model.CommentUser, State: model.CommentPosted}, f.Err
}

// ResolveThread implements Client.
func (f *Fake) ResolveThread(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Resolved = append(f.Resolved, id)
	return f.Err
}

// CreatePR implements Client.
func (f *Fake) CreatePR(_ context.Context, p NewPR) (PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return PR{}, f.Err
	}
	f.Created = append(f.Created, p)
	n := maxKey(f.PRs) + 1
	pr := PR{Number: n, Title: p.Title, Body: p.Body, HeadRef: p.Head, BaseRef: p.Base, Draft: p.Draft, State: "open"}
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
