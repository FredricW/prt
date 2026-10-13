package daemon

import (
	"sync"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
)

// spendMeter turns the RunLog rows a client's runs emit into ledger spend,
// counting each stage's cost once (M5.3).
//
// The rule: a stage's final row (done, cached, failed, stopped or skipped)
// carries what the stage spent in this run (RunLog.Cost: Jev, embeddings,
// reviews, titles, TL;DR; a cached stage made no call and carries 0), and it
// is what is counted. Start rows (running) and progress rows
// (pipeline.IsProgress: the review stage's "k/n reviewed" with the running
// total so far) are never counted, so the review stage is counted once,
// from its final row. One exception: a stage that ends without its totals
// (the review stage stopped by the user or interrupted writes a final row
// with Cost 0, although the calls that finished were paid) is counted at
// its last progress row's cost; in general the final row counts the larger
// of its own cost and the last progress cost of that item and stage.
type spendMeter struct {
	mu sync.Mutex
	// running is the last progress cost per running stage (item, stage);
	// a stage's start row and final row clear it.
	running map[spendKey]float64
}

// spendKey is one stage of one item's run (the queue never runs one item
// twice at once).
type spendKey struct {
	itemID string
	stage  model.Stage
}

func newSpendMeter() *spendMeter { return &spendMeter{running: map[spendKey]float64{}} }

// addSpend records usd spent by this client's runs in its ledger under its
// repo key (nothing when usd ≤ 0).
func (c *InProcess) addSpend(usd float64) {
	if usd <= 0 {
		return
	}
	c.ws.mu.Lock()
	key, l := c.ws.repo.Key, c.ws.ledger
	c.ws.mu.Unlock()
	l.Add(key, usd)
}

// spent returns the cost rl adds to the ledger (see spendMeter).
func (m *spendMeter) spent(rl model.RunLog) float64 {
	k := spendKey{itemID: rl.ItemID, stage: rl.Stage}
	m.mu.Lock()
	defer m.mu.Unlock()
	if pipeline.IsProgress(rl) {
		m.running[k] = max(m.running[k], rl.Cost)
		return 0
	}
	switch rl.Status {
	case model.StageDone, model.StageCached, model.StageFailed, model.StageStopped, model.StageSkipped:
		cost := max(rl.Cost, m.running[k])
		delete(m.running, k)
		return cost
	case model.StageRunning:
		// A new run of the stage starts from nothing.
		delete(m.running, k)
	}
	return 0
}
