package judge

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
)

// unitState describes one change unit in a request state. It has the same
// shape as internal/labels' unit state so Jev sees units the same way in
// every request.
type unitState struct {
	Path      string `json:"path"`
	Symbol    string `json:"symbol,omitempty"`
	Signature string `json:"signature,omitempty"`
	// Change is the git-level kind: added, deleted, modified or moved.
	Change string `json:"change"`
	// Diff is the unit's unified diff lines, each capped at maxLineBytes and
	// possibly cut to fit the token limits (head and tail kept, elision
	// marker line between).
	Diff string `json:"diff"`
}

// pairState is the state of a relation request; A is the unit with the
// smaller id. No embedding score is sent, so request keys stay stable.
type pairState struct {
	A unitState `json:"a"`
	B unitState `json:"b"`
}

// nameState is the state of a name_bullet request. The bullets travel as the
// question's options, not in the state.
type nameState struct {
	PR    titleState     `json:"pr"`
	Group groupUnitState `json:"group"`
}

type titleState struct {
	Title string `json:"title"`
}

type groupUnitState struct {
	Units []unitState `json:"units"`
}

// splitState is the state of a separate_pr request.
type splitState struct {
	File   fileState    `json:"file"`
	Groups []groupState `json:"groups"`
}

type fileState struct {
	Path  string      `json:"path"`
	Units []unitState `json:"units"`
}

// groupState is one row of the group list (GroupSummary without the id,
// which is a hash and means nothing to Jev).
type groupState struct {
	Name  string   `json:"name"`
	Files []string `json:"files"`
	Units int      `json:"units"`
}

// groupStates converts summaries to their wire form, paths sorted.
func groupStates(groups []GroupSummary) []groupState {
	out := make([]groupState, 0, len(groups))
	for _, g := range groups {
		files := append(make([]string, 0, len(g.Paths)), g.Paths...)
		slices.Sort(files)
		out = append(out, groupState{Name: g.Name, Files: files, Units: g.Units})
	}
	return out
}

// maxLineBytes caps a single diff line (minified code).
const maxLineBytes = 2000

// capLine truncates s to maxLineBytes on a rune boundary, ending in "…".
func capLine(s string) string {
	if len(s) <= maxLineBytes {
		return s
	}
	cut := maxLineBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// elisionMarker replaces the middle of a cut diff.
func elisionMarker(n int) string { return fmt.Sprintf("… %d diff lines elided …", n) }

// cutDiff keeps keep of lines: the first ceil(keep/2), the marker, the last
// floor(keep/2). keep < 0 or keep >= len(lines) keeps everything.
func cutDiff(lines []string, keep int) string {
	if keep < 0 || keep >= len(lines) {
		return strings.Join(lines, "\n")
	}
	head, tail := (keep+1)/2, keep/2
	out := make([]string, 0, keep+1)
	out = append(out, lines[:head]...)
	out = append(out, elisionMarker(len(lines)-keep))
	out = append(out, lines[len(lines)-tail:]...)
	return strings.Join(out, "\n")
}

// diffUnit is a unit with its diff split into capped lines.
type diffUnit struct {
	u     model.ChangeUnit
	lines []string
}

// prepare splits and caps the diffs of units.
func prepare(units ...model.ChangeUnit) []diffUnit {
	out := make([]diffUnit, 0, len(units))
	for _, u := range units {
		lines := strings.Split(u.Diff, "\n")
		for i, l := range lines {
			lines[i] = capLine(l)
		}
		out = append(out, diffUnit{u: u, lines: lines})
	}
	return out
}

// state returns the unit state with the diff cut to at most keep lines
// (keep < 0: whole diff).
func (d diffUnit) state(keep int) unitState {
	return unitState{
		Path: d.u.Path, Symbol: d.u.Symbol, Signature: d.u.Signature,
		Change: string(d.u.Kind), Diff: cutDiff(d.lines, keep),
	}
}

// states returns the states of ds with every diff cut to keep lines.
func states(ds []diffUnit, keep int) []unitState {
	out := make([]unitState, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.state(keep))
	}
	return out
}

// maxLines is the longest diff of ds, in lines.
func maxLines(ds []diffUnit) int {
	n := 0
	for _, d := range ds {
		n = max(n, len(d.lines))
	}
	return n
}

// limits are the token limits a request must respect.
type limits struct {
	statePlusQuestion int
	request           int
}

// jevLimits are the Jev endpoint's limits.
func jevLimits() limits {
	return limits{statePlusQuestion: jev.MaxStatePlusQuestion, request: jev.MaxRequestTokens}
}

// fitRequest builds the request for binds with the state build(keep). It
// first tries the whole diffs; when over a limit it binary-searches the
// largest per-diff line cap keep that fits, the same cap for every diff of
// the state (so the longest diffs are cut first and short ones stay whole).
func fitRequest(build func(keep int) any, longest int, binds []binding, lim limits) (jev.Request, error) {
	qTokens, err := longestQuestion(binds)
	if err != nil {
		return jev.Request{}, err
	}
	try := func(keep int) (jev.Request, bool, error) {
		st := build(keep)
		sb, err := json.Marshal(st)
		if err != nil {
			return jev.Request{}, false, fmt.Errorf("encode state: %w", err)
		}
		if jev.EstimateTokensLen(len(sb))+qTokens > lim.statePlusQuestion {
			return jev.Request{}, false, nil
		}
		req := newRequest(st, binds)
		rb, err := json.Marshal(req)
		if err != nil {
			return jev.Request{}, false, fmt.Errorf("encode request: %w", err)
		}
		return req, jev.EstimateTokensLen(len(rb)) <= lim.request, nil
	}
	req, ok, err := try(-1)
	if err != nil || ok {
		return req, err
	}
	lo, hi := -1, longest
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		_, ok, err := try(mid)
		if err != nil {
			return jev.Request{}, err
		}
		if ok {
			lo = mid
		} else {
			hi = mid
		}
	}
	if lo < 0 {
		return jev.Request{}, fmt.Errorf("request does not fit the %d-token state+question / %d-token request limits even with empty diffs",
			lim.statePlusQuestion, lim.request)
	}
	req, _, err = try(lo)
	return req, err
}

// longestQuestion is the token estimate of the largest `"name":{question}`
// entry of binds.
func longestQuestion(binds []binding) (int, error) {
	longest := 0
	for _, b := range binds {
		q, err := json.Marshal(b.wq.Q)
		if err != nil {
			return 0, fmt.Errorf("encode question %s: %w", b.name, err)
		}
		longest = max(longest, jev.EstimateTokensLen(len(b.name)+3+len(q)))
	}
	return longest, nil
}

// newRequest builds the request carrying binds against st.
func newRequest(st any, binds []binding) jev.Request {
	qs := make(map[string]jev.Question, len(binds))
	for _, b := range binds {
		qs[b.name] = b.wq.Q
	}
	return jev.Request{Model: jev.Model, State: st, Questions: qs}
}
