package workspace

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/proto"
)

func TestParseQualified(t *testing.T) {
	tests := []struct {
		q           string
		repo, local string
		ok          bool
	}{
		{"acme/api#12", "acme/api", "#12", true},
		{"api#12", "api", "#12", true},
		{"  api#12 ", "api", "#12", true},
		{"api:feat/x", "api", "feat/x", true},
		{"team/portal:feat/x", "team/portal", "feat/x", true},
		{"api: feat/x", "api", "feat/x", true},
		{"!api:fix", "api", "!fix", true},
		{"! billing-svc:fix", "billing-svc", "!fix", true},
		{"!acme/api#7", "acme/api", "!#7", true},
		{"api:feat#2", "api", "feat#2", true},
		{"feat#2", "feat", "#2", true}, // syntactically qualified; Resolve decides
		{"#12", "", "", false},
		{"!#12", "", "", false},
		{"12", "", "", false},
		{"feat/x", "", "", false},
		{"feat#x", "", "", false},
		{"feat#", "", "", false},
		{"", "", "", false},
		{"!", "", "", false},
		{":x", "", "", false},
		{"x:", "", "", false},
		{"the fix: typo", "", "", false},
		{"two words#3", "", "", false},
	}
	for _, tt := range tests {
		repo, local, ok := ParseQualified(tt.q)
		if repo != tt.repo || local != tt.local || ok != tt.ok {
			t.Errorf("ParseQualified(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.q, repo, local, ok, tt.repo, tt.local, tt.ok)
		}
	}
}

func TestResolve(t *testing.T) {
	c := newTestClient(t,
		newFake(proto.RepoInfo{Key: "api", Label: "acme/api", GHRepo: "acme/api", Status: proto.RepoGitHub}),
		newFake(proto.RepoInfo{Key: "billing-svc", Label: "acme/billing-svc", GHRepo: "acme/billing-svc", Status: proto.RepoGitHub}),
		newFake(proto.RepoInfo{Key: "svc/b", Label: "acme/payments", GHRepo: "acme/payments", Status: proto.RepoGitHub}),
		newFake(proto.RepoInfo{Key: "team/portal", Label: "team/portal", Status: proto.RepoNoRemote}),
		newFake(proto.RepoInfo{Key: "x/web", Label: "x/web", Status: proto.RepoNoRemote}),
		newFake(proto.RepoInfo{Key: "y/web", Label: "y/web", Status: proto.RepoNoRemote}),
		newFake(proto.RepoInfo{Key: "one/kit", Label: "acme/tool", GHRepo: "acme/tool", Status: proto.RepoGitHub}),
		newFake(proto.RepoInfo{Key: "two/box", Label: "other/tool", GHRepo: "other/tool", Status: proto.RepoGitHub}),
	)
	tests := []struct {
		name    string
		want    string // key
		wantErr string
	}{
		{name: "api", want: "api"},                      // key
		{name: " api ", want: "api"},                    // trimmed
		{name: "ACME/API", want: "api"},                 // label, any case
		{name: "acme/billing-svc", want: "billing-svc"}, // label
		{name: "team/portal", want: "team/portal"},      // key with a slash
		{name: "Portal", want: "team/portal"},           // unique base name
		{name: "b", want: "svc/b"},                      // base name before repo name
		{name: "payments", want: "svc/b"},               // unique repo name
		{name: "web", wantErr: `workspace: ambiguous repo "web": x/web, y/web`},
		{name: "tool", wantErr: `workspace: ambiguous repo "tool": one/kit, two/box`},
		{name: "nope", wantErr: `workspace: unknown repo "nope" (repos: api, billing-svc, one/kit, svc/b, team/portal, two/box, x/web, y/web)`},
		{name: "", wantErr: `workspace: unknown repo ""`},
	}
	for _, tt := range tests {
		m, err := c.Resolve(tt.name)
		switch {
		case tt.wantErr != "":
			if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
				t.Errorf("Resolve(%q) = %q, %v; want error %q", tt.name, m.Info.Key, err, tt.wantErr)
			}
		case err != nil || m.Info.Key != tt.want:
			t.Errorf("Resolve(%q) = %q, %v; want %q", tt.name, m.Info.Key, err, tt.want)
		}
	}
}

// Two clones of one GitHub repo (both "acme/portal") get labels that tell
// them apart; their shared GitHub name is ambiguous, their keys are not. A
// repo labelled with its own key keeps it.
func TestDuplicateLabels(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t,
		newFake(proto.RepoInfo{Key: "portal", Label: "acme/portal", GHRepo: "acme/portal", Status: proto.RepoGitHub}),
		newFake(proto.RepoInfo{Key: "portal-2", Label: "acme/portal", GHRepo: "acme/portal", Status: proto.RepoGitHub}),
		newFake(proto.RepoInfo{Key: "acme/Portal", Label: "acme/Portal", Status: proto.RepoNoRemote}),
		newFake(proto.RepoInfo{Key: "web", Label: "acme/web", GHRepo: "acme/web", Status: proto.RepoGitHub}),
	)
	ws, err := c.Workspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, r := range ws.Repos {
		labels = append(labels, r.Key+"="+r.Label)
	}
	want := []string{"acme/Portal=acme/Portal", "portal=acme/portal (portal)", "portal-2=acme/portal (portal-2)", "web=acme/web"}
	if !slices.Equal(labels, want) {
		t.Errorf("labels = %q, want %q", labels, want)
	}
	if _, err := c.Resolve("acme/portal"); err == nil || !errors.Is(err, errAmbiguousRepo) {
		t.Errorf("Resolve(acme/portal) = %v, want ambiguous", err)
	}
	for name, key := range map[string]string{"portal-2": "portal-2", "acme/portal (portal-2)": "portal-2", "acme/web": "web"} {
		if m, err := c.Resolve(name); err != nil || m.Info.Key != key {
			t.Errorf("Resolve(%q) = %q, %v; want %q", name, m.Info.Key, err, key)
		}
	}
	// A qualifier naming both clones is an error, never free text.
	if _, err := c.Add(ctx, "acme/portal#7"); err == nil || !strings.Contains(err.Error(), `ambiguous repo "acme/portal"`) {
		t.Errorf("Add(acme/portal#7) = %v, want the ambiguity", err)
	}
}

// An ambiguous repo part of a qualified query fails with the ambiguity
// (naming the keys) in add, preview and search; an unknown one is free text.
func TestAmbiguousQualifier(t *testing.T) {
	ctx := context.Background()
	a := newFake(proto.RepoInfo{Key: "team-a/billing-svc", Label: "team-a/billing-svc", Status: proto.RepoNoRemote})
	b := newFake(proto.RepoInfo{Key: "team-b/billing-svc", Label: "team-b/billing-svc", Status: proto.RepoNoRemote})
	c := newTestClient(t, a, b)
	const want = `workspace: ambiguous repo "billing-svc": team-a/billing-svc, team-b/billing-svc`
	if _, err := c.Add(ctx, "billing-svc#7"); err == nil || err.Error() != want {
		t.Errorf("Add = %v, want %q", err, want)
	}
	if _, err := c.Preview(ctx, "billing-svc:feat/x"); err == nil || err.Error() != want {
		t.Errorf("Preview = %v, want %q", err, want)
	}
	if _, err := c.Search(ctx, proto.SearchParams{Query: "billing-svc#7", Source: proto.SearchLocal}); err == nil || err.Error() != want {
		t.Errorf("Search = %v, want %q", err, want)
	}
	if len(a.argsOf("Search")) != 0 || len(a.argsOf("Add")) != 0 {
		t.Error("an ambiguous qualifier was sent to a repo")
	}
	if _, err := c.Search(ctx, proto.SearchParams{Query: "nope#7", Source: proto.SearchLocal}); err != nil {
		t.Errorf("unknown qualifier: %v, want a free-text search", err)
	}
}
