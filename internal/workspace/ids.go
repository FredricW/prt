package workspace

import (
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// The out* functions rewrite a member's results to workspace ids: every
// item id becomes model.RepoItemID(key, id) and Item.Repo the key. They
// copy every slice and pointer they change, so a member's values are never
// modified in place.

// pfx prefixes a repo-local id ("" stays "").
func pfx(key, id string) string {
	if id == "" {
		return ""
	}
	return model.RepoItemID(key, id)
}

// localOf strips key's prefix from an incoming id. An id with no prefix is
// already local and is kept; one with another repo's prefix is kept too, so
// the member rejects it as a mismatch.
func localOf(key, id string) string {
	if k, local := model.SplitRepoItemID(id); k == key {
		return local
	}
	return id
}

// mapEach returns a copy of in with f applied to every element (nil stays
// nil).
func mapEach[T any](in []T, f func(*T)) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	copy(out, in)
	for i := range out {
		f(&out[i])
	}
	return out
}

// outItem prefixes the item's id and sets Repo; an item without an id (a
// failed call's zero value) is returned unchanged.
func outItem(key string, it model.Item) model.Item {
	if it.ID == "" {
		return it
	}
	it.ID = pfx(key, it.ID)
	it.Repo = key
	return it
}

func outItems(key string, items []model.Item) []model.Item {
	return mapEach(items, func(it *model.Item) { *it = outItem(key, *it) })
}

func outComment(key string, cm model.Comment) model.Comment {
	cm.ItemID = pfx(key, cm.ItemID)
	return cm
}

func outComments(key string, cms []model.Comment) []model.Comment {
	return mapEach(cms, func(cm *model.Comment) { cm.ItemID = pfx(key, cm.ItemID) })
}

func outReviews(key string, rs []model.Review) []model.Review {
	return mapEach(rs, func(r *model.Review) { r.ItemID = pfx(key, r.ItemID) })
}

func outRunLog(key string, rl model.RunLog) model.RunLog {
	rl.ItemID = pfx(key, rl.ItemID)
	return rl
}

func outIDs(key string, ids []string) []string {
	return mapEach(ids, func(id *string) { *id = pfx(key, *id) })
}

func outCalls(key string, calls []model.ReviewCall) []model.ReviewCall {
	return mapEach(calls, func(rc *model.ReviewCall) { rc.ItemID = pfx(key, rc.ItemID) })
}

// outDetail rewrites every item id of a Detail: the item, files, units,
// groups, reviews, comments, finding marks, run logs, the aggregate and the
// run's live routing table.
func outDetail(key string, d proto.Detail) proto.Detail {
	d.Item = outItem(key, d.Item)
	d.Files = mapEach(d.Files, func(f *model.File) { f.ItemID = pfx(key, f.ItemID) })
	d.Units = mapEach(d.Units, func(u *model.ChangeUnit) { u.ItemID = pfx(key, u.ItemID) })
	d.Groups = mapEach(d.Groups, func(g *model.Group) { g.ItemID = pfx(key, g.ItemID) })
	d.Reviews = outReviews(key, d.Reviews)
	d.Comments = outComments(key, d.Comments)
	d.FindingMarks = mapEach(d.FindingMarks, func(fm *model.FindingMark) { fm.ItemID = pfx(key, fm.ItemID) })
	d.RunLogs = mapEach(d.RunLogs, func(rl *model.RunLog) { rl.ItemID = pfx(key, rl.ItemID) })
	if d.Agg != nil {
		agg := *d.Agg
		agg.ItemID = pfx(key, agg.ItemID)
		d.Agg = &agg
	}
	if d.Run != nil {
		run := *d.Run
		run.Reviews = outReviews(key, run.Reviews)
		d.Run = &run
	}
	return d
}

func outSubmit(key string, r proto.SubmitResult) proto.SubmitResult {
	r.Posted = outComments(key, r.Posted)
	r.Skipped = outComments(key, r.Skipped)
	return r
}

func outCreatePR(key string, r proto.CreatePRResult) proto.CreatePRResult {
	r.Item = outItem(key, r.Item)
	return r
}

// outEvent rewrites an event: its ItemID, Item and RunLog, and Repo = key.
func outEvent(key string, e proto.Event) proto.Event {
	e.Repo = key
	e.ItemID = pfx(key, e.ItemID)
	if e.Item != nil {
		it := outItem(key, *e.Item)
		e.Item = &it
	}
	if e.RunLog != nil {
		rl := outRunLog(key, *e.RunLog)
		e.RunLog = &rl
	}
	return e
}
