package diffview

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/diff"
)

// MaxIntraBytes is the combined length of a paired -/+ line above which
// intra-line highlighting is skipped (minified files).
const MaxIntraBytes = maxIntraBytes

// Clean makes diff text safe to draw: tabs become four spaces and control
// characters become '?' (shared with the Code page so both viewers sanitise
// the same way).
func Clean(s string) string { return clean(s) }

// Clip cuts s to at most limit bytes on a rune boundary and reports whether
// it cut, so rendering cost does not grow with line length.
func Clip(s string, limit int) (string, bool) { return clip(s, limit) }

// WriteSegments writes text into b split at spans: unchanged parts in base,
// changed parts in emph, each cleaned. Spans past the end of text are ignored.
func WriteSegments(b *strings.Builder, text string, spans []diff.Span, base, emph lipgloss.Style) {
	writeSegments(b, text, spans, base, emph)
}

// IntraSpans returns the changed byte spans of a paired old/new line, or nil
// for both when the pair is longer than MaxIntraBytes.
func IntraSpans(oldText, newText string) (oldSpans, newSpans []diff.Span) {
	if len(oldText)+len(newText) > maxIntraBytes {
		return nil, nil
	}
	return diff.IntraLine(oldText, newText)
}
