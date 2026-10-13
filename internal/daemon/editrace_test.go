package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
)

// editsStore blocks the first GroupEdits read (the cluster stage has
// started) until release is closed, and reports every read on reads.
type editsStore struct {
	store.Store
	once    sync.Once
	entered chan struct{}
	release chan struct{}
	reads   chan int
}

func (s *editsStore) GroupEdits(ctx context.Context, itemID string) ([]model.GroupEdit, error) {
	es, err := s.Store.GroupEdits(ctx, itemID)
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	s.reads <- len(es)
	return es, err
}

// A group edit made while the item is already in the cluster stage (edits
// read) must trigger another run that applies it, not be dropped by the
// queue's in-flight dedupe.
func TestEditDuringRunReruns(t *testing.T) {
	ctx := context.Background()
	st := &editsStore{Store: newTestStore(t), entered: make(chan struct{}), release: make(chan struct{}), reads: make(chan int, 4)}
	put(t, st, branchItem("a", 1))
	c := newClient(t, st, branchGit("a"), nil, nil)
	id := model.BranchItemID("a")
	if err := c.Analyse(ctx, false, id); err != nil {
		t.Fatal(err)
	}
	<-st.entered
	if err := c.EditGroups(ctx, proto.GroupEditParams{ItemID: id, Edit: model.GroupEdit{Op: model.EditSplit, Units: []string{"u1"}}}); err != nil {
		t.Fatal(err)
	}
	close(st.release)
	for want := 0; want < 2; want++ {
		select {
		case n := <-st.reads:
			if want == 1 && n != 1 {
				t.Fatalf("second run read %d edits, want 1", n)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d cluster runs; the edit's re-run was dropped", want)
		}
	}
}
