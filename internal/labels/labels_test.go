package labels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
)

func ptr(f float64) *float64 { return &f }

// mkUnit builds a modified unit whose diff alternates context and added
// lines: one removed line, then `added` added lines with a context line after
// every fifth. The new side starts at line 100.
func mkUnit(id, path string, added int) model.ChangeUnit {
	lines := []string{"-old()"}
	for i := range added {
		lines = append(lines, fmt.Sprintf("+added(%d)", i))
		if i%5 == 4 {
			lines = append(lines, " ctx()")
		}
	}
	return model.ChangeUnit{
		ID: id, ItemID: "pr:1", Path: path, Symbol: "fn", Signature: "func fn()",
		Range: model.Range{Old: model.Span{Start: 100, Count: 1}, New: model.Span{Start: 100, Count: added}},
		Diff:  strings.Join(lines, "\n"), Kind: model.UnitModified, Adds: added, Dels: 1,
	}
}

// wantLines is the new-side line numbers mkUnit's added lines get.
func wantLines(added int) []int {
	var out []int
	n := 100
	for i := range added {
		out = append(out, n)
		n++
		if i%5 == 4 {
			n++
		}
	}
	return out
}

func baseInput(units ...model.ChangeUnit) Input {
	return Input{
		Ctx: Context{
			ItemID: "pr:1", Title: "Add retry", Bullets: []string{"add retry to fetch", "fix tz bug"},
			Role: "backend", Owns: []string{"api/**"}, TestsTouched: []string{"z_test.go", "a_test.go"},
		},
		Units:     units,
		Catalogue: questions.Default(),
	}
}

// answering returns a Fake answer func with fixed answers by wire name.
func answering(m map[string]jev.Answer) func(string, jev.Question, any) jev.Answer {
	return func(name string, q jev.Question, st any) jev.Answer {
		if a, ok := m[name]; ok {
			return a
		}
		return jev.DefaultAnswer(name, q, st)
	}
}

func byKey(ls []model.Label) map[string]model.Label {
	out := map[string]model.Label{}
	for _, l := range ls {
		out[fmt.Sprintf("%s|%s|%d", l.UnitID, l.Question, l.Line)] = l
	}
	return out
}

func get(t *testing.T, ls []model.Label, unit, q string, line int) model.Label {
	t.Helper()
	l, ok := byKey(ls)[fmt.Sprintf("%s|%s|%d", unit, q, line)]
	if !ok {
		t.Fatalf("no label %s %s line %d", unit, q, line)
	}
	return l
}

func TestAnswerMapping(t *testing.T) {
	fake := &jev.Fake{Answer: answering(map[string]jev.Answer{
		"behaviour": {Type: jev.Boolean, Probability: ptr(0.8)},
		"covered":   {Type: jev.Boolean, Probability: ptr(0.3)},
		"kind": {Type: jev.Choice, Choice: "refactor",
			Probabilities: map[string]float64{"refactor": 0.7, "feature": 0.2, "bugfix": 0.1}},
		"risk": {Type: jev.Score, Score: ptr(3.2),
			Probabilities: map[string]float64{"0": 0.1, "1": 0.1, "2": 0.1, "3": 0.6, "4": 0.1}},
		"risk_tags__auth": {Type: jev.Boolean, Probability: ptr(0.7)},
		"desc_bullet": {Type: jev.Choice, Choice: "b2",
			Probabilities: map[string]float64{"b1": 0.1, "b2": 0.85, "none": 0.05}},
		"fmt__l01":        {Type: jev.Boolean, Probability: ptr(0.95)},
		"suspicious__l02": {Type: jev.Score, Score: ptr(0.1), Probabilities: map[string]float64{"0": 0.9, "1": 0.1}},
	})}
	res, err := Run(context.Background(), fake, baseInput(mkUnit("u1", "api/a.go", 2)))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		q     string
		line  int
		value string
		p     float64
		conf  float64
		score float64
		probs map[string]float64
	}{
		{"behaviour", 0, model.LabelYes, 0.8, 0.8, 0, nil},
		{"covered", 0, model.LabelNo, 0.3, 0.7, 0, nil},
		{"kind", 0, "refactor", 0.7, 0.7, 0, map[string]float64{"refactor": 0.7, "feature": 0.2, "bugfix": 0.1}},
		{"risk", 0, "4", 0.6, 0.6, 4.2, map[string]float64{"1": 0.1, "2": 0.1, "3": 0.1, "4": 0.6, "5": 0.1}},
		{"risk_tags.auth", 0, model.LabelYes, 0.7, 0.7, 0, nil},
		{"risk_tags.money", 0, model.LabelNo, 0.1, 0.9, 0, nil},
		{"desc_bullet", 0, "b2", 0.85, 0.85, 0, map[string]float64{"b1": 0.1, "b2": 0.85, "none": 0.05}},
		{"fmt", 100, model.LabelYes, 0.95, 0.95, 0, nil},
		{"suspicious", 101, "1", 0.9, 0.9, 1.1, map[string]float64{"1": 0.9, "2": 0.1}},
	}
	for _, tt := range tests {
		l := get(t, res.Labels, "u1", tt.q, tt.line)
		if l.Value != tt.value || !near(l.P, tt.p) || !near(l.Confidence, tt.conf) || !near(l.Score, tt.score) ||
			l.Model != jev.Model || !reflect.DeepEqual(l.Probs, tt.probs) {
			t.Errorf("%s line %d = %+v", tt.q, tt.line, l)
		}
	}
	// Every live unit label id appears exactly once.
	var unitQs []string
	for _, l := range res.Labels {
		if l.Line == 0 {
			unitQs = append(unitQs, l.Question)
		}
	}
	if want := 13 + len(model.RiskTags); len(unitQs) != want || !sort.StringsAreSorted(unitQs) {
		t.Errorf("unit labels = %d %q, want %d sorted", len(unitQs), unitQs, want)
	}
	if res.Calls != 2 || res.Unavailable {
		t.Errorf("calls = %d unavailable = %v", res.Calls, res.Unavailable)
	}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

func TestUnitRequestShape(t *testing.T) {
	fake := &jev.Fake{}
	if _, err := Run(context.Background(), fake, baseInput(mkUnit("u1", "api/a.go", 0))); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("calls = %d, want 1 (no added lines -> no line request)", len(fake.Calls))
	}
	req := fake.Calls[0]
	crit := req.Questions["desc_bullet"].Criteria.(map[string]string)
	want := map[string]string{"b1": "add retry to fetch", "b2": "fix tz bug", "none": "implements none of the listed bullets"}
	if !reflect.DeepEqual(crit, want) {
		t.Errorf("desc_bullet criteria = %v", crit)
	}
	b, err := json.Marshal(req.State)
	if err != nil {
		t.Fatal(err)
	}
	wantState := `{"unit":{"path":"api/a.go","symbol":"fn","signature":"func fn()","change":"modified","diff":"-old()"},` +
		`"pr":{"title":"Add retry","bullets":[{"id":"b1","text":"add retry to fetch"},{"id":"b2","text":"fix tz bug"}]},` +
		`"reviewer":{"role":"backend","owns":["api/**"]},"tests_touched":["a_test.go","z_test.go"]}`
	if string(b) != wantState {
		t.Errorf("state =\n%s\nwant\n%s", b, wantState)
	}
	var names []string
	for n := range req.Questions {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, deferred := range []string{"follows_pattern", "duplicates_helper", "interface", "reasoning", "needs_context__callers", "role", "state"} {
		if slices.Contains(names, deferred) {
			t.Errorf("deferred question %s was sent", deferred)
		}
	}
	if len(names) != 13+len(model.RiskTags) {
		t.Errorf("questions = %d %q", len(names), names)
	}
}

func TestStateOmitsUndeclaredInputs(t *testing.T) {
	cat, err := questions.Parse([]byte(`
[[question]]
scope = "unit"
id = "behaviour"
type = "N"
prompt = "Runtime change?"
feeds = ["filters"]
`), "t")
	if err != nil {
		t.Fatal(err)
	}
	in := baseInput(mkUnit("u1", "a.go", 0))
	in.Catalogue = cat
	fake := &jev.Fake{}
	if _, err := Run(context.Background(), fake, in); err != nil {
		t.Fatal(err)
	}
	st := fake.Calls[0].State.(State)
	if st.PR != nil || st.Reviewer != nil || st.TestsTouched != nil || st.Lines != nil {
		t.Fatalf("state carries undeclared inputs: %+v", st)
	}
}

func TestNoBulletsDecidedWithoutJev(t *testing.T) {
	in := baseInput(mkUnit("u1", "a.go", 0))
	in.Ctx.Bullets = nil
	fake := &jev.Fake{}
	res, err := Run(context.Background(), fake, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, sent := fake.Calls[0].Questions["desc_bullet"]; sent {
		t.Error("desc_bullet sent with no bullets")
	}
	l := get(t, res.Labels, "u1", "desc_bullet", 0)
	if l.Value != model.BulletNone || l.Confidence != 1 || l.Model != ModelNoBullets {
		t.Errorf("desc_bullet = %+v", l)
	}
}

func TestLineBatches(t *testing.T) {
	fake := &jev.Fake{}
	in := baseInput(mkUnit("u1", "a.go", 45))
	res, err := Run(context.Background(), fake, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 4 {
		t.Fatalf("calls = %d, want 1 unit + 3 line requests", len(fake.Calls))
	}
	want := wantLines(45)
	for k, size := range []int{20, 20, 5} {
		st := fake.Calls[k+1].State.(State)
		if len(st.Lines) != size {
			t.Fatalf("batch %d has %d lines, want %d", k, len(st.Lines), size)
		}
		if len(fake.Calls[k+1].Questions) != size*6 {
			t.Errorf("batch %d has %d questions, want %d", k, len(fake.Calls[k+1].Questions), size*6)
		}
		for j, ls := range st.Lines {
			if ls.Line != want[k*20+j] || ls.Slot != fmt.Sprintf("l%02d", j+1) || ls.Text != fmt.Sprintf("added(%d)", k*20+j) {
				t.Errorf("batch %d line %d = %+v", k, j, ls)
			}
		}
	}
	q := fake.Calls[1].Questions["fmt__l03"]
	if !strings.Contains(q.Instructions, `"l03"`) || !strings.Contains(q.Instructions, fmt.Sprint(want[2])) {
		t.Errorf("fmt__l03 instructions = %q", q.Instructions)
	}
	var got []int
	for _, l := range res.Labels {
		if l.Question == model.QFmt && l.Line > 0 {
			got = append(got, l.Line)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("fmt label lines = %v, want %v", got, want)
	}
	// Order: unit labels, then by line then question.
	for i := 1; i < len(res.Labels); i++ {
		a, b := res.Labels[i-1], res.Labels[i]
		if a.Line > b.Line || (a.Line == b.Line && a.Question > b.Question) {
			t.Fatalf("labels out of order at %d: %+v then %+v", i, a, b)
		}
	}
}

func TestLineBatchSize(t *testing.T) {
	fake := &jev.Fake{}
	in := baseInput(mkUnit("u1", "a.go", 7))
	in.LineBatch = 3
	if _, err := Run(context.Background(), fake, in); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 4 {
		t.Fatalf("calls = %d, want 1 + 3 (3/3/1)", len(fake.Calls))
	}
}

func requestKeys(t *testing.T, reqs []jev.Request) []string {
	t.Helper()
	var keys []string
	for _, r := range reqs {
		k, err := jev.RequestKey(r)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestDeterministic(t *testing.T) {
	units := []model.ChangeUnit{mkUnit("u1", "a.go", 25), mkUnit("u2", "b.go", 3), mkUnit("u3", "c.go", 0)}
	run := func() ([]string, []model.Label) {
		fake := &jev.Fake{}
		in := baseInput(units...)
		in.Concurrency = 4
		res, err := Run(context.Background(), fake, in)
		if err != nil {
			t.Fatal(err)
		}
		return requestKeys(t, fake.Calls), res.Labels
	}
	k1, l1 := run()
	k2, l2 := run()
	if !slices.Equal(k1, k2) {
		t.Errorf("request keys differ:\n%v\n%v", k1, k2)
	}
	if !reflect.DeepEqual(l1, l2) {
		t.Error("labels differ between runs")
	}
	var order []string
	for _, l := range l1 {
		if len(order) == 0 || order[len(order)-1] != l.UnitID {
			order = append(order, l.UnitID)
		}
	}
	if !slices.Equal(order, []string{"u1", "u2", "u3"}) {
		t.Errorf("unit order = %v", order)
	}
}

func bigUnit(lines int) model.ChangeUnit {
	var b []string
	for i := range lines {
		b = append(b, fmt.Sprintf("+line %03d %s", i, strings.Repeat("x", 60)))
	}
	u := mkUnit("big", "big.go", 0)
	u.Diff = strings.Join(b, "\n")
	u.Adds = lines
	return u
}

func oneQuestion(t *testing.T, scope string) questions.Catalogue {
	t.Helper()
	cat, err := questions.Parse([]byte(`
[[question]]
scope = "`+scope+`"
id = "behaviour"
type = "N"
prompt = "Does this change alter runtime behaviour?"
feeds = ["filters"]
`), "t")
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestDiffTruncation(t *testing.T) {
	fake := &jev.Fake{}
	in := baseInput(bigUnit(200))
	in.Catalogue = oneQuestion(t, "unit")
	in.MaxStatePlusQuestion = 500
	in.MaxRequestTokens = 1000
	res, err := Run(context.Background(), fake, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 1 || len(res.Labels) != 1 {
		t.Fatalf("calls = %d labels = %d", len(fake.Calls), len(res.Labels))
	}
	st := fake.Calls[0].State.(State)
	if !strings.Contains(st.Unit.Diff, "diff lines elided") {
		t.Fatalf("diff not truncated: %d bytes", len(st.Unit.Diff))
	}
	lines := strings.Split(st.Unit.Diff, "\n")
	if !strings.HasPrefix(lines[0], "+line 000") || !strings.HasPrefix(lines[len(lines)-1], "+line 199") {
		t.Errorf("head/tail not kept: %q ... %q", lines[0], lines[len(lines)-1])
	}
	sb, _ := json.Marshal(st)
	qb, _ := json.Marshal(fake.Calls[0].Questions["behaviour"])
	if jev.EstimateTokens(string(sb))+jev.EstimateTokens(string(qb)) > 500 {
		t.Errorf("state+question exceeds limit")
	}
	// Same input, same truncation.
	fake2 := &jev.Fake{}
	if _, err := Run(context.Background(), fake2, in); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(requestKeys(t, fake.Calls), requestKeys(t, fake2.Calls)) {
		t.Error("truncation not deterministic")
	}
}

func TestQuestionSplitting(t *testing.T) {
	fake := &jev.Fake{}
	in := baseInput(mkUnit("u1", "a.go", 0))
	in.MaxRequestTokens = 700
	res, err := Run(context.Background(), fake, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) < 2 {
		t.Fatalf("calls = %d, want split", len(fake.Calls))
	}
	seen := map[string]bool{}
	for _, r := range fake.Calls {
		b, _ := json.Marshal(r)
		if jev.EstimateTokens(string(b)) > 700 {
			t.Errorf("request of %d tokens exceeds limit", jev.EstimateTokens(string(b)))
		}
		for n := range r.Questions {
			if seen[n] {
				t.Errorf("question %s sent twice", n)
			}
			seen[n] = true
		}
	}
	if len(seen) != 13+len(model.RiskTags) || res.Calls != len(fake.Calls) {
		t.Errorf("questions = %d calls = %d", len(seen), res.Calls)
	}
	if n := len(res.Labels); n != 13+len(model.RiskTags) {
		t.Errorf("labels = %d", n)
	}
}

func TestCannotFit(t *testing.T) {
	tests := []struct {
		name          string
		spq, requests int
	}{
		{"state too big", 20, 64000},
		{"question too big for request", 32000, 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := baseInput(mkUnit("u1", "a.go", 0))
			in.MaxStatePlusQuestion, in.MaxRequestTokens = tt.spq, tt.requests
			fake := &jev.Fake{}
			if _, err := Run(context.Background(), fake, in); err == nil || !strings.Contains(err.Error(), "a.go") {
				t.Fatalf("err = %v", err)
			}
			if len(fake.Calls) != 0 {
				t.Errorf("calls = %d", len(fake.Calls))
			}
		})
	}
}

func TestUnavailable(t *testing.T) {
	fake := &jev.Fake{Err: fmt.Errorf("%w: HTTP 503", jev.ErrUnavailable)}
	res, err := Run(context.Background(), fake, baseInput(mkUnit("u1", "a.go", 3), mkUnit("u2", "b.go", 0)))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Unavailable || res.Calls != 3 {
		t.Fatalf("unavailable = %v calls = %d", res.Unavailable, res.Calls)
	}
	wantN := 2*(13+len(model.RiskTags)) + 3*6 // every unit question of both units + 6 per added line
	unknown := 0
	for _, l := range res.Labels {
		if model.IsUnknown(l) {
			unknown++
			if l.P != 0 || l.Confidence != 0 || l.Probs != nil || l.Model != "" {
				t.Errorf("unknown label not zeroed: %+v", l)
			}
		}
	}
	if unknown != wantN {
		t.Errorf("unknown labels = %d, want %d", unknown, wantN)
	}
	if got := get(t, res.Labels, "u1", "fmt", 102); !model.IsUnknown(got) {
		t.Errorf("line label = %+v", got)
	}
}

func TestMissingOrMalformedAnswerIsUnknown(t *testing.T) {
	fake := &jev.Fake{Answer: answering(map[string]jev.Answer{
		"kind":      {Type: jev.Choice, Choice: "bogus"},
		"behaviour": {Type: jev.Boolean},
		"risk":      {Type: jev.Score, Probabilities: map[string]float64{"9": 1}},
		"effect":    {Type: jev.Boolean, Probability: ptr(0.5)},
	})}
	c := dropClient{Client: fake, drop: "breaking"}
	res, err := Run(context.Background(), c, baseInput(mkUnit("u1", "a.go", 0)))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"kind", "behaviour", "risk", "effect", "breaking"} {
		if l := get(t, res.Labels, "u1", q, 0); !model.IsUnknown(l) {
			t.Errorf("%s = %+v, want unknown", q, l)
		}
	}
	if res.Unavailable {
		t.Error("malformed answers must not mark Jev unavailable")
	}
}

// dropClient removes one answer from every response and adds a cost.
type dropClient struct {
	jev.Client
	drop string
	cost string
}

func (d dropClient) Evaluate(ctx context.Context, req jev.Request) (jev.Response, error) {
	resp, err := d.Client.Evaluate(ctx, req)
	delete(resp.Answers, d.drop)
	resp.ProviderMetadata.Gateway.Cost = d.cost
	return resp, err
}

func TestCallsAndCost(t *testing.T) {
	c := dropClient{Client: &jev.Fake{}, cost: "0.00125"}
	res, err := Run(context.Background(), c, baseInput(mkUnit("u1", "a.go", 21), mkUnit("u2", "b.go", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Calls != 5 || !near(res.Cost, 5*0.00125) {
		t.Errorf("calls = %d cost = %v", res.Calls, res.Cost)
	}
}

func TestNoRecordingIsError(t *testing.T) {
	in := baseInput(mkUnit("u1", "api/a.go", 2))
	probe := &jev.Fake{}
	if _, err := Run(context.Background(), probe, in); err != nil {
		t.Fatal(err)
	}
	key, _ := jev.RequestKey(probe.Calls[0])
	_, err := Run(context.Background(), &jev.Replay{Dir: t.TempDir()}, in)
	if !errors.Is(err, jev.ErrNoRecording) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{key, "api/a.go"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestFatalErrorStopsNewCalls(t *testing.T) {
	fake := &jev.Fake{Err: errors.New("jev: HTTP 400: bad request")}
	units := []model.ChangeUnit{mkUnit("u1", "a.go", 0), mkUnit("u2", "b.go", 0), mkUnit("u3", "c.go", 0)}
	res, err := Run(context.Background(), fake, baseInput(units...))
	if err == nil || !strings.Contains(err.Error(), "a.go") {
		t.Fatalf("err = %v", err)
	}
	if res.Calls != 1 || res.Unavailable {
		t.Errorf("calls = %d unavailable = %v", res.Calls, res.Unavailable)
	}
}

func TestCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, &jev.Fake{}, baseInput(mkUnit("u1", "a.go", 0)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRecordReplayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := baseInput(mkUnit("u1", "a.go", 23), mkUnit("u2", "b.go", 0))
	in.Concurrency = 3
	live := &jev.Fake{Answer: answering(map[string]jev.Answer{
		"risk":     {Type: jev.Score, Score: ptr(2.5), Probabilities: map[string]float64{"0": 0.1, "1": 0.1, "2": 0.3, "3": 0.5}},
		"fmt__l02": {Type: jev.Boolean, Probability: ptr(0.77)},
	})}
	recorded, err := Run(context.Background(), &jev.Recorder{Live: live, Dir: dir}, in)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Run(context.Background(), &jev.Replay{Dir: dir}, in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recorded, replayed) {
		t.Fatalf("replay differs:\n%+v\n%+v", recorded.Labels[:3], replayed.Labels[:3])
	}
	if l := get(t, replayed.Labels, "u1", "risk", 0); l.Value != "4" || !near(l.Score, 3.5) {
		t.Errorf("risk = %+v", l)
	}
}

// countingClient records the peak number of concurrent calls.
type countingClient struct {
	jev.Client
	mu        sync.Mutex
	cur, peak int
}

func (c *countingClient) Evaluate(ctx context.Context, req jev.Request) (jev.Response, error) {
	c.mu.Lock()
	c.cur++
	c.peak = max(c.peak, c.cur)
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.cur--; c.mu.Unlock() }()
	return c.Client.Evaluate(ctx, req)
}

func TestConcurrencyBound(t *testing.T) {
	c := &countingClient{Client: &jev.Fake{}}
	in := baseInput(mkUnit("u1", "a.go", 60), mkUnit("u2", "b.go", 60))
	in.Concurrency = 2
	if _, err := Run(context.Background(), c, in); err != nil {
		t.Fatal(err)
	}
	if c.peak > 2 {
		t.Errorf("peak concurrency = %d", c.peak)
	}
}
