//go:build live

package gh

import (
	"context"
	"testing"
	"time"
)

// TestLiveAPI talks to the real GitHub API for fredricw/prt using the token
// from `gh auth token`. Run with: go test -tags live ./internal/gh/ -run Live
func TestLiveAPI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, err := TokenFromGH(ctx)
	if err != nil {
		t.Skipf("no GitHub token: %v", err)
	}
	a := NewAPI(ctx, Repo{Owner: "fredricw", Name: "prt"}, tok)
	login, err := a.Viewer(ctx)
	if err != nil || login == "" {
		t.Fatalf("Viewer = %q, %v", login, err)
	}
	if _, err := a.SearchPRs(ctx, "is:pr", 5); err != nil {
		t.Fatalf("SearchPRs: %v", err)
	}
	if _, err := a.CODEOWNERS(ctx, ""); err != nil {
		t.Fatalf("CODEOWNERS: %v", err)
	}
}
