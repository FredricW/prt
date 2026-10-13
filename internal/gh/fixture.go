package gh

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/fredricw/prt/internal/model"
)

// Fixture is the JSON format of testdata/fixtures/prs.json: the offline
// stand-in for GitHub used by `make demo` (`prt --fixtures FILE`) and tests.
//
//	{
//	  "viewer": "fredric",
//	  "repo":   "acme/fixture",
//	  "prs":    [ {PR...}, ... ],
//	  "diffs":  { "1": "diff --git ..." },   // optional; PRDiff fallback
//	  "threads": { "1": [ {Thread...} ] },   // optional (M4)
//	  "codeowners": "..."                    // optional
//	}
//
// PR head refs must name branches that exist in the fixture repo; headSha
// may be empty, in which case consumers resolve it from the local ref.
type Fixture struct {
	Viewer     string              `json:"viewer"`
	Repo       string              `json:"repo"`
	PRs        []PR                `json:"prs"`
	Diffs      map[string]string   `json:"diffs,omitempty"`
	Threads    map[string][]Thread `json:"threads,omitempty"`
	CodeOwners string              `json:"codeowners,omitempty"`
}

// LoadFixture reads a Fixture file and returns a Fake serving it.
func LoadFixture(path string) (*Fake, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("gh: read fixture: %w", err)
	}
	var fx Fixture
	if err := json.Unmarshal(b, &fx); err != nil {
		return nil, fmt.Errorf("gh: fixture %s: %w", path, err)
	}
	return FakeFromFixture(fx)
}

// FakeFromFixture builds a Fake from a decoded Fixture.
func FakeFromFixture(fx Fixture) (*Fake, error) {
	f := &Fake{Login: fx.Viewer, Repo: fx.Repo, PRs: map[int]PR{}, Diffs: map[int]string{}, ThreadMap: map[int][]Thread{}, Owners: fx.CodeOwners}
	for _, p := range fx.PRs {
		if p.Number <= 0 {
			return nil, fmt.Errorf("gh: fixture PR %q has no number", p.Title)
		}
		if _, dup := f.PRs[p.Number]; dup {
			return nil, fmt.Errorf("gh: fixture PR #%d listed twice", p.Number)
		}
		f.PRs[p.Number] = p
	}
	for k, d := range fx.Diffs {
		n, err := strconv.Atoi(k)
		if err != nil {
			return nil, fmt.Errorf("gh: fixture diffs key %q: %w", k, err)
		}
		f.Diffs[n] = d
	}
	for k, ts := range fx.Threads {
		n, err := strconv.Atoi(k)
		if err != nil {
			return nil, fmt.Errorf("gh: fixture threads key %q: %w", k, err)
		}
		f.ThreadMap[n] = normaliseThreads(ts, n)
	}
	return f, nil
}

// normaliseThreads returns copies of PR number's fixture threads whose
// comments look as API.Threads would return them (see normaliseComment).
func normaliseThreads(ts []Thread, number int) []Thread {
	out := make([]Thread, len(ts))
	for i, t := range ts {
		cs := make([]model.Comment, len(t.Comments))
		for j, c := range t.Comments {
			cs[j] = normaliseComment(c, t, number)
		}
		t.Comments = cs
		out[i] = t
	}
	return out
}

// normaliseComment fills what a fixture may leave out of a thread comment,
// as API.Threads sets it: ID gh-<GHID>, ItemID pr:<number>, Source github,
// ThreadID/Path/Side/Line from the thread (Line falls back to OriginalLine),
// State resolved for a resolved thread (posted otherwise) and Outdated
// from the thread.
func normaliseComment(c model.Comment, t Thread, number int) model.Comment {
	if c.ID == "" && c.GHID != 0 {
		c.ID = CommentID(c.GHID)
	}
	c.ItemID = model.PRItemID(number)
	if c.Source == "" {
		c.Source = model.CommentGitHub
	}
	if c.ThreadID == "" {
		c.ThreadID = t.ID
	}
	if c.Path == "" {
		c.Path = t.Path
	}
	if c.Side == "" {
		c.Side = t.Side
	}
	if c.Line == 0 {
		c.Line = t.Line
		if c.Line == 0 {
			c.Line = t.OriginalLine
		}
	}
	c.State = model.CommentPosted
	if t.Resolved {
		c.State = model.CommentResolved
	}
	c.Outdated = c.Outdated || t.Outdated || t.Line == 0 && t.OriginalLine != 0
	return c
}
