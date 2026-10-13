package jev

import (
	"context"
	"errors"
	"testing"
)

func TestReplayMiss(t *testing.T) {
	req := Request{State: "s", Questions: map[string]Question{"q": {Type: Boolean, Instructions: "?"}}}
	for _, tc := range []struct {
		name        string
		unavailable bool
	}{
		{"strict", false},
		{"degrade", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Replay{Dir: t.TempDir(), MissUnavailable: tc.unavailable}
			_, err := r.Evaluate(context.Background(), req)
			if !errors.Is(err, ErrNoRecording) {
				t.Fatalf("err = %v, want ErrNoRecording", err)
			}
			if got := errors.Is(err, ErrUnavailable); got != tc.unavailable {
				t.Fatalf("errors.Is(err, ErrUnavailable) = %v, want %v (err %v)", got, tc.unavailable, err)
			}
		})
	}
}
