package review

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanTLDR(t *testing.T) {
	long := strings.Repeat("word ", 40) + "end."
	multibyte := strings.Repeat("ændring ", 30) + "slutt."
	cases := []struct {
		name, in, want string
	}{
		{"plain", "Adds request options to getUser and breaks two callers.", "Adds request options to getUser and breaks two callers."},
		{"lead-in and quotes", `TL;DR: "Adds options to getUser."`, "Adds options to getUser."},
		{"markdown", "**Breaking:** `getUser` now needs [options](http://x). More text.", "Breaking: getUser now needs options."},
		{"fenced and list", "```\n- Refactors billing export.\n```", "Refactors billing export."},
		{"smart quotes", "“Mostly formatting with one risky auth change.”", "Mostly formatting with one risky auth change."},
		{"second sentence dropped", "First sentence here. Second one.", "First sentence here."},
		{"exactly 160", strings.Repeat("a", 159) + ".", strings.Repeat("a", 159) + "."},
	}
	for _, c := range cases {
		if got := CleanTLDR(c.in); got != c.want {
			t.Errorf("%s: CleanTLDR = %q, want %q", c.name, got, c.want)
		}
	}
	for name, in := range map[string]string{"ascii": long, "multibyte": multibyte, "no spaces": strings.Repeat("é", 400)} {
		got := CleanTLDR(in)
		n := utf8.RuneCountInString(got)
		if n > MaxTLDR || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
			t.Errorf("%s: %d runes, %q", name, n, got)
		}
		if name != "no spaces" && strings.HasSuffix(strings.TrimSuffix(got, "…"), " ") {
			t.Errorf("%s: not cut at a word boundary: %q", name, got)
		}
		if name != "no spaces" && strings.Contains(got, "ændrin…") {
			t.Errorf("%s: cut inside a word: %q", name, got)
		}
	}
}

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"Validate upload size":                   "Validate upload size",
		`"Profile cache invalidation."`:          "Profile cache invalidation",
		"Title: **Refactor billing export** now": "Refactor billing export",
		"Don't break callers":                    "Dont break callers",
		"cache-invalidation fix":                 "cache invalidation fix",
		"\n\n```\nRetry   failed\njobs\n```":     "Retry failed",
		"Æøå endring på profil og mer":           "Æøå endring på",
		"":                                       "",
		"!!!":                                    "",
	}
	for in, want := range cases {
		if got := CleanTitle(in); got != want {
			t.Errorf("CleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
