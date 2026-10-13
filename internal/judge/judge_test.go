package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json")

func mkUnit(id, path, sym, diff string) model.ChangeUnit {
	return model.ChangeUnit{
		ID: id, ItemID: "pr:7", FileID: path, Path: path, Symbol: sym,
		Signature: func() string {
			if sym == "" {
				return ""
			}
			return "func " + sym + "()"
		}(),
		Diff: diff, Kind: model.UnitModified,
	}
}

var (
	unitA = mkUnit("aaa1", "fetch/client.go", "Fetch", "@@ -10,3 +10,4 @@\n func Fetch() {\n-\treturn get()\n+\treturn retry(get, 3)\n }")
	unitB = mkUnit("bbb2", "fetch/client_test.go", "TestFetch", "@@ -5,2 +5,3 @@\n func TestFetch(t *testing.T) {\n+\tassertRetries(t, 3)\n }")
	unitC = mkUnit("ccc3", "README.md", "", "@@ -1,1 +1,2 @@\n # fetch\n+Retries three times.")
)

// respondClient answers with a function of the request; it counts calls.
type respondClient struct {
	mu      sync.Mutex
	calls   int
	respond func(i int, req jev.Request) (jev.Response, error)
}

func (r *respondClient) Evaluate(_ context.Context, req jev.Request) (jev.Response, error) {
	r.mu.Lock()
	i := r.calls
	r.calls++
	r.mu.Unlock()
	return r.respond(i, req)
}

func checkGolden(t *testing.T, name string, req jev.Request) {
	t.Helper()
	got, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s request differs from %s:\n%s", name, path, got)
	}
}

func onlyCall(t *testing.T, f *jev.Fake) jev.Request {
	t.Helper()
	if len(f.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(f.Calls))
	}
	return f.Calls[0]
}

func TestGoldenRequests(t *testing.T) {
	ctx := context.Background()
	cat := questions.Default()

	f := &jev.Fake{}
	if _, err := Pairs(ctx, f, PairInput{Pairs: []Pair{{A: unitB, B: unitA, Cosine: 0.83}}, Catalogue: cat}); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "pair", onlyCall(t, f))

	f = &jev.Fake{}
	_, err := Names(ctx, f, NameInput{
		Title: "Retry fetches", Bullets: []string{"Retry failed fetches three times", "Document the retry"},
		Groups: []GroupText{{ID: "g1", Units: []model.ChangeUnit{unitA, unitB}}}, Catalogue: cat,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "name", onlyCall(t, f))

	f = &jev.Fake{}
	_, err = SeparatePR(ctx, f, SplitInput{
		ItemID: "pr:7", Title: "Retry fetches",
		Files: []FileText{{Path: "README.md", Units: []model.ChangeUnit{unitC}}},
		Groups: []GroupSummary{
			{ID: "g1", Name: "Retry failed fetches three times", Paths: []string{"fetch/client_test.go", "fetch/client.go"}, Units: 2},
			{ID: "g2", Name: "README", Paths: []string{"README.md"}, Units: 1},
		},
		Catalogue: cat,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "split", onlyCall(t, f))
}

func TestPairOrderIndependent(t *testing.T) {
	ctx := context.Background()
	var reqs []jev.Request
	var subjects []string
	for _, p := range []Pair{{A: unitA, B: unitB, Cosine: 0.9}, {A: unitB, B: unitA, Cosine: 0.7}} {
		f := &jev.Fake{}
		res, err := Pairs(ctx, f, PairInput{Pairs: []Pair{p}, Catalogue: questions.Default()})
		if err != nil {
			t.Fatal(err)
		}
		reqs = append(reqs, onlyCall(t, f))
		subjects = append(subjects, res.Labels[0].UnitID)
	}
	k0, _ := jev.RequestKey(reqs[0])
	k1, _ := jev.RequestKey(reqs[1])
	if k0 != k1 || !reflect.DeepEqual(reqs[0], reqs[1]) {
		t.Errorf("swapped pair changes the request: %s vs %s", k0, k1)
	}
	if want := model.PairSubject(unitA.ID, unitB.ID); subjects[0] != want || subjects[1] != want {
		t.Errorf("subjects %v, want %s", subjects, want)
	}
	st := reqs[0].State.(pairState)
	if st.A.Path != unitA.Path || st.B.Path != unitB.Path {
		t.Errorf("a/b not sorted by id: %+v", st)
	}
}

func pf(p float64) *float64 { return &p }

func TestDecodeChoice(t *testing.T) {
	tests := []struct {
		name    string
		answers map[string]jev.Answer
		model   string
		want    model.Label
	}{
		{
			name: "valid choice",
			answers: map[string]jev.Answer{"relation": {Type: jev.Choice, Choice: model.RelCallerUpdate,
				Probabilities: map[string]float64{model.RelCallerUpdate: 0.7, model.RelSameIntent: 0.2, model.RelUnrelated: 0.1, "bogus": 0.5}}},
			want: model.Label{Value: model.RelCallerUpdate, P: 0.7, Confidence: 0.7, Model: jev.Model,
				Probs: map[string]float64{model.RelCallerUpdate: 0.7, model.RelSameIntent: 0.2, model.RelUnrelated: 0.1}},
		},
		{
			name:    "untyped choice keeps response model",
			answers: map[string]jev.Answer{"relation": {Choice: model.RelTestFor, Probabilities: map[string]float64{model.RelTestFor: 0.9}}},
			model:   "typesafe-ai/jev-2",
			want: model.Label{Value: model.RelTestFor, P: 0.9, Confidence: 0.9, Model: "typesafe-ai/jev-2",
				Probs: map[string]float64{model.RelTestFor: 0.9}},
		},
		{
			name:    "out of catalogue",
			answers: map[string]jev.Answer{"relation": {Type: jev.Choice, Choice: "same mechanical pattern", Probabilities: map[string]float64{"same mechanical pattern": 1}}},
			want:    model.Label{Value: model.LabelUnknown},
		},
		{
			name:    "missing",
			answers: map[string]jev.Answer{"other": {Type: jev.Choice, Choice: model.RelTestFor}},
			want:    model.Label{Value: model.LabelUnknown},
		},
		{
			name:    "mistyped",
			answers: map[string]jev.Answer{"relation": {Type: jev.Boolean, Probability: pf(0.9)}},
			want:    model.Label{Value: model.LabelUnknown},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &respondClient{respond: func(int, jev.Request) (jev.Response, error) {
				r := jev.Response{Model: tc.model, Answers: tc.answers}
				r.ProviderMetadata.Gateway.Cost = "0.0005"
				return r, nil
			}}
			res, err := Pairs(context.Background(), c, PairInput{Pairs: []Pair{{A: unitA, B: unitB}}, Catalogue: questions.Default()})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Labels) != 1 || res.Calls != 1 || res.Answered != 1 || res.Unavailable || res.Cost != 0.0005 {
				t.Fatalf("result %+v", res)
			}
			want := tc.want
			want.UnitID, want.Question = model.PairSubject(unitA.ID, unitB.ID), model.QRelation
			if !reflect.DeepEqual(res.Labels[0], want) {
				t.Errorf("label\n got %+v\nwant %+v", res.Labels[0], want)
			}
		})
	}
}

func TestDecodeBoolean(t *testing.T) {
	groups := []GroupSummary{{ID: "g1", Name: "x", Paths: []string{"a.go"}, Units: 1}}
	sctx := SplitCtx(groups)
	tests := []struct {
		name string
		ans  jev.Answer
		want model.Label
	}{
		{"yes", jev.Answer{Type: jev.Boolean, Probability: pf(0.7)},
			model.Label{Value: model.LabelYes, P: 0.7, Confidence: 0.7, Model: jev.Model}},
		{"no", jev.Answer{Type: jev.Boolean, Probability: pf(0.2)},
			model.Label{Value: model.LabelNo, P: 0.2, Confidence: 0.8, Model: jev.Model}},
		{"no probability", jev.Answer{Type: jev.Boolean}, model.Label{Value: model.LabelUnknown}},
		{"out of range", jev.Answer{Type: jev.Boolean, Probability: pf(1.5)}, model.Label{Value: model.LabelUnknown}},
		{"mistyped", jev.Answer{Type: jev.Choice, Choice: "yes"}, model.Label{Value: model.LabelUnknown}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &jev.Fake{Answer: func(string, jev.Question, any) jev.Answer { return tc.ans }}
			res, err := SeparatePR(context.Background(), f, SplitInput{
				ItemID: "pr:7", Files: []FileText{{Path: "a.go", Units: []model.ChangeUnit{unitA}}},
				Groups: groups, Catalogue: questions.Default(),
			})
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			want.UnitID, want.Question, want.Ctx = model.FileSubject("pr:7", "a.go"), model.QSeparatePR, FileCtx(sctx, []model.ChangeUnit{unitA})
			if len(res.Labels) != 1 || !reflect.DeepEqual(res.Labels[0], want) {
				t.Errorf("labels\n got %+v\nwant %+v", res.Labels, want)
			}
		})
	}
}

// manyPairs returns n distinct pairs.
func manyPairs(n int) []Pair {
	var out []Pair
	for i := range n {
		a := mkUnit(fmt.Sprintf("a%02d", i), fmt.Sprintf("a%02d.go", i), "", "+x")
		b := mkUnit(fmt.Sprintf("b%02d", i), fmt.Sprintf("b%02d.go", i), "", "+y")
		out = append(out, Pair{A: b, B: a})
	}
	return out
}

func TestUnavailable(t *testing.T) {
	ctx := context.Background()
	unavailable := fmt.Errorf("%w: 503", jev.ErrUnavailable)

	f := &jev.Fake{Err: unavailable}
	res, err := Pairs(ctx, f, PairInput{Pairs: manyPairs(2), Catalogue: questions.Default(), Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 2 || !res.Unavailable || res.Tripped || res.Failed != 2 || res.Calls != 2 || len(res.Labels) != 2 {
		t.Fatalf("result %+v, calls %d", res, len(f.Calls))
	}
	for _, l := range res.Labels {
		if !model.IsUnknown(l) || l.Confidence != 0 || l.Model != "" || l.Probs != nil {
			t.Errorf("label %+v, want unknown", l)
		}
	}

	// The breaker trips after 3 consecutive failures.
	f = &jev.Fake{Err: unavailable}
	res, err = Pairs(ctx, f, PairInput{Pairs: manyPairs(6), Catalogue: questions.Default(), Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != breakerAfter || res.Calls != breakerAfter || !res.Tripped || res.Failed != 6 || len(res.Labels) != 6 {
		t.Fatalf("result %+v, calls %d", res, len(f.Calls))
	}

	// A success resets the count.
	pattern := []bool{false, false, true, false, false, true, false, false}
	c := &respondClient{respond: func(i int, req jev.Request) (jev.Response, error) {
		if !pattern[i] {
			return jev.Response{}, unavailable
		}
		return (&jev.Fake{}).Evaluate(ctx, req)
	}}
	res, err = Pairs(ctx, c, PairInput{Pairs: manyPairs(len(pattern)), Catalogue: questions.Default(), Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if c.calls != len(pattern) || res.Tripped || res.Answered != 2 || res.Failed != 6 {
		t.Fatalf("result %+v, calls %d", res, c.calls)
	}
}

func TestCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &respondClient{respond: func(i int, req jev.Request) (jev.Response, error) {
		if i == 1 {
			cancel()
			return jev.Response{}, context.Canceled
		}
		return (&jev.Fake{}).Evaluate(ctx, req)
	}}
	res, err := Pairs(ctx, c, PairInput{Pairs: manyPairs(5), Catalogue: questions.Default(), Concurrency: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if c.calls != 2 || len(res.Labels) != 1 || model.IsUnknown(res.Labels[0]) {
		t.Fatalf("calls %d, labels %+v", c.calls, res.Labels)
	}

	f := &jev.Fake{}
	if _, err := Pairs(ctx, f, PairInput{Pairs: manyPairs(2), Catalogue: questions.Default()}); !errors.Is(err, context.Canceled) || len(f.Calls) != 0 {
		t.Fatalf("pre-cancelled: err %v, calls %d", err, len(f.Calls))
	}
}

func TestFatalErrorStopsCalls(t *testing.T) {
	boom := errors.New("400 bad request")
	c := &respondClient{respond: func(i int, req jev.Request) (jev.Response, error) {
		if i == 1 {
			return jev.Response{}, boom
		}
		return (&jev.Fake{}).Evaluate(context.Background(), req)
	}}
	res, err := Pairs(context.Background(), c, PairInput{Pairs: manyPairs(5), Catalogue: questions.Default(), Concurrency: 1})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "pair:") {
		t.Fatalf("err = %v", err)
	}
	if c.calls != 2 || len(res.Labels) != 1 || res.Calls != 2 || res.Answered != 1 {
		t.Fatalf("calls %d, result %+v", c.calls, res)
	}
}

func TestConcurrencyDeterministic(t *testing.T) {
	ctx := context.Background()
	answer := func(_ string, q jev.Question, state any) jev.Answer {
		// Vary the answer by subject so ordering mistakes show.
		b, _ := json.Marshal(state)
		if len(b)%2 == 0 {
			return jev.Answer{Type: jev.Choice, Choice: model.RelUnrelated, Probabilities: map[string]float64{model.RelUnrelated: 0.8}}
		}
		return jev.DefaultAnswer("", q, state)
	}
	var results []Result
	for _, n := range []int{1, 8} {
		res, err := Pairs(ctx, &jev.Fake{Answer: answer}, PairInput{Pairs: manyPairs(20), Catalogue: questions.Default(), Concurrency: n})
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, res)
	}
	if !reflect.DeepEqual(results[0], results[1]) {
		t.Errorf("concurrency changes the result")
	}
	for i := 1; i < len(results[0].Labels); i++ {
		if results[0].Labels[i-1].UnitID >= results[0].Labels[i].UnitID {
			t.Fatalf("labels not sorted by subject")
		}
	}
}

func bigDiff(lines int) string {
	var b strings.Builder
	for i := range lines {
		fmt.Fprintf(&b, "+\tline %05d of a long generated diff ............ x\n", i)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func TestTruncation(t *testing.T) {
	big := mkUnit("zzz9", "gen/big.go", "", bigDiff(4000))
	if len(big.Diff) < 200_000 {
		t.Fatalf("diff only %d bytes", len(big.Diff))
	}
	f := &jev.Fake{}
	if _, err := Pairs(context.Background(), f, PairInput{Pairs: []Pair{{A: big, B: unitA}}, Catalogue: questions.Default()}); err != nil {
		t.Fatal(err)
	}
	req := onlyCall(t, f)
	st := req.State.(pairState)
	sb, _ := json.Marshal(st)
	rb, _ := json.Marshal(req)
	q, _ := json.Marshal(req.Questions["relation"])
	if got := jev.EstimateTokensLen(len(sb)) + jev.EstimateTokens(`"relation":`+string(q)); got > jev.MaxStatePlusQuestion {
		t.Errorf("state+question ~%d tokens", got)
	}
	if got := jev.EstimateTokensLen(len(rb)); got > jev.MaxRequestTokens {
		t.Errorf("request ~%d tokens", got)
	}
	if !strings.Contains(st.B.Diff, "diff lines elided") || st.A.Diff != unitA.Diff {
		t.Errorf("big diff not cut or small diff changed")
	}
	if !strings.HasPrefix(st.B.Diff, "+\tline 00000") || !strings.HasSuffix(st.B.Diff, "line 03999 of a long generated diff ............ x") {
		t.Errorf("head/tail not kept")
	}

	// Several units: the cap cuts only the long diff.
	f = &jev.Fake{}
	_, err := Names(context.Background(), f, NameInput{Bullets: []string{"a", "b"},
		Groups: []GroupText{{ID: "g", Units: []model.ChangeUnit{unitA, big}}}, Catalogue: questions.Default()})
	if err != nil {
		t.Fatal(err)
	}
	ns := onlyCall(t, f).State.(nameState)
	if ns.Group.Units[0].Diff != unitA.Diff || !strings.Contains(ns.Group.Units[1].Diff, "elided") {
		t.Errorf("multi-unit cut wrong")
	}

	// A question that alone exceeds the limit is an error naming the subject.
	cat := questions.Default()
	for i, qq := range cat.Questions {
		if qq.Scope == questions.ScopePair && qq.ID == model.QRelation {
			cat.Questions[i].Prompt = strings.Repeat("why ", 50_000)
		}
	}
	_, err = Pairs(context.Background(), &jev.Fake{}, PairInput{Pairs: []Pair{{A: unitA, B: unitB}}, Catalogue: cat})
	if err == nil || !strings.Contains(err.Error(), model.PairSubject(unitA.ID, unitB.ID)) {
		t.Fatalf("err = %v, want an error naming the pair", err)
	}
}

func TestLongLineCapped(t *testing.T) {
	u := mkUnit("u", "min.js", "", "+"+strings.Repeat("é", 1500)+"\n+ok")
	st := prepare(u)[0].state(-1)
	first, rest, _ := strings.Cut(st.Diff, "\n")
	if len(first) > maxLineBytes+len("…") || !strings.HasSuffix(first, "…") || !utf8.ValidString(first) || rest != "+ok" {
		t.Errorf("capped line %d bytes, valid %v", len(first), utf8.ValidString(first))
	}
}

func TestFitWithTinyLimits(t *testing.T) {
	big := mkUnit("zzz9", "gen/big.go", "", bigDiff(300))
	f := &jev.Fake{}
	if _, err := pairs(context.Background(), f, PairInput{Pairs: []Pair{{A: big, B: unitA}}, Catalogue: questions.Default()},
		limits{statePlusQuestion: 2000, request: 2100}); err != nil {
		t.Fatal(err)
	}
	rb, _ := json.Marshal(onlyCall(t, f))
	if jev.EstimateTokensLen(len(rb)) > 2100 {
		t.Errorf("request over the tiny limit")
	}
}

func TestNamesNoBullets(t *testing.T) {
	f := &jev.Fake{}
	res, err := Names(context.Background(), f, NameInput{Title: "t",
		Groups:    []GroupText{{ID: "g2", Units: []model.ChangeUnit{unitA}}, {ID: "g1", Units: []model.ChangeUnit{unitB}}},
		Catalogue: questions.Default()})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 0 || res.Calls != 0 || len(res.Labels) != 2 {
		t.Fatalf("calls %d, result %+v", len(f.Calls), res)
	}
	want := model.Label{UnitID: model.GroupSubject("g1"), Question: model.QNameBullet, Value: model.BulletNone,
		P: 1, Confidence: 1, Probs: map[string]float64{model.BulletNone: 1}, Model: ModelNoBullets, Ctx: NameCtx("t", nil)}
	if !reflect.DeepEqual(res.Labels[0], want) {
		t.Errorf("label\n got %+v\nwant %+v", res.Labels[0], want)
	}
}

func TestNamesLabels(t *testing.T) {
	f := &jev.Fake{} // picks the first option in sorted order: b1
	bullets := []string{"Retry", "Docs"}
	res, err := Names(context.Background(), f, NameInput{Title: "t", Bullets: bullets,
		Groups: []GroupText{{ID: "g1", Units: []model.ChangeUnit{unitA}}}, Catalogue: questions.Default()})
	if err != nil {
		t.Fatal(err)
	}
	l := res.Labels[0]
	if l.UnitID != model.GroupSubject("g1") || l.Value != "b1" || l.Ctx != NameCtx("t", bullets) || l.P != 0.9 {
		t.Errorf("label %+v", l)
	}
}

func TestCtxHashes(t *testing.T) {
	base := NameCtx("t", []string{"a", "b"})
	for _, other := range []string{NameCtx("u", []string{"a", "b"}), NameCtx("t", []string{"a"}), NameCtx("t", []string{"a", "c"}), NameCtx("t", []string{"b", "a"})} {
		if other == base {
			t.Errorf("NameCtx collision")
		}
	}
	if len(base) != 16 || NameCtx("t", []string{"a", "b"}) != base {
		t.Errorf("NameCtx not stable 16 chars: %q", base)
	}
	gs := []GroupSummary{{ID: "1", Name: "n", Paths: []string{"b.go", "a.go"}, Units: 2}}
	sbase := SplitCtx(gs)
	if SplitCtx([]GroupSummary{{ID: "other", Name: "n", Paths: []string{"a.go", "b.go"}, Units: 2}}) != sbase {
		t.Errorf("SplitCtx depends on id or path order")
	}
	for _, other := range [][]GroupSummary{
		{{Name: "m", Paths: []string{"a.go", "b.go"}, Units: 2}},
		{{Name: "n", Paths: []string{"a.go"}, Units: 2}},
		{{Name: "n", Paths: []string{"a.go", "b.go"}, Units: 3}},
		nil,
	} {
		if SplitCtx(other) == sbase {
			t.Errorf("SplitCtx collision for %+v", other)
		}
	}
}

const deferredTOML = `
[[question]]
scope = "pair"
id = "relation"
type = "C"
prompt = "How are these related?"
options = ["same intent", "unrelated"]
feeds = ["links"]
deferred = "off"

[[question]]
scope = "group"
id = "name_bullet"
type = "C"
prompt = "Which bullet?"
dynamic = "pr_bullets"
feeds = ["group_name"]
deferred = "off"

[[question]]
scope = "file"
id = "separate_pr"
type = "N"
prompt = "Own PR?"
feeds = ["split_suggestion"]
deferred = "off"
`

func TestDeferredAsksNothing(t *testing.T) {
	cat, err := questions.Load([]byte(deferredTOML), "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := &jev.Fake{}
	var results []Result
	r, err := Pairs(ctx, f, PairInput{Pairs: manyPairs(2), Catalogue: cat})
	results = append(results, r)
	if err != nil {
		t.Fatal(err)
	}
	r, err = Names(ctx, f, NameInput{Groups: []GroupText{{ID: "g"}}, Catalogue: cat})
	results = append(results, r)
	if err != nil {
		t.Fatal(err)
	}
	r, err = SeparatePR(ctx, f, SplitInput{ItemID: "pr:1", Files: []FileText{{Path: "a.go"}}, Catalogue: cat})
	results = append(results, r)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range results {
		if !reflect.DeepEqual(r, Result{}) {
			t.Errorf("result %d = %+v, want empty", i, r)
		}
	}
	if len(f.Calls) != 0 {
		t.Errorf("calls = %d", len(f.Calls))
	}
}

func TestDuplicateSubjectsAskedOnce(t *testing.T) {
	f := &jev.Fake{}
	res, err := Pairs(context.Background(), f, PairInput{Pairs: []Pair{{A: unitA, B: unitB}, {A: unitB, B: unitA}}, Catalogue: questions.Default()})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || len(res.Labels) != 1 {
		t.Errorf("calls %d labels %d", len(f.Calls), len(res.Labels))
	}
}
