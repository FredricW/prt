// Package questions defines the Jev question catalogue (spec: "Jev question
// catalogue") loaded from prt.questions.toml, with a built-in default
// (default.toml, every row of the spec table).
//
// Rules enforced at load (Parse): known scope; type N, C or S; slug ids
// unique per scope; every question declares what it feeds (feeds = [] is
// rejected); choice and score questions have at least two fixed options
// unless their options are dynamic; "N ×k" sub-ids are unique slugs; inputs
// are known names; criteria only describe existing options.
//
// Expand turns a catalogue question into the named jev.Questions sent on the
// wire (one per sub-question for "N ×k" rows, dynamic options filled in).
package questions

import (
	_ "embed"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/fredricw/prt/internal/jev"
)

// Scope is what a question is asked about.
type Scope string

// Scopes.
const (
	ScopeLine   Scope = "line"
	ScopeUnit   Scope = "unit"
	ScopeFile   Scope = "file"
	ScopeGroup  Scope = "group"
	ScopePair   Scope = "pair"
	ScopeItem   Scope = "item"
	ScopeBranch Scope = "branch"
)

// Scopes lists every valid scope in spec order.
var Scopes = []Scope{ScopeLine, ScopeUnit, ScopeFile, ScopeGroup, ScopePair, ScopeItem, ScopeBranch}

// Type is the spec's question type letter.
type Type string

// Types: N (Noul → boolean), C (Choice), S (Score).
const (
	Noul   Type = "N"
	Choice Type = "C"
	Score  Type = "S"
)

// DynamicBullets is the only dynamic option source: the PR description
// bullets (model.DescBullets) offered as model.BulletValue(i), plus
// model.BulletNone.
const DynamicBullets = "pr_bullets"

// Input names a question may declare in `inputs` (state attached beyond the
// unit itself). Only some are produced in M1; the rest name inputs of
// deferred questions.
const (
	InputPRTitle        = "pr_title"
	InputPRBullets      = "pr_bullets"
	InputPRBody         = "pr_body"
	InputCallers        = "callers"
	InputChangedTests   = "changed_tests"
	InputRole           = "role"
	InputOwnedGlobs     = "owned_globs"
	InputNeighbours     = "neighbours"
	InputHelpers        = "helpers"
	InputLinks          = "links"
	InputGroupList      = "group_list"
	InputBothUnits      = "both_units"
	InputAggregates     = "aggregates"
	InputHeadlines      = "headlines"
	InputFlagLabels     = "flag_labels"
	InputContractLabels = "contract_labels"
	InputTagCounts      = "tag_counts"
	InputCommentAndDiff = "comment_and_diff"
	InputBranchLog      = "branch_log"
	// Branch-state inputs (M5, judge.BranchState): days since the head
	// commit, commits not on the upstream, and open PR titles (≤ 30).
	InputBranchAge    = "branch_age"
	InputUnpushed     = "unpushed"
	InputOpenPRTitles = "open_pr_titles"
)

// KnownInputs is the set of valid `inputs` names.
var KnownInputs = []string{
	InputPRTitle, InputPRBullets, InputPRBody, InputCallers, InputChangedTests,
	InputRole, InputOwnedGlobs, InputNeighbours, InputHelpers, InputLinks,
	InputGroupList, InputBothUnits, InputAggregates, InputHeadlines,
	InputFlagLabels, InputContractLabels, InputTagCounts, InputCommentAndDiff,
	InputBranchLog, InputBranchAge, InputUnpushed, InputOpenPRTitles,
}

// Question is one catalogue row.
//
// Options mean: for C, the choice values; for S, the rungs lowest→highest;
// for N, either none (one boolean) or the sub-questions of an "N ×k" row,
// each written "<sub-id>: <description>" (labels are "<id>.<sub-id>", see
// model.SubQuestion).
type Question struct {
	Scope   Scope    `toml:"scope"`
	ID      string   `toml:"id"`
	Type    Type     `toml:"type"`
	Prompt  string   `toml:"prompt"`
	Options []string `toml:"options"`
	// Criteria optionally describes options: choice option → description,
	// or "true"/"false" → description for a single boolean. Undescribed
	// choice options are sent with the option text as description.
	Criteria map[string]string `toml:"criteria"`
	// Dynamic, when set (only DynamicBullets), means the choice options are
	// built per request; Options must then be empty.
	Dynamic string   `toml:"dynamic"`
	Inputs  []string `toml:"inputs"`
	Feeds   []string `toml:"feeds"`
	// Deferred names the milestone whose consumer is not built yet (e.g.
	// "M2"). Deferred questions are defined and validated but never sent
	// (see Live). Empty means live; user-added questions are live.
	Deferred string `toml:"deferred"`
}

// IsMulti reports whether q is an "N ×k" row (one boolean per option).
func (q Question) IsMulti() bool { return q.Type == Noul && len(q.Options) > 0 }

// Subs returns the sub-ids and descriptions of an "N ×k" row, in order.
func (q Question) Subs() (ids, descs []string) {
	for _, o := range q.Options {
		id, desc, _ := strings.Cut(o, ":")
		ids = append(ids, strings.TrimSpace(id))
		descs = append(descs, strings.TrimSpace(desc))
	}
	return ids, descs
}

// HasInput reports whether q declares the named input.
func (q Question) HasInput(name string) bool { return slices.Contains(q.Inputs, name) }

// Catalogue is the full set of questions.
type Catalogue struct {
	Questions []Question `toml:"question"`
	Source    string     `toml:"-"`
}

// ByScope returns the questions of one scope, in catalogue order.
func (c Catalogue) ByScope(s Scope) []Question {
	var out []Question
	for _, q := range c.Questions {
		if q.Scope == s {
			out = append(out, q)
		}
	}
	return out
}

// Live returns the non-deferred questions of one scope, in catalogue order.
func (c Catalogue) Live(s Scope) []Question {
	var out []Question
	for _, q := range c.ByScope(s) {
		if q.Deferred == "" {
			out = append(out, q)
		}
	}
	return out
}

//go:embed default.toml
var defaultTOML []byte

// Default returns the built-in catalogue.
func Default() Catalogue {
	c, err := Parse(defaultTOML, "builtin")
	if err != nil {
		panic(fmt.Sprintf("questions: built-in catalogue invalid: %v", err))
	}
	return c
}

// Parse parses and validates a catalogue. nil data means the built-in one.
func Parse(data []byte, source string) (Catalogue, error) {
	if data == nil {
		return Default(), nil
	}
	var c Catalogue
	md, err := toml.Decode(string(data), &c)
	if err != nil {
		return Catalogue{}, fmt.Errorf("questions: %s: %w", source, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return Catalogue{}, fmt.Errorf("questions: %s: unknown key %s", source, und[0])
	}
	seen := map[string]bool{}
	for i, q := range c.Questions {
		key := string(q.Scope) + "/" + q.ID
		if err := validate(q); err != nil {
			return Catalogue{}, fmt.Errorf("questions: %s: question %d (%s): %w", source, i+1, key, err)
		}
		if seen[key] {
			return Catalogue{}, fmt.Errorf("questions: %s: duplicate id %s", source, key)
		}
		seen[key] = true
	}
	c.Source = source
	return c, nil
}

// Load parses a user prt.questions.toml and merges it over the built-in
// catalogue (Merge): users add questions, or redefine or defer (`deferred =
// "off"`) a built-in one by repeating its scope and id. nil data means the
// built-in catalogue.
func Load(data []byte, source string) (Catalogue, error) {
	if data == nil {
		return Default(), nil
	}
	user, err := Parse(data, source)
	if err != nil {
		return Catalogue{}, err
	}
	return Merge(Default(), user), nil
}

// Merge returns base with every question of over applied: a question with
// the same (scope, id) replaces the base one in place, any other is appended
// in over's order. Source is over's.
func Merge(base, over Catalogue) Catalogue {
	key := func(q Question) string { return string(q.Scope) + "/" + q.ID }
	idx := map[string]int{}
	out := Catalogue{Questions: append([]Question(nil), base.Questions...), Source: over.Source}
	for i, q := range out.Questions {
		idx[key(q)] = i
	}
	for _, q := range over.Questions {
		if i, ok := idx[key(q)]; ok {
			out.Questions[i] = q
			continue
		}
		idx[key(q)] = len(out.Questions)
		out.Questions = append(out.Questions, q)
	}
	return out
}

var slugRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// validSlug reports whether s can be part of a wire name: [a-z0-9_]+ without
// "__" (the wire separator) and without a leading or trailing "_".
func validSlug(s string) bool {
	return slugRe.MatchString(s) && !strings.Contains(s, "__") &&
		!strings.HasPrefix(s, "_") && !strings.HasSuffix(s, "_")
}

func validate(q Question) error {
	if q.ID == "" {
		return fmt.Errorf("question without id")
	}
	if !validSlug(q.ID) {
		return fmt.Errorf("id %q must match [a-z0-9_]+ without \"__\"", q.ID)
	}
	if !slices.Contains(Scopes, q.Scope) {
		return fmt.Errorf("unknown scope %q", q.Scope)
	}
	if strings.TrimSpace(q.Prompt) == "" {
		return fmt.Errorf("empty prompt")
	}
	if len(q.Feeds) == 0 {
		return fmt.Errorf("feeds nothing (feeds = [] is rejected)")
	}
	for _, in := range q.Inputs {
		if !slices.Contains(KnownInputs, in) {
			return fmt.Errorf("unknown input %q", in)
		}
	}
	switch q.Type {
	case Noul:
		return validateNoul(q)
	case Choice:
		return validateChoice(q)
	case Score:
		return validateScore(q)
	}
	return fmt.Errorf("bad type %q (want N, C or S)", q.Type)
}

func validateNoul(q Question) error {
	if q.Dynamic != "" {
		return fmt.Errorf("dynamic options are only valid for choice questions")
	}
	if !q.IsMulti() {
		for k := range q.Criteria {
			if k != "true" && k != "false" {
				return fmt.Errorf("boolean criteria key %q (want true/false)", k)
			}
		}
		return nil
	}
	if len(q.Criteria) > 0 {
		return fmt.Errorf("multi-boolean (N ×k) questions describe sub-questions in options, not criteria")
	}
	ids, descs := q.Subs()
	seen := map[string]bool{}
	for i, id := range ids {
		switch {
		case !validSlug(id):
			return fmt.Errorf("sub-id %q must match [a-z0-9_]+ without \"__\"", id)
		case descs[i] == "":
			return fmt.Errorf("sub-question %q needs \"<sub-id>: <description>\"", id)
		case seen[id]:
			return fmt.Errorf("duplicate sub-id %q", id)
		}
		seen[id] = true
	}
	return nil
}

func validateChoice(q Question) error {
	if q.Dynamic != "" {
		if q.Dynamic != DynamicBullets {
			return fmt.Errorf("unknown dynamic option source %q", q.Dynamic)
		}
		if len(q.Options) > 0 {
			return fmt.Errorf("dynamic choice must not list options")
		}
		for k := range q.Criteria {
			if k != noneOption {
				return fmt.Errorf("dynamic choice criteria may only describe %q", noneOption)
			}
		}
		return nil
	}
	if len(q.Options) < 2 {
		return fmt.Errorf("needs at least two options")
	}
	if err := uniqueNonEmpty(q.Options); err != nil {
		return err
	}
	for k := range q.Criteria {
		if !slices.Contains(q.Options, k) {
			return fmt.Errorf("criteria describe unknown option %q", k)
		}
	}
	return nil
}

func validateScore(q Question) error {
	if q.Dynamic != "" {
		return fmt.Errorf("dynamic options are only valid for choice questions")
	}
	if len(q.Options) < 2 {
		return fmt.Errorf("needs at least two options")
	}
	if len(q.Criteria) > 0 {
		return fmt.Errorf("score rungs are described in options, not criteria")
	}
	return uniqueNonEmpty(q.Options)
}

func uniqueNonEmpty(opts []string) error {
	seen := map[string]bool{}
	for _, o := range opts {
		if strings.TrimSpace(o) == "" {
			return fmt.Errorf("empty option")
		}
		if seen[o] {
			return fmt.Errorf("duplicate option %q", o)
		}
		seen[o] = true
	}
	return nil
}

// Wire converts a single-answer catalogue question to a jev.Question. For
// "N ×k" rows and dynamic choices use Expand, which yields every wire
// question; Wire then returns only the first.
func (q Question) Wire() jev.Question {
	ws := q.Expand(Dynamic{})
	if len(ws) == 0 {
		return jev.Question{Type: jev.Boolean, Instructions: q.Prompt}
	}
	return ws[0].Q
}
