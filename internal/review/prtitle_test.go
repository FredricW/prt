package review

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/model"
)

func TestBuildPRTitleGolden(t *testing.T) {
	req := BuildPRTitle([]string{"Dark mode palette", "Theme toggle in settings", "Ungrouped: styles.css"}, "05-spike-dark-mode")
	if req.Task != TaskPRTitle || req.Tier != model.TierFast || req.MaxTokens != TitleMaxTokens || req.GroupID != "" {
		t.Fatalf("request: %+v", req)
	}
	golden(t, "prtitle.golden", renderRequest(req))
}

func TestBuildPRTitleCapsGroups(t *testing.T) {
	var names []string
	for i := range 25 {
		names = append(names, fmt.Sprintf("group %02d %s", i, strings.Repeat("x", 300)))
	}
	p := BuildPRTitle(names, "feat/x").Prompt
	if !strings.Contains(p, "(and 5 more groups)") || strings.Contains(p, "group 20") {
		t.Errorf("groups not capped:\n%s", p)
	}
	if len(p) > prTitleGroups*(titleLineBytes+8)+512 {
		t.Errorf("prompt %d bytes", len(p))
	}
}

// The branch block and the Architecture graph (M5) never feed the TL;DR,
// so their changes do not change its request key.
func TestBuildTLDRIgnoresBranchAndArch(t *testing.T) {
	a := sampleAgg()
	base := BuildTLDR(a)
	a.Branch = &model.BranchInfo{State: model.BranchReady, P: 0.8, SuggestedTitle: "Add x", Unpushed: 3}
	a.Arch = &model.ArchGraph{Modules: []model.ArchModule{{ID: "web"}}, NetEffect: []string{"new module web"}}
	if got := BuildTLDR(a); got.Prompt != base.Prompt || got.System != base.System {
		t.Error("branch or arch changed the TL;DR request")
	}
}

func TestCleanPRTitle(t *testing.T) {
	long := strings.Repeat("word ", 30)
	cases := []struct{ name, in, want string }{
		{"plain", "Add dark mode toggle to settings", "Add dark mode toggle to settings"},
		{"trailing period", "Add dark mode toggle.", "Add dark mode toggle"},
		{"quotes and lead-in", `Title: "Add dark mode toggle"`, "Add dark mode toggle"},
		{"markdown first line", "\n```\n**Add `theme` toggle**\nSecond line.\n```", "Add theme toggle"},
		{"long cut at word", long, strings.TrimSpace(strings.Repeat("word ", 14))},
		{"long no spaces", strings.Repeat("x", 100), strings.Repeat("x", MaxPRTitle)},
		{"empty", "  \n ", ""},
	}
	for _, c := range cases {
		got := CleanPRTitle(c.in)
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
		if utf8.RuneCountInString(got) > MaxPRTitle || strings.Contains(got, "\n") || strings.HasSuffix(got, ".") {
			t.Errorf("%s: %q breaks the title rules", c.name, got)
		}
	}
}
