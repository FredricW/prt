package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/fredricw/prt/internal/model"
)

// Fake answers every question with a function of (question name, question).
// The default answers are deterministic and neutral: boolean p=0.1, first
// choice option with p=0.9, lowest score rung with p=0.9.
type Fake struct {
	mu     sync.Mutex
	Answer func(name string, q Question, state any) Answer
	Err    error
	Calls  []Request
}

var _ Client = (*Fake)(nil)

// Evaluate implements Client.
func (f *Fake) Evaluate(_ context.Context, req Request) (Response, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, req)
	f.mu.Unlock()
	if f.Err != nil {
		return Response{}, f.Err
	}
	ans := f.Answer
	if ans == nil {
		ans = DefaultAnswer
	}
	out := Response{Model: Model, Answers: map[string]Answer{}}
	for name, q := range req.Questions {
		out.Answers[name] = ans(name, q, req.State)
	}
	return out, nil
}

// DefaultAnswer is the Fake's default answer.
func DefaultAnswer(_ string, q Question, _ any) Answer {
	switch q.Type {
	case Boolean:
		p := 0.1
		return Answer{Type: Boolean, Probability: &p}
	case Choice:
		opts := choiceOptions(q.Criteria)
		if len(opts) == 0 {
			return Answer{Type: Choice}
		}
		probs := map[string]float64{}
		for _, o := range opts {
			probs[o] = 0.1 / float64(max(1, len(opts)-1))
		}
		probs[opts[0]] = 0.9
		return Answer{Type: Choice, Choice: opts[0], Probabilities: probs}
	case Score:
		s := 0.0
		return Answer{Type: Score, Score: &s, Probabilities: map[string]float64{"0": 0.9, "1": 0.1}}
	}
	return Answer{Type: q.Type}
}

// choiceOptions returns choice option names in a stable order.
func choiceOptions(criteria any) []string {
	switch c := criteria.(type) {
	case map[string]string:
		return sortedKeys(c)
	case map[string]any:
		m := map[string]string{}
		for k := range c {
			m[k] = ""
		}
		return sortedKeys(m)
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ErrNoRecording is returned by Replay when a request was never recorded.
var ErrNoRecording = errors.New("jev: no recording for request")

// Recording is the on-disk shape of one recorded call.
type Recording struct {
	Key      string   `json:"key"`
	Request  Request  `json:"request"`
	Response Response `json:"response"`
}

// RequestKey is the content hash of a request's canonical JSON (map keys are
// sorted by encoding/json), used as the recording file name.
func RequestKey(req Request) (string, error) {
	if req.Model == "" {
		req.Model = Model
	}
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return model.ContentHash("jev", string(b))[:24], nil
}

// Replay serves recorded responses from Dir (one <key>.json per request).
type Replay struct {
	Dir string
}

var _ Client = (*Replay)(nil)

// Evaluate implements Client.
func (r *Replay) Evaluate(_ context.Context, req Request) (Response, error) {
	key, err := RequestKey(req)
	if err != nil {
		return Response{}, err
	}
	b, err := os.ReadFile(filepath.Join(r.Dir, key+".json"))
	if err != nil {
		return Response{}, fmt.Errorf("%w: %s", ErrNoRecording, key)
	}
	var rec Recording
	if err := json.Unmarshal(b, &rec); err != nil {
		return Response{}, fmt.Errorf("jev: bad recording %s: %w", key, err)
	}
	return rec.Response, nil
}

// Recorder wraps a live Client and writes every successful call to Dir. Use it
// only from `//go:build live` tests or tools, never from normal tests.
type Recorder struct {
	Live Client
	Dir  string
}

var _ Client = (*Recorder)(nil)

// Evaluate implements Client.
func (r *Recorder) Evaluate(ctx context.Context, req Request) (Response, error) {
	resp, err := r.Live.Evaluate(ctx, req)
	if err != nil {
		return resp, err
	}
	key, err := RequestKey(req)
	if err != nil {
		return resp, err
	}
	if req.Model == "" {
		req.Model = Model
	}
	b, err := json.MarshalIndent(Recording{Key: key, Request: req, Response: resp}, "", "  ")
	if err != nil {
		return resp, err
	}
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return resp, err
	}
	return resp, os.WriteFile(filepath.Join(r.Dir, key+".json"), append(b, '\n'), 0o600)
}
