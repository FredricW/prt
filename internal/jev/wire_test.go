package jev

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serve returns a test server answering every request with status and body.
func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPDecodesVendorExamples(t *testing.T) {
	// Response shapes from docs/vendor/ai-gateway-decision.md.
	body := `{"model":"typesafe-ai/jev","answers":{
		"route":{"type":"choice","choice":"billing","probabilities":{"billing":1,"shipping":0,"technical":0}},
		"quality":{"type":"score","score":2.97,"probabilities":{"0":0,"1":0,"2":0.02,"3":0.98}},
		"passed":{"type":"boolean","probability":0.01}},
		"usage":{"inputTokens":275,"outputTokens":20},
		"providerMetadata":{"gateway":{"routing":{"originalModelId":"typesafe-ai/jev"},"cost":"0.00001155","marketCost":"0.00001155","generationId":"gen_x"}}}`
	srv := serve(t, http.StatusOK, body)
	resp, err := NewHTTP(srv.URL, "k").Evaluate(context.Background(), Request{State: "s", Questions: map[string]Question{
		"route":   {Type: Choice, Instructions: "Route.", Criteria: map[string]string{"billing": "b", "shipping": "s", "technical": "t"}},
		"quality": {Type: Score, Instructions: "Rate.", Criteria: []string{"poor", "fair", "good", "excellent"}},
		"passed":  {Type: Boolean, Instructions: "Passed?"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		check      func(Answer) bool
		confidence float64
	}{
		{"route", func(a Answer) bool { return a.Type == Choice && a.Choice == "billing" && len(a.Probabilities) == 3 }, 1},
		{"quality", func(a Answer) bool {
			return a.Type == Score && a.Score != nil && *a.Score == 2.97 && a.Probabilities["3"] == 0.98
		}, 0.98},
		{"passed", func(a Answer) bool { return a.Type == Boolean && a.Probability != nil && *a.Probability == 0.01 }, 0.99},
	}
	for _, tt := range tests {
		a, ok := resp.Answers[tt.name]
		if !ok || !tt.check(a) {
			t.Errorf("%s = %+v", tt.name, a)
		}
		if got := a.Confidence(); got != tt.confidence {
			t.Errorf("%s confidence = %v, want %v", tt.name, got, tt.confidence)
		}
	}
	if resp.Usage.InputTokens != 275 || resp.ProviderMetadata.Gateway.GenerationID != "gen_x" || resp.CostUSD() != 0.00001155 {
		t.Errorf("meta = %+v", resp)
	}
}

func TestHTTPStatusMapping(t *testing.T) {
	tests := []struct {
		status      int
		unavailable bool
	}{
		{http.StatusUnauthorized, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusBadRequest, false},
		{http.StatusNotFound, false},
	}
	for _, tt := range tests {
		srv := serve(t, tt.status, `{"error":"x"}`)
		_, err := NewHTTP(srv.URL, "k").Evaluate(context.Background(), Request{})
		if err == nil {
			t.Fatalf("%d: want error", tt.status)
		}
		if errors.Is(err, ErrUnavailable) != tt.unavailable {
			t.Errorf("%d: err = %v, unavailable want %v", tt.status, err, tt.unavailable)
		}
	}
}

func TestHTTPNetworkErrorIsUnavailable(t *testing.T) {
	srv := serve(t, http.StatusOK, "{}")
	url := srv.URL
	srv.Close()
	_, err := NewHTTP(url, "k").Evaluate(context.Background(), Request{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPCancelledIsNotUnavailable(t *testing.T) {
	srv := serve(t, http.StatusOK, "{}")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewHTTP(srv.URL, "k").Evaluate(ctx, Request{})
	if errors.Is(err, ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestReplayMissingNamesKeyAndDir(t *testing.T) {
	dir := t.TempDir()
	req := Request{State: "nothing recorded"}
	key, err := RequestKey(req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Replay{Dir: dir}).Evaluate(context.Background(), req)
	if !errors.Is(err, ErrNoRecording) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{key, dir, "make record-jev"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestReplayBadRecording(t *testing.T) {
	dir := t.TempDir()
	req := Request{State: "x"}
	key, _ := RequestKey(req)
	if err := os.WriteFile(filepath.Join(dir, key+".json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (&Replay{Dir: dir}).Evaluate(context.Background(), req)
	if err == nil || errors.Is(err, ErrNoRecording) {
		t.Fatalf("err = %v", err)
	}
}

func TestRecorderWritesNoAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"model":"typesafe-ai/jev","answers":{"q":{"type":"boolean","probability":0.7}}}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	rec := &Recorder{Live: NewHTTP(srv.URL, "sekrit-test-key"), Dir: dir}
	req := Request{State: "s", Questions: map[string]Question{"q": {Type: Boolean, Instructions: "?"}}}
	if _, err := rec.Evaluate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sekrit-test-key" {
		t.Fatalf("server saw auth %q", gotAuth)
	}
	key, _ := RequestKey(req)
	b, err := os.ReadFile(filepath.Join(dir, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"Bearer", "Authorization", "sekrit-test-key"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("recording contains %q:\n%s", bad, b)
		}
	}
}

func TestRecorderDoesNotRecordFailures(t *testing.T) {
	dir := t.TempDir()
	rec := &Recorder{Live: &Fake{Err: ErrUnavailable}, Dir: dir}
	if _, err := rec.Evaluate(context.Background(), Request{State: "x"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("recorded %d files on failure", len(entries))
	}
}
