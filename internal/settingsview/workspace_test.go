package settingsview

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/proto"
)

// workspaceInfo is sampleInfo for the billing-svc repo of a workspace.
func workspaceInfo() proto.SettingsInfo {
	info := sampleInfo()
	info.Workspace = "/home/u/code"
	info.Repo = "billing-svc"
	info.RepoLabel = "acme/billing-svc"
	info.RepoDir = "/home/u/code/billing-svc/.git/prt"
	info.Source = "/home/u/code/billing-svc/.git/prt/prt.toml"
	return info
}

// selectGlobal moves from auto_review to the scope field and chooses the
// global scope.
func selectGlobal(t *testing.T, m *Model) {
	t.Helper()
	send(m, kp(tea.KeyEnter), kp(tea.KeyDown))
	if m.fv.Scope != proto.ScopeGlobal {
		t.Fatalf("scope = %q after choosing global", m.fv.Scope)
	}
}

func TestWorkspaceLines(t *testing.T) {
	m := loaded(t, workspaceInfo(), 140, 40)
	v := view(m)
	for _, s := range []string{
		"workspace: /home/u/code · repo: acme/billing-svc",
		"repo    /home/u/code/billing-svc/.git/prt/prt.toml (acme/billing-svc only)",
		"global  /home/u/.config/prt/prt.toml (every repo)",
	} {
		if !strings.Contains(v, s) {
			t.Errorf("workspace view lacks %q:\n%s", s, v)
		}
	}
	if i, j := strings.Index(v, "workspace: "), strings.Index(v, "auto_review"); i < 0 || i > j {
		t.Errorf("the workspace line is not above the form:\n%s", v)
	}

	// Without a label the repo key names the repo.
	info := workspaceInfo()
	info.RepoLabel = ""
	v = view(loaded(t, info, 140, 40))
	if !strings.Contains(v, "workspace: /home/u/code · repo: billing-svc") || !strings.Contains(v, "(billing-svc only)") {
		t.Errorf("key fallback:\n%s", v)
	}

	// A single repo gains only the "saves to" line.
	v = view(loaded(t, sampleInfo(), 140, 40))
	for _, s := range []string{"workspace:", "(every repo)", " only)", "urgency scale"} {
		if strings.Contains(v, s) {
			t.Errorf("single-repo view has %q:\n%s", s, v)
		}
	}
}

func TestSavesToFollowsScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		info proto.SettingsInfo
	}{
		{"single repo", sampleInfo()},
		{"workspace", workspaceInfo()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, global := savesTo(tc.info.RepoDir), savesTo(tc.info.GlobalDir)
			m := loaded(t, tc.info, 140, 40)
			if v := view(m); !strings.Contains(v, repo) || strings.Contains(v, global) {
				t.Fatalf("repo scope: want %q:\n%s", repo, v)
			}
			selectGlobal(t, m)
			if v := view(m); !strings.Contains(v, global) || strings.Contains(v, repo) {
				t.Errorf("global scope: want %q:\n%s", global, v)
			}
			send(m, kp(tea.KeyUp)) // back to repo
			if v := view(m); !strings.Contains(v, repo) {
				t.Errorf("repo again: want %q:\n%s", repo, v)
			}
		})
	}
	if got := savesTo("/home/u/.config/prt"); got != "saves to: /home/u/.config/prt/prt.toml · cost cap, deep slots: /home/u/.config/prt/prt.routing.toml" {
		t.Errorf("savesTo = %q", got)
	}

	// Without a global dir there is no choice: the repo dir.
	info := workspaceInfo()
	info.GlobalDir = ""
	if v := view(loaded(t, info, 140, 40)); !strings.Contains(v, savesTo(info.RepoDir)) {
		t.Errorf("no global dir:\n%s", v)
	}
}

func TestWeightsNote(t *testing.T) {
	const note = "weights here change this repo's urgency scale only"
	tests := []struct {
		name    string
		info    proto.SettingsInfo
		global  bool
		section int
		want    bool
	}{
		{"workspace, repo scope, weights", workspaceInfo(), false, 4, true},
		{"workspace, repo scope, analysis", workspaceInfo(), false, 0, false},
		{"workspace, repo scope, thresholds", workspaceInfo(), false, 5, false},
		{"workspace, global scope, weights", workspaceInfo(), true, 4, false},
		{"single repo, weights", sampleInfo(), false, 4, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := loaded(t, tt.info, 140, 40)
			if tt.global {
				selectGlobal(t, m)
			}
			toSection(t, m, tt.section)
			v := view(m)
			if got := strings.Contains(v, note); got != tt.want {
				t.Errorf("note shown %v, want %v:\n%s", got, tt.want, v)
			}
			if lines := strings.Split(m.View(), "\n"); len(lines) != 40 {
				t.Errorf("%d lines, want 40", len(lines))
			}
		})
	}
	if sectionNames()[4] != sectionUrgency {
		t.Errorf("section 4 is %q", sectionNames()[4])
	}
}

func TestWorkspaceViewFits(t *testing.T) {
	for _, size := range [][2]int{{40, 20}, {80, 24}, {140, 40}} {
		w, h := size[0], size[1]
		m := loaded(t, workspaceInfo(), w, h)
		for i := range 7 {
			toSection(t, m, i)
			lines := strings.Split(m.View(), "\n")
			if len(lines) != h {
				t.Errorf("%dx%d section %d: %d lines", w, h, i, len(lines))
			}
			for _, l := range lines {
				if lw := ansi.StringWidth(l); lw > w {
					t.Errorf("%dx%d section %d: line %d wide: %q", w, h, i, lw, l)
				}
			}
		}
	}
}
