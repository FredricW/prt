package embed

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTextKey(t *testing.T) {
	a, b := TextKey("m1", "x"), TextKey("m2", "x")
	if len(a) != 24 || a == b || a != TextKey("m1", "x") || a == TextKey("m1", "y") {
		t.Fatalf("keys %q %q", a, b)
	}
}

func TestRecorderWritesOnlyMissingInOneCall(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := &Fake{}
	rec := &Recorder{Live: live, Dir: dir}
	if rec.Model() != live.Model() {
		t.Fatalf("model %q", rec.Model())
	}
	first, err := rec.Embed(ctx, []string{"alpha beta", "gamma", "alpha beta"})
	if err != nil {
		t.Fatal(err)
	}
	if live.Calls != 1 || live.Texts != 2 {
		t.Fatalf("live calls %d texts %d, want 1 call with 2 distinct texts", live.Calls, live.Texts)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 2 {
		t.Fatalf("%d files, want 2", len(files))
	}
	p := filepath.Join(dir, TextKey(live.Model(), "gamma")+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "}\n") || !strings.Contains(string(b), "\n  \"key\"") {
		t.Errorf("recording not indented JSON plus newline:\n%s", b)
	}
	var r Recording
	if err := json.Unmarshal(b, &r); err != nil || r.Text != "gamma" || r.Model != live.Model() || len(r.Vector) != 64 {
		t.Errorf("recording %+v, err %v", r, err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", st.Mode().Perm())
	}

	// Second call: one new text, the rest served from disk.
	second, err := rec.Embed(ctx, []string{"gamma", "delta", "alpha beta"})
	if err != nil {
		t.Fatal(err)
	}
	if live.Calls != 2 || live.Texts != 3 {
		t.Fatalf("live calls %d texts %d, want only delta sent", live.Calls, live.Texts)
	}
	if !reflect.DeepEqual(first[1], second[0]) || !reflect.DeepEqual(first[0], second[2]) {
		t.Error("replayed vectors differ from recorded ones")
	}
	// Everything recorded: no live call.
	if _, err := rec.Embed(ctx, []string{"delta", "gamma"}); err != nil || live.Calls != 2 {
		t.Fatalf("all-recorded call: err %v, live calls %d", err, live.Calls)
	}
}

func TestRecorderLiveError(t *testing.T) {
	rec := &Recorder{Live: &Fake{Err: ErrUnavailable}, Dir: t.TempDir()}
	if _, err := rec.Embed(context.Background(), []string{"x"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestReplay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	live := &Fake{}
	if _, err := (&Recorder{Live: live, Dir: dir}).Embed(ctx, []string{"a b", "c"}); err != nil {
		t.Fatal(err)
	}
	if (&Replay{}).Model() != "ollama:embeddinggemma-2" {
		t.Errorf("default model %q", (&Replay{}).Model())
	}
	r := &Replay{Dir: dir, Name: live.Model()}
	got, err := r.Embed(ctx, []string{"c", "a b"})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := live.Embed(ctx, []string{"c", "a b"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("replay = %v, want %v", got, want)
	}
	_, err = r.Embed(ctx, []string{"a b", "never recorded"})
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, ErrNoRecording) {
		t.Fatalf("miss err = %v, want ErrUnavailable and ErrNoRecording", err)
	}
	if key := TextKey(live.Model(), "never recorded"); !strings.Contains(err.Error(), key) {
		t.Errorf("miss error %q does not name key %s", err, key)
	}
	// Another model id never matches these recordings.
	if _, err := (&Replay{Dir: dir}).Embed(ctx, []string{"c"}); !errors.Is(err, ErrNoRecording) {
		t.Errorf("other model: err = %v", err)
	}
}

func TestReplayBadRecording(t *testing.T) {
	dir := t.TempDir()
	r := &Replay{Dir: dir}
	if err := os.WriteFile(filepath.Join(dir, TextKey(r.Model(), "x")+".json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := r.Embed(context.Background(), []string{"x"})
	if err == nil || errors.Is(err, ErrNoRecording) {
		t.Fatalf("err = %v, want a decode error", err)
	}
}
