package review

import (
	"math"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestPriceForAndCost(t *testing.T) {
	if p := PriceFor("anthropic:claude-haiku-5-5"); p.InPerMTok != 1 || p.OutPerMTok != 5 {
		t.Errorf("haiku price %+v", p)
	}
	if PriceFor("claude-opus-5-5") != PriceFor("anthropic:claude-opus-5-5") {
		t.Error("prefix changes the price")
	}
	if p := PriceFor("anthropic:claude-unknown-9"); p != UnknownPrice || p.InPerMTok < PriceFor("anthropic:claude-opus-5-5").InPerMTok {
		t.Errorf("unknown model price %+v, want the deep price", p)
	}
	if got := Cost("anthropic:claude-opus-5-5", 1_000_000, 100_000); math.Abs(got-22.5) > 1e-9 {
		t.Errorf("cost = %v", got)
	}
}

func TestEstimateMatchesUserContent(t *testing.T) {
	req := BuildReview(sampleInput(model.TierFast))
	want := (len(req.System)+len(UserContent(req))+1)/2 + 64
	if got := EstimateInputTokens(req); got != want {
		t.Errorf("EstimateInputTokens = %d, want %d", got, want)
	}
	noCtx := req
	noCtx.Context = nil
	if EstimateInputTokens(noCtx) >= EstimateInputTokens(req) {
		t.Error("context blocks are not counted")
	}
}

func TestEstimateCostUsesMaxTokens(t *testing.T) {
	req := Request{Prompt: strings.Repeat("x", 1000)}
	if EstimateCost("anthropic:claude-haiku-5-5", req) != Cost("anthropic:claude-haiku-5-5", EstimateInputTokens(req), DefaultMaxTokens) {
		t.Error("default output cap not used")
	}
	req.MaxTokens = 20
	if EstimateCost("anthropic:claude-haiku-5-5", req) != Cost("anthropic:claude-haiku-5-5", EstimateInputTokens(req), 20) {
		t.Error("MaxTokens not used")
	}
}
