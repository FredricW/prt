package review

import (
	"context"
	"sync"
)

// Fake is a scripted Reviewer. Respond, when set, computes the result;
// otherwise it returns Default (with Model filled in).
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
	if f.Respond != nil {
		return f.Respond(req)
	}
	r := f.Default
	r.Model = f.Model()
	return r, nil
}
