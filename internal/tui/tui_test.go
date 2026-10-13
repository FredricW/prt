package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/store"
)

func TestSnapshotRendersPickerOverviewCodeAndHelp(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	g := &gitx.Fake{
		RefList: []gitx.Ref{
			{Name: "main", SHA: "b1"},
			{Name: "feat/x", SHA: "h1", Subject: "add greeting", CommitDate: fixedNow.Add(-50 * time.Hour)},
		},
		Ahead:      map[string]int{"main..feat/x": 1},
		MergeBases: map[string]string{"main..h1": "b1"},
		Diffs: map[string]string{"b1..h1": "diff --git a/hello.go b/hello.go\n--- a/hello.go\n+++ b/hello.go\n" +
			"@@ -1 +1,2 @@\n package hello\n+// greeting world\n"},
	}
	c := daemon.NewInProcess(pipeline.Deps{Git: g, Store: st, Config: config.Defaults()})
	defer func() { _ = c.Close() }()

	var out bytes.Buffer
	opts := Options{Now: func() time.Time { return fixedNow }}
	if err := SnapshotWithOptions(ctx, c, &out, 100, 20, 3, opts); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if testing.Verbose() {
		t.Log("\n" + s)
	}
	for _, want := range []string{
		"=== screen: picker + overview · branch:feat/x ===",
		"=== screen: picker + code · branch:feat/x ===",
		"=== screen: help · picker ===",
		"Branches (1)", "Needs you (0)", "feat/x", "feat/x — add greeting", "2d",
		"hello.go", "greeting world", "Keys · picker",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("snapshot missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "\x1b[") {
		t.Error("snapshot must not contain ANSI escapes")
	}

	var again bytes.Buffer
	if err := SnapshotWithOptions(ctx, c, &again, 100, 20, 3, opts); err != nil {
		t.Fatal(err)
	}
	if again.String() != s {
		t.Error("snapshot is not deterministic")
	}
}

func TestSnapshotEmpty(t *testing.T) {
	var out bytes.Buffer
	if err := Snapshot(context.Background(), newFake(), &out, 80, 10, 3); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "=== screen: picker (empty) ===") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}
