package gh

import (
	"context"
	"fmt"
	"strconv"

	"github.com/shurcooL/githubv4"

	"github.com/fredricw/prt/internal/model"
)

// CommentID is the model.Comment id of a GitHub comment ("gh-<databaseId>").
// It must equal comments.GitHubID (gh cannot import internal/comments, so
// the format is replicated here and pinned by a test).
func CommentID(ghID int64) string { return "gh-" + strconv.FormatInt(ghID, 10) }

// reviewCommentNode is the GraphQL selection for one review comment, shared
// by reviewThreads and the reply mutation.
type reviewCommentNode struct {
	DatabaseID     int64 `graphql:"databaseId"`
	Body           string
	CreatedAt      githubv4.DateTime
	Author         *struct{ Login string }
	OriginalCommit *struct{ Oid string }
	ReplyTo        *struct {
		DatabaseID int64 `graphql:"databaseId"`
	}
}

// threadNode is the GraphQL selection for one review thread.
type threadNode struct {
	ID           string
	IsResolved   bool
	IsOutdated   bool
	Path         string
	Line         *int
	StartLine    *int
	OriginalLine *int
	DiffSide     string
	Comments     struct {
		Nodes []reviewCommentNode
	} `graphql:"comments(first: 100)"`
}

// threadsQuery is one page of a pull request's review threads.
type threadsQuery struct {
	Repository struct {
		PullRequest struct {
			ReviewThreads struct {
				PageInfo struct {
					EndCursor   githubv4.String
					HasNextPage bool
				}
				Nodes []threadNode
			} `graphql:"reviewThreads(first: 100, after: $after)"`
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// Threads implements Client: every review thread of the PR (all pages), with
// up to 100 comments per thread, converted as the Thread doc describes.
func (a *API) Threads(ctx context.Context, number int) ([]Thread, error) {
	var out []Thread
	var after *githubv4.String
	for {
		var q threadsQuery
		vars := map[string]any{
			"owner":  githubv4.String(a.Repo.Owner),
			"name":   githubv4.String(a.Repo.Name),
			"number": githubv4.Int(int32(number)), // PR numbers fit in int32
			"after":  after,
		}
		if err := a.GQL.Query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("gh: review threads of #%d: %w", number, err)
		}
		conn := q.Repository.PullRequest.ReviewThreads
		for _, n := range conn.Nodes {
			out = append(out, threadFromNode(n, number))
		}
		if !conn.PageInfo.HasNextPage {
			return out, nil
		}
		cur := conn.PageInfo.EndCursor
		after = &cur
	}
}

// sideFromDiffSide maps GitHub's DiffSide (LEFT/RIGHT) onto model.Side.
func sideFromDiffSide(s string) model.Side {
	if s == "LEFT" {
		return model.SideOld
	}
	return model.SideNew
}

// intOr0 dereferences a nullable GraphQL Int.
func intOr0(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// threadFromNode converts a GraphQL review thread of PR number into a Thread.
func threadFromNode(n threadNode, number int) Thread {
	t := Thread{
		ID:           n.ID,
		Path:         n.Path,
		Line:         intOr0(n.Line),
		StartLine:    intOr0(n.StartLine),
		OriginalLine: intOr0(n.OriginalLine),
		Side:         sideFromDiffSide(n.DiffSide),
		Resolved:     n.IsResolved,
		Outdated:     n.IsOutdated,
	}
	state := model.CommentPosted
	if t.Resolved {
		state = model.CommentResolved
	}
	line, outdated := t.Line, t.Outdated
	if line == 0 {
		line, outdated = t.OriginalLine, true
	}
	t.Comments = make([]model.Comment, 0, len(n.Comments.Nodes))
	for i, cn := range n.Comments.Nodes {
		c := commentFromNode(cn)
		c.ItemID = model.PRItemID(number)
		c.ThreadID = t.ID
		c.Path, c.Side, c.Line, c.StartLine = t.Path, t.Side, line, t.StartLine
		c.State, c.Outdated = state, outdated
		if c.InReplyTo == 0 && i > 0 {
			c.InReplyTo = t.Comments[0].GHID // GitHub omitted replyTo; replies answer the first comment
		}
		t.Comments = append(t.Comments, c)
	}
	return t
}

// commentFromNode maps the comment-level fields of a GraphQL review comment
// (id, author, body, time, commit, reply target) onto a github model.Comment
// in State posted; position and thread fields are left to the caller.
func commentFromNode(n reviewCommentNode) model.Comment {
	c := model.Comment{
		ID:        CommentID(n.DatabaseID),
		GHID:      n.DatabaseID,
		Body:      n.Body,
		CreatedAt: n.CreatedAt.Time,
		Source:    model.CommentGitHub,
		State:     model.CommentPosted,
	}
	if n.Author != nil {
		c.Author = n.Author.Login
	}
	if n.OriginalCommit != nil {
		c.CommitSHA = n.OriginalCommit.Oid
	}
	if n.ReplyTo != nil {
		c.InReplyTo = n.ReplyTo.DatabaseID
	}
	return c
}
