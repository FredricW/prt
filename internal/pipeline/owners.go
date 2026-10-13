package pipeline

import (
	"bufio"
	"context"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/fredricw/prt/internal/model"
)

// codeownersPaths are the locations GitHub reads CODEOWNERS from, in GitHub's
// lookup order (the first one present wins).
var codeownersPaths = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

// ownerRule is one CODEOWNERS line: a path pattern and its owners.
type ownerRule struct {
	pattern string // doublestar pattern derived from the CODEOWNERS pattern
	owners  []string
}

// ownership decides File.Owned for one item: a path is owned when it matches
// one of the user's globs (config user.owns) or when the last matching
// CODEOWNERS rule at the head names the viewer.
type ownership struct {
	globs  []string
	rules  []ownerRule
	viewer string // "@login", lower-cased; "" when unknown
}

// owned reports whether path is owned.
func (o ownership) owned(path string) bool {
	return matchAny(o.globs, path) || o.codeowned(path)
}

func (o ownership) codeowned(path string) bool {
	if o.viewer == "" {
		return false
	}
	var owners []string
	matched := false
	for _, r := range o.rules {
		if matchPattern(r.pattern, path) {
			owners, matched = r.owners, true // last matching rule wins
		}
	}
	if !matched {
		return false
	}
	for _, w := range owners {
		if strings.EqualFold(w, o.viewer) {
			return true
		}
	}
	return false
}

// matchAny reports whether path matches any doublestar glob.
func matchAny(globs []string, path string) bool {
	for _, g := range globs {
		if matchPattern(g, path) {
			return true
		}
	}
	return false
}

// matchPattern matches path against a doublestar pattern; a pattern naming a
// directory also matches everything below it.
func matchPattern(pattern, path string) bool {
	if ok, err := doublestar.Match(pattern, path); err == nil && ok {
		return true
	}
	ok, err := doublestar.Match(strings.TrimSuffix(pattern, "/")+"/**", path)
	return err == nil && ok
}

// parseCodeowners parses a CODEOWNERS file. GitHub's gitignore-style pattern
// semantics are approximated with doublestar:
//
//   - "#" starts a comment; blank lines are ignored; "\#" escapes are not
//     supported.
//   - A leading "/" anchors the pattern at the repo root; a pattern with a
//     "/" elsewhere (except a trailing one) is anchored too.
//   - An unanchored pattern matches at any depth ("*.go" → "**/*.go").
//   - A trailing "/" matches the directory's contents ("docs/" → "docs/**").
//   - "*", "?", "**" and "[...]" are doublestar's; a pattern naming a
//     directory also matches everything below it (see matchPattern).
func parseCodeowners(text string) []ownerRule {
	var rules []ownerRule
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		rules = append(rules, ownerRule{pattern: codeownersGlob(fields[0]), owners: fields[1:]})
	}
	return rules
}

// codeownersGlob converts one CODEOWNERS pattern to a doublestar pattern.
func codeownersGlob(p string) string {
	dir := strings.HasSuffix(p, "/")
	p = strings.TrimSuffix(p, "/")
	anchored := strings.HasPrefix(p, "/") || strings.Contains(p, "/")
	p = strings.TrimPrefix(p, "/")
	if dir {
		p += "/**"
	}
	if !anchored && !strings.HasPrefix(p, "**") {
		p = "**/" + p
	}
	return p
}

// ownershipFor builds the ownership rules for an item analysed at head.
func (p *Pipeline) ownershipFor(ctx context.Context, head string) ownership {
	o := ownership{globs: p.d.Config.User.Owns}
	viewer := p.viewer(ctx)
	if viewer == "" || p.d.Git == nil || head == "" {
		return o
	}
	for _, path := range codeownersPaths {
		b, err := p.d.Git.Show(ctx, head, path)
		if err != nil {
			continue
		}
		o.rules = parseCodeowners(string(b))
		o.viewer = "@" + viewer
		break
	}
	return o
}

// viewer returns the GitHub login of the user ("" without GitHub). A
// successful lookup is cached on the Pipeline; failures are logged and
// retried on the next run.
func (p *Pipeline) viewer(ctx context.Context) string {
	if p.d.GH == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.login != "" {
		return p.login
	}
	login, err := p.d.GH.Viewer(ctx)
	if err != nil {
		p.d.Log.Warn("viewer lookup failed; CODEOWNERS ignored", "err", err)
		return ""
	}
	p.login = login
	return login
}

// markOwned sets File.Owned on files.
func markOwned(files []model.File, o ownership) {
	for i := range files {
		files[i].Owned = o.owned(files[i].Path)
	}
}
