package link

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
)

// mk builds a hand-made unit. lines are unified diff lines (with +/-/space
// prefixes) starting at oldStart/newStart; the ranges, counts and kind are
// derived from them like parse does.
func mk(name string, order int, path, sym string, oldStart, newStart int, lines ...string) model.ChangeUnit {
	u := model.ChangeUnit{
		ID: name, ItemID: "pr:1", FileID: path, Path: path, Symbol: sym, Order: order,
		Diff: strings.Join(lines, "\n"),
	}
	u.Range.Old.Start, u.Range.New.Start = oldStart, newStart
	for _, l := range lines {
		switch l[0] {
		case '+':
			u.Adds++
			u.Range.New.Count++
		case '-':
			u.Dels++
			u.Range.Old.Count++
		default:
			u.Range.New.Count++
			u.Range.Old.Count++
		}
	}
	switch {
	case u.Dels == 0:
		u.Kind = model.UnitAdded
	case u.Adds == 0:
		u.Kind = model.UnitDeleted
	default:
		u.Kind = model.UnitModified
	}
	return u
}

// fakeRepo returns a gitx.Fake with the given base and head trees.
func fakeRepo(base, head map[string]string) *gitx.Fake {
	f := &gitx.Fake{Files: map[string]map[string][]byte{"base": {}, "head": {}}}
	for p, s := range base {
		f.Files["base"][p] = []byte(s)
	}
	for p, s := range head {
		f.Files["head"][p] = []byte(s)
	}
	return f
}

// countingSource counts ListFiles and Show calls.
type countingSource struct {
	Source
	mu    sync.Mutex
	lists int
	shows int
}

func (c *countingSource) ListFiles(ctx context.Context, rev string) ([]string, error) {
	c.mu.Lock()
	c.lists++
	c.mu.Unlock()
	return c.Source.ListFiles(ctx, rev)
}

func (c *countingSource) Show(ctx context.Context, rev, p string) ([]byte, error) {
	c.mu.Lock()
	c.shows++
	c.mu.Unlock()
	return c.Source.Show(ctx, rev, p)
}

// mapCache is an in-memory Cache.
type mapCache struct {
	mu   sync.Mutex
	m    map[string][]byte
	puts int
}

func (c *mapCache) CacheGet(_ context.Context, ns, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.m[ns+"/"+key]
	return b, ok, nil
}

func (c *mapCache) CachePut(_ context.Context, ns, key string, val []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string][]byte{}
	}
	c.m[ns+"/"+key] = val
	c.puts++
	return nil
}

// describeLinks renders links as "kind from->to" (unit ids), in order.
func describeLinks(links []model.Link) []string {
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, fmt.Sprintf("%s %s->%s", l.Kind, l.From, l.To))
	}
	return out
}

// checkLinkInvariants asserts Source/Weight conventions, no self links, no
// duplicate (From, To, Kind) and the (From order, To order, Kind) sort.
func checkLinkInvariants(t *testing.T, units []model.ChangeUnit, links []model.Link) {
	t.Helper()
	order := map[string]int{}
	for _, u := range units {
		order[u.ID] = u.Order
	}
	seen := map[linkKey]bool{}
	for i, l := range links {
		if l.From == l.To {
			t.Errorf("self link %v", l)
		}
		k := linkKey{l.From, l.To, l.Kind}
		if seen[k] {
			t.Errorf("duplicate link %v", l)
		}
		seen[k] = true
		if l.Source != model.LinkStructural || l.Weight != 1 {
			t.Errorf("link %v: want structural weight 1", l)
		}
		if i == 0 {
			continue
		}
		p := links[i-1]
		if order[p.From] > order[l.From] ||
			order[p.From] == order[l.From] && (order[p.To] > order[l.To] || order[p.To] == order[l.To] && p.Kind > l.Kind) {
			t.Errorf("links out of order at %d: %v before %v", i, p, l)
		}
	}
}
