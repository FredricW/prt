package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/store"
)

// Cache namespaces owned by the pipeline (Store.CacheGet/CachePut).
const (
	// nsOverride holds a group's user tier override (D/R → deep, s → none),
	// key "<itemID>|<groupID>", value groupOverride.
	nsOverride = "route-override"
	// nsCallLog holds the raw LLM calls behind one routing row, key
	// "<itemID>|<groupID>" ("<itemID>|" for the TL;DR), value
	// []model.ReviewCall in call order.
	nsCallLog = "review-calls"
	// nsTitle holds group titles by review.RequestKey, value proseCache.
	nsTitle = "group-title"
	// nsTLDR holds TL;DRs by review.RequestKey, value proseCache.
	nsTLDR = "tldr"
)

// Call purposes (model.ReviewCall.Purpose).
const (
	purposeRouted      = "routed"
	purposeEscalation  = "escalation"
	purposeCalibration = "calibration"
	purposeRetry       = "retry"
	purposeTLDR        = "tldr"
	purposeTitle       = "title"
)

// rowKey is the cache key of a routing row (group "" is the item's TL;DR).
func rowKey(itemID, groupID string) string { return itemID + "|" + groupID }

// proseCache is a cached prose answer (group title, TL;DR): the cleaned
// text and the call that produced it, so a cache hit can be shown in the
// call log with what it cost when it ran.
type proseCache struct {
	Text string           `json:"text"`
	Call model.ReviewCall `json:"call"`
}

// callLog collects one run's raw LLM calls per routing row (group id; ""
// for the TL;DR). It is safe for concurrent use.
type callLog struct {
	mu      sync.Mutex
	byGroup map[string][]model.ReviewCall
}

func newCallLog() *callLog { return &callLog{byGroup: map[string][]model.ReviewCall{}} }

// add appends calls to a row's log.
func (c *callLog) add(groupID string, calls ...model.ReviewCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byGroup[groupID] = append(c.byGroup[groupID], calls...)
}

// get returns a copy of a row's calls.
func (c *callLog) get(groupID string) []model.ReviewCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.ReviewCall(nil), c.byGroup[groupID]...)
}

// flush stores the log of every row in groupIDs (an empty log for a row
// without calls this run, so a stale log never outlives its row).
func (c *callLog) flush(ctx context.Context, st store.Store, itemID string, groupIDs []string) error {
	for _, g := range groupIDs {
		calls := c.get(g)
		if calls == nil {
			calls = []model.ReviewCall{}
		}
		b, err := json.Marshal(calls)
		if err != nil {
			return fmt.Errorf("pipeline: encode call log: %w", err)
		}
		if err := st.CachePut(ctx, nsCallLog, rowKey(itemID, g), b); err != nil {
			return fmt.Errorf("pipeline: store call log: %w", err)
		}
	}
	return nil
}

// cost sums the cost of a row's calls, skipping the given purposes.
func (c *callLog) cost(groupID string, skip ...string) float64 {
	total := 0.0
	for _, call := range c.get(groupID) {
		if !contains(skip, call.Purpose) {
			total += call.Cost
		}
	}
	return total
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// CallLog returns the raw LLM calls behind a routing-table row as the last
// run stored them (prompt, context, response, tokens, cost; cache hits are
// marked Cached). groupID "" returns the item's TL;DR calls. A row without
// a stored log returns no calls.
func (p *Pipeline) CallLog(ctx context.Context, itemID, groupID string) ([]model.ReviewCall, error) {
	return storedCalls(ctx, p.d.Store, itemID, groupID)
}

func storedCalls(ctx context.Context, st store.Store, itemID, groupID string) ([]model.ReviewCall, error) {
	b, ok, err := st.CacheGet(ctx, nsCallLog, rowKey(itemID, groupID))
	if err != nil {
		return nil, fmt.Errorf("pipeline: load call log %s %s: %w", itemID, groupID, err)
	}
	if !ok || len(b) == 0 {
		return nil, nil
	}
	var calls []model.ReviewCall
	if err := json.Unmarshal(b, &calls); err != nil {
		return nil, fmt.Errorf("pipeline: decode call log %s %s: %w", itemID, groupID, err)
	}
	return calls, nil
}

// loadProse reads a cached prose answer (ok false when absent or
// unreadable: a bad entry is just asked again).
func (p *Pipeline) loadProse(ctx context.Context, ns, key string) (proseCache, bool) {
	b, ok, err := p.d.Store.CacheGet(ctx, ns, key)
	if err != nil || !ok {
		return proseCache{}, false
	}
	var pc proseCache
	if json.Unmarshal(b, &pc) != nil {
		return proseCache{}, false
	}
	return pc, true
}

// putProse stores a prose answer; a failure is only logged (the answer is
// asked again next run).
func (p *Pipeline) putProse(ctx context.Context, ns, key string, pc proseCache) {
	b, err := json.Marshal(pc)
	if err == nil {
		err = p.d.Store.CachePut(ctx, ns, key, b)
	}
	if err != nil {
		p.d.Log.Warn("store prose cache failed", "ns", ns, "err", err)
	}
}
