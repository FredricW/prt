package settings

import (
	"math"
	"testing"

	"github.com/fredricw/prt/internal/config"
)

// ValidateChanged checks only the keys that differ from the loaded values:
// values config.Load accepts (concurrency 0, a blank fast spec, deep_slots
// 0) pass when written back unchanged and fail when newly set.
func TestValidateChanged(t *testing.T) {
	old := FromConfig(config.Defaults(), config.DefaultRouting())
	old.Concurrency, old.Fast, old.DeepSlots = 0, "", 0
	if err := Validate(old); err == nil {
		t.Fatal("Validate accepted concurrency 0")
	}
	v := old
	v.AutoReview = "manual"
	if err := ValidateChanged(old, v); err != nil {
		t.Errorf("unchanged loaded values blocked a strategy change: %v", err)
	}
	for name, mut := range map[string]func(){
		"bad strategy":    func() { v.AutoReview = "sometimes" },
		"concurrency 100": func() { v.Concurrency = 100 },
		"bad deep spec":   func() { v.Deep = "nocolon" },
		"NaN weight":      func() { v.Rank.WRequested = math.NaN() },
		"keep_pair_p 2":   func() { v.KeepPairP = 2 },
	} {
		v = old
		mut()
		if err := ValidateChanged(old, v); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
