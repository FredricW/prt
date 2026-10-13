package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
)

// facts are what the heuristic reads from a request's state: every string
// in it (paths, diff, bullets, ...) joined by newlines, and the diff lines.
type facts struct {
	text     string // lower-cased
	added    []string
	removed  []string
	depFile  bool
	testFile bool
}

var depFiles = []string{"go.mod", "go.sum", "package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml"}

func readFacts(state any) facts {
	state = normalise(state)
	var parts []string
	collectStrings(state, &parts)
	raw := strings.Join(parts, "\n")
	f := facts{text: strings.ToLower(raw)}
	for _, ln := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(ln, "+++"), strings.HasPrefix(ln, "---"):
		case strings.HasPrefix(ln, "+"):
			f.added = append(f.added, ln[1:])
		case strings.HasPrefix(ln, "-"):
			f.removed = append(f.removed, ln[1:])
		}
	}
	// Judge the file by the unit's path when the state names one; else by
	// the whole text (less precise: it may include other context).
	where := strings.ToLower(findPath(state))
	if where == "" {
		where = f.text
	}
	for _, d := range depFiles {
		if strings.Contains(where, d) {
			f.depFile = true
		}
	}
	for _, t := range []string{"_test.go", ".test.ts", ".test.tsx", ".spec.ts", "__tests__/", "/test_"} {
		if strings.Contains(where, t) {
			f.testFile = true
		}
	}
	return f
}

// normalise turns any state (string, map or struct) into its JSON value
// form (string, []any, map[string]any), as the real endpoint sees it.
func normalise(state any) any {
	b, err := json.Marshal(state)
	if err != nil {
		return state
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return state
	}
	return v
}

// findPath returns the first string value under a "path" or "file" key in
// a state object (maps in key order), or "" if there is none. A string
// state is searched for a "path:" or "file:" line.
func findPath(v any) string {
	switch x := v.(type) {
	case string:
		for _, ln := range strings.Split(x, "\n") {
			l := strings.ToLower(strings.TrimSpace(ln))
			for _, p := range []string{"path:", "file:"} {
				if strings.HasPrefix(l, p) {
					return strings.TrimSpace(ln[strings.Index(ln, ":")+1:])
				}
			}
		}
	case map[string]any:
		for _, k := range sortedKeys(x) {
			if s, ok := x[k].(string); ok && (strings.EqualFold(k, "path") || strings.EqualFold(k, "file")) {
				return s
			}
		}
		for _, k := range sortedKeys(x) {
			if p := findPath(x[k]); p != "" {
				return p
			}
		}
	case map[string]string:
		for _, k := range []string{"path", "Path", "file", "File"} {
			if s, ok := x[k]; ok {
				return s
			}
		}
	}
	return ""
}

// collectStrings appends every string found in v (maps in key order).
func collectStrings(v any, out *[]string) {
	switch x := v.(type) {
	case string:
		*out = append(*out, x)
	case []any:
		for _, e := range x {
			collectStrings(e, out)
		}
	case []string:
		*out = append(*out, x...)
	case map[string]any:
		for _, k := range sortedKeys(x) {
			collectStrings(x[k], out)
		}
	case map[string]string:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			*out = append(*out, x[k])
		}
	default:
		// Structs and other values: render them; good enough for substring
		// heuristics.
		if v != nil {
			*out = append(*out, fmt.Sprint(v))
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// pureDeletion is true when the state's diff removes lines and adds none.
func (f facts) pureDeletion() bool { return len(f.added) == 0 && len(f.removed) > 0 }

// rename is true for the fixture's fetchJSON → requestJSON rename (and any
// change whose added and removed lines differ only by that identifier).
func (f facts) rename() bool {
	return strings.Contains(f.text, "fetchjson") && strings.Contains(f.text, "requestjson")
}

var getUserSig = regexp.MustCompile(`getUser\s*\([^)]*,`)

// signatureChange is true when an added line declares getUser with an extra
// parameter (the fixture's breaking change in #101).
func (f facts) signatureChange() bool {
	for _, ln := range f.added {
		if getUserSig.MatchString(ln) && (strings.Contains(ln, "function") || strings.Contains(ln, "export")) {
			return true
		}
	}
	return false
}

func (f facts) kind() string {
	switch {
	case f.depFile:
		return model.KindDependency
	case f.pureDeletion(), f.rename():
		return "refactor"
	case f.testFile:
		return "test"
	}
	return "feature"
}

// heuristicAnswer answers plausibly from the request state so synthetic
// recordings satisfy the replay test's expectations (breaking getUser in
// #101, refactor in #102 and #103, dependency in #104). Question names are
// matched by label id ("kind", "risk_tags.auth", ...), ignoring any prefix
// before a "/" or ":".
func heuristicAnswer(name string, q jev.Question, state any) jev.Answer {
	f := readFacts(state)
	id := questionID(name)
	switch q.Type {
	case jev.Boolean:
		return boolAnswer(booleanP(id, f))
	case jev.Choice:
		return choiceAnswer(choiceOptions(q.Criteria), pickChoice(id, q, f))
	case jev.Score:
		return scoreAnswer(scoreRungs(q.Criteria), scoreRung(id, f))
	}
	return jev.Answer{Type: q.Type}
}

func questionID(name string) string {
	if i := strings.LastIndexAny(name, "/:"); i >= 0 {
		name = name[i+1:]
	}
	return name
}

func booleanP(id string, f facts) float64 {
	switch {
	case id == model.QBreaking:
		if f.signatureChange() {
			return 0.9
		}
		return 0.05
	case id == model.QBehaviour:
		if f.kind() == "feature" {
			return 0.8
		}
		return 0.1
	case id == model.QReversible:
		return 0.9
	case id == model.QCovered:
		return 0.3
	case id == model.QFmt:
		return 0.05
	case strings.HasPrefix(id, model.QRiskTags+"."):
		return 0.05
	}
	return 0.1
}

func pickChoice(id string, q jev.Question, f facts) string {
	switch id {
	case model.QKind:
		return f.kind()
	case model.QContract:
		if f.signatureChange() {
			return "signature"
		}
		return model.ContractNone
	case model.QDepth:
		switch {
		case f.signatureChange():
			return model.DepthRead
		case f.depFile, f.pureDeletion(), f.rename():
			return model.DepthSkim
		}
		return model.DepthRead
	case model.QEffect:
		return "none"
	case model.QFailMode:
		return "wrong data"
	case model.QDescBullet:
		return bulletFor(q, f)
	}
	return model.BulletNone
}

var backticked = regexp.MustCompile("`([^`]+)`")

// bulletFor picks the first description bullet that names (in backticks) an
// identifier appearing in an added line, else "none".
func bulletFor(q jev.Question, f facts) string {
	crit := criteriaMap(q.Criteria)
	opts := choiceOptions(q.Criteria)
	added := strings.ToLower(strings.Join(f.added, "\n"))
	for _, o := range opts {
		if model.BulletIndex(o) == 0 {
			continue
		}
		for _, m := range backticked.FindAllStringSubmatch(crit[o], -1) {
			ident := strings.ToLower(strings.TrimSuffix(m[1], "()"))
			if len(ident) > 3 && strings.Contains(added, ident) {
				return o
			}
		}
	}
	return model.BulletNone
}

// scoreRung returns the 0-based rung: risk 2 by default, 1 for deletions,
// dependency bumps and renames; relevance 2; others (suspicious) 1.
func scoreRung(id string, f facts) int {
	switch id {
	case model.QRisk:
		if f.pureDeletion() || f.depFile || f.rename() {
			return 0
		}
		return 1
	case model.QRelevance:
		return 1
	}
	return 0
}

func boolAnswer(p float64) jev.Answer { return jev.Answer{Type: jev.Boolean, Probability: &p} }

// choiceAnswer answers want (or, if want is not an option, the first
// option) with p 0.85 and spreads the rest evenly.
func choiceAnswer(opts []string, want string) jev.Answer {
	if len(opts) == 0 {
		return jev.Answer{Type: jev.Choice}
	}
	chosen := opts[0]
	for _, o := range opts {
		if o == want {
			chosen = o
		}
	}
	probs := make(map[string]float64, len(opts))
	for _, o := range opts {
		probs[o] = 0.15 / float64(max(1, len(opts)-1))
	}
	probs[chosen] = 0.85
	if len(opts) == 1 {
		probs[chosen] = 1
	}
	return jev.Answer{Type: jev.Choice, Choice: chosen, Probabilities: probs}
}

// scoreAnswer puts p 0.8 on rung (0-based) and the rest on its neighbours.
func scoreAnswer(rungs, rung int) jev.Answer {
	rungs = max(rungs, 2)
	rung = min(max(rung, 0), rungs-1)
	probs := map[string]float64{strconv.Itoa(rung): 0.8}
	rest := 0.2
	switch {
	case rung > 0 && rung < rungs-1:
		probs[strconv.Itoa(rung-1)], probs[strconv.Itoa(rung+1)] = rest/2, rest/2
	case rung > 0:
		probs[strconv.Itoa(rung-1)] = rest
	default:
		probs[strconv.Itoa(rung+1)] = rest
	}
	s := 0.0
	for k, p := range probs {
		n, _ := strconv.Atoi(k)
		s += float64(n) * p
	}
	return jev.Answer{Type: jev.Score, Score: &s, Probabilities: probs}
}

// choiceOptions returns choice option names, sorted.
func choiceOptions(criteria any) []string {
	m := criteriaMap(criteria)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// criteriaMap returns a choice criteria record as option → description.
func criteriaMap(criteria any) map[string]string {
	switch c := criteria.(type) {
	case map[string]string:
		return c
	case map[string]any:
		out := make(map[string]string, len(c))
		for k, v := range c {
			out[k] = fmt.Sprint(v)
		}
		return out
	}
	return nil
}

// scoreRungs returns the number of rungs of a score question (5 if unknown).
func scoreRungs(criteria any) int {
	switch c := criteria.(type) {
	case []string:
		return len(c)
	case []any:
		return len(c)
	}
	return 5
}
