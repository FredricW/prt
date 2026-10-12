package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/store"
)

func TestSnapshotRendersPickerAndDiff(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	g := &gitx.Fake{
		RefList:    []gitx.Ref{{Name: "main", SHA: "b1"}, {Name: "feat/x", SHA: "h1", Subject: "add y"}},
		Ahead:      map[string]int{"main..feat/x": 1},
		MergeBases: map[string]string{"main..h1": "b1"},
		Diffs:      map[string]string{"b1..h1": "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1,2 @@\n x\n+y\n"},
	}
	c := daemon.NewInProcess(pipeline.Deps{Git: g, Store: st, Config: config.Defaults()})
	defer func() { _ = c.Close() }()

	var out bytes.Buffer
	if err := Snapshot(ctx, c, &out, 100, 20, 3); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"Branches", "feat/x", "+y", "@@ -1 +1,2 @@"} {
		if !strings.Contains(s, want) {
			t.Errorf("snapshot missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "\x1b[") {
		t.Error("snapshot must not contain ANSI escapes")
	}
}
