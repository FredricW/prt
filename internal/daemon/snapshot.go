package daemon

import (
	"context"
	"sync"

	"github.com/fredricw/prt/internal/model"
)

// Stale-while-revalidate (M5.2, the user's "show the old review with a
// small note while the new one runs"): when a full run of an item that has
// a complete analysis starts, the daemon keeps that analysis in memory and
// Detail serves it until the run's aggregate lands, instead of the mix of
// old and new parts the store holds mid-run.
//
// Lifetime: a snapshot is taken just before the pipeline starts writing
// and dropped when the run's aggregate lands, when any run of the item
// completes, or when the item is removed. A run that is stopped or fails
// leaves the store a mix, so its snapshot stays and is served (with
// Detail.Run nil: nothing is in flight) until a later run completes or the
// daemon restarts. Snapshots are in memory only and bounded (maxSnapshots,
// oldest dropped first).

// maxSnapshots bounds the analyses held for stale-while-revalidate: twice
// the default worker pool, so the runs in flight keep theirs while a few
// stopped or failed runs keep serving theirs.
const maxSnapshots = 2 * defaultConcurrency

// snapshots holds at most max analyses, one per item; safe for concurrent
// use. A held analysis is never modified (Detail clones what it serves).
type snapshots struct {
	mu   sync.Mutex
	max  int
	seq  uint64
	byID map[string]heldSnapshot
}

// heldSnapshot is a snapshot and when it was taken (seq: oldest first).
type heldSnapshot struct {
	a   *analysis
	seq uint64
}

func newSnapshots(maxHeld int) *snapshots {
	return &snapshots{max: max(maxHeld, 1), byID: map[string]heldSnapshot{}}
}

// get returns the item's snapshot (nil when none is held).
func (s *snapshots) get(id string) *analysis {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[id].a
}

// has reports whether the item has a snapshot.
func (s *snapshots) has(id string) bool {
	return s.get(id) != nil
}

// put holds a as the item's snapshot, dropping the oldest snapshots of
// other items beyond the bound.
func (s *snapshots) put(id string, a *analysis) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	s.byID[id] = heldSnapshot{a: a, seq: s.seq}
	for len(s.byID) > s.max {
		oldest, first := "", uint64(0)
		for k, h := range s.byID {
			if oldest == "" || h.seq < first {
				oldest, first = k, h.seq
			}
		}
		delete(s.byID, oldest)
	}
}

// drop forgets the item's snapshot.
func (s *snapshots) drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

// len is the number of snapshots held.
func (s *snapshots) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

// captureSnapshot holds the item's stored analysis before a full run
// rewrites it: only an item with a complete analysis (AnalysedSHA and a
// stored aggregate), and only when it has no snapshot yet (one left by a
// stopped or failed run is the last complete analysis; the store is a mix
// since). Errors are logged: the run then serves the store as it goes.
func (c *InProcess) captureSnapshot(ctx context.Context, it model.Item) {
	if it.AnalysedSHA == "" || c.snaps.has(it.ID) {
		return
	}
	a, err := c.loadAnalysis(ctx, it, true)
	if err != nil {
		c.log.Warn("snapshot of the stored analysis failed; serving the store during the run", "item", it.ID, "err", err)
		return
	}
	if a.agg == nil {
		return
	}
	c.snaps.put(it.ID, a)
}

// aggregateLanded reports whether rl is a run's aggregate row that stored
// a new aggregate: the item's analysis is complete again, so Detail stops
// serving the snapshot before the row's events tell screens to reload.
func aggregateLanded(rl model.RunLog) bool {
	return rl.Stage == model.StageAggregate && (rl.Status == model.StageDone || rl.Status == model.StageCached)
}
