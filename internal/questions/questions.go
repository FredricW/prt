// Package questions defines the Jev question catalogue (spec: "Jev question
// catalogue") loaded from prt.questions.toml, with a built-in default.
//
// Rules enforced at load: fixed option lists (choice/score need options),
// every question declares what it feeds (feeds = [] is rejected), ids are
// unique per scope.
//
// TODO(M1): ship the full built-in catalogue in default.toml (all rows of the
// spec table) and build jev.Question values from it.
package questions

import (
	_ "embed"
	"fmt"

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

// Type is the spec's question type letter.
type Type string

// Types: N (Noul → boolean), C (Choice), S (Score).
const (
	Noul   Type = "N"
	Choice Type = "C"
	Score  Type = "S"
)

// Question is one catalogue row.
type Question struct {
	Scope   Scope    `toml:"scope"`
	ID      string   `toml:"id"`
	Type    Type     `toml:"type"`
	Prompt  string   `toml:"prompt"`
	Options []string `toml:"options"` // choice options, or score rungs lowest→highest
	Inputs  []string `toml:"inputs"`
	Feeds   []string `toml:"feeds"`
}

// Wire converts a catalogue question to a jev.Question.
func (q Question) Wire() jev.Question {
	switch q.Type {
	case Choice:
		crit := map[string]string{}
		for _, o := range q.Options {
			crit[o] = o
		}
		return jev.Question{Type: jev.Choice, Instructions: q.Prompt, Criteria: crit}
	case Score:
		return jev.Question{Type: jev.Score, Instructions: q.Prompt, Criteria: append([]string(nil), q.Options...)}
	}
	return jev.Question{Type: jev.Boolean, Instructions: q.Prompt}
}

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
	if _, err := toml.Decode(string(data), &c); err != nil {
		return Catalogue{}, fmt.Errorf("questions: %s: %w", source, err)
	}
	seen := map[string]bool{}
	for _, q := range c.Questions {
		key := string(q.Scope) + "/" + q.ID
		switch {
		case q.ID == "":
			return Catalogue{}, fmt.Errorf("questions: %s: question without id", source)
		case seen[key]:
			return Catalogue{}, fmt.Errorf("questions: %s: duplicate id %s", source, key)
		case len(q.Feeds) == 0:
			return Catalogue{}, fmt.Errorf("questions: %s: %s feeds nothing (feeds = [] is rejected)", source, key)
		case q.Type != Noul && q.Type != Choice && q.Type != Score:
			return Catalogue{}, fmt.Errorf("questions: %s: %s has bad type %q", source, key, q.Type)
		case q.Type != Noul && len(q.Options) < 2:
			return Catalogue{}, fmt.Errorf("questions: %s: %s needs at least two options", source, key)
		}
		seen[key] = true
	}
	c.Source = source
	return c, nil
}
