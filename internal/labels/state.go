package labels

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
)

// State is the JSON object sent as jev.Request.State. Field order is fixed
// by the struct and every optional part is omitted unless a question in the
// request declares the matching input, so identical Input yields identical
// bytes.
type State struct {
	Unit UnitState `json:"unit"`
	// PR is attached for inputs pr_title / pr_bullets.
	PR *PRState `json:"pr,omitempty"`
	// Reviewer is attached for inputs role / owned_globs.
	Reviewer *ReviewerState `json:"reviewer,omitempty"`
	// TestsTouched is attached (possibly empty) for input changed_tests:
	// the changed test files of the item, sorted.
	TestsTouched *[]string `json:"tests_touched,omitempty"`
	// Lines are the added lines a line-scope request asks about.
	Lines []LineState `json:"lines,omitempty"`
}

// UnitState describes the change unit.
type UnitState struct {
	Path      string `json:"path"`
	Symbol    string `json:"symbol,omitempty"`
	Signature string `json:"signature,omitempty"`
	// Change is the git-level kind: added, deleted, modified or moved.
	Change string `json:"change"`
	// Diff is the unit's unified diff lines (+/-/space prefixes), possibly
	// truncated to fit the token limits (head and tail kept, elision
	// marker line in between).
	Diff string `json:"diff"`
}

// PRState is the item's title and description bullets.
type PRState struct {
	Title   string        `json:"title,omitempty"`
	Bullets []BulletState `json:"bullets,omitempty"`
}

// BulletState is one description bullet; ID is model.BulletValue(i), the
// option name used by desc_bullet.
type BulletState struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// ReviewerState is the user's role and owned path globs.
type ReviewerState struct {
	Role string   `json:"role,omitempty"`
	Owns []string `json:"owns,omitempty"`
}

// LineState is one added line of a line-scope request.
type LineState struct {
	// Slot names the line in wire question names ("l01".."l20").
	Slot string `json:"slot"`
	// Line is the new-side line number.
	Line int    `json:"line"`
	Text string `json:"text"`
}

// maxLineBytes caps a single line's text in line requests (minified code).
const maxLineBytes = 2000

// elisionMarker is the line that replaces the middle of a truncated diff.
func elisionMarker(n int) string { return fmt.Sprintf("… %d diff lines elided …", n) }

// baseState builds the state for unit u carrying the inputs declared by qs.
func baseState(u model.ChangeUnit, c Context, qs []questions.Question) State {
	s := State{Unit: UnitState{
		Path: u.Path, Symbol: u.Symbol, Signature: u.Signature,
		Change: string(u.Kind), Diff: u.Diff,
	}}
	has := func(name string) bool {
		return slices.ContainsFunc(qs, func(q questions.Question) bool { return q.HasInput(name) })
	}
	if has(questions.InputPRTitle) || has(questions.InputPRBullets) {
		pr := PRState{}
		if has(questions.InputPRTitle) {
			pr.Title = c.Title
		}
		if has(questions.InputPRBullets) {
			for i, b := range c.Bullets {
				pr.Bullets = append(pr.Bullets, BulletState{ID: model.BulletValue(i + 1), Text: b})
			}
		}
		s.PR = &pr
	}
	if has(questions.InputRole) || has(questions.InputOwnedGlobs) {
		r := ReviewerState{}
		if has(questions.InputRole) {
			r.Role = c.Role
		}
		if has(questions.InputOwnedGlobs) {
			r.Owns = append([]string(nil), c.Owns...)
		}
		s.Reviewer = &r
	}
	if has(questions.InputChangedTests) {
		tests := append(make([]string, 0, len(c.TestsTouched)), c.TestsTouched...)
		slices.Sort(tests)
		s.TestsTouched = &tests
	}
	return s
}

// truncateDiff keeps keep of the diff's lines: the first ceil(keep/2) and
// the last floor(keep/2), with an elision marker line between them. keep >=
// the line count returns the diff unchanged.
func truncateDiff(lines []string, keep int) string {
	if keep >= len(lines) {
		return strings.Join(lines, "\n")
	}
	head := (keep + 1) / 2
	tail := keep / 2
	out := make([]string, 0, keep+1)
	out = append(out, lines[:head]...)
	out = append(out, elisionMarker(len(lines)-keep))
	out = append(out, lines[len(lines)-tail:]...)
	return strings.Join(out, "\n")
}

// addedLine is one added line of a unit.
type addedLine struct {
	line int // new-side line number (sent to Jev)
	pos  int // 1-based position among the unit's added lines (Label.Line)
	text string
}

// addedLines returns the unit's added lines with new-side line numbers,
// counting from Range.New.Start over "+" and " " lines, and positions.
func addedLines(u model.ChangeUnit) []addedLine {
	if u.Diff == "" {
		return nil
	}
	var out []addedLine
	n := u.Range.New.Start
	for _, ln := range strings.Split(u.Diff, "\n") {
		switch {
		case strings.HasPrefix(ln, "+"):
			out = append(out, addedLine{line: n, pos: len(out) + 1, text: capLine(ln[1:])})
			n++
		case strings.HasPrefix(ln, " "):
			n++
		}
	}
	return out
}

// capLine truncates s to maxLineBytes on a rune boundary.
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

// slotName is the wire slot of the i-th (0-based) line of a batch, zero
// padded to the width of the batch size (at least two digits).
func slotName(i, batch int) string {
	width := max(2, len(fmt.Sprint(batch)))
	return fmt.Sprintf("l%0*d", width, i+1)
}
