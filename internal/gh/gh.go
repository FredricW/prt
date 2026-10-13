// Package gh is prt's GitHub boundary. All GitHub reads and writes go through
// the Client interface; the real implementation (API) uses go-github for REST
// and githubv4 for search and review threads, authenticated with the token
// from `gh auth token`. Tests use Fake.
package gh

import (
	"context"
	"errors"
	"time"

	"github.com/fredricw/prt/internal/model"
)

// ErrNotImplemented marks API methods not built yet (filled in by later milestones).
var ErrNotImplemented = errors.New("gh: not implemented")

// ErrNoToken is returned when no GitHub token is available; prt then runs
// with local branches only.
var ErrNoToken = errors.New("gh: no GitHub token (run `gh auth login`)")

// ErrPRNotFound is wrapped by Client.PR when the repository has no pull
// request with that number (search-and-add shows "no PR #N" instead of a
// failure).
var ErrPRNotFound = errors.New("gh: PR not found")

// Repo identifies a GitHub repository.
type Repo struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// String returns "owner/name".
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// PR is the PR metadata prt needs for picker and overview.
type PR struct {
	Number          int      `json:"number"`
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	Author          string   `json:"author"`
	State           string   `json:"state"` // open, closed, merged
	Draft           bool     `json:"draft"`
	HeadRef         string   `json:"headRef"`
	HeadSHA         string   `json:"headSha"`
	BaseRef         string   `json:"baseRef"`
	BaseSHA         string   `json:"baseSha"`
	URL             string   `json:"url"`
	Labels          []string `json:"labels,omitempty"`
	Reviewers       []string `json:"reviewers,omitempty"` // requested reviewers
	ReviewRequested bool     `json:"reviewRequested"`     // viewer is a requested reviewer
	ViewerApproved  bool     `json:"viewerApproved"`
	ViewerCommented bool     `json:"viewerCommented"` // informational; not a Needs-you trigger
	// ViewerReviewedSHA is the commit the viewer's latest submitted review
	// was made on ("" when the viewer has not reviewed). A head that moved
	// past it means "re-review after a push".
	ViewerReviewedSHA string `json:"viewerReviewedSha,omitempty"`
	// ViewerReviewedAt is when that review was submitted (zero when
	// unknown).
	ViewerReviewedAt time.Time `json:"viewerReviewedAt,omitzero"`
	// ViewerAddressed is true when a recent comment by someone else
	// @-mentions the viewer and the viewer has not commented since.
	ViewerAddressed bool `json:"viewerAddressed,omitempty"`
	// CrossRepo is true when the head lives in a fork, so HeadRef names a
	// branch in another repository.
	CrossRepo bool `json:"crossRepo,omitempty"`
	// OpenThreads counts unresolved review threads (first 100).
	OpenThreads  int           `json:"openThreads,omitempty"`
	CI           model.CIState `json:"ci"`
	Additions    int           `json:"additions"`
	Deletions    int           `json:"deletions"`
	ChangedFiles int           `json:"changedFiles"`
	// Files lists changed paths; set only by ListOpenPRs (first 100 files).
	Files     []string  `json:"files,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Thread is a review thread with its comments (GraphQL reviewThreads).
//
// Line/StartLine are the thread's current position on Side (GitHub `line`
// and `startLine`; 0 when outdated, in which case OriginalLine is set).
// Each comment is a model.Comment with Source github, ThreadID = ID, Path,
// Line and Side copied from the thread, GHID (databaseId), Author (login),
// Body, CreatedAt, CommitSHA (originalCommit oid), InReplyTo = the first
// comment's GHID for replies, and State posted (resolved when the thread
// is resolved). Comments are in thread order.
type Thread struct {
	ID           string          `json:"id"` // GraphQL node id
	Path         string          `json:"path"`
	Line         int             `json:"line"`
	StartLine    int             `json:"startLine,omitempty"`
	OriginalLine int             `json:"originalLine,omitempty"`
	Side         model.Side      `json:"side"`
	Resolved     bool            `json:"resolved"`
	Outdated     bool            `json:"outdated"`
	Comments     []model.Comment `json:"comments"`
}

// ReviewEvent is the verdict of a submitted review.
type ReviewEvent string

// Review events (REST values).
const (
	EventApprove        ReviewEvent = "APPROVE"
	EventRequestChanges ReviewEvent = "REQUEST_CHANGES"
	EventComment        ReviewEvent = "COMMENT"
)

// ReviewSubmission is one GitHub review containing all drafts as line comments
// (REST POST /repos/{o}/{r}/pulls/{n}/reviews). BuildReviewRequest turns it
// into the exact REST body.
type ReviewSubmission struct {
	CommitSHA string          `json:"commitId"`
	Event     ReviewEvent     `json:"event"`
	Body      string          `json:"body,omitempty"`
	Comments  []model.Comment `json:"comments"` // path, line, side, body (startLine/startSide for ranges)
}

// SubmittedReview is the outcome of SubmitReview: the review's id and URL
// and the submitted comments with State posted, GHID and ReviewID set (in
// submission order; GHID 0 when GitHub did not report it).
type SubmittedReview struct {
	ID       int64           `json:"id"`
	URL      string          `json:"url,omitempty"`
	Comments []model.Comment `json:"comments"`
}

// NewPR is the input for creating a PR from a branch (`P` on a branch).
type NewPR struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Draft bool   `json:"draft"`
}

// Client is everything prt asks of GitHub.
type Client interface {
	// Viewer returns the authenticated user's login.
	Viewer(ctx context.Context) (string, error)
	// SearchPRs runs a GitHub issue search (e.g. "repo:o/r is:pr is:open involves:@me").
	SearchPRs(ctx context.Context, query string, limit int) ([]PR, error)
	// ListOpenPRs returns up to limit open PRs of the repo, most recently
	// updated first, with Files set (used to match owned-path globs locally,
	// since issue search has no path qualifier).
	ListOpenPRs(ctx context.Context, limit int) ([]PR, error)
	// PR returns one PR's metadata.
	PR(ctx context.Context, number int) (PR, error)
	// PRDiff returns the PR's unified diff (fallback when the head is not local).
	PRDiff(ctx context.Context, number int) (string, error)
	// Threads returns the PR's review threads (GraphQL reviewThreads, all
	// pages) with their comments.
	Threads(ctx context.Context, number int) ([]Thread, error)
	// SubmitReview posts one review with line comments (REST POST
	// /repos/{o}/{r}/pulls/{n}/reviews with BuildReviewRequest's body).
	SubmitReview(ctx context.Context, number int, r ReviewSubmission) (SubmittedReview, error)
	// Reply adds a reply to a review thread (GraphQL
	// addPullRequestReviewThreadReply) and returns it as a github comment
	// of that thread.
	Reply(ctx context.Context, threadID, body string) (model.Comment, error)
	// ResolveThread marks a thread resolved (GraphQL resolveReviewThread).
	ResolveThread(ctx context.Context, threadID string) error
	// CreatePR opens a PR (REST POST /repos/{o}/{r}/pulls) and returns it.
	CreatePR(ctx context.Context, p NewPR) (PR, error)
	// CODEOWNERS returns the CODEOWNERS file content at ref, or "" if absent.
	CODEOWNERS(ctx context.Context, ref string) (string, error)
}
