package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
)

var testNow = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func oneLineDiff(path string) string {
	return "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n@@ -1 +1,2 @@\n x\n+y\n"
}

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// newClient builds an InProcess over fakes; ghc may be nil (offline). The
// auto_review strategy is "all" (every run allowed, the pre-M5.1
// behaviour the older tests assume); the strategy tests set their own in
// mutate.
func newClient(t *testing.T, st store.Store, g gitx.Git, ghc gh.Client, mutate func(*pipeline.Deps)) *InProcess {
	t.Helper()
	d := pipeline.Deps{Git: g, Store: st, Config: config.Defaults(), Now: func() time.Time { return testNow }}
	d.Config.Pipeline.AutoReview = string(model.AutoAll)
	if ghc != nil {
		d.GH = ghc
	}
	if mutate != nil {
		mutate(&d)
	}
	c := NewInProcess(d)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// drain returns the events buffered so far without blocking.
func drain(ch <-chan proto.Event) []proto.Event {
	var out []proto.Event
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		default:
			return out
		}
	}
}

// waitEvent reads events until pred returns true, failing after 5 s or when
// the stream closes.
func waitEvent(t *testing.T, ch <-chan proto.Event, pred func(proto.Event) bool) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatal("event stream closed while waiting")
			}
			if pred(e) {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for event")
		}
	}
}

func put(t *testing.T, st store.Store, items ...model.Item) {
	t.Helper()
	for _, it := range items {
		if err := st.UpsertItem(context.Background(), it); err != nil {
			t.Fatal(err)
		}
	}
}

func itemMap(t *testing.T, c Client) map[string]model.Item {
	t.Helper()
	items, err := c.Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]model.Item{}
	for _, it := range items {
		m[it.ID] = it
	}
	return m
}
