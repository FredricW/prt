package jev

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Regression: the 4-bytes-per-token estimate under-counted every recorded
// request, so a request packed up to the limits could be rejected. The
// estimate of each recorded request's compact JSON must cover the tokens
// the gateway reported.
func TestEstimateTokensCoversRecordings(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "recordings", "jev", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("no recordings")
	}
	checked := 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var rec Recording
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if rec.Synthetic || rec.Response.Usage.InputTokens == 0 {
			continue
		}
		body, err := json.Marshal(rec.Request)
		if err != nil {
			t.Fatal(err)
		}
		if est := EstimateTokensLen(len(body)); est < rec.Response.Usage.InputTokens {
			t.Errorf("%s: %d bytes estimated at %d tokens, gateway counted %d", filepath.Base(p), len(body), est, rec.Response.Usage.InputTokens)
		}
		checked++
	}
	if checked == 0 {
		t.Skip("no recordings with usage")
	}
}
