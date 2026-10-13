package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fredricw/prt/internal/comments"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// fallbackAuthor is a draft's author when the GitHub login is unknown
// (offline, no token): drafts are local until submitted, and GitHub sets
// the real author when they are posted.
const fallbackAuthor = "you"

// SaveComment implements Client: it creates, edits or deletes one local
// user draft of p.ItemID. A new draft gets an id, the viewer as author, the
// analysed head as CommitSHA and a unit anchor; a line no unit covers is
// refused. Only user drafts (Source user, State draft) can be edited or
// deleted. Comment writes of one item are serialised; a patch event
// follows every write.
func (c *InProcess) SaveComment(ctx context.Context, p proto.CommentParams) (model.Comment, error) {
	in := p.Comment
	if in.ItemID == "" {
		in.ItemID = p.ItemID
	}
	if in.ItemID != p.ItemID {
		return model.Comment{}, fmt.Errorf("daemon: comment: comment for %q sent for item %q", in.ItemID, p.ItemID)
	}
	// Lock before reading the item: its UnitsSHA must match the units the
	// draft is placed on (the pipeline moves both under this lock).
	unlock := c.lockComments(p.ItemID)
	defer unlock()
	it, err := c.d.Store.Item(ctx, p.ItemID)
	if err != nil {
		return model.Comment{}, fmt.Errorf("daemon: comment %s: %w", p.ItemID, err)
	}
	var out model.Comment
	switch {
	case p.Delete:
		err = c.deleteDraft(ctx, it.ID, in.ID)
	case in.ID == "":
		out, err = c.newDraft(ctx, it, in)
	default:
		out, err = c.editDraft(ctx, it, in)
	}
	if err != nil {
		return model.Comment{}, err
	}
	c.patch(it.ID)
	return out, nil
}

// deleteDraft removes the user draft id of itemID.
func (c *InProcess) deleteDraft(ctx context.Context, itemID, id string) error {
	stored, err := c.comment(ctx, itemID, id)
	if err != nil {
		return err
	}
	if !isUserDraft(stored) {
		return fmt.Errorf("daemon: comment %s: only your drafts can be deleted (this one is %s, %s)", id, stored.Source, stored.State)
	}
	if err := c.d.Store.DeleteComment(ctx, id); err != nil {
		return fmt.Errorf("daemon: delete comment %s: %w", id, err)
	}
	return nil
}

// newDraft stores a new user draft built from in's position and body.
func (c *InProcess) newDraft(ctx context.Context, it model.Item, in model.Comment) (model.Comment, error) {
	now := c.now()
	d := model.Comment{
		ID: c.newDraftID(it.ID), ItemID: it.ID,
		Path: in.Path, Line: in.Line, StartLine: in.StartLine, Side: in.Side, StartSide: in.StartSide,
		Body: in.Body, Author: c.author(ctx), Source: model.CommentUser, State: model.CommentDraft,
		CommitSHA: draftHead(it), CreatedAt: now, UpdatedAt: now,
	}
	return c.placeDraft(ctx, d)
}

// editDraft updates the body and position of a stored user draft. A
// comment sent without a line keeps the stored position (a body-only
// edit); the path never changes.
func (c *InProcess) editDraft(ctx context.Context, it model.Item, in model.Comment) (model.Comment, error) {
	stored, err := c.comment(ctx, it.ID, in.ID)
	if err != nil {
		return model.Comment{}, err
	}
	if !isUserDraft(stored) {
		return model.Comment{}, fmt.Errorf("daemon: comment %s: only your drafts can be edited (this one is %s, %s)", in.ID, stored.Source, stored.State)
	}
	d := stored
	d.Body = in.Body
	if in.Line > 0 {
		d.Line, d.StartLine, d.Side, d.StartSide = in.Line, in.StartLine, in.Side, in.StartSide
	}
	d.CommitSHA, d.UpdatedAt = draftHead(it), c.now()
	return c.placeDraft(ctx, d)
}

// placeDraft validates a draft, anchors it to the item's units and stores
// it. The line (and a range's start line) must lie in a unit's span on its
// side: GitHub rejects line comments outside the diff.
func (c *InProcess) placeDraft(ctx context.Context, d model.Comment) (model.Comment, error) {
	d, err := normaliseDraft(d)
	if err != nil {
		return model.Comment{}, err
	}
	units, err := c.d.Store.Units(ctx, d.ItemID)
	if err != nil {
		return model.Comment{}, fmt.Errorf("daemon: comment %s: units: %w", d.ItemID, err)
	}
	if len(units) == 0 {
		return model.Comment{}, fmt.Errorf("daemon: comment %s:%d: line not in the diff (the item is not analysed yet)", d.Path, d.Line)
	}
	d = comments.Anchor(d, units)
	if d.UnitID == "" {
		return model.Comment{}, fmt.Errorf("daemon: comment %s:%d (%s): line not in the diff", d.Path, d.Line, d.Side)
	}
	if d.StartLine > 0 {
		start := model.Comment{Path: d.Path, Line: d.StartLine, Side: comments.StartSide(d), UnitID: d.UnitID}
		su := comments.Anchor(start, units).UnitID
		switch {
		case su == "":
			return model.Comment{}, fmt.Errorf("daemon: comment %s:%d (%s): start line not in the diff", d.Path, d.StartLine, start.Side)
		case su != d.UnitID && comments.CrossSide(d):
			// Re-anchoring places a cross-side start by its offset in the
			// end's unit, so both ends must lie in that unit.
			return model.Comment{}, fmt.Errorf("daemon: comment %s: a range across sides must stay within one change", d.Path)
		}
	}
	if err := c.d.Store.PutComment(ctx, d); err != nil {
		return model.Comment{}, fmt.Errorf("daemon: store comment %s: %w", d.ID, err)
	}
	return d, nil
}

// normaliseDraft checks a draft's body, path, line and side, defaults the
// side to new, and turns a same-side range starting on its own line into a
// single-line comment. Start and end are compared only on the same side: a
// cross-side range (comments.CrossSide) is kept as sent.
func normaliseDraft(d model.Comment) (model.Comment, error) {
	switch {
	case strings.TrimSpace(d.Body) == "":
		return d, errors.New("daemon: comment: empty body")
	case d.Path == "" || d.Line <= 0:
		return d, fmt.Errorf("daemon: comment: needs a path and a line (got %q:%d)", d.Path, d.Line)
	}
	if d.Side == "" {
		d.Side = model.SideNew
	}
	if d.StartSide == d.Side {
		d.StartSide = ""
	}
	for _, s := range []model.Side{d.Side, d.StartSide} {
		if s != "" && s != model.SideOld && s != model.SideNew {
			return d, fmt.Errorf("daemon: comment: unknown side %q", s)
		}
	}
	switch {
	case d.StartLine < 0:
		return d, fmt.Errorf("daemon: comment: bad start line %d", d.StartLine)
	case comments.CrossSide(d):
		// A range from one side to the other (a deleted line down to an
		// added one): the two numbers are on different sides and are not
		// compared; GitHub takes start_side LEFT with side RIGHT.
		return d, nil
	case d.StartLine == d.Line:
		d.StartLine = 0
	case d.StartLine > d.Line:
		return d, fmt.Errorf("daemon: comment: start line %d must come before line %d", d.StartLine, d.Line)
	}
	if d.StartLine == 0 {
		d.StartSide = ""
	}
	return d, nil
}

// CommentAction implements Client: accept or dismiss an ai-draft, or reply
// to or resolve a GitHub review thread. Every action emits a patch event.
func (c *InProcess) CommentAction(ctx context.Context, p proto.CommentActionParams) (model.Comment, error) {
	unlock := c.lockComments(p.ItemID)
	defer unlock()
	it, err := c.d.Store.Item(ctx, p.ItemID)
	if err != nil {
		return model.Comment{}, fmt.Errorf("daemon: comment %s %s: %w", p.Action, p.ItemID, err)
	}
	var out model.Comment
	switch p.Action {
	case proto.CommentAccept:
		out, err = c.acceptDraft(ctx, it, p.CommentID)
	case proto.CommentDismiss:
		err = c.dismissDraft(ctx, it, p.CommentID, p.Override)
	case proto.CommentReply:
		out, err = c.reply(ctx, it, p.ThreadID, p.Body)
	case proto.CommentResolve:
		err = c.resolve(ctx, it, p.ThreadID)
	default:
		err = fmt.Errorf("daemon: comment action %q: unknown action", p.Action)
	}
	if err != nil {
		return model.Comment{}, err
	}
	c.patch(it.ID)
	return out, nil
}

// acceptDraft turns an ai-draft into a user draft in the viewer's name and
// marks its finding accepted, so the pipeline does not re-create it.
func (c *InProcess) acceptDraft(ctx context.Context, it model.Item, id string) (model.Comment, error) {
	ai, err := c.aiDraft(ctx, it.ID, id)
	if err != nil {
		return model.Comment{}, err
	}
	now := c.now()
	d := comments.Accept(ai, c.newDraftID(it.ID), c.author(ctx), now)
	if d.CommitSHA == "" {
		d.CommitSHA = draftHead(it)
	}
	// A finding on a line outside the diff cannot be posted there: the draft
	// is kept (the user can edit it) but outdated, so a submit skips it
	// instead of GitHub rejecting the whole review.
	if units, err := c.d.Store.Units(ctx, it.ID); err == nil {
		if a := comments.Anchor(d, units); a.UnitID != "" {
			d = a
		} else {
			d.UnitID, d.Outdated = "", true
		}
	}
	// Draft first, then the mark, then the ai row: a failure part-way never
	// loses the finding.
	if err := c.d.Store.PutComment(ctx, d); err != nil {
		return model.Comment{}, fmt.Errorf("daemon: accept %s: %w", id, err)
	}
	mark := model.FindingMark{ItemID: it.ID, FindingKey: ai.FindingKey, State: model.FindingAccepted, CommentID: d.ID, At: now}
	if err := c.d.Store.PutFindingMark(ctx, mark); err != nil {
		return model.Comment{}, fmt.Errorf("daemon: accept %s: %w", id, err)
	}
	if err := c.d.Store.DeleteComment(ctx, ai.ID); err != nil {
		return model.Comment{}, fmt.Errorf("daemon: accept %s: %w", id, err)
	}
	return d, nil
}

// dismissDraft removes an ai-draft and marks its finding dismissed; with an
// override (`!`) it also stores that override and re-runs the item.
func (c *InProcess) dismissDraft(ctx context.Context, it model.Item, id string, ov *model.Override) error {
	ai, err := c.aiDraft(ctx, it.ID, id)
	if err != nil {
		return err
	}
	var o *model.Override
	if ov != nil {
		n, err := c.normaliseOverride(*ov)
		if err != nil {
			return err
		}
		o = &n
	}
	mark := model.FindingMark{ItemID: it.ID, FindingKey: ai.FindingKey, State: model.FindingDismissed, Override: o, At: c.now()}
	if err := c.d.Store.PutFindingMark(ctx, mark); err != nil {
		return fmt.Errorf("daemon: dismiss %s: %w", id, err)
	}
	if err := c.d.Store.DeleteComment(ctx, ai.ID); err != nil {
		return fmt.Errorf("daemon: dismiss %s: %w", id, err)
	}
	if o == nil {
		return nil
	}
	return c.applyOverride(ctx, it, *o)
}

// reply posts body to a GitHub review thread and stores the reply.
func (c *InProcess) reply(ctx context.Context, it model.Item, threadID, body string) (model.Comment, error) {
	if err := c.requirePR(it, "reply"); err != nil {
		return model.Comment{}, err
	}
	threadID = strings.TrimSpace(threadID)
	switch {
	case threadID == "":
		return model.Comment{}, errors.New("daemon: reply: no thread")
	case strings.TrimSpace(body) == "":
		return model.Comment{}, errors.New("daemon: reply: empty body")
	}
	rc, err := c.d.GH.Reply(ctx, threadID, body)
	if err != nil {
		return model.Comment{}, fmt.Errorf("daemon: reply to %s: %w", threadID, err)
	}
	rc = c.normaliseReply(ctx, it, threadID, rc)
	if err := c.d.Store.PutComment(ctx, rc); err != nil {
		return model.Comment{}, fmt.Errorf("daemon: store reply %s: %w", rc.ID, err)
	}
	return rc, nil
}

// normaliseReply fills what the stored copy of a posted reply needs: the
// item, a GitHub id ("gh-<GHID>", as the thread loader names it, so the
// next load replaces it in place), source, state, time and unit anchor.
func (c *InProcess) normaliseReply(ctx context.Context, it model.Item, threadID string, rc model.Comment) model.Comment {
	rc.ItemID = it.ID
	switch {
	case rc.GHID != 0:
		rc.ID = comments.GitHubID(rc.GHID)
	case rc.ID == "":
		rc.ID = c.newDraftID(it.ID)
	}
	rc.ThreadID = cmp.Or(rc.ThreadID, threadID)
	rc.Source = cmp.Or(rc.Source, model.CommentGitHub)
	rc.State = cmp.Or(rc.State, model.CommentPosted)
	if rc.CreatedAt.IsZero() {
		rc.CreatedAt = c.now()
	}
	// The reply mutation has no diff side: take the position from the
	// thread's stored comments (as the thread loader would).
	if cs, err := c.d.Store.Comments(ctx, it.ID); err == nil {
		for _, t := range cs {
			if t.ThreadID != rc.ThreadID || t.ID == rc.ID {
				continue
			}
			rc.Path, rc.Line, rc.Side = cmp.Or(t.Path, rc.Path), cmp.Or(t.Line, rc.Line), t.Side
			rc.StartLine, rc.StartSide, rc.Outdated = t.StartLine, t.StartSide, t.Outdated || rc.Outdated
			break
		}
	}
	units, err := c.d.Store.Units(ctx, it.ID)
	if err != nil {
		return rc
	}
	// Reanchor anchors a posted github comment (no outdated thread).
	if a := comments.Reanchor([]model.Comment{rc}, units); len(a) == 1 {
		rc = a[0]
	}
	return rc
}

// resolve resolves a GitHub review thread and marks its stored comments
// resolved (the picker's open-thread count drops by one when it was open).
func (c *InProcess) resolve(ctx context.Context, it model.Item, threadID string) error {
	if err := c.requirePR(it, "resolve"); err != nil {
		return err
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return errors.New("daemon: resolve: no thread")
	}
	if err := c.d.GH.ResolveThread(ctx, threadID); err != nil {
		return fmt.Errorf("daemon: resolve %s: %w", threadID, err)
	}
	cs, err := c.d.Store.Comments(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("daemon: resolve %s: %w", threadID, err)
	}
	wasOpen := false
	for _, cm := range cs {
		if cm.ThreadID != threadID || cm.State == model.CommentResolved {
			continue
		}
		wasOpen = true
		cm.State = model.CommentResolved
		if err := c.d.Store.PutComment(ctx, cm); err != nil {
			return fmt.Errorf("daemon: resolve %s: %w", threadID, err)
		}
	}
	if !wasOpen || it.OpenThreads <= 0 {
		return nil
	}
	_, _, err = c.updateItem(ctx, it.ID, func(it *model.Item) { it.OpenThreads = max(0, it.OpenThreads-1) })
	return err
}

// MarkViewed implements Client: it records that the user looked at
// p.HeadSHA (comments.NextViewMark), writing only when the mark changed. It
// queues no re-run: "Since you looked" compares against PrevHeadSHA while
// HeadSHA is the analysed head, so the base the last analysis used stays
// the same when the user looks at that head.
func (c *InProcess) MarkViewed(ctx context.Context, p proto.ViewedParams) error {
	if _, err := c.d.Store.Item(ctx, p.ItemID); err != nil {
		return fmt.Errorf("daemon: viewed %s: %w", p.ItemID, err)
	}
	prev, ok, err := c.d.Store.ViewMark(ctx, p.ItemID)
	if err != nil {
		return fmt.Errorf("daemon: viewed %s: %w", p.ItemID, err)
	}
	next := comments.NextViewMark(prev, ok, p.ItemID, p.HeadSHA, c.now())
	if next.HeadSHA == "" || (ok && next == prev) {
		return nil
	}
	if err := c.d.Store.PutViewMark(ctx, next); err != nil {
		return fmt.Errorf("daemon: viewed %s: %w", p.ItemID, err)
	}
	return nil
}

// Override implements Client: it stores a label override and queues an
// aggregate-only run of the item (pipeline.RunOptions.AggregateOnly).
func (c *InProcess) Override(ctx context.Context, p proto.OverrideParams) error {
	it, err := c.d.Store.Item(ctx, p.ItemID)
	if err != nil {
		return fmt.Errorf("daemon: override %s: %w", p.ItemID, err)
	}
	o, err := c.normaliseOverride(p.Override)
	if err != nil {
		return err
	}
	if err := c.applyOverride(ctx, it, o); err != nil {
		return err
	}
	c.patch(it.ID)
	return nil
}

// normaliseOverride checks an override's unit and question and stamps it.
func (c *InProcess) normaliseOverride(o model.Override) (model.Override, error) {
	o.UnitID, o.Question = strings.TrimSpace(o.UnitID), strings.TrimSpace(o.Question)
	if o.UnitID == "" || o.Question == "" {
		return o, fmt.Errorf("daemon: override: needs a unit and a question (got %q, %q)", o.UnitID, o.Question)
	}
	if o.At.IsZero() {
		o.At = c.now()
	}
	return o, nil
}

// applyOverride stores o and queues an aggregate-only run of the item
// (spec: "An override invalidates aggregate only"); as a rerun job it
// follows a run already in flight, and merged with a queued full run it
// becomes part of that run. Overrides are keyed by unit, not item (spec
// Data model), so every other stored item holding the unit (a branch and
// its PR, stacked PRs) is re-aggregated too.
func (c *InProcess) applyOverride(ctx context.Context, it model.Item, o model.Override) error {
	if err := c.d.Store.PutOverride(ctx, o); err != nil {
		return fmt.Errorf("daemon: override %s/%s: %w", o.UnitID, o.Question, err)
	}
	// The item the user overrode is an explicit request; the other items
	// holding the unit follow automatically (gated by the strategy: an
	// item it does not allow re-aggregates free-only).
	if err := c.markRequested(ctx, it); err != nil {
		return err
	}
	jobs := []job{{itemID: it.ID, urgency: it.Urgency, rerun: true, aggOnly: true, explicit: true}}
	for _, other := range c.itemsWithUnit(ctx, o.UnitID, it.ID) {
		jobs = append(jobs, job{itemID: other.ID, urgency: other.Urgency, rerun: true, aggOnly: true})
	}
	c.q.push(jobs...)
	return nil
}

// itemsWithUnit returns the stored, analysed items other than skip whose
// units include unitID (previews left out). Errors are logged and yield
// fewer items: the others catch up on their next run.
func (c *InProcess) itemsWithUnit(ctx context.Context, unitID, skip string) []model.Item {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		c.log.Warn("override: items", "err", err)
		return nil
	}
	var out []model.Item
	for _, it := range items {
		if it.ID == skip || it.Source == model.SourcePreview || it.AnalysedSHA == "" {
			continue
		}
		us, err := c.d.Store.Units(ctx, it.ID)
		if err != nil {
			c.log.Warn("override: units", "item", it.ID, "err", err)
			continue
		}
		for _, u := range us {
			if u.ID == unitID {
				out = append(out, it)
				break
			}
		}
	}
	return out
}

// lockComments serialises comment writes of one item (the pipeline's
// included); call the returned function to unlock.
func (c *InProcess) lockComments(itemID string) func() {
	return c.commentLock(itemID)
}

// comment returns the stored comment id of itemID.
func (c *InProcess) comment(ctx context.Context, itemID, id string) (model.Comment, error) {
	if id == "" {
		return model.Comment{}, errors.New("daemon: comment: no comment id")
	}
	cs, err := c.d.Store.Comments(ctx, itemID)
	if err != nil {
		return model.Comment{}, fmt.Errorf("daemon: comments of %s: %w", itemID, err)
	}
	for _, cm := range cs {
		if cm.ID == id {
			return cm, nil
		}
	}
	return model.Comment{}, fmt.Errorf("daemon: comment %s: not found on %s", id, itemID)
}

// aiDraft returns the stored ai-draft id of itemID.
func (c *InProcess) aiDraft(ctx context.Context, itemID, id string) (model.Comment, error) {
	cm, err := c.comment(ctx, itemID, id)
	if err != nil {
		return cm, err
	}
	if cm.Source != model.CommentAIDraft {
		return model.Comment{}, fmt.Errorf("daemon: comment %s is not an ai-draft (source %s)", id, cm.Source)
	}
	return cm, nil
}

// isUserDraft reports whether cm is a local, editable user draft.
func isUserDraft(cm model.Comment) bool {
	return cm.Source == model.CommentUser && cm.State == model.CommentDraft
}

// requirePR refuses GitHub writes on items that are not PRs or when
// GitHub is unavailable (with the reason SetGitHubNote recorded).
func (c *InProcess) requirePR(it model.Item, op string) error {
	if err := c.requireGitHub(op); err != nil {
		return err
	}
	if it.Kind != model.KindPR || it.Number <= 0 {
		return fmt.Errorf("daemon: %s: %s is not a pull request", op, it.ID)
	}
	return nil
}

// requireGitHub returns an error naming why GitHub is unavailable.
func (c *InProcess) requireGitHub(op string) error {
	if c.d.GH == nil {
		return fmt.Errorf("daemon: %s: GitHub unavailable: %s", op, c.gitHubNote())
	}
	return nil
}

// author is a new draft's author: the GitHub login (cached after the
// first successful lookup), else fallbackAuthor.
func (c *InProcess) author(ctx context.Context) string {
	return cmp.Or(c.viewerLogin(ctx), fallbackAuthor)
}

// viewerLogin returns the GitHub login ("" without GitHub or when the
// lookup fails; failures are not cached).
func (c *InProcess) viewerLogin(ctx context.Context) string {
	if c.d.GH == nil {
		return ""
	}
	c.viewerMu.Lock()
	defer c.viewerMu.Unlock()
	if c.viewer != "" {
		return c.viewer
	}
	login, err := c.d.GH.Viewer(ctx)
	if err != nil {
		c.log.Warn("viewer lookup failed", "err", err)
		return ""
	}
	c.viewer = login
	return login
}

// newDraftID returns a fresh local comment id for itemID.
func (c *InProcess) newDraftID(itemID string) string {
	return comments.DraftID(itemID, c.now(), c.draftSeq.Add(1))
}

// patch tells screens to re-read the item's Detail.
func (c *InProcess) patch(itemID string) {
	c.send(proto.Event{Type: proto.EventPatch, ItemID: itemID})
}

// draftHead is the head the stored units, and so the item's draft
// positions, describe: UnitsSHA (set by stage 1 with the units), else
// AnalysedSHA, else HeadSHA. Reviews are posted on it.
func draftHead(it model.Item) string { return cmp.Or(it.UnitsSHA, it.AnalysedSHA, it.HeadSHA) }

// sortComments orders comments by path, line, start line, then id (the
// Comments strip's stable order).
func sortComments(cs []model.Comment) {
	slices.SortStableFunc(cs, func(a, b model.Comment) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line), cmp.Compare(a.StartLine, b.StartLine), cmp.Compare(a.ID, b.ID))
	})
}
