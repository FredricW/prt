package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/pipeview"
)

// KeyMap holds every binding (spec: Keybindings and navigation). Fields
// are exported so callers can tweak them; KeyMapFromConfig applies the
// `[keys]` table of prt.toml.
type KeyMap struct {
	// Picker. Sort cycles the order within sections (urgency → age → size
	// → relevance); it shares s with the Pipeline page's SkipReview.
	Up, Down, Top, Bottom, Sort key.Binding
	// Focus.
	Focus, Back, NextFocus, PrevFocus key.Binding
	// Item actions (any focus).
	Refresh, Overview, Code, Open, Checkout key.Binding
	// Main pane scrolling (Code and Overview tabs).
	PageDown, PageUp, HalfPageDown, HalfPageUp, NextFile, PrevFile key.Binding
	// Code page (handed to codeview) and Overview (RawLabels).
	NextUnit, PrevUnit, Filters, Expand, RawLabels key.Binding
	// Code page: by file / by change toggle, merge and split groups. Mode
	// takes its keys away from the Code page's Top (the picker and the
	// Overview keep g as top).
	Mode, MergeGroups, SplitGroup key.Binding
	// Review pipeline (M3). Pipeline opens and closes the Pipeline page for
	// the selected item (P, any focus); Weights opens the Overview's urgency
	// weights overlay (w); ReReview re-reviews the Code page's group on the
	// deep tier (R).
	Pipeline, Weights, ReReview key.Binding
	// Pipeline page only (handed to pipeview): force deep (D), skip review
	// (s), re-run the selected stage (r; elsewhere r stays Refresh), edit
	// routing rules (e), call log (c; elsewhere c stays Checkout), raise
	// the cost cap ($).
	ForceDeep, SkipReview, RerunStage, EditRules, CallLog, RaiseCap key.Binding
	// Review (M4, item scope). Approve (a) and RequestChanges (A) open the
	// submit form with that verdict preselected; Submit (S) opens it. On a
	// branch without a PR, Submit and Approve open the create-PR form. The
	// Code page sees a first (Accept on an ai-draft).
	Approve, RequestChanges, Submit key.Binding
	// Code page comments layer (handed to codeview): comment on line (C),
	// line selection (V), save draft (ctrl+s), accept / dismiss an AI
	// finding (a / d), dismiss and override a label (!), thread panel (t),
	// resolve thread (ctrl+r).
	Comment, Visual, SaveDraft, Accept, Dismiss, OverrideLabel, Thread, Resolve key.Binding
	// Global.
	Help, Quit key.Binding
	// Confirm prompts.
	Yes, No key.Binding
}

// DefaultKeyMap returns the spec's default bindings.
func DefaultKeyMap() KeyMap {
	b := func(desc string, keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(helpKey(keys), desc))
	}
	return KeyMap{
		Up:             b("up", "k", "up"),
		Down:           b("down", "j", "down"),
		Top:            b("top", "g", "home"),
		Bottom:         b("bottom", "G", "end"),
		Sort:           b("cycle sort", "s"),
		Focus:          b("focus main pane", "enter"),
		Back:           b("back to picker", "esc"),
		NextFocus:      b("next pane", "tab"),
		PrevFocus:      b("previous pane", "shift+tab"),
		Refresh:        b("refresh item", "r"),
		Overview:       b("overview tab", "1"),
		Code:           b("code tab", "2"),
		Open:           b("open on GitHub", "o"),
		Checkout:       b("git checkout", "c"),
		PageDown:       b("page down", "pgdown", " "),
		PageUp:         b("page up", "pgup"),
		HalfPageDown:   b("half page down", "ctrl+d"),
		HalfPageUp:     b("half page up", "ctrl+u"),
		NextFile:       b("next file", "J", "]"),
		PrevFile:       b("previous file", "K", "["),
		NextUnit:       b("next unit", "n"),
		PrevUnit:       b("previous unit", "p"),
		Filters:        b("filters", "f"),
		Expand:         b("expand noise", "z"),
		RawLabels:      b("raw labels", "L"),
		Mode:           b("by file / by change", "g"),
		MergeGroups:    b("merge groups", "M"),
		SplitGroup:     b("split group", "X"),
		Pipeline:       b("pipeline page", "P"),
		Weights:        b("urgency weights", "w"),
		ReReview:       b("re-review on deep", "R"),
		ForceDeep:      b("force deep", "D"),
		SkipReview:     b("skip review", "s"),
		RerunStage:     b("re-run stage", "r"),
		EditRules:      b("edit rules", "e"),
		CallLog:        b("call log", "c"),
		RaiseCap:       b("raise cost cap", "$"),
		Approve:        b("approve", "a"),
		RequestChanges: b("request changes", "A"),
		Submit:         b("submit review", "S"),
		Comment:        b("comment on line", "C"),
		Visual:         b("select lines", "V"),
		SaveDraft:      b("save draft", "ctrl+s"),
		Accept:         b("accept AI finding", "a"),
		Dismiss:        b("dismiss AI finding", "d"),
		OverrideLabel:  b("dismiss + override label", "!"),
		Thread:         b("thread panel", "t"),
		Resolve:        b("resolve thread", "ctrl+r"),
		Help:           b("help", "?"),
		Quit:           b("quit", "q", "ctrl+c"),
		Yes:            b("yes", "y", "Y"),
		No:             b("no", "n", "N", "esc"),
	}
}

func helpKey(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// actions maps the config action names (prt.toml `[keys]`) to bindings.
func (k *KeyMap) actions() map[string]*key.Binding {
	return map[string]*key.Binding{
		"up": &k.Up, "down": &k.Down, "top": &k.Top, "bottom": &k.Bottom, "sort": &k.Sort,
		"focus": &k.Focus, "back": &k.Back, "next_focus": &k.NextFocus, "prev_focus": &k.PrevFocus,
		"refresh": &k.Refresh, "overview": &k.Overview, "code": &k.Code, "open": &k.Open,
		"checkout": &k.Checkout, "page_down": &k.PageDown, "page_up": &k.PageUp,
		"half_page_down": &k.HalfPageDown, "half_page_up": &k.HalfPageUp,
		"next_file": &k.NextFile, "prev_file": &k.PrevFile, "help": &k.Help, "quit": &k.Quit,
		"next_unit": &k.NextUnit, "prev_unit": &k.PrevUnit, "filters": &k.Filters,
		"expand": &k.Expand, "raw_labels": &k.RawLabels,
		"mode": &k.Mode, "merge_groups": &k.MergeGroups, "split_group": &k.SplitGroup,
		"pipeline": &k.Pipeline, "weights": &k.Weights, "rereview": &k.ReReview,
		"force_deep": &k.ForceDeep, "skip_review": &k.SkipReview, "rerun_stage": &k.RerunStage,
		"edit_rules": &k.EditRules, "call_log": &k.CallLog, "raise_cap": &k.RaiseCap,
		"approve": &k.Approve, "request_changes": &k.RequestChanges, "submit": &k.Submit,
		"comment": &k.Comment, "visual": &k.Visual, "save_draft": &k.SaveDraft, "accept": &k.Accept,
		"dismiss": &k.Dismiss, "override": &k.OverrideLabel, "thread": &k.Thread, "resolve": &k.Resolve,
	}
}

// codeKeys builds the codeview bindings from the key map, so the Code page
// follows prt.toml [keys]. The filter toggles keep codeview's defaults
// (1–9); they are only live while the filters bar captures keys, so they
// never clash with the tab keys. Close reuses Back (esc). Top loses the
// Mode keys (g toggles the mode on the Code page, spec keyboard table).
func (k KeyMap) codeKeys() codeview.Keys {
	ck := codeview.DefaultKeys()
	ck.Down, ck.Up, ck.Top, ck.Bottom = k.Down, k.Up, without(k.Top, k.Mode), k.Bottom
	ck.PageDown, ck.PageUp, ck.HalfPageDown, ck.HalfPageUp = k.PageDown, k.PageUp, k.HalfPageDown, k.HalfPageUp
	ck.NextUnit, ck.PrevUnit, ck.NextFile, ck.PrevFile = k.NextUnit, k.PrevUnit, k.NextFile, k.PrevFile
	ck.Filters, ck.Expand, ck.RawLabels, ck.Close = k.Filters, k.Expand, k.RawLabels, k.Back
	ck.Mode, ck.MergeGroups, ck.SplitGroup = k.Mode, k.MergeGroups, k.SplitGroup
	ck.ReReview = k.ReReview
	ck.Comment, ck.Visual, ck.SaveDraft = k.Comment, k.Visual, k.SaveDraft
	ck.Accept, ck.Dismiss, ck.OverrideLabel = k.Accept, k.Dismiss, k.OverrideLabel
	ck.Thread, ck.Resolve, ck.StripJump = k.Thread, k.Resolve, relabel(k.Focus, "jump to comment")
	return ck
}

// pipeKeys builds the Pipeline page bindings from the key map: movement
// keys are shared, J/K (next/previous file) move between panels, enter
// (Focus) opens a stage log and esc (Back) closes it.
func (k KeyMap) pipeKeys() pipeview.Keys {
	pk := pipeview.DefaultKeys()
	pk.Down, pk.Up, pk.Top, pk.Bottom = k.Down, k.Up, k.Top, k.Bottom
	pk.NextPanel, pk.PrevPanel = relabel(k.NextFile, "next panel"), relabel(k.PrevFile, "previous panel")
	pk.Enter, pk.Close = relabel(k.Focus, "stage log"), relabel(k.Back, "close")
	pk.ForceDeep, pk.Skip, pk.Rerun = k.ForceDeep, k.SkipReview, k.RerunStage
	pk.EditRules, pk.CallLog, pk.RaiseCap = k.EditRules, k.CallLog, k.RaiseCap
	return pk
}

// relabel returns b's keys with another help description.
func relabel(b key.Binding, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(b.Keys()...), key.WithHelp(b.Help().Key, desc))
}

// without returns b minus the keys of other; the help key is the first
// remaining key. With no key left the binding is disabled.
func without(b, other key.Binding) key.Binding {
	drop := map[string]bool{}
	for _, s := range other.Keys() {
		drop[s] = true
	}
	var keep []string
	for _, s := range b.Keys() {
		if !drop[s] {
			keep = append(keep, s)
		}
	}
	if len(keep) == 0 {
		return key.NewBinding(key.WithDisabled(), key.WithHelp("", b.Help().Desc))
	}
	return key.NewBinding(key.WithKeys(keep...), key.WithHelp(keep[0], b.Help().Desc))
}

// jump is the Overview's enter binding: the Focus keys, described as a jump.
func (k KeyMap) jump() key.Binding {
	return key.NewBinding(key.WithKeys(k.Focus.Keys()...), key.WithHelp(k.Focus.Help().Key, "jump to unit"))
}

// KeyActions lists the action names accepted by KeyMapFromConfig, sorted.
func KeyActions() []string {
	var k KeyMap
	names := make([]string, 0, len(k.actions()))
	for n := range k.actions() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// KeyMapFromConfig returns the default key map with the given overrides
// applied. Keys map an action name (see KeyActions) to one key or a
// comma-separated list ("j,down"); the first key is shown in help. Unknown
// actions and empty key lists are errors.
func KeyMapFromConfig(overrides map[string]string) (KeyMap, error) {
	km := DefaultKeyMap()
	acts := km.actions()
	names := make([]string, 0, len(overrides))
	for n := range overrides {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		b, ok := acts[name]
		if !ok {
			return DefaultKeyMap(), fmt.Errorf("tui: unknown key action %q (known: %s)", name, strings.Join(KeyActions(), ", "))
		}
		keys := splitKeys(overrides[name])
		if len(keys) == 0 {
			return DefaultKeyMap(), fmt.Errorf("tui: key action %q has no keys", name)
		}
		b.SetKeys(keys...)
		b.SetHelp(keys[0], b.Help().Desc)
	}
	return km, nil
}

func splitKeys(s string) []string {
	var out []string
	for _, k := range strings.Split(s, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// pickerHelp is the help shown when the picker has focus.
func (k KeyMap) pickerHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Down, k.Up, k.Top, k.Bottom, k.Sort},
		{k.Focus, k.NextFocus, k.Overview, k.Code, k.Pipeline},
		{k.Refresh, k.Open, k.Checkout},
		k.reviewKeys(),
		{k.Help, k.Quit},
	}
}

// reviewKeys are the item-scope review bindings (submit form).
func (k KeyMap) reviewKeys() []key.Binding {
	return []key.Binding{k.Submit, k.Approve, k.RequestChanges}
}

// codeHelp is the help shown when the Code tab has focus: the component's
// own groups, then the root model's.
func (k KeyMap) codeHelp(component [][]key.Binding) [][]key.Binding {
	out := append([][]key.Binding(nil), component...)
	return append(out,
		k.reviewKeys(),
		[]key.Binding{k.Back, k.NextFocus, k.Overview, k.Code, k.Pipeline},
		[]key.Binding{k.Refresh, k.Open, k.Checkout, k.Help, k.Quit},
	)
}

// overviewHelp is the help shown when the Overview tab has focus.
func (k KeyMap) overviewHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Down, k.Up, k.PageDown, k.PageUp, k.HalfPageDown, k.HalfPageUp, k.Top, k.Bottom},
		{k.jump(), k.RawLabels, k.Weights},
		k.reviewKeys(),
		{k.Back, k.NextFocus, k.Overview, k.Code, k.Pipeline},
		{k.Refresh, k.Open, k.Checkout, k.Help, k.Quit},
	}
}

// pipelineHelp is the help shown when the Pipeline page has focus: the
// component's own groups, then the root model's (P or esc closes the page;
// r and c act on the page, so refresh and checkout are not listed).
func (k KeyMap) pipelineHelp(component [][]key.Binding) [][]key.Binding {
	out := append([][]key.Binding(nil), component...)
	return append(out,
		[]key.Binding{k.closePipeline(), k.NextFocus, k.Overview, k.Code},
		[]key.Binding{k.Open, k.Help, k.Quit},
	)
}

// closePipeline is the Pipeline key described as closing the page.
func (k KeyMap) closePipeline() key.Binding { return relabel(k.Pipeline, "close pipeline") }

// pickerShort is the status-line hint list for the picker.
func (k KeyMap) pickerShort() []key.Binding {
	return []key.Binding{k.Down, k.Up, k.Focus, k.Overview, k.Code, k.Pipeline, k.Refresh, k.Checkout, k.Open, k.Help, k.Quit}
}

// mainShort is the status-line hint list for the main pane; component holds
// the Code page's or the Pipeline page's own hints.
func (k KeyMap) mainShort(tab Tab, component []key.Binding) []key.Binding {
	switch tab {
	case TabCode:
		out := append([]key.Binding(nil), component...)
		return append(out, k.Submit, k.Back, k.Overview, k.Help, k.Quit)
	case TabPipeline:
		out := append([]key.Binding(nil), component...)
		return append(out, k.closePipeline(), k.Help, k.Quit)
	}
	return []key.Binding{k.Down, k.Up, k.jump(), k.RawLabels, k.Weights, k.Submit, k.Approve, k.Pipeline, k.Back, k.Code, k.Help, k.Quit}
}
