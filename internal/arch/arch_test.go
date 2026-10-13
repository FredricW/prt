package arch

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
)

// repo is the fake repo of the table tests:
//
//	base: a→b, b→c, d→c, old→b, e→d
//	head: a→b, b→n, n→c, d→c, e→d  (old/ deleted, n/ added)
func repo() *gitx.Fake {
	f := &gitx.Fake{Files: map[string]map[string][]byte{"base": {}, "head": {}}}
	put := func(rev, p, s string) { f.Files[rev][p] = []byte(s) }
	for _, rev := range []string{"base", "head"} {
		put(rev, "a/a.ts", "import { b } from \"../b/b\";\nexport const a = b;\n")
		put(rev, "c/c.ts", "export const c = 1;\n")
		put(rev, "d/d.ts", "import { c } from \"../c/c\";\nexport const d = c;\n")
		put(rev, "e/e.ts", "import { d } from \"../d/d\";\nexport const e = d;\n")
		put(rev, "README.md", "# readme\n")
	}
	put("base", "b/b.ts", "import { c } from \"../c/c\";\nexport const b = c;\n")
	put("base", "old/o.ts", "import { b } from \"../b/b\";\nexport const o = b;\n")
	put("head", "b/b.ts", "import { n } from \"../n/n\";\nexport const b = n;\n")
	put("head", "n/n.ts", "import { c } from \"../c/c\";\nexport const n = c;\n")
	return f
}

func files() []model.File {
	return []model.File{
		{Path: "b/b.ts", Owned: true, Adds: 1, Dels: 1},
		{Path: "n/n.ts", Added: true, Adds: 2},
		{Path: "old/o.ts", Deleted: true, Dels: 2},
		{Path: "README.md", Adds: 1},
	}
}

func agg() *model.ItemAgg {
	return &model.ItemAgg{
		Units: []model.UnitAgg{
			{UnitID: "u1", Path: "b/b.ts", Attention: model.AttnEyes, RiskLevel: 4, Untested: true},
			{UnitID: "u2", Path: "b/b.ts", Attention: model.AttnSkim, RiskLevel: 2, Unsure: true},
			{UnitID: "u3", Path: "n/n.ts", Attention: model.AttnSkim, RiskLevel: 3},
			{UnitID: "u4", Path: "old/o.ts", Attention: model.AttnSkip, Breaking: true, RiskLevel: 1},
		},
		Undocumented: []string{"u3"},
		Headlines: []model.Headline{
			{UnitID: "u1", Path: "b/b.ts", Kind: "breaking", Text: "getB"},
			{UnitID: "u1", Path: "b/b.ts", Kind: "signature", Text: "getB"},
			{UnitID: "u3", Path: "n/n.ts", Kind: "test weakened", Text: "n"},
		},
	}
}

func input() Input {
	return Input{ItemID: "pr:1", BaseSHA: "base", HeadSHA: "head", Files: files(), Agg: agg(), Src: repo()}
}

// unreadable fails ListFiles for one revision.
type unreadable struct {
	Source
	rev string
}

func (u unreadable) ListFiles(ctx context.Context, rev string) ([]string, error) {
	if rev == u.rev {
		return nil, errors.New("fatal: bad object " + rev)
	}
	return u.Source.ListFiles(ctx, rev)
}

func moduleByID(g *model.ArchGraph, id string) (model.ArchModule, bool) {
	for _, m := range g.Modules {
		if m.ID == id {
			return m, true
		}
	}
	return model.ArchModule{}, false
}

func edgeList(g *model.ArchGraph) []string {
	out := make([]string, 0, len(g.Edges))
	for _, e := range g.Edges {
		out = append(out, fmt.Sprintf("%s→%s %s %d", e.From, e.To, e.Status, e.Imports))
	}
	return out
}

func moduleList(g *model.ArchGraph) []string {
	out := make([]string, 0, len(g.Modules))
	for _, m := range g.Modules {
		out = append(out, fmt.Sprintf("%s %s h%d", m.ID, m.Status, m.Hop))
	}
	return out
}

func TestBuild(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*Input)
		check func(*testing.T, *model.ArchGraph)
	}{
		{"modules, hops and statuses at hop 2", nil, func(t *testing.T, g *model.ArchGraph) {
			want := []string{"b changed h0", "n new h0", "old removed h0", "a context h1", "c context h1", "d context h2"}
			if got := moduleList(g); !reflect.DeepEqual(got, want) {
				t.Errorf("modules\n got %v\nwant %v", got, want)
			}
			if g.MaxHop != DefaultMaxHop || g.Note != "" {
				t.Errorf("MaxHop %d note %q", g.MaxHop, g.Note)
			}
		}},
		{"edges with status and counts", nil, func(t *testing.T, g *model.ArchGraph) {
			want := []string{"a→b same 1", "b→c removed 1", "b→n new 1", "d→c same 1", "n→c new 1", "old→b removed 1"}
			if got := edgeList(g); !reflect.DeepEqual(got, want) {
				t.Errorf("edges\n got %v\nwant %v", got, want)
			}
		}},
		{"hop 1 leaves out d", func(in *Input) { in.MaxHop = 1 }, func(t *testing.T, g *model.ArchGraph) {
			if _, ok := moduleByID(g, "d"); ok || g.MaxHop != 1 {
				t.Errorf("hop 1: %v (MaxHop %d)", moduleList(g), g.MaxHop)
			}
			for _, e := range g.Edges {
				if e.From == "d" || e.To == "d" {
					t.Errorf("edge to a hidden module: %+v", e)
				}
			}
		}},
		{"hop 3 reaches e", func(in *Input) { in.MaxHop = 3 }, func(t *testing.T, g *model.ArchGraph) {
			if m, ok := moduleByID(g, "e"); !ok || m.Hop != 3 {
				t.Errorf("e: %+v %v", m, ok)
			}
		}},
		{"roll-ups and attention", nil, func(t *testing.T, g *model.ArchGraph) {
			b, _ := moduleByID(g, "b")
			want := model.ArchModule{ID: "b", Status: model.ArchChanged, Owned: true, Files: []string{"b/b.ts"}, FileCount: 1,
				Units: 2, Eyes: 1, Skim: 1, Unsure: 1, Untested: 1, RiskMax: 4, Attention: model.AttnEyes}
			if !reflect.DeepEqual(b, want) {
				t.Errorf("b\n got %+v\nwant %+v", b, want)
			}
			n, _ := moduleByID(g, "n")
			if n.Attention != model.AttnSkim || n.Undocumented != 1 || n.FileCount != 1 || n.Owned {
				t.Errorf("n %+v", n)
			}
			old, _ := moduleByID(g, "old")
			if old.Attention != model.AttnSkip || old.Breaking != 1 || old.FileCount != 1 || !reflect.DeepEqual(old.Files, []string{"old/o.ts"}) {
				t.Errorf("old %+v", old)
			}
			c, _ := moduleByID(g, "c")
			if c.Attention != "" || c.Units != 0 || c.FileCount != 1 || c.Files != nil {
				t.Errorf("context c %+v", c)
			}
		}},
		{"contracts on edges into b", nil, func(t *testing.T, g *model.ArchGraph) {
			for _, e := range g.Edges {
				want := []string(nil)
				if e.To == "b" {
					want = []string{"getB · breaking", "getB · signature"}
				}
				if !reflect.DeepEqual(e.Contracts, want) {
					t.Errorf("%s→%s contracts %v, want %v", e.From, e.To, e.Contracts, want)
				}
			}
		}},
		{"net effect", nil, func(t *testing.T, g *model.ArchGraph) {
			want := []string{"new module n", "old removed", "b now imports n", "and 3 more import changes"}
			if !reflect.DeepEqual(g.NetEffect, want) {
				t.Errorf("net effect %q", g.NetEffect)
			}
		}},
		{"net effect without the cap", func(in *Input) {
			in.Files = []model.File{{Path: "b/b.ts"}}
		}, func(t *testing.T, g *model.ArchGraph) {
			want := []string{"b now imports n", "n now imports c", "b no longer imports c", "old no longer imports b"}
			if !reflect.DeepEqual(g.NetEffect, want) {
				t.Errorf("net effect %q", g.NetEffect)
			}
		}},
		{"head unreadable uses base plus the diff", func(in *Input) {
			in.Src = unreadable{Source: repo(), rev: "head"}
		}, func(t *testing.T, g *model.ArchGraph) {
			if !strings.Contains(g.Note, "head not readable") {
				t.Errorf("note %q", g.Note)
			}
			want := []string{"a→b same 1", "b→c same 1", "d→c same 1", "old→b same 1"}
			if got := edgeList(g); !reflect.DeepEqual(got, want) {
				t.Errorf("edges %v", got)
			}
			for id, st := range map[string]string{"b": model.ArchChanged, "n": model.ArchNew, "old": model.ArchRemoved} {
				if m, _ := moduleByID(g, id); m.Status != st {
					t.Errorf("%s status %q, want %q", id, m.Status, st)
				}
			}
			if len(g.NetEffect) != 2 {
				t.Errorf("net effect %q", g.NetEffect)
			}
		}},
		{"nothing readable keeps the touched modules", func(in *Input) {
			in.Src = unreadable{Source: unreadable{Source: repo(), rev: "head"}, rev: "base"}
		}, func(t *testing.T, g *model.ArchGraph) {
			if !strings.Contains(g.Note, "import analysis unavailable") || len(g.Edges) != 0 {
				t.Errorf("note %q edges %v", g.Note, edgeList(g))
			}
			want := []string{"b changed h0", "n new h0", "old removed h0"}
			if got := moduleList(g); !reflect.DeepEqual(got, want) {
				t.Errorf("modules %v", got)
			}
		}},
		{"no aggregate: git facts only", func(in *Input) {
			in.Agg = nil
			in.Units = []model.ChangeUnit{{ID: "x", Path: "b/b.ts"}, {ID: "y", Path: "b/b.ts"}}
		}, func(t *testing.T, g *model.ArchGraph) {
			b, _ := moduleByID(g, "b")
			if b.Units != 2 || b.Attention != "" || b.Eyes != 0 {
				t.Errorf("b %+v", b)
			}
			for _, e := range g.Edges {
				if len(e.Contracts) > 0 {
					t.Errorf("contracts without labels: %+v", e)
				}
			}
		}},
		{"only non-source files", func(in *Input) {
			in.Files = []model.File{{Path: "README.md"}, {Path: "web/package.json"}}
		}, func(t *testing.T, g *model.ArchGraph) {
			if len(g.Modules) != 0 || g.Note != "no source modules touched" {
				t.Errorf("modules %v note %q", moduleList(g), g.Note)
			}
		}},
		{"rename touches both modules", func(in *Input) {
			in.Files = []model.File{{Path: "c/moved.ts", OldPath: "old/o.ts", Renamed: true}}
		}, func(t *testing.T, g *model.ArchGraph) {
			old, _ := moduleByID(g, "old")
			c, _ := moduleByID(g, "c")
			if old.Hop != 0 || c.Hop != 0 || !reflect.DeepEqual(old.Files, []string{"c/moved.ts"}) {
				t.Errorf("old %+v c %+v", old, c)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := input()
			if c.edit != nil {
				c.edit(&in)
			}
			g, err := Build(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, g)
			again, err := Build(context.Background(), in)
			if err != nil || !reflect.DeepEqual(g, again) {
				t.Errorf("Build is not deterministic (err %v)", err)
			}
		})
	}
}

func TestBuildOrdersByAttention(t *testing.T) {
	in := input()
	in.Agg.Units[0].Attention = model.AttnSkip // b: skim (u2) now
	in.Agg.Units[2].Attention = model.AttnEyes // n: eyes
	g, err := Build(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"n new h0", "b changed h0", "old removed h0", "a context h1", "c context h1", "d context h2"}
	if got := moduleList(g); !reflect.DeepEqual(got, want) {
		t.Errorf("order %v", got)
	}
}

func TestBuildCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Build(ctx, input()); !errors.Is(err, context.Canceled) {
		t.Errorf("err %v, want context.Canceled", err)
	}
	// Cancelled while reading: the listing sees the cancellation.
	ctx, cancel = context.WithCancel(context.Background())
	in := input()
	in.Src = cancelOnList{Source: repo(), cancel: cancel}
	if _, err := Build(ctx, in); !errors.Is(err, context.Canceled) {
		t.Errorf("mid-read err %v, want context.Canceled", err)
	}
}

// cancelOnList cancels the context when the tree is listed.
type cancelOnList struct {
	Source
	cancel context.CancelFunc
}

func (c cancelOnList) ListFiles(ctx context.Context, rev string) ([]string, error) {
	c.cancel()
	return nil, ctx.Err()
}

// memCache is an in-memory Cache.
type memCache struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (c *memCache) CacheGet(_ context.Context, ns, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.m[ns+"/"+key]
	return b, ok, nil
}

func (c *memCache) CachePut(_ context.Context, ns, key string, val []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string][]byte{}
	}
	c.m[ns+"/"+key] = val
	return nil
}

// countShows counts Show calls.
type countShows struct {
	Source
	mu sync.Mutex
	n  int
}

func (c *countShows) Show(ctx context.Context, rev, p string) ([]byte, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.Source.Show(ctx, rev, p)
}

func TestBuildUsesCache(t *testing.T) {
	src := &countShows{Source: repo()}
	in := input()
	in.Src, in.Cache = src, &memCache{}
	first, err := Build(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	n := src.n
	second, err := Build(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if src.n != n || !reflect.DeepEqual(first, second) {
		t.Errorf("second build read %d files", src.n-n)
	}
}
