package daemon

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
)

// upsertCounter counts item writes.
type upsertCounter struct {
	store.Store
	n atomic.Int64
}

func (s *upsertCounter) UpsertItem(ctx context.Context, it model.Item) error {
	s.n.Add(1)
	return s.Store.UpsertItem(ctx, it)
}

func itemEvents(evs []proto.Event) int {
	n := 0
	for _, e := range evs {
		if e.Type == proto.EventItem {
			n++
		}
	}
	return n
}

// A discovery pass where only the clock moved writes nothing and sends no
// event until an item's age crosses a whole day (M5.2: it used to rewrite
// and announce every item on every refresh tick).
func TestDiscoveryClockOnlyChurn(t *testing.T) {
	ctx := context.Background()
	clock := &perfClock{t: testNow}
	st := &upsertCounter{Store: newTestStore(t)}
	ghc := &gh.Fake{Login: "me", PRs: map[int]gh.PR{
		7: {Number: 7, Title: "PR", HeadRef: "feat/pr", HeadSHA: "h7", BaseRef: "main", ReviewRequested: true,
			CreatedAt: testNow.Add(-50 * time.Hour), UpdatedAt: testNow.Add(-time.Hour)},
	}}
	g := branchGit("feat/y")
	g.RefList[0].CommitDate = testNow.Add(-30 * time.Hour)
	g.Ahead = map[string]int{"main..feat/y": 1}
	c := newClient(t, st, g, ghc, func(d *pipeline.Deps) { d.Now = clock.now })
	pass := func() (writes, events int) {
		t.Helper()
		drain(c.Events())
		before := st.n.Load()
		if err := c.Discover(ctx); err != nil {
			t.Fatal(err)
		}
		return int(st.n.Load() - before), itemEvents(drain(c.Events()))
	}
	if w, e := pass(); w != 2 || e != 2 {
		t.Fatalf("first pass: %d writes, %d item events; want both items stored", w, e)
	}
	first := itemMap(t, c)
	for _, step := range []struct {
		name         string
		advance      time.Duration
		writes, evts int
	}{
		{"clock unchanged", 0, 0, 0},
		{"one refresh tick", 60 * time.Second, 0, 0},
		{"an hour of ticks", time.Hour, 0, 0},
		{"a day later", 24 * time.Hour, 2, 2},
		{"next tick", 60 * time.Second, 0, 0},
	} {
		clock.advance(step.advance)
		if w, e := pass(); w != step.writes || e != step.evts {
			t.Errorf("%s: %d writes, %d item events; want %d, %d", step.name, w, e, step.writes, step.evts)
		}
	}
	later := itemMap(t, c)
	for id, it := range first {
		if later[id].Urgency <= it.Urgency {
			t.Errorf("%s: urgency %v after a day, was %v; the age term must still count", id, later[id].Urgency, it.Urgency)
		}
	}
	// A settings save forgets the days: the next pass stores what moved.
	c.resetUrgencyDays()
	clock.advance(60 * time.Second)
	if w, e := pass(); w != 2 || e != 2 {
		t.Errorf("after a settings reset: %d writes, %d item events; want the moved urgencies stored", w, e)
	}
	// A real change still writes at once.
	ghc.PRs[7] = func(pr gh.PR) gh.PR { pr.Title = "renamed"; return pr }(ghc.PRs[7])
	if w, e := pass(); w != 1 || e != 1 {
		t.Errorf("after a title change: %d writes, %d item events; want 1", w, e)
	}
}
