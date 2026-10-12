package review

import (
	"context"
	"errors"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestFromSpecAndTiers(t *testing.T) {
	r, err := FromSpec("anthropic:claude-haiku-5-5", "")
	if err != nil || r.Model() != "anthropic:claude-haiku-5-5" {
		t.Fatalf("%v %v", r, err)
	}
	if _, err := r.Review(context.Background(), Request{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing key must be ErrUnavailable, got %v", err)
	}
	if _, err := FromSpec("nope", ""); err == nil {
		t.Fatal("want error")
	}
	tiers := Tiers{Fast: &Fake{Name: "f"}, Deep: &Fake{Name: "d"}}
	if tiers.For(model.TierDeep).Model() != "d" || tiers.For(model.TierNone) != nil {
		t.Fatal("tier lookup")
	}
}
