package codeview

import (
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/diffview"
	"github.com/fredricw/prt/internal/model"
)

// blockAtCursor returns the block of kind under the cursor: the block whose
// first row the cursor is on, else the first such block drawn under the
// cursor's diff line; nil when none (or the cursor is out of view).
func (m *Model) blockAtCursor(kind blockKind) *cblock {
	if !m.cursorVisible() {
		return nil
	}
	if m.cur.block != "" {
		if bi := m.c.blockIndex(m.cur.block); bi >= 0 && m.c.blocks[bi].kind == kind {
			return &m.c.blocks[bi]
		}
		return nil
	}
	for _, bi := range m.c.blocksAt(m.cur.unit, m.cur.line) {
		if m.c.blocks[bi].kind == kind {
			return &m.c.blocks[bi]
		}
	}
	return nil
}

// acceptKey implements a: accept the ai-draft under the cursor. Without
// one the key is not consumed, so the root's approve (a) gets it.
func (m *Model) acceptKey() bool {
	b := m.blockAtCursor(blockAI)
	if b == nil {
		return false
	}
	m.pendingComment = &CommentOp{Kind: CommentOpAccept, CommentID: b.c.ID}
	m.note = "AI finding accepted: it becomes your draft"
	return true
}

// dismissKey implements d: dismiss the ai-draft under the cursor (a note
// when there is none).
func (m *Model) dismissKey() {
	b := m.blockAtCursor(blockAI)
	if b == nil {
		m.note = "dismiss: no AI finding under the cursor"
		return
	}
	m.pendingComment = &CommentOp{Kind: CommentOpDismiss, CommentID: b.c.ID}
	m.note = "AI finding dismissed"
}

// overrideKey implements !: on an ai-draft, dismiss it and mark one of its
// unit's labels wrong; elsewhere, mark a label of the cursor's unit wrong
// (spec Keybindings "mark a label wrong"). Both open the label picker.
func (m *Model) overrideKey() {
	if b := m.blockAtCursor(blockAI); b != nil {
		unit := b.unit
		if unit == "" {
			unit = b.c.UnitID
		}
		m.openPicker(unit, b.c.ID)
		return
	}
	id := m.cursorUnitID()
	if id == "" {
		m.note = "mark a label wrong: no unit here"
		return
	}
	m.openPicker(id, "")
}

// threadKey implements t: open the side panel on the thread under the
// cursor.
func (m *Model) threadKey() {
	b := m.blockAtCursor(blockThread)
	if b == nil {
		m.note = "thread: no GitHub thread under the cursor"
		return
	}
	m.openPanel(b)
}

// labelPicker is the `!` picker: first one of the unit's unit-scope labels,
// then its corrected value. aiID is the ai-draft dismissed with the
// override ("" for a plain override). loading is set while the unit's
// labels are being fetched (M5.2): the picker then says "loading labels…"
// and only esc cancels.
type labelPicker struct {
	unit    string
	aiID    string
	labels  []model.Label
	chosen  int      // index into labels; -1 while picking the label
	values  []string // corrected values of the chosen label
	sel     int
	loading bool
}

// openPicker opens the label picker on unit id. When the unit has no label
// that can be corrected it shows a note; started on an ai-draft (aiID), it
// still dismisses the finding, without an override. Without the unit's
// labels on the page (M5.2) it opens waiting for them.
func (m *Model) openPicker(id, aiID string) {
	m.picker = &labelPicker{unit: id, aiID: aiID, chosen: -1, loading: true}
	if m.wantLabels(true, id) {
		m.fillPicker()
	}
	if m.picker != nil {
		m.vis, m.visRow = nil, -1
	}
}

// fillPicker fills the open picker from the page's labels of its unit, or
// closes it with a note when none can be corrected.
func (m *Model) fillPicker() {
	pk := m.picker
	ls := pickableLabels(m.c.labels, pk.unit)
	if len(ls) == 0 {
		m.picker = nil
		if pk.aiID != "" {
			m.pendingComment = &CommentOp{Kind: CommentOpDismiss, CommentID: pk.aiID}
			m.note = "AI finding dismissed; no labels on its unit to mark wrong"
			return
		}
		m.note = "mark a label wrong: no labels on this unit to correct"
		return
	}
	pk.labels, pk.loading = ls, false
}

// pickableLabels are unit id's unit-scope labels (no line labels, no
// unknown answers) that have at least one other value, by question.
func pickableLabels(all []model.Label, id string) []model.Label {
	var out []model.Label
	for _, l := range all {
		if l.UnitID == id && l.Line == 0 && !model.IsUnknown(l) && len(correctedValues(l)) > 0 {
			out = append(out, l)
		}
	}
	slices.SortStableFunc(out, func(a, b model.Label) int { return strings.Compare(a.Question, b.Question) })
	return out
}

// currentValue is a label's value as it stands (the override when set).
func currentValue(l model.Label) string {
	if l.Overridden != nil {
		return *l.Overridden
	}
	return l.Value
}

// correctedValues lists the values a label can be corrected to, without
// its current one: 1–5 for a score, yes/no for a boolean, the options of
// Probs for a choice (none when the options are unknown).
func correctedValues(l model.Label) []string {
	var opts []string
	switch {
	case isScore(l):
		opts = []string{"1", "2", "3", "4", "5"}
	case len(l.Probs) > 0:
		for k := range l.Probs {
			opts = append(opts, k)
		}
		slices.Sort(opts)
	case l.Value == model.LabelYes || l.Value == model.LabelNo:
		opts = []string{model.LabelYes, model.LabelNo}
	}
	cur := currentValue(l)
	return slices.DeleteFunc(opts, func(v string) bool { return v == cur })
}

// isScore reports whether a label answers a score question: an
// interpolated Score, or Probs keyed by the rungs "1".."5".
func isScore(l model.Label) bool {
	if l.Score > 0 {
		return true
	}
	if len(l.Probs) == 0 {
		return false
	}
	for k := range l.Probs {
		if n, err := strconv.Atoi(k); err != nil || n < 1 || n > 5 {
			return false
		}
	}
	return true
}

// pickerKey handles a key while the picker is open: j/k move, StripJump
// (enter) picks, Close cancels; ctrl+c falls through, the rest is
// swallowed.
func (m *Model) pickerKey(k tea.KeyMsg) bool {
	kk, pk := m.keys, m.picker
	n := len(pk.labels)
	if pk.chosen >= 0 {
		n = len(pk.values)
	}
	switch {
	case key.Matches(k, kk.Close):
		m.picker = nil
		m.note = "mark a label wrong: cancelled"
	case k.String() == "ctrl+c":
		return false
	case pk.loading:
		// Waiting for the unit's labels: nothing to move or pick yet.
	case key.Matches(k, kk.Down):
		pk.sel = min(pk.sel+1, n-1)
	case key.Matches(k, kk.Up):
		pk.sel = max(pk.sel-1, 0)
	case key.Matches(k, kk.StripJump):
		m.pick()
	}
	return true
}

// pick takes the selected row: a label moves on to its values, a value
// queues the override (with the dismissal when started on an ai-draft).
func (m *Model) pick() {
	pk := m.picker
	if pk.chosen < 0 {
		pk.chosen, pk.sel = pk.sel, 0
		pk.values = correctedValues(pk.labels[pk.chosen])
		return
	}
	l := pk.labels[pk.chosen]
	ov := &model.Override{UnitID: pk.unit, Question: l.Question, Value: pk.values[pk.sel]}
	m.picker = nil
	what := " (" + l.Question + " → " + ov.Value + ")"
	if pk.aiID != "" {
		m.pendingComment = &CommentOp{Kind: CommentOpDismiss, CommentID: pk.aiID, Override: ov}
		m.note = "AI finding dismissed; " + noteOverrideFiled + what
		return
	}
	m.pendingComment = &CommentOp{Kind: CommentOpOverride, Override: ov}
	m.note = noteOverrideFiled + what
}

// noteOverrideFiled is the note after `!` queued a label override (the
// daemon re-runs aggregation on it; spec M5 "`!` overrides feeding
// re-aggregation").
const noteOverrideFiled = "override filed — re-aggregating"

// pickerView draws the picker over the pane: its title and rows.
func (m Model) pickerView(lay layout, out []string) (string, string, []string) {
	pk := m.picker
	title := "mark a label wrong — " + m.c.unitLabel(pk.unit)
	if pk.aiID != "" {
		title = "dismiss AI finding + mark a label wrong — " + m.c.unitLabel(pk.unit)
	}
	title += " · " + m.keys.StripJump.Help().Key + " picks · " + m.keys.Close.Help().Key + " cancels"
	rows := m.pickerRows()
	for i := range rows[1:] {
		marker := "  "
		if i == pk.sel && !pk.loading {
			marker = "▸ "
		}
		rows[i+1] = marker + rows[i+1]
	}
	// Keep the selection in view below the prompt line.
	off := max(0, pk.sel+2-max(1, lay.viewH))
	if off > 0 {
		rows = append(rows[:1], rows[1+off:]...)
	}
	for i := 0; i < len(out) && i < len(rows); i++ {
		out[i] = fit(diffview.Clean(rows[i]), lay.paneW)
	}
	return diffview.Clean(title), "", out
}

// pickerRows are the picker's prompt line and its rows (without markers):
// the labels, the chosen label's values, or "loading labels…" while the
// unit's labels are fetched.
func (m Model) pickerRows() []string {
	pk := m.picker
	switch {
	case pk.loading:
		return []string{"which label is wrong?", loadingLabels}
	case pk.chosen < 0:
		rows := []string{"which label is wrong?"}
		for _, l := range pk.labels {
			rows = append(rows, l.Question+"  "+currentValue(l)+" "+ProbText(l.P))
		}
		return rows
	}
	l := pk.labels[pk.chosen]
	return append([]string{l.Question + ": " + currentValue(l) + " → corrected value?"}, pk.values...)
}
