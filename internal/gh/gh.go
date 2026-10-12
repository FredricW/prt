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

// Repo identifies a GitHub repository.
type Repo struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// String returns "owner/name".
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// PR is the PR metadata prt needs for picker and overview.
type PR struct {
	Number          int           `json:"number"`
	Title           string        `json:"title"`
	Body            string        `json:"body"`
	Author          string        `json:"author"`
	State           string        `json:"state"` // open, closed, merged
	Draft           bool          `json:"draft"`
	HeadRef         string        `json:"headRef"`
	HeadSHA         string        `json:"headSha"`
	BaseRef         string        `json:"baseRef"`
	BaseSHA         string        `json:"baseSha"`
	URL             string        `json:"url"`
	Labels          []string      `json:"labels,omitempty"`
	Reviewers       []string      `json:"reviewers,omitempty"` // requested reviewers
	ReviewRequested bool          `json:"reviewRequested"`     // viewer is a requested reviewer
	ViewerApproved  bool          `json:"viewerApproved"`
	ViewerCommented bool          `json:"viewerCommented"`
	CI              model.CIState `json:"ci"`
	Additions       int           `json:"additions"`
	Deletions       int           `json:"deletions"`
	ChangedFiles    int           `json:"changedFiles"`
	CreatedAt       time.Time     `json:"createdAt"`
	UpdatedAt       time.Time     `json:"updatedAt"`
}

// Thread is a review thread with its comments.
type Thread struct {
	ID       string          `json:"id"` // GraphQL node id
	Path     string          `json:"path"`
	Line     int             `json:"line"`
	Side     model.Side      `json:"side"`
	Resolved bool            `json:"resolved"`
	Outdated bool            `json:"outdated"`
	Comments []model.Comment `json:"comments"`
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
// (REST POST /repos/{o}/{r}/pulls/{n}/reviews).
type ReviewSubmission struct {
	CommitSHA string          `json:"commitId"`
	Event     ReviewEvent     `json:"event"`
	Body      string          `json:"body,omitempty"`
	Comments  []model.Comment `json:"comments"` // path, line, side, body (startLine for ranges)
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
	// PR returns one PR's metadata.
	PR(ctx context.Context, number int) (PR, error)
	// PRDiff returns the PR's unified diff (fallback when the head is not local).
	PRDiff(ctx context.Context, number int) (string, error)
	// Threads returns the PR's review threads.
	Threads(ctx context.Context, number int) ([]Thread, error)
	// SubmitReview posts one review with line comments; returns the posted comments with GHIDs.
	SubmitReview(ctx context.Context, number int, r ReviewSubmission) ([]model.Comment, error)
	// Reply adds a reply to a review thread.
	Reply(ctx context.Context, number int, inReplyTo int64, body string) (model.Comment, error)
	// ResolveThread marks a thread resolved.
	ResolveThread(ctx context.Context, threadID string) error
	// CreatePR opens a PR and returns it.
	CreatePR(ctx context.Context, p NewPR) (PR, error)
	// CODEOWNERS returns the CODEOWNERS file content at ref, or "" if absent.
	CODEOWNERS(ctx context.Context, ref string) (string, error)
}
