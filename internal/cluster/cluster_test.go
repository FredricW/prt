package cluster

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

// unitMod adjusts a test unit.
type unitMod func(*model.ChangeUnit)

func atPath(p string) unitMod   { return func(u *model.ChangeUnit) { u.Path = p } }
func sym(s string) unitMod      { return func(u *model.ChangeUnit) { u.Symbol = s } }
func shapeOf(h string) unitMod  { return func(u *model.ChangeUnit) { u.ShapeHash = h } }
func withDiff(d string) unitMod { return func(u *model.ChangeUnit) { u.Diff = d } }

// u is a unit with path src/<id>.go; items() sets Order.
func u(id string, mods ...unitMod) model.ChangeUnit {
	cu := model.ChangeUnit{ID: id, ItemID: "pr:1", Path: "src/" + id + ".go", Adds: 1}
	for _, m := range mods {
		m(&cu)
	}
	return cu
}

// items numbers units in the given order.
func items(us ...model.ChangeUnit) []model.ChangeUnit {
	for i := range us {
		us[i].Order = i
	}
	return us
}

// unitsOf makes plain units in order.
func unitsOf(ids ...string) []model.ChangeUnit {
	us := make([]model.ChangeUnit, len(ids))
	for i, id := range ids {
		us[i] = u(id)
	}
	return items(us...)
}

func st(from, to string, kind model.LinkKind) model.Link {
	return model.Link{From: from, To: to, Kind: kind, Weight: 1, Source: model.LinkStructural}
}

func same(from, to string) model.Link { return st(from, to, model.LinkSameSymbol) }

func jv(from, to string, w, p float64) model.Link {
	return model.Link{From: from, To: to, Kind: model.LinkEmbedNeighbor, Weight: w, Source: model.LinkJev, Relation: model.RelSameIntent, P: p}
}

func emb(from, to string, w float64) model.Link {
	return model.Link{From: from, To: to, Kind: model.LinkEmbedNeighbor, Weight: w, Source: model.LinkEmbed}
}

func mc(from, to string) model.Link {
	return model.Link{From: from, To: to, Kind: model.LinkMechanical, Weight: 1, Source: model.LinkEmbed}
}

func mcJev(from, to string) model.Link {
	return model.Link{From: from, To: to, Kind: model.LinkMechanical, Weight: 1, Source: model.LinkJev, Relation: model.RelSamePattern, P: 0.9}
}

// describe renders groups as "FLAGS:ids" (M mechanical, U ungrouped, E user
// edited), in result order.
func describe(gs []model.Group) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		flags := ""
		if g.Mechanical {
			flags += "M"
		}
		if g.Ungrouped {
			flags += "U"
		}
		if g.UserEdited {
			flags += "E"
		}
		ids := strings.Join(g.UnitIDs, ",")
		if flags != "" {
			ids = flags + ":" + ids
		}
		out[i] = ids
	}
	return out
}

func assertGroups(t *testing.T, r Result, want []string) {
	t.Helper()
	if got := describe(r.Groups); !reflect.DeepEqual(got, want) {
		t.Errorf("groups = %q, want %q", got, want)
	}
	checkInvariants(t, r, nil)
}

func TestComponents(t *testing.T) {
	tests := []struct {
		name  string
		units []model.ChangeUnit
		links []model.Link
		want  []string
	}{
		{
			name:  "simple components",
			units: unitsOf("a", "b", "c", "d"),
			links: []model.Link{st("a", "b", model.LinkCalls), st("c", "d", model.LinkImports)},
			want:  []string{"b,a", "d,c"},
		},
		{
			name:  "ungrouped singletons",
			units: unitsOf("a", "b", "c"),
			links: []model.Link{same("a", "b")},
			want:  []string{"a,b", "U:c"},
		},
		{
			name:  "structural plus jev edges",
			units: unitsOf("a", "b", "c", "d"),
			links: []model.Link{same("a", "b"), jv("b", "c", 0.8, 0.7)},
			want:  []string{"a,b,c", "U:d"},
		},
		{
			name:  "unconfirmed embed candidates ignored",
			units: unitsOf("a", "b"),
			links: []model.Link{emb("a", "b", 0.95)},
			want:  []string{"U:a", "U:b"},
		},
		{
			name:  "foreign and self links ignored",
			units: unitsOf("a", "b"),
			links: []model.Link{same("a", "zz"), same("a", "a")},
			want:  []string{"U:a", "U:b"},
		},
		{
			name:  "empty item",
			units: nil,
			want:  []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Cluster(Input{ItemID: "pr:1", Units: tc.units, Links: tc.links})
			assertGroups(t, r, tc.want)
		})
	}
}

func TestSplitOversized(t *testing.T) {
	tests := []struct {
		name  string
		max   int
		units []model.ChangeUnit
		links []model.Link
		want  []string
		cut   int
	}{
		{
			name:  "weakest jev link cut first",
			max:   3,
			units: unitsOf("a", "b", "c", "d", "e"),
			links: []model.Link{same("a", "b"), jv("b", "c", 0.80, 0.9), same("c", "d"), jv("d", "e", 0.90, 0.9)},
			want:  []string{"a,b", "c,d,e"},
			cut:   1,
		},
		{
			name:  "weight tie: lowest P cut",
			max:   2,
			units: unitsOf("a", "b", "c"),
			links: []model.Link{jv("a", "b", 0.8, 0.7), jv("b", "c", 0.8, 0.9)},
			want:  []string{"b,c", "U:a"},
			cut:   1,
		},
		{
			name:  "full tie: last link by order cut",
			max:   2,
			units: unitsOf("a", "b", "c"),
			links: []model.Link{jv("b", "c", 0.8, 0.9), jv("a", "b", 0.8, 0.9)},
			want:  []string{"a,b", "U:c"},
			cut:   1,
		},
		{
			name:  "structural-only oversize splits at its bridge",
			max:   2,
			units: unitsOf("a", "b", "c", "d"),
			links: []model.Link{same("a", "b"), same("b", "c"), same("c", "d")},
			want:  []string{"a,b", "c,d"},
			cut:   1,
		},
		{
			name:  "structural star stays whole (no singleton is split off)",
			max:   3,
			units: unitsOf("h", "a", "b", "c", "d"),
			links: []model.Link{st("a", "h", model.LinkCalls), st("b", "h", model.LinkCalls), st("c", "h", model.LinkCalls), st("d", "h", model.LinkCalls)},
			want:  []string{"h,a,b,c,d"},
		},
		{
			name:  "test unit bridging two features is cut loose first",
			max:   4,
			units: unitsOf("f1", "c1", "f2", "c2", "t"),
			links: []model.Link{
				st("c1", "f1", model.LinkCalls), st("c2", "f2", model.LinkCalls),
				st("t", "f1", model.LinkTests), st("t", "f2", model.LinkTests),
			},
			// Both test links have equal betweenness; the later pair (t–f2)
			// goes first and splits {f2,c2} off.
			want: []string{"f1,c1,t", "f2,c2"},
			cut:  1,
		},
		{
			name:  "all links of a cut pair count as cut",
			max:   2,
			units: unitsOf("a", "b", "c", "d"),
			links: []model.Link{same("a", "b"), st("b", "c", model.LinkCalls), st("b", "c", model.LinkTests), same("c", "d")},
			want:  []string{"a,b", "c,d"},
			cut:   2,
		},
		{
			name:  "stops when no jev link remains inside",
			max:   2,
			units: unitsOf("a", "b", "c", "d"),
			links: []model.Link{same("a", "b"), same("b", "c"), jv("c", "d", 0.9, 0.9)},
			want:  []string{"a,b,c", "U:d"},
			cut:   1,
		},
		{
			name:  "cycle needs two cuts",
			max:   2,
			units: unitsOf("a", "b", "c", "d"),
			links: []model.Link{same("a", "b"), jv("b", "c", 0.7, 0.9), same("c", "d"), jv("d", "a", 0.75, 0.9)},
			want:  []string{"a,b", "c,d"},
			cut:   2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Cluster(Input{ItemID: "pr:1", Units: tc.units, Links: tc.links, Options: Options{MaxGroupUnits: tc.max}})
			assertGroups(t, r, tc.want)
			if r.Stats.CutLinks != tc.cut {
				t.Errorf("cut links = %d, want %d", r.Stats.CutLinks, tc.cut)
			}
			// Every link is either listed in exactly one group or cut.
			listed := 0
			for _, g := range r.Groups {
				listed += len(g.Links)
			}
			if listed+r.Stats.CutLinks != len(tc.links) {
				t.Errorf("listed %d + cut %d links, want %d", listed, r.Stats.CutLinks, len(tc.links))
			}
		})
	}
}

func TestMechanical(t *testing.T) {
	mechUnits := func() []model.ChangeUnit {
		return items(u("e", shapeOf("s")), u("m1", shapeOf("s")), u("m2", shapeOf("s")), u("x", shapeOf("t")), u("c"))
	}
	tests := []struct {
		name    string
		units   []model.ChangeUnit
		links   []model.Link
		want    []string
		ejected int
	}{
		{
			name:    "verified group with one ejection",
			units:   mechUnits(),
			links:   []model.Link{mc("e", "m1"), mc("e", "m2"), mc("e", "x")},
			want:    []string{"M:e,m1,m2", "U:x", "U:c"},
			ejected: 1,
		},
		{
			name:  "class of two forms no group",
			units: mechUnits(),
			links: []model.Link{mc("e", "m1")},
			want:  []string{"U:e", "U:m1", "U:m2", "U:x", "U:c"},
		},
		{
			name:  "jev mechanical link in class of two acts as edge",
			units: mechUnits(),
			links: []model.Link{mcJev("e", "m1")},
			want:  []string{"e,m1", "U:m2", "U:x", "U:c"},
		},
		{
			name:  "members are removed from the general graph",
			units: mechUnits(),
			links: []model.Link{mc("e", "m1"), mc("e", "m2"), st("c", "m1", model.LinkCalls), jv("x", "m2", 0.9, 0.9)},
			want:  []string{"M:e,m1,m2", "U:x", "U:c"},
		},
		{
			// Same shape hash ($s : $s), different literal edits: the
			// shape alone would collapse three unrelated JSON edits.
			name: "same shape, different literals is ejected",
			units: items(
				u("e", shapeOf("s"), withDiff("-  \"k\": \"old\",\n+  \"k\": \"new\",\n")),
				u("m1", shapeOf("s"), withDiff("-  \"k\": \"old\",\n+  \"k\": \"new\",\n")),
				u("m2", shapeOf("s"), withDiff("-  \"k\": \"old\",\n+  \"k\": \"new\",\n")),
				u("x", shapeOf("s"), withDiff("-  \"k\": \"alt\",\n+  \"k\": \"neu\",\n")),
			),
			links:   []model.Link{mc("e", "m1"), mc("e", "m2"), mc("e", "x")},
			want:    []string{"M:e,m1,m2", "U:x"},
			ejected: 1,
		},
		{
			// A rename at call sites with different arguments is one edit.
			name: "rename with different context verifies",
			units: items(
				u("e", shapeOf("s"), withDiff("-\tgetUser(a)\n+\tfetchUser(a)\n")),
				u("m1", shapeOf("s"), withDiff("-\tgetUser(b, \"x\")\n+\tfetchUser(b, \"x\")\n")),
				u("m2", shapeOf("s"), withDiff("-  getUser(c)\n+  fetchUser(c)\n")),
			),
			links: []model.Link{mc("e", "m1"), mc("e", "m2")},
			want:  []string{"M:e,m1,m2"},
		},
		{
			// Regression: the lowest-Order unit of the shape class makes
			// another literal edit. The four identical renames still group,
			// with their own example; the odd one is ejected.
			name: "odd lowest-order unit does not sink the class",
			units: items(
				u("a", shapeOf("s"), withDiff("-\tfoo(x)\n+\tbar(x)\n")),
				u("b", shapeOf("s"), withDiff("-\tgetUser(id)\n+\tfetchUser(id)\n")),
				u("c", shapeOf("s"), withDiff("-\tgetUser(id)\n+\tfetchUser(id)\n")),
				u("d", shapeOf("s"), withDiff("-\tgetUser(id)\n+\tfetchUser(id)\n")),
				u("e", shapeOf("s"), withDiff("-\tgetUser(id)\n+\tfetchUser(id)\n")),
			),
			links:   []model.Link{mc("a", "b"), mc("a", "c"), mc("a", "d"), mc("a", "e")},
			want:    []string{"M:b,c,d,e", "U:a"},
			ejected: 1,
		},
		{
			name: "two literal edits of one shape form two groups",
			units: items(
				u("a", shapeOf("s"), withDiff("-\"v\": \"1.0\"\n+\"v\": \"1.1\"\n")),
				u("b", shapeOf("s"), withDiff("-\"n\": \"a\"\n+\"n\": \"b\"\n")),
				u("c", shapeOf("s"), withDiff("-\"v\": \"1.0\"\n+\"v\": \"1.1\"\n")),
				u("d", shapeOf("s"), withDiff("-\"n\": \"a\"\n+\"n\": \"b\"\n")),
				u("e", shapeOf("s"), withDiff("-\"v\": \"1.0\"\n+\"v\": \"1.1\"\n")),
				u("f", shapeOf("s"), withDiff("-\"n\": \"a\"\n+\"n\": \"b\"\n")),
			),
			links: []model.Link{mc("a", "c"), mc("a", "e"), mc("a", "b"), mc("b", "d"), mc("b", "f")},
			want:  []string{"M:a,c,e", "M:b,d,f"},
		},
		{
			name:  "empty shape hash never verifies",
			units: items(u("e"), u("m1"), u("m2")),
			links: []model.Link{mc("e", "m1"), mc("e", "m2")},
			want:  []string{"U:e", "U:m1", "U:m2"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Cluster(Input{ItemID: "pr:1", Units: tc.units, Links: tc.links})
			assertGroups(t, r, tc.want)
			if r.Stats.Ejected != tc.ejected {
				t.Errorf("ejected = %d, want %d", r.Stats.Ejected, tc.ejected)
			}
		})
	}

	r := Cluster(Input{ItemID: "pr:1", Units: mechUnits(), Links: []model.Link{mc("e", "x"), mc("e", "m2"), mc("e", "m1")}})
	g := r.Groups[0]
	if g.ExampleUnit != "e" || g.Count != 3 || !reflect.DeepEqual(g.Ejected, []string{"x"}) {
		t.Errorf("mechanical group = %+v", g)
	}
	if want := []model.Link{mc("e", "m1"), mc("e", "m2")}; !reflect.DeepEqual(g.Links, want) {
		t.Errorf("mechanical links = %+v, want %+v", g.Links, want)
	}
	wantStats := model.GroupingStats{Units: 5, Groups: 1, Mechanical: 1, MechanicalCollapsed: 2, Ejected: 1, Ungrouped: 2}
	if !reflect.DeepEqual(r.Stats, wantStats) {
		t.Errorf("stats = %+v, want %+v", r.Stats, wantStats)
	}

	// A partition re-rooted away from the candidate star's root gets
	// code-made example → member links, and the odd root is ejected.
	rename := withDiff("-\tgetUser(id)\n+\tfetchUser(id)\n")
	r = Cluster(Input{ItemID: "pr:1", Units: items(
		u("a", shapeOf("s"), withDiff("-\tfoo(x)\n+\tbar(x)\n")),
		u("b", shapeOf("s"), rename), u("c", shapeOf("s"), rename), u("d", shapeOf("s"), rename),
	), Links: []model.Link{mc("a", "b"), mc("a", "c"), mc("a", "d")}})
	g = r.Groups[0]
	if g.ExampleUnit != "b" || g.Count != 3 || !reflect.DeepEqual(g.Ejected, []string{"a"}) {
		t.Errorf("re-rooted group = %+v", g)
	}
	if want := []model.Link{mc("b", "c"), mc("b", "d")}; !reflect.DeepEqual(g.Links, want) {
		t.Errorf("re-rooted links = %+v, want %+v", g.Links, want)
	}
}

func TestRolesAndOrder(t *testing.T) {
	iface := model.Label{UnitID: "b", Question: model.QInterface, Value: model.LabelYes, P: 0.8, Confidence: 0.8}
	tests := []struct {
		name   string
		units  []model.ChangeUnit
		links  []model.Link
		labels []model.Label
		want   []string
	}{
		{
			name:   "interface via label",
			units:  unitsOf("a", "b"),
			links:  []model.Link{same("a", "b")},
			labels: []model.Label{iface},
			want:   []string{"b,a"},
		},
		{
			name:   "unknown interface label ignored",
			units:  unitsOf("a", "b"),
			links:  []model.Link{same("a", "b")},
			labels: []model.Label{{UnitID: "b", Question: model.QInterface, Value: model.LabelUnknown, P: 0.9}},
			want:   []string{"a,b"},
		},
		{
			name:  "interface via incoming calls",
			units: unitsOf("a", "b", "c"),
			links: []model.Link{st("a", "c", model.LinkCalls), same("a", "b")},
			want:  []string{"c,a,b"},
		},
		{
			name:  "test via path",
			units: items(u("t", atPath("src/x_test.go")), u("a")),
			links: []model.Link{same("t", "a")},
			want:  []string{"a,t"},
		},
		{
			name:  "test via tests link",
			units: unitsOf("t", "a"),
			links: []model.Link{st("t", "a", model.LinkTests)},
			want:  []string{"a,t"},
		},
		{
			name: "group category order",
			units: items(
				u("u1"),
				u("m1", shapeOf("s")), u("m2", shapeOf("s")), u("m3", shapeOf("s")),
				u("t1", atPath("web/__tests__/a.ts")), u("t2", atPath("web/b.spec.tsx")),
				u("c1"), u("c2"),
				u("i1"), u("i2"),
			),
			links: []model.Link{
				mc("m1", "m2"), mc("m1", "m3"), same("t1", "t2"), same("c1", "c2"), st("i1", "i2", model.LinkImports),
			},
			want: []string{"i2,i1", "c1,c2", "t1,t2", "M:m1,m2,m3", "U:u1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Cluster(Input{ItemID: "pr:1", Units: tc.units, Links: tc.links, Labels: tc.labels})
			assertGroups(t, r, tc.want)
		})
	}
}

func TestPlaceholderNames(t *testing.T) {
	us := items(
		u("f1", sym("fetchUser")), u("f2"), u("f3"),
		u("p1", atPath("web/cache.ts")), u("p2"),
		u("m1", sym("rename"), shapeOf("s")), u("m2", shapeOf("s")), u("m3", shapeOf("s")),
		u("lone", atPath("cmd/main.go")),
	)
	links := []model.Link{same("f1", "f2"), same("f1", "f3"), same("p1", "p2"), mc("m1", "m2"), mc("m1", "m3")}
	r := Cluster(Input{ItemID: "pr:1", Units: us, Links: links})
	var names []string
	for _, g := range r.Groups {
		names = append(names, g.Name)
		if g.NameSource != model.NamePlaceholder {
			t.Errorf("group %v name source %q", g.UnitIDs, g.NameSource)
		}
	}
	want := []string{"fetchUser (+2)", "cache.ts (+1)", "rename ×3", "main.go"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %q, want %q", names, want)
	}
}

func TestEdits(t *testing.T) {
	edit := func(op model.GroupEditOp, ids ...string) model.GroupEdit {
		return model.GroupEdit{ItemID: "pr:1", Op: op, Units: ids}
	}
	pairs := []model.Link{same("a", "b"), same("c", "d")}
	mech4 := items(u("e", shapeOf("s")), u("m1", shapeOf("s")), u("m2", shapeOf("s")), u("m3", shapeOf("s")))
	tests := []struct {
		name    string
		units   []model.ChangeUnit
		links   []model.Link
		edits   []model.GroupEdit
		want    []string
		applied int
	}{
		{
			name:    "merge across groups",
			units:   unitsOf("a", "b", "c", "d"),
			links:   pairs,
			edits:   []model.GroupEdit{edit(model.EditMerge, "b", "c")},
			want:    []string{"E:b,c", "UE:a", "UE:d"},
			applied: 1,
		},
		{
			name:    "split",
			units:   unitsOf("a", "b", "c"),
			links:   []model.Link{same("a", "b"), same("b", "c")},
			edits:   []model.GroupEdit{edit(model.EditSplit, "c")},
			want:    []string{"E:a,b", "UE:c"},
			applied: 1,
		},
		{
			name:  "unknown units ignored",
			units: unitsOf("a", "b", "c", "d"),
			links: pairs,
			edits: []model.GroupEdit{edit(model.EditMerge, "zz"), edit(model.EditMerge), edit(model.EditReset, "a")},
			want:  []string{"a,b", "c,d"},
		},
		{
			name:    "unknown units dropped from an applied edit",
			units:   unitsOf("a", "b", "c", "d"),
			links:   pairs,
			edits:   []model.GroupEdit{edit(model.EditMerge, "zz", "d", "a")},
			want:    []string{"E:a,d", "UE:b", "UE:c"},
			applied: 1,
		},
		{
			name:  "merge with one surviving unit is skipped",
			units: unitsOf("a", "b", "c", "d"),
			links: pairs,
			edits: []model.GroupEdit{edit(model.EditMerge, "a", "zz")},
			want:  []string{"a,b", "c,d"},
		},
		{
			name:    "sequential edits",
			units:   unitsOf("a", "b", "c", "d"),
			links:   pairs,
			edits:   []model.GroupEdit{edit(model.EditMerge, "a", "b", "c", "d"), edit(model.EditSplit, "d")},
			want:    []string{"E:a,b,c", "UE:d"},
			applied: 2,
		},
		{
			name:    "mechanical group shrinking below the minimum",
			units:   mech4[:3],
			links:   []model.Link{mc("e", "m1"), mc("e", "m2")},
			edits:   []model.GroupEdit{edit(model.EditSplit, "m2")},
			want:    []string{"E:e,m1", "UE:m2"},
			applied: 1,
		},
		{
			name:    "mechanical group staying at the minimum",
			units:   mech4,
			links:   []model.Link{mc("e", "m1"), mc("e", "m2"), mc("e", "m3")},
			edits:   []model.GroupEdit{edit(model.EditSplit, "m3")},
			want:    []string{"ME:e,m1,m2", "UE:m3"},
			applied: 1,
		},
		{
			name:    "mechanical group losing its example",
			units:   mech4,
			links:   []model.Link{mc("e", "m1"), mc("e", "m2"), mc("e", "m3")},
			edits:   []model.GroupEdit{edit(model.EditSplit, "e")},
			want:    []string{"ME:m1,m2,m3", "UE:e"},
			applied: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Cluster(Input{ItemID: "pr:1", Units: tc.units, Links: tc.links, Edits: tc.edits})
			assertGroups(t, r, tc.want)
			if r.Stats.UserEdits != tc.applied {
				t.Errorf("user edits = %d, want %d", r.Stats.UserEdits, tc.applied)
			}
		})
	}
}

func TestEditDetails(t *testing.T) {
	us := items(u("e", shapeOf("s")), u("m1", shapeOf("s")), u("m2", shapeOf("s")), u("m3", shapeOf("s")), u("c"))
	links := []model.Link{mc("e", "m1"), mc("e", "m2"), mc("e", "m3"), st("c", "m1", model.LinkCalls)}

	r := Cluster(Input{ItemID: "pr:1", Units: us, Links: links, Edits: []model.GroupEdit{{Op: model.EditSplit, Units: []string{"m3"}}}})
	g := r.Groups[0]
	if !g.Mechanical || g.Count != 3 || g.ExampleUnit != "e" || g.Name != "e.go ×3" {
		t.Errorf("shrunk mechanical group = %+v", g)
	}
	if want := []model.Link{mc("e", "m1"), mc("e", "m2")}; !reflect.DeepEqual(g.Links, want) {
		t.Errorf("links = %+v, want %+v", g.Links, want)
	}

	r = Cluster(Input{ItemID: "pr:1", Units: us, Links: links, Edits: []model.GroupEdit{{Op: model.EditMerge, Units: []string{"c", "m1"}}}})
	merged := r.Groups[0]
	if merged.Mechanical || !merged.UserEdited || !reflect.DeepEqual(merged.UnitIDs, []string{"m1", "c"}) {
		t.Fatalf("merged group = %+v", merged)
	}
	if want := []model.Link{st("c", "m1", model.LinkCalls)}; !reflect.DeepEqual(merged.Links, want) {
		t.Errorf("merged links = %+v, want %+v", merged.Links, want)
	}
	if merged.Name != "m1.go (+1)" {
		t.Errorf("merged name = %q", merged.Name)
	}
}

// shuffled returns copies of units and links in a random order.
func shuffled(rng *rand.Rand, us []model.ChangeUnit, ls []model.Link) ([]model.ChangeUnit, []model.Link) {
	u2 := append([]model.ChangeUnit(nil), us...)
	l2 := append([]model.Link(nil), ls...)
	rng.Shuffle(len(u2), func(i, j int) { u2[i], u2[j] = u2[j], u2[i] })
	rng.Shuffle(len(l2), func(i, j int) { l2[i], l2[j] = l2[j], l2[i] })
	return u2, l2
}

// randomInput builds a random item: n units with a few shape classes and
// links of every kind and source.
func randomInput(rng *rand.Rand, n int) Input {
	us := make([]model.ChangeUnit, n)
	for i := range us {
		id := fmt.Sprintf("u%02d", i)
		var mods []unitMod
		if rng.Intn(3) == 0 {
			mods = append(mods, shapeOf(fmt.Sprintf("s%d", rng.Intn(3))))
		}
		if rng.Intn(5) == 0 {
			mods = append(mods, atPath("src/"+id+"_test.go"))
		}
		if rng.Intn(2) == 0 {
			mods = append(mods, sym("fn"+id))
		}
		us[i] = u(id, mods...)
		us[i].Order = i
	}
	pick := func() string { return us[rng.Intn(n)].ID }
	kinds := []model.LinkKind{model.LinkCalls, model.LinkTests, model.LinkImports, model.LinkMoved, model.LinkSameSymbol}
	var ls []model.Link
	for range n {
		ls = append(ls, st(pick(), pick(), kinds[rng.Intn(len(kinds))]))
	}
	for range n {
		w := float64(rng.Intn(5))/10 + 0.5
		ls = append(ls, jv(pick(), pick(), w, float64(rng.Intn(4))/10+0.6), emb(pick(), pick(), w))
	}
	for range n / 2 {
		if rng.Intn(4) == 0 {
			ls = append(ls, mcJev(pick(), pick()))
		} else {
			ls = append(ls, mc(pick(), pick()))
		}
	}
	ls = append(ls, same(us[0].ID, "missing"))
	var edits []model.GroupEdit
	ops := []model.GroupEditOp{model.EditMerge, model.EditSplit, model.EditReset}
	for range 4 {
		edits = append(edits, model.GroupEdit{Op: ops[rng.Intn(len(ops))], Units: []string{pick(), pick(), "gone"}})
	}
	return Input{ItemID: "pr:9", Units: us, Links: ls, Edits: edits, Options: Options{MaxGroupUnits: 4, MechanicalMin: 2}}
}

// bigStructural is a connected structural graph of n units: a random
// spanning tree plus 2n random calls links.
func bigStructural(n int) ([]model.ChangeUnit, []model.Link) {
	rng := rand.New(rand.NewSource(int64(n)))
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("u%04d", i)
	}
	var ls []model.Link
	for i := 1; i < n; i++ {
		ls = append(ls, st(ids[i], ids[rng.Intn(i)], model.LinkCalls))
	}
	for range 2 * n {
		a, b := rng.Intn(n), rng.Intn(n)
		if a != b {
			ls = append(ls, st(ids[a], ids[b], model.LinkCalls))
		}
	}
	return unitsOf(ids...), ls
}

// Regression: the structural split recomputed betweenness after every
// removal (O(V·E²); n=400 took about two minutes). It must stay fast.
func TestStructuralSplitScales(t *testing.T) {
	us, ls := bigStructural(400)
	start := time.Now()
	r := Cluster(Input{ItemID: "pr:1", Units: us, Links: ls})
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("400-unit structural split took %v", d)
	}
	checkInvariants(t, r, us)
}

func TestClusterContextCancelled(t *testing.T) {
	us, ls := bigStructural(200)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ClusterContext(ctx, Input{ItemID: "pr:1", Units: us, Links: ls}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestDeterministic(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	in := randomInput(rng, 30)
	want := Cluster(in)
	for i := range 20 {
		in2 := in
		in2.Units, in2.Links = shuffled(rng, in.Units, in.Links)
		if got := Cluster(in2); !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffle %d: result differs\n got %q\nwant %q", i, describe(got.Groups), describe(want.Groups))
		}
	}
}

func TestRandomGraphInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := range 50 {
		in := randomInput(rng, 5+rng.Intn(40))
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			checkInvariants(t, Cluster(in), in.Units)
		})
	}
}

// checkInvariants checks that every unit is in exactly one group (when units
// is non-nil), ids match GroupID(UnitIDs), Order is the index and stats add
// up.
func checkInvariants(t *testing.T, r Result, units []model.ChangeUnit) {
	t.Helper()
	seen := map[string]int{}
	ungrouped, mech := 0, 0
	for i, g := range r.Groups {
		if g.ID != model.GroupID(g.UnitIDs) || g.Order != i || len(g.UnitIDs) == 0 {
			t.Errorf("group %d: id/order/units wrong: %+v", i, g)
		}
		if g.Ungrouped {
			ungrouped++
			if len(g.UnitIDs) != 1 || g.Mechanical {
				t.Errorf("ungrouped group %d: %+v", i, g)
			}
		}
		if g.Mechanical {
			mech++
			if g.Count != len(g.UnitIDs) || g.UnitIDs[0] != g.ExampleUnit {
				t.Errorf("mechanical group %d: %+v", i, g)
			}
		}
		for _, id := range g.UnitIDs {
			seen[id]++
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("unit %s in %d groups", id, n)
		}
	}
	if units != nil && len(seen) != len(units) {
		t.Errorf("%d units grouped, want %d", len(seen), len(units))
	}
	if r.Stats.Ungrouped != ungrouped || r.Stats.Mechanical != mech || r.Stats.Groups != len(r.Groups)-ungrouped {
		t.Errorf("stats %+v disagree with groups", r.Stats)
	}
}
