package workspace

import (
	"fmt"
	"path"
	"strings"
	"unicode"
)

// ParseQualified splits a repo-qualified query (see the package doc):
// "<repo>#<digits>" gives (repo, "#digits") and "<repo>:<ref>" (split at
// the first ':', which keys and git refs cannot contain) gives (repo, ref).
// Spaces around the query and its parts are trimmed; a leading "!" (the
// closed-PR search) moves to the local part. Both parts must be non-empty
// and the repo part must hold no space (so free text such as "fix: typo"
// is not qualified); anything else returns ok false. ParseQualified only
// parses: callers resolve the repo part (Client.Resolve) and treat the
// whole query as unqualified when that fails.
func ParseQualified(q string) (repo, local string, ok bool) {
	q = strings.TrimSpace(q)
	bang := ""
	if rest, found := strings.CutPrefix(q, "!"); found {
		bang, q = "!", strings.TrimSpace(rest)
	}
	if r, l, found := strings.Cut(q, ":"); found {
		r, l = strings.TrimSpace(r), strings.TrimSpace(l)
		if !validRepoPart(r) || l == "" {
			return "", "", false
		}
		return r, bang + l, true
	}
	i := strings.LastIndex(q, "#")
	if i <= 0 || !allDigits(q[i+1:]) {
		return "", "", false
	}
	r := strings.TrimSpace(q[:i])
	if !validRepoPart(r) {
		return "", "", false
	}
	return r, bang + q[i:], true
}

// validRepoPart reports a non-empty repo part without spaces.
func validRepoPart(r string) bool {
	return r != "" && !strings.ContainsFunc(r, unicode.IsSpace)
}

// allDigits reports a non-empty string of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Resolve finds the member a name refers to, trying in order: the exact
// key; the label (GitHub "owner/name", any case); a unique last path
// element of a key ("portal" for "team/portal", any case); a unique repo
// name of an owner/name label ("billing-svc" for "acme/billing-svc", any
// case). A name that matches several members at the first rule that
// matches any is ambiguous.
func (c *Client) Resolve(name string) (Member, error) {
	name = strings.TrimSpace(name)
	if i, ok := c.byKey[name]; ok && name != "" {
		return c.members[i], nil
	}
	rules := []func(Member) bool{
		func(m Member) bool { return strings.EqualFold(m.Info.Label, name) },
		func(m Member) bool { return strings.EqualFold(path.Base(m.Info.Key), name) },
		func(m Member) bool { return strings.EqualFold(repoName(m.Info.GHRepo), name) },
	}
	for _, match := range rules {
		var hits []Member
		for _, m := range c.members {
			if name != "" && match(m) {
				hits = append(hits, m)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		default:
			return Member{}, fmt.Errorf("workspace: ambiguous repo %q: %s", name, strings.Join(memberKeys(hits), ", "))
		}
	}
	return Member{}, fmt.Errorf("workspace: unknown repo %q (repos: %s)", name, strings.Join(memberKeys(c.members), ", "))
}

// repoName is the name part of "owner/name" ("" when there is none).
func repoName(ghRepo string) string {
	if _, n, ok := strings.Cut(ghRepo, "/"); ok {
		return n
	}
	return ""
}

func memberKeys(ms []Member) []string {
	keys := make([]string, 0, len(ms))
	for _, m := range ms {
		keys = append(keys, m.Info.Key)
	}
	return keys
}
