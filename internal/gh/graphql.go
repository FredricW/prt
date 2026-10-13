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
	IsCrossRepository  bool
	CreatedAt          githubv4.DateTime
	UpdatedAt          githubv4.DateTime
	ViewerLatestReview *struct {
		State  string
		Commit *struct{ Oid string }
	}
	Comments struct {
		Nodes []struct {
			ViewerDidAuthor bool
			Body            string
		}
	} `graphql:"comments(last: 20)"`
	ReviewThreads struct {
		Nodes []struct{ IsResolved bool }
	} `graphql:"reviewThreads(first: 100)"`
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
	p.CrossRepo = n.IsCrossRepository
	if r := n.ViewerLatestReview; r != nil && r.State != "PENDING" {
		p.ViewerApproved = r.State == "APPROVED"
		p.ViewerCommented = true
		if r.State != "DISMISSED" && r.Commit != nil {
			p.ViewerReviewedSHA = r.Commit.Oid
		}
	}
	for _, c := range n.Comments.Nodes {
		p.ViewerCommented = p.ViewerCommented || c.ViewerDidAuthor
		switch {
		case c.ViewerDidAuthor:
			p.ViewerAddressed = false // the viewer has answered since
		case mentions(c.Body, viewer):
			p.ViewerAddressed = true
		}
	}
	for _, t := range n.ReviewThreads.Nodes {
		if !t.IsResolved {
			p.OpenThreads++
		}
	}
	if len(n.Commits.Nodes) > 0 {
		if roll := n.Commits.Nodes[len(n.Commits.Nodes)-1].Commit.StatusCheckRollup; roll != nil {
			p.CI = ciFromRollup(roll.State)
		}
	}
	return p
}

// mentions reports whether body @-mentions login (case-insensitive; the
// mention must end at a character that cannot continue a login).
func mentions(body, login string) bool {
	if login == "" {
		return false
	}
	lb, needle := strings.ToLower(body), "@"+strings.ToLower(login)
	for i := 0; ; {
		j := strings.Index(lb[i:], needle)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(needle)
		before := start == 0 || !isLoginChar(lb[start-1])
		if before && (end == len(lb) || !isLoginChar(lb[end])) {
			return true
		}
		i = end
	}
}

func isLoginChar(b byte) bool {
	return b == '-' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z'
}

// openPRsQuery lists a repository's open pull requests with changed paths.
type openPRsQuery struct {
	Repository struct {
		PullRequests struct {
			PageInfo struct {
				EndCursor   githubv4.String
				HasNextPage bool
			}
			Nodes []struct {
				PullRequest prNode `graphql:"... on PullRequest"`
				Files       struct {
					Nodes []struct{ Path string }
				} `graphql:"files(first: 100)"`
			}
		} `graphql:"pullRequests(states: OPEN, first: $first, after: $after, orderBy: {field: UPDATED_AT, direction: DESC})"`
	} `graphql:"repository(owner: $owner, name: $name)"`
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
