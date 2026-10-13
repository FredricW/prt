package labels

import (
	"slices"
	"strconv"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
)

// InputHash returns the hash of the item-level inputs q declares, as
// baseState sends them: PR title, description bullets, reviewer role, owned
// globs and changed tests. It is "" when q declares none of them, i.e. its
// answer depends on the unit alone and may be reused by any item with the
// same unit. Labels carry it as model.Label.Ctx so a cached answer is only
// reused under the same inputs (DECISIONS "M1 review fixes").
func InputHash(q questions.Question, c Context) string {
	var parts []string
	add := func(name string, vals ...string) {
		if q.HasInput(name) {
			parts = append(parts, name, strconv.Itoa(len(vals)))
			parts = append(parts, vals...)
		}
	}
	add(questions.InputPRTitle, c.Title)
	add(questions.InputPRBullets, c.Bullets...)
	add(questions.InputRole, c.Role)
	add(questions.InputOwnedGlobs, c.Owns...)
	tests := slices.Clone(c.TestsTouched)
	slices.Sort(tests)
	add(questions.InputChangedTests, tests...)
	if len(parts) == 0 {
		return ""
	}
	return model.ContentHash(append([]string{"label-inputs"}, parts...)...)[:16]
}

// InputHashes maps the label id (questions.Question.LabelIDs) of every live
// unit- and line-scope question with item-level inputs to its InputHash.
// Label ids missing from the map have no item-level inputs.
func InputHashes(cat questions.Catalogue, c Context) map[string]string {
	out := map[string]string{}
	for _, s := range []questions.Scope{questions.ScopeUnit, questions.ScopeLine} {
		for _, q := range cat.Live(s) {
			h := InputHash(q, c)
			if h == "" {
				continue
			}
			for _, id := range q.LabelIDs() {
				out[id] = h
			}
		}
	}
	return out
}
