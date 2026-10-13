package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fredricw/prt/internal/comments"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// SubmitReview implements Client: it posts the item's user drafts
// (comments.ForSubmit: p.CommentIDs when set, outdated ones skipped,
// ai-drafts never) as one GitHub review on the head the drafts' lines
// describe (draftHead: the stored units' head, which moves with them in
// stage 1, before the run finishes). A draft whose CommitSHA is another
// commit is skipped, never posted with mismatched lines. Drafts are
// pending while the request runs, posted (GHID, ReviewID) after it, and
// back to drafts if it fails: a failed submission never loses a draft.
// On success the item records the review commit (and the approval), and a
// re-run reloads the threads (the pipeline's MergeGitHub then replaces the
// local posted copies with GitHub's).
func (c *InProcess) SubmitReview(ctx context.Context, p proto.SubmitParams) (proto.SubmitResult, error) {
	ev, err := reviewEvent(p.Event)
	if err != nil {
		return proto.SubmitResult{}, err
	}
	// Lock before reading the item, so its UnitsSHA matches the drafts'
	// positions (the pipeline moves both under this lock).
	unlock := c.lockComments(p.ItemID)
	defer unlock()
	it, err := c.d.Store.Item(ctx, p.ItemID)
	if err != nil {
		return proto.SubmitResult{}, fmt.Errorf("daemon: submit review %s: %w", p.ItemID, err)
	}
	if err := c.requirePR(it, "submit review"); err != nil {
		return proto.SubmitResult{}, err
	}
	cs, err := c.d.Store.Comments(ctx, it.ID)
	if err != nil {
		return proto.SubmitResult{}, fmt.Errorf("daemon: submit review %s: %w", it.ID, err)
	}
	commit := draftHead(it)
	send, skipped := comments.ForSubmit(cs, p.CommentIDs)
	send, elsewhere := onCommit(send, commit)
	skipped = append(skipped, elsewhere...)
	if ev != gh.EventApprove && len(send) == 0 && strings.TrimSpace(p.Body) == "" {
		return proto.SubmitResult{Skipped: skipped}, fmt.Errorf("daemon: submit review %s: %s needs a summary or at least one draft", it.ID, ev)
	}
	res := proto.SubmitResult{Skipped: skipped}
	sub, posted, err := c.submit(ctx, it, commit, ev, p.Body, send)
	res.ReviewID, res.URL, res.Posted = sub.ID, sub.URL, posted
	c.patch(it.ID)
	return res, err
}

// onCommit splits drafts into those whose lines describe commit (CommitSHA
// commit, or unset on older stored drafts) and the rest.
func onCommit(cs []model.Comment, commit string) (on, off []model.Comment) {
	for _, d := range cs {
		if d.CommitSHA == "" || d.CommitSHA == commit {
			on = append(on, d)
		} else {
			off = append(off, d)
		}
	}
	return on, off
}

// reviewEvent maps a wire review event to GitHub's.
func reviewEvent(e proto.ReviewEvent) (gh.ReviewEvent, error) {
	switch e {
	case proto.EventApprove:
		return gh.EventApprove, nil
	case proto.EventRequestChanges:
		return gh.EventRequestChanges, nil
	case proto.EventComment:
		return gh.EventComment, nil
	default:
		return "", fmt.Errorf("daemon: submit review: unknown event %q", e)
	}
}

// submit sends send as one review of PR it on commit; the caller holds the
// item's comment lock. It returns the review and the stored posted copies.
func (c *InProcess) submit(ctx context.Context, it model.Item, commit string, ev gh.ReviewEvent, body string, send []model.Comment) (gh.SubmittedReview, []model.Comment, error) {
	if err := c.putStates(ctx, send, model.CommentPending); err != nil {
		return gh.SubmittedReview{}, nil, errors.Join(err, c.putStates(context.WithoutCancel(ctx), send, model.CommentDraft))
	}
	sub, err := c.d.GH.SubmitReview(ctx, it.Number, gh.ReviewSubmission{CommitSHA: commit, Event: ev, Body: body, Comments: send})
	if err != nil {
		err = fmt.Errorf("daemon: submit review on #%d: %w", it.Number, err)
		return gh.SubmittedReview{}, nil, errors.Join(err, c.putStates(context.WithoutCancel(ctx), send, model.CommentDraft))
	}
	// The review exists on GitHub now: record it even if ctx ends.
	ctx = context.WithoutCancel(ctx)
	posted := postedCopies(send, sub, commit, c.now())
	var errs []error
	for _, pc := range posted {
		if err := c.d.Store.PutComment(ctx, pc); err != nil {
			errs = append(errs, fmt.Errorf("daemon: store posted comment %s: %w", pc.ID, err))
		}
	}
	if _, _, err := c.updateItem(ctx, it.ID, func(it *model.Item) {
		it.ViewerReviewedSHA, it.ViewerReviewedAt = commit, c.now()
		if ev == gh.EventApprove {
			it.ViewerApproved = true
		}
	}); err != nil {
		errs = append(errs, err)
	}
	// Automatic (gated): submitting is not a request to analyse; the
	// re-run refreshes the section and aggregate where the strategy allows.
	c.q.push(job{itemID: it.ID, urgency: it.Urgency, rerun: true})
	return sub, posted, errors.Join(errs...)
}

// putStates stores copies of cs with State st (all of them, joining errors).
func (c *InProcess) putStates(ctx context.Context, cs []model.Comment, st model.CommentState) error {
	var errs []error
	for _, cm := range cs {
		cm.State = st
		if err := c.d.Store.PutComment(ctx, cm); err != nil {
			errs = append(errs, fmt.Errorf("daemon: mark comment %s %s: %w", cm.ID, st, err))
		}
	}
	return errors.Join(errs...)
}

// postedCopies returns the sent drafts as posted comments: State posted,
// GHID and ReviewID from GitHub's answer (matched by id, else by position),
// CommitSHA the review's commit; ids and everything else are kept.
func postedCopies(send []model.Comment, sub gh.SubmittedReview, commit string, now time.Time) []model.Comment {
	byID := make(map[string]model.Comment, len(sub.Comments))
	for _, rc := range sub.Comments {
		if rc.ID != "" {
			byID[rc.ID] = rc
		}
	}
	out := make([]model.Comment, 0, len(send))
	for i, d := range send {
		rc, ok := byID[d.ID]
		if !ok && i < len(sub.Comments) {
			rc = sub.Comments[i]
		}
		d.State, d.GHID, d.ReviewID = model.CommentPosted, rc.GHID, cmp.Or(rc.ReviewID, sub.ID)
		d.CommitSHA, d.UpdatedAt = commit, now
		out = append(out, d)
	}
	return out
}

// CreatePR implements Client: it opens a PR from the branch item p.ItemID
// (head = its ref; base = p.Base, else the item's base, else the default
// branch, without an "origin/" prefix), stores the PR item, moves the
// branch's drafts to it and posts them as one COMMENT review, removes the
// branch item (the PR now stands for it) and queues the PR's analysis. If
// the drafts cannot be posted the PR item is still returned, with the
// drafts kept as drafts on it, and an error saying so.
func (c *InProcess) CreatePR(ctx context.Context, p proto.CreatePRParams) (proto.CreatePRResult, error) {
	br, err := c.d.Store.Item(ctx, p.ItemID)
	if err != nil {
		return proto.CreatePRResult{}, fmt.Errorf("daemon: create PR %s: %w", p.ItemID, err)
	}
	newPR, err := c.newPRFor(ctx, br, p)
	if err != nil {
		return proto.CreatePRResult{}, err
	}
	unlock := c.lockComments(br.ID)
	defer unlock()
	pr, err := c.d.GH.CreatePR(ctx, newPR)
	if err != nil {
		return proto.CreatePRResult{}, fmt.Errorf("daemon: create PR for %s: %w", br.Ref, err)
	}
	// The PR exists on GitHub now: finish the local bookkeeping regardless.
	ctx = context.WithoutCancel(ctx)
	prIt, err := c.storeCreatedPR(ctx, pr, br)
	res := proto.CreatePRResult{Item: prIt, URL: pr.URL}
	if err != nil {
		return res, err
	}
	var errs []error
	res.Attached, err = c.attachDrafts(ctx, br, prIt)
	errs = append(errs, err)
	errs = append(errs, c.Remove(ctx, br.ID), c.analyseCreatedPR(ctx, br, prIt.ID))
	if stored, err := c.d.Store.Item(ctx, prIt.ID); err == nil {
		res.Item = stored
	}
	c.patch(prIt.ID)
	return res, errors.Join(errs...)
}

// analyseCreatedPR queues the analysis of the PR created from branch br.
// The PR inherits the branch's request: explicit when the user had asked
// for, or analysed, the branch's head (its caches make the run cheap);
// otherwise automatic, gated by the strategy (M5.1).
func (c *InProcess) analyseCreatedPR(ctx context.Context, br model.Item, prID string) error {
	if br.Requested() || (br.AnalysedSHA != "" && br.AnalysedSHA == br.HeadSHA) {
		return c.Analyse(ctx, false, prID)
	}
	return c.queueAuto(ctx, prID)
}

// newPRFor validates a create-PR request for branch item br and returns
// what GitHub is sent.
func (c *InProcess) newPRFor(ctx context.Context, br model.Item, p proto.CreatePRParams) (gh.NewPR, error) {
	if br.Kind != model.KindBranch || br.Ref == "" {
		return gh.NewPR{}, fmt.Errorf("daemon: create PR: %s is not a local branch", br.ID)
	}
	if err := c.requireGitHub("create PR"); err != nil {
		return gh.NewPR{}, err
	}
	if n := c.unpushed(ctx, br); n > 0 {
		return gh.NewPR{}, fmt.Errorf("daemon: create PR: push %s first: %s not on its upstream (GitHub opens a PR on the pushed branch only)", br.Ref, plural(n, "commit"))
	}
	if n, ok, err := c.prForRef(ctx, br.Ref); err != nil {
		return gh.NewPR{}, err
	} else if ok {
		return gh.NewPR{}, fmt.Errorf("daemon: create PR: %s already has PR #%d", br.Ref, n)
	}
	title := cmp.Or(strings.TrimSpace(p.Title), strings.TrimSpace(br.Title))
	if title == "" {
		return gh.NewPR{}, errors.New("daemon: create PR: needs a title")
	}
	base, err := c.prBase(ctx, p.Base, br.BaseRef)
	if err != nil {
		return gh.NewPR{}, err
	}
	if base == br.Ref {
		return gh.NewPR{}, fmt.Errorf("daemon: create PR: head and base are both %s", base)
	}
	return gh.NewPR{Title: title, Body: p.Body, Head: br.Ref, Base: base, Draft: p.Draft}, nil
}

// unpushed counts branch item br's commits that are not on its upstream:
// re-counted from git when the branch has an upstream (the user may have
// pushed since discovery), else discovery's count (Item.Unpushed, which is
// every commit ahead of the base for a branch that was never pushed).
func (c *InProcess) unpushed(ctx context.Context, br model.Item) int {
	refs, err := c.d.Git.Refs(ctx, false)
	if err != nil {
		return br.Unpushed
	}
	for _, r := range refs {
		if r.Remote || r.Name != br.Ref || r.Upstream == "" {
			continue
		}
		if n, err := c.d.Git.CountAhead(ctx, r.Upstream, r.Name); err == nil {
			return n
		}
	}
	return br.Unpushed
}

// shortSHA is the first 7 characters of a SHA.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// plural is "1 commit" / "n commits".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// prForRef returns the number of a stored same-repo PR item whose head ref
// is ref.
func (c *InProcess) prForRef(ctx context.Context, ref string) (int, bool, error) {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("daemon: create PR: items: %w", err)
	}
	for _, it := range items {
		if it.Kind == model.KindPR && it.Ref == ref && !it.CrossRepo {
			return it.Number, true, nil
		}
	}
	return 0, false, nil
}

// prBase picks a PR base branch name: want, else the branch item's base,
// else the default branch. Branch items may store the remote-tracking
// "origin/<name>" as base (gitx.CompareRef); GitHub wants the bare name.
func (c *InProcess) prBase(ctx context.Context, want, itemBase string) (string, error) {
	base := cmp.Or(strings.TrimSpace(want), itemBase)
	if base == "" {
		def, err := c.d.Git.DefaultBranch(ctx)
		if err != nil {
			return "", fmt.Errorf("daemon: create PR: no base branch: %w", err)
		}
		base = def
	}
	base = strings.TrimPrefix(base, "origin/")
	if base == "" {
		return "", errors.New("daemon: create PR: no base branch")
	}
	return base, nil
}

// storeCreatedPR upserts the ranked PR item for a PR just created from
// branch item br (the viewer authored it; its head is the branch head when
// GitHub did not report one) and returns it.
func (c *InProcess) storeCreatedPR(ctx context.Context, pr gh.PR, br model.Item) (model.Item, error) {
	viewer := c.viewerLogin(ctx)
	pr.Author = cmp.Or(pr.Author, viewer)
	head := cmp.Or(c.prHead(ctx, pr), br.HeadSHA)
	id := model.PRItemID(pr.Number)
	if err := c.upsertDiscovered(ctx, id, func(it *model.Item) {
		applyPR(it, pr, head)
		if it.Source != model.SourceAdded {
			it.Section = prSection(pr, head, viewer)
		}
	}); err != nil {
		return model.Item{}, fmt.Errorf("daemon: create PR: store #%d: %w", pr.Number, err)
	}
	it, err := c.d.Store.Item(ctx, id)
	if err != nil {
		return model.Item{}, fmt.Errorf("daemon: create PR: load #%d: %w", pr.Number, err)
	}
	return it, nil
}

// attachDrafts moves the branch's user drafts to the new PR item and posts
// them as one COMMENT review on the commit their lines describe (the
// branch's draftHead). When that is not the PR's head on GitHub (the
// branch moved since its analysis), nothing is posted: they stay drafts on
// the PR item, and its analysis re-anchors them onto the PR's head. It
// returns how many were posted; on failure they stay drafts on the PR item.
func (c *InProcess) attachDrafts(ctx context.Context, br, prIt model.Item) (int, error) {
	unlock := c.lockComments(prIt.ID)
	defer unlock()
	cs, err := c.d.Store.Comments(ctx, br.ID)
	if err != nil {
		return 0, fmt.Errorf("daemon: PR #%d created, but the drafts of %s could not be read: %w", prIt.Number, br.ID, err)
	}
	commit := draftHead(br)
	var moved []model.Comment
	for _, d := range cs {
		if !isUserDraft(d) {
			continue
		}
		d.ItemID, d.CommitSHA = prIt.ID, cmp.Or(d.CommitSHA, commit)
		if err := c.d.Store.PutComment(ctx, d); err != nil {
			return 0, fmt.Errorf("daemon: PR #%d created, but draft %s could not be moved to it: %w", prIt.Number, d.ID, err)
		}
		moved = append(moved, d)
	}
	send, _ := comments.ForSubmit(moved, nil)
	send, _ = onCommit(send, commit)
	if len(send) == 0 {
		return 0, nil
	}
	if prIt.HeadSHA != "" && prIt.HeadSHA != commit {
		return 0, fmt.Errorf("daemon: PR #%d created, but its %d draft(s) describe %s, not the PR's head %s: they stay drafts on it until it is analysed",
			prIt.Number, len(send), shortSHA(commit), shortSHA(prIt.HeadSHA))
	}
	_, posted, err := c.submit(ctx, prIt, commit, gh.EventComment, "", send)
	switch {
	case err != nil && len(posted) == 0:
		return 0, fmt.Errorf("daemon: PR #%d created, but its %d draft(s) were not attached and stay drafts on it: %w", prIt.Number, len(send), err)
	case err != nil:
		return len(posted), fmt.Errorf("daemon: PR #%d created and its drafts attached, but: %w", prIt.Number, err)
	}
	return len(posted), nil
}
