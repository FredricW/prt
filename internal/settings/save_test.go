package settings

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/proto"
)

var update = flag.Bool("update", false, "rewrite testdata/golden files")

// sampleValues differs from the defaults in every field.
func sampleValues() proto.SettingsValues {
	v := FromConfig(config.Defaults(), config.DefaultRouting())
	v.AutoReview = "needs-you+yours"
	v.Role = `backend "core" \ ops`
	v.Owns = []string{"api/**", "internal/{a,b}/*.go"}
	v.CostCapUSD = 2
	v.DeepSlots = 5
	v.Fast = "anthropic:claude-haiku-6"
	v.Deep = "anthropic:claude-opus-6"
	v.Embed = "ollama:nomic-embed-code"
	v.Rank.WRequested = 4
	v.Rank.WMinutes = 0.015
	v.Rank.WReadyStale = 0
	v.UnsureBelow = 0.55
	v.KeepPairP = 1
	v.EmbedMinCosine = 0.7
	v.MaxGroupUnits = 12
	v.Concurrency = 6
	return v
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loaded reads dir back the way the daemon does.
func loaded(t *testing.T, dir string) (proto.SettingsValues, config.Routing) {
	t.Helper()
	c, err := config.Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	r, err := config.LoadRouting([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	return FromConfig(c, r), r
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, got)
		return
	}
	if want := readFile(t, path); got != want {
		t.Errorf("%s differs from golden:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// keptLines checks that every line of before outside an edited key's value
// appears in after, in order, and that edited lines' trailing comments
// survive.
func keptLines(t *testing.T, before, after string, edits []edit) {
	t.Helper()
	d, err := parseDoc(before)
	if err != nil {
		t.Fatal(err)
	}
	edited := map[int]bool{}
	for _, e := range d.entries {
		for _, ed := range edits {
			want := []string{ed.key}
			if ed.table != "" {
				want = []string{ed.table, ed.key}
			}
			if !e.inArray && reflect.DeepEqual(e.path, want) {
				for i := e.start; i <= e.end; i++ {
					edited[i] = true
				}
				if tail := strings.TrimSpace(d.lines[e.end][e.valEnd:]); tail != "" && !strings.Contains(after, tail) {
					t.Errorf("trailing comment %q of %s lost", tail, ed.key)
				}
			}
		}
	}
	out := strings.Split(strings.ReplaceAll(after, "\r\n", "\n"), "\n")
	j := 0
	for i, line := range d.lines {
		if edited[i] {
			continue
		}
		for j < len(out) && out[j] != line {
			j++
		}
		if j == len(out) {
			t.Errorf("line %d %q not kept in order", i+1, line)
			return
		}
		j++
	}
}

func TestSaveFixtures(t *testing.T) {
	commented := readFile(t, filepath.Join("testdata", "commented.toml"))
	tests := []struct {
		name   string
		golden string
		text   *string // nil: no prt.toml at all
	}{
		{"commented", "commented.toml", ptr(commented)},
		{"keys and github", "keys_and_github.toml", ptr(readFile(t, filepath.Join("testdata", "keys_and_github.toml")))},
		{"multi-line owns", "multiline_owns.toml", ptr(readFile(t, filepath.Join("testdata", "multiline_owns.toml")))},
		{"missing tables, no final newline", "missing_tables.toml", ptr(readFile(t, filepath.Join("testdata", "missing_tables.toml")))},
		{"empty file", "empty.toml", ptr("")},
		{"no file", "empty.toml", nil},
		{"crlf", "commented_crlf.toml", ptr(strings.ReplaceAll(commented, "\n", "\r\n"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "prt")
			path := filepath.Join(dir, config.MainFile)
			before := ""
			if tt.text != nil {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				before = *tt.text
				writeFile(t, path, before)
			}
			v := sampleValues()
			if err := Save(dir, v, "builtin"); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, _ := loaded(t, dir)
			if !reflect.DeepEqual(got, v) {
				t.Errorf("read back\n%+v\nwant\n%+v", got, v)
			}
			after := readFile(t, path)
			checkGolden(t, tt.golden, after)
			keptLines(t, before, after, mainEdits(v))
			if strings.Contains(before, "\r\n") && strings.Count(after, "\n") != strings.Count(after, "\r\n") {
				t.Errorf("CRLF file got bare LF lines:\n%q", after)
			}
			assertIdempotent(t, dir, v)
		})
	}
}

// assertIdempotent saves v again: the files must not change at all (bytes
// and modification time).
func assertIdempotent(t *testing.T, dir string, v proto.SettingsValues) {
	t.Helper()
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	files := []string{filepath.Join(dir, config.MainFile), filepath.Join(dir, config.RoutingFile)}
	before := make([]string, len(files))
	for i, f := range files {
		before[i] = readFile(t, f)
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(dir, v, "builtin"); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	for i, f := range files {
		if got := readFile(t, f); got != before[i] {
			t.Errorf("%s changed on an identical save:\n%s", filepath.Base(f), got)
		}
		st, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if !st.ModTime().Equal(old) {
			t.Errorf("%s rewritten on an identical save", filepath.Base(f))
		}
	}
}

func ptr(s string) *string { return &s }

func TestSaveRouting(t *testing.T) {
	rulesText := readFile(t, filepath.Join("testdata", "routing_with_rules.toml"))
	v := sampleValues()

	t.Run("existing file keeps rules", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, config.RoutingFile)
		writeFile(t, path, rulesText)
		if err := Save(dir, v, "builtin"); err != nil {
			t.Fatal(err)
		}
		got, r := loaded(t, dir)
		if got.DeepSlots != v.DeepSlots || got.CostCapUSD != v.CostCapUSD {
			t.Errorf("scalars = %d, %v", got.DeepSlots, got.CostCapUSD)
		}
		if len(r.Rules) != 2 || r.Rules[0].When != "risk >= 3" || r.EscalateBelow != 0.5 {
			t.Errorf("rules or other scalars changed: %+v", r)
		}
		after := readFile(t, path)
		checkGolden(t, "routing_with_rules.toml", after)
		keptLines(t, rulesText, after, routingEdits(v))
		assertIdempotent(t, dir, v)
	})

	t.Run("seeded from the active file", func(t *testing.T) {
		seedDir, dir := t.TempDir(), t.TempDir()
		seed := filepath.Join(seedDir, config.RoutingFile)
		writeFile(t, seed, rulesText)
		if err := Save(dir, v, seed); err != nil {
			t.Fatal(err)
		}
		_, r := loaded(t, dir)
		if len(r.Rules) != 2 || r.DeepSlots != v.DeepSlots {
			t.Errorf("seeded routing = %+v", r)
		}
		if readFile(t, seed) != rulesText {
			t.Error("seed file modified")
		}
	})

	for _, seed := range []string{"builtin", "", filepath.Join(t.TempDir(), "gone.toml")} {
		t.Run("builtin seed "+filepath.Base(seed), func(t *testing.T) {
			dir := t.TempDir()
			if err := Save(dir, v, seed); err != nil {
				t.Fatal(err)
			}
			text := readFile(t, filepath.Join(dir, config.RoutingFile))
			if text != "deep_slots = 5\ncost_cap_usd = 2.0\n" {
				t.Errorf("routing file = %q", text)
			}
			_, r := loaded(t, dir)
			if len(r.Rules) != len(config.DefaultRouting().Rules) || len(r.Rules) != 5 {
				t.Errorf("rules = %d, want the 5 built-ins", len(r.Rules))
			}
		})
	}

	t.Run("rules only, scalars inserted before the first rule", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, config.RoutingFile), "# my rules\n\n# first rule\n[[rule]]\nwhen = \"true\"\ntier = \"none\"\n")
		if err := Save(dir, v, "builtin"); err != nil {
			t.Fatal(err)
		}
		want := "# my rules\n\ndeep_slots = 5\ncost_cap_usd = 2.0\n\n# first rule\n[[rule]]\nwhen = \"true\"\ntier = \"none\"\n"
		if got := readFile(t, filepath.Join(dir, config.RoutingFile)); got != want {
			t.Errorf("got\n%s\nwant\n%s", got, want)
		}
	})
}

func TestSaveRefusesAndWritesNothing(t *testing.T) {
	bad := sampleValues()
	bad.KeepPairP = 2
	tests := []struct {
		name    string
		text    string
		v       proto.SettingsValues
		wantErr string
	}{
		{"invalid values", "[user]\nrole = \"x\"\n", bad, "keep_pair_p must be between 0 and 1"},
		{"duplicate key", "[pipeline]\nconcurrency = 4\nconcurrency = 5\n", sampleValues(), "does not decode"},
		{"inline table", "models = { fast = \"x\" }\n", sampleValues(), "does not decode"},
		{"unterminated array", "[user]\nowns = [\n\"a\",\n", sampleValues(), "cannot edit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, config.MainFile)
			writeFile(t, path, tt.text)
			err := Save(dir, tt.v, "builtin")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if got := readFile(t, path); got != tt.text {
				t.Errorf("prt.toml changed to %q", got)
			}
			if _, err := os.Stat(filepath.Join(dir, config.RoutingFile)); !os.IsNotExist(err) {
				t.Errorf("routing file written: %v", err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Errorf("dir has %d entries, want only prt.toml", len(entries))
			}
		})
	}
}

func TestVerifyCatchesMismatch(t *testing.T) {
	v := sampleValues()
	good, err := applyEdits("", mainEdits(v))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyMain(good, v); err != nil {
		t.Fatalf("good text rejected: %v", err)
	}
	if err := verifyMain(strings.Replace(good, "concurrency = 6", "concurrency = 7", 1), v); err == nil || !strings.Contains(err.Error(), "pipeline.concurrency") {
		t.Errorf("mismatch not caught: %v", err)
	}
	if err := verifyRouting("deep_slots = 5\ncost_cap_usd = 2.5\n", v); err == nil || !strings.Contains(err.Error(), "cost_cap_usd") {
		t.Errorf("routing mismatch not caught: %v", err)
	}
}

func TestSaveModesAndSymlinks(t *testing.T) {
	v := sampleValues()
	t.Run("new files are 0600, existing modes kept", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, config.MainFile), "")
		if err := os.Chmod(filepath.Join(dir, config.MainFile), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := Save(dir, v, "builtin"); err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]os.FileMode{config.MainFile: 0o640, config.RoutingFile: 0o600} {
			st, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != want {
				t.Errorf("%s mode = %v, want %v", name, st.Mode().Perm(), want)
			}
		}
	})
	t.Run("symlinked prt.toml is written through", func(t *testing.T) {
		dir, other := t.TempDir(), t.TempDir()
		target := filepath.Join(other, "real.toml")
		writeFile(t, target, "# mine\n")
		link := filepath.Join(dir, config.MainFile)
		if err := os.Symlink(target, link); err != nil {
			t.Skip("symlinks unsupported:", err)
		}
		if err := Save(dir, v, "builtin"); err != nil {
			t.Fatal(err)
		}
		if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
			t.Errorf("symlink replaced: %v", err)
		}
		if !strings.HasPrefix(readFile(t, target), "# mine\n\n[user]\n") {
			t.Errorf("target = %q", readFile(t, target))
		}
	})
}

func TestEditorSet(t *testing.T) {
	tests := []struct {
		name, in, table, key, lit, want string
	}{
		{"replace keeps comment and indent", "[a]\n  k = 1   # one\n", "a", "k", "2", "[a]\n  k = 2   # one\n"},
		{"equal value left alone", "[a]\nk = 1.50 # x\n", "a", "k", "1.5", "[a]\nk = 1.50 # x\n"},
		{"multi-line array replaced as a unit", "[a]\nk = [\n 1, # c\n 2,\n] # end\nz = 0\n", "a", "k", "[3]", "[a]\nk = [3] # end\nz = 0\n"},
		{"append after last key", "[a]\nx = 1\n\n# about b\n[b]\n", "a", "k", "2", "[a]\nx = 1\nk = 2\n\n# about b\n[b]\n"},
		{"empty table", "[a]\n[b]\n", "a", "k", "2", "[a]\nk = 2\n[b]\n"},
		{"new table", "x = 1", "a", "k", "2", "x = 1\n\n[a]\nk = 2\n"},
		{"array tables untouched", "[[a]]\nk = 1\n", "a", "k", "2", "[[a]]\nk = 1\n\n[a]\nk = 2\n"},
		{"quoted key and header", "[\"a\"]\n'k' = 1\n", "a", "k", "2", "[\"a\"]\n'k' = 2\n"},
		{"top level before header", "# c\n[a]\n", "", "k", "2", "k = 2\n\n# c\n[a]\n"},
		{"top level after keys", "j = 0\n\n[a]\nk = 5\n", "", "k", "2", "j = 0\nk = 2\n\n[a]\nk = 5\n"},
		{"crlf", "[a]\r\nk = 1\r\n", "a", "j", "2", "[a]\r\nk = 1\r\nj = 2\r\n"},
		{"literal multi-line string skipped", "[a]\ns = '''\nk = 1\n'''\n", "a", "k", "2", "[a]\ns = '''\nk = 1\n'''\nk = 2\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := parseDoc(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.set(tt.table, tt.key, tt.lit); err != nil {
				t.Fatal(err)
			}
			if got := d.String(); got != tt.want {
				t.Errorf("got\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestTOMLLiterals(t *testing.T) {
	floats := map[float64]string{3: "3.0", 0.02: "0.02", 0: "0.0", 1e21: "1000000000000000000000.0", 1e-7: "0.0000001"}
	for f, want := range floats {
		if got := tomlFloat(f); got != want {
			t.Errorf("tomlFloat(%v) = %q, want %q", f, got, want)
		}
	}
	if got := tomlString("a\"b\\c\n\x01é"); got != `"a\"b\\c\n\u0001é"` {
		t.Errorf("tomlString = %s", got)
	}
	if got := tomlStrings(nil); got != "[]" {
		t.Errorf("tomlStrings(nil) = %s", got)
	}
	if got := tomlStrings([]string{"a", "b"}); got != `["a", "b"]` {
		t.Errorf("tomlStrings = %s", got)
	}
}
