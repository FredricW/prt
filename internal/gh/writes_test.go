package gh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

// restRecorder records REST requests (method, path, body) for assertions.
type restRecorder struct {
	mu   sync.Mutex
	reqs []restCall
}

type restCall struct {
	Method, Path, Query string
	Body                []byte
}

func (r *restRecorder) record(req *http.Request) []byte {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, restCall{Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery, Body: b})
	return b
}

func (r *restRecorder) calls() []restCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]restCall(nil), r.reqs...)
}

func TestCommentIDFormat(t *testing.T) {
	// Must equal comments.GitHubID (DECISIONS "M4 shared contracts").
	if got := CommentID(5001); got != "gh-5001" {
		t.Errorf("CommentID(5001) = %q, want gh-5001", got)
	}
}

func TestAPIThreads(t *testing.T) {
	a, fg := newTestAPI(t, nil)
	threads, err := a.Threads(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 3 {
		t.Fatalf("threads = %d, want 3 (two pages)", len(threads))
	}

	reqs := fg.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2 pages", len(reqs))
	}
	for i, wantAfter := range []any{nil, "dGhyZWFkczoy"} {
		q := reqs[i]
		for _, want := range []string{"reviewThreads(first: 100, after: $after)", "pullRequest(number: $number)", "comments(first: 100)", "originalCommit", "replyTo", "diffSide", "isOutdated"} {
			if !strings.Contains(q.Query, want) {
				t.Errorf("page %d query lacks %q: %s", i+1, want, q.Query)
			}
		}
		v := q.Variables
		if v["owner"] != "acme" || v["name"] != "widgets" || v["number"] != float64(42) || v["after"] != wantAfter {
			t.Errorf("page %d variables = %v", i+1, v)
		}
	}

	wantThreads := []struct {
		id                        string
		line, startLine, origLine int
		side                      model.Side
		resolved, outdated        bool
		comments                  int
		commentLine               int
		commentState              model.CommentState
		commentOutdated           bool
	}{
		{"PRRT_a", 12, 0, 12, model.SideNew, false, false, 2, 12, model.CommentPosted, false},
		{"PRRT_b", 0, 0, 7, model.SideOld, false, true, 1, 7, model.CommentPosted, true},
		{"PRRT_c", 5, 3, 5, model.SideNew, true, false, 2, 5, model.CommentResolved, false},
	}
	for i, w := range wantThreads {
		th := threads[i]
		if th.ID != w.id || th.Line != w.line || th.StartLine != w.startLine || th.OriginalLine != w.origLine ||
			th.Side != w.side || th.Resolved != w.resolved || th.Outdated != w.outdated || len(th.Comments) != w.comments {
			t.Errorf("thread %d = %+v", i, th)
			continue
		}
		for _, c := range th.Comments {
			if c.ThreadID != w.id || c.ItemID != "pr:42" || c.Source != model.CommentGitHub || c.Path != th.Path ||
				c.Side != w.side || c.Line != w.commentLine || c.StartLine != w.startLine ||
				c.State != w.commentState || c.Outdated != w.commentOutdated || c.ID != fmt.Sprintf("gh-%d", c.GHID) {
				t.Errorf("thread %s comment = %+v", w.id, c)
			}
		}
	}

	first, reply := threads[0].Comments[0], threads[0].Comments[1]
	if first.GHID != 7001 || first.Author != "fredric" || first.Body != "Why a 1 hour TTL?" || first.InReplyTo != 0 ||
		first.CommitSHA != "aaaa000000000000000000000000000000000001" ||
		!first.CreatedAt.Equal(time.Date(2026, 10, 6, 16, 2, 0, 0, time.UTC)) {
		t.Errorf("first comment = %+v", first)
	}
	if reply.GHID != 7002 || reply.InReplyTo != 7001 || reply.Author != "alice" || reply.CommitSHA != "aaaa000000000000000000000000000000000002" {
		t.Errorf("reply = %+v", reply)
	}
	ghost, noReplyTo := threads[2].Comments[0], threads[2].Comments[1]
	if ghost.Author != "" || ghost.InReplyTo != 0 {
		t.Errorf("ghost comment = %+v", ghost)
	}
	if noReplyTo.InReplyTo != 7004 || noReplyTo.CommitSHA != "" {
		t.Errorf("reply without replyTo = %+v, want InReplyTo the first comment", noReplyTo)
	}
}

// submission is the review the SubmitReview tests post: a single-line
// new-side comment, an old-side comment and a multi-line range.
func submission() ReviewSubmission {
	return ReviewSubmission{
		CommitSHA: "cccc000000000000000000000000000000000001",
		Event:     EventRequestChanges,
		Body:      "Two blockers.",
		Comments: []model.Comment{
			{ID: "d1", ItemID: "pr:9", UnitID: "u1", Path: "web/src/api.ts", Line: 12, Side: model.SideNew, Body: "Use 5 minutes.", Source: model.CommentUser, State: model.CommentPending},
			{ID: "d2", ItemID: "pr:9", UnitID: "u2", Path: "web/src/old.ts", Line: 7, Side: model.SideOld, Body: "Keep the guard.", Source: model.CommentUser, State: model.CommentPending},
			{ID: "d3", ItemID: "pr:9", UnitID: "u3", UnitOffset: 2, Path: "web/src/range.ts", StartLine: 3, Line: 5, Side: model.SideNew, Body: "Extract a helper.", Source: model.CommentUser, State: model.CommentPending},
		},
	}
}

const reviewsPath = "/repos/acme/widgets/pulls/9/reviews"

func TestAPISubmitReview(t *testing.T) {
	golden := readTestdata(t, "submit_review_request.json")
	tests := []struct {
		name         string
		listStatus   int // status of the follow-up comments listing
		wantGHIDs    []int64
		wantRequests int
	}{
		// 9003 and 9002 match by path+line+body; d1's body was normalised by
		// GitHub, so it takes the remaining listed comment in order.
		{"ids mapped", http.StatusOK, []int64{9001, 9002, 9003}, 3},
		{"listing fails", http.StatusInternalServerError, []int64{0, 0, 0}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec restRecorder
			a, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				rec.record(r)
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && r.URL.Path == reviewsPath:
					_, _ = w.Write([]byte(`{"id":555,"html_url":"https://github.com/acme/widgets/pull/9#pullrequestreview-555","state":"CHANGES_REQUESTED"}`))
				case r.Method == http.MethodGet && r.URL.Path == reviewsPath+"/555/comments":
					if tt.listStatus != http.StatusOK {
						w.WriteHeader(tt.listStatus)
						_, _ = w.Write([]byte(`{"message":"boom"}`))
						return
					}
					if r.URL.Query().Get("page") == "2" {
						_, _ = w.Write([]byte(`[{"id":9001,"path":"web/src/api.ts","line":12,"body":"Use 5 minutes.\r\n"}]`))
						return
					}
					w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=2>; rel="next"`, r.Host, r.URL.Path))
					_, _ = w.Write([]byte(`[{"id":9003,"path":"web/src/range.ts","line":5,"body":"Extract a helper."},{"id":9002,"path":"web/src/old.ts","line":7,"body":"Keep the guard."}]`))
				default:
					http.NotFound(w, r)
				}
			})
			in := submission()
			got, err := a.SubmitReview(context.Background(), 9, in)
			if err != nil {
				t.Fatal(err)
			}
			calls := rec.calls()
			if len(calls) != tt.wantRequests {
				t.Fatalf("requests = %d, want %d: %+v", len(calls), tt.wantRequests, calls)
			}
			if calls[0].Method != http.MethodPost || calls[0].Path != reviewsPath {
				t.Errorf("first request = %s %s", calls[0].Method, calls[0].Path)
			}
			if string(calls[0].Body) != string(golden) {
				t.Errorf("body =\n%s\nwant\n%s", calls[0].Body, golden)
			}
			if calls[1].Method != http.MethodGet || calls[1].Path != reviewsPath+"/555/comments" {
				t.Errorf("follow-up = %s %s", calls[1].Method, calls[1].Path)
			}
			if got.ID != 555 || got.URL != "https://github.com/acme/widgets/pull/9#pullrequestreview-555" || len(got.Comments) != 3 {
				t.Fatalf("review = %+v", got)
			}
			for i, c := range got.Comments {
				want := in.Comments[i]
				want.State, want.ReviewID, want.GHID = model.CommentPosted, 555, tt.wantGHIDs[i]
				if c != want {
					t.Errorf("comment %d = %+v\nwant %+v", i, c, want)
				}
			}
		})
	}
}

func TestAPISubmitReviewRejected(t *testing.T) {
	long := strings.Repeat("é", 400) // 800 bytes, cut on a rune boundary
	a, _ := newTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = fmt.Fprintf(w, `{"message":"Unprocessable Entity","errors":["Line could not be resolved %s"]}`, long)
	})
	_, err := a.SubmitReview(context.Background(), 9, submission())
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "Unprocessable Entity: Line could not be resolved") {
		t.Errorf("err %q lacks GitHub's message", msg)
	}
	_, detail, _ := strings.Cut(msg, ErrRejected.Error()+": ")
	if len(detail) > rejectMsgMax || !strings.HasSuffix(detail, "é") {
		t.Errorf("detail is %d bytes (max %d) or splits a rune: %q", len(detail), rejectMsgMax, detail[max(0, len(detail)-10):])
	}
}

func TestAPISubmitReviewInvalid(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*ReviewSubmission)
	}{
		{"empty comment body", func(r *ReviewSubmission) { r.Comments[1].Body = "  " }},
		{"unknown event", func(r *ReviewSubmission) { r.Event = "MERGE" }},
		{"comment without line", func(r *ReviewSubmission) { r.Comments[0].Line = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec restRecorder
			a, fg := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) { rec.record(r); http.NotFound(w, r) })
			in := submission()
			tt.mod(&in)
			if _, err := a.SubmitReview(context.Background(), 9, in); !errors.Is(err, ErrBadReview) {
				t.Fatalf("err = %v, want ErrBadReview", err)
			}
			if n := len(rec.calls()) + len(fg.requests()); n != 0 {
				t.Errorf("%d requests sent for an invalid review", n)
			}
		})
	}
}

func TestAPISubmitReviewNoComments(t *testing.T) {
	var rec restRecorder
	a, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		_, _ = w.Write([]byte(`{"id":7,"html_url":"u"}`))
	})
	got, err := a.SubmitReview(context.Background(), 9, ReviewSubmission{CommitSHA: "c", Event: EventApprove})
	if err != nil || got.ID != 7 || len(got.Comments) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
	calls := rec.calls()
	if len(calls) != 1 || string(calls[0].Body) != `{"commit_id":"c","event":"APPROVE"}`+"\n" {
		t.Errorf("calls = %+v (an approval lists no comments)", calls)
	}
}

func TestAPIReply(t *testing.T) {
	a, fg := newTestAPI(t, nil)
	c, err := a.Reply(context.Background(), "PRRT_a", "Done in the latest push.")
	if err != nil {
		t.Fatal(err)
	}
	reqs := fg.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	q := reqs[0]
	for _, want := range []string{"mutation", "$input:AddPullRequestReviewThreadReplyInput!", "addPullRequestReviewThreadReply(input: $input)", "databaseId", "originalCommit", "replyTo"} {
		if !strings.Contains(q.Query, want) {
			t.Errorf("mutation lacks %q: %s", want, q.Query)
		}
	}
	input, _ := q.Variables["input"].(map[string]any)
	if input["pullRequestReviewThreadId"] != "PRRT_a" || input["body"] != "Done in the latest push." || len(input) != 2 {
		t.Errorf("input = %v", q.Variables)
	}
	want := model.Comment{
		ID: "gh-7010", GHID: 7010, ThreadID: "PRRT_a", Path: "web/src/api.ts", Line: 12,
		Body: "Done in the latest push.", Author: "fredric", Source: model.CommentGitHub, State: model.CommentPosted,
		InReplyTo: 7001, CommitSHA: "aaaa000000000000000000000000000000000003",
		CreatedAt: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC),
	}
	if c != want {
		t.Errorf("reply = %+v\nwant %+v", c, want)
	}
}

func TestAPIReplyEmptyBody(t *testing.T) {
	a, fg := newTestAPI(t, nil)
	if _, err := a.Reply(context.Background(), "PRRT_a", " \n"); err == nil {
		t.Fatal("want error for an empty reply")
	}
	if n := len(fg.requests()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
}

func TestAPIResolveThread(t *testing.T) {
	a, fg := newTestAPI(t, nil)
	if err := a.ResolveThread(context.Background(), "PRRT_a"); err != nil {
		t.Fatal(err)
	}
	reqs := fg.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	q := reqs[0]
	for _, want := range []string{"mutation", "$input:ResolveReviewThreadInput!", "resolveReviewThread(input: $input)", "isResolved"} {
		if !strings.Contains(q.Query, want) {
			t.Errorf("mutation lacks %q: %s", want, q.Query)
		}
	}
	if input, _ := q.Variables["input"].(map[string]any); input["threadId"] != "PRRT_a" || len(input) != 1 {
		t.Errorf("input = %v", q.Variables)
	}
}

func TestAPIResolveThreadStillOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"resolveReviewThread":{"thread":{"id":"PRRT_a","isResolved":false}}}}`))
	}))
	t.Cleanup(srv.Close)
	a := newAPIWithURLs(testRepo, srv.Client(), srv.URL, srv.URL+"/graphql")
	if err := a.ResolveThread(context.Background(), "PRRT_a"); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Errorf("err = %v, want still-unresolved error", err)
	}
}

const createdPRJSON = `{
  "number": 12, "title": "Add cache", "body": "", "state": "open", "draft": true, "merged": false,
  "html_url": "https://github.com/acme/widgets/pull/12",
  "user": {"login": "fredric"},
  "head": {"ref": "feat/cache", "sha": "1212121212121212121212121212121212121212", "repo": {"full_name": "acme/widgets"}},
  "base": {"ref": "main", "sha": "abababababababababababababababababababab", "repo": {"full_name": "acme/widgets"}},
  "additions": 10, "deletions": 2, "changed_files": 3,
  "created_at": "2026-10-13T10:00:00Z", "updated_at": "2026-10-13T10:00:01Z"
}`

func TestAPICreatePR(t *testing.T) {
	var rec restRecorder
	a, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if r.Method != http.MethodPost || r.URL.Path != "/repos/acme/widgets/pulls" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(createdPRJSON))
	})
	p, err := a.CreatePR(context.Background(), NewPR{Title: "Add cache", Head: "feat/cache", Base: "main", Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	calls := rec.calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	wantBody := `{"title":"Add cache","head":"feat/cache","base":"main","body":"","draft":true}` + "\n"
	if string(calls[0].Body) != wantBody {
		t.Errorf("body = %s, want %s", calls[0].Body, wantBody)
	}
	want := PR{
		Number: 12, Title: "Add cache", Author: "fredric", State: "open", Draft: true,
		HeadRef: "feat/cache", HeadSHA: strings.Repeat("12", 20), BaseRef: "main", BaseSHA: strings.Repeat("ab", 20),
		URL: "https://github.com/acme/widgets/pull/12", Additions: 10, Deletions: 2, ChangedFiles: 3,
		CI:        model.CIUnknown,
		CreatedAt: time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 13, 10, 0, 1, 0, time.UTC),
	}
	if p.Number != want.Number || p.Title != want.Title || p.Author != want.Author || p.State != want.State ||
		p.Draft != want.Draft || p.HeadRef != want.HeadRef || p.HeadSHA != want.HeadSHA || p.BaseRef != want.BaseRef ||
		p.BaseSHA != want.BaseSHA || p.URL != want.URL || p.Additions != want.Additions || p.Deletions != want.Deletions ||
		p.ChangedFiles != want.ChangedFiles || p.CI != want.CI || p.CrossRepo ||
		!p.CreatedAt.Equal(want.CreatedAt) || !p.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("PR = %+v\nwant %+v", p, want)
	}
}

func TestAPICreatePRErrors(t *testing.T) {
	tests := []struct {
		name    string
		in      NewPR
		wantErr error
		sent    int
	}{
		{"no title", NewPR{Head: "h", Base: "main"}, ErrBadPR, 0},
		{"no head", NewPR{Title: "t", Base: "main"}, ErrBadPR, 0},
		{"no base", NewPR{Title: "t", Head: "h", Base: " "}, ErrBadPR, 0},
		{"exists", NewPR{Title: "t", Head: "h", Base: "main"}, ErrRejected, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec restRecorder
			a, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				rec.record(r)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for acme:h."}]}`))
			})
			_, err := a.CreatePR(context.Background(), tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if errors.Is(tt.wantErr, ErrRejected) && !strings.Contains(err.Error(), "A pull request already exists") {
				t.Errorf("err %q lacks GitHub's message", err)
			}
			if n := len(rec.calls()); n != tt.sent {
				t.Errorf("requests = %d, want %d", n, tt.sent)
			}
		})
	}
}

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 5, "abc"},
		{"abcdef", 3, "abc"},
		{"aé", 2, "a"},
		{"aéb", 3, "aé"},
	}
	for _, tt := range tests {
		if got := truncateUTF8(tt.in, tt.n); got != tt.want {
			t.Errorf("truncateUTF8(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}
