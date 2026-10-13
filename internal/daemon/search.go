package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
)

// defaultSearchLimit is the hit cap when SearchParams.Limit is not positive.
const defaultSearchLimit = 20

// Search notes (SearchResult.Note): a source that could not run is not an
// error, so the prompt can show why it has nothing.
const (
	noteGitHubUnavailable = "GitHub unavailable"
	noteGitHubFailed      = "GitHub search failed: "
)

// Search implements Client: one source of the picker's search-and-add (see
// DECISIONS "Branch states, search and command line"). A leading "!" in the
// query is stripped for every source; the prompt uses it to ask the closed
// source. GitHub problems come back as a Note, never as an error; only a
// cancelled context or an unknown source is an error.
func (c *InProcess) Search(ctx context.Context, p proto.SearchParams) (proto.SearchResult, error) {
	res := proto.SearchResult{Source: p.Source, Query: p.Query}
	q := searchTerms(p.Query)
	limit := p.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	var (
		hits []proto.SearchHit
		err  error
	)
	switch p.Source {
	case proto.SearchLocal:
		hits, err = c.searchLocal(ctx, q, limit)
	case proto.SearchOpen, proto.SearchClosed:
		hits, res.Note, err = c.searchGitHub(ctx, p.Source, q, limit)
	default:
		return res, fmt.Errorf("daemon: search: unknown source %q", p.Source)
	}
	if err != nil {
		return res, err
	}
	if err := c.markListed(ctx, hits); err != nil {
		return res, err
	}
	res.Hits = hits
	return res, nil
}

// searchTerms is the query without surrounding space and its leading "!".
func searchTerms(query string) string {
	q := strings.TrimSpace(query)
	return strings.TrimSpace(strings.TrimPrefix(q, "!"))
}

// searchLocal matches local and remote-tracking refs by name or tip subject
// (case-insensitive substring). Local branches come first, then
// remote-tracking ones; within each, the most recently committed first. The
// default branch (and its remote-tracking copy), HEAD symrefs and
// remote-tracking refs that duplicate a local branch are left out. An empty
// query returns the most recently committed refs.
func (c *InProcess) searchLocal(ctx context.Context, q string, limit int) ([]proto.SearchHit, error) {
	refs, err := c.d.Git.Refs(ctx, true)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("daemon: search local refs: %w", err)
	}
	def, _ := c.d.Git.DefaultBranch(ctx) // unknown default: nothing excluded
	refs = searchableRefs(refs, def)
	lq := strings.ToLower(q)
	var hits []proto.SearchHit
	for _, r := range refs {
		if lq != "" && !strings.Contains(strings.ToLower(r.Name), lq) && !strings.Contains(strings.ToLower(r.Subject), lq) {
			continue
		}
		hits = append(hits, refHit(r))
		if len(hits) == limit {
			break
		}
	}
	return hits, nil
}

// searchableRefs drops the refs search never offers and sorts the rest:
// local before remote-tracking, newest commit first, then by name.
func searchableRefs(refs []gitx.Ref, def string) []gitx.Ref {
	local := map[string]bool{}
	for _, r := range refs {
		if !r.Remote {
			local[r.Name] = true
			if r.Upstream != "" {
				local[r.Upstream] = true // its remote-tracking copy is a duplicate
			}
		}
	}
	out := make([]gitx.Ref, 0, len(refs))
	for _, r := range refs {
		if isHEAD(r) || (def != "" && (r.Name == def || r.Name == "origin/"+def)) {
			continue
		}
		if r.Remote && (local[r.Name] || local[withoutRemote(r.Name)]) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Remote != b.Remote {
			return !a.Remote
		}
		if !a.CommitDate.Equal(b.CommitDate) {
			return a.CommitDate.After(b.CommitDate)
		}
		return a.Name < b.Name
	})
	return out
}

// isHEAD reports a HEAD symref ("HEAD", "origin/HEAD").
func isHEAD(r gitx.Ref) bool {
	return r.Name == "HEAD" || strings.HasSuffix(r.Name, "/HEAD") || strings.HasSuffix(r.FullName, "/HEAD")
}

// withoutRemote strips the remote name from a remote-tracking ref
// ("origin/feat/x" → "feat/x").
func withoutRemote(name string) string {
	if _, rest, ok := strings.Cut(name, "/"); ok {
		return rest
	}
	return name
}

// refHit is the search hit of a ref.
func refHit(r gitx.Ref) proto.SearchHit {
	return proto.SearchHit{
		Source: proto.SearchLocal, Kind: model.KindBranch, Ref: r.Name, Title: r.Subject, Author: r.Author,
		UpdatedAt: r.CommitDate, Query: r.Name, ItemID: model.BranchItemID(r.Name),
	}
}

// searchGitHub runs the open or closed source. It returns a note instead of
// an error when GitHub is unavailable or fails; a cancelled context is
// returned as an error. An empty query asks nothing. On the open source a
// bare number ("482", "#482") looks that PR up directly, whatever its state.
func (c *InProcess) searchGitHub(ctx context.Context, src proto.SearchSource, q string, limit int) ([]proto.SearchHit, string, error) {
	if c.d.GH == nil {
		return nil, noteGitHubUnavailable, nil
	}
	if q == "" {
		return nil, "", nil
	}
	if n, ok := prNumber(q); ok && src == proto.SearchOpen {
		return c.searchNumber(ctx, n)
	}
	state := "is:open"
	if src == proto.SearchClosed {
		state = "is:closed"
	}
	prs, err := c.d.GH.SearchPRs(ctx, "is:pr "+state+" "+q, limit)
	if err != nil {
		return ghFailure(ctx, err)
	}
	hits := make([]proto.SearchHit, 0, len(prs))
	for _, pr := range prs {
		hits = append(hits, prHit(src, pr))
	}
	return hits, "", nil
}

// searchNumber returns exactly PR #n; a PR that does not exist is no hit
// with a note, not a failure.
func (c *InProcess) searchNumber(ctx context.Context, n int) ([]proto.SearchHit, string, error) {
	pr, err := c.d.GH.PR(ctx, n)
	if errors.Is(err, gh.ErrPRNotFound) {
		return nil, fmt.Sprintf("no PR #%d", n), nil
	}
	if err != nil {
		return ghFailure(ctx, err)
	}
	return []proto.SearchHit{prHit(proto.SearchOpen, pr)}, "", nil
}

// ghFailure turns a GitHub error into a note, unless ctx was cancelled.
func ghFailure(ctx context.Context, err error) ([]proto.SearchHit, string, error) {
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	return nil, noteGitHubFailed + err.Error(), nil
}

// prNumber parses a bare PR number ("482" or "#482").
func prNumber(q string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(q, "#"))
	return n, err == nil && n > 0
}

// prHit is the search hit of a PR.
func prHit(src proto.SearchSource, pr gh.PR) proto.SearchHit {
	state := strings.ToLower(pr.State)
	if state == "" {
		state = "open"
	}
	return proto.SearchHit{
		Source: src, Kind: model.KindPR, Number: pr.Number, Ref: pr.HeadRef, Title: pr.Title, Author: pr.Author,
		State: state, UpdatedAt: pr.UpdatedAt, Query: "#" + strconv.Itoa(pr.Number), ItemID: model.PRItemID(pr.Number),
	}
}

// markListed sets Listed on hits whose item is in the picker (stored and not
// a preview).
func (c *InProcess) markListed(ctx context.Context, hits []proto.SearchHit) error {
	if len(hits) == 0 {
		return nil
	}
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		return fmt.Errorf("daemon: search: items: %w", err)
	}
	listed := make(map[string]bool, len(items))
	for _, it := range items {
		if it.Source != model.SourcePreview {
			listed[it.ID] = true
		}
	}
	for i := range hits {
		hits[i].Listed = listed[hits[i].ItemID]
	}
	return nil
}

// Preview implements Client: it resolves query like Add, stores a new item
// as model.SourcePreview with no section (Items never lists it), queues its
// analysis and returns it. An item already stored is returned unchanged.
// Only one preview exists at a time: every other preview is removed.
func (c *InProcess) Preview(ctx context.Context, query string) (model.Item, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return model.Item{}, errors.New("daemon: preview: empty query")
	}
	it, err := c.resolveAdd(ctx, q, model.SourcePreview)
	if err != nil {
		return model.Item{}, err
	}
	c.removePreviews(ctx, it.ID)
	c.analyseNow(it)
	return it, nil
}

// RemovePreview implements Client: it deletes id silently when it is still
// a preview and does nothing otherwise (see removePreview).
func (c *InProcess) RemovePreview(ctx context.Context, id string) error {
	return c.removePreview(ctx, id)
}

// removePreviews deletes every stored preview except keep, silently (no
// event: a preview was never listed). Failures are logged.
func (c *InProcess) removePreviews(ctx context.Context, keep string) {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		c.log.Warn("remove previews: items", "err", err)
		return
	}
	for _, it := range items {
		if it.Source != model.SourcePreview || it.ID == keep {
			continue
		}
		if err := c.removePreview(ctx, it.ID); err != nil {
			c.log.Warn("remove preview", "item", it.ID, "err", err)
		}
	}
}

// removePreview deletes id if it is still a preview (under the item lock,
// so a concurrent promotion wins). It reports nothing when id is gone.
func (c *InProcess) removePreview(ctx context.Context, id string) error {
	c.itemMu.Lock()
	defer c.itemMu.Unlock()
	it, err := c.d.Store.Item(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("daemon: load %s: %w", id, err)
	}
	if it.Source != model.SourcePreview {
		return nil
	}
	if err := c.d.Store.RemoveItem(ctx, id); err != nil {
		return fmt.Errorf("daemon: remove preview %s: %w", id, err)
	}
	c.cancelRun(id)
	c.forgetItem(ctx, id)
	return nil
}
