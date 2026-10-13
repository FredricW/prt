package pipeview

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/model"
)

const ellipsis = "…"

// prob formats a probability the spec's way (".86").
func prob(p float64) string { return codeview.ProbText(p) }

// money formats a USD amount: two decimals when that is exact or the amount
// is at least $0.10 ("$0.18", "$0.40"), else four ("$0.0123"); "$0" for 0.
func money(v float64) string {
	switch {
	case v == 0:
		return "$0"
	case v >= 0.1 || math.Abs(v*100-math.Round(v*100)) < 1e-9:
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("$%.4f", v)
}

// dur formats a wall time in milliseconds: "340 ms", "12.4 s", "2m05s".
func dur(ms int64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%d ms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.1f s", float64(ms)/1000)
	}
	s := ms / 1000
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

// estimate formats an estimated duration: "~48 s", "~3.2 s", "~2m05s".
func estimate(ms int64) string {
	switch {
	case ms < 10_000:
		return fmt.Sprintf("~%.1f s", float64(ms)/1000)
	case ms < 60_000:
		return fmt.Sprintf("~%.0f s", float64(ms)/1000)
	}
	return "~" + dur(ms)
}

// clock formats a timestamp in its own zone ("10-13 14:03:12"); "" when
// zero.
func clock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("01-02 15:04:05")
}

// plural returns "1 row" / "3 rows".
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// trunc cuts s to w cells with an ellipsis.
func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, ellipsis)
}

// padRight truncates s to w cells and pads it with spaces to exactly w.
func padRight(s string, w int) string {
	s = trunc(s, w)
	if n := w - ansi.StringWidth(s); n > 0 {
		s += strings.Repeat(" ", n)
	}
	return s
}

// padLeft truncates s to w cells and right-aligns it in w cells.
func padLeft(s string, w int) string {
	s = trunc(s, w)
	if n := w - ansi.StringWidth(s); n > 0 {
		s = strings.Repeat(" ", n) + s
	}
	return s
}

// wrap wraps s (which may hold ANSI styles) to w cells; continuation lines
// are indented by indent spaces.
func wrap(s string, w, indent int) []string {
	if w-indent < 8 || ansi.StringWidth(s) <= w {
		return []string{s}
	}
	parts := strings.Split(ansi.Wrap(s, w-indent, " "), "\n")
	pad := strings.Repeat(" ", indent)
	for i := 1; i < len(parts); i++ {
		parts[i] = pad + strings.TrimLeft(parts[i], " ")
	}
	return parts
}

// ctxShort abbreviates a context kind for the routing table.
var ctxShort = map[string]string{
	model.CtxCallers: "callers", model.CtxTypes: "types", model.CtxTests: "tests",
	model.CtxConfig: "config", model.CtxCodeUnderTest: "cut",
}

// ctxList renders a context list ("callers,tests"); "—" when empty.
func ctxList(ctx []string) string {
	if len(ctx) == 0 {
		return "—"
	}
	out := make([]string, len(ctx))
	for i, c := range ctx {
		if s, ok := ctxShort[c]; ok {
			out[i] = s
		} else {
			out[i] = c
		}
	}
	return strings.Join(out, ",")
}

// countPair is a name and a count for "name N" lists.
type countPair struct {
	name string
	n    int
}

// byCount sorts a count map by count (largest first), then name, and drops
// zero counts.
func byCount(m map[string]int) []countPair {
	out := make([]countPair, 0, len(m))
	for k, v := range m {
		if v > 0 {
			out = append(out, countPair{k, v})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return out[i].name < out[j].name
	})
	return out
}

// joinCounts renders pairs as "a 3 · b 1"; empty when none.
func joinCounts(ps []countPair) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = fmt.Sprintf("%s %d", p.name, p.n)
	}
	return strings.Join(parts, " · ")
}

// cellWidths splits total cells by share: floors, then the remainder to the
// largest fractional parts; every positive share gets at least one cell
// (the Overview composition bar's rule).
func cellWidths(shares []int, total int) []int {
	sum := 0
	for _, s := range shares {
		sum += s
	}
	out := make([]int, len(shares))
	if sum <= 0 || total <= 0 {
		return out
	}
	used := 0
	frac := make([]float64, len(shares))
	for i, s := range shares {
		exact := float64(s) / float64(sum) * float64(total)
		out[i] = int(exact)
		frac[i] = exact - float64(out[i])
		used += out[i]
	}
	idx := make([]int, len(shares))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return frac[idx[a]] > frac[idx[b]] })
	for k := 0; used < total; k = (k + 1) % len(idx) {
		if shares[idx[k]] > 0 { // a positive share exists, so this ends
			out[idx[k]]++
			used++
		}
	}
	for i := range out {
		if shares[i] == 0 || out[i] > 0 {
			continue
		}
		widest := 0
		for j := range out {
			if out[j] > out[widest] {
				widest = j
			}
		}
		if out[widest] > 1 {
			out[widest]--
			out[i] = 1
		}
	}
	return out
}
