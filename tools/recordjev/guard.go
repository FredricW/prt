package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/fredricw/prt/internal/jev"
)

// ErrBudget is returned (instead of calling Jev) when a call could push the
// cumulative Jev spend over the cap. It is not jev.ErrUnavailable, so the
// labels stage fails and the tool exits non-zero.
var ErrBudget = errors.New("recordjev: Jev budget cap reached")

// firstCallReserve is the reserve assumed before any call has been priced.
const firstCallReserve = 0.01

// guard sits in front of the recording client. For each request it:
//
//  1. serves an existing recording from Dir without calling (no spend);
//     with acceptSynthetic false (live mode), synthetic recordings do not
//     count, so they are replaced by real ones;
//  2. refuses with ErrBudget when prior + spent + reserve × (in flight + 1)
//     exceeds capUSD, where reserve is the largest single-call cost seen in
//     this run (firstCallReserve before the first priced call);
//  3. otherwise calls next (a jev.Recorder) and adds the response's
//     gateway cost to spent.
type guard struct {
	next            jev.Client
	dir             string
	capUSD          float64
	prior           float64 // cumulative Jev spend before this run (ledger)
	acceptSynthetic bool

	mu       sync.Mutex
	spent    float64
	maxCall  float64
	inflight int
	calls    int // calls made to next
	reused   int // requests served from existing recordings
}

var _ jev.Client = (*guard)(nil)

// Evaluate implements jev.Client.
func (g *guard) Evaluate(ctx context.Context, req jev.Request) (jev.Response, error) {
	key, err := jev.RequestKey(req)
	if err != nil {
		return jev.Response{}, fmt.Errorf("recordjev: request key: %w", err)
	}
	if resp, ok := g.recorded(key); ok {
		g.mu.Lock()
		g.reused++
		g.mu.Unlock()
		return resp, nil
	}
	if err := g.reserve(key); err != nil {
		return jev.Response{}, err
	}
	resp, err := g.next.Evaluate(ctx, req)
	g.settle(resp, err)
	return resp, err
}

// recorded returns the stored response for key, if a usable recording exists.
func (g *guard) recorded(key string) (jev.Response, bool) {
	b, err := os.ReadFile(filepath.Join(g.dir, key+".json"))
	if err != nil {
		return jev.Response{}, false
	}
	var rec jev.Recording
	if err := json.Unmarshal(b, &rec); err != nil {
		return jev.Response{}, false
	}
	if rec.Synthetic && !g.acceptSynthetic {
		return jev.Response{}, false
	}
	return rec.Response, true
}

// reserve checks the budget and counts the call as in flight.
func (g *guard) reserve(key string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.maxCall
	if r == 0 {
		r = firstCallReserve
	}
	if g.prior+g.spent+r*float64(g.inflight+1) > g.capUSD {
		return fmt.Errorf("%w: request %s not sent: spent $%.4f before this run + $%.4f this run + reserve $%.4f > cap $%.2f",
			ErrBudget, key, g.prior, g.spent, r*float64(g.inflight+1), g.capUSD)
	}
	g.inflight++
	return nil
}

// settle records a finished call.
func (g *guard) settle(resp jev.Response, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight--
	if err != nil {
		return
	}
	g.calls++
	c := resp.CostUSD()
	g.spent += c
	g.maxCall = max(g.maxCall, c)
}

// totals returns calls made, requests reused and the spend of this run.
func (g *guard) totals() (calls, reused int, spent float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls, g.reused, g.spent
}
