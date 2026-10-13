package route

import (
	"slices"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Group code facts and derived names of the `when` language.
const (
	nameKind       = "kind"
	nameMechanical = "mechanical"
	nameUngrouped  = "ungrouped"
	nameUnits      = "units"
	nameLines      = "lines"
	tagPrefix      = "tag."

	kindMixed   = "mixed"
	kindUnknown = "unknown"
)

// qType is how a unit label question aggregates to a group value.
type qType int8

const (
	qBool   qType = iota + 1 // any unit with P(yes) >= 0.5
	qRung                    // max integer rung (model.ScoreLevel)
	qScore                   // max interpolated score
	qChoice                  // shared value, else "mixed"
)

// unitQuestions are the unit-scope catalogue questions routing can read.
// Multi-boolean rows (risk_tags, needs_context) appear as their sub-labels.
var unitQuestions = buildUnitQuestions()

func buildUnitQuestions() map[string]qType {
	m := map[string]qType{
		model.QBehaviour:        qBool,
		model.QBreaking:         qBool,
		model.QReversible:       qBool,
		model.QCovered:          qBool,
		model.QTestWeakened:     qBool,
		model.QFollowsPattern:   qBool,
		model.QDuplicatesHelper: qBool,
		model.QInterface:        qBool,
		model.QRisk:             qRung,
		model.QRelevance:        qScore,
		model.QReasoning:        qScore,
		model.QEffect:           qChoice,
		model.QDescBullet:       qChoice,
		model.QContract:         qChoice,
		model.QFailMode:         qChoice,
		model.QDepth:            qChoice,
		model.QKind:             qChoice,
	}
	for _, s := range model.RiskTags {
		m[model.SubQuestion(model.QRiskTags, s)] = qBool
	}
	for _, s := range model.NeedsContextKinds {
		m[model.SubQuestion(model.QNeedsContext, s)] = qBool
	}
	return m
}

// KnownIdent reports whether name is an identifier the `when` language
// defines: a unit question id (or multi-boolean sub-label), tag.<risk tag>,
// kind, mechanical, ungrouped, units or lines.
func KnownIdent(name string) bool {
	if _, ok := unitQuestions[name]; ok {
		return true
	}
	if sub, ok := strings.CutPrefix(name, tagPrefix); ok {
		return slices.Contains(model.RiskTags, sub)
	}
	switch name {
	case nameMechanical, nameUngrouped, nameUnits, nameLines:
		return true
	}
	return false
}

// groupEnv is the Env of one group; values are computed on lookup.
type groupEnv struct {
	f      GroupFacts
	labels map[string]map[string]model.Label // unit id → question → label
}

// NewEnv builds the Env of a group (semantics in the package doc).
func NewEnv(f GroupFacts) Env { return newGroupEnv(f) }

func newGroupEnv(f GroupFacts) *groupEnv {
	in := make(map[string]bool, len(f.Units))
	for _, u := range f.Units {
		in[u.ID] = true
	}
	idx := make(map[string]map[string]model.Label, len(f.Units))
	for _, l := range f.Labels {
		if l.Line > 0 || !in[l.UnitID] {
			continue
		}
		m := idx[l.UnitID]
		if m == nil {
			m = map[string]model.Label{}
			idx[l.UnitID] = m
		}
		m[l.Question] = l // later duplicates win
	}
	return &groupEnv{f: f, labels: idx}
}

// Lookup implements Env.
func (e *groupEnv) Lookup(name string) (Value, bool) {
	switch name {
	case nameMechanical:
		return Value{Kind: KindBool, B: e.f.Group.Mechanical}, true
	case nameUngrouped:
		return Value{Kind: KindBool, B: e.f.Group.Ungrouped}, true
	case nameUnits:
		return Value{Kind: KindNum, N: float64(len(e.f.Units))}, true
	case nameLines:
		n := 0
		for _, u := range e.f.Units {
			n += u.Adds + u.Dels
		}
		return Value{Kind: KindNum, N: float64(n)}, true
	case nameKind:
		return e.kind(), true
	}
	if sub, ok := strings.CutPrefix(name, tagPrefix); ok {
		return triValue(e.tag(sub)), true
	}
	t, ok := unitQuestions[name]
	if !ok {
		if t, ok = e.inferType(name); !ok {
			return Value{}, false
		}
	}
	if e.f.LabelsMissing {
		return Value{}, true
	}
	switch t {
	case qBool:
		return triValue(e.anyYes(name)), true
	case qRung, qScore:
		return e.maxScore(name, t == qRung), true
	}
	return e.choice(name), true
}

// label returns unit u's label for q when it is a real answer.
func (e *groupEnv) label(unitID, q string) (model.Label, bool) {
	l, ok := e.labels[unitID][q]
	if !ok || model.IsUnknown(l) {
		return model.Label{}, false
	}
	return l, true
}

// anyYes is "any unit": True when a known unit label has P(yes) ≥ 0.5,
// else Unknown when any unit's label is unknown or missing, else False.
func (e *groupEnv) anyYes(q string) Tri {
	unknown := false
	for _, u := range e.f.Units {
		l, ok := e.label(u.ID, q)
		switch {
		case !ok:
			unknown = true
		case l.P >= 0.5:
			return True
		}
	}
	if unknown {
		return Unknown
	}
	return False
}

// tag is tag.<sub>: true when any UnitAgg carries the tag (aggregate folds
// swallow lines into error_path and pii lines into pii) or any unit's
// risk_tags.<sub> label is yes; otherwise the labels decide False/Unknown.
func (e *groupEnv) tag(sub string) Tri {
	if e.f.LabelsMissing {
		return Unknown
	}
	for _, a := range e.f.Aggs {
		if slices.Contains(a.RiskTags, sub) {
			return True
		}
	}
	return e.anyYes(model.SubQuestion(model.QRiskTags, sub))
}

// maxScore is the max over units of the integer rung (rung) or the
// interpolated score. When some units' labels are unknown, missing or
// malformed it is Unknown with a lower bound (AtLeast, N = the known max)
// when at least one unit is known, so `risk >= 4` with a known 5 is still
// True (Kleene: the max is ≥ 5 whatever the unknown units say).
func (e *groupEnv) maxScore(q string, rung bool) Value {
	best, known, unknown := 0.0, false, false
	for _, u := range e.f.Units {
		l, ok := e.label(u.ID, q)
		if !ok {
			unknown = true
			continue
		}
		n := float64(model.ScoreLevel(l))
		if !rung && l.Score > 0 {
			n = l.Score
		}
		if n <= 0 {
			unknown = true // malformed answer
			continue
		}
		best, known = max(best, n), true
	}
	switch {
	case !known:
		return Value{}
	case unknown:
		return Value{N: best, AtLeast: true}
	}
	return Value{Kind: KindNum, N: best}
}

// choice is the value every unit shares, else "mixed". "mixed" is decided
// as soon as two known values differ, even when other units are unknown
// (whatever they say, the group is mixed); it is Unknown when the known
// units agree but some unit's label is unknown or missing.
func (e *groupEnv) choice(q string) Value {
	vals := make([]string, 0, len(e.f.Units))
	for _, u := range e.f.Units {
		if l, ok := e.label(u.ID, q); ok {
			vals = append(vals, l.Value)
		} else {
			vals = append(vals, kindUnknown)
		}
	}
	return foldChoice(vals)
}

// foldChoice folds unit values ("unknown" for an unknown unit) into the
// group value: the value every unit shares; "mixed" once two known values
// differ (whatever the unknown units say); otherwise Unknown — with Either
// set to the value the known units share, since the group can then only be
// that value or "mixed".
func foldChoice(vals []string) Value {
	shared, unknown := "", false
	for _, v := range vals {
		switch {
		case v == kindUnknown || v == "":
			unknown = true
		case shared == "":
			shared = v
		case v != shared:
			return Value{Kind: KindStr, S: kindMixed}
		}
	}
	switch {
	case shared == "":
		return Value{}
	case unknown:
		return Value{Either: shared}
	}
	return Value{Kind: KindStr, S: shared}
}

// kind is GroupAgg.Kind, or the same rule computed from units and labels.
// Code facts (git-moved, formatting-only units) decide a unit's kind even
// when its label is unknown, so kind does not follow LabelsMissing blindly.
func (e *groupEnv) kind() Value {
	k := e.f.GroupAgg.Kind
	// GroupAgg.Kind "unknown" carries no Either bound; derive it from the
	// units so `kind in [...]` can still be decided.
	if k == "" || k == kindUnknown {
		return e.derivedKind()
	}
	return Value{Kind: KindStr, S: k}
}

func (e *groupEnv) derivedKind() Value {
	vals := make([]string, 0, len(e.f.Units))
	for _, u := range e.f.Units {
		vals = append(vals, e.unitKind(u))
	}
	return foldChoice(vals)
}

// unitKind is a unit's effective kind: "moved" for git-moved units,
// "formatting" for format-only units or kind formatting, else the kind label
// ("unknown" when unknown or missing).
func (e *groupEnv) unitKind(u model.ChangeUnit) string {
	if u.Kind == model.UnitMoved {
		return model.NoiseMoved
	}
	if u.Format {
		return model.KindFormatting
	}
	if e.f.LabelsMissing {
		return kindUnknown
	}
	if l, ok := e.label(u.ID, model.QKind); ok {
		return l.Value
	}
	return kindUnknown
}

// anyUnitValue reports whether any unit has a known label q with one of
// the given values.
func (e *groupEnv) anyUnitValue(q string, vals ...string) bool {
	if e.f.LabelsMissing {
		return false
	}
	for _, u := range e.f.Units {
		if l, ok := e.label(u.ID, q); ok && slices.Contains(vals, l.Value) {
			return true
		}
	}
	return false
}

// inferType guesses the type of a question outside the built-in table (a
// user-added catalogue row) from its first known label; ok=false when no
// unit has the label at all.
func (e *groupEnv) inferType(q string) (qType, bool) {
	seen := false
	for _, u := range e.f.Units {
		l, has := e.labels[u.ID][q]
		if !has {
			continue
		}
		seen = true
		if model.IsUnknown(l) {
			continue
		}
		switch {
		case l.Value == model.LabelYes || l.Value == model.LabelNo:
			return qBool, true
		case model.ScoreLevel(l) > 0:
			return qScore, true
		}
		return qChoice, true
	}
	return qChoice, seen
}
