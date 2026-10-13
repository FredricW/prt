package review

import (
	"errors"
	"reflect"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestParseReview(t *testing.T) {
	units := sampleUnits()
	plain := `{"findings":[{"file":"web/src/api/client.ts","line":11,"severity":"high","claim":"Skipping the cache drops the auth check. Also other things.","fix":"Check auth first."}],"verdict":"request_changes","confidence":0.8}`
	cases := []struct {
		name     string
		text     string
		want     []model.Finding
		verdict  string
		conf     float64
		wantErr  bool
		wantNone bool
	}{
		{name: "plain", text: plain, verdict: VerdictRequestChanges, conf: 0.8,
			want: []model.Finding{{UnitID: "u-client", Path: "web/src/api/client.ts", Line: 11, Severity: model.SevHigh, Claim: "Skipping the cache drops the auth check.", Fix: "Check auth first."}}},
		{name: "fenced", text: "```json\n" + plain + "\n```", verdict: VerdictRequestChanges, conf: 0.8,
			want: []model.Finding{{UnitID: "u-client", Path: "web/src/api/client.ts", Line: 11, Severity: model.SevHigh, Claim: "Skipping the cache drops the auth check.", Fix: "Check auth first."}}},
		{name: "prose wrapped", text: "Here is my review {not json}:\n" + plain + "\nHope this helps {ok}.", verdict: VerdictRequestChanges, conf: 0.8,
			want: []model.Finding{{UnitID: "u-client", Path: "web/src/api/client.ts", Line: 11, Severity: model.SevHigh, Claim: "Skipping the cache drops the auth check.", Fix: "Check auth first."}}},
		{name: "bad severity, string line, dot path", text: `{"findings":[{"path":"./web/src/api/client.test.ts","line":"2","severity":"Critical","claim":"Uses e.g. an empty object as opts. Second."}],"verdict":"Comment","confidence":"0.4"}`,
			verdict: VerdictComment, conf: 0.4,
			want: []model.Finding{{UnitID: "u-test", Path: "web/src/api/client.test.ts", Line: 2, Severity: model.SevLow, Claim: "Uses e.g. an empty object as opts."}}},
		{name: "confidence above range", text: `{"findings":[],"verdict":"approve","confidence":7}`, verdict: VerdictApprove, conf: 1, wantNone: true},
		{name: "confidence below range", text: `{"findings":[],"verdict":"approve","confidence":-0.5}`, verdict: VerdictApprove, conf: 0, wantNone: true},
		{name: "deletion anchors to old span", text: `{"findings":[{"file":"server/internal/billing/legacy.go","line":41,"severity":"medium","claim":"Callers of LegacyExport remain"}],"verdict":"request changes","confidence":0.5}`,
			verdict: VerdictRequestChanges, conf: 0.5,
			want: []model.Finding{{UnitID: "u-legacy", Path: "server/internal/billing/legacy.go", Line: 41, Side: model.SideOld, Severity: model.SevMedium, Claim: "Callers of LegacyExport remain"}}},
		{name: "nearest unit in file", text: `{"findings":[{"file":"web/src/api/client.ts","line":90,"severity":"low","claim":"Far away."}],"verdict":"weird","confidence":0.5}`,
			verdict: VerdictComment, conf: 0.5,
			want: []model.Finding{{UnitID: "u-client", Path: "web/src/api/client.ts", Line: 90, Severity: model.SevLow, Claim: "Far away."}}},
		{name: "unanchored file", text: `{"findings":[{"file":"other.go","line":3,"severity":"high","claim":"Elsewhere."}],"confidence":0.5}`,
			verdict: VerdictRequestChanges, conf: 0.5,
			want: []model.Finding{{Path: "other.go", Line: 3, Severity: model.SevHigh, Claim: "Elsewhere."}}},
		{name: "empty claim dropped", text: `{"findings":[{"file":"a.go","line":1,"claim":"  "}],"verdict":"approve","confidence":0.9}`, verdict: VerdictApprove, conf: 0.9, wantNone: true},
		{name: "invalid json", text: `{"findings": [ {"file": "a.go", `, wantErr: true},
		{name: "prose only", text: "Looks good to me.", wantErr: true},
		{name: "unrelated object", text: `{"answer": 42}`, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs, v, conf, err := ParseReview(c.text, units)
			if c.wantErr {
				if !errors.Is(err, ErrBadResponse) {
					t.Fatalf("err = %v, want ErrBadResponse", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if v != c.verdict || conf != c.conf {
				t.Errorf("verdict %q conf %v, want %q %v", v, conf, c.verdict, c.conf)
			}
			if c.wantNone {
				if len(fs) != 0 {
					t.Errorf("findings %+v, want none", fs)
				}
				return
			}
			if !reflect.DeepEqual(fs, c.want) {
				t.Errorf("findings\n got %+v\nwant %+v", fs, c.want)
			}
		})
	}
}

func TestParseReviewOrderAndDedupe(t *testing.T) {
	text := `{"findings":[
	 {"file":"b.go","line":1,"severity":"low","claim":"B."},
	 {"file":"a.go","line":5,"severity":"low","claim":"Low."},
	 {"file":"a.go","line":5,"severity":"high","claim":"High."},
	 {"file":"a.go","line":5,"severity":"high","claim":"High."},
	 {"file":"a.go","line":2,"severity":"medium","claim":"Earlier."}],"verdict":"comment","confidence":0.5}`
	fs, _, _, err := ParseReview(text, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fs {
		got = append(got, f.Claim)
	}
	want := []string{"Earlier.", "High.", "Low.", "B."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}
}

func TestFakeReviewJSONRoundTrip(t *testing.T) {
	in := []model.Finding{{Path: "web/src/api/client.ts", Line: 12, Severity: model.SevMedium, Claim: "A claim.", Fix: "A fix."}}
	fs, v, c, err := ParseReview(FakeReviewJSON(in, VerdictComment, 0.9), sampleUnits())
	if err != nil || v != VerdictComment || c != 0.9 || len(fs) != 1 || fs[0].UnitID != "u-client" || fs[0].Fix != "A fix." {
		t.Fatalf("%+v %q %v %v", fs, v, c, err)
	}
	if fs, _, _, err := ParseReview(FakeReviewJSON(nil, VerdictApprove, 1), nil); err != nil || fs != nil {
		t.Fatalf("empty: %+v %v", fs, err)
	}
}

func TestFirstSentence(t *testing.T) {
	cases := map[string]string{
		"One. Two.":                       "One.",
		"Calls foo.Bar twice! Then more.": "Calls foo.Bar twice!",
		"Version 1.2 is wrong? Yes.":      "Version 1.2 is wrong?",
		"Use e.g. a map. Then.":           "Use e.g. a map.",
		"No terminator":                   "No terminator",
		"  multi\n line\tclaim.  Next":    "multi line claim.",
	}
	for in, want := range cases {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}
