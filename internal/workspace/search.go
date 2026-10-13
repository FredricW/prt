package workspace

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

const (
	// noteNoGitHub is the search note when no searched repo has GitHub.
	noteNoGitHub = "GitHub unavailable in every repo"
	// refCandidateLimit is the hit cap of the local search that looks for
	// the repos holding an unqualified ref (the match must be exact, so the
	// substring search must not stop early).
	refCandidateLimit = 500
)

// Search implements daemon.Client. It searches the repo a qualified query
// names (with the local part; the qualifier wins over p.Repo), else
// p.Repo's repo when set, else every repo in parallel. The GitHub sources skip repos without GitHub. Hits come in
// workspace order with Repo set, ItemID prefixed and Query qualified
// ("<key>#N", "<key>:<ref>"), so adding or previewing a hit is never
// ambiguous; the limit applies per repo. Notes read "<label>: <note>",
// joined by "; ". A repo's failure is a note; only when every searched
// repo fails is it an error.
func (c *Client) Search(ctx context.Context, p proto.SearchParams) (proto.SearchResult, error) {
	res := proto.SearchResult{Source: p.Source, Query: p.Query}
	targets, query, err := c.searchTargets(p)
	if err != nil {
		return res, err
	}
	if p.Source == proto.SearchOpen || p.Source == proto.SearchClosed {
		eligible := c.gitHubMembers(targets)
		if len(eligible) == 0 {
			res.Note = noteNoGitHub
			if len(targets) == 1 {
				res.Note = label(targets[0]) + ": GitHub unavailable"
			}
			return res, nil
		}
		targets = eligible
	}
	if len(targets) == 0 {
		return res, nil
	}
	sp := proto.SearchParams{Query: query, Source: p.Source, Limit: p.Limit}
	out := fanOut(ctx, targets, 0, func(ctx context.Context, m Member) (proto.SearchResult, error) {
		return m.Client.Search(ctx, sp)
	})
	if err := ctx.Err(); err != nil {
		return res, err
	}
	var notes []string
	failed := 0
	for i, m := range targets {
		if err := out.err[i]; err != nil {
			failed++
			notes = append(notes, label(m)+": "+err.Error())
			continue
		}
		for _, h := range out.vals[i].Hits {
			res.Hits = append(res.Hits, outHit(m.Info.Key, h))
		}
		if n := out.vals[i].Note; n != "" {
			notes = append(notes, label(m)+": "+n)
		}
	}
	if failed == len(targets) {
		return res, errors.Join(out.errs(targets)...)
	}
	res.Note = strings.Join(notes, "; ")
	return res, nil
}

// searchTargets picks the members a search asks and the query they get: a
// qualified query that resolves goes to the repo it names with its local
// part, even when p.Repo (the picker's filter) names another, since the
// explicit qualifier is what the user typed; else p.Repo's repo, else every
// repo.
func (c *Client) searchTargets(p proto.SearchParams) ([]Member, string, error) {
	if repo, local, ok := ParseQualified(p.Query); ok {
		if m, err := c.Resolve(repo); err == nil {
			return []Member{m}, local, nil
		}
	}
	if p.Repo != "" {
		m, err := c.Resolve(p.Repo)
		if err != nil {
			return nil, "", err
		}
		return []Member{m}, p.Query, nil
	}
	return c.members, p.Query, nil
}

// gitHubMembers are the members of ms whose (live) status is RepoGitHub.
func (c *Client) gitHubMembers(ms []Member) []Member {
	var out []Member
	for _, m := range ms {
		if c.info(m).Status == proto.RepoGitHub {
			out = append(out, m)
		}
	}
	return out
}

// outHit rewrites a member's search hit for the workspace.
func outHit(key string, h proto.SearchHit) proto.SearchHit {
	h.Repo = key
	h.ItemID = pfx(key, h.ItemID)
	switch {
	case h.Kind == model.KindPR && h.Number > 0:
		h.Query = key + "#" + strconv.Itoa(h.Number)
	case h.Query != "":
		h.Query = key + ":" + h.Query
	case h.Ref != "":
		h.Query = key + ":" + h.Ref
	}
	return h
}

// Add implements daemon.Client: a qualified query goes to its repo with
// the local part; otherwise the one repo that has the PR or ref (see
// addTarget).
func (c *Client) Add(ctx context.Context, query string) (model.Item, error) {
	m, local, err := c.addTarget(ctx, query)
	if err != nil {
		return model.Item{}, err
	}
	it, err := m.Client.Add(ctx, local)
	return outItem(m.Info.Key, it), err
}

// Preview implements daemon.Client, choosing the repo as Add does.
func (c *Client) Preview(ctx context.Context, query string) (model.Item, error) {
	m, local, err := c.addTarget(ctx, query)
	if err != nil {
		return model.Item{}, err
	}
	it, err := m.Client.Preview(ctx, local)
	return outItem(m.Info.Key, it), err
}

// addTarget picks the member an add or preview goes to: the repo of a
// resolvable qualified query (with its local part); the only member; else
// the one member that has it — for "#N"/"N" a GitHub repo whose open
// search finds PR N, for anything else a repo whose local search finds a
// ref named exactly q. None or several is an error naming the way out.
func (c *Client) addTarget(ctx context.Context, query string) (Member, string, error) {
	q := strings.TrimSpace(query)
	if repo, local, ok := ParseQualified(q); ok {
		if m, err := c.Resolve(repo); err == nil {
			return m, local, nil
		}
	}
	switch len(c.members) {
	case 0:
		return Member{}, "", errors.New("workspace: no repositories")
	case 1:
		return c.members[0], q, nil
	}
	if q == "" {
		return Member{}, "", errors.New("workspace: empty query")
	}
	cands, example, err := c.candidates(ctx, q)
	if err != nil {
		return Member{}, "", err
	}
	switch len(cands) {
	case 1:
		return cands[0], q, nil
	case 0:
		return Member{}, "", fmt.Errorf("workspace: no repo has %q", q)
	}
	ex := make([]string, 0, len(cands))
	for _, m := range cands {
		ex = append(ex, example(m.Info.Key))
	}
	return Member{}, "", fmt.Errorf("workspace: ambiguous %q: qualify it, e.g. %s", q, strings.Join(ex, " or "))
}

// candidates are the members that have the PR number or ref q, in
// workspace order, and how to qualify q for one of them. A member whose
// search fails is not a candidate (logged).
func (c *Client) candidates(ctx context.Context, q string) ([]Member, func(key string) string, error) {
	ms := c.members
	sp := proto.SearchParams{Source: proto.SearchLocal, Query: q, Limit: refCandidateLimit}
	match := func(h proto.SearchHit) bool { return h.Ref == q }
	example := func(key string) string { return key + ":" + q }
	if n, ok := prNumber(q); ok {
		ms = c.gitHubMembers(c.members)
		sp = proto.SearchParams{Source: proto.SearchOpen, Query: "#" + strconv.Itoa(n), Limit: 1}
		match = func(h proto.SearchHit) bool { return h.Number == n }
		example = func(key string) string { return key + "#" + strconv.Itoa(n) }
	}
	out := fanOut(ctx, ms, 0, func(ctx context.Context, m Member) (proto.SearchResult, error) {
		return m.Client.Search(ctx, sp)
	})
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	var cands []Member
	for i, m := range ms {
		if err := out.err[i]; err != nil {
			c.log.Warn("workspace: add: search", "repo", m.Info.Key, "err", err)
			continue
		}
		for _, h := range out.vals[i].Hits {
			if match(h) {
				cands = append(cands, m)
				break
			}
		}
	}
	return cands, example, nil
}

// prNumber parses "N" or "#N" (N > 0).
func prNumber(q string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(q, "#"))
	return n, err == nil && n > 0
}
