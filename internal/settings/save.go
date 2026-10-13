package settings

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// newFileMode is the mode of a settings file Save creates.
const newFileMode fs.FileMode = 0o600

// canonical returns v as Save writes it: auto_review in its canonical
// spelling. v must be valid.
func canonical(v proto.SettingsValues) proto.SettingsValues {
	a, _ := model.ParseAutoReview(v.AutoReview)
	v.AutoReview = string(a)
	v.Owns = slices.Clone(v.Owns)
	return v
}

// edit is one key Save writes.
type edit struct {
	table, key, lit string
}

// mainEdits lists the prt.toml keys of v in file order.
func mainEdits(v proto.SettingsValues) []edit {
	es := []edit{
		{"user", "role", tomlString(v.Role)},
		{"user", "owns", tomlStrings(v.Owns)},
		{"pipeline", "auto_review", tomlString(v.AutoReview)},
		{"pipeline", "unsure_below", tomlFloat(v.UnsureBelow)},
		{"pipeline", "keep_pair_p", tomlFloat(v.KeepPairP)},
		{"pipeline", "embed_min_cosine", tomlFloat(v.EmbedMinCosine)},
		{"pipeline", "max_group_units", tomlInt(v.MaxGroupUnits)},
		{"pipeline", "concurrency", tomlInt(v.Concurrency)},
		{"models", "fast", tomlString(v.Fast)},
		{"models", "deep", tomlString(v.Deep)},
		{"models", "embed", tomlString(v.Embed)},
	}
	for _, w := range rankWeights(v.Rank) {
		es = append(es, edit{"rank", w.key, tomlFloat(w.val)})
	}
	return es
}

// routingEdits lists the prt.routing.toml keys of v (top level).
func routingEdits(v proto.SettingsValues) []edit {
	return []edit{
		{"", "deep_slots", tomlInt(v.DeepSlots)},
		{"", "cost_cap_usd", tomlFloat(v.CostCapUSD)},
	}
}

// applyEdits parses text, applies es and returns the new text.
func applyEdits(text string, es []edit) (string, error) {
	d, err := parseDoc(text)
	if err != nil {
		return "", fmt.Errorf("cannot edit: %w", err)
	}
	for _, e := range es {
		if err := d.set(e.table, e.key, e.lit); err != nil {
			return "", fmt.Errorf("cannot set %s: %w", dotted(e.table, e.key), err)
		}
	}
	return d.String(), nil
}

func dotted(table, key string) string {
	if table == "" {
		return key
	}
	return table + "." + key
}

// verifyMain decodes an edited prt.toml the way config.Load does and checks
// every key SettingsValues covers came out as v.
func verifyMain(text string, v proto.SettingsValues) error {
	c := config.Defaults()
	if _, err := toml.Decode(text, &c); err != nil {
		return fmt.Errorf("edited file does not decode, nothing written: %w", err)
	}
	got := FromConfig(c, config.Routing{CostCapUSD: v.CostCapUSD, DeepSlots: v.DeepSlots})
	got.AutoReview = c.Pipeline.AutoReview // raw, before Load's normalisation
	return compare(got, v)
}

// verifyRouting decodes an edited prt.routing.toml and checks its scalars.
func verifyRouting(text string, v proto.SettingsValues) error {
	r := config.DefaultRouting()
	r.Rules = nil
	if _, err := toml.Decode(text, &r); err != nil {
		return fmt.Errorf("edited file does not decode, nothing written: %w", err)
	}
	if r.DeepSlots != v.DeepSlots {
		return mismatch("deep_slots")
	}
	if r.CostCapUSD != v.CostCapUSD {
		return mismatch("cost_cap_usd")
	}
	return nil
}

// compare names the first prt.toml key whose decoded value is not v's.
func compare(got, want proto.SettingsValues) error {
	checks := []struct {
		key string
		ok  bool
	}{
		{"user.role", got.Role == want.Role},
		{"user.owns", slices.Equal(got.Owns, want.Owns)},
		{"pipeline.auto_review", got.AutoReview == want.AutoReview},
		{"pipeline.unsure_below", got.UnsureBelow == want.UnsureBelow},
		{"pipeline.keep_pair_p", got.KeepPairP == want.KeepPairP},
		{"pipeline.embed_min_cosine", got.EmbedMinCosine == want.EmbedMinCosine},
		{"pipeline.max_group_units", got.MaxGroupUnits == want.MaxGroupUnits},
		{"pipeline.concurrency", got.Concurrency == want.Concurrency},
		{"models.fast", got.Fast == want.Fast},
		{"models.deep", got.Deep == want.Deep},
		{"models.embed", got.Embed == want.Embed},
		{"rank", got.Rank == want.Rank},
	}
	for _, c := range checks {
		if !c.ok {
			return mismatch(c.key)
		}
	}
	return nil
}

func mismatch(key string) error {
	return fmt.Errorf("edited file would not read back %s as saved (duplicate or unusual key layout?), nothing written", key)
}

// readIfExists returns a file's text and whether it exists.
func readIfExists(path string) (string, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("settings: read %s: %w", path, err)
	}
	return string(b), true, nil
}

// readSeed returns the text a new routing file starts from: the active
// routing file's, or "" for the built-in rules ("" / "builtin", or a path
// that no longer exists, which means the built-ins are what is active).
func readSeed(seed string) (string, error) {
	if seed == "" || seed == "builtin" {
		return "", nil
	}
	text, _, err := readIfExists(seed)
	return text, err
}

// writeAtomic replaces path with data: a temp file in the same directory,
// synced, given the old file's mode (newFileMode for a new file), then
// renamed over it. A symlinked path is written through to its target.
func writeAtomic(path string, data []byte) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	mode := newFileMode
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("settings: write %s: %w", path, err)
	}
	tmp := f.Name()
	if err := fillTemp(f, data, mode); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("settings: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("settings: write %s: %w", path, err)
	}
	return nil
}

// fillTemp writes, syncs, closes and chmods the temp file.
func fillTemp(f *os.File, data []byte, mode fs.FileMode) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(f.Name(), mode)
}
