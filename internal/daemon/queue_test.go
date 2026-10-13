package daemon

import (
	"context"
	"sync"
	"testing"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
)

// A routing-reload job merged with any other job for the same item loses
// keepReviewed: the other job routes every group by the rules.
func TestQueueMergeKeepReviewed(t *testing.T) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	release := make(chan struct{})
	var ran []job
	q := newQueue(1, &wg, func(j job) {
		if j.itemID == "block" {
			<-release
		}
		mu.Lock()
		ran = append(ran, j)
		mu.Unlock()
	})
	q.push(job{itemID: "block"})
	q.push(job{itemID: "a", keepReviewed: true}, job{itemID: "b", keepReviewed: true})
	q.push(job{itemID: "a"}, job{itemID: "b", keepReviewed: true})
	close(release)
	wg.Wait()
	got := map[string]bool{}
	for _, j := range ran {
		got[j.itemID] = j.keepReviewed
	}
	if len(ran) != 3 || got["a"] || !got["b"] {
		t.Errorf("ran %+v; want a merged without keepReviewed, b with it", ran)
	}
}

// blockingQueue returns a one-slot queue whose jobs for item "busy" wait
// for release, and the record of jobs run.
func blockingQueue(t *testing.T) (*queue, chan struct{}, func() []job, *sync.WaitGroup) {
	t.Helper()
	var wg sync.WaitGroup
	var mu sync.Mutex
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	var ran []job
	q := newQueue(1, &wg, func(j job) {
		if j.itemID == "busy" {
			started <- struct{}{}
			<-release
		}
		mu.Lock()
		ran = append(ran, j)
		mu.Unlock()
	})
	q.push(job{itemID: "busy"})
	<-started
	return q, release, func() []job {
		mu.Lock()
		defer mu.Unlock()
		return append([]job(nil), ran...)
	}, &wg
}

// A job the user asked for (search-and-add, preview) starts before every
// waiting automatic job, however urgent (M5 review finding).
func TestQueueUserJobsStartFirst(t *testing.T) {
	q, release, ran, wg := blockingQueue(t)
	q.push(job{itemID: "hot", urgency: 9}, job{itemID: "warm", urgency: 5})
	q.push(job{itemID: "added", urgency: 0.1, user: true})
	// A later automatic job for a waiting user job keeps it first.
	q.push(job{itemID: "added", urgency: 0.1})
	close(release)
	wg.Wait()
	var order []string
	for _, j := range ran() {
		order = append(order, j.itemID)
	}
	if len(order) != 4 || order[1] != "added" || order[2] != "hot" || order[3] != "warm" {
		t.Errorf("order = %v, want busy added hot warm", order)
	}
}

func TestQueueMergeAggregateOnly(t *testing.T) {
	cases := []struct {
		name    string
		pushes  [][]job
		wantAgg map[string]bool // item -> aggOnly of its single run
	}{
		{
			name:    "agg-only with full becomes full",
			pushes:  [][]job{{{itemID: "a", aggOnly: true, rerun: true}}, {{itemID: "a"}}},
			wantAgg: map[string]bool{"a": false},
		},
		{
			name:    "full with agg-only stays full",
			pushes:  [][]job{{{itemID: "a"}}, {{itemID: "a", aggOnly: true, rerun: true}}},
			wantAgg: map[string]bool{"a": false},
		},
		{
			name:    "two agg-only merge into one",
			pushes:  [][]job{{{itemID: "a", aggOnly: true, rerun: true}}, {{itemID: "a", aggOnly: true, rerun: true}}},
			wantAgg: map[string]bool{"a": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, release, ran, wg := blockingQueue(t)
			for _, js := range tc.pushes {
				q.push(js...)
			}
			close(release)
			wg.Wait()
			got := map[string][]bool{}
			for _, j := range ran() {
				if j.itemID != "busy" {
					got[j.itemID] = append(got[j.itemID], j.aggOnly)
				}
			}
			for id, want := range tc.wantAgg {
				if len(got[id]) != 1 || got[id][0] != want {
					t.Errorf("%s ran with aggOnly %v; want one run with %v", id, got[id], want)
				}
			}
		})
	}
}

// An aggregate-only job behind a running full run of the same item runs
// after it; a plain job behind a running aggregate-only run is not dropped.
func TestQueueAggregateOnlyFollowsRunningRun(t *testing.T) {
	q, release, ran, wg := blockingQueue(t)
	q.push(job{itemID: "busy", aggOnly: true, rerun: true})
	close(release)
	wg.Wait()
	js := ran()
	if len(js) != 2 || js[0].aggOnly || !js[1].aggOnly {
		t.Fatalf("ran %+v; want the full run then the aggregate-only one", js)
	}

	var wg2 sync.WaitGroup
	release2 := make(chan struct{})
	started := make(chan struct{}, 2)
	var mu sync.Mutex
	var ran2 []job
	q2 := newQueue(1, &wg2, func(j job) {
		if j.aggOnly {
			started <- struct{}{}
			<-release2
		}
		mu.Lock()
		ran2 = append(ran2, j)
		mu.Unlock()
	})
	q2.push(job{itemID: "x", aggOnly: true, rerun: true})
	<-started
	q2.push(job{itemID: "x"})
	close(release2)
	wg2.Wait()
	if len(ran2) != 2 || ran2[1].aggOnly {
		t.Errorf("ran %+v; want the aggregate-only run then a full run", ran2)
	}
}

func TestJobOptions(t *testing.T) {
	j := job{force: true, fromStage: model.StageJev, keepReviewed: true, aggOnly: true}
	want := pipeline.RunOptions{Force: true, FromStage: model.StageJev, KeepReviewed: true, AggregateOnly: true}
	if got := j.options(); got != want {
		t.Errorf("options = %+v, want %+v", got, want)
	}
	if got := (job{}).options(); got != (pipeline.RunOptions{}) {
		t.Errorf("zero job options = %+v", got)
	}
}

// An override follows the unit: every other analysed item holding it is
// re-aggregated too (M5 review finding).
func TestOverrideReaggregatesItemsSharingTheUnit(t *testing.T) {
	ctx := context.Background()
	c, st, jobs := commentClient(t, nil)
	shared := model.Item{ID: model.BranchItemID("feat/x"), Kind: model.KindBranch, Ref: "feat/x", AnalysedSHA: "h1", Source: model.SourceAdded, Section: model.SectionAdded}
	other := model.Item{ID: model.PRItemID(99), Kind: model.KindPR, Number: 99, AnalysedSHA: "h9", Source: model.SourceRanked}
	put(t, st, shared, other)
	if err := st.PutUnits(ctx, shared.ID, []model.ChangeUnit{{ID: "u1"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUnits(ctx, other.ID, []model.ChangeUnit{{ID: "u2"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Override(ctx, proto.OverrideParams{ItemID: cItem, Override: model.Override{UnitID: "u1", Question: "risk", Value: "2"}}); err != nil {
		t.Fatal(err)
	}
	js := jobs.waitJobs(t, 2)
	got := map[string]bool{}
	for _, j := range js {
		got[j.itemID] = j.aggOnly
	}
	if len(got) != 2 || !got[cItem] || !got[shared.ID] {
		t.Errorf("override jobs = %+v; want aggregate-only for %s and %s", js, cItem, shared.ID)
	}
}

func TestOverrideQueuesAggregateOnly(t *testing.T) {
	ctx := context.Background()
	c, st, jobs := commentClient(t, nil)
	if err := c.Override(ctx, proto.OverrideParams{ItemID: cItem, Override: model.Override{UnitID: "u1", Question: "risk", Value: "2"}}); err != nil {
		t.Fatal(err)
	}
	js := jobs.waitJobs(t, 1)
	if !js[0].aggOnly || !js[0].rerun || !js[0].options().AggregateOnly {
		t.Errorf("override job = %+v", js[0])
	}

	putComments(t, st, aiDraftComment())
	o := &model.Override{UnitID: "u1", Question: "risk", Value: "low"}
	if _, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: cItem, Action: proto.CommentDismiss, CommentID: "ai-k1", Override: o}); err != nil {
		t.Fatal(err)
	}
	js = jobs.waitJobs(t, 2)
	if !js[1].aggOnly || !js[1].options().AggregateOnly {
		t.Errorf("dismiss-with-override job = %+v", js[1])
	}
}
