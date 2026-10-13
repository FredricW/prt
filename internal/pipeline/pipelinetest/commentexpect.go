package pipelinetest

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fredricw/prt/internal/comments"
	"github.com/fredricw/prt/internal/model"
)

// ReviewedSHA101 is #101's viewerReviewedSha in prs.json: the earlier head
// the viewer reviewed, generated at refs/fixture/01-user-profile-refactor/v1
// (one-hour TTL, clearProfileCache without inflight.clear()).
const ReviewedSHA101 = "e661461bccca9ae9395ab9e74da9836914d81a0c"

// The fixture's review threads on #101 (prs.json "threads").
var (
	gitHubComments101   = []string{"gh-5001", "gh-5002", "gh-5003", "gh-5004", "gh-5005"}
	resolvedComments101 = []string{"gh-5004", "gh-5005"} // the token thread
)

// The viewer's earlier comments "Since you looked" asks about, in order.
const (
	ttlComment   = "gh-5001" // line 6: the TTL is an hour; addressed by the push
	catchComment = "gh-5003" // line 75: errors swallowed; not addressed
	cachePath    = "web/src/hooks/useProfileCache.ts"
)

// CommentExpectations returns how the fixture run's comment layer deviates
// from the fixture (M4), independent of Jev's answers:
//
//   - #101 holds the five GitHub comments of its three threads (Source
//     github, the token thread's two resolved, the others posted);
//   - every PR's ai-drafts are exactly the ones its routing rows' findings
//     call for (comments.AIDrafts; no finding is marked in the fixture) and
//     there are no other comments;
//   - #101's Since block: base ReviewedSHA101 from the viewer's review,
//     changed units exactly the useProfileCache.ts units that differ from
//     that head (the TTL constant and clearProfileCache), and rows for
//     gh-5001 (line 6) then gh-5003 (line 75); unknown rows carry the
//     "labels unavailable" note;
//   - no other PR has a Since block.
func CommentExpectations(outs map[int]Outcome) []string {
	var out []string
	for _, n := range PRNumbers {
		o := outs[n]
		out = append(out, aiDraftsMatch(n, o)...)
		if n != 101 && o.Agg.Since != nil {
			out = append(out, fmt.Sprintf("#%d: unexpected Since block %+v", n, *o.Agg.Since))
		}
		if n != 101 {
			out = append(out, onlyAIDrafts(n, o)...)
		}
	}
	o := outs[101]
	out = append(out, gitHub101(o)...)
	return append(out, since101(o)...)
}

// aiDraftsMatch checks that the stored ai-drafts are the ones the rows ask
// for.
func aiDraftsMatch(n int, o Outcome) []string {
	var want []string
	for _, c := range comments.AIDrafts(o.Item.ID, o.Reviews, o.Units, nil, Now) {
		want = append(want, c.ID)
	}
	var got []string
	for _, c := range o.Comments {
		if c.Source == model.CommentAIDraft {
			got = append(got, c.ID)
		}
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		return []string{fmt.Sprintf("#%d: ai-drafts %v, want %v (one per finding with a line)", n, got, want)}
	}
	return nil
}

// onlyAIDrafts checks that a PR without threads has no other comments.
func onlyAIDrafts(n int, o Outcome) []string {
	var out []string
	for _, c := range o.Comments {
		if c.Source != model.CommentAIDraft {
			out = append(out, fmt.Sprintf("#%d: unexpected %s comment %s", n, c.Source, c.ID))
		}
	}
	return out
}

// gitHub101 checks #101's GitHub comments.
func gitHub101(o Outcome) []string {
	var out, got []string
	for _, c := range o.Comments {
		switch c.Source {
		case model.CommentGitHub:
			got = append(got, c.ID)
			want := model.CommentPosted
			if slices.Contains(resolvedComments101, c.ID) {
				want = model.CommentResolved
			}
			if c.State != want || c.ThreadID == "" || c.ItemID != o.Item.ID {
				out = append(out, fmt.Sprintf("#101: comment %s state %q thread %q item %q, want %q in a thread of %s", c.ID, c.State, c.ThreadID, c.ItemID, want, o.Item.ID))
			}
		case model.CommentAIDraft:
		default:
			out = append(out, fmt.Sprintf("#101: unexpected %s comment %s", c.Source, c.ID))
		}
	}
	if !slices.Equal(got, gitHubComments101) {
		out = append(out, fmt.Sprintf("#101: GitHub comments %v, want %v", got, gitHubComments101))
	}
	return out
}

// since101 checks #101's Since block (answers excepted).
func since101(o Outcome) []string {
	s := o.Agg.Since
	if s == nil {
		return []string{"#101: no Since block (viewer reviewed " + ReviewedSHA101[:7] + ")"}
	}
	var out []string
	if s.BaseSHA != ReviewedSHA101 || s.Source != model.SinceReviewed || s.HeadSHA != o.Item.HeadSHA {
		out = append(out, fmt.Sprintf("#101: Since base %s (%s) head %s, want %s (reviewed) head %s", s.BaseSHA, s.Source, s.HeadSHA, ReviewedSHA101, o.Item.HeadSHA))
	}
	out = append(out, changedUnits101(o, s.ChangedUnits)...)
	var ids []string
	for _, r := range s.Comments {
		ids = append(ids, fmt.Sprintf("%s:%d", r.CommentID, r.Line))
		if r.Path != cachePath || r.Excerpt == "" || r.Resolved {
			out = append(out, fmt.Sprintf("#101: Since row %+v, want %s, an excerpt, unresolved", r, cachePath))
		}
		if !r.Known && !strings.Contains(s.Note, "labels unavailable") {
			out = append(out, fmt.Sprintf("#101: unknown Since row %s without the labels-unavailable note (%q)", r.CommentID, s.Note))
		}
	}
	if want := []string{ttlComment + ":6", catchComment + ":75"}; !slices.Equal(ids, want) {
		out = append(out, fmt.Sprintf("#101: Since comments %v, want %v", ids, want))
	}
	return out
}

// changedUnits101 checks that the changed units are exactly the TTL
// constant and clearProfileCache units of useProfileCache.ts.
func changedUnits101(o Outcome, ids []string) []string {
	byID := map[string]model.ChangeUnit{}
	for _, u := range o.Units {
		byID[u.ID] = u
	}
	sawTTL, sawClear := false, false
	var out []string
	for _, id := range ids {
		u, ok := byID[id]
		switch {
		case !ok:
			out = append(out, "#101: Since changed unit "+id+" is not a current unit")
		case u.Path == cachePath && u.Symbol == "clearProfileCache":
			sawClear = true
		case u.Path == cachePath && strings.Contains(u.Diff, "PROFILE_TTL_MS"):
			sawTTL = true
		default:
			out = append(out, fmt.Sprintf("#101: Since changed unit %s %s did not change since %s", u.Path, u.Symbol, ReviewedSHA101[:7]))
		}
	}
	if !sawTTL || !sawClear || len(ids) != 2 {
		out = append(out, fmt.Sprintf("#101: Since changed units %v (ttl %v, clearProfileCache %v), want exactly the TTL constant and clearProfileCache", ids, sawTTL, sawClear))
	}
	return out
}

// AddressedExpectations checks #101's recorded comment_addressed answers:
// gh-5001 (the TTL, fixed by the push) addressed and gh-5003 (errors still
// swallowed) not. recorded is false while any row is unknown (answers not
// recorded yet); problems are then nil.
func AddressedExpectations(o Outcome) (problems []string, recorded bool) {
	s := o.Agg.Since
	if s == nil || len(s.Comments) == 0 {
		return []string{"#101: no Since comments to check"}, true
	}
	want := map[string]bool{ttlComment: true, catchComment: false}
	for _, r := range s.Comments {
		if !r.Known {
			return nil, false
		}
	}
	for _, r := range s.Comments {
		if w, ok := want[r.CommentID]; ok && r.Addressed != w {
			problems = append(problems, fmt.Sprintf("#101: %s addressed = %v (p %.2f), want %v", r.CommentID, r.Addressed, r.P, w))
		}
	}
	return problems, true
}
