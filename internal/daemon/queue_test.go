package daemon

import (
	"sync"
	"testing"
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
