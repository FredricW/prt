package review

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/fredricw/prt/internal/model"
)

// Fake is a scripted Reviewer. Respond, when set, computes the result;
// otherwise it returns Default. Model is filled in when the result leaves
// it empty.
type Fake struct {
	mu      sync.Mutex
	Name    string
	Respond func(req Request) (Result, error)
	Default Result
	Calls   []Request
}

var _ Reviewer = (*Fake)(nil)

// Model implements Reviewer.
func (f *Fake) Model() string {
	if f.Name == "" {
		return "fake:reviewer"
	}
	return f.Name
}

// Review implements Reviewer.
func (f *Fake) Review(_ context.Context, req Request) (Result, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, req)
	f.mu.Unlock()
	r, err := f.Default, error(nil)
	if f.Respond != nil {
		r, err = f.Respond(req)
	}
	if r.Model == "" {
		r.Model = f.Model()
	}
	return r, err
}

// fakeFinding is the wire shape of one finding in a review response.
type fakeFinding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Claim    string `json:"claim"`
	Fix      string `json:"fix"`
}

// FakeReviewJSON renders a valid TaskReview response text (the JSON shape
// the review system prompt asks for) from findings, a verdict and a
// confidence. ParseReview reads it back.
func FakeReviewJSON(findings []model.Finding, verdict string, confidence float64) string {
	out := struct {
		Findings   []fakeFinding `json:"findings"`
		Verdict    string        `json:"verdict"`
		Confidence float64       `json:"confidence"`
	}{Findings: []fakeFinding{}, Verdict: verdict, Confidence: confidence}
	for _, f := range findings {
		out.Findings = append(out.Findings, fakeFinding{File: f.Path, Line: f.Line, Severity: string(f.Severity), Claim: f.Claim, Fix: f.Fix})
	}
	b, _ := json.Marshal(out) // plain structs: cannot fail
	return string(b)
}

// Fake prose for the non-review tasks (FakeRespond).
const (
	FakeTLDR    = "Fake summary of the change from its labels."
	FakeTitle   = "Fake group title"
	FakePRTitle = "Fake pull request title"
)

// FakeRespond returns a Fake.Respond that answers every task offline: a
// review is FakeReviewJSON with one low finding on the first unit's first
// new line (verdict comment) and the given confidence, parsed and anchored
// like a real response; TL;DR, group title and PR title get fixed prose.
// Token counts are EstimateInputTokens and the text length; cost is 0.
func FakeRespond(confidence float64) func(Request) (Result, error) {
	return func(req Request) (Result, error) {
		res := Result{InputTokens: EstimateInputTokens(req)}
		switch req.Task {
		case TaskReview:
			var fs []model.Finding
			if len(req.Units) > 0 {
				u := req.Units[0]
				fs = append(fs, model.Finding{Path: u.Path, Line: anchorSpan(u).Start, Severity: model.SevLow,
					Claim: "Fake finding for offline runs.", Fix: "No change needed."})
			}
			res.Text = FakeReviewJSON(fs, VerdictComment, confidence)
			var err error
			res.Findings, res.Verdict, res.Confidence, err = ParseReview(res.Text, req.Units)
			if err != nil {
				return res, err
			}
		case TaskTLDR:
			res.Text = FakeTLDR
		case TaskGroupTitle:
			res.Text = FakeTitle
		default:
			res.Text = FakePRTitle
		}
		res.OutputTokens = len(res.Text)/4 + 1
		res.StopReason = "end_turn"
		return res, nil
	}
}
