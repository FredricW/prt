package workspace

import (
	"strings"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/proto"
)

// noteBadConfiguredRepo is RepoInfo.Note when [github] repo does not parse.
const noteBadConfiguredRepo = "bad github.repo in prt.toml"

// Describe is the workspace's description of a discovered repository
// (proto.RepoInfo without the live Role and SpentUSD): configured is the
// repo's [github] repo ("owner/name", "" when unset), origin its origin
// remote URL ("" when it has none), ghWired whether a GitHub client is
// wired for it and ghNote why not (or where the wired client comes from,
// e.g. a fixture file).
//
// The GitHub repo comes from configured when set (a value that does not
// parse is RepoNotGitHub with a note), else from origin (none: RepoNoRemote;
// not a GitHub URL: RepoNotGitHub). A GitHub repo is RepoGitHub when a
// client is wired, else RepoGitHubDisabled; both carry ghNote. The label is
// the GitHub "owner/name" when known, else the key.
func Describe(r Repo, configured, origin string, ghWired bool, ghNote string) proto.RepoInfo {
	info := proto.RepoInfo{Key: r.Key, Root: r.Root, GitDir: r.GitDir}
	configured = strings.TrimSpace(configured)
	switch {
	case configured != "":
		if gr, ok := gh.ParseRemote("github.com/" + configured); ok {
			info.GHRepo = gr.String()
		} else {
			info.Status, info.Note = proto.RepoNotGitHub, noteBadConfiguredRepo
		}
	case strings.TrimSpace(origin) == "":
		info.Status = proto.RepoNoRemote
	default:
		if gr, ok := gh.ParseRemote(origin); ok {
			info.GHRepo = gr.String()
		} else {
			info.Status = proto.RepoNotGitHub
		}
	}
	if info.GHRepo != "" {
		info.Status, info.Note = proto.RepoGitHubDisabled, ghNote
		if ghWired {
			info.Status = proto.RepoGitHub
		}
	}
	info.Label = info.GHRepo
	if info.Label == "" {
		info.Label = r.Key
	}
	return info
}
