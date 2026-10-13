package quality

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestBoardTotalsAndRendering(t *testing.T) {
	rows := []Row{
		{Item: "pr:1", Headlines: 3, HeadlinesUnsure: 2, MustReadRange: [2]int{1, 5},
			Ratios: map[string]Ratio{MEyesRecall: {1, 2}, MLinesToRead: {4, 10}, MMustRead: {1, 1}, MFidelity: {}}},
		{Item: "branch:x", Headlines: 0,
			Ratios: map[string]Ratio{MEyesRecall: {3, 3}, MLinesToRead: {0, 5}, MMustRead: {0, 1}, MBranchState: {1, 1}}},
	}
	b := NewBoard(rows)
	tot := b.Totals.Ratios
	if tot[MEyesRecall] != (Ratio{4, 5}) || tot[MLinesToRead] != (Ratio{4, 15}) || tot[MBranchState] != (Ratio{1, 1}) || b.Totals.Headlines != 3 {
		t.Errorf("totals = %+v", b.Totals)
	}
	if tot[MFidelity].Applies() {
		t.Errorf("a metric no row has must not apply: %+v", tot[MFidelity])
	}
	md := b.Markdown()
	lines := strings.Split(strings.TrimSpace(md), "\n")
	if len(lines) != 5 {
		t.Fatalf("markdown has %d lines, want header, rule, 2 rows, totals:\n%s", len(lines), md)
	}
	for _, want := range []string{"| pr:1 | 3 (+2 unsure) | 1/2 (0.50) |", "yes: 4 in 1–5", "| branch:x | 0 | 3/3 (1.00) |", "| **totals** | 3 (+2 unsure) | 4/5 (0.80) |", "—"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if b.Markdown() != md {
		t.Error("markdown not deterministic")
	}
	js, err := b.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Items []struct {
			Item    string                     `json:"item"`
			Metrics map[string]json.RawMessage `json:"metrics"`
		} `json:"items"`
	}
	if err := json.Unmarshal(js, &back); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, back.Items[0].Metrics[MEyesRecall]); err != nil || compact.String() != `{"num":1,"den":2,"value":0.5}` {
		t.Errorf("eyes recall JSON = %s (%v)", compact.String(), err)
	}
	if got := string(back.Items[0].Metrics[MFidelity]); got != "null" {
		t.Errorf("n/a fidelity JSON = %s", got)
	}
	b.Notes = []string{"a note"}
	if !strings.Contains(b.Markdown(), "Note: a note") {
		t.Error("notes not rendered")
	}
}

func TestRatioJSONNull(t *testing.T) {
	b, err := json.Marshal(map[string]Ratio{"x": {}})
	if err != nil || string(b) != `{"x":null}` {
		t.Errorf("n/a ratio JSON = %s, %v", b, err)
	}
	if RatioCell(Ratio{}) != "—" || RatioCell(Ratio{2, 3}) != "2/3 (0.67)" {
		t.Error("RatioCell")
	}
	if _, ok := Info(MFidelity); !ok {
		t.Error("Info(fidelity)")
	}
	if _, ok := Info("nope"); ok {
		t.Error("Info(nope)")
	}
}
