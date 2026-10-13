package route

import (
	"math"
	"sync"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestPlan(t *testing.T) {
	ds := []Decision{
		{GroupID: "a", Tier: model.TierFast, HotSpot: 1},
		{GroupID: "b", Tier: model.TierDeep, HotSpot: 5},
		{GroupID: "c", Tier: model.TierNone, HotSpot: 9},
		{GroupID: "d", Tier: model.TierDeep, HotSpot: 5},
		{GroupID: "e", Tier: model.TierFast, HotSpot: 3},
	}
	est := map[string]float64{"a": 0.02, "b": 0.20, "c": 0.50, "d": 0.20, "e": 0.03}
	estimate := func(d Decision) float64 { return est[d.GroupID] }
	tests := []struct {
		name    string
		cap     float64
		spent   float64
		order   []string
		overCap map[string]string
	}{
		{
			name: "fits", cap: 1, order: []string{"c", "b", "d", "e", "a"},
		},
		{
			// b reserves 0.20 of 0.35, d (0.20) does not fit, the cheaper e
			// and a still do.
			name: "over cap, later cheaper fits", cap: 0.40, spent: 0.05,
			order:   []string{"c", "b", "d", "e", "a"},
			overCap: map[string]string{"d": "over cost cap: est $0.20, $0.15 of $0.40 left"},
		},
		{
			name: "no cap", cap: 0, spent: 3, order: []string{"c", "b", "d", "e", "a"},
		},
		{
			name: "everything over", cap: 0.40, spent: 0.40, order: []string{"c", "b", "d", "e", "a"},
			overCap: map[string]string{
				"b": "over cost cap: est $0.20, $0.00 of $0.40 left",
				"d": "over cost cap: est $0.20, $0.00 of $0.40 left",
				"e": "over cost cap: est $0.03, $0.00 of $0.40 left",
				"a": "over cost cap: est $0.02, $0.00 of $0.40 left",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := PlanInput{Decisions: ds, Estimate: estimate, CapUSD: tt.cap, Spent: tt.spent}
			got := Plan(in)
			if len(got) != len(tt.order) {
				t.Fatalf("len = %d", len(got))
			}
			for i, p := range got {
				if p.GroupID != tt.order[i] || p.Order != i {
					t.Errorf("pos %d = %s (order %d), want %s", i, p.GroupID, p.Order, tt.order[i])
				}
				note, over := tt.overCap[p.GroupID]
				if p.OverCap != over || p.QueueNote != note {
					t.Errorf("%s: overCap %v %q, want %v %q", p.GroupID, p.OverCap, p.QueueNote, over, note)
				}
				wantEst := est[p.GroupID]
				if p.Tier == model.TierNone {
					wantEst = 0
				}
				if p.EstCost != wantEst {
					t.Errorf("%s: est %v, want %v", p.GroupID, p.EstCost, wantEst)
				}
			}
			again := Plan(in)
			for i := range got {
				if got[i].GroupID != again[i].GroupID || got[i].OverCap != again[i].OverCap {
					t.Fatal("plan not deterministic")
				}
			}
		})
	}
}

func TestPlanExactFitAndNilEstimate(t *testing.T) {
	ds := []Decision{{GroupID: "a", Tier: model.TierFast}, {GroupID: "b", Tier: model.TierFast}}
	got := Plan(PlanInput{Decisions: ds, Estimate: func(Decision) float64 { return 0.1 }, CapUSD: 0.3, Spent: 0.1})
	if got[0].OverCap || got[1].OverCap {
		t.Errorf("exact fit marked over cap: %+v", got)
	}
	got = Plan(PlanInput{Decisions: ds, CapUSD: 0.1})
	if got[0].EstCost != 0 || got[0].OverCap {
		t.Errorf("nil estimate: %+v", got[0])
	}
}

func TestBudget(t *testing.T) {
	b := NewBudget(1, 0.25)
	if !b.Reserve(0.5) || b.Reserve(0.5) {
		t.Fatal("reserve")
	}
	if got := b.Remaining(); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("remaining = %v, want 0.25", got)
	}
	b.Settle(0.5, 0.1) // actual cheaper than estimate
	if got := b.Remaining(); math.Abs(got-0.65) > 1e-9 {
		t.Errorf("remaining = %v, want 0.65", got)
	}
	b.Settle(0, 2) // overspend clamps at 0
	if got := b.Remaining(); got != 0 {
		t.Errorf("remaining = %v, want 0", got)
	}
	u := NewBudget(0, 5)
	if !u.Reserve(100) || !math.IsInf(u.Remaining(), 1) {
		t.Error("unlimited budget")
	}
}

func TestBudgetConcurrent(t *testing.T) {
	b := NewBudget(20, 0)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Reserve(0.1) {
				mu.Lock()
				ok++
				mu.Unlock()
				b.Settle(0.1, 0.05)
			}
		}()
	}
	wg.Wait()
	if ok != 200 {
		t.Errorf("reserved %d, want 200 (actuals settle below the estimate)", ok)
	}
	if got := b.Remaining(); math.Abs(got-10) > 1e-6 {
		t.Errorf("remaining = %v, want 10", got)
	}
	c := NewBudget(1, 0)
	got := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.Reserve(0.1) {
				mu.Lock()
				got++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if got != 10 {
		t.Errorf("reserved %d of 0.1 against 1.0, want 10", got)
	}
}
