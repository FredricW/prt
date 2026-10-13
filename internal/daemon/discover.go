package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/aggregate"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
)

// Search queries for the picker sources (spec: Picker, Sources).
const (
	involvesQuery = "is:pr is:open involves:@me"
	ownedQuery    = "is:pr is:open path:"
	searchLimit   = 100
)

// Discover implements Client. It refreshes the picker sources:
//
//   - open PRs involving the viewer and PRs touching owned paths (GitHub);
//   - local branches with commits not on the default branch that do not
//     belong to a PR (matched by head SHA or head ref);
//   - Added items keep their section but get fresh metadata.
//
// Ranked items that disappeared from every source are removed. GitHub
// errors never fail discovery: they are logged, reported as an EventLog
// note, and PR items from the cache are kept.
func (c *InProcess) Discover(ctx context.Context) error {
	def, err := c.d.Git.DefaultBranch(ctx)
	if err != nil {
		return fmt.Errorf("daemon: default branch: %w", err)
	}
	seen := map[string]bool{}
	ghOK := c.discoverPRs(ctx, seen)
	prHeads, prRefs, err := c.prIndex(ctx, seen, ghOK)
	if err != nil {
		return err
	}
	if err := c.discoverBranches(ctx, def, prHeads, prRefs, seen); err != nil {
		return err
	}
	return c.prune(ctx, seen, ghOK)
}

// discoverPRs runs the GitHub searches and upserts PR items. It reports
// whether GitHub answered (false when offline or on error).
func (c *InProcess) discoverPRs(ctx context.Context, seen map[string]bool) bool {
	if c.d.GH == nil {
		return false
	}
	viewer, prs, err := c.searchPRs(ctx)
	if err != nil {
		c.log.Warn("GitHub unavailable; local branches only", "err", err)
		c.send(proto.Event{Type: proto.EventLog, Note: "GitHub unavailable: " + err.Error()})
		return false
	}
	for _, pr := range prs {
		if err := c.upsertPR(ctx, pr, viewer); err != nil {
			c.log.Warn("store PR item", "pr", pr.Number, "err", err)
			continue
		}
		seen[model.PRItemID(pr.Number)] = true
	}
	return true
}

// searchPRs returns the viewer and the open PRs from all GitHub sources,
// deduplicated and sorted by number. Owned-path searches are best effort:
// their errors are logged only.
func (c *InProcess) searchPRs(ctx context.Context) (string, []gh.PR, error) {
	viewer, err := c.d.GH.Viewer(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("viewer: %w", err)
	}
	hits, err := c.d.GH.SearchPRs(ctx, involvesQuery, searchLimit)
	if err != nil {
		return "", nil, fmt.Errorf("search: %w", err)
	}
	byNum := map[int]gh.PR{}
	for _, pr := range hits {
		byNum[pr.Number] = pr
	}
	for _, glob := range c.d.Config.User.Owns {
		res, err := c.d.GH.SearchPRs(ctx, ownedQuery+glob, searchLimit)
		if err != nil {
			c.log.Warn("owned-path search failed", "glob", glob, "err", err)
			continue
		}
		for _, pr := range res {
			if _, ok := byNum[pr.Number]; !ok {
				byNum[pr.Number] = pr
			}
		}
	}
	prs := make([]gh.PR, 0, len(byNum))
	for _, pr := range byNum {
		prs = append(prs, pr)
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number < prs[j].Number })
	return viewer, prs, nil
}

// prSection assigns a ranked PR to a picker section (spec: Sections).
func prSection(pr gh.PR, viewer string) model.Section {
	authored := viewer != "" && strings.EqualFold(pr.Author, viewer)
	switch {
	case pr.ReviewRequested,
		pr.ViewerApproved && pr.CI == model.CIRed,
		pr.ViewerCommented && !authored:
		return model.SectionNeedsYou
	case authored:
		return model.SectionYours
	default:
		// Owned-path matches and any other involvement. TODO(M1): only
		// owned-path matches once relevance labels exist.
		return model.SectionNeedsYou
	}
}

// prHead is the PR's head SHA, resolved from the local head ref when GitHub
// did not report one (fixtures).
func (c *InProcess) prHead(ctx context.Context, pr gh.PR) string {
	if pr.HeadSHA != "" || pr.HeadRef == "" {
		return pr.HeadSHA
	}
	sha, err := c.d.Git.ResolveRef(ctx, pr.HeadRef)
	if err != nil {
		return ""
	}
	return sha
}

// applyPR copies PR metadata onto an item (identity and ranking fields the
// pipeline owns are left alone).
func applyPR(it *model.Item, pr gh.PR, head string) {
	it.Kind, it.Number = model.KindPR, pr.Number
	it.Ref, it.BaseRef, it.Title, it.Body, it.Author = pr.HeadRef, pr.BaseRef, pr.Title, pr.Body, pr.Author
	it.Reviewers, it.CI, it.Labels, it.URL, it.Draft = pr.Reviewers, pr.CI, pr.Labels, pr.URL, pr.Draft
	it.ViewerApproved, it.ReviewRequested = pr.ViewerApproved, pr.ReviewRequested
	if pr.Additions+pr.Deletions > 0 { // fixtures may omit counts; keep the diff's
		it.Additions, it.Deletions = pr.Additions, pr.Deletions
	}
	it.CreatedAt, it.UpdatedAt = pr.CreatedAt, pr.UpdatedAt
	markStale(it, head)
	if head != "" {
		it.HeadSHA = head
	}
}

// markStale flags an analysed item whose head moved since its analysis.
func markStale(it *model.Item, head string) {
	if it.HeadSHA == "" || head == "" || it.HeadSHA == head {
		return
	}
	if it.State == model.StateReady || it.State == model.StateError {
		it.State, it.StateNote = model.StateStale, "new push since analysis"
	}
}

func (c *InProcess) upsertPR(ctx context.Context, pr gh.PR, viewer string) error {
	head := c.prHead(ctx, pr)
	return c.upsertDiscovered(ctx, model.PRItemID(pr.Number), func(it *model.Item) {
		applyPR(it, pr, head)
		if it.Source != model.SourceAdded {
			it.Section = prSection(pr, viewer)
		}
	})
}

// upsertDiscovered loads (or creates as ranked) an item, applies fill,
// recomputes the urgency estimate and stores it, emitting EventItem.
func (c *InProcess) upsertDiscovered(ctx context.Context, id string, fill func(*model.Item)) error {
	c.itemMu.Lock()
	it, err := c.d.Store.Item(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		it = model.Item{ID: id, Source: model.SourceRanked}
	case err != nil:
		c.itemMu.Unlock()
		return fmt.Errorf("daemon: load %s: %w", id, err)
	}
	fill(&it)
	c.setUrgency(&it)
	err = c.d.Store.UpsertItem(ctx, it)
	c.itemMu.Unlock()
	if err != nil {
		return fmt.Errorf("daemon: store %s: %w", id, err)
	}
	c.send(proto.Event{Type: proto.EventItem, ItemID: id, Item: &it})
	return nil
}

// setUrgency computes the M0 urgency from item metadata alone; it equals
// what the aggregate stage computes once the diff's line counts are known.
// TODO(M3): keep label terms from the stored ItemAgg.
func (c *InProcess) setUrgency(it *model.Item) {
	age, release, ciRed := aggregate.Facts(*it, c.d.Config.GitHub.ReleaseBranches, c.now())
	in := aggregate.Input{Item: *it, Weights: c.d.Config.Rank, AgeDays: age, Release: release, CIRedAfterApproval: ciRed}
	_, it.Urgency, it.Why = aggregate.Urgency(in, aggregate.StandInMinutes(it.Additions+it.Deletions))
}

// prIndex returns the head SHAs and head refs of the PR items that stay in
// the picker, for hiding local branches that belong to a PR.
func (c *InProcess) prIndex(ctx context.Context, seen map[string]bool, ghOK bool) (map[string]bool, map[string]bool, error) {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("daemon: items: %w", err)
	}
	heads, refs := map[string]bool{}, map[string]bool{}
	for _, it := range items {
		if it.Kind != model.KindPR || (ghOK && it.Source == model.SourceRanked && !seen[it.ID]) {
			continue
		}
		if it.HeadSHA != "" {
			heads[it.HeadSHA] = true
		}
		if it.Ref != "" {
			refs[it.Ref] = true
		}
	}
	return heads, refs, nil
}

// discoverBranches upserts local branches ahead of the default branch that
// do not belong to a PR.
func (c *InProcess) discoverBranches(ctx context.Context, def string, prHeads, prRefs, seen map[string]bool) error {
	refs, err := c.d.Git.Refs(ctx, false)
	if err != nil {
		return fmt.Errorf("daemon: refs: %w", err)
	}
	for _, r := range refs {
		if r.Remote || r.Name == def || prHeads[r.SHA] || prRefs[r.Name] {
			continue
		}
		ahead, err := c.d.Git.CountAhead(ctx, def, r.Name)
		if err != nil || ahead == 0 {
			continue
		}
		unpushed := ahead
		if r.Upstream != "" {
			if n, err := c.d.Git.CountAhead(ctx, r.Upstream, r.Name); err == nil {
				unpushed = n
			}
		}
		id := model.BranchItemID(r.Name)
		if err := c.upsertDiscovered(ctx, id, func(it *model.Item) {
			applyBranch(it, r, def, ahead, unpushed)
		}); err != nil {
			return err
		}
		seen[id] = true
	}
	return nil
}

func applyBranch(it *model.Item, r gitx.Ref, def string, ahead, unpushed int) {
	it.Kind, it.Ref, it.BaseRef, it.Title, it.Author = model.KindBranch, r.Name, def, r.Subject, r.Author
	if it.Source != model.SourceAdded {
		it.Section = model.SectionBranches
	}
	markStale(it, r.SHA)
	it.HeadSHA, it.Ahead, it.Unpushed, it.UpdatedAt = r.SHA, ahead, unpushed, r.CommitDate
}

// prune removes ranked items no source returned. PR items are kept when
// GitHub did not answer (cache).
func (c *InProcess) prune(ctx context.Context, seen map[string]bool, ghOK bool) error {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		return fmt.Errorf("daemon: items: %w", err)
	}
	for _, it := range items {
		if it.Source == model.SourceAdded || seen[it.ID] || (it.Kind == model.KindPR && !ghOK) {
			continue
		}
		if err := c.Remove(ctx, it.ID); err != nil {
			return err
		}
	}
	return nil
}
