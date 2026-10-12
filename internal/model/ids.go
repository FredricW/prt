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
