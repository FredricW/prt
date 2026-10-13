package gh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

var testRepo = Repo{Owner: "acme", Name: "widgets"}

// gqlRequest is one recorded GraphQL request body.
type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// fakeGitHub is an httptest server serving recorded GraphQL responses from
// testdata and delegating every other path to rest.
type fakeGitHub struct {
	t    *testing.T
	rest http.HandlerFunc

	mu  sync.Mutex
	gql []gqlRequest
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/graphql" {
		if f.rest == nil {
			http.NotFound(w, r)
			return
		}
		f.rest(w, r)
		return
	}
	var req gqlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.gql = append(f.gql, req)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(readTestdata(f.t, recordingFor(req)))
}

// recordingFor picks the testdata file answering a GraphQL request.
func recordingFor(req gqlRequest) string {
	switch {
	case strings.Contains(req.Query, "search("):
		if req.Variables["after"] != nil {
			return "search_page2.json"
		}
		return "search_page1.json"
	case strings.Contains(req.Query, "repository("):
		return "pr.json"
	default:
		return "viewer.json"
	}
}

func (f *fakeGitHub) requests() []gqlRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.gql)
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	return b
}

// newTestAPI starts a fake GitHub and returns an API pointed at it.
func newTestAPI(t *testing.T, rest http.HandlerFunc) (*API, *fakeGitHub) {
	t.Helper()
	fg := &fakeGitHub{t: t, rest: rest}
	srv := httptest.NewServer(fg)
	t.Cleanup(srv.Close)
	return newAPIWithURLs(testRepo, srv.Client(), srv.URL+"/", srv.URL+"/graphql"), fg
}

func TestAPIViewerCached(t *testing.T) {
	a, fg := newTestAPI(t, nil)
	for range 2 {
		got, err := a.Viewer(context.Background())
		if err != nil || got != "fredric" {
			t.Fatalf("Viewer = %q, %v", got, err)
		}
	}
	if n := len(fg.requests()); n != 1 {
		t.Errorf("viewer queried %d times, want 1 (cached)", n)
	}
}

func TestAPISearchPRs(t *testing.T) {
	a, fg := newTestAPI(t, nil)
	prs, err := a.SearchPRs(context.Background(), "involves:@me is:open", 0)
	if err != nil {
		t.Fatal(err)
	}
	var nums []int
	for _, p := range prs {
		nums = append(nums, p.Number)
	}
	if !slices.Equal(nums, []int{1, 2, 3}) {
		t.Fatalf("numbers = %v, want [1 2 3] (empty non-PR node skipped)", nums)
	}

	var searches []gqlRequest
	for _, r := range fg.requests() {
		if strings.Contains(r.Query, "search(") {
			searches = append(searches, r)
		}
	}
	if len(searches) != 2 {
		t.Fatalf("search requests = %d, want 2 pages", len(searches))
	}
	q, _ := searches[0].Variables["query"].(string)
	for _, want := range []string{"repo:acme/widgets", "is:pr", "involves:@me", "is:open"} {
		if !strings.Contains(q, want) {
			t.Errorf("search query %q lacks %q", q, want)
		}
	}
	if searches[0].Variables["first"] != float64(100) {
		t.Errorf("first = %v, want 100", searches[0].Variables["first"])
	}
	if searches[1].Variables["after"] != "Y3Vyc29yOjI=" {
		t.Errorf("page 2 after = %v", searches[1].Variables["after"])
	}
	for _, field := range []string{"viewerLatestReview", "statusCheckRollup", "reviewRequests(first: 20)", "labels(first: 20)", "... on PullRequest"} {
		if !strings.Contains(searches[0].Query, field) {
			t.Errorf("query lacks %q", field)
		}
	}
}

func TestAPISearchPRsLimit(t *testing.T) {
	a, fg := newTestAPI(t, nil)
	prs, err := a.SearchPRs(context.Background(), "repo:acme/widgets is:pr", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].Number != 1 {
		t.Fatalf("got %+v", prs)
	}
	reqs := fg.requests()
	last := reqs[len(reqs)-1]
	if last.Variables["first"] != float64(1) {
		t.Errorf("first = %v, want 1", last.Variables["first"])
	}
	if q := last.Variables["query"]; q != "repo:acme/widgets is:pr" {
		t.Errorf("query rewritten to %q, want unchanged", q)
	}
	if len(reqs) != 2 { // viewer + one search page
		t.Errorf("requests = %d, want 2", len(reqs))
	}
}

func TestAPISearchMapping(t *testing.T) {
	a, _ := newTestAPI(t, nil)
	prs, err := a.SearchPRs(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	byNum := map[int]PR{}
	for _, p := range prs {
		byNum[p.Number] = p
	}
	tests := []struct {
		num             int
		state           string
		draft           bool
		ci              model.CIState
		reviewRequested bool
		approved        bool
		commented       bool
		labels          []string
		reviewers       []string
	}{
		{1, "open", false, model.CIGreen, true, false, false, []string{"feature", "auth"}, []string{"fredric", "bob"}},
		{2, "open", true, model.CIRed, false, true, true, nil, nil},
		{3, "merged", false, model.CIUnknown, false, false, true, []string{"cleanup"}, nil},
	}
	for _, tt := range tests {
		p, ok := byNum[tt.num]
		if !ok {
			t.Errorf("#%d missing", tt.num)
			continue
		}
		if p.State != tt.state || p.Draft != tt.draft || p.CI != tt.ci {
			t.Errorf("#%d state/draft/ci = %q/%v/%q, want %q/%v/%q", tt.num, p.State, p.Draft, p.CI, tt.state, tt.draft, tt.ci)
		}
		if p.ReviewRequested != tt.reviewRequested || p.ViewerApproved != tt.approved || p.ViewerCommented != tt.commented {
			t.Errorf("#%d requested/approved/commented = %v/%v/%v, want %v/%v/%v", tt.num,
				p.ReviewRequested, p.ViewerApproved, p.ViewerCommented, tt.reviewRequested, tt.approved, tt.commented)
		}
		if !slices.Equal(p.Labels, tt.labels) || !slices.Equal(p.Reviewers, tt.reviewers) {
			t.Errorf("#%d labels/reviewers = %v/%v", tt.num, p.Labels, p.Reviewers)
		}
	}
	p1 := byNum[1]
	if p1.Author != "alice" || p1.HeadRef != "feat/login" || p1.HeadSHA != strings.Repeat("1", 40) ||
		p1.BaseRef != "main" || p1.URL != "https://github.com/acme/widgets/pull/1" ||
		p1.Additions != 120 || p1.Deletions != 8 || p1.ChangedFiles != 6 ||
		!p1.UpdatedAt.Equal(time.Date(2026, 10, 12, 15, 30, 0, 0, time.UTC)) {
		t.Errorf("#1 fields = %+v", p1)
	}
}

func TestAPIPR(t *testing.T) {
	a, fg := newTestAPI(t, nil)
	p, err := a.PR(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if p.Number != 7 || p.State != "closed" || p.CI != model.CIPending || !p.ViewerCommented || p.ViewerApproved || p.ReviewRequested {
		t.Errorf("PR = %+v", p)
	}
	reqs := fg.requests()
	vars := reqs[len(reqs)-1].Variables
	if vars["owner"] != "acme" || vars["name"] != "widgets" || vars["number"] != float64(7) {
		t.Errorf("variables = %v", vars)
	}
}

func TestCIFromRollup(t *testing.T) {
	tests := map[string]model.CIState{
		"SUCCESS":  model.CIGreen,
		"FAILURE":  model.CIRed,
		"ERROR":    model.CIRed,
		"PENDING":  model.CIPending,
		"EXPECTED": model.CIPending,
		"":         model.CIUnknown,
		"WEIRD":    model.CIUnknown,
	}
	for in, want := range tests {
		if got := ciFromRollup(in); got != want {
			t.Errorf("ciFromRollup(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScopeQuery(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "repo:acme/widgets is:pr"},
		{"involves:@me is:open", "repo:acme/widgets is:pr involves:@me is:open"},
		{"repo:other/x login", "is:pr repo:other/x login"},
		{"is:pr is:open", "repo:acme/widgets is:pr is:open"},
		{"REPO:a/b IS:PR", "REPO:a/b IS:PR"},
	}
	for _, tt := range tests {
		if got := scopeQuery(tt.in, testRepo); got != tt.want {
			t.Errorf("scopeQuery(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAPIPRDiff(t *testing.T) {
	const diff = "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"
	var accept, path string
	a, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		accept, path = r.Header.Get("Accept"), r.URL.Path
		_, _ = w.Write([]byte(diff))
	})
	got, err := a.PRDiff(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if got != diff {
		t.Errorf("diff = %q", got)
	}
	if path != "/repos/acme/widgets/pulls/5" {
		t.Errorf("path = %q", path)
	}
	if accept != "application/vnd.github.v3.diff" {
		t.Errorf("Accept = %q", accept)
	}
}

func TestAPICODEOWNERS(t *testing.T) {
	const owners = "* @fredric\n"
	tests := []struct {
		name    string
		present string // path that exists, "" for none
		want    string
		tried   []string
	}{
		{"root", "CODEOWNERS", owners, []string{"CODEOWNERS"}},
		{"dot-github", ".github/CODEOWNERS", owners, []string{"CODEOWNERS", ".github/CODEOWNERS"}},
		{"docs", "docs/CODEOWNERS", owners, []string{"CODEOWNERS", ".github/CODEOWNERS", "docs/CODEOWNERS"}},
		{"none", "", "", []string{"CODEOWNERS", ".github/CODEOWNERS", "docs/CODEOWNERS"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var tried []string
			a, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				p := strings.TrimPrefix(r.URL.Path, "/repos/acme/widgets/contents/")
				mu.Lock()
				tried = append(tried, p)
				mu.Unlock()
				if r.URL.Query().Get("ref") != "abc123" {
					http.Error(w, "missing ref", http.StatusBadRequest)
					return
				}
				if p != tt.present {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"message":"Not Found"}`))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{
					"type": "file", "encoding": "base64", "path": p,
					"content": base64.StdEncoding.EncodeToString([]byte(owners)),
				})
			})
			got, err := a.CODEOWNERS(context.Background(), "abc123")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(tried, tt.tried) {
				t.Errorf("tried %v, want %v", tried, tt.tried)
			}
		})
	}
}

func TestAPIUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	t.Cleanup(srv.Close)
	a := newAPIWithURLs(testRepo, srv.Client(), srv.URL, srv.URL+"/graphql")
	ctx := context.Background()
	calls := map[string]func() error{
		"Viewer":     func() error { _, err := a.Viewer(ctx); return err },
		"SearchPRs":  func() error { _, err := a.SearchPRs(ctx, "is:open", 10); return err },
		"PR":         func() error { _, err := a.PR(ctx, 1); return err },
		"PRDiff":     func() error { _, err := a.PRDiff(ctx, 1); return err },
		"CODEOWNERS": func() error { _, err := a.CODEOWNERS(ctx, ""); return err },
	}
	for name, call := range calls {
		err := call()
		if !errors.Is(err, ErrUnauthorized) || !errors.Is(err, ErrNoToken) {
			t.Errorf("%s: err = %v, want ErrUnauthorized (wrapping ErrNoToken)", name, err)
		}
	}
}

func TestAPINetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // nothing listens any more
	a := newAPIWithURLs(testRepo, srv.Client(), srv.URL, srv.URL+"/graphql")
	_, err := a.Viewer(context.Background())
	if err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want a wrapped network error", err)
	}
	if !strings.HasPrefix(err.Error(), "gh: viewer:") {
		t.Errorf("err %q lacks context", err)
	}
}

func TestAPIGraphQLErrorNotCached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"boom"}]}`))
	}))
	t.Cleanup(srv.Close)
	a := newAPIWithURLs(testRepo, srv.Client(), srv.URL, srv.URL+"/graphql")
	if _, err := a.Viewer(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if a.viewer != "" {
		t.Error("failed lookup must not be cached")
	}
}

func TestAPINotImplemented(t *testing.T) {
	a := newAPIWithURLs(testRepo, nil, "http://127.0.0.1:0/", "http://127.0.0.1:0/graphql")
	ctx := context.Background()
	if _, err := a.Threads(ctx, 1); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Threads: %v", err)
	}
	if err := a.ResolveThread(ctx, "x"); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("ResolveThread: %v", err)
	}
}
