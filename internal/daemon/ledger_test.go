package daemon

import (
	"context"
	"sync"
	"testing"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// Concurrent adds and reads are race-free and sum exactly (binary
// fractions, so the order of the additions does not matter).
func TestLedgerConcurrent(t *testing.T) {
	l := NewLedger()
	var wg sync.WaitGroup
	for _, repo := range []string{"a", "b", "c"} {
		for range 50 {
			wg.Add(2)
			go func() { defer wg.Done(); l.Add(repo, 0.25) }()
			go func() { defer wg.Done(); _, _ = l.Total(), l.Repo(repo) }()
		}
	}
	wg.Wait()
	if l.Total() != 37.5 || l.Repo("a") != 12.5 || l.Repo("c") != 12.5 || l.Repo("none") != 0 {
		t.Errorf("total %v, a %v, c %v", l.Total(), l.Repo("a"), l.Repo("c"))
	}
	l.Add("a", 0)
	l.Add("a", -1)
	if l.Total() != 37.5 {
		t.Errorf("non-positive spend changed the total to %v", l.Total())
	}
}

// A nil ledger reads as empty and ignores adds; a zero one works.
func TestLedgerNilAndZero(t *testing.T) {
	var l *Ledger
	l.Add("a", 1)
	if l.Total() != 0 || l.Repo("a") != 0 {
		t.Errorf("nil ledger: total %v, a %v", l.Total(), l.Repo("a"))
	}
	var z Ledger
	z.Add("a", 0.5)
	if z.Total() != 0.5 || z.Repo("a") != 0.5 {
		t.Errorf("zero ledger: total %v, a %v", z.Total(), z.Repo("a"))
	}
}

// stageRows sets item id and stage s on rows.
func stageRows(id string, s model.Stage, rows ...model.RunLog) []model.RunLog {
	for i := range rows {
		rows[i].ItemID, rows[i].Stage = id, s
	}
	return rows
}

// rowStart is a stage's start row.
func rowStart() model.RunLog { return model.RunLog{Status: model.StageRunning} }

// rowProgress is a progress row (pipeline.IsProgress) with the running cost.
func rowProgress(cost float64) model.RunLog {
	return model.RunLog{Status: model.StageRunning, In: 3, Out: 1, Cost: cost}
}

// rowEnd is a stage's final row.
func rowEnd(st model.StageStatus, cost float64) model.RunLog {
	return model.RunLog{Status: st, Cost: cost}
}

// The spend meter counts each stage's final row once, never start or
// progress rows; a stage that ends without its totals (a stopped review)
// counts its last progress cost.
func TestSpendMeter(t *testing.T) {
	cases := []struct {
		name string
		rows []model.RunLog
		want float64
	}{
		{"start rows cost nothing", stageRows("i", model.StageJev, rowStart()), 0},
		{"done row", stageRows("i", model.StageJev, rowStart(), rowEnd(model.StageDone, 0.25)), 0.25},
		{"cached row", stageRows("i", model.StageJev, rowStart(), rowEnd(model.StageCached, 0)), 0},
		{"failed row keeps what it spent", stageRows("i", model.StageJev, rowStart(), rowEnd(model.StageFailed, 0.125)), 0.125},
		{"skipped row keeps what it spent", stageRows("i", model.StageReview, rowStart(), rowEnd(model.StageSkipped, 0.125)), 0.125},
		{"review counted once, from its final row",
			stageRows("i", model.StageReview, rowStart(), rowProgress(0.125), rowProgress(0.25), rowEnd(model.StageDone, 0.375)), 0.375},
		{"stopped review counts its last progress",
			stageRows("i", model.StageReview, rowStart(), rowProgress(0.125), rowProgress(0.5), rowEnd(model.StageStopped, 0)), 0.5},
		{"interrupted review counts its last progress",
			stageRows("i", model.StageReview, rowStart(), rowProgress(0.25), rowEnd(model.StageFailed, 0)), 0.25},
		{"a new run of the stage forgets the last one's progress",
			append(stageRows("i", model.StageReview, rowStart(), rowProgress(0.5)),
				stageRows("i", model.StageReview, rowStart(), rowEnd(model.StageDone, 0.125))...), 0.125},
		{"progress of another item is its own",
			append(stageRows("i", model.StageReview, rowStart(), rowProgress(0.5)),
				stageRows("j", model.StageReview, rowStart(), rowEnd(model.StageStopped, 0))...), 0},
		{"queued rows cost nothing", stageRows("i", model.StageReview, rowEnd(model.StageQueued, 1)), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newSpendMeter()
			got := 0.0
			for _, rl := range tc.rows {
				got += m.spent(rl)
			}
			if got != tc.want {
				t.Errorf("spent %v, want %v", got, tc.want)
			}
		})
	}
}

// A client's Emit wrapper adds each stage's cost once to the shared ledger
// under its repo key; Workspace and RepoInfo report it.
func TestEmitAddsStageCostsToLedger(t *testing.T) {
	ctx := context.Background()
	led := NewLedger()
	a := newPooledClient(t, Options{Ledger: led, Repo: proto.RepoInfo{Key: "acme/api"}}, nil)
	b := newPooledClient(t, Options{Ledger: led, Repo: proto.RepoInfo{Key: "acme/web"}}, nil)
	id := model.BranchItemID("x")
	var rows []model.RunLog
	rows = append(rows, stageRows(id, model.StageFetch, rowStart(), rowEnd(model.StageDone, 0))...)
	rows = append(rows, stageRows(id, model.StageJev, rowStart(), rowEnd(model.StageDone, 0.25))...)
	rows = append(rows, stageRows(id, model.StageCluster, rowStart(), rowEnd(model.StageCached, 0))...)
	rows = append(rows, stageRows(id, model.StageReview, rowStart(), rowProgress(0.125), rowProgress(0.25), rowEnd(model.StageDone, 0.375))...)
	rows = append(rows, stageRows(id, model.StageAggregate, rowStart(), rowEnd(model.StageDone, 0.0625))...)
	for _, rl := range rows {
		a.d.Emit(rl)
	}
	for _, rl := range stageRows(id, model.StageReview, rowStart(), rowProgress(0.5), rowEnd(model.StageStopped, 0)) {
		b.d.Emit(rl)
	}
	if got := led.Repo("acme/api"); got != 0.6875 {
		t.Errorf("acme/api spent %v, want 0.6875", got)
	}
	if got := led.Repo("acme/web"); got != 0.5 {
		t.Errorf("acme/web spent %v, want 0.5", got)
	}
	ws, err := a.Workspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ws.SpentUSD != 1.1875 || len(ws.Repos) != 1 || ws.Repos[0].SpentUSD != 0.6875 || b.RepoInfo().SpentUSD != 0.5 {
		t.Errorf("workspace %+v, B's repo %+v", ws, b.RepoInfo())
	}
}

// A single-repo client (NewInProcess) has a private ledger under key "".
func TestSingleRepoClientLedger(t *testing.T) {
	c := newClient(t, newTestStore(t), branchGit(), nil, nil)
	for _, rl := range stageRows("i", model.StageJev, rowStart(), rowEnd(model.StageDone, 0.5)) {
		c.d.Emit(rl)
	}
	ws, err := c.Workspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ws.Multi || ws.SpentUSD != 0.5 || len(ws.Repos) != 1 || ws.Repos[0].Key != "" || ws.Repos[0].SpentUSD != 0.5 {
		t.Errorf("workspace = %+v", ws)
	}
	if ws.Concurrency != slotsFor(c.cfg()) {
		t.Errorf("concurrency %d, want %d", ws.Concurrency, slotsFor(c.cfg()))
	}
}
