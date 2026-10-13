package aggregate

import (
	"path"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// unwiredUnits returns the units that cannot implement a description bullet
// on their own (M2 fidelity rule): pure-added code (no deleted lines) with a
// symbol, outside test files, that no calls or imports link from a non-test
// unit of the item points to. A new helper nothing calls does not implement
// a behaviour. It returns nil when links is nil (stage 2 did not run), which
// keeps the M1 rule: every claiming unit counts.
func unwiredUnits(units []model.ChangeUnit, links []model.Link) map[string]bool {
	if links == nil {
		return nil
	}
	byID := make(map[string]model.ChangeUnit, len(units))
	for _, u := range units {
		byID[u.ID] = u
	}
	wired := map[string]bool{}
	for _, l := range links {
		if l.Kind != model.LinkCalls && l.Kind != model.LinkImports {
			continue
		}
		from, ok := byID[l.From]
		if ok && l.From != l.To && !isTestPath(from.Path) {
			wired[l.To] = true
		}
	}
	out := map[string]bool{}
	for _, u := range units {
		if !wired[u.ID] && u.Dels == 0 && strings.TrimSpace(u.Symbol) != "" && !isTestPath(u.Path) {
			out[u.ID] = true
		}
	}
	return out
}

// isTestPath reports whether p is a test file: *_test.go, test_*.py,
// *_test.py, *.test.* / *.spec.* for ts/tsx/js/jsx/mjs/cjs, or any path
// under __tests__, tests or test (copy of the pipeline's rule).
func isTestPath(p string) bool {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasSuffix(base, ".py") && (strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")):
		return true
	}
	ext := path.Ext(base)
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		stem := strings.TrimSuffix(base, ext)
		if strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") {
			return true
		}
	}
	for _, dir := range strings.Split(path.Dir(p), "/") {
		if dir == "__tests__" || dir == "tests" || dir == "test" {
			return true
		}
	}
	return false
}
