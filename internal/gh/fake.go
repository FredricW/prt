package gh

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/fredricw/prt/internal/model"
)

// Fake is an in-memory Client for tests. SearchPRs returns every PR whose
// title, head ref or number contains a non-qualifier search term (qualifiers
// such as "is:open" are ignored), or all PRs for a query of only qualifiers.
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
func (f *Fake) Viewer(context.Context) (string, error) { return f.Login, f.Err }

// SearchPRs implements Client.
func (f *Fake) SearchPRs(_ context.Context, query string, limit int) ([]PR, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	var terms []string
	for _, w := range strings.Fields(query) {
		if !strings.Contains(w, ":") {
			terms = append(terms, strings.ToLower(w))
		}
	}
	var out []PR
	for n := 1; n <= maxKey(f.PRs); n++ {
		p, ok := f.PRs[n]
		if !ok || !matches(p, terms) {
			continue
		}
		out = append(out, p)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
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
	return f.ThreadMap[number], f.Err
}

// SubmitReview implements Client.
func (f *Fake) SubmitReview(_ context.Context, _ int, r ReviewSubmission) ([]model.Comment, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
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
	if f.Err != nil {
		return PR{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
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
func (f *Fake) CODEOWNERS(context.Context, string) (string, error) { return f.Owners, f.Err }
