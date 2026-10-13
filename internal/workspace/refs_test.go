package workspace

import (
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
