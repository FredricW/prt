package route

import (
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/fredricw/prt/internal/model"
)

// costEpsilon absorbs float rounding when an estimate exactly fills the cap.
const costEpsilon = 1e-9

// Planned is one decision placed in the review queue.
type Planned struct {
	Decision
	Order   int     // dispatch position (hot-spot order, ties by group order)
	EstCost float64 // estimated cost of the routed call (0 for none / cached)
	// OverCap is set when the estimate does not fit the remaining cap;
	// the group stays queued (QueueNote) until the cap is raised.
	OverCap   bool
	QueueNote string
}

// PlanInput is the input of Plan.
type PlanInput struct {
	Decisions []Decision
	// Estimate returns the upper-bound cost of a decision's routed call
	// (0 when the review is cached or the tier is none).
	Estimate func(Decision) float64
	CapUSD   float64 // effective per-item cap
	Spent    float64 // already counted against the cap (e.g. TL;DR, titles)
}

// Plan orders decisions by hot spot (desc; ties keep input order) and
// reserves estimated cost against the cap in that order; decisions whose
// estimate does not fit are OverCap (a later, cheaper decision may still
// fit). Tier none costs nothing and is never OverCap; CapUSD ≤ 0 means no
// cap. Deterministic: estimates only, never wall-clock or actual cost.
func Plan(in PlanInput) []Planned {
	idx := make([]int, len(in.Decisions))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return in.Decisions[idx[a]].HotSpot > in.Decisions[idx[b]].HotSpot
	})
	b := NewBudget(in.CapUSD, in.Spent)
	out := make([]Planned, 0, len(idx))
	for order, i := range idx {
		d := in.Decisions[i]
		p := Planned{Decision: d, Order: order}
		if d.Tier != model.TierNone && in.Estimate != nil {
			p.EstCost = max(in.Estimate(d), 0)
		}
		if !b.Reserve(p.EstCost) {
			p.OverCap = true
			p.QueueNote = fmt.Sprintf("over cost cap: est $%.2f, $%.2f of $%.2f left", p.EstCost, b.Remaining(), in.CapUSD)
		}
		out = append(out, p)
	}
	return out
}

// Budget is a concurrency-safe running budget for the calls that follow
// the plan (escalations, calibration samples), checked with estimates in a
// fixed order by the caller.
type Budget interface {
	// Reserve takes est from the remaining cap; false when it does not fit.
	Reserve(est float64) bool
	// Settle replaces a reservation with the actual cost (or releases it
	// with actual 0 when the call failed before spending).
	Settle(est, actual float64)
	Remaining() float64
}

// budget implements Budget; capUSD ≤ 0 means unlimited.
type budget struct {
	mu       sync.Mutex
	capUSD   float64
	spent    float64
	reserved float64
}

// NewBudget returns a Budget with cap and an amount already spent. A cap
// ≤ 0 is unlimited: Reserve always succeeds and Remaining is +Inf.
func NewBudget(capUSD, spent float64) Budget {
	return &budget{capUSD: capUSD, spent: spent}
}

func (b *budget) Reserve(est float64) bool {
	est = max(est, 0)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.capUSD > 0 && b.spent+b.reserved+est > b.capUSD+costEpsilon {
		return false
	}
	b.reserved += est
	return true
}

func (b *budget) Settle(est, actual float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reserved = max(b.reserved-max(est, 0), 0)
	b.spent += max(actual, 0)
}

func (b *budget) Remaining() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.capUSD <= 0 {
		return math.Inf(1)
	}
	return max(b.capUSD-b.spent-b.reserved, 0)
}
