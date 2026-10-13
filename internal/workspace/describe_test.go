package workspace

import (
	"testing"

	"github.com/fredricw/prt/internal/proto"
)

func TestDescribe(t *testing.T) {
	repo := Repo{Key: "svc/billing", Root: "/w/svc/billing", GitDir: "/w/svc/billing/.git", CommonDir: "/w/svc/billing/.git"}
	tests := []struct {
		name       string
		configured string
		origin     string
		wired      bool
		ghNote     string
		want       proto.RepoInfo
	}{
		{
			name: "origin on GitHub, client wired", origin: "git@github.com:acme/billing-svc.git", wired: true,
			want: proto.RepoInfo{Label: "acme/billing-svc", GHRepo: "acme/billing-svc", Status: proto.RepoGitHub},
		},
		{
			name: "wired with a note", origin: "https://github.com/acme/billing-svc", wired: true, ghNote: "fixtures: prs.json",
			want: proto.RepoInfo{Label: "acme/billing-svc", GHRepo: "acme/billing-svc", Status: proto.RepoGitHub, Note: "fixtures: prs.json"},
		},
		{
			name: "on GitHub, no client", origin: "https://github.com/acme/billing-svc.git", ghNote: "no token from `gh auth token`",
			want: proto.RepoInfo{Label: "acme/billing-svc", GHRepo: "acme/billing-svc", Status: proto.RepoGitHubDisabled, Note: "no token from `gh auth token`"},
		},
		{
			name: "configured wins over origin", configured: "acme/payments", origin: "git@github.com:acme/billing-svc.git", wired: true,
			want: proto.RepoInfo{Label: "acme/payments", GHRepo: "acme/payments", Status: proto.RepoGitHub},
		},
		{
			name: "configured without origin", configured: " acme/payments ",
			want: proto.RepoInfo{Label: "acme/payments", GHRepo: "acme/payments", Status: proto.RepoGitHubDisabled},
		},
		{
			name: "bad configured repo", configured: "payments", origin: "git@github.com:acme/billing-svc.git", wired: true,
			want: proto.RepoInfo{Label: "svc/billing", Status: proto.RepoNotGitHub, Note: noteBadConfiguredRepo},
		},
		{
			name: "no remote", wired: true, ghNote: "ignored",
			want: proto.RepoInfo{Label: "svc/billing", Status: proto.RepoNoRemote},
		},
		{
			name: "not GitHub", origin: "git@gitlab.com:acme/billing.git", wired: true, ghNote: "ignored",
			want: proto.RepoInfo{Label: "svc/billing", Status: proto.RepoNotGitHub},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			want.Key, want.Root, want.GitDir = repo.Key, repo.Root, repo.GitDir
			if got := Describe(repo, tt.configured, tt.origin, tt.wired, tt.ghNote); got != want {
				t.Errorf("Describe = %+v\nwant       %+v", got, want)
			}
		})
	}
}
