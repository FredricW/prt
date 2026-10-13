// Package settings backs the settings page (M5.1): it maps the active
// configuration to the page's values, validates them, writes them back to
// prt.toml and prt.routing.toml preserving every other line (comments,
// unknown keys, [keys], [github], routing rules), creates the starter
// prt.questions.toml, and reports the environment (presence of keys,
// Ollama reachability, GitHub) without ever reading out a secret.
//
// The API below was fixed by the M5.1 lead (DECISIONS "M5.1 shared
// contracts"); the settings work package implements the stubbed bodies
// without changing these signatures. The daemon is the only caller.
package settings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/proto"
)

// probeTimeout bounds one Ollama probe.
const probeTimeout = time.Second

// FromConfig returns the page values of a loaded prt.toml and
// prt.routing.toml.
func FromConfig(c config.Config, r config.Routing) proto.SettingsValues {
	return proto.SettingsValues{
		AutoReview: string(c.Pipeline.Strategy()),
		Role:       c.User.Role, Owns: slices.Clone(c.User.Owns),
		CostCapUSD: r.CostCapUSD, DeepSlots: r.DeepSlots,
		Fast: c.Models.Fast, Deep: c.Models.Deep, Embed: c.Models.Embed,
		Rank:        c.Rank,
		UnsureBelow: c.Pipeline.UnsureBelow, KeepPairP: c.Pipeline.KeepPairP,
		EmbedMinCosine: c.Pipeline.EmbedMinCosine, MaxGroupUnits: c.Pipeline.MaxGroupUnits,
		Concurrency: c.Pipeline.Concurrency,
	}
}

// Changes says which groups of settings differ between two value sets, so
// the daemon knows what to re-run after a save (DECISIONS "M5.1 settings
// applied live").
type Changes struct {
	Strategy    bool // auto_review
	Identity    bool // role, owns (Jev label inputs, file ownership)
	Grouping    bool // keep_pair_p, embed_min_cosine, max_group_units
	Aggregate   bool // rank weights, unsure_below
	Routing     bool // cost_cap_usd, deep_slots
	Models      bool // fast, deep, embed
	Concurrency bool // concurrency
}

// Any reports whether anything changed.
func (c Changes) Any() bool {
	return c.Strategy || c.Identity || c.Grouping || c.Aggregate || c.Routing || c.Models || c.Concurrency
}

// Diff compares two value sets.
func Diff(old, cur proto.SettingsValues) Changes {
	return Changes{
		Strategy:    old.AutoReview != cur.AutoReview,
		Identity:    old.Role != cur.Role || !slices.Equal(old.Owns, cur.Owns),
		Grouping:    old.KeepPairP != cur.KeepPairP || old.EmbedMinCosine != cur.EmbedMinCosine || old.MaxGroupUnits != cur.MaxGroupUnits,
		Aggregate:   old.Rank != cur.Rank || old.UnsureBelow != cur.UnsureBelow,
		Routing:     old.CostCapUSD != cur.CostCapUSD || old.DeepSlots != cur.DeepSlots,
		Models:      old.Fast != cur.Fast || old.Deep != cur.Deep || old.Embed != cur.Embed,
		Concurrency: old.Concurrency != cur.Concurrency,
	}
}

// Validate checks v before a save: a known auto_review; probabilities and
// cosines in [0, 1]; max_group_units, concurrency and deep_slots ≥ 1;
// cost_cap_usd > 0; weights finite and ≥ 0; owns globs valid
// (doublestar.ValidatePattern); fast, deep and embed non-empty
// "provider:model" specs. The error names the first bad field in words
// the form can show ("keep_pair_p must be between 0 and 1").
func Validate(v proto.SettingsValues) error {
	checks := []func(proto.SettingsValues) error{
		validateStrategy, validateIdentity, validateKnobs, validateRank, validateModels,
	}
	for _, check := range checks {
		if err := check(v); err != nil {
			return err
		}
	}
	return nil
}

// Save writes v into dir/prt.toml and dir/prt.routing.toml (dir is a scope
// directory: config.RepoDir or config.GlobalDir; created 0o755 if needed),
// editing only the keys SettingsValues covers and keeping every other line
// byte for byte. A key that exists is replaced in place (its trailing
// comment kept); a missing key is appended to its table; a missing table is
// appended to the file. When dir has no routing file, the new one starts
// as a copy of seedRouting (the active routing file's path; "" or
// "builtin" means only the two scalar keys are written, so the built-in
// rules stay active). Each file is written atomically (temp file in dir,
// then rename) and only after the edited text decodes (config.Load /
// config.LoadRouting on dir) to exactly v; otherwise nothing changes and
// the error says why. Save validates v first (Validate).
func Save(dir string, v proto.SettingsValues, seedRouting string) error {
	if err := Validate(v); err != nil {
		return err
	}
	v = canonical(v)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("settings: create %s: %w", dir, err)
	}
	mainPath := filepath.Join(dir, config.MainFile)
	routingPath := filepath.Join(dir, config.RoutingFile)

	mainOld, mainExists, err := readIfExists(mainPath)
	if err != nil {
		return err
	}
	mainNew, err := applyEdits(mainOld, mainEdits(v))
	if err != nil {
		return fmt.Errorf("settings: %s: %w", mainPath, err)
	}
	if err := verifyMain(mainNew, v); err != nil {
		return fmt.Errorf("settings: %s: %w", mainPath, err)
	}

	routingOld, routingExists, err := readIfExists(routingPath)
	if err != nil {
		return err
	}
	if !routingExists {
		if routingOld, err = readSeed(seedRouting); err != nil {
			return err
		}
	}
	routingNew, err := applyEdits(routingOld, routingEdits(v))
	if err != nil {
		return fmt.Errorf("settings: %s: %w", routingPath, err)
	}
	if err := verifyRouting(routingNew, v); err != nil {
		return fmt.Errorf("settings: %s: %w", routingPath, err)
	}

	if !mainExists || mainNew != mainOld {
		if err := writeAtomic(mainPath, []byte(mainNew)); err != nil {
			return err
		}
	}
	if !routingExists || routingNew != routingOld {
		return writeAtomic(routingPath, []byte(routingNew))
	}
	return nil
}

// EnsureQuestions creates path with a commented starter prt.questions.toml
// when it does not exist (the built-in catalogue still applies: user
// questions merge over it, see questions.Load) and reports whether it
// created the file. An existing file is never touched.
func EnsureQuestions(path string) (bool, error) {
	if _, err := os.Lstat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("settings: %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("settings: create %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil // created by someone else meanwhile: leave it alone
	}
	if err != nil {
		return false, fmt.Errorf("settings: create %s: %w", path, err)
	}
	if _, err := f.WriteString(questionsStarter); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("settings: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("settings: write %s: %w", path, err)
	}
	return true, nil
}

// Prober checks the local embedding server. The daemon uses HTTPProber;
// tests use a fake.
type Prober interface {
	Ollama(ctx context.Context) proto.Probe
}

// HTTPProber probes Ollama with GET <Host>/api/tags (1 s timeout). Host ""
// means $OLLAMA_HOST, else http://127.0.0.1:11434 (a bare host:port gets
// http://). Client nil means a client with that timeout.
type HTTPProber struct {
	Host   string
	Client *http.Client
}

// Ollama implements Prober: OK on a 2xx answer; otherwise Note says
// "unreachable at <host>" or "HTTP <code> from <host>".
func (h HTTPProber) Ollama(ctx context.Context) proto.Probe {
	host := h.Host
	if strings.TrimSpace(host) == "" {
		host = os.Getenv("OLLAMA_HOST")
	}
	base := embed.OllamaBaseURL(host)
	shown := "the configured host"
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		shown = u.Host // never echo userinfo from OLLAMA_HOST
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tags", nil)
	if err != nil {
		return proto.Probe{Note: "unreachable at " + shown}
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: probeTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return proto.Probe{Note: "unreachable at " + shown}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return proto.Probe{Note: fmt.Sprintf("HTTP %d from %s", resp.StatusCode, shown)}
	}
	return proto.Probe{OK: true, Note: "reachable at " + shown}
}

// Env builds the settings page's environment status: JevKey and
// AnthropicKey from getenv("JEV_API_KEY") and getenv("PRT_ANTHROPIC_API_KEY")
// (presence only: OK true when non-empty, Note "set" or "not set"; the
// value is never copied), Ollama from p (nil p: Note "not checked"), and
// GitHub as given by the daemon (whether a GitHub client is wired, with its
// note).
func Env(ctx context.Context, getenv func(string) string, p Prober, github proto.Probe) proto.EnvStatus {
	if getenv == nil {
		getenv = os.Getenv
	}
	st := proto.EnvStatus{
		JevKey:       presence(getenv("JEV_API_KEY")),
		AnthropicKey: presence(getenv("PRT_ANTHROPIC_API_KEY")),
		Ollama:       proto.Probe{Note: "not checked"},
		GitHub:       github,
	}
	if p != nil {
		st.Ollama = p.Ollama(ctx)
	}
	return st
}

// presence reports whether a secret is set without ever holding its value.
func presence(secret string) proto.Probe {
	if strings.TrimSpace(secret) == "" {
		return proto.Probe{Note: "not set"}
	}
	return proto.Probe{OK: true, Note: "set"}
}
