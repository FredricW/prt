// Package route is pipeline stage 6: it evaluates prt.routing.toml rules top
// to bottom against a group's aggregate labels (first match wins), decides
// attached context (callers / types / tests / config), and respects deep_slots
// and the per-item cost cap.
//
// The `when` language is small: label ids, tag.<name>, kind, numbers,
// strings, `== != >= <= > <`, `&& || !`, parentheses, `in [a, b]`, true/false.
//
// TODO(M3): implement Parse (≈200-line recursive-descent parser) and Route.
package route

import (
	"errors"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

// ErrNotImplemented marks the placeholder parser.
var ErrNotImplemented = errors.New("route: not implemented")

// Env is what a rule expression is evaluated against for one group.
type Env interface {
	// Bool returns a boolean label (e.g. "breaking", "mechanical", "tag.auth").
	Bool(name string) (bool, bool)
	// Num returns a numeric label (e.g. "risk").
	Num(name string) (float64, bool)
	// Str returns a string label (e.g. "kind").
	Str(name string) (string, bool)
}

// Expr is a compiled rule expression.
type Expr interface {
	Eval(env Env) (bool, error)
	String() string // the source text, shown verbatim on the Pipeline page
}

// Parse compiles a `when` expression. TODO(M3).
func Parse(src string) (Expr, error) {
	if src == "true" {
		return constExpr{src: src, v: true}, nil
	}
	return nil, ErrNotImplemented
}

type constExpr struct {
	src string
	v   bool
}

func (c constExpr) Eval(Env) (bool, error) { return c.v, nil }
func (c constExpr) String() string         { return c.src }

// Decision is the routing outcome for one group.
type Decision struct {
	GroupID string
	Tier    model.Tier
	Rule    string // the `when` text that fired
	Prompts []string
	Context []string
}

// Route decides tiers for groups. TODO(M3): placeholder sends everything to fast.
func Route(groups []model.Group, _ config.Routing, _ func(model.Group) Env) []Decision {
	out := make([]Decision, 0, len(groups))
	for _, g := range groups {
		out = append(out, Decision{GroupID: g.ID, Tier: model.TierFast, Rule: "true"})
	}
	return out
}
