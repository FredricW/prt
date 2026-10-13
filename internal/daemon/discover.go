package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/fredricw/prt/internal/aggregate"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
)

// Picker source limits (spec: Picker, Sources).
const (
	involvesQuery = "is:pr is:open involves:@me"
	searchLimit   = 100
	// ownedLimit caps the open PRs listed to match owned-path globs.
	ownedLimit = 100
)

// Discover implements Client. It refreshes the picker sources:
//
//   - open PRs involving the viewer, and open PRs whose changed files match
//     an owned-path glob (GitHub);
//   - Added PRs that no source returned get fresh metadata;
//   - local branches with commits not on the default branch that do not
//     belong to a PR (matched by head SHA, or head ref for same-repo PRs).
//
// Ranked items that disappeared from every source are removed. GitHub
// errors never fail discovery: they are logged, reported as an EventLog
// note, and PR items from the cache are kept. A missing default branch
// skips only the branch source. Runs are serialised, so two overlapping
// callers (daemon loop, TUI ticks) never prune each other's results.
func (c *InProcess) Discover(ctx context.Context) error {
	c.discoverMu.Lock()
	defer c.discoverMu.Unlock()
	seen := map[string]bool{}
	ghOK := c.discoverPRs(ctx, seen)
	if ghOK {
		c.refreshAdded(ctx, seen)
	}
	prHeads, prRefs, err := c.prIndex(ctx, seen, ghOK)
	if err != nil {
		return err
	}
	branchesOK := true
	def, err := c.d.Git.DefaultBranch(ctx)
	if err != nil {
		branchesOK = false
		c.log.Warn("no default branch; local branches skipped", "err", err)
		c.send(proto.Event{Type: proto.EventLog, Note: "no default branch; local branches skipped"})
	} else if err := c.discoverBranches(ctx, def, prHeads, prRefs, seen); err != nil {
		return err
	}
	return c.prune(ctx, seen, ghOK, branchesOK)
}

// discoverPRs runs the GitHub sources and upserts PR items. It reports
// whether GitHub answered (false when offline or on error).
func (c *InProcess) discoverPRs(ctx context.Context, seen map[string]bool) bool {
	if c.d.GH == nil {
		c.send(proto.Event{Type: proto.EventLog, Note: c.gitHubNote()})
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
// deduplicated and sorted by number. The owned-path source lists open PRs
// with their changed files and matches them against user.owns locally
// (issue search has no path qualifier); its errors are logged only.
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
	if owns := c.d.Config.User.Owns; len(owns) > 0 {
		open, err := c.d.GH.ListOpenPRs(ctx, ownedLimit)
		if err != nil {
			c.log.Warn("owned-path source failed", "err", err)
		}
		for _, pr := range open {
			if _, ok := byNum[pr.Number]; !ok && touchesOwned(pr.Files, owns) {
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

// touchesOwned reports whether any path matches any owned glob.
func touchesOwned(paths, globs []string) bool {
	for _, p := range paths {
		for _, g := range globs {
			if ok, err := doublestar.Match(g, p); err == nil && ok {
				return true
			}
		}
	}
	return false
}

// refreshAdded gives Added PR items that no source returned fresh metadata
// (one PR call each; failures are logged and the cached item is kept).
func (c *InProcess) refreshAdded(ctx context.Context, seen map[string]bool) {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		c.log.Warn("refresh added: items", "err", err)
		return
	}
	viewer, _ := c.d.GH.Viewer(ctx)
	for _, it := range items {
		if it.Source != model.SourceAdded || it.Kind != model.KindPR || it.Number <= 0 || seen[it.ID] {
			continue
		}
		pr, err := c.d.GH.PR(ctx, it.Number)
		if err != nil {
			c.log.Warn("refresh added PR", "pr", it.Number, "err", err)
			continue
		}
		if err := c.upsertPR(ctx, pr, viewer); err != nil {
			c.log.Warn("store added PR", "pr", it.Number, "err", err)
		}
	}
}

// prSection assigns a ranked PR to a picker section (spec: Sections). Needs
// you is limited to the spec's four triggers: review requested, CI red on
// something the viewer approved, re-review after a push (the viewer's latest
// review is on an older commit than head), and a comment addressed to the
// viewer. Authored PRs go to Yours; any other involvement (owned paths,
// earlier comments, answered mentions) goes to Involved.
func prSection(pr gh.PR, head, viewer string) model.Section {
	authored := viewer != "" && strings.EqualFold(pr.Author, viewer)
	reReview := pr.ViewerReviewedSHA != "" && head != "" && pr.ViewerReviewedSHA != head
	switch {
	case pr.ReviewRequested, pr.ViewerApproved && pr.CI == model.CIRed:
		return model.SectionNeedsYou
	case authored:
		return model.SectionYours
	case reReview, pr.ViewerAddressed:
		return model.SectionNeedsYou
	default:
		return model.SectionInvolved
	}
}

// prHead is the PR's head SHA, resolved from the local head ref when GitHub
// did not report one (fixtures). Fork PRs are never resolved locally: their
// head ref names a branch in another repository.
func (c *InProcess) prHead(ctx context.Context, pr gh.PR) string {
	if pr.HeadSHA != "" || pr.HeadRef == "" || pr.CrossRepo {
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
	it.OpenThreads, it.CrossRepo = pr.OpenThreads, pr.CrossRepo
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
// Items in flight are left alone here; the run itself notices the moved
// head when it finishes (see InProcess.finish).
func markStale(it *model.Item, head string) {
	if it.HeadSHA == "" || head == "" || it.HeadSHA == head {
		return
	}
	if it.State == model.StateReady || it.State == model.StateError {
		it.State, it.StateNote = model.StateStale, staleNote
	}
}

// staleNote is the state note of an item whose head moved since analysis.
const staleNote = "new push since analysis"

func (c *InProcess) upsertPR(ctx context.Context, pr gh.PR, viewer string) error {
	head := c.prHead(ctx, pr)
	return c.upsertDiscovered(ctx, model.PRItemID(pr.Number), func(it *model.Item) {
		applyPR(it, pr, head)
		if it.Source != model.SourceAdded {
			it.Section = prSection(pr, head, viewer)
		}
	})
}

// upsertDiscovered loads (or creates as ranked) an item, applies fill,
// recomputes the urgency estimate and stores it, emitting EventItem.
func (c *InProcess) upsertDiscovered(ctx context.Context, id string, fill func(*model.Item)) error {
	c.itemMu.Lock()
	it, err := c.d.Store.Item(ctx, id)
	var before []byte
	prevHead := ""
	switch {
	case errors.Is(err, store.ErrNotFound):
		it = model.Item{ID: id, Source: model.SourceRanked}
	case err != nil:
		c.itemMu.Unlock()
		return fmt.Errorf("daemon: load %s: %w", id, err)
	default:
		before = itemKey(it)
		prevHead = it.HeadSHA
	}
	fill(&it)
	c.setUrgency(ctx, &it, before != nil && prevHead == it.HeadSHA)
	if before != nil && bytes.Equal(before, itemKey(it)) {
		// Unchanged: no write and no event (a discovery pass would
		// otherwise make every client reload the list once per item).
		c.itemMu.Unlock()
		return nil
	}
	err = c.d.Store.UpsertItem(ctx, it)
	c.itemMu.Unlock()
	if err != nil {
		return fmt.Errorf("daemon: store %s: %w", id, err)
	}
	c.send(proto.Event{Type: proto.EventItem, ItemID: id, Item: &it})
	return nil
}

// itemKey is the item's stored form, for change detection. Times are
// normalised to UTC so a store round trip compares equal.
func itemKey(it model.Item) []byte {
	it.CreatedAt, it.UpdatedAt = it.CreatedAt.UTC(), it.UpdatedAt.UTC()
	b, err := json.Marshal(it)
	if err != nil {
		return nil
	}
	return b
}

// setUrgency recomputes the urgency from fresh item metadata. Minutes come
// from the stored ItemAgg (label-based once any label is known) when that
// aggregate describes the item's current head: sameHead (discovery did not
// move the head in this pass), not stale, and analysed at this head or on
// its first analysis. Otherwise they are the stand-in from the line counts.
// TODO(M3): keep label terms from the stored ItemAgg too.
func (c *InProcess) setUrgency(ctx context.Context, it *model.Item, sameHead bool) {
	age, release, ciRed := aggregate.Facts(*it, c.d.Config.GitHub.ReleaseBranches, c.now())
	in := aggregate.Input{Item: *it, Weights: c.d.Config.Rank, AgeDays: age, Release: release, CIRedAfterApproval: ciRed}
	it.Minutes = aggregate.StandInMinutes(it.Additions + it.Deletions)
	if sameHead && it.State != model.StateStale && (it.AnalysedSHA == "" || it.AnalysedSHA == it.HeadSHA) {
		if agg, ok, err := c.d.Store.Agg(ctx, it.ID); err == nil && ok {
			it.Minutes = agg.Minutes
		}
	}
	_, it.Urgency, it.Why = aggregate.Urgency(in, it.Minutes)
}

// prIndex returns the head SHAs and head refs of the PR items that stay in
// the picker, for hiding local branches that belong to a PR. Fork PRs add
// only their SHA: their head ref names a branch in another repository, so
// a same-named local branch is unrelated.
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
		if it.Ref != "" && !it.CrossRepo {
			refs[it.Ref] = true
		}
	}
	return heads, refs, nil
}

// discoverBranches upserts local branches ahead of the default branch that
// do not belong to a PR. Ahead counts are measured against the
// remote-tracking default branch when it exists (gitx.CompareRef).
func (c *InProcess) discoverBranches(ctx context.Context, def string, prHeads, prRefs, seen map[string]bool) error {
	refs, err := c.d.Git.Refs(ctx, false)
	if err != nil {
		return fmt.Errorf("daemon: refs: %w", err)
	}
	base := gitx.CompareRef(ctx, c.d.Git, def)
	for _, r := range refs {
		if r.Remote || r.Name == def || prHeads[r.SHA] || prRefs[r.Name] {
			continue
		}
		id := model.BranchItemID(r.Name)
		ahead, err := c.d.Git.CountAhead(ctx, base, r.Name)
		if err != nil {
			// Unknown, not absent: keep whatever is stored.
			c.log.Warn("count ahead", "branch", r.Name, "base", base, "err", err)
			seen[id] = true
			continue
		}
		if ahead == 0 {
			continue
		}
		unpushed := ahead
		if r.Upstream != "" {
			if n, err := c.d.Git.CountAhead(ctx, r.Upstream, r.Name); err == nil {
				unpushed = n
			}
		}
		if err := c.upsertDiscovered(ctx, id, func(it *model.Item) {
			applyBranch(it, r, base, ahead, unpushed)
		}); err != nil {
			return err
		}
		seen[id] = true
	}
	return nil
}

func applyBranch(it *model.Item, r gitx.Ref, base string, ahead, unpushed int) {
	it.Kind, it.Ref, it.BaseRef, it.Title, it.Author = model.KindBranch, r.Name, base, r.Subject, r.Author
	if it.Source != model.SourceAdded {
		it.Section = model.SectionBranches
	}
	markStale(it, r.SHA)
	it.HeadSHA, it.Ahead, it.Unpushed, it.UpdatedAt = r.SHA, ahead, unpushed, r.CommitDate
}

// prune removes ranked items no source returned. PR items are kept when
// GitHub did not answer, branch items when the branch source did not run.
func (c *InProcess) prune(ctx context.Context, seen map[string]bool, ghOK, branchesOK bool) error {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		return fmt.Errorf("daemon: items: %w", err)
	}
	for _, it := range items {
		if it.Source == model.SourceAdded || seen[it.ID] ||
			(it.Kind == model.KindPR && !ghOK) || (it.Kind == model.KindBranch && !branchesOK) {
			continue
		}
		if err := c.Remove(ctx, it.ID); err != nil {
			return err
		}
	}
	return nil
}
