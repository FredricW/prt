package gh

import (
	"strings"

	"github.com/shurcooL/githubv4"

	"github.com/fredricw/prt/internal/model"
)

// prNode is the GraphQL selection for one pull request, shared by search and
// repository.pullRequest queries.
type prNode struct {
	Number      int
	Title       string
	Body        string
	Author      struct{ Login string }
	State       string
	IsDraft     bool
	HeadRefName string
	HeadRefOid  string
	BaseRefName string
	BaseRefOid  string
	URL         string `graphql:"url"`
	Labels      struct {
		Nodes []struct{ Name string }
	} `graphql:"labels(first: 20)"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer struct {
				User struct{ Login string } `graphql:"... on User"`
			}
		}
	} `graphql:"reviewRequests(first: 20)"`
	Additions          int
	Deletions          int
	ChangedFiles       int
	CreatedAt          githubv4.DateTime
	UpdatedAt          githubv4.DateTime
	ViewerLatestReview *struct{ State string }
	Comments           struct {
		Nodes []struct{ ViewerDidAuthor bool }
	} `graphql:"comments(last: 20)"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct{ State string }
			}
		}
	} `graphql:"commits(last: 1)"`
}

// searchQuery is `search(type: ISSUE, ...)` restricted to pull requests.
type searchQuery struct {
	Search struct {
		PageInfo struct {
			EndCursor   githubv4.String
			HasNextPage bool
		}
		Nodes []struct {
			PullRequest prNode `graphql:"... on PullRequest"`
		}
	} `graphql:"search(type: ISSUE, query: $query, first: $first, after: $after)"`
}

// prQuery fetches one pull request by number.
type prQuery struct {
	Repository struct {
		PullRequest prNode `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// prFromNode maps a GraphQL pull request onto PR. viewer is the authenticated
// login, used for ReviewRequested.
func prFromNode(n prNode, viewer string) PR {
	p := PR{
		Number:       n.Number,
		Title:        n.Title,
		Body:         n.Body,
		Author:       n.Author.Login,
		State:        strings.ToLower(n.State),
		Draft:        n.IsDraft,
		HeadRef:      n.HeadRefName,
		HeadSHA:      n.HeadRefOid,
		BaseRef:      n.BaseRefName,
		BaseSHA:      n.BaseRefOid,
		URL:          n.URL,
		Additions:    n.Additions,
		Deletions:    n.Deletions,
		ChangedFiles: n.ChangedFiles,
		CreatedAt:    n.CreatedAt.Time,
		UpdatedAt:    n.UpdatedAt.Time,
		CI:           model.CIUnknown,
	}
	for _, l := range n.Labels.Nodes {
		p.Labels = append(p.Labels, l.Name)
	}
	for _, r := range n.ReviewRequests.Nodes {
		login := r.RequestedReviewer.User.Login
		if login == "" { // team or bot request
			continue
		}
		p.Reviewers = append(p.Reviewers, login)
		if viewer != "" && strings.EqualFold(login, viewer) {
			p.ReviewRequested = true
		}
	}
	if r := n.ViewerLatestReview; r != nil {
		p.ViewerApproved = r.State == "APPROVED"
		p.ViewerCommented = r.State != "PENDING"
	}
	for _, c := range n.Comments.Nodes {
		p.ViewerCommented = p.ViewerCommented || c.ViewerDidAuthor
	}
	if len(n.Commits.Nodes) > 0 {
		if roll := n.Commits.Nodes[len(n.Commits.Nodes)-1].Commit.StatusCheckRollup; roll != nil {
			p.CI = ciFromRollup(roll.State)
		}
	}
	return p
}

// ciFromRollup maps a GitHub StatusState onto model.CIState.
func ciFromRollup(state string) model.CIState {
	switch state {
	case "SUCCESS":
		return model.CIGreen
	case "FAILURE", "ERROR":
		return model.CIRed
	case "PENDING", "EXPECTED":
		return model.CIPending
	default:
		return model.CIUnknown
	}
}
