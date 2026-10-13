package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
)

// --branches runs the fixture branch after the PRs: the dry run counts its
// one branch.state request and suggests the flag in the record command.
func TestDryRunWithBranches(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"--branches", "--embed", "none", "--out", t.TempDir()}, func(string) string { return "" }, &out, &errOut)
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, errOut.String())
	}
	for _, want := range []string{"M5 requests: branch state 1 (0 recorded, 1 to record)", "--branches"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, out.String())
		}
	}
	// Without the flag the branch is not run.
	out.Reset()
	if code := run(context.Background(), []string{"--embed", "none", "--out", t.TempDir()}, func(string) string { return "" }, &out, &errOut); code != 0 {
		t.Fatalf("code %d, stderr %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "M5 requests") {
		t.Errorf("branch counted without --branches:\n%s", out.String())
	}
}

func TestIsBranchState(t *testing.T) {
	cases := map[string]bool{model.QState: true, model.QState + "__x": true, model.QCommentAddressed: false, model.QKind: false}
	for name, want := range cases {
		if got := isBranchState(jev.Request{Questions: map[string]jev.Question{name: {}}}); got != want {
			t.Errorf("isBranchState(%s) = %v, want %v", name, got, want)
		}
	}
}
