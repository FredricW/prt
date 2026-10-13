// Package route is pipeline stage 6: it evaluates prt.routing.toml rules top
// to bottom against a group's aggregate labels (first match wins), decides
// attached context (callers / types / tests / config / code_under_test) and
// specialist prompts, and plans the review queue under deep_slots and the
// per-item cost cap (hot-spot order). Pure: no I/O, no model calls.
//
// # The `when` language
//
// Identifiers: label ids (any catalogue question id such as `breaking`,
// `risk`, `relevance`, `behaviour`, `covered`, `contract`, `depth`, and
// sub-labels `needs_context.callers`), `tag.<sub>` (risk tags: auth, async,
// error_path, data_write, money, pii, io_in_loop, date_tz), `kind`,
// `mechanical`, `ungrouped`, `units`, `lines`; bare words inside `in [...]`
// and on the right of `==`/`!=` are string literals (`kind in [formatting,
// moved]`, `kind == test`); numbers; "quoted strings"; `true`/`false`.
// Operators: `!`, `&&`, `||`, `==`, `!=`, `>=`, `<=`, `>`, `<`, `in [a, b]`,
// parentheses. Precedence: `!` > comparison/`in` > `&&` > `||`.
//
// # Group values (Env)
//
//   - booleans are "any unit": `breaking` is true when any unit's breaking
//     P ≥ 0.5; `tag.auth` when any unit has the tag;
//   - numbers are the max over units: `risk` = max risk rung (1..5),
//     `relevance` max interpolated relevance;
//   - `kind` = model.GroupAgg.Kind (uniform kind, "moved", "formatting",
//     "unknown" or "mixed");
//   - `mechanical`, `ungrouped`, `units`, `lines` are code facts (never
//     unknown).
//
// # Unknown labels (spec failure handling: "routing treats unknown as deep")
//
// Evaluation is three-valued (Kleene): an unknown label (Jev unavailable,
// value "unknown", never asked) is Unknown; `false || U = U`, `true || U =
// true`, `false && U = false`, `!U = U`, comparisons with U are U. A rule
// evaluating True fires; False moves on; Unknown routes the group deep
// (Decision.Unknown, words "labels unknown → deep"). Short-circuit keeps
// `mechanical || kind in [...]` true for a mechanical group whose labels
// are unknown.
package route

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

// Tri is a three-valued truth value.
type Tri int8

// Truth values.
const (
	False Tri = iota
	True
	Unknown
)

// ValueKind is the type of an Env value.
type ValueKind int8

// Value kinds.
const (
	KindUnknown ValueKind = iota // known name, no real answer
	KindBool
	KindNum
	KindStr
)

// Value is one label value of a group.
type Value struct {
	Kind ValueKind
	B    bool
	N    float64
	S    string
	// AtLeast marks an Unknown number whose true value is at least N: a max
	// over units where some units are unknown and the known ones reach N.
	// Comparisons the bound settles (`risk >= 4` with a known 5) decide;
	// the rest stay Unknown.
	AtLeast bool
	// Either marks an Unknown choice (such as `kind`) whose true value is
	// either Either or "mixed": the known units share Either and the rest
	// are unknown. `in [...]`, `==` and `!=` decide when both candidates
	// give the same answer; the rest stay Unknown.
	Either string
}

// Env is what a rule expression is evaluated against for one group.
// Lookup returns ok=false for a name it does not define (treated as
// Unknown by Eval; Compile warns about such names).
type Env interface {
	Lookup(name string) (Value, bool)
}

// Expr is a compiled rule expression.
type Expr interface {
	// Eval returns the three-valued result; err is a type error (e.g.
	// `risk && kind`), which Route treats as a rule that does not fire and
	// reports in Decision.Err.
	Eval(env Env) (Tri, error)
	String() string   // the source text, shown verbatim on the Pipeline page
	Idents() []string // identifiers referenced (for validation)
}

// Rule is one compiled routing rule.
type Rule struct {
	Index   int // 1-based position in the file
	When    Expr
	Tier    model.Tier
	Prompts []string
	Context []string
	// Words is "if <when> → <tier>" for the rules panel and routing table.
	Words string
}

// Rules is a compiled prt.routing.toml.
type Rules struct {
	Rules    []Rule
	Config   config.Routing
	Warnings []string // unknown identifiers (unknown context/prompt names are Compile errors)
}

// ctxOrder is the canonical order of context kinds.
var ctxOrder = []string{model.CtxCallers, model.CtxTypes, model.CtxTests, model.CtxConfig, model.CtxCodeUnderTest}

var promptNames = []string{model.PromptSecurity, model.PromptPerformance, model.PromptTests}

// Compile parses and validates every rule. Errors name the 1-based rule
// index: a parse error, a tier outside none/fast/deep, an unknown prompt or
// context name. Identifiers outside the known set (KnownIdent) are
// warnings, since a user-added catalogue question may define them.
func Compile(r config.Routing) (Rules, error) {
	out := Rules{Config: r}
	for i, cr := range r.Rules {
		rule, err := compileRule(i+1, cr)
		if err != nil {
			return Rules{}, err
		}
		for _, id := range rule.When.Idents() {
			if !KnownIdent(id) {
				out.Warnings = append(out.Warnings, fmt.Sprintf("rule %d: unknown identifier %q (unknown unless a catalogue question defines it)", i+1, id))
			}
		}
		out.Rules = append(out.Rules, rule)
	}
	return out, nil
}

func compileRule(index int, cr config.Rule) (Rule, error) {
	e, err := Parse(cr.When)
	if err != nil {
		return Rule{}, fmt.Errorf("route: rule %d: when %q: %w", index, cr.When, err)
	}
	tier := model.Tier(cr.Tier)
	switch tier {
	case model.TierNone, model.TierFast, model.TierDeep:
	default:
		return Rule{}, fmt.Errorf("route: rule %d: unknown tier %q (want none, fast or deep)", index, cr.Tier)
	}
	for _, p := range cr.Prompts {
		if !slices.Contains(promptNames, p) {
			return Rule{}, fmt.Errorf("route: rule %d: unknown prompt %q (want one of %s)", index, p, strings.Join(promptNames, ", "))
		}
	}
	for _, c := range cr.Context {
		if !slices.Contains(ctxOrder, c) {
			return Rule{}, fmt.Errorf("route: rule %d: unknown context %q (want one of %s)", index, c, strings.Join(ctxOrder, ", "))
		}
	}
	return Rule{
		Index:   index,
		When:    e,
		Tier:    tier,
		Prompts: slices.Clone(cr.Prompts),
		Context: slices.Clone(cr.Context),
		Words:   "if " + cr.When + " → " + cr.Tier,
	}, nil
}

// GroupFacts is what routing reads for one group.
type GroupFacts struct {
	Group model.Group
	// Units are the group's units in review order; Aggs the matching unit
	// aggregates (same order; nil when aggregate is missing) and Labels the
	// unit-scope labels of those units (current Ctx, overrides applied).
	Units    []model.ChangeUnit
	Aggs     []model.UnitAgg
	Labels   []model.Label
	GroupAgg model.GroupAgg
	// LabelsMissing is ItemAgg.LabelsMissing (Jev unavailable for the item).
	LabelsMissing bool
}

// Decision is the routing outcome for one group.
type Decision struct {
	GroupID   string
	Tier      model.Tier
	RuleIndex int    // 1-based; 0 when Unknown decided
	Rule      string // the `when` text that fired ("" when Unknown decided)
	Words     string // "if … → tier" or "labels unknown → deep"
	// Prompts are the specialist prompts to append: every prompt whose
	// trigger fired, for any reviewed decision (security: tag.auth|tag.pii|tag.money;
	// performance: tag.io_in_loop|tag.async|kind perf|effect performance;
	// tests: kind test|test_weakened). Context is the rule's context
	// kinds plus the stage-6 decisions: callers when any unit is breaking,
	// types when a signature changed (contract signature or api shape),
	// tests when the group has untested risky units, config when an
	// env/flag label fired. Both nil for tier none.
	Prompts []string
	Context []string
	Unknown bool    // routed deep because a label it needed was unknown
	HotSpot float64 // GroupAgg.HotSpot, the queue key
	Err     string  // type errors of rules that were skipped
}

// Route decides tiers for groups: rules top to bottom, first match wins.
// A rule evaluating Unknown routes the group deep (Decision.Unknown); a rule
// with a type error is skipped and reported in Decision.Err. A group no rule
// matches gets tier none with words "no rule matched".
func Route(rs Rules, groups []GroupFacts) []Decision {
	out := make([]Decision, 0, len(groups))
	for _, g := range groups {
		out = append(out, decide(rs, g))
	}
	return out
}

func decide(rs Rules, g GroupFacts) Decision {
	env := newGroupEnv(g)
	d := Decision{GroupID: g.Group.ID, Tier: model.TierNone, Words: "no rule matched", HotSpot: groupHotSpot(g)}
	var errs []string
	for _, r := range rs.Rules {
		t, err := r.When.Eval(env)
		if err != nil {
			errs = append(errs, fmt.Sprintf("rule %d: %v", r.Index, err))
			continue
		}
		if t == False {
			continue
		}
		if t == Unknown {
			d.Tier, d.Unknown = model.TierDeep, true
			d.Words = fmt.Sprintf("labels unknown → deep (rule %d)", r.Index)
			d.Prompts = firedPrompts(env, nil)
			d.Context = autoContext(env, nil)
		} else {
			d.Tier, d.RuleIndex, d.Rule, d.Words = r.Tier, r.Index, r.When.String(), r.Words
			if r.Tier != model.TierNone {
				d.Prompts = firedPrompts(env, r.Prompts)
				d.Context = autoContext(env, r.Context)
			}
		}
		break
	}
	d.Err = strings.Join(errs, "; ")
	return d
}

// groupHotSpot is GroupAgg.HotSpot, falling back to the max unit hot spot.
func groupHotSpot(g GroupFacts) float64 {
	if g.GroupAgg.HotSpot != 0 {
		return g.GroupAgg.HotSpot
	}
	best := 0.0
	for _, a := range g.Aggs {
		best = max(best, model.UnitHotSpot(a))
	}
	return best
}

// firedPrompts is every specialist prompt whose trigger fired (spec stage
// 7: "appended when the matching tag fired"), whichever rule decided: the
// rule's own prompts first in rule order, then the others in canonical
// order. A rule's `prompts` list orders, it does not gate.
func firedPrompts(e *groupEnv, rulePrompts []string) []string {
	var out []string
	for _, p := range append(slices.Clone(rulePrompts), promptNames...) {
		if !slices.Contains(out, p) && promptFired(e, p) {
			out = append(out, p)
		}
	}
	return out
}

// AutoPrompts are the specialist prompts whose trigger fired for a group
// on its own: what a review the user forces deep gets when no rule routed
// the group.
func AutoPrompts(f GroupFacts) []string { return firedPrompts(newGroupEnv(f), nil) }

// promptFired evaluates a specialist prompt's trigger. Kind and effect are
// read per unit ("any unit has kind perf"), like the other triggers.
func promptFired(e *groupEnv, p string) bool {
	switch p {
	case model.PromptSecurity:
		return anyTag(e, "auth", "pii", "money")
	case model.PromptPerformance:
		return anyTag(e, "io_in_loop", "async") ||
			e.anyUnitValue(model.QKind, "perf") ||
			e.anyUnitValue(model.QEffect, "performance")
	case model.PromptTests:
		return e.anyUnitValue(model.QKind, "test") || kindIs(e, "test") ||
			(!e.f.LabelsMissing && e.anyYes(model.QTestWeakened) == True)
	}
	return false
}

func anyTag(e *groupEnv, subs ...string) bool {
	for _, s := range subs {
		if e.tag(s) == True {
			return true
		}
	}
	return false
}

func kindIs(e *groupEnv, k string) bool {
	v := e.kind()
	return v.Kind == KindStr && v.S == k
}

// AutoContext is the stage-6 context of a group on its own (no rule's
// context): what a review the user forces deep gets when no rule attached
// any.
func AutoContext(f GroupFacts) []string { return autoContext(newGroupEnv(f), nil) }

// autoContext is the rule's context ∪ the stage-6 decisions, in canonical
// order: callers when any unit is breaking, types when any unit's contract
// is a signature or api shape change, tests when any unit is untested and
// risky, config when any unit's contract is env/flag.
func autoContext(e *groupEnv, ruleCtx []string) []string {
	want := map[string]bool{}
	for _, c := range ruleCtx {
		want[c] = true
	}
	if !e.f.LabelsMissing && e.anyYes(model.QBreaking) == True {
		want[model.CtxCallers] = true
	}
	if hasContract(e, "signature", "api shape") {
		want[model.CtxTypes] = true
	}
	for _, a := range e.f.Aggs {
		if a.Untested {
			want[model.CtxTests] = true
		}
	}
	if hasContract(e, "env/flag") {
		want[model.CtxConfig] = true
	}
	var out []string
	for _, c := range ctxOrder {
		if want[c] {
			out = append(out, c)
		}
	}
	return out
}

// hasContract reads the contract label, falling back to UnitAgg.Contract.
func hasContract(e *groupEnv, vals ...string) bool {
	if e.anyUnitValue(model.QContract, vals...) {
		return true
	}
	for _, a := range e.f.Aggs {
		if slices.Contains(vals, a.Contract) {
			return true
		}
	}
	return false
}
