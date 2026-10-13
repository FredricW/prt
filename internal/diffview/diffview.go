// Package diffview is the plain unified diff viewer used by the Code tab:
// a Bubble Tea component (not a tea.Model; the parent forwards messages)
// that renders lines lazily, so only the visible window is styled and a
// 5,000-line PR stays responsive (spec: "Bubble Tea structure").
//
// SetDiff parses the diff once into a compact index of logical rows (file
// headers, hunk headers, diff lines, "no newline" markers). View styles only
// rows[offset:offset+height], computes intra-line highlights for paired lines
// on demand (spec: "Diff rendering") and caches rendered rows per width.
package diffview

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/diff"
)

// wheelStep is how many rows one mouse wheel notch scrolls.
const wheelStep = 3

// Model is the diff viewport. The zero value is not usable; call New.
type Model struct {
	r      *lipgloss.Renderer
	st     styles
	files  []diff.File
	meta   []fileMeta
	raw    []string // fallback lines when the diff cannot be parsed
	rows   []row
	heads  []int // row index of every file header, ascending
	gutter int   // digits of the widest line number
	width  int
	height int
	offset int
	cache  *renderCache
}

// New returns an empty viewer. r may be nil (default renderer); snapshots pass
// a renderer bound to io.Discard so output has no colour.
func New(r *lipgloss.Renderer) Model {
	if r == nil {
		r = lipgloss.DefaultRenderer()
	}
	return Model{r: r, st: newStyles(r), width: 80, height: 20, cache: newRenderCache()}
}

// SetDiff replaces the content with a raw `git diff` text. It indexes the
// rows in O(n) and renders nothing. Parse errors, or text that holds no
// `diff --git` sections, fall back to showing the raw text line by line.
func (m *Model) SetDiff(raw string) {
	m.files, m.meta, m.raw, m.rows, m.heads, m.gutter = nil, nil, nil, nil, nil, 0
	m.offset = 0
	m.cache = newRenderCache()
	if raw == "" {
		return
	}
	files, err := diff.Parse(raw)
	if err != nil || len(files) == 0 {
		m.raw = strings.Split(strings.TrimRight(raw, "\n"), "\n")
		m.rows = rawRows(len(m.raw))
		return
	}
	m.files = files
	idx := buildIndex(files)
	m.meta, m.rows, m.heads, m.gutter = idx.meta, idx.rows, idx.heads, idx.gutter
}

// SetSize sets the viewport size in cells. A width change drops the
// rendered-row cache; intra-line spans are kept.
func (m *Model) SetSize(width, height int) {
	width, height = max(1, width), max(1, height)
	if width != m.width && m.cache != nil {
		m.cache.resetLines()
	}
	m.width, m.height = width, height
	m.offset = clamp(m.offset, 0, m.maxOffset())
}

// Files returns the parsed files (for headers, counts, open-on-GitHub).
func (m Model) Files() []diff.File { return m.files }

// RowCount returns the number of rendered rows (file headers, hunk headers, lines).
func (m Model) RowCount() int { return len(m.rows) }

// Offset returns the index of the top visible row.
func (m Model) Offset() int { return m.offset }

// CurrentFile returns the path of the file owning the top visible row, or ""
// when there is no parsed diff.
func (m Model) CurrentFile() string {
	if m.offset >= len(m.rows) || m.rows[m.offset].kind == kindRaw {
		return ""
	}
	return m.files[m.rows[m.offset].file].Path()
}

// Line returns the file path and new-side line number of the top visible row,
// for opening the file in $EDITOR or on GitHub. File headers give line 0,
// hunk headers the hunk's first new line, and deleted lines the nearest
// preceding new-side line in the hunk (or the hunk start). path is "" when
// there is no parsed diff.
func (m Model) Line() (path string, newLine int) {
	if m.offset >= len(m.rows) || m.rows[m.offset].kind == kindRaw {
		return "", 0
	}
	rw := m.rows[m.offset]
	f := m.files[rw.file]
	switch rw.kind {
	case kindHunk:
		return f.Path(), f.Hunks[rw.hunk].NewStart
	case kindLine, kindNoEOL:
		return f.Path(), newSideLine(f.Hunks[rw.hunk], int(rw.line))
	default:
		return f.Path(), 0
	}
}

// newSideLine maps line li of h to a new-side line number.
func newSideLine(h diff.Hunk, li int) int {
	for i := li; i >= 0; i-- {
		if n := h.Lines[i].NewNo; n > 0 {
			return n
		}
	}
	return max(1, h.NewStart)
}

// ScrollBy moves the view by n rows (negative is up), clamped. Scrolling down
// never pulls back a top row placed past the normal bottom by NextFile.
func (m *Model) ScrollBy(n int) {
	limit := max(m.maxOffset(), m.offset)
	m.offset = clamp(m.offset+n, 0, limit)
}

// GotoTop scrolls to the first row.
func (m *Model) GotoTop() { m.offset = 0 }

// GotoBottom scrolls so the last row is visible.
func (m *Model) GotoBottom() { m.offset = m.maxOffset() }

// NextFile scrolls so the next file header is the top row. The last file's
// header may end up on top with empty space below it.
func (m *Model) NextFile() {
	i := sort.SearchInts(m.heads, m.offset+1)
	if i < len(m.heads) {
		m.offset = m.heads[i]
	}
}

// PrevFile scrolls so the previous file header (or, when inside a file below
// its header, that file's header) is the top row.
func (m *Model) PrevFile() {
	i := sort.SearchInts(m.heads, m.offset) - 1
	if i >= 0 {
		m.offset = m.heads[i]
	}
}

func (m *Model) maxOffset() int { return max(0, len(m.rows)-m.height) }

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

// Update handles navigation keys (j/k, down/up, ctrl+d/ctrl+u half page,
// pgdown/pgup/space full page, g/G, home/end, ]/[ for next/previous file)
// and mouse wheel events (3 rows per notch). Other messages are ignored.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		m.handleKey(msg.String())
	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.ScrollBy(-wheelStep)
		case tea.MouseButtonWheelDown:
			m.ScrollBy(wheelStep)
		}
	}
	return m, nil
}

func (m *Model) handleKey(key string) {
	half := max(1, m.height/2)
	switch key {
	case "j", "down":
		m.ScrollBy(1)
	case "k", "up":
		m.ScrollBy(-1)
	case "ctrl+d":
		m.ScrollBy(half)
	case "ctrl+u":
		m.ScrollBy(-half)
	case "pgdown", " ":
		m.ScrollBy(m.height)
	case "pgup":
		m.ScrollBy(-m.height)
	case "g", "home":
		m.GotoTop()
	case "G", "end":
		m.GotoBottom()
	case "]":
		m.NextFile()
	case "[":
		m.PrevFile()
	}
}

// View renders exactly the visible window: height lines joined by "\n", each
// at most width cells. Only rows[offset:offset+height] are styled.
func (m Model) View() string {
	lines := make([]string, m.height)
	if len(m.rows) == 0 {
		lines[0] = m.st.empty.Render(fit("(no diff yet)", m.width))
		return strings.Join(lines, "\n")
	}
	for i := range lines {
		ri := m.offset + i
		if ri >= len(m.rows) {
			break
		}
		lines[i] = m.renderRow(ri)
	}
	return strings.Join(lines, "\n")
}
