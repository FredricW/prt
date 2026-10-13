package gh

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
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
	f := &Fake{Login: fx.Viewer, PRs: map[int]PR{}, Diffs: map[int]string{}, ThreadMap: map[int][]Thread{}, Owners: fx.CodeOwners}
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
		f.ThreadMap[n] = ts
	}
	return f, nil
}
