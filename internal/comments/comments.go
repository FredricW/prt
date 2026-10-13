// Package comments is the pure logic of prt's comment layer (spec Code page
// "Review comments", Data model "Comment", Overview "Since you looked"):
// anchoring comments to change units and re-anchoring them after a push,
// reconciling stored comments with GitHub review threads, turning review
// findings into ai-draft comments, choosing what a review submission sends,
// and the arithmetic of "Since you looked".
//
// It does no I/O: the pipeline and the daemon load and store comments and
// call these functions. Contracts are in DECISIONS "M4 shared contracts".
package comments

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fredricw/prt/internal/model"
)

// GitHubID is the Comment.ID of a comment loaded from GitHub: "gh-<GHID>".
func GitHubID(ghID int64) string { return "gh-" + strconv.FormatInt(ghID, 10) }

// AIDraftID is the Comment.ID of the ai-draft for a finding key:
// "ai-<key>".
func AIDraftID(findingKey string) string { return "ai-" + findingKey }

// DraftID is a new local draft's Comment.ID: "d-" + a hash of the item,
// the time and a caller-supplied sequence number (unique per process).
func DraftID(itemID string, now time.Time, seq uint64) string {
	return "d-" + model.ContentHash("draft", itemID, now.UTC().Format(time.RFC3339Nano), strconv.FormatUint(seq, 10))[:16]
}

// span returns u's line span on side (SideOld: Range.Old; else Range.New).
func span(u model.ChangeUnit, side model.Side) model.Span {
	if side == model.SideOld {
		return u.Range.Old
	}
	return u.Range.New
}

// covers reports whether u's span on side holds line.
func covers(u model.ChangeUnit, side model.Side, line int) bool {
	s := span(u, side)
	return s.Count > 0 && line >= s.Start && line < s.Start+s.Count
}

// Anchor returns c with UnitID and UnitOffset set from the unit whose span
// on c.Side covers c.Line (SideOld: Range.Old; SideNew or "": Range.New),
// preferring the unit c.UnitID already names when it still covers the
// line; among others the first in Order. A line no unit covers leaves
// UnitID "" and UnitOffset 0. Outdated is cleared when a unit is found.
func Anchor(c model.Comment, units []model.ChangeUnit) model.Comment {
	var best *model.ChangeUnit
	for i := range units {
		u := &units[i]
		if u.Path != c.Path || !covers(*u, c.Side, c.Line) {
			continue
		}
		if u.ID == c.UnitID {
			best = u
			break
		}
		if best == nil || u.Order < best.Order {
			best = u
		}
	}
	if best == nil {
		c.UnitID, c.UnitOffset, c.StartOffset = "", 0, 0
		return c
	}
	c.UnitID, c.UnitOffset, c.Outdated = best.ID, c.Line-span(*best, c.Side).Start, false
	c.StartOffset = 0
	if c.StartLine > 0 {
		c.StartOffset = c.StartLine - span(*best, StartSide(c)).Start
	}
	return c
}

// StartSide is the side of c.StartLine: c.StartSide, else c.Side ("" stays
// "", which counts as new).
func StartSide(c model.Comment) model.Side {
	return cmp.Or(c.StartSide, c.Side)
}

// CrossSide reports whether c is a range whose start and end lie on
// different sides (e.g. from a deleted line down to an added one). GitHub
// accepts such ranges (start_side LEFT, side RIGHT); their two line numbers
// are not comparable.
func CrossSide(c model.Comment) bool {
	return c.StartLine > 0 && normSide(StartSide(c)) != normSide(c.Side)
}

// normSide maps "" to new.
func normSide(s model.Side) model.Side {
	if s == model.SideOld {
		return model.SideOld
	}
	return model.SideNew
}

// movable reports whether Reanchor moves c: local comments that are not
// posted yet (user drafts and pending, ai-drafts).
func movable(c model.Comment) bool {
	return c.Source != model.CommentGitHub && (c.State == model.CommentDraft || c.State == model.CommentPending)
}

// Reanchor moves comments onto the current units after a push or rebase
// (spec Identity rules): a comment whose UnitID is still present gets Line
// = that unit's start on Side + UnitOffset (and StartLine shifted by the
// same amount, or, for a CrossSide range, the unit's start on the start
// side + StartOffset); otherwise it is re-anchored by Path + Line (Anchor);
// a comment that neither finds is marked Outdated (its Path/Line kept). Only
// local comments that are not posted (user drafts and pending, ai-drafts)
// move; github and posted comments keep their position and only get
// UnitID/UnitOffset, except outdated github comments, whose line belongs to
// an older commit: they get no unit. It returns the comments that changed,
// in input order.
func Reanchor(cs []model.Comment, units []model.ChangeUnit) []model.Comment {
	byID := make(map[string]model.ChangeUnit, len(units))
	for _, u := range units {
		if _, dup := byID[u.ID]; !dup {
			byID[u.ID] = u
		}
	}
	var changed []model.Comment
	for _, c := range cs {
		n := reanchorOne(c, units, byID)
		if n != c {
			changed = append(changed, n)
		}
	}
	return changed
}

func reanchorOne(c model.Comment, units []model.ChangeUnit, byID map[string]model.ChangeUnit) model.Comment {
	if !movable(c) {
		if c.Source == model.CommentGitHub && c.Outdated {
			// Its line is on the commit the thread was made on (OriginalLine),
			// not on the current head: no current unit holds it, so it is
			// listed in the strip only.
			c.UnitID, c.UnitOffset = "", 0
			return c
		}
		a := Anchor(c, units)
		c.UnitID, c.UnitOffset = a.UnitID, a.UnitOffset
		return c
	}
	if u, ok := byID[c.UnitID]; ok && c.UnitID != "" {
		if s := span(u, c.Side); s.Count > 0 {
			line := s.Start + c.UnitOffset
			switch {
			case CrossSide(c):
				// The start's span moves on its own: place it by its offset in
				// the unit's span on the start side.
				c.StartLine = span(u, StartSide(c)).Start + c.StartOffset
			case c.StartLine > 0:
				c.StartLine += line - c.Line
			}
			c.Path, c.Line, c.Outdated = u.Path, line, false
			return c
		}
	}
	a := Anchor(c, units)
	if a.UnitID == "" {
		a.Outdated = true
	}
	return a
}

// MergeGitHub reconciles an item's stored comments with the comments just
// loaded from its review threads (all Source github, ID = GitHubID):
//   - put: every loaded comment that is new or differs from its stored
//     copy (anchor fields UnitID/UnitOffset of the stored copy are kept);
//   - del: stored github comments no longer on GitHub, and local posted
//     user comments whose GHID now appears among the loaded ones (GitHub's
//     copy replaces the local one).
//
// Drafts, pending comments and ai-drafts are never touched.
func MergeGitHub(stored, loaded []model.Comment) (put []model.Comment, del []string) {
	loadedIDs := make(map[string]bool, len(loaded))
	loadedGH := make(map[int64]bool, len(loaded))
	for _, c := range loaded {
		loadedIDs[c.ID] = true
		if c.GHID != 0 {
			loadedGH[c.GHID] = true
		}
	}
	prev := map[string]model.Comment{}
	for _, s := range stored {
		switch {
		case s.Source == model.CommentGitHub:
			if !loadedIDs[s.ID] {
				del = append(del, s.ID)
			} else {
				prev[s.ID] = s
			}
		case s.Source == model.CommentUser && s.State == model.CommentPosted && s.GHID != 0 && loadedGH[s.GHID]:
			del = append(del, s.ID)
		}
	}
	for _, c := range loaded {
		if p, ok := prev[c.ID]; ok {
			c.UnitID, c.UnitOffset = p.UnitID, p.UnitOffset
			if p == c {
				continue
			}
		}
		put = append(put, c)
	}
	slices.Sort(del)
	return put, del
}

// findingSide is the finding's side ("" counts as new).
func findingSide(f model.Finding) model.Side { return normSide(f.Side) }

// FindingKey identifies a review finding across runs: a content hash of
// the item, the group, the finding's unit, path, side, severity and claim
// (not its line, so it survives a push that shifts the unit).
func FindingKey(itemID, groupID string, f model.Finding) string {
	return model.ContentHash("finding", itemID, groupID, f.UnitID, f.Path, string(findingSide(f)), string(f.Severity), f.Claim)[:16]
}

// DraftBody is the markdown body of an ai-draft for f: "**<severity>**
// <claim>" plus, when f.Fix is set, a blank line and "Suggested fix:
// <fix>".
func DraftBody(f model.Finding) string {
	b := "**" + string(f.Severity) + "** " + f.Claim
	if fix := strings.TrimSpace(f.Fix); fix != "" {
		b += "\n\nSuggested fix: " + fix
	}
	return b
}

// AIDrafts returns the ai-draft comments the item's routing rows call for:
// one per finding with a path and a line > 0 (Source ai-draft, State
// draft, ID AIDraftID(key), FindingKey, Severity, Side from the finding,
// Body DraftBody, Author the reviewing model, anchored with Anchor),
// skipping findings whose key has a FindingMark (accepted or dismissed).
// Calibration findings are not drafts. Sorted by path, line, ID.
func AIDrafts(itemID string, reviews []model.Review, units []model.ChangeUnit, marks []model.FindingMark, now time.Time) []model.Comment {
	marked := make(map[string]bool, len(marks))
	for _, m := range marks {
		if m.ItemID == itemID || m.ItemID == "" {
			marked[m.FindingKey] = true
		}
	}
	seen := map[string]bool{}
	var out []model.Comment
	for _, r := range reviews {
		for _, f := range r.Findings {
			if f.Path == "" || f.Line <= 0 {
				continue
			}
			key := FindingKey(itemID, r.GroupID, f)
			if marked[key] || seen[key] {
				continue
			}
			seen[key] = true
			c := model.Comment{
				ID: AIDraftID(key), ItemID: itemID, UnitID: f.UnitID, Path: f.Path, Line: f.Line,
				Side: findingSide(f), Body: DraftBody(f), Author: r.Model,
				Source: model.CommentAIDraft, State: model.CommentDraft,
				FindingKey: key, Severity: f.Severity, CreatedAt: now,
			}
			out = append(out, Anchor(c, units))
		}
	}
	sortByPosition(out)
	return out
}

// SyncAIDrafts compares an item's stored comments with the wanted ai-drafts
// (AIDrafts): put holds wanted drafts that are new or whose position, anchor
// or body changed (keeping the stored CreatedAt); del the ids of stored
// ai-drafts no longer wanted, sorted.
func SyncAIDrafts(stored, want []model.Comment) (put []model.Comment, del []string) {
	have := map[string]model.Comment{}
	for _, s := range stored {
		if s.Source == model.CommentAIDraft {
			have[s.ID] = s
		}
	}
	wanted := make(map[string]bool, len(want))
	for _, w := range want {
		wanted[w.ID] = true
		s, ok := have[w.ID]
		if ok {
			w.CreatedAt, w.UpdatedAt = s.CreatedAt, s.UpdatedAt
			if s == w {
				continue
			}
		}
		put = append(put, w)
	}
	for id := range have {
		if !wanted[id] {
			del = append(del, id)
		}
	}
	slices.Sort(del)
	return put, del
}

// Accept turns an ai-draft into an editable user draft with a new id,
// author and creation time (Source user, State draft, FindingKey and
// anchor kept, Severity cleared).
func Accept(ai model.Comment, id, author string, now time.Time) model.Comment {
	c := ai
	c.ID, c.Author, c.Source, c.State, c.Severity = id, author, model.CommentUser, model.CommentDraft, ""
	c.CreatedAt, c.UpdatedAt = now, now
	return c
}

// ForSubmit picks the comments a review submission sends: user drafts of
// the item (only those in ids when ids is non-empty), in path/line order.
// Outdated drafts and drafts without a path, line or body go to skipped;
// ai-drafts and pending/posted/github comments are never sent.
func ForSubmit(cs []model.Comment, ids []string) (send, skipped []model.Comment) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for _, c := range cs {
		if c.Source != model.CommentUser || c.State != model.CommentDraft || (len(want) > 0 && !want[c.ID]) {
			continue
		}
		if c.Outdated || c.Path == "" || c.Line <= 0 || strings.TrimSpace(c.Body) == "" {
			skipped = append(skipped, c)
			continue
		}
		send = append(send, c)
	}
	sortByPosition(send)
	sortByPosition(skipped)
	return send, skipped
}

// NextViewMark returns the view mark after the user looked at head of an
// item: unchanged when head is "" or the stored HeadSHA; otherwise HeadSHA
// = head, PrevHeadSHA = the old HeadSHA (when there was one) and At = now.
func NextViewMark(prev model.ViewMark, ok bool, itemID, head string, now time.Time) model.ViewMark {
	if head == "" || (ok && prev.HeadSHA == head) {
		if !ok {
			prev.ItemID = itemID
		}
		return prev
	}
	v := model.ViewMark{ItemID: itemID, HeadSHA: head, At: now}
	if ok {
		v.PrevHeadSHA = prev.HeadSHA
	}
	return v
}

// SinceBase returns the head "Since you looked" compares the analysed head
// with, and its source: with a view mark, PrevHeadSHA when HeadSHA is the
// analysed head, else HeadSHA (model.SinceViewed); when that gives no base
// (no mark, or the first head looked at is the analysed one),
// it.ViewerReviewedSHA (model.SinceReviewed). ("", "") when there is no
// base or it equals the analysed head.
func SinceBase(it model.Item, analysedHead string, mark model.ViewMark, ok bool) (base, source string) {
	if ok {
		b := mark.HeadSHA
		if b == analysedHead {
			b = mark.PrevHeadSHA
		}
		if b != "" && b != analysedHead {
			return b, model.SinceViewed
		}
	}
	if r := it.ViewerReviewedSHA; r != "" && r != analysedHead {
		return r, model.SinceReviewed
	}
	return "", ""
}

// ChangedUnits returns the ids of cur's units that are not in old (unit ids
// are content hashes), in cur's order; never nil.
func ChangedUnits(old, cur []model.ChangeUnit) []string {
	had := make(map[string]bool, len(old))
	for _, u := range old {
		had[u.ID] = true
	}
	out := []string{}
	for _, u := range cur {
		if !had[u.ID] {
			out = append(out, u.ID)
		}
	}
	return out
}

// Previous returns the user's earlier top-level comments that "Since you
// looked" asks about: Author == viewer (case-insensitive, viewer non-empty),
// Source github (or user with State posted), InReplyTo 0, CommitSHA !=
// analysedHead. A local posted copy whose GHID also appears as a github
// comment is dropped. In path/line order.
func Previous(cs []model.Comment, viewer, analysedHead string) []model.Comment {
	if viewer == "" {
		return nil
	}
	onGitHub := map[int64]bool{}
	for _, c := range cs {
		if c.Source == model.CommentGitHub && c.GHID != 0 {
			onGitHub[c.GHID] = true
		}
	}
	var out []model.Comment
	for _, c := range cs {
		if !strings.EqualFold(c.Author, viewer) || c.InReplyTo != 0 || c.CommitSHA == analysedHead {
			continue
		}
		switch {
		case c.Source == model.CommentGitHub:
		case c.Source == model.CommentUser && c.State == model.CommentPosted && (c.GHID == 0 || !onGitHub[c.GHID]):
		default:
			continue
		}
		out = append(out, c)
	}
	sortByPosition(out)
	return out
}

// maxExcerptRunes is the longest Excerpt.
const maxExcerptRunes = 80

// Excerpt is the first non-blank line of a comment body, trimmed and cut to
// 80 runes with "…".
func Excerpt(body string) string {
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if r := []rune(ln); len(r) > maxExcerptRunes {
			return string(r[:maxExcerptRunes-1]) + "…"
		}
		return ln
	}
	return ""
}

// sortByPosition orders comments by path, line, start line, then id.
func sortByPosition(cs []model.Comment) {
	slices.SortStableFunc(cs, func(a, b model.Comment) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line), cmp.Compare(a.StartLine, b.StartLine), cmp.Compare(a.ID, b.ID))
	})
}
