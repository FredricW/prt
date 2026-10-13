package embed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/fredricw/prt/internal/model"
)

// DefaultReplayModel is the model id Replay reports when Name is empty: the
// default local model the recordings are made with.
const DefaultReplayModel = "ollama:" + DefaultOllamaModel

// ErrNoRecording is returned (wrapped together with ErrUnavailable) by
// Replay when a text was never recorded.
var ErrNoRecording = errors.New("embed: no recording for text")

// TextKey is the recording key of one text embedded by modelID: a content
// hash of both, so a recording is found again only for the same model and
// byte-identical text.
func TextKey(modelID, text string) string {
	return model.ContentHash("embed", modelID, text)[:24]
}

// Recording is the on-disk shape of one recorded embedding
// (<Dir>/<Key>.json).
type Recording struct {
	Key    string    `json:"key"`
	Model  string    `json:"model"`
	Text   string    `json:"text"`
	Vector []float32 `json:"vector"`
}

// readRecording loads the recording of key from dir. ok is false when no
// file exists; other problems are errors.
func readRecording(dir, key string) (vec []float32, ok bool, err error) {
	path := filepath.Join(dir, key+".json")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("embed: read recording %s: %w", path, err)
	}
	var rec Recording
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, false, fmt.Errorf("embed: bad recording %s: %w", path, err)
	}
	if len(rec.Vector) == 0 {
		return nil, false, fmt.Errorf("embed: recording %s has no vector", path)
	}
	return rec.Vector, true, nil
}

// writeRecording stores one recording as indented JSON plus a newline.
func writeRecording(dir string, rec Recording) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("embed: record %s: %w", rec.Key, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("embed: record %s: %w", rec.Key, err)
	}
	if err := os.WriteFile(filepath.Join(dir, rec.Key+".json"), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("embed: record %s: %w", rec.Key, err)
	}
	return nil
}

// Replay serves recorded embeddings from Dir (one <TextKey>.json per text).
// It never touches the network. A text without a recording fails the whole
// call with an error wrapping both ErrUnavailable (so the pipeline skips
// stage 3) and ErrNoRecording (so tests can tell a miss from an outage).
type Replay struct {
	Dir  string
	Name string // model id; default DefaultReplayModel
}

var _ Embedder = (*Replay)(nil)

// Model implements Embedder.
func (r *Replay) Model() string {
	if r.Name == "" {
		return DefaultReplayModel
	}
	return r.Name
}

// Embed implements Embedder.
func (r *Replay) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := TextKey(r.Model(), t)
		vec, ok, err := readRecording(r.Dir, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: %w %s (model %s) in %s (run make record-embed)", ErrUnavailable, ErrNoRecording, key, r.Model(), r.Dir)
		}
		out[i] = vec
	}
	return out, nil
}

// Recorder wraps a live Embedder: texts already recorded in Dir are served
// from disk; the rest go to Live in one Embed call (adapters batch
// internally) and each is written to Dir as a Recording. Use it only from
// tools or `//go:build live` tests, never from normal tests with a real
// Live.
type Recorder struct {
	Live Embedder
	Dir  string
}

var _ Embedder = (*Recorder)(nil)

// Model implements Embedder.
func (r *Recorder) Model() string { return r.Live.Model() }

// Embed implements Embedder.
func (r *Recorder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	m := r.Model()
	out := make([][]float32, len(texts))
	var missing []string
	missingIdx := map[string][]int{} // text → positions in texts
	for i, t := range texts {
		vec, ok, err := readRecording(r.Dir, TextKey(m, t))
		if err != nil {
			return nil, err
		}
		if ok {
			out[i] = vec
			continue
		}
		if _, seen := missingIdx[t]; !seen {
			missing = append(missing, t)
		}
		missingIdx[t] = append(missingIdx[t], i)
	}
	if len(missing) == 0 {
		return out, nil
	}
	vecs, err := r.Live.Embed(ctx, missing)
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(missing) {
		return nil, fmt.Errorf("embed: record: %d vectors for %d texts", len(vecs), len(missing))
	}
	for j, t := range missing {
		if err := writeRecording(r.Dir, Recording{Key: TextKey(m, t), Model: m, Text: t, Vector: vecs[j]}); err != nil {
			return nil, err
		}
		for _, i := range missingIdx[t] {
			out[i] = vecs[j]
		}
	}
	return out, nil
}
