package questions

import (
	"maps"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
)

// WireSep separates the parts of a wire name: "<id>__<sub>" for "N ×k"
// sub-questions, and callers append "__<slot>" for per-line questions
// (e.g. "fmt__l03", "risk_tags__auth"). Ids and sub-ids never contain it.
const WireSep = "__"

// noneOption is the extra choice of dynamic bullet questions.
const noneOption = model.BulletNone

// defaultNoneDescription describes noneOption when the catalogue does not.
const defaultNoneDescription = "implements none of the listed bullets"

// Dynamic carries the per-request data that dynamic options are built from.
type Dynamic struct {
	// Bullets are the PR description bullets (model.DescBullets), offered
	// as model.BulletValue(i) (1-based).
	Bullets []string
}

// WireQuestion is one named question as sent to Jev, plus what is needed to
// map its answer back to a model.Label.
type WireQuestion struct {
	// Name is the key in jev.Request.Questions: a safe JSON key built from
	// [a-z0-9_] parts joined by WireSep.
	Name string
	// Label is the model label question id: the catalogue id, or
	// model.SubQuestion(id, sub) for "N ×k" rows.
	Label string
	// Type is the catalogue type of the answer.
	Type Type
	// Options are the valid answers: choice values (Value is one of them)
	// or score rungs (lowest→highest). Empty for booleans.
	Options []string
	// Q is the wire question.
	Q jev.Question
}

// Expand returns the wire questions for q, in a stable order: one for a
// plain question, one per sub-question for "N ×k" rows. Dynamic choices take
// their options from dyn; with no bullets the only option is
// model.BulletNone (callers answer such a question without asking Jev, see
// Answerable).
func (q Question) Expand(dyn Dynamic) []WireQuestion {
	switch q.Type {
	case Choice:
		return []WireQuestion{q.wireChoice(dyn)}
	case Score:
		rungs := append([]string(nil), q.Options...)
		return []WireQuestion{{
			Name: q.ID, Label: q.ID, Type: Score, Options: rungs,
			Q: jev.Question{Type: jev.Score, Instructions: q.Prompt, Criteria: rungs},
		}}
	}
	if !q.IsMulti() {
		wq := WireQuestion{Name: q.ID, Label: q.ID, Type: Noul,
			Q: jev.Question{Type: jev.Boolean, Instructions: q.Prompt}}
		if len(q.Criteria) > 0 {
			wq.Q.Criteria = maps.Clone(q.Criteria)
		}
		return []WireQuestion{wq}
	}
	ids, descs := q.Subs()
	out := make([]WireQuestion, 0, len(ids))
	for i, sub := range ids {
		out = append(out, WireQuestion{
			Name:  q.ID + WireSep + sub,
			Label: model.SubQuestion(q.ID, sub),
			Type:  Noul,
			Q: jev.Question{
				Type:         jev.Boolean,
				Instructions: q.Prompt + "\n" + sub + ": " + descs[i],
			},
		})
	}
	return out
}

func (q Question) wireChoice(dyn Dynamic) WireQuestion {
	crit := map[string]string{}
	var opts []string
	if q.Dynamic == DynamicBullets {
		for i, b := range dyn.Bullets {
			v := model.BulletValue(i + 1)
			opts = append(opts, v)
			crit[v] = b
		}
		none := q.Criteria[noneOption]
		if none == "" {
			none = defaultNoneDescription
		}
		opts = append(opts, noneOption)
		crit[noneOption] = none
	} else {
		opts = append(opts, q.Options...)
		for _, o := range q.Options {
			d := q.Criteria[o]
			if d == "" {
				d = o
			}
			crit[o] = d
		}
	}
	return WireQuestion{
		Name: q.ID, Label: q.ID, Type: Choice, Options: opts,
		Q: jev.Question{Type: jev.Choice, Instructions: q.Prompt, Criteria: crit},
	}
}

// LabelIDs returns the model label question ids q produces: its id, or
// model.SubQuestion(id, sub) per sub-question of an "N ×k" row.
func (q Question) LabelIDs() []string {
	if !q.IsMulti() {
		return []string{q.ID}
	}
	ids, _ := q.Subs()
	out := make([]string, len(ids))
	for i, sub := range ids {
		out[i] = model.SubQuestion(q.ID, sub)
	}
	return out
}

// Answerable reports whether w needs Jev at all: a choice with a single
// option (a dynamic bullet question when the PR has no bullets) is decided
// without asking.
func (w WireQuestion) Answerable() bool { return w.Type != Choice || len(w.Options) >= 2 }
