package review

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestRequestKeyDeterminism(t *testing.T) {
	a := BuildReview(sampleInput(model.TierFast))
	b := BuildReview(sampleInput(model.TierFast))
	ka, err := RequestKey("anthropic:claude-haiku-5-5", a)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := RequestKey("anthropic:claude-haiku-5-5", b)
	if ka != kb || len(ka) != 24 {
		t.Fatalf("keys %q %q", ka, kb)
	}
	if kd, _ := RequestKey("anthropic:claude-opus-5-5", a); kd == ka {
		t.Error("model spec not in the key")
	}
	b.Units = nil
	if k, _ := RequestKey("anthropic:claude-haiku-5-5", b); k != ka {
		t.Error("Units changed the key")
	}
	b.Prompt += " "
	if k, _ := RequestKey("anthropic:claude-haiku-5-5", b); k == ka {
		t.Error("prompt not in the key")
	}
}

func TestRecorderReplayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	live := &Fake{Name: "anthropic:claude-haiku-5-5", Respond: FakeRespond(0.9)}
	rec := &Recorder{Live: live, Dir: dir}
	req := BuildReview(sampleInput(model.TierFast))
	want, err := rec.Review(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(want.Findings) != 1 || want.Findings[0].UnitID != "u-client" || want.Confidence != 0.9 {
		t.Fatalf("fake result %+v", want)
	}
	tldr := BuildTLDR(sampleAgg())
	if _, err := rec.Review(ctx, tldr); err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 2 {
		t.Fatalf("%d recordings", len(files))
	}
	r := &Replay{Name: live.Model(), Dir: dir}
	got, err := r.Review(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != want.Text || got.Verdict != want.Verdict || len(got.Findings) != 1 || got.Findings[0] != want.Findings[0] {
		t.Errorf("replayed %+v, recorded %+v", got, want)
	}
	// Units are not part of the key: replay anchors to the current units.
	req.Units = nil
	got, err = r.Review(ctx, req)
	if err != nil || got.Findings[0].UnitID != "" {
		t.Errorf("re-anchored without units: %+v %v", got.Findings, err)
	}
	if got, err := r.Review(ctx, tldr); err != nil || got.Text != FakeTLDR {
		t.Errorf("tldr replay %+v %v", got, err)
	}
	rc, ok, err := LoadRecording(dir, live.Model(), tldr)
	if err != nil || !ok || rc.Model != live.Model() || rc.Request.Task != TaskTLDR {
		t.Errorf("LoadRecording %+v %v %v", rc, ok, err)
	}
}

func TestReplayMiss(t *testing.T) {
	dir := t.TempDir()
	req := Request{Task: TaskTLDR, Prompt: "never recorded"}
	_, err := (&Replay{Name: "m", Dir: dir}).Review(context.Background(), req)
	if !errors.Is(err, ErrNoRecording) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("strict miss: %v", err)
	}
	_, err = (&Replay{Name: "m", Dir: dir, MissUnavailable: true}).Review(context.Background(), req)
	if !errors.Is(err, ErrNoRecording) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("degrading miss: %v", err)
	}
	if _, ok, err := LoadRecording(dir, "m", req); ok || err != nil {
		t.Fatalf("LoadRecording miss: %v %v", ok, err)
	}
	p, _ := RecordingPath(dir, "m", req)
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Replay{Name: "m", Dir: dir}).Review(context.Background(), req); err == nil {
		t.Fatal("corrupt recording accepted")
	}
}

func TestRecorderSkipsFailures(t *testing.T) {
	dir := t.TempDir()
	rec := &Recorder{Live: &Fake{Respond: func(Request) (Result, error) { return Result{Cost: 0.1}, ErrBadResponse }}, Dir: dir}
	if _, err := rec.Review(context.Background(), Request{Prompt: "x"}); !errors.Is(err, ErrBadResponse) {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 0 {
		t.Fatalf("failed call recorded: %v", files)
	}
}

// TestRecordingHasNoKey records a real-adapter call (against a local
// server) and checks neither the recording nor any error carries the key.
func TestRecordingHasNoKey(t *testing.T) {
	dir := t.TempDir()
	a := newTestAnthropic(t, "claude-haiku-5-5", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, messagesReply("Title words here", 50, 4, "end_turn"))
	})
	rec := &Recorder{Live: a, Dir: dir}
	if _, err := rec.Review(context.Background(), BuildGroupTitle(model.Group{ID: "g"}, sampleUnits())); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("%d recordings", len(files))
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	assertNoKey(t, string(b))
}
