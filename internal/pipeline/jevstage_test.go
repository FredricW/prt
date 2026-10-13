package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
	"github.com/fredricw/prt/internal/store"
)

// labelDiff touches an owned web file, a server file, a script, a test and
// a doc; every hunk adds lines.
const labelDiff = `diff --git a/web/src/api/client.ts b/web/src/api/client.ts
--- a/web/src/api/client.ts
+++ b/web/src/api/client.ts
@@ -1,3 +1,4 @@
 export function getUser(id: string) {
-  return fetchJSON("/users/" + id);
+  const url = "/users/" + id;
+  return fetchJSON(url);
 }
diff --git a/server/users.go b/server/users.go
--- a/server/users.go
+++ b/server/users.go
@@ -1,2 +1,3 @@
 package server
+var Limit = 10
 func F() {}
diff --git a/scripts/report.py b/scripts/report.py
--- a/scripts/report.py
+++ b/scripts/report.py
@@ -1 +1,2 @@
 import os
+import sys
diff --git a/web/src/__tests__/client.test.ts b/web/src/__tests__/client.test.ts
--- a/web/src/__tests__/client.test.ts
+++ b/web/src/__tests__/client.test.ts
@@ -1 +1,2 @@
 import { getUser } from "../api/client";
+test("x", () => {});
diff --git a/docs/notes.md b/docs/notes.md
--- a/docs/notes.md
+++ b/docs/notes.md
@@ -1 +1,2 @@
 # Notes
+More.
`

var labelNow = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// labelEnv is a pipeline over fakes for one PR item with labelDiff.
type labelEnv struct {
	st     store.Store
	git    *gitx.Fake
	item   model.Item
	events []model.RunLog
}

func newLabelEnv(t *testing.T) *labelEnv {
	t.Helper()
	e := &labelEnv{
		st: newStore(t),
		git: &gitx.Fake{
			MergeBases: map[string]string{"main..h1": "b1"},
			Diffs:      map[string]string{"b1..h1": labelDiff},
		},
		item: model.Item{ID: model.PRItemID(1), Kind: model.KindPR, Number: 1, Ref: "feat/x", BaseRef: "main", HeadSHA: "h1",
			Title: "feat: users", Body: "- Build the url once\n- Add a limit\n", CreatedAt: labelNow.Add(-24 * time.Hour)},
	}
	if err := e.st.UpsertItem(context.Background(), e.item); err != nil {
		t.Fatal(err)
	}
	return e
}

// pipeline builds a Pipeline sharing the env's store; jc may be nil.
func (e *labelEnv) pipeline(jc jev.Client, mutate ...func(*Deps)) *Pipeline {
	d := Deps{Git: e.git, Store: e.st, Config: config.Defaults(), Questions: questions.Default(),
		Now: func() time.Time { return labelNow }, Emit: func(r model.RunLog) { e.events = append(e.events, r) }}
	if jc != nil {
		d.Jev = jc
	}
	for _, m := range mutate {
		m(&d)
	}
	return New(d)
}

func (e *labelEnv) run(t *testing.T, p *Pipeline) {
	t.Helper()
	e.events = nil
	if err := p.Run(context.Background(), e.item); err != nil {
		t.Fatal(err)
	}
}

// stage returns the finished RunLog row of stage s from the last run.
func (e *labelEnv) stage(t *testing.T, s model.Stage) model.RunLog {
	t.Helper()
	for i := len(e.events) - 1; i >= 0; i-- {
		if r := e.events[i]; r.Stage == s && r.Status != model.StageRunning {
			return r
		}
	}
	t.Fatalf("no finished %s row in %+v", s, e.events)
	return model.RunLog{}
}

func (e *labelEnv) units(t *testing.T) []model.ChangeUnit {
	t.Helper()
	us, err := e.st.Units(context.Background(), e.item.ID)
	if err != nil || len(us) == 0 {
		t.Fatalf("units = %v, err %v", us, err)
	}
	return us
}

func (e *labelEnv) labels(t *testing.T) []model.Label {
	t.Helper()
	ls, err := e.st.Labels(context.Background(), unitIDs(e.units(t)))
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

func (e *labelEnv) storedItem(t *testing.T) model.Item {
	t.Helper()
	it, err := e.st.Item(context.Background(), e.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func (e *labelEnv) agg(t *testing.T) model.ItemAgg {
	t.Helper()
	agg, ok, err := e.st.Agg(context.Background(), e.item.ID)
	if err != nil || !ok {
		t.Fatalf("agg ok=%v err=%v", ok, err)
	}
	return agg
}

// assertAllUnknown checks every live unit question of every unit is stored
// as unknown and the item is marked labels-missing.
func (e *labelEnv) assertAllUnknown(t *testing.T) {
	t.Helper()
	have := map[string]bool{}
	for _, l := range e.labels(t) {
		if l.Line == 0 {
			if !model.IsUnknown(l) || l.Confidence != 0 {
				t.Errorf("label %+v, want unknown with confidence 0", l)
			}
			have[l.UnitID+"/"+l.Question] = true
		}
	}
	for _, u := range e.units(t) {
		for _, q := range liveLabelIDs(questions.Default(), questions.ScopeUnit) {
			if !have[u.ID+"/"+q] {
				t.Errorf("unit %s: no unknown %s label", u.Path, q)
			}
		}
	}
	if !e.agg(t).LabelsMissing || !e.storedItem(t).LabelsUnavailable {
		t.Error("agg.LabelsMissing / item.LabelsUnavailable not set")
	}
	if rl := e.stage(t, model.StageJev); rl.Status != model.StageDone || !strings.HasPrefix(rl.Note, "Jev unavailable") {
		t.Errorf("jev row = %+v", rl)
	}
}

// Passes with the labels stub (labels.Run is not called without Jev).
func TestJevNilWritesUnknownLabels(t *testing.T) {
	e := newLabelEnv(t)
	e.run(t, e.pipeline(nil))
	e.assertAllUnknown(t)
	// Labels unknown: routing sends the groups deep; with no reviewer the
	// review stage is skipped with a note.
	if rl := e.stage(t, model.StageRoute); rl.Status != model.StageDone || !strings.Contains(rl.Note, "labels unknown") {
		t.Errorf("route row = %+v", rl)
	}
	if rl := e.stage(t, model.StageReview); rl.Status != model.StageSkipped || !strings.HasPrefix(rl.Note, "reviews skipped: no reviewer configured") {
		t.Errorf("review row = %+v", rl)
	}
	for _, s := range []model.Stage{model.StageLink, model.StageCluster} {
		if rl := e.stage(t, s); rl.Status != model.StageDone {
			t.Errorf("%s row = %+v, want done", s, rl)
		}
	}
	if rl := e.stage(t, model.StageEmbed); rl.Status != model.StageSkipped || rl.Note != noteNoEmbedder {
		t.Errorf("embed row = %+v, want skipped with %q", rl, noteNoEmbedder)
	}
}

// Needs the real internal/labels (the stub labels nothing and never
// reports Unavailable).
func TestJevUnavailableWritesUnknownLabels(t *testing.T) {
	e := newLabelEnv(t)
	e.run(t, e.pipeline(&jev.Fake{Err: jev.ErrUnavailable}))
	e.assertAllUnknown(t)
}

// Needs the real internal/labels.
func TestJevLabelsCachedByUnit(t *testing.T) {
	e := newLabelEnv(t)
	fake := &jev.Fake{}
	p := e.pipeline(fake)
	e.run(t, p)
	first := len(fake.Calls)
	if first == 0 {
		t.Fatal("first run made no Jev calls")
	}
	for _, l := range e.labels(t) {
		if model.IsUnknown(l) {
			t.Fatalf("unknown label after a working run: %+v", l)
		}
	}
	rl := e.stage(t, model.StageJev)
	// Name and split requests belong to the cluster row.
	if want := first - countQuestion(fake.Calls, model.QNameBullet, model.QSeparatePR); rl.Calls != want || rl.Status != model.StageDone || rl.Out == 0 {
		t.Errorf("first jev row = %+v (fake saw %d label and pair calls)", rl, want)
	}
	if e.agg(t).LabelsMissing || e.storedItem(t).LabelsUnavailable {
		t.Error("labels-missing set after a working run")
	}

	e.run(t, p)
	if len(fake.Calls) != first {
		t.Errorf("second run made %d Jev calls, want 0", len(fake.Calls)-first)
	}
	if rl := e.stage(t, model.StageJev); !rl.CacheHit || rl.Status != model.StageCached || rl.Calls != 0 {
		t.Errorf("second jev row = %+v, want cache hit", rl)
	}
}

// countQuestion counts the requests that carry any of the question ids
// (wire names equal to the id or starting with "<id>__").
func countQuestion(reqs []jev.Request, ids ...string) int {
	n := 0
	for _, r := range reqs {
		found := false
		for name := range r.Questions {
			for _, id := range ids {
				found = found || name == id || strings.HasPrefix(name, id+"__")
			}
		}
		if found {
			n++
		}
	}
	return n
}

// Regression: answers that depend on item-level inputs (desc_bullet on the
// PR bullets, covered on the changed tests, relevance on role/owns) were
// cached by unit id alone. A branch analysed first (no description, so
// desc_bullet = none) made the PR from that branch reuse "none" forever.
func TestItemInputsInvalidateCachedLabels(t *testing.T) {
	e := newLabelEnv(t)
	fake := &jev.Fake{Answer: func(name string, q jev.Question, st any) jev.Answer {
		if name == model.QDescBullet {
			return jev.Answer{Type: jev.Choice, Choice: "b1", Probabilities: map[string]float64{"b1": 0.8, "b2": 0.1, "none": 0.1}}
		}
		return jev.DefaultAnswer(name, q, st)
	}}
	p := e.pipeline(fake)
	branch := model.Item{ID: model.BranchItemID("feat/x"), Kind: model.KindBranch, Ref: "feat/x", BaseRef: "main", HeadSHA: "h1"}
	if err := e.st.UpsertItem(context.Background(), branch); err != nil {
		t.Fatal(err)
	}
	if err := p.Run(context.Background(), branch); err != nil {
		t.Fatal(err)
	}
	before := len(fake.Calls)
	for _, c := range fake.Calls {
		if _, ok := c.Questions[model.QDescBullet]; ok {
			t.Fatal("desc_bullet sent for an item without bullets")
		}
	}

	e.run(t, p) // the PR from the same branch, with two bullets
	asked := 0
	for _, c := range fake.Calls[before:] {
		if _, ok := c.Questions[model.QDescBullet]; ok {
			asked++
		}
	}
	if units := e.units(t); asked != len(units) {
		t.Fatalf("desc_bullet asked for %d of %d units after the bullets appeared", asked, len(units))
	}
	if rl := e.stage(t, model.StageJev); rl.CacheHit {
		t.Errorf("jev row = %+v, want a miss", rl)
	}
	for _, ua := range e.agg(t).Units {
		if ua.DescBullet != 1 || ua.Undocumented {
			t.Errorf("unit %s desc bullet %d undocumented %v, want b1", ua.UnitID, ua.DescBullet, ua.Undocumented)
		}
	}

	// Same inputs again: a cache hit.
	n := len(fake.Calls)
	e.run(t, p)
	if len(fake.Calls) != n || !e.stage(t, model.StageJev).CacheHit {
		t.Errorf("unchanged inputs re-asked %d calls", len(fake.Calls)-n)
	}
}

func TestCurrentLabels(t *testing.T) {
	hashes := map[string]string{model.QDescBullet: "h2"}
	ls := []model.Label{
		{UnitID: "u", Question: model.QKind, Value: "refactor", Confidence: 0.9},
		{UnitID: "u", Question: model.QDescBullet, Value: "none", Confidence: 1, Ctx: "h1"},
		{UnitID: "v", Question: model.QDescBullet, Value: "b2", Confidence: 0.8, Ctx: "h2"},
	}
	got := currentLabels(ls, hashes)
	if got[0].Value != "refactor" || !model.IsUnknown(got[1]) || got[1].Ctx != "h2" || got[2].Value != "b2" {
		t.Errorf("currentLabels = %+v", got)
	}
}

// Needs the real internal/labels.
func TestJevUnknownsReaskedWhenJevReturns(t *testing.T) {
	e := newLabelEnv(t)
	e.run(t, e.pipeline(nil))
	e.assertAllUnknown(t)

	fake := &jev.Fake{}
	e.run(t, e.pipeline(fake))
	if len(fake.Calls) == 0 {
		t.Fatal("unknown labels were not re-asked")
	}
	for _, l := range e.labels(t) {
		if l.Line == 0 && model.IsUnknown(l) {
			t.Errorf("still unknown: %+v", l)
		}
	}
	if e.agg(t).LabelsMissing || e.storedItem(t).LabelsUnavailable {
		t.Error("labels-missing still set after Jev answered")
	}
}

// flakyJev fails the first line-question request with ErrUnavailable and
// answers everything else through Fake.
type flakyJev struct {
	jev.Fake
	mu     sync.Mutex
	failed bool
}

func (f *flakyJev) Evaluate(ctx context.Context, req jev.Request) (jev.Response, error) {
	f.mu.Lock()
	fail := false
	if !f.failed {
		for name := range req.Questions {
			if strings.Contains(name, "__l") {
				fail, f.failed = true, true
				break
			}
		}
	}
	f.mu.Unlock()
	if fail {
		return jev.Response{}, fmt.Errorf("%w: HTTP 429", jev.ErrUnavailable)
	}
	return f.Fake.Evaluate(ctx, req)
}

// Regression: one failed request (a 429 among many) marked the whole item
// labels-unavailable, and its line labels stayed unknown forever because
// one known line satisfied the cache check.
func TestJevPartialFailureKeepsLabelsAndReasks(t *testing.T) {
	e := newLabelEnv(t)
	fj := &flakyJev{}
	p := e.pipeline(fj)
	e.run(t, p)
	if e.agg(t).LabelsMissing || e.storedItem(t).LabelsUnavailable {
		t.Fatal("one failed request marked the item labels-unavailable")
	}
	rl := e.stage(t, model.StageJev)
	if !strings.Contains(rl.Note, "1 of") {
		t.Errorf("jev note = %q, want the partial failure count", rl.Note)
	}
	unknownLines := 0
	for _, l := range e.labels(t) {
		if l.Line > 0 && model.IsUnknown(l) {
			unknownLines++
		}
	}
	if unknownLines == 0 {
		t.Fatal("failed line batch left no unknown line labels")
	}

	n := len(fj.Calls)
	e.run(t, p)
	if len(fj.Calls) == n || e.stage(t, model.StageJev).CacheHit {
		t.Fatal("unknown line labels were not re-asked")
	}
	for _, l := range e.labels(t) {
		if model.IsUnknown(l) {
			t.Errorf("still unknown after Jev recovered: %+v", l)
		}
	}
}

// Needs the real internal/labels (a non-ErrUnavailable client error is a
// labels.Run error).
func TestJevErrorFailsStageSoftly(t *testing.T) {
	e := newLabelEnv(t)
	e.run(t, e.pipeline(&jev.Fake{Err: errors.New("bad request: boom")}))
	rl := e.stage(t, model.StageJev)
	if rl.Status != model.StageFailed || !strings.Contains(rl.Err, "boom") {
		t.Fatalf("jev row = %+v, want failed with the client error", rl)
	}
	if ag := e.stage(t, model.StageAggregate); ag.Status != model.StageDone {
		t.Errorf("aggregate row = %+v", ag)
	}
	if !e.agg(t).LabelsMissing || !e.storedItem(t).LabelsUnavailable {
		t.Error("labels-missing not set after a failed jev stage")
	}
	for _, l := range e.labels(t) {
		if !model.IsUnknown(l) {
			t.Errorf("label %+v, want unknown", l)
		}
	}
}

// Passes with the labels stub.
func TestFileOwnershipFromGlobsAndCodeowners(t *testing.T) {
	e := newLabelEnv(t)
	e.git.Files = map[string]map[string][]byte{"h1": {
		".github/CODEOWNERS": []byte("# owners\n*           @someone\nserver/    @alice\nscripts/** @alice\n/scripts/  @Fredric @bob\n"),
		// Ignored: .github/CODEOWNERS wins.
		"CODEOWNERS": []byte("* @fredric\n"),
	}}
	ghc := &gh.Fake{Login: "fredric"}
	e.run(t, e.pipeline(nil, func(d *Deps) {
		d.GH = ghc
		d.Config.User.Owns = []string{"web/**"}
	}))
	files, err := e.st.Files(context.Background(), e.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"web/src/api/client.ts":            true,  // owns glob
		"web/src/__tests__/client.test.ts": true,  // owns glob
		"server/users.go":                  false, // @alice
		"scripts/report.py":                true,  // last matching rule names @fredric
		"docs/notes.md":                    false, // "*" → @someone
	}
	for _, f := range files {
		if w, ok := want[f.Path]; !ok || f.Owned != w {
			t.Errorf("%s owned = %v, want %v", f.Path, f.Owned, w)
		}
	}

	// Without a viewer CODEOWNERS is ignored; globs still apply.
	e.run(t, e.pipeline(nil, func(d *Deps) { d.Config.User.Owns = []string{"docs/*.md"} }))
	files, _ = e.st.Files(context.Background(), e.item.ID)
	for _, f := range files {
		if f.Owned != (f.Path == "docs/notes.md") {
			t.Errorf("no GitHub: %s owned = %v", f.Path, f.Owned)
		}
	}
}

func TestViewerErrorIgnored(t *testing.T) {
	e := newLabelEnv(t)
	e.git.Files = map[string]map[string][]byte{"h1": {"CODEOWNERS": []byte("* @fredric\n")}}
	e.run(t, e.pipeline(nil, func(d *Deps) { d.GH = &gh.Fake{Login: "fredric", Err: errors.New("offline")} }))
	files, _ := e.st.Files(context.Background(), e.item.ID)
	for _, f := range files {
		if f.Owned {
			t.Errorf("%s owned without a viewer", f.Path)
		}
	}
}

func TestCodeownersGlob(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"*", "a/b/c.go", true},
		{"*.go", "a/b/c.go", true},
		{"*.go", "c.ts", false},
		{"/docs/", "docs/a/b.md", true},
		{"/docs/", "x/docs/b.md", false},
		{"docs/", "x/docs/b.md", true},
		{"apps/web", "apps/web/x.ts", true},
		{"apps/web", "x/apps/web/x.ts", false},
		{"web/**", "web/src/a.ts", true},
		{"/build/logs/", "build/logs/a.log", true},
		{"**/logs", "a/logs/b.txt", true},
	}
	for _, c := range cases {
		if got := matchPattern(codeownersGlob(c.pattern), c.path); got != c.want {
			t.Errorf("%q vs %q = %v (glob %q), want %v", c.pattern, c.path, got, codeownersGlob(c.pattern), c.want)
		}
	}
}

func TestIsTestPath(t *testing.T) {
	cases := map[string]bool{
		"server/users_test.go":             true,
		"server/users.go":                  false,
		"scripts/test_report.py":           true,
		"scripts/report_test.py":           true,
		"scripts/report.py":                false,
		"web/src/a.test.ts":                true,
		"web/src/a.spec.tsx":               true,
		"web/src/__tests__/helpers.ts":     true,
		"tests/fixtures/data.json":         true,
		"web/src/testing.ts":               false,
		"web/src/contest.ts":               false,
		"web/src/components/Latest.tsx":    false,
		"pkg/test/util.go":                 true,
		"web/src/api/client.test-utils.ts": false,
	}
	for p, want := range cases {
		if got := isTestPath(p); got != want {
			t.Errorf("isTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestUnitsNeedingLabels(t *testing.T) {
	cat := questions.Default()
	full := func(u model.ChangeUnit) []model.Label {
		var ls []model.Label
		for _, q := range liveLabelIDs(cat, questions.ScopeUnit) {
			ls = append(ls, model.Label{UnitID: u.ID, Question: q, Value: "x", Confidence: 0.9})
		}
		return ls
	}
	lines := func(u model.ChangeUnit, line int) []model.Label {
		var ls []model.Label
		for _, q := range liveLabelIDs(cat, questions.ScopeLine) {
			ls = append(ls, model.Label{UnitID: u.ID, Question: q, Line: line, Value: model.LabelNo})
		}
		return ls
	}
	del := model.ChangeUnit{ID: "del", Dels: 2}
	add := model.ChangeUnit{ID: "add", Adds: 1, Range: model.Range{New: model.Span{Start: 3, Count: 1}}, Diff: "+x"}
	partial := model.ChangeUnit{ID: "partial", Dels: 1}
	unknown := model.ChangeUnit{ID: "unknown", Dels: 1}
	noLines := model.ChangeUnit{ID: "nolines", Adds: 1, Diff: "+y"}
	// Regression: one labelled line used to satisfy every line, so a failed
	// line batch stayed unknown forever.
	someLines := model.ChangeUnit{ID: "somelines", Adds: 2, Diff: "+a\n+b"}
	unknownLine := model.ChangeUnit{ID: "unknownline", Adds: 2, Diff: "+a\n+b"}

	var stored []model.Label
	stored = append(stored, full(del)...)
	stored = append(stored, full(add)...)
	stored = append(stored, lines(add, 1)...) // Line = position among added lines
	stored = append(stored, full(someLines)...)
	stored = append(stored, lines(someLines, 1)...)
	stored = append(stored, full(unknownLine)...)
	stored = append(stored, lines(unknownLine, 1)...)
	ul := lines(unknownLine, 2)
	ul[0].Value = model.LabelUnknown
	stored = append(stored, ul...)
	stored = append(stored, full(partial)[1:]...)
	u := full(unknown)
	u[0].Value = model.LabelUnknown
	stored = append(stored, u...)
	stored = append(stored, full(noLines)...)

	got := unitsNeedingLabels([]model.ChangeUnit{del, add, partial, unknown, noLines, someLines, unknownLine}, stored, cat, nil)
	var ids []string
	for _, u := range got {
		ids = append(ids, u.ID)
	}
	if strings.Join(ids, ",") != "partial,unknown,nolines,somelines,unknownline" {
		t.Errorf("need = %v, want [partial unknown nolines somelines unknownline]", ids)
	}
}

func TestUnknownLabelsShape(t *testing.T) {
	cat := questions.Default()
	u := model.ChangeUnit{ID: "u", Adds: 2, Dels: 1, Range: model.Range{New: model.Span{Start: 10, Count: 3}},
		Diff: " a\n-b\n+c\n+d\n\\ No newline at end of file"}
	if got := model.AddedLineNumbers(u); len(got) != 2 || got[0] != 11 || got[1] != 12 {
		t.Fatalf("AddedLineNumbers = %v, want [11 12]", got)
	}
	ls := unknownLabels([]model.ChangeUnit{u}, cat, nil)
	for _, l := range ls {
		if l.Line != 0 && l.Line != 1 && l.Line != 2 {
			t.Errorf("line label at %d, want position 1 or 2", l.Line)
		}
	}
	nUnit, nLine := len(liveLabelIDs(cat, questions.ScopeUnit)), len(liveLabelIDs(cat, questions.ScopeLine))
	if len(ls) != nUnit+2*nLine {
		t.Fatalf("%d labels, want %d", len(ls), nUnit+2*nLine)
	}
	var sawRiskTag bool
	for _, l := range ls {
		if !model.IsUnknown(l) || l.P != 0 || l.Confidence != 0 || l.Probs != nil || l.Model != "" {
			t.Errorf("not an unknown placeholder: %+v", l)
		}
		sawRiskTag = sawRiskTag || l.Question == model.SubQuestion(model.QRiskTags, "auth")
	}
	if !sawRiskTag {
		t.Error("risk_tags not expanded into sub-questions")
	}
}

// Unknown placeholders never overwrite a known stored answer.
func TestUnknownsKeepKnownAnswers(t *testing.T) {
	cat := questions.Default()
	u := model.ChangeUnit{ID: "u", Dels: 1}
	stored := []model.Label{
		{UnitID: "u", Question: model.QKind, Value: "refactor", Confidence: 0.9},
		{UnitID: "u", Question: model.QRisk, Value: model.LabelUnknown},
	}
	got := withUnknowns(nil, stored, []model.ChangeUnit{u}, cat, nil)
	for _, l := range got {
		if l.Question == model.QKind {
			t.Errorf("known kind overwritten: %+v", l)
		}
	}
	if len(got) != len(liveLabelIDs(cat, questions.ScopeUnit))-1 {
		t.Errorf("%d unknowns, want one per live unit label id except kind", len(got))
	}
	ret := []model.Label{
		{UnitID: "u", Question: model.QKind, Value: model.LabelUnknown},
		{UnitID: "u", Question: model.QRisk, Value: model.LabelUnknown},
		{UnitID: "u", Question: model.QEffect, Value: "ui", Confidence: 0.8},
	}
	kept := dropShadowedUnknowns(ret, stored, nil)
	if len(kept) != 2 || kept[0].Question != model.QRisk || kept[1].Question != model.QEffect {
		t.Errorf("kept = %+v", kept)
	}
}

func TestApplyOverrides(t *testing.T) {
	ls := []model.Label{
		{UnitID: "u1", Question: model.QKind, Value: "feature", P: 0.6, Confidence: 0.6, Probs: map[string]float64{"feature": 0.6, "refactor": 0.4}},
		{UnitID: "u1", Question: model.QBreaking, Value: model.LabelYes, P: 0.7, Confidence: 0.7},
		{UnitID: "u1", Question: model.QFmt, Line: 4, Value: model.LabelNo, P: 0.1, Confidence: 0.9},
	}
	ovs := []model.Override{
		{UnitID: "u1", Question: model.QKind, Value: "refactor"},
		{UnitID: "u1", Question: model.QBreaking, Value: model.LabelNo},
		{UnitID: "u1", Question: model.QFmt, Value: model.LabelYes}, // line labels untouched
		{UnitID: "u2", Question: model.QRisk, Value: "4"},
	}
	got := applyOverrides(ls, ovs)
	if len(got) != 4 {
		t.Fatalf("labels = %+v", got)
	}
	k := got[0]
	if k.Value != "refactor" || k.P != 1 || k.Confidence != 1 || k.Overridden == nil || *k.Overridden != "refactor" || k.Probs["refactor"] != 1 {
		t.Errorf("kind = %+v", k)
	}
	if b := got[1]; b.Value != model.LabelNo || b.P != 0 || b.Confidence != 1 || b.Overridden == nil {
		t.Errorf("breaking = %+v", b)
	}
	if f := got[2]; f.Value != model.LabelNo || f.Overridden != nil {
		t.Errorf("line label changed: %+v", f)
	}
	if r := got[3]; r.UnitID != "u2" || r.Value != "4" || r.Score != 4 || r.Confidence != 1 {
		t.Errorf("added override label = %+v", r)
	}
}

// Overrides reach aggregate: the override shows up in the aggregate's unit
// kind. Needs the real internal/aggregate (UnitAgg.Kind).
func TestOverridesReachAggregate(t *testing.T) {
	e := newLabelEnv(t)
	p := e.pipeline(nil)
	e.run(t, p)
	u := e.units(t)[0]
	if err := e.st.PutOverride(context.Background(), model.Override{UnitID: u.ID, Question: model.QKind, Value: "refactor", At: labelNow}); err != nil {
		t.Fatal(err)
	}
	e.run(t, p)
	for _, ua := range e.agg(t).Units {
		if ua.UnitID == u.ID {
			if ua.Kind != "refactor" {
				t.Errorf("unit kind = %q, want the override", ua.Kind)
			}
			return
		}
	}
	t.Errorf("unit %s missing from agg.Units", u.ID)
}

func TestApplyAggCopiesPickerFields(t *testing.T) {
	e := newLabelEnv(t)
	p := e.pipeline(nil)
	agg := model.ItemAgg{
		ItemID: e.item.ID, Minutes: 7, Urgency: 3, Why: "asked you", Added: 4, Removed: 1,
		LabelsMissing: true, UnitsEyes: 3,
		Headlines: []model.Headline{{Kind: HeadlineBreaking}, {Kind: "signature"}, {Kind: HeadlineBreaking}},
	}
	f := model.Fetch{HeadSHA: "h1", BaseSHA: "b1"}
	if err := p.applyAgg(context.Background(), e.item, f, agg); err != nil {
		t.Fatal(err)
	}
	it := e.storedItem(t)
	if it.Breaking != 2 || it.NeedsEyes != 3 || !it.LabelsUnavailable || it.Minutes != 7 || it.Additions != 4 || it.Deletions != 1 || it.Why != "asked you" {
		t.Errorf("item = %+v", it)
	}
	agg.LabelsMissing, agg.Headlines, agg.UnitsEyes = false, nil, 0
	if err := p.applyAgg(context.Background(), e.item, f, agg); err != nil {
		t.Fatal(err)
	}
	if it := e.storedItem(t); it.Breaking != 0 || it.NeedsEyes != 0 || it.LabelsUnavailable {
		t.Errorf("fields not cleared: %+v", it)
	}
}

// The labels context carries the item's bullets, the user's role and globs
// and the touched tests.
func TestLabelContext(t *testing.T) {
	e := newLabelEnv(t)
	p := e.pipeline(nil, func(d *Deps) { d.Config.User = config.User{Role: "frontend", Owns: []string{"web/**"}} })
	files := []model.File{{Path: "web/a.ts"}, {Path: "web/a.test.ts"}, {Path: "server/b_test.go"}}
	c := p.labelContext(e.item, files)
	if c.ItemID != e.item.ID || c.Title != e.item.Title || len(c.Bullets) != 2 || c.Bullets[1] != "Add a limit" ||
		c.Role != "frontend" || len(c.Owns) != 1 || strings.Join(c.TestsTouched, ",") != "web/a.test.ts,server/b_test.go" {
		t.Errorf("context = %+v", c)
	}
}
