package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"

	"github.com/fredricw/prt/internal/codeview"
)

// KeyMap holds every binding (spec: Keybindings and navigation). Fields
// are exported so callers can tweak them; KeyMapFromConfig applies the
// `[keys]` table of prt.toml.
type KeyMap struct {
	// Picker.
	Up, Down, Top, Bottom key.Binding
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
		Up:           b("up", "k", "up"),
		Down:         b("down", "j", "down"),
		Top:          b("top", "g", "home"),
		Bottom:       b("bottom", "G", "end"),
		Focus:        b("focus main pane", "enter"),
		Back:         b("back to picker", "esc"),
		NextFocus:    b("next pane", "tab"),
		PrevFocus:    b("previous pane", "shift+tab"),
		Refresh:      b("refresh item", "r"),
		Overview:     b("overview tab", "1"),
		Code:         b("code tab", "2"),
		Open:         b("open on GitHub", "o"),
		Checkout:     b("git checkout", "c"),
		PageDown:     b("page down", "pgdown", " "),
		PageUp:       b("page up", "pgup"),
		HalfPageDown: b("half page down", "ctrl+d"),
		HalfPageUp:   b("half page up", "ctrl+u"),
		NextFile:     b("next file", "J", "]"),
		PrevFile:     b("previous file", "K", "["),
		NextUnit:     b("next unit", "n"),
		PrevUnit:     b("previous unit", "p"),
		Filters:      b("filters", "f"),
		Expand:       b("expand noise", "z"),
		RawLabels:    b("raw labels", "L"),
		Mode:         b("by file / by change", "g"),
		MergeGroups:  b("merge groups", "M"),
		SplitGroup:   b("split group", "X"),
		Help:         b("help", "?"),
		Quit:         b("quit", "q", "ctrl+c"),
		Yes:          b("yes", "y", "Y"),
		No:           b("no", "n", "N", "esc"),
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
		"up": &k.Up, "down": &k.Down, "top": &k.Top, "bottom": &k.Bottom,
		"focus": &k.Focus, "back": &k.Back, "next_focus": &k.NextFocus, "prev_focus": &k.PrevFocus,
		"refresh": &k.Refresh, "overview": &k.Overview, "code": &k.Code, "open": &k.Open,
		"checkout": &k.Checkout, "page_down": &k.PageDown, "page_up": &k.PageUp,
		"half_page_down": &k.HalfPageDown, "half_page_up": &k.HalfPageUp,
		"next_file": &k.NextFile, "prev_file": &k.PrevFile, "help": &k.Help, "quit": &k.Quit,
		"next_unit": &k.NextUnit, "prev_unit": &k.PrevUnit, "filters": &k.Filters,
		"expand": &k.Expand, "raw_labels": &k.RawLabels,
		"mode": &k.Mode, "merge_groups": &k.MergeGroups, "split_group": &k.SplitGroup,
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
	return ck
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
		{k.Down, k.Up, k.Top, k.Bottom},
		{k.Focus, k.NextFocus, k.Overview, k.Code},
		{k.Refresh, k.Open, k.Checkout},
		{k.Help, k.Quit},
	}
}

// codeHelp is the help shown when the Code tab has focus: the component's
// own groups, then the root model's.
func (k KeyMap) codeHelp(component [][]key.Binding) [][]key.Binding {
	out := append([][]key.Binding(nil), component...)
	return append(out,
		[]key.Binding{k.Back, k.NextFocus, k.Overview, k.Code},
		[]key.Binding{k.Refresh, k.Open, k.Checkout, k.Help, k.Quit},
	)
}

// overviewHelp is the help shown when the Overview tab has focus.
func (k KeyMap) overviewHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Down, k.Up, k.PageDown, k.PageUp, k.HalfPageDown, k.HalfPageUp, k.Top, k.Bottom},
		{k.jump(), k.RawLabels},
		{k.Back, k.NextFocus, k.Overview, k.Code},
		{k.Refresh, k.Open, k.Checkout, k.Help, k.Quit},
	}
}

// pickerShort is the status-line hint list for the picker.
func (k KeyMap) pickerShort() []key.Binding {
	return []key.Binding{k.Down, k.Up, k.Focus, k.Overview, k.Code, k.Refresh, k.Checkout, k.Open, k.Help, k.Quit}
}

// mainShort is the status-line hint list for the main pane; component holds
// the Code page's own hints.
func (k KeyMap) mainShort(tab Tab, component []key.Binding) []key.Binding {
	if tab == TabCode {
		out := append([]key.Binding(nil), component...)
		return append(out, k.Back, k.Overview, k.Help, k.Quit)
	}
	return []key.Binding{k.Down, k.Up, k.jump(), k.RawLabels, k.Back, k.Code, k.Help, k.Quit}
}
