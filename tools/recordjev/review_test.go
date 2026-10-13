package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
)

// reviewKey is a dummy Anthropic key that must never appear in output.
const reviewKey = "sk-ant-recordjev-test-NEVER-PRINT"

const haiku = "anthropic:claude-haiku-5-5"

// costReviewer answers every request with a fixed cost (and counts calls).
type costReviewer struct {
	cost  float64
	in    int
	calls atomic.Int32
	block chan struct{} // when set, Review waits for it
}

func (c *costReviewer) Model() string { return haiku }

func (c *costReviewer) Review(_ context.Context, req review.Request) (review.Result, error) {
	c.calls.Add(1)
	if c.block != nil {
		<-c.block
	}
	return review.Result{Text: review.FakeTLDR, Model: haiku, Cost: c.cost, InputTokens: c.in}, nil
}

func tldrReq(s string) review.Request {
	return review.Request{Task: review.TaskTLDR, Tier: model.TierFast, System: "s", Prompt: s, MaxTokens: 100}
}

func TestReviewGuardRefusesOverReviewCap(t *testing.T) {
	next := &costReviewer{cost: 0.001}
	req := tldrReq("a")
	est := review.EstimateCost(haiku, req)
	b := &reviewBudget{capUSD: 3.00, totalCap: 5, prior: 3.00 - est/2}
	g := &reviewGuard{next: next, dir: t.TempDir(), budget: b}
	_, err := g.Review(context.Background(), req)
	if !errors.Is(err, ErrReviewBudget) || errors.Is(err, review.ErrUnavailable) || !strings.Contains(err.Error(), "review cap") {
		t.Fatalf("err = %v, want ErrReviewBudget over the review cap", err)
	}
	if next.calls.Load() != 0 {
		t.Fatal("reviewer called")
	}
	if _, refused := b.totals(); refused != 1 {
		t.Fatalf("refused %d", refused)
	}
}

func TestReviewGuardRefusesOverTotalCap(t *testing.T) {
	next := &costReviewer{cost: 0.001}
	b := &reviewBudget{capUSD: 3.00, totalCap: 5, prior: 0, priorTotal: 4.99999}
	g := &reviewGuard{next: next, dir: t.TempDir(), budget: b}
	_, err := g.Review(context.Background(), tldrReq("a"))
	if !errors.Is(err, ErrReviewBudget) || !strings.Contains(err.Error(), "total cap $5.00") {
		t.Fatalf("err = %v, want ErrReviewBudget over the $5 total", err)
	}
	if next.calls.Load() != 0 {
		t.Fatal("reviewer called")
	}
}

func TestReviewGuardReservesInFlight(t *testing.T) {
	next := &costReviewer{cost: 0.0001, block: make(chan struct{})}
	req1, req2 := tldrReq("one"), tldrReq("two")
	est := review.EstimateCost(haiku, req1)
	// Room for one estimate, not two.
	b := &reviewBudget{capUSD: est * 1.5, totalCap: 5}
	g := &reviewGuard{next: next, dir: t.TempDir(), budget: b}
	var wg sync.WaitGroup
	var firstErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, firstErr = g.Review(context.Background(), req1)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for next.calls.Load() == 0 {
		if time.Now().After(deadline) {
			close(next.block)
			wg.Wait()
			t.Fatalf("first call never reached the reviewer: %v", firstErr)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := g.Review(context.Background(), req2); !errors.Is(err, ErrReviewBudget) || !strings.Contains(err.Error(), "in flight") {
		t.Fatalf("second call while first in flight: %v", err)
	}
	close(next.block)
	wg.Wait()
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	spent, refused := b.totals()
	if spent != 0.0001 || refused != 1 || b.reserved > 1e-12 || b.reserved < -1e-12 {
		t.Fatalf("spent %v refused %d reserved %v", spent, refused, b.reserved)
	}
	// The reservation is released: the second request fits now.
	if _, err := g.Review(context.Background(), req2); err != nil {
		t.Fatalf("after settle: %v", err)
	}
	st := g.stats()
	if st.calls != 2 || st.written != 2 || st.reused != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestReviewGuardServesRecordings(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	rec, synth := tldrReq("recorded"), tldrReq("synthetic")
	if _, err := (&review.Recorder{Live: &costReviewer{cost: 0.5}, Dir: dir}).Review(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := (&review.Recorder{Live: &costReviewer{}, Dir: dir, Synthetic: true}).Review(ctx, synth); err != nil {
		t.Fatal(err)
	}
	next := &costReviewer{cost: 0.01}
	b := &reviewBudget{capUSD: 0, totalCap: 5} // no call fits
	g := &reviewGuard{next: next, dir: dir, budget: b}
	res, err := g.Review(ctx, rec)
	if err != nil || res.Text != review.FakeTLDR {
		t.Fatalf("recorded request: %+v %v", res, err)
	}
	if next.calls.Load() != 0 {
		t.Fatal("recorded request was sent")
	}
	if _, err := g.Review(ctx, synth); !errors.Is(err, ErrReviewBudget) {
		t.Fatalf("synthetic recording must be re-recorded (here refused): %v", err)
	}
	if st := g.stats(); st.reused != 1 || st.calls != 0 {
		t.Fatalf("stats %+v", st)
	}
	if spent, _ := b.totals(); spent != 0 {
		t.Fatalf("spent %v", spent)
	}
}

func TestReviewGuardSettlesFailedSpendAndWarnsOnEstimate(t *testing.T) {
	b := &reviewBudget{capUSD: 3, totalCap: 5}
	failing := &review.Fake{Name: haiku, Respond: func(review.Request) (review.Result, error) {
		return review.Result{Cost: 0.02, InputTokens: 1 << 20}, review.ErrBadResponse
	}}
	g := &reviewGuard{next: failing, dir: t.TempDir(), budget: b}
	if _, err := g.Review(context.Background(), tldrReq("x")); !errors.Is(err, review.ErrBadResponse) {
		t.Fatal(err)
	}
	spent, _ := b.totals()
	st := g.stats()
	if spent != 0.02 || st.calls != 1 || st.written != 0 || len(st.underEstimates) != 1 {
		t.Fatalf("spent %v stats %+v", spent, st)
	}
	if len(st.failures) != 1 || !strings.Contains(st.failures[0], "tldr") {
		t.Fatalf("failures %q", st.failures)
	}
}

// A rejected request (e.g. HTTP 400) costs nothing and writes nothing, but
// must still be reported so the live run exits non-zero.
func TestReviewGuardCountsFreeFailures(t *testing.T) {
	b := &reviewBudget{capUSD: 3, totalCap: 5}
	failing := &review.Fake{Name: haiku, Respond: func(review.Request) (review.Result, error) {
		return review.Result{}, errors.New("HTTP 400 invalid_request_error")
	}}
	g := &reviewGuard{next: failing, dir: t.TempDir(), budget: b}
	for i := range maxFailuresShown + 2 {
		_, _ = g.Review(context.Background(), tldrReq(strings.Repeat("x", i+1)))
	}
	st := g.stats()
	if st.calls != 0 || len(st.failures) != maxFailuresShown || st.moreFailures != 2 {
		t.Fatalf("stats %+v", st)
	}
}

func TestReviewGuardReady(t *testing.T) {
	g := &reviewGuard{next: &review.Recorder{Live: review.NewAnthropic("claude-haiku-5-5", ""), Dir: t.TempDir()}}
	if err := review.Ready(g); !errors.Is(err, review.ErrNoKey) {
		t.Fatalf("Ready = %v", err)
	}
}

func TestLedgerTotalAndByService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SPEND.md")
	if got, err := ledgerTotal(path); err != nil || got != 0 {
		t.Fatalf("missing ledger total = %v, %v", got, err)
	}
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, r := range []ledgerRow{
		{At: at, Service: "jev", Calls: 1, Cost: 0.25},
		{At: at, Service: "ollama", Calls: 1, Cost: 0},
		{At: at, Service: "anthropic", Calls: 3, Cost: 1.5},
		{At: at, Service: "anthropic", Calls: 1, Cost: 0.5},
	} {
		if err := appendLedger(path, r); err != nil {
			t.Fatal(err)
		}
	}
	total, err := ledgerTotal(path)
	if err != nil || total < 2.2499 || total > 2.2501 {
		t.Fatalf("total %v, %v", total, err)
	}
	if a, err := ledgerSpent(path, "anthropic"); err != nil || a != 2 {
		t.Fatalf("anthropic %v, %v", a, err)
	}
	if j, err := ledgerSpent(path, "jev"); err != nil || j != 0.25 {
		t.Fatalf("jev %v, %v", j, err)
	}
}

// reviewArgs are the flags of a review-mode run writing into dir (Jev and
// embeddings are replayed from the committed recordings).
func reviewArgs(dir string, extra ...string) []string {
	return append([]string{"--review", "--embed", "replay", "--review-out", filepath.Join(dir, "review"), "--spend", filepath.Join(dir, "SPEND.md")}, extra...)
}

func TestReviewDryRun(t *testing.T) {
	dir := t.TempDir()
	getenv := func(k string) string { return map[string]string{review.AnthropicKeyEnv: reviewKey}[k] }
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), reviewArgs(dir), getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"review dry run (no LLM calls):", "upper-bound cost $", "already recorded in", "make record-review"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out+stderr.String(), reviewKey) {
		t.Fatal("key printed")
	}
	if _, err := os.Stat(filepath.Join(dir, "SPEND.md")); err == nil {
		t.Error("dry run wrote a ledger row")
	}
	if recs, _ := filepath.Glob(filepath.Join(dir, "review", "*.json")); len(recs) != 0 {
		t.Errorf("dry run wrote %d recordings", len(recs))
	}
}

func TestReviewCounterCounts(t *testing.T) {
	dir := t.TempDir()
	recorded := tldrReq("recorded")
	if _, err := (&review.Recorder{Live: &costReviewer{}, Dir: dir}).Review(context.Background(), recorded); err != nil {
		t.Fatal(err)
	}
	c := &reviewCounter{next: &review.Fake{Name: haiku, Respond: review.FakeRespond(0.9)}, dir: dir}
	for _, r := range []review.Request{recorded, tldrReq("new"), {Task: review.TaskGroupTitle, Prompt: "t", MaxTokens: 20}} {
		if _, err := c.Review(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	s := c.snapshot()
	want := review.EstimateCost(haiku, tldrReq("new")) + review.EstimateCost(haiku, review.Request{Task: review.TaskGroupTitle, Prompt: "t", MaxTokens: 20})
	if s.requests != 3 || s.recorded != 1 || s.toRecordCost != want || s.taskSummary() != "group-title 1, tldr 2" || s.tokens <= 0 {
		t.Fatalf("counts %+v (%s)", s, s.taskSummary())
	}
}

func TestReviewLiveWithoutKeyExits(t *testing.T) {
	var stdout, stderr bytes.Buffer
	getenv := func(k string) string { return map[string]string{"JEV_API_KEY": "jev-key"}[k] }
	code := run(context.Background(), reviewArgs(t.TempDir(), "--live"), getenv, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), review.AnthropicKeyEnv) || strings.Contains(stderr.String(), "jev-key") {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
}

func TestReviewExclusiveWithSynthetic(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--review", "--synthetic"}, func(string) string { return "" }, &stdout, &stderr); code == 0 {
		t.Fatal("want failure")
	}
}

// fakeMessages is a local Anthropic Messages endpoint answering each task
// with a valid response (task told apart by the system prompt).
func fakeMessages(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System    string `json:"system"`
			MaxTokens int    `json:"max_tokens"`
		}
		if r.Header.Get("x-api-key") != reviewKey || json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`)
			return
		}
		served.Add(1)
		text := review.FakeReviewJSON(nil, review.VerdictApprove, 0.9)
		switch {
		case strings.Contains(body.System, "three words"):
			text = review.FakeTitle
		case strings.Contains(body.System, "TL;DR"):
			text = review.FakeTLDR
		}
		_, _ = fmt.Fprintf(w, `{"content":[{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":10}}`, text)
	}))
	t.Cleanup(srv.Close)
	return srv, &served
}

// TestReviewLiveAgainstLocalServer runs live review mode against a local
// Messages endpoint: no real network, Jev never called, an anthropic
// ledger row appended, the key nowhere in output or recordings.
func TestReviewLiveAgainstLocalServer(t *testing.T) {
	srv, served := fakeMessages(t)
	jevHits := atomic.Int32{}
	jevSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { jevHits.Add(1) }))
	defer jevSrv.Close()
	dir := t.TempDir()
	getenv := func(k string) string {
		return map[string]string{review.AnthropicKeyEnv: reviewKey, anthropicBaseURLEnv: srv.URL, "JEV_BASE_URL": jevSrv.URL, "JEV_API_KEY": "jev"}[k]
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), reviewArgs(dir, "--live"), getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if jevHits.Load() != 0 {
		t.Fatalf("Jev called %d times in review mode", jevHits.Load())
	}
	out := stdout.String() + stderr.String()
	if !strings.Contains(out, "review live:") || strings.Contains(out, reviewKey) {
		t.Fatalf("output:\n%s", out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "SPEND.md"))
	if err != nil || !strings.Contains(string(b), "| anthropic | ") {
		t.Fatalf("ledger: %q, %v", b, err)
	}
	recs, _ := filepath.Glob(filepath.Join(dir, "review", "*.json"))
	if len(recs) != int(served.Load()) {
		t.Errorf("%d recordings for %d served calls", len(recs), served.Load())
	}
	for _, p := range recs {
		rb, _ := os.ReadFile(p)
		if strings.Contains(string(rb), reviewKey) {
			t.Fatalf("recording %s contains the key", p)
		}
	}
	// A second run reuses every recording.
	before := served.Load()
	stdout.Reset()
	if code := run(context.Background(), reviewArgs(dir, "--live"), getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("second run code %d, stderr %s", code, stderr.String())
	}
	if served.Load() != before {
		t.Errorf("second run sent %d calls", served.Load()-before)
	}
}
