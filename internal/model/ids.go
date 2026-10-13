package model

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// PRItemID returns the item id of a PR.
func PRItemID(number int) string { return "pr:" + strconv.Itoa(number) }

// BranchItemID returns the item id of a local branch.
func BranchItemID(ref string) string { return "branch:" + ref }

// RepoIDSep joins a workspace repo key and a repo-local item id (M5.3,
// multi-repo). Git ref names cannot contain ':' and repo keys never do
// (CleanRepoKey), so the first "::" of an id is always this separator.
const RepoIDSep = "::"

// CleanRepoKey makes s usable as a workspace repo key: slash-separated,
// no leading "./" or trailing "/", and ':' (which would clash with
// RepoIDSep and the "repo:branch" qualifier) replaced by '_'. "" stays "".
func CleanRepoKey(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	s = strings.ReplaceAll(s, ":", "_")
	for strings.HasPrefix(s, "./") {
		s = s[2:]
	}
	return strings.Trim(s, "/")
}

// RepoItemID returns the workspace-wide id of a repo-local item id (M5.3):
// "<repo>::<id>". Per-repo stores keep the local id; only the workspace
// client (internal/workspace) adds and strips the prefix, so stores written
// before M5.3 need no migration. An empty repo returns id unchanged (the
// single-repo case).
func RepoItemID(repo, id string) string {
	if repo == "" {
		return id
	}
	return repo + RepoIDSep + id
}

// SplitRepoItemID is the inverse of RepoItemID: the repo key and the
// repo-local id. An id without a repo prefix returns ("", id).
func SplitRepoItemID(id string) (repo, local string) {
	if r, l, ok := strings.Cut(id, RepoIDSep); ok && r != "" {
		return r, l
	}
	return "", id
}

// NormaliseHunk trims trailing whitespace from each line and drops blank lines
// at either end, so unit ids survive whitespace-only noise around a hunk. It
// keeps the +/-/space prefixes, so the same text added and removed differ.
func NormaliseHunk(hunk string) string {
	lines := strings.Split(strings.ReplaceAll(hunk, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, strings.TrimRight(l, " \t"))
	}
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// UnitID is sha256(path + "\n" + normalised hunk), hex encoded (spec: ChangeUnit.id).
// The same hunk in two items has the same id.
func UnitID(path, hunk string) string {
	h := sha256.New()
	h.Write([]byte(path))
	h.Write([]byte{'\n'})
	h.Write([]byte(NormaliseHunk(hunk)))
	return hex.EncodeToString(h.Sum(nil))
}

// DuplicateUnitID is the id of the n-th (n >= 2) unit of one item whose
// UnitID equals id (identical path and diff body, e.g. the same edit in two
// functions of one file). Ids must be unique within an item, so later
// duplicates mix in their enclosing symbol and occurrence number; the first
// keeps the plain content id. See DECISIONS "Duplicate unit ids".
func DuplicateUnitID(id, symbol string, n int) string {
	return ContentHash("dup-unit", id, symbol, strconv.Itoa(n))
}

// GroupID is the hash of the sorted unit id list; it is also the cache key for
// a group's review.
func GroupID(unitIDs []string) string {
	ids := append([]string(nil), unitIDs...)
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return hex.EncodeToString(sum[:])
}

// ContentHash hashes arbitrary parts into a cache key.
func ContentHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strconv.Itoa(len(p))))
		h.Write([]byte{':'})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}
