package codeview

import (
	"cmp"
	"slices"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/model"
)

// blockKind is what an inline comment block shows.
type blockKind uint8

const (
	blockUser   blockKind = iota // one of the user's comments: draft, pending or posted
	blockAI                      // an ai-draft (a review finding not yet accepted or dismissed)
	blockThread                  // a GitHub review thread: its first comment and the replies
)

// cblock is one comment block of Detail.Comments as the Code page draws it:
// a user comment, an ai-draft, or a GitHub thread (all comments sharing a
// ThreadID). unit and line say where it is drawn: under line index line of
// unit's diff lines, or under the unit header when line is -1 (outdated, or
// anchored to a unit by id only); unit "" means it is listed in the strip
// only.
type cblock struct {
	id      string
	kind    blockKind
	c       model.Comment   // the comment; a thread's first comment
	replies []model.Comment // a thread's other comments, oldest first
	unit    string
	line    int
}

// resolved reports whether a thread block is resolved on GitHub.
func (b *cblock) resolved() bool {
	if b.kind != blockThread {
		return false
	}
	if b.c.State == model.CommentResolved {
		return true
	}
	for _, r := range b.replies {
		if r.State == model.CommentResolved {
			return true
		}
	}
	return false
}

// openThread reports whether b is an unresolved GitHub thread (❱ in the
// gutter).
func (b *cblock) openThread() bool { return b.kind == blockThread && !b.resolved() }

// editable reports whether b is a user draft C can edit.
func (b *cblock) editable() bool {
	return b.kind == blockUser && b.c.State == model.CommentDraft
}

// linesOf returns unit id's parsed diff lines, parsing each unit once.
func (c *content) linesOf(id string) []diff.Line {
	if ls, ok := c.lineCache[id]; ok {
		return ls
	}
	u := c.units[id]
	if u == nil {
		return nil
	}
	ls := unitLines(u)
	c.lineCache[id] = ls
	return ls
}

// indexComments builds c.blocks from the detail's comments: GitHub
// comments are grouped by ThreadID, blocks are sorted by position and each
// is placed on a unit line (see placeComment).
func indexComments(c *content, cs []model.Comment) {
	c.unitBlocks = map[string][]int{}
	threads := map[string]int{}
	for _, cm := range cs {
		switch cm.Source {
		case model.CommentGitHub:
			if bi, ok := threads[cm.ThreadID]; ok && cm.ThreadID != "" {
				c.blocks[bi].replies = append(c.blocks[bi].replies, cm)
				continue
			}
			id := "thread:" + cm.ThreadID
			if cm.ThreadID == "" {
				id = "thread:" + cm.ID
			}
			threads[cm.ThreadID] = len(c.blocks)
			c.blocks = append(c.blocks, cblock{id: id, kind: blockThread, c: cm})
		case model.CommentAIDraft:
			c.blocks = append(c.blocks, cblock{id: cm.ID, kind: blockAI, c: cm})
		default:
			c.blocks = append(c.blocks, cblock{id: cm.ID, kind: blockUser, c: cm})
		}
	}
	for i := range c.blocks {
		if c.blocks[i].kind == blockThread {
			orderThread(&c.blocks[i])
		}
	}
	slices.SortStableFunc(c.blocks, func(a, b cblock) int {
		return cmp.Or(cmp.Compare(a.c.Path, b.c.Path), cmp.Compare(a.c.Line, b.c.Line),
			cmp.Compare(a.c.StartLine, b.c.StartLine), cmp.Compare(a.id, b.id))
	})
	for i := range c.blocks {
		b := &c.blocks[i]
		b.unit, b.line = c.placeComment(b.c)
		if b.unit != "" {
			c.unitBlocks[b.unit] = append(c.unitBlocks[b.unit], i)
		}
	}
}

// orderThread makes the thread's top-level comment (InReplyTo 0, oldest)
// its first comment and sorts the replies oldest first.
func orderThread(b *cblock) {
	all := append([]model.Comment{b.c}, b.replies...)
	slices.SortStableFunc(all, func(x, y model.Comment) int {
		if (x.InReplyTo == 0) != (y.InReplyTo == 0) {
			if x.InReplyTo == 0 {
				return -1
			}
			return 1
		}
		return cmp.Or(x.CreatedAt.Compare(y.CreatedAt), cmp.Compare(x.GHID, y.GHID))
	})
	b.c, b.replies = all[0], all[1:]
}

// placeComment finds where a comment is drawn: the diff line matching its
// path, side and line (in its UnitID first, then any unit of its path in
// file order); else under the header of its UnitID when that unit exists
// (outdated comments always go there); else nowhere ("", -1).
func (c *content) placeComment(cm model.Comment) (string, int) {
	if !cm.Outdated && cm.Line > 0 {
		if u := c.units[cm.UnitID]; u != nil && u.Path == cm.Path {
			if li := lineIndex(c.linesOf(u.ID), cm.Side, cm.Line); li >= 0 {
				return u.ID, li
			}
		}
		for _, f := range c.files {
			if f.path != cm.Path {
				continue
			}
			for _, id := range f.units {
				if li := lineIndex(c.linesOf(id), cm.Side, cm.Line); li >= 0 {
					return id, li
				}
			}
		}
	}
	if c.units[cm.UnitID] != nil {
		return cm.UnitID, -1
	}
	return "", -1
}

// lineIndex returns the index of the line numbered n on side among ls
// (old: deleted and context lines by old number; new: added and context
// lines by new number), or -1.
func lineIndex(ls []diff.Line, side model.Side, n int) int {
	for i, l := range ls {
		switch {
		case side == model.SideOld && l.Op != diff.Add && l.OldNo == n:
			return i
		case side != model.SideOld && l.Op != diff.Del && l.NewNo == n:
			return i
		}
	}
	return -1
}

// lineSide returns the side and number a diff line is commented on: old
// for a deleted line, new for added and context lines.
func lineSide(l diff.Line) (model.Side, int) {
	if l.Op == diff.Del {
		return model.SideOld, l.OldNo
	}
	return model.SideNew, l.NewNo
}

// hasComments reports whether unit id draws at least one comment block
// (the "has comments" filter; such units never collapse).
func (c *content) hasComments(id string) bool { return len(c.unitBlocks[id]) > 0 }

// blockIndex returns the index of the block with id, or -1.
func (c *content) blockIndex(id string) int {
	for i := range c.blocks {
		if c.blocks[i].id == id {
			return i
		}
	}
	return -1
}

// blocksAt returns the indices of unit id's blocks drawn under line li
// (-1: under the header), in order.
func (c *content) blocksAt(id string, li int) []int {
	var out []int
	for _, bi := range c.unitBlocks[id] {
		if c.blocks[bi].line == li {
			out = append(out, bi)
		}
	}
	return out
}

// threadLines returns the line indices of unit id that carry an unresolved
// GitHub thread (nil when none).
func (c *content) threadLines(id string) map[int]bool {
	var out map[int]bool
	for _, bi := range c.unitBlocks[id] {
		if b := &c.blocks[bi]; b.line >= 0 && b.openThread() {
			if out == nil {
				out = map[int]bool{}
			}
			out[b.line] = true
		}
	}
	return out
}

// indexSince fills the "changed since last look" set from Agg.Since.
func indexSince(c *content, s *model.SinceLooked) {
	if s == nil {
		return
	}
	c.sinceKnown = true
	c.since = make(map[string]bool, len(s.ChangedUnits))
	for _, id := range s.ChangedUnits {
		c.since[id] = true
	}
}
