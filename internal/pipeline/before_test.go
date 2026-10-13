package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
)

// numbered returns n lines "<tag> <i>".
func numbered(tag string, n int) []byte {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s %d\n", tag, i)
	}
	return []byte(b.String())
}

// comment_addressed's "before" code must come from where the comment's
// line number belongs (regression: a GitHub comment's head-relative Line
// was read in its original commit, and old-side comments in the head-side
// file).
func TestBeforeCodeUsesTheCommentsOwnLine(t *testing.T) {
	g := &gitx.Fake{
		Files: map[string]map[string][]byte{
			"orig": {"a.go": numbered("orig", 80)},
			"mb":   {"a.go": numbered("base", 80)},
		},
		MergeBases: map[string]string{"orig..base-now": "mb"},
	}
	p := &Pipeline{d: Deps{Git: g}}
	it := model.Item{BaseSHA: "base-now"}
	ctx := context.Background()

	// GitHub comment: Line 50 on the current head, originalLine 20 on orig.
	gh := model.Comment{Path: "a.go", Line: 50, OriginalLine: 20, Side: model.SideNew, CommitSHA: "orig", Source: model.CommentGitHub}
	if got := p.beforeCode(ctx, it, gh, "since"); !strings.Contains(got, "20: orig 20\n") || strings.Contains(got, "orig 50") {
		t.Errorf("github comment before =\n%s", got)
	}
	// Old side: read at the merge base, not at the comment's commit.
	old := gh
	old.Side, old.OriginalLine, old.Line = model.SideOld, 0, 30
	if got := p.beforeCode(ctx, it, old, "since"); !strings.Contains(got, "30: base 30\n") {
		t.Errorf("old-side before =\n%s", got)
	}
	// Local posted copy: Line is already on CommitSHA.
	local := model.Comment{Path: "a.go", Line: 7, Side: model.SideNew, CommitSHA: "orig", Source: model.CommentUser}
	if got := p.beforeCode(ctx, it, local, "since"); !strings.Contains(got, "7: orig 7\n") {
		t.Errorf("local before =\n%s", got)
	}
}
