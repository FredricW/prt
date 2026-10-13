package review

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

// ErrNoRecording is returned by Replay when a request was never recorded.
var ErrNoRecording = errors.New("review: no recording for request")

// Recording is the on-disk shape of one recorded call
// (testdata/recordings/review/<key>.json).
type Recording struct {
	Key     string  `json:"key"`
	Model   string  `json:"model"` // "provider:model" spec
	Request Request `json:"request"`
	Result  Result  `json:"result"`
	// Synthetic marks a hand-written response (not from the real API).
	Synthetic bool `json:"synthetic,omitempty"`
	// Truncated marks a response cut off at max_tokens before it was
	// usable (IsTruncated). Replay returns it with an error wrapping
	// ErrTruncated, as the live call did, so the pipeline's retry with a
	// larger cap (a request of its own, recorded under its own key) is
	// reached on replay too.
	Truncated bool `json:"truncated,omitempty"`
}

// RequestKey is the deterministic recording key of a request sent to a
// model: a content hash of the model spec and the request's canonical JSON.
func RequestKey(modelSpec string, req Request) (string, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return model.ContentHash("review", modelSpec, string(b))[:24], nil
}

// RecordingPath is the file a request to modelSpec is recorded in under dir.
func RecordingPath(dir, modelSpec string, req Request) (string, error) {
	key, err := RequestKey(modelSpec, req)
	if err != nil {
		return "", fmt.Errorf("review: request key: %w", err)
	}
	return filepath.Join(dir, key+".json"), nil
}

// LoadRecording reads the recording of a request to modelSpec from dir;
// ok is false when none exists.
func LoadRecording(dir, modelSpec string, req Request) (rec Recording, ok bool, err error) {
	path, err := RecordingPath(dir, modelSpec, req)
	if err != nil {
		return Recording{}, false, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Recording{}, false, nil
	}
	if err != nil {
		return Recording{}, false, fmt.Errorf("review: read recording %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return Recording{}, false, fmt.Errorf("review: bad recording %s: %w", path, err)
	}
	return rec, true, nil
}

// Replay serves recorded results from Dir. Name is the model spec it
// stands in for (keys include it, so fast and deep need one Replay each).
type Replay struct {
	Name string
	Dir  string
	// MissUnavailable turns a missing recording into an error wrapping
	// ErrUnavailable as well as ErrNoRecording (demo/snapshot degrade);
	// tests leave it false so a miss is loud.
	MissUnavailable bool
}

var _ Reviewer = (*Replay)(nil)

// Model implements Reviewer.
func (r *Replay) Model() string { return r.Name }

// Review implements Reviewer.
func (r *Replay) Review(_ context.Context, req Request) (Result, error) {
	rec, ok, err := LoadRecording(r.Dir, r.Name, req)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		key, _ := RequestKey(r.Name, req)
		if r.MissUnavailable {
			return Result{}, fmt.Errorf("%w: %w %s in %s", ErrUnavailable, ErrNoRecording, key, r.Dir)
		}
		return Result{}, fmt.Errorf("%w %s in %s (run make record-review)", ErrNoRecording, key, r.Dir)
	}
	res := rec.Result
	if res.InputTokens > 0 || res.OutputTokens > 0 {
		// Cost follows the current price table, not the one in force when
		// the call was recorded (token counts are what was billed).
		res.Cost = Cost(r.Name, res.InputTokens, res.OutputTokens)
	}
	if rec.Truncated {
		return res, fmt.Errorf("review: %s (stop_reason %s): %w (recorded as %s)", r.Name, res.StopReason, ErrTruncated, rec.Key)
	}
	return reanchor(res, req)
}

// reanchor re-parses a recorded TaskReview response against the request's
// units (Units are not part of the key, so a recording made for the same
// prompt anchors to the units of the current run). Results without text
// (hand-written recordings) and other tasks are returned as stored.
func reanchor(res Result, req Request) (Result, error) {
	if req.Task != TaskReview || res.Text == "" {
		return res, nil
	}
	fs, verdict, conf, err := ParseReview(res.Text, req.Units)
	if err != nil {
		return res, fmt.Errorf("review: recorded response: %w", err)
	}
	res.Findings, res.Verdict, res.Confidence = fs, verdict, conf
	return res, nil
}

// Recorder wraps a live Reviewer and writes every successful call to Dir,
// and every call cut off at max_tokens (IsTruncated) as a Truncated
// recording that Replay answers with the same error. Other failures are
// not recorded. Only tools and `//go:build live` tests use it. It never
// sees keys.
type Recorder struct {
	Live      Reviewer
	Dir       string
	Synthetic bool
}

var _ Reviewer = (*Recorder)(nil)

// Model implements Reviewer.
func (r *Recorder) Model() string { return r.Live.Model() }

// Ready implements Checker by asking the live reviewer, so a recorder over
// an adapter without a key is skipped like the adapter itself.
func (r *Recorder) Ready() error { return Ready(r.Live) }

// Review implements Reviewer.
func (r *Recorder) Review(ctx context.Context, req Request) (Result, error) {
	res, callErr := r.Live.Review(ctx, req)
	truncated := IsTruncated(res, callErr)
	if callErr != nil && !truncated {
		return res, callErr
	}
	if err := r.write(req, res, truncated); err != nil {
		return res, errors.Join(callErr, err)
	}
	return res, callErr
}

// write stores one recording under the request's key.
func (r *Recorder) write(req Request, res Result, truncated bool) error {
	key, err := RequestKey(r.Live.Model(), req)
	if err != nil {
		return fmt.Errorf("review: record: %w", err)
	}
	rec := Recording{Key: key, Model: r.Live.Model(), Request: req, Result: res, Synthetic: r.Synthetic, Truncated: truncated}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("review: record %s: %w", key, err)
	}
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return fmt.Errorf("review: record %s: %w", key, err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, key+".json"), append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("review: record %s: %w", key, err)
	}
	return nil
}
