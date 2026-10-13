package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
)

func req(state string) jev.Request {
	return jev.Request{Model: jev.Model, State: state, Questions: map[string]jev.Question{"q": {Type: jev.Boolean, Instructions: "?"}}}
}

// costClient answers every request with a fixed gateway cost.
type costClient struct {
	mu    sync.Mutex
	cost  string
	calls int
}

func (c *costClient) Evaluate(_ context.Context, r jev.Request) (jev.Response, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	resp := jev.Response{Model: jev.Model, Answers: map[string]jev.Answer{}}
	for n, q := range r.Questions {
		resp.Answers[n] = jev.DefaultAnswer(n, q, r.State)
	}
	resp.ProviderMetadata.Gateway.Cost = c.cost
	return resp, nil
}

func TestGuardRefusesAtCapBeforeCalling(t *testing.T) {
	next := &costClient{cost: "0.001"}
	g := &guard{next: next, dir: t.TempDir(), capUSD: 1.00, prior: 0.995}
	_, err := g.Evaluate(context.Background(), req("a"))
	if !errors.Is(err, ErrBudget) || errors.Is(err, jev.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrBudget (not ErrUnavailable)", err)
	}
	if next.calls != 0 {
		t.Fatalf("next called %d times", next.calls)
	}
}

func TestGuardReserveIsLargestCall(t *testing.T) {
	next := &costClient{cost: "0.30"}
	g := &guard{next: next, dir: t.TempDir(), capUSD: 1.00}
	ctx := context.Background()
	for i, s := range []string{"a", "b", "c"} {
		if _, err := g.Evaluate(ctx, req(s)); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	// 0.90 spent + 0.30 reserve > 1.00.
	if _, err := g.Evaluate(ctx, req("d")); !errors.Is(err, ErrBudget) {
		t.Fatalf("4th call err = %v, want ErrBudget", err)
	}
	calls, reused, spent := g.totals()
	if calls != 3 || reused != 0 || next.calls != 3 || spent < 0.899 || spent > 0.901 {
		t.Fatalf("calls %d reused %d next %d spent %v", calls, reused, next.calls, spent)
	}
}

func TestGuardServesRecordingsWithoutSpend(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	// Record "a" as real and "b" as synthetic.
	if _, err := (&jev.Recorder{Live: &costClient{cost: "0.5"}, Dir: dir}).Evaluate(ctx, req("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := (&jev.Recorder{Live: &jev.Fake{}, Dir: dir, Synthetic: true}).Evaluate(ctx, req("b")); err != nil {
		t.Fatal(err)
	}
	next := &costClient{cost: "0.01"}
	g := &guard{next: next, dir: dir, capUSD: 0.005} // cap too low for any call
	if _, err := g.Evaluate(ctx, req("a")); err != nil {
		t.Fatalf("recorded request: %v", err)
	}
	if next.calls != 0 {
		t.Fatal("recorded request was sent")
	}
	// Live mode does not accept a synthetic recording: it must be re-sent
	// (here refused by the cap).
	if _, err := g.Evaluate(ctx, req("b")); !errors.Is(err, ErrBudget) {
		t.Fatalf("synthetic recording in live mode: err = %v, want ErrBudget", err)
	}
	g.acceptSynthetic = true
	if _, err := g.Evaluate(ctx, req("b")); err != nil {
		t.Fatalf("synthetic recording in synthetic mode: %v", err)
	}
	if calls, reused, spent := g.totals(); calls != 0 || reused != 2 || spent != 0 {
		t.Fatalf("calls %d reused %d spent %v", calls, reused, spent)
	}
}

func TestLedgerParseAndAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SPEND.md")
	if got, err := ledgerSpent(path, "jev"); err != nil || got != 0 {
		t.Fatalf("missing ledger = %v, %v", got, err)
	}
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	if err := appendLedger(path, ledgerRow{At: at, Service: "jev", Calls: 10, Cost: 0.12, Cumulative: 0.12, Written: 10}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedger(path, ledgerRow{At: at, Service: "review", Calls: 1, Cost: 2, Cumulative: 2, Written: 1}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedger(path, ledgerRow{At: at, Service: "jev", Calls: 2, Cost: 0.03, Cumulative: 0.15, Written: 2}); err != nil {
		t.Fatal(err)
	}
	got, err := ledgerSpent(path, "jev")
	if err != nil || got < 0.1499 || got > 0.1501 {
		t.Fatalf("jev spend = %v, %v", got, err)
	}
	b, _ := os.ReadFile(path)
	text := string(b)
	if strings.Count(text, "# Real model spend") != 1 || !strings.Contains(text, "| 2026-10-13T12:00:00Z | jev | 10 | $0.1200 | $0.1200 | 10 |") {
		t.Fatalf("ledger:\n%s", text)
	}
	if _, err := parseLedger("| x | jev | 1 | $abc | $1 | 1 |\n", "jev"); err == nil {
		t.Fatal("bad cost should be an error")
	}
}

func TestLiveWithoutKeyExits(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--live", "--out", t.TempDir()}, func(string) string { return "" }, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "JEV_API_KEY") {
		t.Fatalf("code %d, stderr %q", code, errOut.String())
	}
}

func TestExclusiveFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--live", "--synthetic"}, func(string) string { return "k" }, &out, &errOut); code == 0 {
		t.Fatal("want failure")
	}
}

func TestDryRunOffline(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--out", t.TempDir()}, func(string) string { return "" }, &out, &errOut)
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Jev requests") || !strings.Contains(out.String(), "dry run") {
		t.Fatalf("stdout %q", out.String())
	}
}

// TestLiveAgainstLocalServer runs live mode against an httptest server (no
// real network): every request is recorded and the spend lands in the
// ledger. The key is a dummy.
func TestLiveAgainstLocalServer(t *testing.T) {
	var mu sync.Mutex
	served := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rq jev.Request
		if err := json.NewDecoder(r.Body).Decode(&rq); err != nil || r.Header.Get("Authorization") != "Bearer dummy" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		mu.Lock()
		served++
		mu.Unlock()
		resp := jev.Response{Model: jev.Model, Answers: map[string]jev.Answer{}}
		for n, q := range rq.Questions {
			resp.Answers[n] = jev.DefaultAnswer(n, q, rq.State)
		}
		resp.ProviderMetadata.Gateway.Cost = "0.0001"
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	dir := t.TempDir()
	out, spend := filepath.Join(dir, "jev"), filepath.Join(dir, "SPEND.md")
	getenv := func(k string) string {
		return map[string]string{"JEV_API_KEY": "dummy", "JEV_BASE_URL": srv.URL}[k]
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--live", "--out", out, "--spend", spend, "--cap", "5"}, getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	recs, _ := filepath.Glob(filepath.Join(out, "*.json"))
	if len(recs) != served {
		t.Errorf("%d recordings for %d served requests", len(recs), served)
	}
	for _, p := range recs {
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "dummy") {
			t.Fatalf("recording %s contains the key", p)
		}
	}
	b, err := os.ReadFile(spend)
	if err != nil || !strings.Contains(string(b), "| jev | ") {
		t.Fatalf("ledger: %q, %v", b, err)
	}
	if strings.Contains(stdout.String(), "dummy") || strings.Contains(stderr.String(), "dummy") {
		t.Fatal("key printed")
	}

	// A second run sends nothing: everything is recorded.
	before := served
	stdout.Reset()
	if code := run(context.Background(), []string{"--live", "--out", out, "--spend", spend, "--cap", "5"}, getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("second run code %d, stderr %s", code, stderr.String())
	}
	if served != before {
		t.Errorf("second run sent %d requests", served-before)
	}
}

// TestSyntheticSatisfiesExpectations: heuristic recordings over the real
// pipeline satisfy the replay test's assertions, and a second synthetic run
// reuses every recording.
func TestSyntheticSatisfiesExpectations(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--synthetic", "--out", out}, func(string) string { return "" }, &stdout, &stderr); code != 0 {
		t.Fatalf("code %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "replay expectations: all hold") {
		t.Fatalf("stdout:\n%s", stdout.String())
	}
	recs, _ := filepath.Glob(filepath.Join(out, "*.json"))
	stdout.Reset()
	if code := run(context.Background(), []string{"--synthetic", "--out", out}, func(string) string { return "" }, &stdout, &stderr); code != 0 {
		t.Fatalf("second run code %d", code)
	}
	if !strings.Contains(stdout.String(), "synthetic: 0 recordings written") || len(recs) == 0 {
		t.Fatalf("second run: %s (%d recordings)", stdout.String(), len(recs))
	}
}

func TestHeuristicAnswers(t *testing.T) {
	kindQ := jev.Question{Type: jev.Choice, Criteria: map[string]string{"feature": "", "refactor": "", "dependency": "", "test": ""}}
	breakQ := jev.Question{Type: jev.Boolean}
	riskQ := jev.Question{Type: jev.Score, Criteria: []string{"1", "2", "3", "4", "5"}}
	cases := []struct {
		name  string
		state any
		kind  string
		brk   bool
		risk  string
	}{
		{"dep", map[string]any{"path": "server/go.mod", "diff": "-\tgithub.com/x v1\n+\tgithub.com/x v2"}, model.KindDependency, false, "0"},
		{"breaking", map[string]any{"path": "web/src/api/client.ts", "diff": "-export async function getUser(id: string) {\n+export async function getUser(id: string, opts: GetUserOptions) {"}, "feature", true, "1"},
		{"deleted", map[string]any{"path": "server/internal/billing/legacy.go", "diff": "-func LegacyExport() {}\n-func parseLegacyDate() {}"}, "refactor", false, "0"},
		{"rename", "path: web/src/api/users.ts\n-import { fetchJSON } from \"./client\";\n+import { requestJSON } from \"./client\";", "refactor", false, "0"},
	}
	for _, c := range cases {
		if got := heuristicAnswer("kind", kindQ, c.state); got.Choice != c.kind {
			t.Errorf("%s: kind = %q, want %q", c.name, got.Choice, c.kind)
		}
		if got := heuristicAnswer(model.QBreaking, breakQ, c.state); (*got.Probability >= 0.5) != c.brk {
			t.Errorf("%s: breaking p = %v", c.name, *got.Probability)
		}
		got := heuristicAnswer(model.QRisk, riskQ, c.state)
		best, bestP := "", 0.0
		for k, p := range got.Probabilities {
			if p > bestP {
				best, bestP = k, p
			}
		}
		if best != c.risk || got.Score == nil {
			t.Errorf("%s: risk rung = %q (%v), want %q", c.name, best, got.Probabilities, c.risk)
		}
	}
	bullets := jev.Question{Type: jev.Choice, Criteria: map[string]string{
		"b1": "`getUser` takes a required `GetUserOptions` argument", "b2": "Invalidate the profile cache on logout", "none": "none",
	}}
	st := map[string]any{"path": "web/src/api/client.ts", "diff": "+export async function getUser(id: string, opts: GetUserOptions) {"}
	if got := heuristicAnswer(model.QDescBullet, bullets, st); got.Choice != "b1" {
		t.Errorf("desc_bullet = %q, want b1", got.Choice)
	}
}
