package main

import (
	"context"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/log"
)

// --offline never probes Ollama: the settings page shows it "not checked".
func TestSettingsDepsOffline(t *testing.T) {
	if sd := settingsDeps(options{offline: true}); sd.Prober != nil {
		t.Errorf("offline settings deps probe Ollama: %T", sd.Prober)
	}
	if sd := settingsDeps(options{}); sd.Prober == nil || sd.Models == nil || sd.Getenv == nil {
		t.Errorf("online settings deps = %+v, want a prober, a model factory and getenv", sd)
	}
}

// fakeWork records the daemon loop's calls.
type fakeWork struct {
	mu    sync.Mutex
	calls []string
	tick  chan struct{}
}

func (f *fakeWork) Discover(context.Context) error {
	f.mu.Lock()
	f.calls = append(f.calls, "discover")
	f.mu.Unlock()
	return nil
}

func (f *fakeWork) AnalysePending(context.Context) error {
	f.mu.Lock()
	f.calls = append(f.calls, "pending")
	f.mu.Unlock()
	select {
	case f.tick <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeWork) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// The daemon loop discovers, then queues pending analysis, at once and on
// every tick, and returns when its context ends.
func TestDiscoverLoop(t *testing.T) {
	w := &fakeWork{tick: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		discoverLoop(ctx, w, 5*time.Millisecond, log.New(io.Discard))
		close(done)
	}()
	for range 3 {
		select {
		case <-w.tick:
		case <-time.After(5 * time.Second):
			t.Fatal("loop did not tick")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop on cancel")
	}
	calls := w.snapshot()
	if len(calls) < 6 {
		t.Fatalf("calls = %v, want at least three passes", calls)
	}
	for i := 0; i+1 < len(calls); i += 2 {
		if calls[i] != "discover" || calls[i+1] != "pending" {
			t.Fatalf("calls = %v, want discover then pending on every pass", calls)
		}
	}

	// interval <= 0 runs one pass.
	once := &fakeWork{tick: make(chan struct{}, 1)}
	discoverLoop(context.Background(), once, 0, log.New(io.Discard))
	if got := once.snapshot(); !slices.Equal(got, []string{"discover", "pending"}) {
		t.Errorf("one pass = %v", got)
	}
}
