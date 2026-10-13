package surface

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata/golden")

// TestGolden compares the full ChangeSurface of multi-file cases with
// testdata/golden/<name>.json (go test ./internal/surface -run Golden -update
// rewrites them).
func TestGolden(t *testing.T) {
	cases := []struct {
		name string
		in   func(t *testing.T) Input
	}{
		{"fixture-101", func(t *testing.T) Input { return fixtureInput(t, "01-user-profile-refactor.patch") }},
		{"fixture-102", func(t *testing.T) Input { return fixtureInput(t, "02-rename-fetchjson.patch") }},
		{"fixture-103", func(t *testing.T) Input { return fixtureInput(t, "03-remove-legacy-billing.patch") }},
		{"fixture-104", func(t *testing.T) Input { return fixtureInput(t, "04-bump-deps.patch") }},
		{"fixture-105", func(t *testing.T) Input { return fixtureInput(t, "05-spike-dark-mode.patch") }},
		{"fixture-106", func(t *testing.T) Input { return fixtureInput(t, "06-user-preferences-api.patch") }},
		{"fixture-106-diff-only", func(t *testing.T) Input {
			in := fixtureInput(t, "06-user-preferences-api.patch")
			in.Src = nil
			return in
		}},
		{"mixed", func(t *testing.T) Input {
			return patchInput(t, readFile(t, filepath.Join("testdata", "golden", "mixed.diff")))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.MarshalIndent(build(context.Background(), tc.in(t)), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			p := filepath.Join("testdata", "golden", tc.name+".json")
			if *update {
				if err := os.WriteFile(p, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from the golden file (run with -update after checking):\n%s", tc.name, got)
			}
		})
	}
}
