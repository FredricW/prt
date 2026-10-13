package settings

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/config"
)

// A missing prt.toml is seeded from the active one (its other keys and
// comments carried over); an existing one is never re-seeded; no seed
// writes only the page's keys.
func TestSaveSeedsMain(t *testing.T) {
	v := sampleValues()
	const global = "# my global config\n[github]\nrelease_branches = [\"rel/*\"]\n\n[keys]\nquit = \"Q\"\n\n[models]\njev_base_url = \"https://example.test\"\n"
	seedDir := t.TempDir()
	seed := filepath.Join(seedDir, config.MainFile)
	writeFile(t, seed, global)

	t.Run("seeded", func(t *testing.T) {
		dir := t.TempDir()
		if err := SaveSeeded(dir, v, seed, "builtin"); err != nil {
			t.Fatal(err)
		}
		c, err := config.Load([]string{dir})
		if err != nil {
			t.Fatal(err)
		}
		if c.Keys["quit"] != "Q" || !reflect.DeepEqual(c.GitHub.ReleaseBranches, []string{"rel/*"}) || c.Models.JevBaseURL != "https://example.test" {
			t.Errorf("seeded config lost keys: %+v", c)
		}
		if got, _ := loaded(t, dir); !reflect.DeepEqual(got, v) {
			t.Errorf("values = %+v, want %+v", got, v)
		}
		if !strings.HasPrefix(readFile(t, filepath.Join(dir, config.MainFile)), "# my global config\n") {
			t.Error("comment not carried over")
		}
		if readFile(t, seed) != global {
			t.Error("seed file modified")
		}
	})

	t.Run("existing file not re-seeded", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, config.MainFile), "[user]\nrole = \"x\"\n")
		if err := SaveSeeded(dir, v, seed, "builtin"); err != nil {
			t.Fatal(err)
		}
		if text := readFile(t, filepath.Join(dir, config.MainFile)); strings.Contains(text, "quit") {
			t.Errorf("existing file re-seeded:\n%s", text)
		}
	})

	for _, s := range []string{"builtin", ""} {
		t.Run("no seed "+s, func(t *testing.T) {
			dir := t.TempDir()
			if err := SaveSeeded(dir, v, s, "builtin"); err != nil {
				t.Fatal(err)
			}
			if text := readFile(t, filepath.Join(dir, config.MainFile)); strings.Contains(text, "[keys]") {
				t.Errorf("unseeded file has [keys]:\n%s", text)
			}
		})
	}
}
