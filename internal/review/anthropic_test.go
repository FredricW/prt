package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

// wireRequest is what the fake Messages endpoint decodes.
type wireRequest struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature *float64  `json:"temperature"`
	System      string    `json:"system"`
	Messages    []message `json:"messages"`
	Stream      *bool     `json:"stream"`
}

// messagesReply renders a Messages API response body.
func messagesReply(text string, in, out int, stop string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_test", "type": "message", "role": "assistant", "model": "claude-test",
		"content":     []map[string]string{{"type": "text", "text": text}},
		"stop_reason": stop,
		"usage":       map[string]int{"input_tokens": in, "output_tokens": out},
	})
	return string(b)
}

// newTestAnthropic returns an adapter for model name pointed at h.
func newTestAnthropic(t *testing.T, name string, h http.HandlerFunc) *Anthropic {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a := NewAnthropic(name, testKey)
	a.BaseURL = srv.URL
	return a
}

// assertNoKey fails when s contains the test key.
func assertNoKey(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, testKey) {
		t.Fatalf("key leaked: %q", s)
	}
}

func TestAnthropicRequestShape(t *testing.T) {
	var got wireRequest
	var hdr http.Header
	var path, method string
	a := newTestAnthropic(t, "claude-haiku-5-5", func(w http.ResponseWriter, r *http.Request) {
		path, method, hdr = r.URL.Path, r.Method, r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprint(w, messagesReply("Short sentence.", 1200, 30, "end_turn"))
	})
	req := Request{Task: TaskTLDR, Tier: model.TierFast, System: "sys", Prompt: "prompt", Context: []string{"### callers: x\nbody"}, MaxTokens: 100}
	res, err := a.Review(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/v1/messages" {
		t.Errorf("%s %s", method, path)
	}
	if hdr.Get("x-api-key") != testKey || hdr.Get("anthropic-version") != "2023-06-01" || hdr.Get("content-type") != "application/json" {
		t.Errorf("headers: version %q type %q key ok %v", hdr.Get("anthropic-version"), hdr.Get("content-type"), hdr.Get("x-api-key") == testKey)
	}
	if hdr.Get("Authorization") != "" {
		t.Error("Authorization header sent")
	}
	if got.Model != "claude-haiku-5-5" || got.MaxTokens != 100 || got.Temperature == nil || *got.Temperature != 0 || got.System != "sys" || got.Stream != nil {
		t.Errorf("body: %+v", got)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" || got.Messages[0].Content != UserContent(req) || !strings.Contains(got.Messages[0].Content, "### callers: x\nbody") {
		t.Errorf("messages: %+v", got.Messages)
	}
	if res.Text != "Short sentence." || res.InputTokens != 1200 || res.OutputTokens != 30 || res.StopReason != "end_turn" || res.Model != "anthropic:claude-haiku-5-5" {
		t.Errorf("result: %+v", res)
	}
	if res.Cost != Cost("anthropic:claude-haiku-5-5", 1200, 30) || res.Cost <= 0 {
		t.Errorf("cost %v", res.Cost)
	}
}

func TestAnthropicDefaultMaxTokensAndConcatenatedText(t *testing.T) {
	var got wireRequest
	a := newTestAnthropic(t, "claude-opus-5-5", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = fmt.Fprint(w, `{"content":[{"type":"text","text":"Part one, "},{"type":"thinking","text":"hidden"},{"type":"text","text":"part two."}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":4}}`)
	})
	res, err := a.Review(context.Background(), Request{Task: TaskGroupTitle, Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxTokens != DefaultMaxTokens || got.System != "" {
		t.Errorf("max_tokens %d system %q", got.MaxTokens, got.System)
	}
	if res.Text != "Part one, part two." {
		t.Errorf("text %q", res.Text)
	}
}

func TestAnthropicStatusMapping(t *testing.T) {
	cases := []struct {
		status      int
		typ         string
		unavailable bool
	}{
		{401, "authentication_error", true},
		{403, "permission_error", true},
		{429, "rate_limit_error", true},
		{500, "api_error", true},
		{502, "", true},
		{503, "", true},
		{504, "", true},
		{529, "overloaded_error", true},
		{400, "invalid_request_error", false},
		{404, "not_found_error", false},
		{413, "request_too_large", false},
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.status), func(t *testing.T) {
			a := newTestAnthropic(t, "claude-haiku-5-5", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				// The message echoes the key, as a misbehaving proxy might.
				_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":"bad x-api-key %s"}}`, c.typ, r.Header.Get("x-api-key"))
			})
			_, err := a.Review(context.Background(), Request{Prompt: "p"})
			if err == nil {
				t.Fatal("want error")
			}
			if errors.Is(err, ErrUnavailable) != c.unavailable {
				t.Errorf("ErrUnavailable = %v, want %v (%v)", !c.unavailable, c.unavailable, err)
			}
			if !strings.Contains(err.Error(), fmt.Sprint(c.status)) || (c.typ != "" && !strings.Contains(err.Error(), c.typ)) {
				t.Errorf("error lacks status or type: %v", err)
			}
			assertNoKey(t, err.Error())
		})
	}
}

func TestAnthropicTimeout(t *testing.T) {
	a := newTestAnthropic(t, "claude-haiku-5-5", func(w http.ResponseWriter, r *http.Request) {
		// Drain the body so the server notices the client hanging up.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	a.TimeoutFast, a.TimeoutDeep = 30*time.Millisecond, time.Hour
	if a.Timeout(model.TierFast) != 30*time.Millisecond || NewAnthropic("m", "").Timeout(model.TierDeep) != DeepTimeout || NewAnthropic("m", "").Timeout(model.TierFast) != FastTimeout {
		t.Fatal("timeout selection")
	}
	start := time.Now()
	_, err := a.Review(context.Background(), Request{Tier: model.TierFast, Prompt: "p"})
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "no response within") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("timeout not applied")
	}
}

func TestAnthropicContextCancel(t *testing.T) {
	started := make(chan struct{})
	a := newTestAnthropic(t, "claude-haiku-5-5", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	_, err := a.Review(ctx, Request{Prompt: "p"})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestAnthropicNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	a := NewAnthropic("claude-haiku-5-5", testKey)
	a.BaseURL = url
	_, err := a.Review(context.Background(), Request{Prompt: "p"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	assertNoKey(t, err.Error())
}

func TestAnthropicReviewParses(t *testing.T) {
	text := "```json\n" + FakeReviewJSON([]model.Finding{{Path: "web/src/api/client.ts", Line: 11, Severity: model.SevHigh, Claim: "Auth is skipped."}}, VerdictRequestChanges, 0.7) + "\n```"
	a := newTestAnthropic(t, "claude-opus-5-5", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, messagesReply(text, 3000, 200, "end_turn"))
	})
	req := BuildReview(sampleInput(model.TierDeep))
	res, err := a.Review(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictRequestChanges || res.Confidence != 0.7 || len(res.Findings) != 1 || res.Findings[0].UnitID != "u-client" || res.Text != text {
		t.Fatalf("result: %+v", res)
	}
}

func TestAnthropicBadReviewKeepsCost(t *testing.T) {
	a := newTestAnthropic(t, "claude-opus-5-5", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, messagesReply(`{"findings": [ {"file": "a.go"`, 3000, 2048, "max_tokens"))
	})
	res, err := a.Review(context.Background(), BuildReview(sampleInput(model.TierDeep)))
	if !errors.Is(err, ErrBadResponse) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrBadResponse", err)
	}
	if !strings.Contains(err.Error(), "max_tokens") {
		t.Errorf("error lacks the stop reason: %v", err)
	}
	if res.InputTokens != 3000 || res.OutputTokens != 2048 || res.Cost != Cost("anthropic:claude-opus-5-5", 3000, 2048) {
		t.Errorf("tokens/cost lost: %+v", res)
	}
}

func TestAnthropicGarbageBody(t *testing.T) {
	a := newTestAnthropic(t, "claude-haiku-5-5", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<html>proxy</html>")
	})
	if _, err := a.Review(context.Background(), Request{Prompt: "p"}); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnthropicNoKeySendsNothing(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	a := NewAnthropic("claude-haiku-5-5", "")
	a.BaseURL = srv.URL
	if err := Ready(a); !errors.Is(err, ErrNoKey) || !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), AnthropicKeyEnv) {
		t.Fatalf("Ready = %v", err)
	}
	if _, err := a.Review(context.Background(), Request{Prompt: "p"}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Review = %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent without a key", hits.Load())
	}
	if err := Ready(NewAnthropic("m", testKey)); err != nil {
		t.Fatalf("Ready with key = %v", err)
	}
}

// pessimisticTokens simulates a tokenizer that is denser than any Claude
// tokenizer on prompt-like text (2.6 bytes per token plus framing), so
// the fake endpoint's usage is a realistic upper case for real usage.
func pessimisticTokens(r wireRequest) int {
	n := len(r.System)
	for _, m := range r.Messages {
		n += len(m.Content)
	}
	return int(float64(n)/2.6) + 20
}

// TestEstimateIsUpperBound sends the real builders' requests to a fake
// endpoint whose usage follows pessimisticTokens and whose output uses
// the full MaxTokens: the pre-call estimate must cover the actual input
// tokens and the actual cost.
func TestEstimateIsUpperBound(t *testing.T) {
	agg := sampleAgg()
	reqs := map[string]Request{
		"review fast": BuildReview(sampleInput(model.TierFast)),
		"review deep": BuildReview(sampleInput(model.TierDeep)),
		"tldr":        BuildTLDR(agg),
		"title":       BuildGroupTitle(model.Group{ID: "g"}, sampleUnits()),
		"cjk":         {Prompt: strings.Repeat("变更了用户接口。", 200), MaxTokens: 50},
		"empty":       {},
	}
	for name, req := range reqs {
		for _, m := range []string{"claude-haiku-5-5", "claude-opus-5-5"} {
			a := newTestAnthropic(t, m, func(w http.ResponseWriter, r *http.Request) {
				var wr wireRequest
				_ = json.NewDecoder(r.Body).Decode(&wr)
				_, _ = fmt.Fprint(w, messagesReply("ok", pessimisticTokens(wr), wr.MaxTokens, "max_tokens"))
			})
			res, _ := a.Review(context.Background(), req)
			if res.InputTokens == 0 {
				t.Fatalf("%s/%s: no usage", name, m)
			}
			if est := EstimateInputTokens(req); est < res.InputTokens {
				t.Errorf("%s/%s: estimate %d < actual %d input tokens", name, m, est, res.InputTokens)
			}
			if est := EstimateCost(a.Model(), req); est < res.Cost {
				t.Errorf("%s/%s: estimated cost %v < actual %v", name, m, est, res.Cost)
			}
		}
	}
}
