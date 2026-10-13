package pipelinetest_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/pipeline/pipelinetest"
	"github.com/fredricw/prt/internal/testutil"
)

func reportAll(t *testing.T, problems []string) {
	t.Helper()
	for _, p := range problems {
		t.Error(p)
	}
}

// TestFixtureRoundTrip records the fixture run with a fake through
// jev.Recorder, then replays it: identical labels and aggregates prove the
// requests are deterministic, and no request is missing from the recording.
func TestFixtureRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := testutil.FixtureRepo(t)
	prs := testutil.FixturePRs(t)
	dir := t.TempDir()
	fake := &jev.Fake{}
	rec, err := pipelinetest.FixtureRun(ctx, repo, prs, &jev.Recorder{Live: fake, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	reportAll(t, pipelinetest.Problems(rec))
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) > len(fake.Calls) {
		t.Errorf("%d recordings for %d calls", len(files), len(fake.Calls))
	}

	rep, err := pipelinetest.FixtureRun(ctx, repo, prs, &jev.Replay{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	reportAll(t, pipelinetest.Problems(rep))
	for _, n := range pipelinetest.PRNumbers {
		a, b := rec[n], rep[n]
		if !reflect.DeepEqual(a.Labels, b.Labels) {
			t.Errorf("#%d: labels differ between record and replay (%d vs %d)", n, len(a.Labels), len(b.Labels))
		}
		if !reflect.DeepEqual(a.Agg, b.Agg) {
			t.Errorf("#%d: aggregates differ between record and replay", n)
		}
		for _, rl := range b.RunLogs {
			if strings.Contains(rl.Err, jev.ErrNoRecording.Error()) {
				t.Errorf("#%d: %s", n, rl.Err)
			}
		}
	}
}

// TestRecordedReplay replays the committed Jev recordings over the fixture
// PRs. It never touches the network: a request without a recording fails
// the test and is listed.
func TestRecordedReplay(t *testing.T) {
	dir := testutil.RecordingsDir(t, "jev")
	recs, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(recs) == 0 {
		t.Skip("NO JEV RECORDINGS YET: run `make record-jev` (DECISIONS: M1 Jev recordings flow); replay assertions skipped")
	}
	replay := &missingKeys{next: &jev.Replay{Dir: dir}}
	outs, err := pipelinetest.FixtureRun(context.Background(), testutil.FixtureRepo(t), testutil.FixturePRs(t), replay)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.keys) > 0 {
		t.Fatalf("%d requests have no recording in %s (re-record with `make record-jev`): %v", len(replay.keys), dir, replay.keys)
	}
	reportAll(t, pipelinetest.Problems(outs))
	reportAll(t, pipelinetest.Expectations(outs))
}

// missingKeys records the keys Replay could not serve.
type missingKeys struct {
	next jev.Client
	mu   sync.Mutex
	keys []string
}

func (m *missingKeys) Evaluate(ctx context.Context, req jev.Request) (jev.Response, error) {
	resp, err := m.next.Evaluate(ctx, req)
	if errors.Is(err, jev.ErrNoRecording) {
		key, _ := jev.RequestKey(req)
		m.mu.Lock()
		m.keys = append(m.keys, key)
		m.mu.Unlock()
	}
	return resp, err
}

// TestFixtureRunNeedsGitRepo: a bad repo dir is a setup error.
func TestFixtureRunNeedsGitRepo(t *testing.T) {
	_, err := pipelinetest.FixtureRun(context.Background(), t.TempDir(), testutil.FixturePRs(t), &jev.Fake{})
	if err == nil {
		t.Fatal("want error for a directory that is not a git repo")
	}
}

func TestTopShare(t *testing.T) {
	if got := pipelinetest.TopShare(map[string]float64{"b": 0.4, "a": 0.4, "c": 0.2}); got != "a" {
		t.Errorf("TopShare = %q", got)
	}
	if got := pipelinetest.TopShare(nil); got != "" {
		t.Errorf("TopShare(nil) = %q", got)
	}
}
