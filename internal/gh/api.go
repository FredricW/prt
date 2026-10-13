package gh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/google/go-github/v66/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"

	"github.com/fredricw/prt/internal/model"
)

// ErrUnauthorized is returned (wrapped) when GitHub answers 401: the token is
// missing, expired or revoked. It wraps ErrNoToken, so callers that degrade to
// local-only mode on ErrNoToken also degrade on a rejected token.
var ErrUnauthorized = fmt.Errorf("%w: token rejected (401 Unauthorized)", ErrNoToken)

const (
	defaultRESTURL = "https://api.github.com/"
	defaultGQLURL  = "https://api.github.com/graphql"
)

// TokenFromGH runs `gh auth token`. The token is never logged or stored.
func TokenFromGH(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrNoToken, err)
	}
	tok := strings.TrimSpace(out.String())
	if tok == "" {
		return "", ErrNoToken
	}
	return tok, nil
}

var remoteRe = regexp.MustCompile(`github\.com[:/]([^/]+)/([^/]+?)(?:\.git)?/?$`)

// ParseRemote extracts owner/name from a GitHub remote URL (https or ssh).
func ParseRemote(url string) (Repo, bool) {
	m := remoteRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return Repo{}, false
	}
	return Repo{Owner: m[1], Name: m[2]}, true
}

// API implements Client against api.github.com: go-github for REST (diffs,
// contents, reviews) and githubv4 for search and PR metadata.
type API struct {
	Repo Repo
	REST *github.Client
	GQL  *githubv4.Client

	mu     sync.Mutex
	viewer string // cached login after the first successful Viewer call
}

var _ Client = (*API)(nil)

// NewAPI builds an API client with a static token.
func NewAPI(ctx context.Context, repo Repo, token string) *API {
	hc := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	return newAPIWithURLs(repo, hc, defaultRESTURL, defaultGQLURL)
}

// newAPIWithURLs builds an API whose REST and GraphQL endpoints are restURL
// and gqlURL (tests point both at an httptest server). Every response passes
// through authTransport, which turns 401 into ErrUnauthorized.
func newAPIWithURLs(repo Repo, hc *http.Client, restURL, gqlURL string) *API {
	if hc == nil {
		hc = &http.Client{}
	}
	wrapped := &http.Client{
		Transport:     authTransport{base: hc.Transport},
		CheckRedirect: hc.CheckRedirect,
		Jar:           hc.Jar,
		Timeout:       hc.Timeout,
	}
	rest := github.NewClient(wrapped)
	if u, err := url.Parse(withTrailingSlash(restURL)); err == nil {
		rest.BaseURL = u
	}
	return &API{Repo: repo, REST: rest, GQL: githubv4.NewEnterpriseClient(gqlURL, wrapped)}
}

func withTrailingSlash(s string) string {
	if strings.HasSuffix(s, "/") {
		return s
	}
	return s + "/"
}

// authTransport maps HTTP 401 responses to ErrUnauthorized so both the REST
// and GraphQL clients surface it as a typed error (http.Client wraps it in a
// *url.Error, which unwraps).
type authTransport struct{ base http.RoundTripper }

// RoundTrip implements http.RoundTripper.
func (t authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		return nil, ErrUnauthorized
	}
	return resp, nil
}

// Viewer implements Client. The login is cached after the first success.
func (a *API) Viewer(ctx context.Context) (string, error) {
	a.mu.Lock()
	cached := a.viewer
	a.mu.Unlock()
	if cached != "" {
		return cached, nil
	}
	var q struct {
		Viewer struct{ Login string }
	}
	if err := a.GQL.Query(ctx, &q, nil); err != nil {
		return "", fmt.Errorf("gh: viewer: %w", err)
	}
	if q.Viewer.Login == "" {
		return "", errors.New("gh: viewer: empty login")
	}
	a.mu.Lock()
	a.viewer = q.Viewer.Login
	a.mu.Unlock()
	return q.Viewer.Login, nil
}

// SearchPRs implements Client. The query is scoped to the API's repo and to
// PRs (repo: and is:pr are added when missing) and paginated up to limit
// results; limit <= 0 means 100.
func (a *API) SearchPRs(ctx context.Context, query string, limit int) ([]PR, error) {
	if limit <= 0 {
		limit = 100
	}
	viewer, err := a.Viewer(ctx)
	if err != nil {
		return nil, err
	}
	full := scopeQuery(query, a.Repo)
	var out []PR
	var after *githubv4.String
	for len(out) < limit {
		nodes, next, err := a.searchPage(ctx, full, min(limit-len(out), 100), after)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if n.Number > 0 && len(out) < limit {
				out = append(out, prFromNode(n, viewer))
			}
		}
		if next == nil {
			break
		}
		after = next
	}
	return out, nil
}

// searchPage fetches one page of search results; next is nil on the last page.
func (a *API) searchPage(ctx context.Context, query string, first int, after *githubv4.String) ([]prNode, *githubv4.String, error) {
	var q searchQuery
	vars := map[string]any{
		"query": githubv4.String(query),
		"first": githubv4.Int(int32(min(first, 100))),
		"after": after,
	}
	if err := a.GQL.Query(ctx, &q, vars); err != nil {
		return nil, nil, fmt.Errorf("gh: search %q: %w", query, err)
	}
	nodes := make([]prNode, 0, len(q.Search.Nodes))
	for _, n := range q.Search.Nodes {
		nodes = append(nodes, n.PullRequest)
	}
	if !q.Search.PageInfo.HasNextPage {
		return nodes, nil, nil
	}
	cur := q.Search.PageInfo.EndCursor
	return nodes, &cur, nil
}

// scopeQuery adds `repo:owner/name` and `is:pr` to a search query that lacks them.
func scopeQuery(query string, repo Repo) string {
	hasRepo, hasPR := false, false
	for _, w := range strings.Fields(query) {
		lw := strings.ToLower(w)
		hasRepo = hasRepo || strings.HasPrefix(lw, "repo:")
		hasPR = hasPR || lw == "is:pr" || lw == "type:pr"
	}
	parts := []string{}
	if !hasRepo {
		parts = append(parts, "repo:"+repo.String())
	}
	if !hasPR {
		parts = append(parts, "is:pr")
	}
	if q := strings.TrimSpace(query); q != "" {
		parts = append(parts, q)
	}
	return strings.Join(parts, " ")
}

// PR implements Client.
func (a *API) PR(ctx context.Context, number int) (PR, error) {
	viewer, err := a.Viewer(ctx)
	if err != nil {
		return PR{}, err
	}
	var q prQuery
	vars := map[string]any{
		"owner":  githubv4.String(a.Repo.Owner),
		"name":   githubv4.String(a.Repo.Name),
		"number": githubv4.Int(int32(number)), // PR numbers fit in int32
	}
	if err := a.GQL.Query(ctx, &q, vars); err != nil {
		return PR{}, fmt.Errorf("gh: PR #%d: %w", number, err)
	}
	if q.Repository.PullRequest.Number == 0 {
		return PR{}, fmt.Errorf("gh: PR #%d not found in %s", number, a.Repo)
	}
	return prFromNode(q.Repository.PullRequest, viewer), nil
}

// PRDiff implements Client (REST GET /repos/{o}/{r}/pulls/{n} with the v3 diff media type).
func (a *API) PRDiff(ctx context.Context, number int) (string, error) {
	diff, _, err := a.REST.PullRequests.GetRaw(ctx, a.Repo.Owner, a.Repo.Name, number, github.RawOptions{Type: github.Diff})
	if err != nil {
		return "", fmt.Errorf("gh: diff of PR #%d: %w", number, err)
	}
	return diff, nil
}

// codeownersPaths are the locations GitHub reads CODEOWNERS from, in its order.
var codeownersPaths = []string{"CODEOWNERS", ".github/CODEOWNERS", "docs/CODEOWNERS"}

// CODEOWNERS implements Client. It tries CODEOWNERS, .github/CODEOWNERS and
// docs/CODEOWNERS at ref (empty ref means the default branch) and returns
// "" with no error when none exists.
func (a *API) CODEOWNERS(ctx context.Context, ref string) (string, error) {
	for _, p := range codeownersPaths {
		fc, _, resp, err := a.REST.Repositories.GetContents(ctx, a.Repo.Owner, a.Repo.Name, p, &github.RepositoryContentGetOptions{Ref: ref})
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("gh: CODEOWNERS %s@%s: %w", p, ref, err)
		}
		if fc == nil { // a directory listing, not a file
			continue
		}
		s, err := fc.GetContent()
		if err != nil {
			return "", fmt.Errorf("gh: decode CODEOWNERS %s: %w", p, err)
		}
		return s, nil
	}
	return "", nil
}

// Threads implements Client. TODO(M4): GraphQL reviewThreads.
func (a *API) Threads(context.Context, int) ([]Thread, error) { return nil, ErrNotImplemented }

// SubmitReview implements Client. TODO(M4): REST POST /repos/{o}/{r}/pulls/{n}/reviews.
func (a *API) SubmitReview(context.Context, int, ReviewSubmission) ([]model.Comment, error) {
	return nil, ErrNotImplemented
}

// Reply implements Client. TODO(M4): REST reply to a review comment.
func (a *API) Reply(context.Context, int, int64, string) (model.Comment, error) {
	return model.Comment{}, ErrNotImplemented
}

// ResolveThread implements Client. TODO(M4): GraphQL resolveReviewThread.
func (a *API) ResolveThread(context.Context, string) error { return ErrNotImplemented }

// CreatePR implements Client. TODO(M4): REST POST /repos/{o}/{r}/pulls.
func (a *API) CreatePR(context.Context, NewPR) (PR, error) { return PR{}, ErrNotImplemented }
