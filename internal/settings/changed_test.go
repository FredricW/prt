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

// ApplyChanged takes from v only the keys that differ from old; every other
// key keeps base's value.
func TestApplyChanged(t *testing.T) {
	base := FromConfig(config.Defaults(), config.DefaultRouting())
	base.Role, base.Owns, base.Rank.WAge, base.CostCapUSD = "backend", []string{"api/**"}, 0.5, 2
	old := base
	old.Role, old.Owns, old.Rank.WAge, old.Rank.WRisk, old.CostCapUSD = "frontend", []string{"web/**"}, 0.25, 7, 9
	v := old
	v.Concurrency, v.Rank.WRisk, v.Fast = base.Concurrency+3, 8, "anthropic:x"
	got := ApplyChanged(base, old, v)
	want := base
	want.Concurrency, want.Rank.WRisk, want.Fast = v.Concurrency, 8, "anthropic:x"
	if err := compare(got, want); err != nil || got.CostCapUSD != want.CostCapUSD || got.DeepSlots != want.DeepSlots {
		t.Errorf("ApplyChanged = %+v, want %+v (%v)", got, want, err)
	}
	v.Owns = []string{"docs/**"}
	if got := ApplyChanged(base, old, v); len(got.Owns) != 1 || got.Owns[0] != "docs/**" || base.Owns[0] != "api/**" {
		t.Errorf("owns = %v (base %v)", got.Owns, base.Owns)
	}
}
