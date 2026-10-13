package codeview

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// ! on an ai-draft whose unit has no label to correct still dismisses the
// finding (without an override) instead of only showing a note.
func TestOverrideOnAIDraftWithoutLabelsDismisses(t *testing.T) {
	d := loadComments(t)
	var ls []model.Label
	for _, l := range d.Labels {
		if l.UnitID != "u1" {
			ls = append(ls, l)
		}
	}
	d.Labels = ls
	m := newModel(t, d, 120, 40)
	allOff(&m)
	moveTo(t, &m, model.SideNew, 13, "")
	press(t, &m, "!")
	op, ok := m.PendingComment()
	if !ok || !reflect.DeepEqual(op, CommentOp{Kind: CommentOpDismiss, CommentID: "ai-k1"}) {
		t.Fatalf("op = %+v (%v), note %q", op, ok, m.Note())
	}
	if m.Capturing() || !strings.Contains(m.Note(), "dismissed") {
		t.Errorf("capturing %v note %q", m.Capturing(), m.Note())
	}
}

// a and d act on the ai-draft selected in the focused strip, so a finding
// the pane cannot place (off the diff) can still be accepted or dismissed.
func TestStripActsOnSelectedAIDraft(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want CommentOpKind
	}{{"a", CommentOpAccept}, {"d", CommentOpDismiss}} {
		m := commentModel(t, 120, 40)
		m.SetStripFocus(true)
		sel := -1
		for i, b := range m.c.blocks {
			if b.kind == blockAI {
				sel = i
			}
		}
		if sel < 0 {
			t.Fatal("fixture has no ai-draft block")
		}
		m.stripSel = sel
		press(t, &m, tc.key)
		op, ok := m.PendingComment()
		if !ok || op.Kind != tc.want || op.CommentID != "ai-k1" {
			t.Errorf("%s in the strip: op %+v (%v)", tc.key, op, ok)
		}
	}
}
