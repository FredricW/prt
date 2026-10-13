package review

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestIsTruncated(t *testing.T) {
	for name, tc := range map[string]struct {
		res  Result
		err  error
		want bool
	}{
		"no error":                       {Result{StopReason: StopMaxTokens}, nil, false},
		"ErrTruncated":                   {Result{}, fmt.Errorf("x: %w", ErrTruncated), true},
		"parse failure at max_tokens":    {Result{StopReason: StopMaxTokens}, fmt.Errorf("parse: %w", ErrBadResponse), true},
		"parse failure at end_turn":      {Result{StopReason: "end_turn"}, fmt.Errorf("parse: %w", ErrBadResponse), false},
		"unavailable at max_tokens":      {Result{StopReason: StopMaxTokens}, ErrUnavailable, false},
		"ErrTruncated is a bad response": {Result{}, ErrTruncated, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := IsTruncated(tc.res, tc.err); got != tc.want {
				t.Errorf("IsTruncated = %v, want %v", got, tc.want)
			}
		})
	}
	if !errors.Is(ErrTruncated, ErrBadResponse) {
		t.Error("ErrTruncated does not wrap ErrBadResponse")
	}
}

func TestGrow(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 2 * DefaultMaxTokens}, {1024, 2048}, {ReviewMaxTokensFast, 8192}, {5000, MaxTokensCeiling}} {
		got, ok := Grow(Request{Prompt: "p", MaxTokens: tc.in})
		if !ok || got.MaxTokens != tc.want || got.Prompt != "p" {
			t.Errorf("Grow(%d) = %d %v, want %d", tc.in, got.MaxTokens, ok, tc.want)
		}
	}
	if got, ok := Grow(Request{MaxTokens: MaxTokensCeiling}); ok || got.MaxTokens != MaxTokensCeiling {
		t.Errorf("Grow at the ceiling = %d %v", got.MaxTokens, ok)
	}
	a, _ := RequestKey("m", Request{Prompt: "p", MaxTokens: 1024})
	grown, _ := Grow(Request{Prompt: "p", MaxTokens: 1024})
	if b, _ := RequestKey("m", grown); a == b {
		t.Error("a grown request shares the recording key")
	}
}

// TestAnthropicTruncation: a review cut off at max_tokens wraps ErrTruncated
// (and ErrBadResponse); prose cut off with no text does too; prose that
// stopped at max_tokens with text is fine.
func TestAnthropicTruncation(t *testing.T) {
	for name, tc := range map[string]struct {
		req       Request
		text      string
		stop      string
		truncated bool
		ok        bool
	}{
		"review cut off":       {BuildReview(sampleInput(model.TierFast)), `{"findings": [ {"file": "a.go"`, StopMaxTokens, true, false},
		"review bad JSON":      {BuildReview(sampleInput(model.TierFast)), `no json`, "end_turn", false, false},
		"prose without text":   {BuildTLDR(sampleAgg()), ``, StopMaxTokens, true, false},
		"prose with text":      {BuildTLDR(sampleAgg()), `A sentence that was long enough`, StopMaxTokens, false, true},
		"prose empty end_turn": {BuildTLDR(sampleAgg()), ``, "end_turn", false, true},
	} {
		t.Run(name, func(t *testing.T) {
			a := newTestAnthropic(t, "claude-haiku-5-5", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, messagesReply(tc.text, 900, 1024, tc.stop))
			})
			res, err := a.Review(context.Background(), tc.req)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok %v", err, tc.ok)
			}
			if got := errors.Is(err, ErrTruncated); got != tc.truncated {
				t.Errorf("ErrTruncated = %v (%v), want %v", got, err, tc.truncated)
			}
			if IsTruncated(res, err) != tc.truncated || res.OutputTokens != 1024 || res.Cost == 0 {
				t.Errorf("result %+v", res)
			}
		})
	}
}

// TestRecordTruncatedThenRetry records a fake that cuts off the first
// request and answers the grown one, then replays both from the directory:
// the truncated recording comes back with ErrTruncated and its tokens, the
// grown request with the review.
func TestRecordTruncatedThenRetry(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	live := &Fake{Name: "anthropic:claude-haiku-5-5", Respond: FakeTruncate(2*ReviewMaxTokensFast, FakeRespond(0.9))}
	rec := &Recorder{Live: live, Dir: dir}
	req := BuildReview(sampleInput(model.TierFast))
	if res, err := rec.Review(ctx, req); !errors.Is(err, ErrTruncated) || res.Text != truncatedText {
		t.Fatalf("first call = %+v, %v; want a truncation", res, err)
	}
	grown, ok := Grow(req)
	if !ok {
		t.Fatal("cannot grow")
	}
	if _, err := rec.Review(ctx, grown); err != nil {
		t.Fatalf("grown call: %v", err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 2 {
		t.Fatalf("%d recordings, want the truncated one and the retry", len(files))
	}
	rc, ok, err := LoadRecording(dir, live.Model(), req)
	if err != nil || !ok || !rc.Truncated || rc.Result.StopReason != StopMaxTokens {
		t.Fatalf("truncated recording %+v %v %v", rc, ok, err)
	}
	r := &Replay{Name: live.Model(), Dir: dir}
	res, err := r.Review(ctx, req)
	if !errors.Is(err, ErrTruncated) || !IsTruncated(res, err) || res.OutputTokens != ReviewMaxTokensFast || res.Cost == 0 {
		t.Errorf("replayed truncation = %+v, %v", res, err)
	}
	res, err = r.Review(ctx, grown)
	if err != nil || res.Verdict != VerdictComment || len(res.Findings) != 1 {
		t.Errorf("replayed retry = %+v, %v", res, err)
	}
	if n := len(live.Calls); n != 2 || live.Calls[1].MaxTokens != 2*ReviewMaxTokensFast {
		t.Errorf("live calls %d, second MaxTokens %d", n, live.Calls[len(live.Calls)-1].MaxTokens)
	}
}

// TestFakeTruncateProse: prose below the threshold comes back empty and
// truncated; at the threshold it is answered.
func TestFakeTruncateProse(t *testing.T) {
	f := &Fake{Respond: FakeTruncate(TLDRMaxTokens+1, FakeRespond(0.9))}
	req := BuildTLDR(sampleAgg())
	res, err := f.Review(context.Background(), req)
	if !IsTruncated(res, err) || res.Text != "" || res.OutputTokens != TLDRMaxTokens {
		t.Errorf("truncated prose = %+v, %v", res, err)
	}
	grown, _ := Grow(req)
	if res, err := f.Review(context.Background(), grown); err != nil || res.Text != FakeTLDR {
		t.Errorf("grown prose = %+v, %v", res, err)
	}
}
