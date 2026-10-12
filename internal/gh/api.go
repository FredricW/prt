package gh

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"

	"github.com/google/go-github/v66/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"

	"github.com/fredricw/prt/internal/model"
)

// TokenFromGH runs `gh auth token`. The token is never logged or stored.
func TokenFromGH(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrNoToken, err)
	}
	tok := strings.TrimSpace(out.String())
	if tok == "" {
		return "", ErrNoToken
	}
	return tok, nil
}

var remoteRe = regexp.MustCompile(`github\.com[:/]([^/]+)/([^/]+?)(?:\.git)?/?$`)

// ParseRemote extracts owner/name from a GitHub remote URL (https or ssh).
func ParseRemote(url string) (Repo, bool) {
	m := remoteRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return Repo{}, false
	}
	return Repo{Owner: m[1], Name: m[2]}, true
}

// API implements Client against api.github.com.
type API struct {
	Repo Repo
	REST *github.Client
	GQL  *githubv4.Client
}

var _ Client = (*API)(nil)

// NewAPI builds an API client with a static token.
func NewAPI(ctx context.Context, repo Repo, token string) *API {
	hc := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	return newAPI(repo, hc)
}

func newAPI(repo Repo, hc *http.Client) *API {
	return &API{Repo: repo, REST: github.NewClient(hc), GQL: githubv4.NewClient(hc)}
}

// Viewer implements Client. TODO(M0).
func (a *API) Viewer(context.Context) (string, error) { return "", ErrNotImplemented }

// SearchPRs implements Client. TODO(M0): githubv4 search.
func (a *API) SearchPRs(context.Context, string, int) ([]PR, error) { return nil, ErrNotImplemented }

// PR implements Client. TODO(M0).
func (a *API) PR(context.Context, int) (PR, error) { return PR{}, ErrNotImplemented }

// PRDiff implements Client. TODO(M0).
func (a *API) PRDiff(context.Context, int) (string, error) { return "", ErrNotImplemented }

// Threads implements Client. TODO(M4).
func (a *API) Threads(context.Context, int) ([]Thread, error) { return nil, ErrNotImplemented }

// SubmitReview implements Client. TODO(M4).
func (a *API) SubmitReview(context.Context, int, ReviewSubmission) ([]model.Comment, error) {
	return nil, ErrNotImplemented
}

// Reply implements Client. TODO(M4).
func (a *API) Reply(context.Context, int, int64, string) (model.Comment, error) {
	return model.Comment{}, ErrNotImplemented
}

// ResolveThread implements Client. TODO(M4).
func (a *API) ResolveThread(context.Context, string) error { return ErrNotImplemented }

// CreatePR implements Client. TODO(M4).
func (a *API) CreatePR(context.Context, NewPR) (PR, error) { return PR{}, ErrNotImplemented }

// CODEOWNERS implements Client. TODO(M1).
func (a *API) CODEOWNERS(context.Context, string) (string, error) { return "", ErrNotImplemented }
