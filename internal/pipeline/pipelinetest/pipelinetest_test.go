package pipelinetest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
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

// TestRecordedReplay replays the committed Jev recordings (and embedding
// recordings, when present) over the fixture PRs. It never touches the
// network: a request without a recording fails the test and is listed.
//
// Transition rule (like M1's): while no Jev recording carries an M2
// question (relation, name_bullet, separate_pr), requests made only of M2
// questions answer jev.ErrUnavailable (pairs, names and split stay unknown)
// and the M2 grouping assertions are skipped with a loud log line; every
// other request replays strictly. Once any M2 recording exists, every miss
// fails.
func TestRecordedReplay(t *testing.T) {
	dir := testutil.RecordingsDir(t, "jev")
	recs, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(recs) == 0 {
		t.Skip("NO JEV RECORDINGS YET: run `make record-jev` (DECISIONS: M1 Jev recordings flow); replay assertions skipped")
	}
	haveM2 := hasM2Recordings(t, recs)
	var jc jev.Client = &jev.Replay{Dir: dir}
	if !haveM2 {
		jc = &m2Unavailable{next: jc}
	}
	replay := &missingKeys{next: jc}
	emb := embedReplay(t)
	outs, err := pipelinetest.FixtureRun(context.Background(), testutil.FixtureRepo(t), testutil.FixturePRs(t), replay, embedOption(emb))
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.keys) > 0 {
		t.Fatalf("%d requests have no recording in %s (re-record with `make record-jev`): %v", len(replay.keys), dir, replay.keys)
	}
	if emb != nil && len(emb.misses) > 0 {
		t.Fatalf("%d embedding texts have no recording (re-record with `make record-embed`): %v", len(emb.misses), emb.misses)
	}
	reportAll(t, pipelinetest.Problems(outs))
	reportAll(t, pipelinetest.Expectations(outs))
	if !haveM2 {
		t.Logf("NO M2 JEV RECORDINGS YET (no recorded request asks %s): pair, name and split requests answered unavailable; "+
			"GroupExpectations skipped. Integrator: run `make record-jev-dry`, then `make record-jev`.", strings.Join(m2Questions, ", "))
		return
	}
	reportAll(t, pipelinetest.GroupExpectations(outs))
}

// m2Questions are the M2 question ids (pair, group and file scope).
var m2Questions = []string{model.QRelation, model.QNameBullet, model.QSeparatePR}

// isM2Question reports whether a wire question name belongs to an M2
// question (the name or "<name>__<suffix>").
func isM2Question(name string) bool {
	base, _, _ := strings.Cut(name, "__")
	for _, q := range m2Questions {
		if base == q {
			return true
		}
	}
	return false
}

// hasM2Recordings reports whether any recording's request asks an M2
// question.
func hasM2Recordings(t *testing.T, paths []string) bool {
	t.Helper()
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var rec jev.Recording
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Fatalf("recording %s: %v", p, err)
		}
		for name := range rec.Request.Questions {
			if isM2Question(name) {
				return true
			}
		}
	}
	return false
}

// m2Unavailable answers requests made only of M2 questions with
// jev.ErrUnavailable and passes every other request to next.
type m2Unavailable struct{ next jev.Client }

func (m *m2Unavailable) Evaluate(ctx context.Context, req jev.Request) (jev.Response, error) {
	allM2 := len(req.Questions) > 0
	for name := range req.Questions {
		allM2 = allM2 && isM2Question(name)
	}
	if allM2 {
		return jev.Response{}, fmt.Errorf("%w: no M2 recordings yet (transition rule)", jev.ErrUnavailable)
	}
	return m.next.Evaluate(ctx, req)
}

// embedMisses wraps an embedder and records the texts it had no recording
// for (embed.ErrNoRecording), which the pipeline would otherwise quietly
// treat as "embeddings unavailable".
type embedMisses struct {
	next   embed.Embedder
	mu     sync.Mutex
	misses []string
}

func (e *embedMisses) Model() string { return e.next.Model() }

func (e *embedMisses) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	v, err := e.next.Embed(ctx, texts)
	if errors.Is(err, embed.ErrNoRecording) {
		e.mu.Lock()
		e.misses = append(e.misses, err.Error())
		e.mu.Unlock()
	}
	return v, err
}

// embedReplay returns a miss-tracking embed.Replay over
// testdata/recordings/embed, or nil when that directory has no recordings.
func embedReplay(t *testing.T) *embedMisses {
	t.Helper()
	dir := testutil.RecordingsDir(t, "embed")
	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) == 0 {
		t.Logf("no embedding recordings in %s: stage 3 is skipped (record with `make record-embed`)", dir)
		return nil
	}
	return &embedMisses{next: &embed.Replay{Dir: dir}}
}

// embedOption passes e to FixtureRun (no option when e is nil).
func embedOption(e *embedMisses) pipelinetest.Option {
	if e == nil {
		return pipelinetest.WithEmbedder(nil)
	}
	return pipelinetest.WithEmbedder(e)
}

// TestFixtureGrouping runs the fixture PRs with a deterministic fake Jev
// (pairs in one directory are "same intent", others "unrelated"; no group
// names; nothing should be split off) and recorded embeddings (embed.Fake
// when none are recorded), and checks the M2 grouping.
func TestFixtureGrouping(t *testing.T) {
	ctx := context.Background()
	emb := embedReplay(t)
	var e embed.Embedder = &embed.Fake{}
	if emb != nil {
		e = emb
	}
	outs, err := pipelinetest.FixtureRun(ctx, testutil.FixtureRepo(t), testutil.FixturePRs(t), &jev.Fake{Answer: groupingAnswer}, pipelinetest.WithEmbedder(e))
	if err != nil {
		t.Fatal(err)
	}
	if emb != nil && len(emb.misses) > 0 {
		t.Fatalf("%d embedding texts have no recording (re-record with `make record-embed`): %v", len(emb.misses), emb.misses)
	}
	reportAll(t, pipelinetest.Problems(outs))
	reportAll(t, pipelinetest.GroupExpectations(outs))
}

// groupingAnswer is TestFixtureGrouping's Jev: relation "same intent" (p
// .8) when both units share a directory, else "unrelated"; name_bullet
// "none"; separate_pr p .1; defaults for everything else.
func groupingAnswer(name string, q jev.Question, state any) jev.Answer {
	base, _, _ := strings.Cut(name, "__")
	switch base {
	case model.QRelation:
		want := model.RelUnrelated
		if ps := statePaths(state); len(ps) == 2 && path.Dir(ps[0]) == path.Dir(ps[1]) {
			want = model.RelSameIntent
		}
		return choice(q, want, 0.8)
	case model.QNameBullet:
		return choice(q, model.BulletNone, 0.9)
	case model.QSeparatePR:
		p := 0.1
		return jev.Answer{Type: jev.Boolean, Probability: &p}
	}
	return jev.DefaultAnswer(name, q, state)
}

// choice answers want with probability p, the rest spread over the other
// options of q.
func choice(q jev.Question, want string, p float64) jev.Answer {
	var opts []string
	switch c := q.Criteria.(type) {
	case map[string]string:
		for k := range c {
			opts = append(opts, k)
		}
	case map[string]any:
		for k := range c {
			opts = append(opts, k)
		}
	}
	probs := map[string]float64{want: p}
	for _, o := range opts {
		if o != want {
			probs[o] = (1 - p) / float64(max(1, len(opts)-1))
		}
	}
	return jev.Answer{Type: jev.Choice, Choice: want, Probabilities: probs}
}

// statePaths returns every "path" string in a request state, in JSON key
// order.
func statePaths(state any) []string {
	b, err := json.Marshal(state)
	if err != nil {
		return nil
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if s, ok := x[k].(string); ok && k == "path" {
					out = append(out, s)
				} else {
					walk(x[k])
				}
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
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
