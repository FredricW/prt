package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
)

var ttlCase = CommentCase{
	ID: "gh-5001", Path: "web/src/hooks/useProfileCache.ts", Line: 6, Side: model.SideNew,
	Body:   "An hour is far longer than the 5 minute TTL.",
	Before: "6: export const PROFILE_TTL_MS = 60 * 60 * 1000;",
	Diff:   "@@ -6 +6 @@\n-export const PROFILE_TTL_MS = 60 * 60 * 1000;\n+export const PROFILE_TTL_MS = 5 * 60 * 1000;",
}

func TestCommentAddressedLabels(t *testing.T) {
	yes := 0.93
	f := &jev.Fake{Answer: func(_ string, q jev.Question, _ any) jev.Answer {
		return jev.Answer{Type: q.Type, Probability: &yes}
	}}
	other := ttlCase
	other.ID, other.Line, other.Body, other.Before = "gh-5003", 75, "This swallows load errors.", ""
	res, err := CommentAddressed(context.Background(), f, AddressedInput{
		ItemID: "pr:101", Cases: []CommentCase{ttlCase, other, ttlCase}, Catalogue: questions.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 2 || res.Calls != 2 || res.Answered != 2 {
		t.Fatalf("calls = %d (res %d/%d), want 2 (duplicate ids asked once)", len(f.Calls), res.Calls, res.Answered)
	}
	if len(res.Labels) != 2 {
		t.Fatalf("labels = %+v", res.Labels)
	}
	want := map[string]CommentCase{
		model.CommentSubject("pr:101", "gh-5001"): ttlCase,
		model.CommentSubject("pr:101", "gh-5003"): other,
	}
	for _, l := range res.Labels {
		cc, ok := want[l.UnitID]
		if !ok {
			t.Errorf("unexpected subject %q", l.UnitID)
			continue
		}
		if l.Question != model.QCommentAddressed || l.Value != model.LabelYes || l.P != yes || l.Ctx != AddressedCtx(cc) || l.Ctx == "" {
			t.Errorf("label %+v, want yes p=%.2f ctx %s", l, yes, AddressedCtx(cc))
		}
	}
}

func TestCommentAddressedState(t *testing.T) {
	tests := []struct {
		name       string
		cc         CommentCase
		wantBefore bool
	}{
		{"with code when commented", ttlCase, true},
		{"base head not local", CommentCase{ID: "c", Path: "a.go", Line: 3, Side: model.SideOld, Body: "b", Diff: "+x"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &jev.Fake{}
			if _, err := CommentAddressed(context.Background(), f, AddressedInput{ItemID: "i", Cases: []CommentCase{tc.cc}, Catalogue: questions.Default()}); err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(onlyCall(t, f).State)
			if err != nil {
				t.Fatal(err)
			}
			var st map[string]any
			if err := json.Unmarshal(b, &st); err != nil {
				t.Fatal(err)
			}
			c, _ := st["comment"].(map[string]any)
			if c["body"] != tc.cc.Body || c["path"] != tc.cc.Path || st["latestPushDiff"] != tc.cc.Diff {
				t.Errorf("state = %s", b)
			}
			if c["side"] != sideName(tc.cc.Side) {
				t.Errorf("side = %v, want %s", c["side"], sideName(tc.cc.Side))
			}
			if _, has := st["codeWhenCommented"]; has != tc.wantBefore {
				t.Errorf("codeWhenCommented present = %v, want %v (%s)", has, tc.wantBefore, b)
			}
		})
	}
}

func TestAddressedCtxChangesWithInputs(t *testing.T) {
	base := AddressedCtx(ttlCase)
	for name, mut := range map[string]func(*CommentCase){
		"diff":   func(c *CommentCase) { c.Diff += "\n+more" },
		"body":   func(c *CommentCase) { c.Body += "!" },
		"line":   func(c *CommentCase) { c.Line++ },
		"side":   func(c *CommentCase) { c.Side = model.SideOld },
		"before": func(c *CommentCase) { c.Before = "" },
		"path":   func(c *CommentCase) { c.Path = "x.ts" },
	} {
		cc := ttlCase
		mut(&cc)
		if AddressedCtx(cc) == base {
			t.Errorf("ctx unchanged when %s changes", name)
		}
	}
	same := ttlCase
	same.ID = "other id"
	if AddressedCtx(same) != base {
		t.Error("ctx must not depend on the comment id")
	}
}

func TestCommentAddressedUnavailableAndDeferred(t *testing.T) {
	f := &jev.Fake{Err: fmt.Errorf("down: %w", jev.ErrUnavailable)}
	res, err := CommentAddressed(context.Background(), f, AddressedInput{ItemID: "i", Cases: []CommentCase{ttlCase}, Catalogue: questions.Default()})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Unavailable || len(res.Labels) != 1 || !model.IsUnknown(res.Labels[0]) || res.Labels[0].Ctx != AddressedCtx(ttlCase) {
		t.Errorf("unavailable result = %+v", res)
	}

	cat, err := questions.Load([]byte(`
[[question]]
scope = "item"
id = "comment_addressed"
type = "N"
prompt = "Addressed?"
inputs = ["comment_and_diff"]
feeds = ["since_you_looked"]
deferred = "off"
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	f = &jev.Fake{}
	if res, err := CommentAddressed(context.Background(), f, AddressedInput{ItemID: "i", Cases: []CommentCase{ttlCase}, Catalogue: cat}); err != nil || len(res.Labels) != 0 || len(f.Calls) != 0 {
		t.Errorf("deferred question: res %+v err %v calls %d", res, err, len(f.Calls))
	}
}

func TestCommentAddressedTruncates(t *testing.T) {
	big := ttlCase
	big.Diff = bigDiff(4000)
	big.Before = bigDiff(400)
	big.Body = strings.Repeat("é", 3000)
	f := &jev.Fake{}
	if _, err := commentAddressed(context.Background(), f, AddressedInput{ItemID: "i", Cases: []CommentCase{big}, Catalogue: questions.Default()},
		limits{statePlusQuestion: 2000, request: 3000}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(onlyCall(t, f).State)
	if !strings.Contains(string(b), "diff lines elided") {
		t.Error("an over-limit diff must be cut with an elision marker")
	}
	if est := jev.EstimateTokensLen(len(b)); est > 2000 {
		t.Errorf("state estimate %d tokens over the 2000 limit", est)
	}
	if _, err := commentAddressed(context.Background(), &jev.Fake{}, AddressedInput{ItemID: "i", Cases: []CommentCase{big}, Catalogue: questions.Default()},
		limits{statePlusQuestion: 10, request: 10}); err == nil || errors.Is(err, context.Canceled) {
		t.Errorf("impossible limits must fail, got %v", err)
	}
}
