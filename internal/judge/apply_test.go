package judge

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/model"
)

func relLabel(a, b, v string, p float64) model.Label {
	return model.Label{UnitID: model.PairSubject(a, b), Question: model.QRelation, Value: v, P: p, Confidence: p, Model: "m"}
}

func TestConfirm(t *testing.T) {
	cands := []model.Link{
		{From: "b", To: "a", Kind: model.LinkEmbedNeighbor, Weight: 0.91, Source: model.LinkEmbed}, // 0.60 kept
		{From: "c", To: "d", Kind: model.LinkEmbedNeighbor, Weight: 0.85, Source: model.LinkEmbed}, // 0.59 dropped
		{From: "e", To: "f", Kind: model.LinkMechanical, Weight: 1, Source: model.LinkEmbed},       // unrelated
		{From: "g", To: "h", Kind: model.LinkEmbedNeighbor, Weight: 0.8, Source: model.LinkEmbed},  // unknown
		{From: "i", To: "j", Kind: model.LinkEmbedNeighbor, Weight: 0.8, Source: model.LinkEmbed},  // no label
		{From: "k", To: "l", Kind: model.LinkMechanical, Weight: 1, Source: model.LinkEmbed},       // 0.95 kept
	}
	labels := []model.Label{
		relLabel("a", "b", model.RelCallerUpdate, 0.60),
		relLabel("c", "d", model.RelSameIntent, 0.59),
		relLabel("e", "f", model.RelUnrelated, 0.99),
		{UnitID: model.PairSubject("g", "h"), Question: model.QRelation, Value: model.LabelUnknown},
		relLabel("k", "l", model.RelSamePattern, 0.95),
		{UnitID: model.PairSubject("i", "j"), Question: "other", Value: model.RelSameIntent, P: 1},
	}
	kept, rejected := Confirm(cands, labels, 0)
	want := []model.Link{
		{From: "b", To: "a", Kind: model.LinkEmbedNeighbor, Weight: 0.91, Source: model.LinkJev, Relation: model.RelCallerUpdate, P: 0.60},
		{From: "k", To: "l", Kind: model.LinkMechanical, Weight: 1, Source: model.LinkJev, Relation: model.RelSamePattern, P: 0.95},
	}
	if !reflect.DeepEqual(kept, want) || rejected != 2 {
		t.Errorf("kept %+v rejected %d", kept, rejected)
	}
	if cands[0].Source != model.LinkEmbed {
		t.Errorf("input mutated")
	}
	kept, rejected = Confirm(cands, labels, 0.97)
	if len(kept) != 0 || rejected != 4 {
		t.Errorf("keepP 0.97: kept %d rejected %d", len(kept), rejected)
	}
}

func TestApplyNames(t *testing.T) {
	long := strings.Repeat("ø", 70)
	bullets := []string{"Retry failed fetches", long}
	ctx := NameCtx("t", bullets)
	nl := func(g, v string, p float64, c string) model.Label {
		return model.Label{UnitID: model.GroupSubject(g), Question: model.QNameBullet, Value: v, P: p, Confidence: p, Ctx: c}
	}
	groups := []model.Group{
		{ID: "g1", Name: "ph1", NameSource: model.NamePlaceholder},
		{ID: "g2", Name: "ph2", NameSource: model.NamePlaceholder},
		{ID: "g3", Name: "ph3", NameSource: model.NamePlaceholder}, // none
		{ID: "g4", Name: "ph4", NameSource: model.NamePlaceholder}, // stale ctx
		{ID: "g5", Name: "ph5", NameSource: model.NamePlaceholder}, // P < 0.5
		{ID: "g6", Name: "ph6", NameSource: model.NamePlaceholder}, // b3 out of range
		{ID: "g7", Name: "ph7", NameSource: model.NamePlaceholder}, // unknown
	}
	labels := []model.Label{
		nl("g1", "b1", 0.8, ctx),
		nl("g2", "b2", 0.5, ctx),
		nl("g3", model.BulletNone, 0.9, ctx),
		nl("g4", "b1", 0.9, "stale"),
		nl("g5", "b1", 0.49, ctx),
		nl("g6", "b3", 0.9, ctx),
		{UnitID: model.GroupSubject("g7"), Question: model.QNameBullet, Value: model.LabelUnknown, Ctx: ctx},
	}
	orig := append([]model.Group(nil), groups...)
	out := ApplyNames(groups, labels, bullets, ctx)
	if !reflect.DeepEqual(groups, orig) {
		t.Fatalf("input mutated")
	}
	want0 := model.Group{ID: "g1", Name: "Retry failed fetches", NameSource: model.NameBullet, NameP: 0.8, DescBullet: "Retry failed fetches"}
	if !reflect.DeepEqual(out[0], want0) {
		t.Errorf("g1 = %+v", out[0])
	}
	if utf8.RuneCountInString(out[1].Name) != 60 || !strings.HasSuffix(out[1].Name, "…") || !utf8.ValidString(out[1].Name) ||
		out[1].DescBullet != long || out[1].NameP != 0.5 {
		t.Errorf("g2 = %+v", out[1])
	}
	for i := 2; i < len(out); i++ {
		if !reflect.DeepEqual(out[i], orig[i]) {
			t.Errorf("group %s changed: %+v", out[i].ID, out[i])
		}
	}
	if got := cutRunes(strings.Repeat("x", 60), 60); got != strings.Repeat("x", 60) {
		t.Errorf("60 runes cut: %q", got)
	}
}

func TestSuggestion(t *testing.T) {
	units := []model.ChangeUnit{
		{ID: "u1", Path: "a.go"}, {ID: "u2", Path: "b.go"}, {ID: "u3", Path: "b.go"}, {ID: "u4", Path: "c.go"}, {ID: "u5", Path: "d.go"},
	}
	groups := []model.Group{
		{ID: "g2", UnitIDs: []string{"u3", "u1"}},
		{ID: "g1", UnitIDs: []string{"u2"}},
		{ID: "g3", UnitIDs: []string{"u4"}},
	}
	ctx := "c1"
	fl := func(path string, p float64, c string) model.Label {
		v := model.LabelNo
		if p >= 0.5 {
			v = model.LabelYes
		}
		return model.Label{UnitID: model.FileSubject("pr:1", path), Question: model.QSeparatePR, Value: v, P: p, Ctx: c}
	}
	labels := []model.Label{
		fl("a.go", 0.7, ctx),
		fl("b.go", 0.9, ctx),
		fl("c.go", 0.7, ctx),
		fl("d.go", 0.49, ctx),
		fl("e.go", 0.99, "stale"),
		{UnitID: model.FileSubject("pr:2", "x.go"), Question: model.QSeparatePR, Value: model.LabelYes, P: 1, Ctx: ctx},
	}
	got := Suggestion("pr:1", units, groups, labels, ctx)
	want := &model.SplitSuggestion{Files: []model.SplitFile{
		{Path: "b.go", P: 0.9, GroupIDs: []string{"g2", "g1"}},
		{Path: "a.go", P: 0.7, GroupIDs: []string{"g2"}},
		{Path: "c.go", P: 0.7, GroupIDs: []string{"g3"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("suggestion\n got %+v\nwant %+v", got, want)
	}
	if s := Suggestion("pr:1", units, groups, labels[3:5], ctx); s != nil {
		t.Errorf("want nil, got %+v", s)
	}
	if s := Suggestion("pr:1", units, groups, nil, ctx); s != nil {
		t.Errorf("want nil, got %+v", s)
	}
}
