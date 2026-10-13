package review

import (
	"regexp"
	"strings"
	"unicode"
)

// abbreviations end in a dot without ending a sentence (lower-cased,
// without the final dot).
var abbreviations = map[string]bool{"e.g": true, "i.e": true, "etc": true, "vs": true, "cf": true, "approx": true}

// firstSentence collapses whitespace and returns s up to and including its
// first sentence terminator (".", "!" or "?" followed by a space or the
// end), skipping common abbreviations and dots inside words ("foo.Bar",
// "v1.2"). The whole string when no terminator is found.
func firstSentence(s string) string {
	s = oneLine(s)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '.' && c != '!' && c != '?' {
			continue
		}
		if i+1 < len(s) && s[i+1] != ' ' {
			continue
		}
		if c == '.' && abbreviations[strings.ToLower(lastWord(s[:i]))] {
			continue
		}
		return s[:i+1]
	}
	return s
}

// lastWord returns the text after the last space of s.
func lastWord(s string) string {
	if i := strings.LastIndexByte(s, ' '); i >= 0 {
		return s[i+1:]
	}
	return s
}

var (
	mdLink    = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	leadIn    = regexp.MustCompile(`(?i)^(tl;?dr|summary|title)\s*[:\-–—]\s*`)
	listMark  = regexp.MustCompile(`^(#{1,6}\s+|[-*+>]\s+|\d+[.)]\s+)`)
	quoteRune = strings.NewReplacer(`"`, "", "“", "", "”", "", "„", "", "«", "", "»", "")
	mdMarks   = strings.NewReplacer("**", "", "__", "", "`", "", "*", "")
)

// stripMarkdown removes code fences, emphasis, links, list and heading
// markers, double quotes and a "TL;DR:"/"Summary:"/"Title:" lead-in, and
// joins the remaining lines with spaces.
func stripMarkdown(text string) string {
	var parts []string
	for _, ln := range strings.Split(text, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			continue
		}
		t = listMark.ReplaceAllString(t, "")
		parts = append(parts, t)
	}
	s := strings.Join(parts, " ")
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdMarks.Replace(s)
	s = quoteRune.Replace(s)
	s = oneLine(s)
	s = leadIn.ReplaceAllString(s, "")
	return strings.Trim(s, "'‘’ ")
}

// CleanTLDR returns one plain sentence of at most MaxTLDR characters
// (runes): markdown, quotes and a "TL;DR:" lead-in are stripped, only the
// first sentence is kept, and a longer sentence is cut at a word boundary
// and ends with "…".
func CleanTLDR(text string) string {
	s := firstSentence(stripMarkdown(text))
	r := []rune(s)
	if len(r) <= MaxTLDR {
		return s
	}
	cut := r[:MaxTLDR-1]
	if i := lastSpace(cut); i > MaxTLDR/2 {
		cut = cut[:i]
	}
	out := strings.TrimRightFunc(string(cut), func(c rune) bool {
		return unicode.IsSpace(c) || unicode.IsPunct(c)
	})
	return out + "…"
}

// lastSpace returns the index of the last space rune in r, -1 if none.
func lastSpace(r []rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if unicode.IsSpace(r[i]) {
			return i
		}
	}
	return -1
}

// CleanPRTitle returns a suggested PR title: the first non-empty line of
// text with markdown, quotes and a "Title:" lead-in stripped, at most
// MaxPRTitle runes (a longer line is cut at a word boundary when one is in
// the second half), without trailing periods, commas, colons or dashes.
func CleanPRTitle(text string) string {
	line := ""
	for _, ln := range strings.Split(text, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			continue
		}
		if line = stripMarkdown(t); line != "" {
			break
		}
	}
	if r := []rune(line); len(r) > MaxPRTitle {
		cut := r[:MaxPRTitle]
		if i := lastSpace(r[:MaxPRTitle+1]); i > MaxPRTitle/2 {
			cut = r[:i]
		}
		line = string(cut)
	}
	return strings.TrimRight(line, " .,;:-–—")
}

// CleanTitle returns a title of at most three words, without punctuation
// or quotes: the first non-empty line of text with markdown and a
// "Title:" lead-in stripped, apostrophes dropped and every other
// non-letter, non-digit rune treated as a word break.
func CleanTitle(text string) string {
	line := ""
	for _, ln := range strings.Split(text, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			continue
		}
		line = stripMarkdown(t)
		if line != "" {
			break
		}
	}
	line = strings.NewReplacer("'", "", "’", "", "‘", "").Replace(line)
	words := strings.FieldsFunc(line, func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) })
	if len(words) > 3 {
		words = words[:3]
	}
	return strings.Join(words, " ")
}
