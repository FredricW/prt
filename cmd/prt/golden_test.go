package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/testutil"
)

var update = flag.Bool("update", false, "rewrite cmd/prt/testdata/*.golden from the current output")

// TestSingleRepoSnapshotGolden pins the single-repo snapshot as `make
// snapshot` renders it (the fixture repo with the recorded answers and the
// seeded drafts, six items, 120x30), so work on workspaces or anything else
// cannot change a single repo's screens unnoticed (M5.3). The store is
// fresh, so stages show as run, not cached. Regenerate with
// `go test ./cmd/prt -run SnapshotGolden -update` after an intended change.
func TestSingleRepoSnapshotGolden(t *testing.T) {
	isolate(t)
	repo := testutil.FixtureRepo(t)
	rec := testutil.Testdata(t, "recordings")
	code, out, errOut := runPrt("--no-daemon", "--offline",
		"--jev-replay", filepath.Join(rec, "jev"), "--embed-replay", filepath.Join(rec, "embed"),
		"--review-replay", filepath.Join(rec, "review"),
		"--snapshot", "--width", "120", "--height", "30", "--max-items", "6", "--now", "2026-10-13T12:00:00Z",
		"--repo", repo, "--fixtures", testutil.FixturePRs(t), "--drafts", testutil.Testdata(t, "fixtures", "drafts.json"))
	if code != 0 {
		t.Fatalf("code %d err %q", code, errOut)
	}
	golden := filepath.Join("testdata", "snapshot_single.golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if out != string(want) {
		t.Errorf("single-repo snapshot changed (-update after an intended change):\n%s", screenDiff(string(want), out))
	}
}

// screenDiff names every "=== screen: … ===" block that differs between
// want and got, with its first differing line.
func screenDiff(want, got string) string {
	ws, gs := splitScreens(want), splitScreens(got)
	var b strings.Builder
	for i := 0; i < max(len(ws), len(gs)); i++ {
		var w, g string
		if i < len(ws) {
			w = ws[i]
		}
		if i < len(gs) {
			g = gs[i]
		}
		if w == g {
			continue
		}
		wl, gl := strings.Split(w, "\n"), strings.Split(g, "\n")
		for j := 0; j < max(len(wl), len(gl)); j++ {
			var a, c string
			if j < len(wl) {
				a = wl[j]
			}
			if j < len(gl) {
				c = gl[j]
			}
			if a != c {
				fmt.Fprintf(&b, "screen %d %q, line %d:\n  want %q\n  got  %q\n", i, firstLine(w+g), j, a, c)
				break
			}
		}
	}
	if len(ws) != len(gs) {
		fmt.Fprintf(&b, "%d screens, want %d\n", len(gs), len(ws))
	}
	return b.String()
}

func splitScreens(s string) []string { return strings.Split(s, "=== screen: ") }

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}
