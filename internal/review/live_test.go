//go:build live

package review

import (
	"context"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// TestLiveGroupTitle makes one real fast-tier call (a three-word title,
// about $0.001 at the assumed prices). Run with:
//
//	PRT_ANTHROPIC_API_KEY=… go test -tags live -run TestLiveGroupTitle ./internal/review
func TestLiveGroupTitle(t *testing.T) {
	key := AnthropicKeyFromEnv()
	if key == "" {
		t.Skip(AnthropicKeyEnv + " not set")
	}
	a := NewAnthropic("claude-haiku-5-5", key)
	res, err := a.Review(context.Background(), BuildGroupTitle(model.Group{ID: "live"}, sampleUnits()))
	if err != nil {
		t.Fatal(err)
	}
	title := CleanTitle(res.Text)
	if title == "" || res.InputTokens == 0 || res.Cost <= 0 {
		t.Fatalf("result %+v, title %q", res, title)
	}
	if est := EstimateInputTokens(BuildGroupTitle(model.Group{ID: "live"}, sampleUnits())); est < res.InputTokens {
		t.Errorf("estimate %d below actual %d input tokens", est, res.InputTokens)
	}
	t.Logf("title %q, %d in / %d out tokens, $%.5f", title, res.InputTokens, res.OutputTokens, res.Cost)
}
