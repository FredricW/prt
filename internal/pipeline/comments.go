package pipeline

import (
	"context"

	"github.com/fredricw/prt/internal/comments"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/model"
)

// loadThreads loads a PR's review threads and reconciles the item's stored
// comments with them (comments.MergeGitHub). It runs inside the fetch
// stage on every run of a PR with GitHub, cache hits included (threads
// change without pushes), and counts the call on that row. Failure is
// soft: it is logged and noted on the row ("threads unavailable: …") and
// the stored comments are kept.
func (p *Pipeline) loadThreads(ctx context.Context, it model.Item, rl *model.RunLog) {
	if !p.canUseGitHub(it) {
		return
	}
	rl.Calls++
	ts, err := p.d.GH.Threads(ctx, it.Number)
	if err != nil {
		if ctx.Err() == nil {
			p.d.Log.Warn("review threads unavailable; stored comments kept", "item", it.ID, "err", err)
			rl.Note = joinNotes(rl.Note, "threads unavailable: "+err.Error())
		}
		return
	}
	stored, err := p.d.Store.Comments(ctx, it.ID)
	if err != nil {
		p.d.Log.Warn("load comments failed; threads not merged", "item", it.ID, "err", err)
		rl.Note = joinNotes(rl.Note, "threads not merged: "+err.Error())
		return
	}
	put, del := comments.MergeGitHub(stored, threadComments(it.ID, ts))
	p.writeComments(ctx, it.ID, put, del)
}

// threadComments flattens review threads into github comments, normalised
// defensively (the client should already have done this): ID
// comments.GitHubID(GHID), ItemID, Source github, ThreadID, Path/Line/Side
// (and StartLine) from the thread when the comment lacks them (Line falls
// back to the thread's OriginalLine), State resolved for a resolved thread
// and posted otherwise, Outdated when the comment or thread is (or the
// thread has only an original line), as gh.API does. Comments with neither a
// GHID nor an ID are dropped.
func threadComments(itemID string, ts []gh.Thread) []model.Comment {
	var out []model.Comment
	for _, t := range ts {
		for _, c := range t.Comments {
			if c.GHID != 0 {
				c.ID = comments.GitHubID(c.GHID)
			}
			if c.ID == "" {
				continue
			}
			out = append(out, normaliseThreadComment(itemID, t, c))
		}
	}
	return out
}

// normaliseThreadComment fills c's thread-level fields from t.
func normaliseThreadComment(itemID string, t gh.Thread, c model.Comment) model.Comment {
	c.ItemID, c.Source, c.ThreadID = itemID, model.CommentGitHub, t.ID
	if c.Path == "" {
		c.Path = t.Path
	}
	if c.Side == "" {
		c.Side = t.Side
	}
	if c.Line == 0 {
		c.Line = t.Line
		if c.Line == 0 {
			c.Line = t.OriginalLine
		}
	}
	if c.StartLine == 0 {
		c.StartLine = t.StartLine
	}
	c.State = model.CommentPosted
	if t.Resolved {
		c.State = model.CommentResolved
	}
	c.Outdated = c.Outdated || t.Outdated || (t.Line == 0 && t.OriginalLine != 0)
	return c
}

// reanchorComments moves the item's stored comments onto the units stage 1
// just stored (comments.Reanchor) and stores the ones that changed. Store
// errors are logged only: comments never fail the parse stage.
func (p *Pipeline) reanchorComments(ctx context.Context, itemID string, units []model.ChangeUnit) {
	stored, err := p.d.Store.Comments(ctx, itemID)
	if err != nil {
		p.d.Log.Warn("load comments failed; not re-anchored", "item", itemID, "err", err)
		return
	}
	p.writeComments(ctx, itemID, comments.Reanchor(stored, units), nil)
}

// syncAIDrafts materialises one ai-draft per finding of the routing rows
// (comments.AIDrafts, skipping findings the user accepted or dismissed)
// and deletes the ai-drafts whose finding is gone (comments.SyncAIDrafts).
// Errors are logged only.
func (p *Pipeline) syncAIDrafts(ctx context.Context, itemID string, rows []model.Review, units []model.ChangeUnit) {
	marks, err := p.d.Store.FindingMarks(ctx, itemID)
	if err != nil {
		p.d.Log.Warn("load finding marks failed; ai-drafts not synced", "item", itemID, "err", err)
		return
	}
	stored, err := p.d.Store.Comments(ctx, itemID)
	if err != nil {
		p.d.Log.Warn("load comments failed; ai-drafts not synced", "item", itemID, "err", err)
		return
	}
	put, del := comments.SyncAIDrafts(stored, comments.AIDrafts(itemID, rows, units, marks, p.d.Now()))
	p.writeComments(ctx, itemID, put, del)
}

// writeComments stores put and deletes del, logging failures.
func (p *Pipeline) writeComments(ctx context.Context, itemID string, put []model.Comment, del []string) {
	for _, c := range put {
		if err := p.d.Store.PutComment(ctx, c); err != nil {
			p.d.Log.Warn("store comment failed", "item", itemID, "comment", c.ID, "err", err)
		}
	}
	for _, id := range del {
		if err := p.d.Store.DeleteComment(ctx, id); err != nil {
			p.d.Log.Warn("delete comment failed", "item", itemID, "comment", id, "err", err)
		}
	}
}
