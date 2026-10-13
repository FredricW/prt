// Package config loads prt's three optional TOML files:
//
//	prt.toml          identity, weights, pipeline knobs, models
//	prt.routing.toml  routing rules (first match wins)
//	prt.questions.toml the Jev question catalogue (parsed by package questions)
//
// Each file is looked up independently in .git/prt/, then
// $XDG_CONFIG_HOME/prt/ (default ~/.config/prt/), then falls back to built-in
// defaults. Secrets never come from these files.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// File names.
const (
	MainFile      = "prt.toml"
	RoutingFile   = "prt.routing.toml"
	QuestionsFile = "prt.questions.toml"
)

// User is the reviewer's identity.
type User struct {
	Role string   `toml:"role"`
	Owns []string `toml:"owns"`
}

// GitHub settings.
type GitHub struct {
	Repo            string   `toml:"repo"` // "owner/name"; default from origin remote
	ReleaseBranches []string `toml:"release_branches"`
}

// Rank holds urgency weights (spec: Ranking and aggregation).
type Rank struct {
	WRequested  float64 `toml:"w_requested"`
	WBreaking   float64 `toml:"w_breaking"`
	WRisk       float64 `toml:"w_risk"`
	WTestGap    float64 `toml:"w_test_gap"`
	WRelevance  float64 `toml:"w_relevance"`
	WRelease    float64 `toml:"w_release"`
	WCIRed      float64 `toml:"w_ci_red"`
	WAge        float64 `toml:"w_age"`
	WMinutes    float64 `toml:"w_minutes"`
	WUnpushed   float64 `toml:"w_unpushed"` // branches only
	WReadyStale float64 `toml:"w_ready_stale"`
}

// Pipeline knobs.
type Pipeline struct {
	MaxGroupUnits  int     `toml:"max_group_units"`
	EmbedMinCosine float64 `toml:"embed_min_cosine"`
	EmbedTopK      int     `toml:"embed_top_k"`
	KeepPairP      float64 `toml:"keep_pair_p"`
	UnsureBelow    float64 `toml:"unsure_below"`
	Concurrency    int     `toml:"concurrency"`
	LineBatch      int     `toml:"line_batch"`
	MechanicalMin  int     `toml:"mechanical_min"`
	RefreshSeconds int     `toml:"refresh_seconds"`
	// MaxPairsPerUnit caps the relation pairs judged per item at this many
	// times its unit count, highest cosine first (≤ 0: default 3). Not in
	// the spec: a cost bound for embedders whose cosines are compressed
	// (see DECISIONS "Pair requests are capped per item").
	MaxPairsPerUnit int `toml:"max_pairs_per_unit"`
}

// DefaultMaxPairsPerUnit is the default max_pairs_per_unit.
const DefaultMaxPairsPerUnit = 3

// Models names the external models. Endpoints and keys come from env:
// JEV_BASE_URL / JEV_API_KEY, OLLAMA_HOST, PRT_ANTHROPIC_API_KEY.
type Models struct {
	Embed      string `toml:"embed"`        // "ollama:<model>" or "onnx:<path>"
	JevBaseURL string `toml:"jev_base_url"` // overrides default; JEV_BASE_URL env wins
	Fast       string `toml:"fast"`
	Deep       string `toml:"deep"`
	// JevEndpoint is the spec's original key. It is accepted so the spec's
	// sample prt.toml loads, but ignored (Jev is reached through the AI
	// Gateway at JevBaseURL); Load adds a warning when it is set.
	JevEndpoint string `toml:"jev_endpoint"`
}

// Config is prt.toml.
type Config struct {
	User     User              `toml:"user"`
	GitHub   GitHub            `toml:"github"`
	Rank     Rank              `toml:"rank"`
	Pipeline Pipeline          `toml:"pipeline"`
	Models   Models            `toml:"models"`
	Keys     map[string]string `toml:"keys"` // rebinding: action -> key

	// Source is the path the config was read from, or "builtin".
	Source string `toml:"-"`
	// Warnings are non-fatal problems found while loading (unknown or
	// deprecated keys); the caller logs them.
	Warnings []string `toml:"-"`
}

// Defaults returns the built-in prt.toml values.
func Defaults() Config {
	return Config{
		GitHub: GitHub{ReleaseBranches: []string{"release/*"}},
		Rank: Rank{
			WRequested: 3.0, WBreaking: 2.0, WRisk: 0.8, WTestGap: 0.6, WRelevance: 0.5,
			WRelease: 2.0, WCIRed: 2.5, WAge: 0.4, WMinutes: 0.02, WUnpushed: 0.3, WReadyStale: 1.0,
		},
		Pipeline: Pipeline{
			MaxGroupUnits: 8, EmbedMinCosine: 0.72, EmbedTopK: 3, KeepPairP: 0.60, UnsureBelow: 0.60,
			Concurrency: 4, LineBatch: 20, MechanicalMin: 3, RefreshSeconds: 60,
			MaxPairsPerUnit: DefaultMaxPairsPerUnit,
		},
		Models: Models{
			Embed: "ollama:embeddinggemma-2",
			Fast:  "anthropic:claude-haiku-5-5",
			Deep:  "anthropic:claude-opus-5-5",
		},
		Source: "builtin",
	}
}

// Rule is one routing rule.
type Rule struct {
	When    string   `toml:"when"`
	Tier    string   `toml:"tier"`
	Prompts []string `toml:"prompts"`
	Context []string `toml:"context"`
}

// Routing is prt.routing.toml.
type Routing struct {
	DeepSlots         int     `toml:"deep_slots"`
	CostCapUSD        float64 `toml:"cost_cap_usd"`
	EscalateBelow     float64 `toml:"escalate_below"`
	CalibrationSample float64 `toml:"calibration_sample"`
	Rules             []Rule  `toml:"rule"`

	Source   string   `toml:"-"`
	Warnings []string `toml:"-"`
}

// DefaultRouting returns the built-in routing rules from the spec.
func DefaultRouting() Routing {
	return Routing{
		DeepSlots: 3, CostCapUSD: 0.40, EscalateBelow: 0.60, CalibrationSample: 0.05,
		Rules: []Rule{
			{When: "mechanical || kind in [formatting, moved, generated, dependency]", Tier: "none"},
			{When: "risk >= 4 || tag.auth || tag.money || tag.async", Tier: "deep", Prompts: []string{"security"}, Context: []string{"callers", "tests"}},
			{When: "breaking", Tier: "fast", Context: []string{"callers"}},
			{When: "kind == test", Tier: "fast", Prompts: []string{"tests"}, Context: []string{"code_under_test"}},
			{When: "true", Tier: "fast"},
		},
		Source: "builtin",
	}
}

// Dirs returns the lookup directories in order: <gitDir>/prt, then
// $XDG_CONFIG_HOME/prt (or ~/.config/prt). gitDir may be empty.
func Dirs(gitDir string) []string {
	var dirs []string
	if gitDir != "" {
		dirs = append(dirs, filepath.Join(gitDir, "prt"))
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		if home, err := os.UserHomeDir(); err == nil {
			xdg = filepath.Join(home, ".config")
		}
	}
	if xdg != "" {
		dirs = append(dirs, filepath.Join(xdg, "prt"))
	}
	return dirs
}

// Find returns the first existing path of name in dirs, or "".
func Find(dirs []string, name string) string {
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// Load reads prt.toml over the defaults (missing keys keep their defaults).
func Load(dirs []string) (Config, error) {
	c := Defaults()
	p := Find(dirs, MainFile)
	if p == "" {
		return c, nil
	}
	warns, err := decodeFile(p, &c)
	if err != nil {
		return Defaults(), err
	}
	if c.Models.JevEndpoint != "" {
		warns = append(warns, fmt.Sprintf("%s: models.jev_endpoint is ignored; Jev is reached through models.jev_base_url (env JEV_BASE_URL)", p))
	}
	c.Source, c.Warnings = p, warns
	return c, nil
}

// LoadRouting reads prt.routing.toml. A file with rules replaces the default
// rule list entirely; scalar knobs it omits keep their defaults.
func LoadRouting(dirs []string) (Routing, error) {
	r := DefaultRouting()
	p := Find(dirs, RoutingFile)
	if p == "" {
		return r, nil
	}
	defRules := r.Rules
	r.Rules = nil
	warns, err := decodeFile(p, &r)
	if err != nil {
		return DefaultRouting(), err
	}
	if len(r.Rules) == 0 {
		r.Rules = defRules
	}
	r.Source, r.Warnings = p, warns
	return r, nil
}

// ReadQuestions returns the raw prt.questions.toml bytes and path, or
// (nil, "builtin") if absent; package questions parses and validates it.
func ReadQuestions(dirs []string) ([]byte, string, error) {
	p := Find(dirs, QuestionsFile)
	if p == "" {
		return nil, "builtin", nil
	}
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, p, err
	}
	return b, p, nil
}

// decodeFile decodes a TOML file into v. Syntax and type errors are fatal;
// unknown keys are returned as warnings so a config written for another
// version of prt (or copied from the spec) still starts with defaults.
func decodeFile(path string, v any) ([]string, error) {
	md, err := toml.DecodeFile(path, v)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return []string{fmt.Sprintf("%s: unknown keys %v ignored", path, und)}, nil
	}
	return nil, nil
}
