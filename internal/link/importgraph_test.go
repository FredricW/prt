package link

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/testutil"
)

// failingList is a Source whose listing fails.
type failingList struct{ Source }

func (failingList) ListFiles(context.Context, string) ([]string, error) {
	return nil, errors.New("bad revision")
}

func TestImportGraphResolution(t *testing.T) {
	src := fakeRepo(map[string]string{
		"web/src/api/client.ts":     "export function getUser(id: string) { return id }\n",
		"web/src/api/index.ts":      "export { getUser } from \"./client\";\n",
		"web/src/components/A.tsx":  "import { getUser } from \"../api/client\";\nimport * as api from \"../api\";\nimport React from \"react\";\nexport const A = () => getUser(api.x);\n",
		"web/src/components/B.tsx":  "import type { User } from \"../api/client\";\nimport { getUser } from \"../api/client\";\nexport const B = 1;\n",
		"server/cmd/api/main.go":    "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/portal/server/internal/auth\"\n)\n\nfunc main() { fmt.Println(auth.Check()) }\n",
		"server/internal/auth/a.go": "package auth\n\nfunc Check() bool { return true }\n",
		"server/internal/auth/b.go": "package auth\n\nfunc other() {}\n",
		"scripts/report.py":         "import json\n\ndef render():\n    return json.dumps({})\n",
		"scripts/test_report.py":    "from report import render\n\nrender()\n",
		"README.md":                 "# not source\n",
	}, nil)
	g, err := ImportGraph(context.Background(), src, nil, "base")
	if err != nil {
		t.Fatal(err)
	}
	wantFiles := []string{
		"scripts/report.py", "scripts/test_report.py", "server/cmd/api/main.go",
		"server/internal/auth/a.go", "server/internal/auth/b.go", "web/src/api/client.ts",
		"web/src/api/index.ts", "web/src/components/A.tsx", "web/src/components/B.tsx",
	}
	if !reflect.DeepEqual(g.Files, wantFiles) {
		t.Errorf("Files = %v", g.Files)
	}
	cases := []struct {
		file  string
		want  []string
		stmts int
	}{
		{"web/src/components/A.tsx", []string{"web/src/api/client.ts", "web/src/api/index.ts"}, 2},
		{"web/src/components/B.tsx", []string{"web/src/api/client.ts"}, 2},
		{"web/src/api/index.ts", []string{"web/src/api/client.ts"}, 1},
		{"server/cmd/api/main.go", []string{"server/internal/auth/a.go", "server/internal/auth/b.go"}, 1},
		{"scripts/test_report.py", []string{"scripts/report.py"}, 1},
		{"scripts/report.py", nil, 0},
		{"server/internal/auth/a.go", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			if got := g.Targets(c.file); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Targets = %v, want %v", got, c.want)
			}
			if n := len(g.Imports[c.file]); n != c.stmts {
				t.Errorf("%d resolving statements, want %d", n, c.stmts)
			}
		})
	}
	if !g.Complete || g.Truncated {
		t.Errorf("complete %v truncated %v", g.Complete, g.Truncated)
	}
}

func TestImportGraphErrorsAndCache(t *testing.T) {
	ctx := context.Background()
	if _, err := ImportGraph(ctx, failingList{fakeRepo(nil, nil)}, nil, "base"); !errors.Is(err, ErrUnreadable) {
		t.Errorf("listing failure: err %v, want ErrUnreadable", err)
	}
	if _, err := ImportGraph(ctx, nil, nil, "base"); !errors.Is(err, ErrUnreadable) {
		t.Errorf("nil source: err %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ImportGraph(cctx, fakeRepo(nil, nil), nil, "base"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: err %v", err)
	}

	src := &countingSource{Source: fakeRepo(map[string]string{
		"a/x.ts": "import { y } from \"../b/y\";\n",
		"b/y.ts": "export const y = 1;\n",
	}, nil)}
	cache := &mapCache{}
	first, err := ImportGraph(ctx, src, cache, "base")
	if err != nil {
		t.Fatal(err)
	}
	shows := src.shows
	again, err := ImportGraph(ctx, src, cache, "base")
	if err != nil {
		t.Fatal(err)
	}
	if src.shows != shows || src.lists != 1 {
		t.Errorf("second graph read files: shows %d → %d, lists %d", shows, src.shows, src.lists)
	}
	if !reflect.DeepEqual(first, again) {
		t.Errorf("cached graph differs:\n%+v\n%+v", first, again)
	}
	if _, ok := cache.m[indexNS+"/"+indexKey("base")]; !ok {
		t.Errorf("index not cached under %s: %v", indexNS, cache.m)
	}
}

func TestImportGraphFixtureBase(t *testing.T) {
	ctx := context.Background()
	g, err := gitx.Open(ctx, testutil.FixtureRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	fg, err := ImportGraph(ctx, g, nil, "main")
	if err != nil {
		t.Fatal(err)
	}
	has := func(from, to string) bool { return slices.Contains(fg.Targets(from), to) }
	for _, e := range [][2]string{
		{"web/src/components/UserCard.tsx", "web/src/api/client.ts"},
		{"web/src/components/UserCard.tsx", "web/src/utils/format.ts"},
		{"web/src/App.tsx", "web/src/components/UserList.tsx"},
		{"server/cmd/api/main.go", "server/internal/auth/middleware.go"},
		{"server/cmd/api/main.go", "server/internal/billing/invoice.go"},
		{"server/cmd/api/main.go", "server/internal/users/users.go"},
		{"scripts/test_report.py", "scripts/report.py"},
	} {
		if !has(e[0], e[1]) {
			t.Errorf("no import edge %s → %s (targets %v)", e[0], e[1], fg.Targets(e[0]))
		}
	}
	components := 0
	for _, f := range fg.Files {
		if dirOf(f) != "web/src/components" {
			continue
		}
		for _, tg := range fg.Targets(f) {
			if dirOf(tg) == "web/src/api" {
				components++
				break
			}
		}
	}
	if components < 5 {
		t.Errorf("%d components import web/src/api, want ≥ 5", components)
	}
}
