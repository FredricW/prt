package settings

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// maxConcurrency bounds [pipeline] concurrency (queue slots).
const maxConcurrency = 64

// validateStrategy checks auto_review: a known strategy, "" included as an
// error (the form always sends one).
func validateStrategy(v proto.SettingsValues) error {
	if strings.TrimSpace(v.AutoReview) == "" {
		return fmt.Errorf("auto_review must be one of manual, needs-you, needs-you+yours or all")
	}
	if _, err := model.ParseAutoReview(v.AutoReview); err != nil {
		return err
	}
	return nil
}

// validateIdentity checks role and the owns globs.
func validateIdentity(v proto.SettingsValues) error {
	if !validText(v.Role) {
		return fmt.Errorf("role must be valid UTF-8 text")
	}
	for _, g := range v.Owns {
		switch {
		case strings.TrimSpace(g) == "":
			return fmt.Errorf("owns must not contain an empty glob")
		case !validText(g) || !doublestar.ValidatePattern(g):
			return fmt.Errorf("owns: %q is not a valid glob", g)
		}
	}
	return nil
}

// validateKnobs checks the pipeline and routing numbers.
func validateKnobs(v proto.SettingsValues) error {
	for _, p := range []struct {
		key string
		val float64
	}{
		{"unsure_below", v.UnsureBelow},
		{"keep_pair_p", v.KeepPairP},
		{"embed_min_cosine", v.EmbedMinCosine},
	} {
		if !(p.val >= 0 && p.val <= 1) { // also rejects NaN
			return fmt.Errorf("%s must be between 0 and 1", p.key)
		}
	}
	switch {
	case v.MaxGroupUnits < 1:
		return fmt.Errorf("max_group_units must be at least 1")
	case v.Concurrency < 1 || v.Concurrency > maxConcurrency:
		return fmt.Errorf("concurrency must be between 1 and %d", maxConcurrency)
	case v.DeepSlots < 1:
		return fmt.Errorf("deep_slots must be at least 1")
	case !finite(v.CostCapUSD) || v.CostCapUSD <= 0:
		return fmt.Errorf("cost_cap_usd must be a number greater than 0")
	}
	return nil
}

// validateRank checks every w_* weight.
func validateRank(v proto.SettingsValues) error {
	for _, w := range rankWeights(v.Rank) {
		if !finite(w.val) || w.val < 0 {
			return fmt.Errorf("%s must be a number of at least 0", w.key)
		}
	}
	return nil
}

// validateModels checks the fast, deep and embed specs.
func validateModels(v proto.SettingsValues) error {
	for _, m := range []struct{ key, spec string }{{"fast", v.Fast}, {"deep", v.Deep}} {
		if !validSpec(m.spec) {
			return fmt.Errorf("%s must be \"provider:model\" (e.g. anthropic:claude-haiku-5-5)", m.key)
		}
	}
	kind, _, _ := strings.Cut(v.Embed, ":")
	if !validSpec(v.Embed) || (kind != "ollama" && kind != "onnx") {
		return fmt.Errorf("embed must be \"ollama:<model>\" or \"onnx:<path>\"")
	}
	return nil
}

// validSpec reports whether s is "<provider>:<model>" with both parts
// non-empty and no surrounding space.
func validSpec(s string) bool {
	p, m, ok := strings.Cut(s, ":")
	return ok && validText(s) && s == strings.TrimSpace(s) &&
		strings.TrimSpace(p) != "" && strings.TrimSpace(m) != ""
}

// weight is one [rank] key and its value.
type weight struct {
	key string
	val float64
}

// rankWeights lists the w_* keys of r in declaration order, from the toml
// tags, so a weight added to config.Rank is saved without touching this
// package.
func rankWeights(r config.Rank) []weight {
	rv := reflect.ValueOf(r)
	rt := rv.Type()
	out := make([]weight, 0, rt.NumField())
	for i := range rt.NumField() {
		f := rt.Field(i)
		key, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if key == "" || key == "-" || f.Type.Kind() != reflect.Float64 {
			continue
		}
		out = append(out, weight{key: key, val: rv.Field(i).Float()})
	}
	return out
}
