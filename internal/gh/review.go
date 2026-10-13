package gh

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// ReviewRequest is the exact REST body of POST
// /repos/{o}/{r}/pulls/{n}/reviews. API.SubmitReview sends it and
// Fake.SubmitReview records it (Fake.Payloads), so tests assert the bytes
// GitHub would receive.
type ReviewRequest struct {
	CommitID string          `json:"commit_id,omitempty"`
	Body     string          `json:"body,omitempty"`
	Event    string          `json:"event"`
	Comments []ReviewComment `json:"comments,omitempty"`
}

// ReviewComment is one line comment of a ReviewRequest. Side is "LEFT"
// (old) or "RIGHT" (new); StartLine/StartSide are set only for multi-line
// ranges (StartLine < Line on one side, or a start on the other side).
type ReviewComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
}

// ErrBadReview is wrapped by BuildReviewRequest for submissions GitHub
// would reject.
var ErrBadReview = errors.New("gh: invalid review")

// RESTSide maps a diff side to GitHub's LEFT/RIGHT ("" counts as new).
func RESTSide(s model.Side) string {
	if s == model.SideOld {
		return "LEFT"
	}
	return "RIGHT"
}

// BuildReviewRequest validates r and returns its REST body. The event must
// be APPROVE, REQUEST_CHANGES or COMMENT; REQUEST_CHANGES and COMMENT need
// a body or at least one comment. Every comment needs a path, a line > 0 and
// a non-blank body. StartSide "" means Side. On one side, a StartLine above
// Line is an error and a StartLine equal to Line (or 0) is a single-line
// comment; a range whose start side differs from its side is sent as is.
func BuildReviewRequest(r ReviewSubmission) (ReviewRequest, error) {
	req := ReviewRequest{CommitID: r.CommitSHA, Body: r.Body, Event: string(r.Event)}
	switch r.Event {
	case EventApprove:
	case EventRequestChanges, EventComment:
		if strings.TrimSpace(r.Body) == "" && len(r.Comments) == 0 {
			return ReviewRequest{}, fmt.Errorf("%w: %s needs a summary or comments", ErrBadReview, r.Event)
		}
	default:
		return ReviewRequest{}, fmt.Errorf("%w: unknown event %q", ErrBadReview, r.Event)
	}
	for i, c := range r.Comments {
		switch {
		case c.Path == "":
			return ReviewRequest{}, fmt.Errorf("%w: comment %d has no path", ErrBadReview, i+1)
		case c.Line <= 0:
			return ReviewRequest{}, fmt.Errorf("%w: comment %d on %s has no line", ErrBadReview, i+1, c.Path)
		case strings.TrimSpace(c.Body) == "":
			return ReviewRequest{}, fmt.Errorf("%w: comment %d on %s:%d is empty", ErrBadReview, i+1, c.Path, c.Line)
		}
		rc := ReviewComment{Path: c.Path, Line: c.Line, Side: RESTSide(c.Side), Body: c.Body}
		ss := c.StartSide
		if ss == "" {
			ss = c.Side
		}
		cross := c.StartLine > 0 && RESTSide(ss) != rc.Side
		switch {
		case cross:
			// Start and end on different sides (LEFT→RIGHT): the numbers are
			// not comparable, GitHub takes the range as is.
			rc.StartLine, rc.StartSide = c.StartLine, RESTSide(ss)
		case c.StartLine > c.Line:
			return ReviewRequest{}, fmt.Errorf("%w: comment %d on %s starts at %d after its line %d", ErrBadReview, i+1, c.Path, c.StartLine, c.Line)
		case c.StartLine > 0 && c.StartLine < c.Line:
			rc.StartLine, rc.StartSide = c.StartLine, RESTSide(ss)
		}
		req.Comments = append(req.Comments, rc)
	}
	return req, nil
}
