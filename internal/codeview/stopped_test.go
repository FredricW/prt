package codeview

import (
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// A review row the user stopped (M5.1) reads "stopped", never "failed".
func TestReviewStoppedStatus(t *testing.T) {
	r := &model.Review{GroupID: "g", Tier: model.TierFast, Status: model.ReviewStopped}
	if got := reviewMeta(r); got != "fast · stopped" {
		t.Errorf("reviewMeta = %q, want %q", got, "fast · stopped")
	}
	if got := notReviewedWhy(r); got != "stopped" {
		t.Errorf("notReviewedWhy = %q, want stopped", got)
	}
}
