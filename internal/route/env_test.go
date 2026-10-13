package route

import (
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func lookup(t *testing.T, f GroupFacts, name string) Value {
	t.Helper()
	v, _ := NewEnv(f).Lookup(name)
	return v
}

func TestEnvBooleansAnyUnit(t *testing.T) {
	a, c := unit("a", 1), unit("c", 2)
	base := append(fullLabels("a", "feature"), fullLabels("c", "feature")...)
	tests := []struct {
		name   string
		labels []model.Label
		want   Value
	}{
		{"none true", base, b(false)},
		{"one true", set(clone(base), yes("c", model.QBreaking)), b(true)},
		{"true beats unknown", set(set(clone(base), yes("c", model.QBreaking)), unknownLabel("a", model.QBreaking)), b(true)},
		{"unknown", set(clone(base), unknownLabel("a", model.QBreaking)), unk},
		{"missing", drop(clone(base), "a", model.QBreaking), unk},
		{"P below half", set(clone(base), model.Label{UnitID: "a", Question: model.QBreaking, Value: model.LabelNo, P: 0.49}), b(false)},
		{"P at half", set(clone(base), model.Label{UnitID: "a", Question: model.QBreaking, Value: model.LabelYes, P: 0.5}), b(true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lookup(t, group([]model.ChangeUnit{a, c}, tt.labels), model.QBreaking)
			if got != tt.want {
				t.Errorf("breaking = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEnvNumbers(t *testing.T) {
	a, c := unit("a", 1), unit("c", 2)
	base := append(fullLabels("a", "feature"), fullLabels("c", "feature")...)
	labels := set(set(clone(base), score("a", model.QRisk, 4, 3.6)), score("c", model.QRelevance, 4, 4.3))
	f := group([]model.ChangeUnit{a, c}, labels)
	if got := lookup(t, f, model.QRisk); got != n(4) {
		t.Errorf("risk = %+v, want rung 4", got)
	}
	if got := lookup(t, f, model.QRelevance); got != n(4.3) {
		t.Errorf("relevance = %+v, want 4.3", got)
	}
	f.Labels = set(clone(labels), unknownLabel("c", model.QRisk))
	if got := lookup(t, f, model.QRisk); got != (Value{N: 4, AtLeast: true}) {
		t.Errorf("risk with an unknown unit = %+v, want unknown, at least 4", got)
	}
	f.Labels = set(set(clone(labels), unknownLabel("c", model.QRisk)), unknownLabel("a", model.QRisk))
	if got := lookup(t, f, model.QRisk); got != unk {
		t.Errorf("risk with no known unit = %+v, want unknown", got)
	}
	if got := lookup(t, f, nameUnits); got != n(2) {
		t.Errorf("units = %+v", got)
	}
	if got := lookup(t, f, nameLines); got != n(20) {
		t.Errorf("lines = %+v", got)
	}
}

func TestEnvChoice(t *testing.T) {
	a, c := unit("a", 1), unit("c", 2)
	base := append(fullLabels("a", "feature"), fullLabels("c", "feature")...)
	f := group([]model.ChangeUnit{a, c}, base)
	if got := lookup(t, f, model.QContract); got != s(model.ContractNone) {
		t.Errorf("contract = %+v", got)
	}
	f.Labels = set(clone(base), choice("c", model.QContract, "signature"))
	if got := lookup(t, f, model.QContract); got != s("mixed") {
		t.Errorf("contract = %+v, want mixed", got)
	}
	f.Labels = set(clone(base), unknownLabel("c", model.QContract))
	if got := lookup(t, f, model.QContract); got != unk {
		t.Errorf("contract = %+v, want unknown", got)
	}
}

func TestEnvKind(t *testing.T) {
	moved := unit("m", 3)
	moved.Kind = model.UnitMoved
	format := unit("f", 4)
	format.Format = true
	tests := []struct {
		name   string
		units  []model.ChangeUnit
		labels []model.Label
		agg    string
		want   Value
	}{
		{"uniform", []model.ChangeUnit{unit("a", 1), unit("c", 2)}, append(fullLabels("a", "test"), fullLabels("c", "test")...), "", s("test")},
		{"mixed", []model.ChangeUnit{unit("a", 1), unit("c", 2)}, append(fullLabels("a", "test"), fullLabels("c", "feature")...), "", s("mixed")},
		{"moved without labels", []model.ChangeUnit{moved}, nil, "", s("moved")},
		{"format-only", []model.ChangeUnit{format}, fullLabels("f", "refactor"), "", s("formatting")},
		{"formatting label", []model.ChangeUnit{unit("a", 1)}, fullLabels("a", "formatting"), "", s("formatting")},
		{"unknown", []model.ChangeUnit{unit("a", 1), unit("c", 2)}, fullLabels("a", "feature"), "", unk},
		{"unknown beats mixed", []model.ChangeUnit{unit("a", 1), unit("c", 2), moved}, fullLabels("a", "feature"), "", unk},
		{"agg wins", []model.ChangeUnit{unit("a", 1)}, fullLabels("a", "feature"), "dependency", s("dependency")},
		{"agg unknown", []model.ChangeUnit{unit("a", 1)}, fullLabels("a", "feature"), "unknown", unk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := group(tt.units, tt.labels)
			f.GroupAgg.Kind = tt.agg
			if got := lookup(t, f, nameKind); got != tt.want {
				t.Errorf("kind = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEnvTags(t *testing.T) {
	a, c := unit("a", 1), unit("c", 2)
	base := append(fullLabels("a", "feature"), fullLabels("c", "feature")...)
	f := group([]model.ChangeUnit{a, c}, base)
	if got := lookup(t, f, "tag.auth"); got != b(false) {
		t.Errorf("tag.auth = %+v, want false", got)
	}
	// From labels.
	f.Labels = set(clone(base), yes("c", "risk_tags.auth"))
	if got := lookup(t, f, "tag.auth"); got != b(true) {
		t.Errorf("tag.auth from labels = %+v", got)
	}
	// From aggs: swallow lines fold into error_path although the label is no.
	f.Labels = base
	f.Aggs = []model.UnitAgg{{UnitID: "a"}, {UnitID: "c", RiskTags: []string{"error_path"}}}
	if got := lookup(t, f, "tag.error_path"); got != b(true) {
		t.Errorf("tag.error_path from aggs = %+v", got)
	}
	// Unknown label and no agg tag.
	f.Labels = set(clone(base), unknownLabel("a", "risk_tags.pii"))
	if got := lookup(t, f, "tag.pii"); got != unk {
		t.Errorf("tag.pii = %+v, want unknown", got)
	}
	// The sub-label itself reads the labels only.
	if got := lookup(t, f, "risk_tags.error_path"); got != b(false) {
		t.Errorf("risk_tags.error_path = %+v, want false", got)
	}
}

func TestEnvLabelsMissing(t *testing.T) {
	a := unit("a", 1)
	f := group([]model.ChangeUnit{a}, fullLabels("a", "feature"))
	f.Group.Mechanical = true
	f.Aggs = []model.UnitAgg{{UnitID: "a", RiskTags: []string{"auth"}}}
	f.LabelsMissing = true
	for _, name := range []string{model.QBreaking, model.QRisk, model.QContract, "tag.auth", "risk_tags.auth", model.QRelevance} {
		if got := lookup(t, f, name); got != unk {
			t.Errorf("%s = %+v, want unknown", name, got)
		}
	}
	if got := lookup(t, f, nameKind); got != unk {
		t.Errorf("kind = %+v, want unknown", got)
	}
	if got := lookup(t, f, nameMechanical); got != b(true) {
		t.Errorf("mechanical = %+v", got)
	}
	if got := lookup(t, f, nameUngrouped); got != b(false) {
		t.Errorf("ungrouped = %+v", got)
	}
}

func TestEnvUndefinedAndInferred(t *testing.T) {
	a := unit("a", 1)
	labels := append(fullLabels("a", "feature"), yes("a", "custom_flag"), score("a", "custom_score", 3, 3.4), choice("a", "custom_choice", "x"))
	env := NewEnv(group([]model.ChangeUnit{a}, labels))
	if _, ok := env.Lookup("nonsense"); ok {
		t.Error("nonsense defined")
	}
	for name, want := range map[string]Value{"custom_flag": b(true), "custom_score": n(3.4), "custom_choice": s("x")} {
		if got, ok := env.Lookup(name); !ok || got != want {
			t.Errorf("%s = %+v, %v; want %+v", name, got, ok, want)
		}
	}
	// A known question nobody answered is unknown, not undefined.
	if got, ok := env.Lookup(model.QReasoning); !ok || got != unk {
		t.Errorf("reasoning = %+v, %v; want unknown", got, ok)
	}
}

func clone(ls []model.Label) []model.Label { return append([]model.Label(nil), ls...) }

func drop(ls []model.Label, unitID, q string) []model.Label {
	out := ls[:0]
	for _, l := range ls {
		if l.UnitID != unitID || l.Question != q {
			out = append(out, l)
		}
	}
	return out
}
