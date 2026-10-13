package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
)

func branchInput() BranchStateInput {
	return BranchStateInput{
		ItemID:       "branch:05-spike-dark-mode",
		Ref:          "05-spike-dark-mode",
		Subjects:     []string{"wip: try a dark palette", "spike: dark mode toggle"},
		AgeDays:      16,
		Unpushed:     2,
		OpenPRTitles: []string{"Retry failed fetches", "Add rate limiting"},
		Catalogue:    questions.Default(),
	}
}

func TestBranchStateGolden(t *testing.T) {
	f := &jev.Fake{}
	res, err := BranchState(context.Background(), f, branchInput())
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "branch", onlyCall(t, f))
	if len(res.Labels) != 1 || res.Calls != 1 {
		t.Fatalf("result %+v", res)
	}
	l := res.Labels[0]
	if l.UnitID != model.BranchSubject("branch:05-spike-dark-mode") || l.Question != model.QState || l.Ctx != BranchCtx(branchInput()) {
		t.Errorf("label %+v", l)
	}
	opts := []string{model.BranchWIP, model.BranchReady, model.BranchSpike, model.BranchAbandoned, model.BranchSuperseded}
	found := false
	for _, o := range opts {
		found = found || l.Value == o
	}
	if !found || len(l.Probs) == 0 {
		t.Errorf("value %q not an option (probs %v)", l.Value, l.Probs)
	}
}

func TestBranchCtxChangesWithInputs(t *testing.T) {
	base := BranchCtx(branchInput())
	for name, mod := range map[string]func(*BranchStateInput){
		"ref":      func(in *BranchStateInput) { in.Ref = "other" },
		"subjects": func(in *BranchStateInput) { in.Subjects = in.Subjects[:1] },
		"age":      func(in *BranchStateInput) { in.AgeDays++ },
		"unpushed": func(in *BranchStateInput) { in.Unpushed = 0 },
		"prs":      func(in *BranchStateInput) { in.OpenPRTitles = nil },
	} {
		in := branchInput()
		mod(&in)
		if BranchCtx(in) == base {
			t.Errorf("%s: Ctx unchanged", name)
		}
	}
	in := branchInput()
	in.Catalogue = questions.Catalogue{}
	if BranchCtx(in) != base || len(base) != 16 {
		t.Errorf("Ctx depends on the catalogue or is not 16 chars")
	}
}

func TestBranchStateCapsAndTruncates(t *testing.T) {
	in := branchInput()
	in.OpenPRTitles = nil
	for i := range 40 {
		in.OpenPRTitles = append(in.OpenPRTitles, fmt.Sprintf("PR %d", i))
	}
	in.Subjects = nil
	for i := range 200 {
		in.Subjects = append(in.Subjects, fmt.Sprintf("commit %03d %s", i, strings.Repeat("x", 400)))
	}
	f := &jev.Fake{}
	if _, err := branchStateWith(context.Background(), f, in, limits{statePlusQuestion: 4000, request: 4200}); err != nil {
		t.Fatal(err)
	}
	st := onlyCall(t, f).State.(branchState)
	if len(st.OpenPRs) != MaxOpenPRTitles {
		t.Errorf("open PRs = %d", len(st.OpenPRs))
	}
	if len(st.Subjects) > maxBranchSubjects+1 || !strings.Contains(strings.Join(st.Subjects, "\n"), "commits elided") {
		t.Errorf("subjects not cut: %d", len(st.Subjects))
	}
	if !strings.HasPrefix(st.Subjects[0], "commit 000") || len(st.Subjects[0]) > maxSubjectBytes+len("…") {
		t.Errorf("first subject %q", st.Subjects[0][:20])
	}
	rb, _ := json.Marshal(onlyCall(t, f))
	if jev.EstimateTokensLen(len(rb)) > 4200 {
		t.Errorf("request over limit")
	}
}

const branchDeferredTOML = `
[[question]]
scope = "branch"
id = "state"
type = "C"
prompt = "State?"
options = ["WIP", "spike"]
feeds = ["picker"]
deferred = "off"
`

func TestBranchStateDeferredAndUnavailable(t *testing.T) {
	cat, err := questions.Load([]byte(branchDeferredTOML), "test")
	if err != nil {
		t.Fatal(err)
	}
	in := branchInput()
	in.Catalogue = cat
	f := &jev.Fake{}
	res, err := BranchState(context.Background(), f, in)
	if err != nil || len(f.Calls) != 0 || len(res.Labels) != 0 {
		t.Fatalf("deferred: err %v calls %d res %+v", err, len(f.Calls), res)
	}
	down := &respondClient{respond: func(int, jev.Request) (jev.Response, error) { return jev.Response{}, jev.ErrUnavailable }}
	res, err = BranchState(context.Background(), down, branchInput())
	if err != nil || !res.Unavailable || len(res.Labels) != 1 || !model.IsUnknown(res.Labels[0]) {
		t.Fatalf("unavailable: err %v res %+v", err, res)
	}
}
