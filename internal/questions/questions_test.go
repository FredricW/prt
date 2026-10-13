package questions

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
)

// specRows is the spec's catalogue table: scope, id, type and, for N ×k
// rows, the number of sub-questions.
var specRows = []struct {
	scope Scope
	id    string
	typ   Type
	subs  int
}{
	{ScopeLine, model.QFmt, Noul, 0},
	{ScopeLine, model.QValueTweak, Noul, 0},
	{ScopeLine, model.QLeftover, Noul, 0},
	{ScopeLine, model.QSwallow, Noul, 0},
	{ScopeLine, model.QPIIOrSecret, Noul, 0},
	{ScopeLine, model.QSuspicious, Score, 0},
	{ScopeUnit, model.QKind, Choice, 0},
	{ScopeUnit, model.QBehaviour, Noul, 0},
	{ScopeUnit, model.QEffect, Choice, 0},
	{ScopeUnit, model.QDescBullet, Choice, 0},
	{ScopeUnit, model.QBreaking, Noul, 0},
	{ScopeUnit, model.QContract, Choice, 0},
	{ScopeUnit, model.QReversible, Noul, 0},
	{ScopeUnit, model.QRiskTags, Noul, 8},
	{ScopeUnit, model.QFailMode, Choice, 0},
	{ScopeUnit, model.QRisk, Score, 0},
	{ScopeUnit, model.QCovered, Noul, 0},
	{ScopeUnit, model.QTestWeakened, Noul, 0},
	{ScopeUnit, model.QFollowsPattern, Noul, 0},
	{ScopeUnit, model.QDuplicatesHelper, Noul, 0},
	{ScopeUnit, model.QInterface, Noul, 0},
	{ScopeUnit, model.QRelevance, Score, 0},
	{ScopeUnit, model.QDepth, Choice, 0},
	{ScopeUnit, model.QNeedsContext, Noul, 4},
	{ScopeUnit, model.QReasoning, Score, 0},
	{ScopeFile, model.QRole, Choice, 0},
	{ScopeFile, model.QSeparatePR, Noul, 0},
	{ScopeGroup, model.QNameBullet, Choice, 0},
	{ScopePair, model.QRelation, Choice, 0},
	{ScopePair, model.QSemanticConflict, Choice, 0},
	{ScopeItem, model.QShape, Choice, 0},
	{ScopeItem, model.QDescSufficient, Noul, 0},
	{ScopeItem, model.QFlagged, Noul, 0},
	{ScopeItem, model.QRollback, Choice, 0},
	{ScopeItem, model.QNeedsOtherReviewer, Choice, 0},
	{ScopeItem, model.QCommentAddressed, Noul, 0},
	{ScopeBranch, model.QState, Choice, 0},
}

func find(c Catalogue, s Scope, id string) (Question, bool) {
	for _, q := range c.ByScope(s) {
		if q.ID == id {
			return q, true
		}
	}
	return Question{}, false
}

func TestDefaultHasEverySpecRow(t *testing.T) {
	c := Default()
	if len(c.Questions) != len(specRows) {
		t.Errorf("catalogue has %d questions, spec table has %d", len(c.Questions), len(specRows))
	}
	for _, r := range specRows {
		t.Run(string(r.scope)+"/"+r.id, func(t *testing.T) {
			q, ok := find(c, r.scope, r.id)
			if !ok {
				t.Fatal("missing")
			}
			if q.Type != r.typ {
				t.Errorf("type = %s, want %s", q.Type, r.typ)
			}
			if r.subs > 0 && len(q.Options) != r.subs {
				t.Errorf("sub-questions = %d, want %d", len(q.Options), r.subs)
			}
			if r.typ == Score && len(q.Options) != 5 {
				t.Errorf("score rungs = %d, want 5", len(q.Options))
			}
		})
	}
}

func TestDefaultOptionsMatchSpec(t *testing.T) {
	c := Default()
	tests := []struct {
		scope Scope
		id    string
		want  []string
	}{
		{ScopeUnit, model.QKind, []string{"feature", "bugfix", "refactor", "perf", "test", "config", "docs", "dependency", "generated", "formatting"}},
		{ScopeUnit, model.QEffect, []string{"none", "ui", "api response", "stored data", "performance"}},
		{ScopeUnit, model.QContract, []string{"none", "signature", "api shape", "schema/migration", "env/flag", "event format"}},
		{ScopeUnit, model.QFailMode, []string{"crash", "wrong data", "security", "slowdown", "cosmetic"}},
		{ScopeUnit, model.QDepth, []string{model.DepthSkip, model.DepthSkim, model.DepthRead, model.DepthRun}},
		{ScopeFile, model.QRole, []string{"core", "glue", "ui", "test", "config", "generated"}},
		{ScopePair, model.QRelation, []string{"same intent", "caller update", "test for", "same pattern", "unrelated"}},
		{ScopePair, model.QSemanticConflict, []string{"none", "same", "contradictory", "order-dependent"}},
		{ScopeItem, model.QShape, []string{"single-purpose", "mixed", "sweeping refactor", "WIP"}},
		{ScopeItem, model.QRollback, []string{"trivial", "revert", "needs migration", "one-way"}},
		{ScopeItem, model.QNeedsOtherReviewer, []string{"none", "security", "backend", "data"}},
		{ScopeBranch, model.QState, []string{"WIP", "ready for PR", "spike", "abandoned", "superseded"}},
	}
	for _, tt := range tests {
		q, _ := find(c, tt.scope, tt.id)
		if !slices.Equal(q.Options, tt.want) {
			t.Errorf("%s options = %q, want %q", tt.id, q.Options, tt.want)
		}
	}
	if q, _ := find(c, ScopeUnit, model.QContract); q.Options[0] != model.ContractNone {
		t.Errorf("contract none = %q", q.Options[0])
	}
}

func TestDefaultSubIDs(t *testing.T) {
	c := Default()
	for _, tt := range []struct {
		id   string
		want []string
	}{
		{model.QRiskTags, model.RiskTags},
		{model.QNeedsContext, model.NeedsContextKinds},
	} {
		q, _ := find(c, ScopeUnit, tt.id)
		ids, _ := q.Subs()
		if !slices.Equal(ids, tt.want) {
			t.Errorf("%s subs = %q, want %q", tt.id, ids, tt.want)
		}
	}
}

func TestDefaultDynamicBullets(t *testing.T) {
	c := Default()
	for _, r := range []struct {
		s  Scope
		id string
	}{{ScopeUnit, model.QDescBullet}, {ScopeGroup, model.QNameBullet}} {
		q, _ := find(c, r.s, r.id)
		if q.Dynamic != DynamicBullets {
			t.Errorf("%s dynamic = %q", r.id, q.Dynamic)
		}
	}
}

func TestLiveSetMatchesDecisions(t *testing.T) {
	c := Default()
	want := map[Scope][]string{
		ScopeLine: {"fmt", "value_tweak", "leftover", "swallow", "pii_or_secret", "suspicious"},
		ScopeUnit: {"kind", "behaviour", "effect", "desc_bullet", "breaking", "contract", "reversible",
			"risk_tags", "fail_mode", "risk", "covered", "test_weakened", "relevance", "depth"},
	}
	for _, s := range Scopes {
		var got []string
		for _, q := range c.Live(s) {
			got = append(got, q.ID)
		}
		if !slices.Equal(got, want[s]) {
			t.Errorf("live %s = %q, want %q", s, got, want[s])
		}
	}
	for _, q := range c.Questions {
		if q.Deferred != "" && !strings.HasPrefix(q.Deferred, "M") {
			t.Errorf("%s deferred = %q", q.ID, q.Deferred)
		}
	}
}

func TestEveryIDIsAModelConstant(t *testing.T) {
	consts := map[string]bool{}
	for _, r := range specRows {
		consts[r.id] = true
	}
	for _, q := range Default().Questions {
		if !consts[q.ID] {
			t.Errorf("%s/%s is not a model.Q* constant", q.Scope, q.ID)
		}
	}
}

const validRow = `
[[question]]
scope = "unit"
id = "x"
type = "N"
prompt = "Is it?"
feeds = ["filters"]
`

func row(fields string) []byte {
	return []byte("[[question]]\nprompt = \"Is it?\"\n" + fields + "\n")
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"feeds empty", row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="N"` + "\nfeeds=[]"), "feeds nothing"},
		{"feeds missing", row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="N"`), "feeds nothing"},
		{"bad type", row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="B"` + "\nfeeds=[\"a\"]"), "bad type"},
		{"bad scope", row(`scope="repo"` + "\n" + `id="x"` + "\n" + `type="N"` + "\nfeeds=[\"a\"]"), "unknown scope"},
		{"duplicate id", []byte(validRow + validRow), "duplicate id"},
		{"choice one option", row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="C"` + "\noptions=[\"a\"]\nfeeds=[\"a\"]"), "two options"},
		{"score one rung", row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="S"` + "\noptions=[\"a\"]\nfeeds=[\"a\"]"), "two options"},
		{"bad sub-id", row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="N"` + "\noptions=[\"Auth Z: d\", \"b: d\"]\nfeeds=[\"a\"]"), "sub-id"},
		{"duplicate sub-id", row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="N"` + "\noptions=[\"a: d\", \"a: e\"]\nfeeds=[\"a\"]"), "duplicate sub-id"},
		{"sub without description", row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="N"` + "\noptions=[\"a\", \"b: e\"]\nfeeds=[\"a\"]"), "description"},
		{"bad id", row(`scope="unit"` + "\n" + `id="Risk Tags"` + "\n" + `type="N"` + "\nfeeds=[\"a\"]"), "id"},
		{"id with separator", row(`scope="unit"` + "\n" + `id="a__b"` + "\n" + `type="N"` + "\nfeeds=[\"a\"]"), "id"},
		{"no id", row(`scope="unit"` + "\n" + `type="N"` + "\nfeeds=[\"a\"]"), "without id"},
		{"unknown dynamic", row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="C"` + "\ndynamic=\"files\"\nfeeds=[\"a\"]"), "dynamic"},
		{"dynamic on boolean", row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="N"` + "\ndynamic=\"pr_bullets\"\nfeeds=[\"a\"]"), "dynamic"},
		{"unknown input", row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="N"` + "\ninputs=[\"vibes\"]\nfeeds=[\"a\"]"), "unknown input"},
		{"unknown key", []byte(validRow + "defered = \"M2\"\n"), "unknown key"},
		{"choice criteria mismatch", row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="C"` +
			"\noptions=[\"a\", \"b\"]\nfeeds=[\"a\"]\n[question.criteria]\nc = \"?\""), "unknown option"},
		{"syntax", []byte("[[question]\n"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.data, "t")
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestParseAccepts(t *testing.T) {
	data := row(`scope="unit"` + "\n" + `id="x"` + "\n" + `type="C"` + "\ndynamic=\"pr_bullets\"\nfeeds=[\"a\"]")
	c, err := Parse(data, "t")
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != "t" || len(c.Live(ScopeUnit)) != 1 {
		t.Fatalf("catalogue = %+v", c)
	}
	if def, err := Parse(nil, "x"); err != nil || len(def.Questions) != len(specRows) {
		t.Fatalf("nil data: %v, %d questions", err, len(def.Questions))
	}
}

func TestExpand(t *testing.T) {
	c := Default()
	q := func(s Scope, id string) Question { x, _ := find(c, s, id); return x }

	t.Run("choice", func(t *testing.T) {
		ws := q(ScopeUnit, model.QKind).Expand(Dynamic{})
		if len(ws) != 1 || ws[0].Name != "kind" || ws[0].Label != "kind" || ws[0].Q.Type != jev.Choice {
			t.Fatalf("got %+v", ws)
		}
		crit := ws[0].Q.Criteria.(map[string]string)
		if len(crit) != 10 || crit["refactor"] == "" || crit["refactor"] == "refactor" {
			t.Fatalf("criteria = %v", crit)
		}
	})
	t.Run("score", func(t *testing.T) {
		ws := q(ScopeUnit, model.QRisk).Expand(Dynamic{})
		rungs := ws[0].Q.Criteria.([]string)
		if ws[0].Q.Type != jev.Score || len(rungs) != 5 || !strings.HasPrefix(rungs[0], "1:") || !strings.HasPrefix(rungs[4], "5:") {
			t.Fatalf("got %+v", ws[0])
		}
	})
	t.Run("boolean", func(t *testing.T) {
		ws := q(ScopeUnit, model.QBehaviour).Expand(Dynamic{})
		if len(ws) != 1 || ws[0].Q.Type != jev.Boolean || ws[0].Q.Criteria != nil {
			t.Fatalf("got %+v", ws)
		}
	})
	t.Run("multi", func(t *testing.T) {
		ws := q(ScopeUnit, model.QRiskTags).Expand(Dynamic{})
		if len(ws) != len(model.RiskTags) {
			t.Fatalf("got %d", len(ws))
		}
		for i, w := range ws {
			sub := model.RiskTags[i]
			if w.Name != "risk_tags__"+sub || w.Label != model.SubQuestion("risk_tags", sub) || w.Q.Type != jev.Boolean {
				t.Errorf("sub %d = %+v", i, w)
			}
			if !strings.Contains(w.Q.Instructions, sub+": ") {
				t.Errorf("instructions %q lack the sub description", w.Q.Instructions)
			}
		}
	})
	t.Run("dynamic bullets", func(t *testing.T) {
		ws := q(ScopeUnit, model.QDescBullet).Expand(Dynamic{Bullets: []string{"add retry", "fix tz"}})
		w := ws[0]
		if !slices.Equal(w.Options, []string{"b1", "b2", "none"}) || !w.Answerable() {
			t.Fatalf("options = %q", w.Options)
		}
		crit := w.Q.Criteria.(map[string]string)
		want := map[string]string{"b1": "add retry", "b2": "fix tz", "none": "implements none of the listed bullets"}
		if fmt.Sprint(crit) != fmt.Sprint(want) {
			t.Fatalf("criteria = %v", crit)
		}
		if empty := q(ScopeUnit, model.QDescBullet).Expand(Dynamic{})[0]; empty.Answerable() {
			t.Fatalf("no bullets should not be sent: %+v", empty)
		}
	})
	t.Run("label ids", func(t *testing.T) {
		if got := q(ScopeUnit, model.QNeedsContext).LabelIDs(); !slices.Equal(got, []string{
			"needs_context.callers", "needs_context.types", "needs_context.tests", "needs_context.config"}) {
			t.Errorf("needs_context = %q", got)
		}
		if got := q(ScopeUnit, model.QKind).LabelIDs(); !slices.Equal(got, []string{"kind"}) {
			t.Errorf("kind = %q", got)
		}
	})
	t.Run("wire compat", func(t *testing.T) {
		for _, x := range c.Questions {
			w := x.Wire()
			want := map[Type]jev.QuestionType{Noul: jev.Boolean, Choice: jev.Choice, Score: jev.Score}[x.Type]
			if w.Type != want {
				t.Errorf("%s: wire type %s", x.ID, w.Type)
			}
		}
	})
}
