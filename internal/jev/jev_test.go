package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPEvaluate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/evaluate" || r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var req Request
		if err := json.Unmarshal(b, &req); err != nil || req.Model != Model {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"model":"typesafe-ai/jev","answers":{"refund":{"type":"boolean","probability":0.98}},
			"usage":{"inputTokens":275,"outputTokens":20},"providerMetadata":{"gateway":{"cost":"0.00001155"}}}`))
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL, "test-key")
	resp, err := c.Evaluate(context.Background(), Request{
		State:     "I was charged twice.",
		Questions: map[string]Question{"refund": {Type: Boolean, Instructions: "Money back?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := resp.Answers["refund"]
	if a.Probability == nil || *a.Probability != 0.98 || a.Confidence() != 0.98 {
		t.Fatalf("answer = %+v", a)
	}
	if resp.CostUSD() != 0.00001155 {
		t.Fatalf("cost = %v", resp.CostUSD())
	}
}

func TestHTTPNoKeyIsUnavailable(t *testing.T) {
	_, err := NewHTTP("http://127.0.0.1:0", "").Evaluate(context.Background(), Request{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestRecordReplay(t *testing.T) {
	dir := t.TempDir()
	req := Request{State: "x", Questions: map[string]Question{
		"kind": {Type: Choice, Instructions: "kind?", Criteria: map[string]string{"feature": "", "refactor": ""}},
	}}
	rec := &Recorder{Live: &Fake{}, Dir: dir}
	want, err := rec.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (&Replay{Dir: dir}).Evaluate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Answers["kind"].Choice != want.Answers["kind"].Choice || got.Answers["kind"].Choice != "feature" {
		t.Fatalf("replay mismatch: %+v vs %+v", got, want)
	}
	req.State = "y"
	if _, err := (&Replay{Dir: dir}).Evaluate(context.Background(), req); !errors.Is(err, ErrNoRecording) {
		t.Fatalf("want ErrNoRecording, got %v", err)
	}
}
