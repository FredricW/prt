package settingsview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/theme"
)

// Field keys (the TOML key names; "scope" and "action" are the page's).
const (
	keyAutoReview = "auto_review"
	keyScope      = "scope"
	keyRole       = "role"
	keyOwns       = "owns"
	keyCostCap    = "cost_cap_usd"
	keyDeepSlots  = "deep_slots"
	keyFast       = "fast"
	keyDeep       = "deep"
	keyEmbed      = "embed"
	keyUnsure     = "unsure_below"
	keyKeepPair   = "keep_pair_p"
	keyCosine     = "embed_min_cosine"
	keyMaxUnits   = "max_group_units"
	keyConc       = "concurrency"
	keyAction     = "action"
)

// sectionNames are the form's groups in order (one page each).
func sectionNames() []string {
	return []string{"Analysis", "You", "Cost", "Models", "Urgency weights", "Thresholds", "Environment"}
}

// labelWidth pads inline input titles so the values line up.
const labelWidth = 18

// autoDescs explains each strategy in the auto_review select.
func autoDescs() map[model.AutoReview]string {
	return map[model.AutoReview]string{
		model.AutoManual:        "nothing runs model stages unless you ask",
		model.AutoNeedsYou:      "PRs that need you",
		model.AutoNeedsYouYours: "plus your own PRs",
		model.AutoAll:           "everything, branches too",
	}
}

// builtForm is a Huh form bound to fv and which section each field key
// lives in.
type builtForm struct {
	form    *huh.Form
	section map[string]int
}

// buildForm builds the settings form over fv (the fields write into it).
func buildForm(info proto.SettingsInfo, fv *formValues, th theme.Theme) builtForm {
	groups := [][]huh.Field{
		analysisFields(info, fv),
		youFields(fv),
		costFields(fv),
		modelFields(fv),
		rankInputs(fv),
		thresholdFields(fv),
		envFields(info, fv),
	}
	names := sectionNames()
	section := map[string]int{}
	hg := make([]*huh.Group, 0, len(groups))
	for i, fs := range groups {
		for _, f := range fs {
			if k := f.GetKey(); k != "" {
				section[k] = i
			}
		}
		hg = append(hg, huh.NewGroup(fs...).Title(names[i]))
	}
	f := huh.NewForm(hg...).
		WithTheme(formTheme(th)).
		WithKeyMap(formKeys()).
		WithShowHelp(false).
		WithShowErrors(false)
	return builtForm{form: f, section: section}
}

func analysisFields(info proto.SettingsInfo, fv *formValues) []huh.Field {
	descs := autoDescs()
	opts := make([]huh.Option[string], 0, len(model.AutoReviews))
	for _, a := range model.AutoReviews {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%-16s %s", a, descs[a]), string(a)))
	}
	fs := []huh.Field{
		huh.NewSelect[string]().Key(keyAutoReview).Title("auto_review").
			Description("which items run their model stages without being asked").
			Options(opts...).Value(&fv.AutoReview),
	}
	if info.GlobalDir == "" {
		fv.Scope = proto.ScopeRepo
		return fs
	}
	return append(fs, huh.NewSelect[proto.SettingsScope]().Key(keyScope).Title("save to").
		Options(
			huh.NewOption("repo    "+join(info.RepoDir, "prt.toml"), proto.ScopeRepo),
			huh.NewOption("global  "+join(info.GlobalDir, "prt.toml"), proto.ScopeGlobal),
		).Value(&fv.Scope))
}

func youFields(fv *formValues) []huh.Field {
	return []huh.Field{
		input(keyRole, "role", &fv.Role),
		huh.NewText().Key(keyOwns).Title("owns").
			Description("one glob per line (ctrl+j new line)").
			Lines(4).ExternalEditor(false).Value(&fv.Owns),
	}
}

func costFields(fv *formValues) []huh.Field {
	return []huh.Field{
		input(keyCostCap, "cost cap ($)", &fv.CostCap),
		input(keyDeepSlots, keyDeepSlots, &fv.DeepSlots),
	}
}

func modelFields(fv *formValues) []huh.Field {
	return []huh.Field{
		input(keyFast, keyFast, &fv.Fast),
		input(keyDeep, keyDeep, &fv.Deep),
		input(keyEmbed, keyEmbed, &fv.Embed),
	}
}

func rankInputs(fv *formValues) []huh.Field {
	rf := rankFields()
	out := make([]huh.Field, 0, len(rf))
	for i, f := range rf {
		out = append(out, input(f.key, f.key, &fv.Rank[i]))
	}
	return out
}

func thresholdFields(fv *formValues) []huh.Field {
	return []huh.Field{
		input(keyUnsure, keyUnsure, &fv.UnsureBelow),
		input(keyKeepPair, keyKeepPair, &fv.KeepPairP),
		input(keyCosine, keyCosine, &fv.EmbedMinCosine),
		input(keyMaxUnits, keyMaxUnits, &fv.MaxGroupUnits),
		input(keyConc, keyConc, &fv.Concurrency),
	}
}

func envFields(info proto.SettingsInfo, fv *formValues) []huh.Field {
	return []huh.Field{
		// The lines go in the note's title: its description is rendered as
		// markdown-ish text (underscores turn italic, raw escape codes even
		// without colour).
		huh.NewNote().Title(strings.Join(envLines(info.Env), "\n")),
		huh.NewSelect[ActionKind]().Key(keyAction).Title("action").
			Options(
				huh.NewOption("Save", ActSave),
				huh.NewOption("Edit routing rules (prt.routing.toml)", ActEditRouting),
				huh.NewOption("Edit questions (prt.questions.toml; applies after daemon restart)", ActEditQuestions),
				huh.NewOption("Cancel", ActCancel),
			).Value(&fv.Action),
	}
}

// input is one inline text field with its label padded to labelWidth and
// the key's validator (validators), if any.
func input(k, label string, v *string) *huh.Input {
	in := huh.NewInput().Key(k).Title(fmt.Sprintf("%-*s", labelWidth, label)).Inline(true).Value(v)
	if check := validators()[k]; check != nil {
		in = in.Validate(check)
	}
	return in
}

// validators are the inline checks by field key (local parse and range
// only; the daemon validates authoritatively on save).
func validators() map[string]func(string) error {
	v := map[string]func(string) error{
		keyCostCap:   positiveDollars("cost cap"),
		keyDeepSlots: atLeastOne(keyDeepSlots),
		keyFast:      notEmpty(keyFast),
		keyDeep:      notEmpty(keyDeep),
		keyEmbed:     notEmpty(keyEmbed),
		keyUnsure:    unitInterval(keyUnsure),
		keyKeepPair:  unitInterval(keyKeepPair),
		keyCosine:    unitInterval(keyCosine),
		keyMaxUnits:  atLeastOne(keyMaxUnits),
		keyConc:      atLeastOne(keyConc),
	}
	for _, f := range rankFields() {
		v[f.key] = weight(f.key)
	}
	return v
}

// textField is one text field's key and value.
type textField struct {
	key string
	val *string
}

// textFields lists fv's single-line text fields in form order.
func (fv *formValues) textFields() []textField {
	out := []textField{
		{keyRole, &fv.Role},
		{keyCostCap, &fv.CostCap}, {keyDeepSlots, &fv.DeepSlots},
		{keyFast, &fv.Fast}, {keyDeep, &fv.Deep}, {keyEmbed, &fv.Embed},
	}
	for i, f := range rankFields() {
		out = append(out, textField{f.key, &fv.Rank[i]})
	}
	return append(out,
		textField{keyUnsure, &fv.UnsureBelow}, textField{keyKeepPair, &fv.KeepPairP},
		textField{keyCosine, &fv.EmbedMinCosine}, textField{keyMaxUnits, &fv.MaxGroupUnits},
		textField{keyConc, &fv.Concurrency})
}

// validateAll runs every inline check on fv (fields skipped with pgdown or
// left with shift+tab were never checked by Huh) and returns the first
// failure in form order.
func validateAll(fv *formValues) error {
	checks := validators()
	for _, tf := range fv.textFields() {
		if c := checks[tf.key]; c != nil {
			if err := c(*tf.val); err != nil {
				return err
			}
		}
	}
	return nil
}

// envLines are the environment presence lines: "JEV_API_KEY  ✓ set",
// "Ollama  ✗ unreachable at 127.0.0.1:11434". Only Probe.OK and Probe.Note
// are shown: the page never has a value to leak.
func envLines(e proto.EnvStatus) []string {
	return []string{
		envLine("JEV_API_KEY", e.JevKey, "set", "not set"),
		envLine("PRT_ANTHROPIC_API_KEY", e.AnthropicKey, "set", "not set"),
		envLine("Ollama", e.Ollama, "reachable", "unreachable"),
		envLine("GitHub", e.GitHub, "connected", "unavailable"),
	}
}

func envLine(name string, p proto.Probe, ok, bad string) string {
	mark, note := "✓", ok
	if !p.OK {
		mark, note = "✗", bad
	}
	if p.Note != "" {
		note = p.Note
	}
	return name + "  " + mark + " " + note
}

// join appends a file name to a directory for display.
func join(dir, file string) string {
	if dir == "" {
		return file
	}
	return strings.TrimSuffix(dir, "/") + "/" + file
}

// formKeys is Huh's keymap with esc as the abort key (the page turns an
// abort into ActCancel), select filtering off (its esc would clash) and
// the external editor off (the page has its own edit actions).
func formKeys() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))
	km.Select.Filter.SetEnabled(false)
	km.Text.Editor.SetEnabled(false)
	km.Input.AcceptSuggestion.SetEnabled(false)
	return km
}

// formTheme builds a Huh theme from prt's palette on the page's renderer,
// so a colour-free renderer yields plain text (glyphs keep the meaning:
// "┃" marks the focused field, "▸" the selected option).
func formTheme(th theme.Theme) *huh.Theme {
	r := th.R
	ns := func() lipgloss.Style { return r.NewStyle() }
	var t huh.Theme
	t.Form.Base = ns()
	t.Group.Base = ns()
	t.Group.Title = th.Accent.Bold(true)
	t.Group.Description = th.Dim
	t.FieldSeparator = ns().SetString("\n")

	f := &t.Focused
	f.Base = ns().PaddingLeft(1).BorderStyle(lipgloss.ThickBorder()).BorderLeft(true).BorderForeground(th.P.Accent)
	f.Card = f.Base
	f.Title = th.Bright
	f.NoteTitle = th.Text // the environment lines
	f.Description = th.Dim
	f.ErrorIndicator = th.Del.SetString(" *")
	f.ErrorMessage = th.Del
	f.SelectSelector = th.Accent.SetString("▸ ")
	f.Option = th.Text
	f.SelectedOption = th.Accent
	f.NextIndicator = th.Accent.MarginLeft(1).SetString("→")
	f.PrevIndicator = th.Accent.MarginRight(1).SetString("←")
	f.MultiSelectSelector = th.Accent.SetString("▸ ")
	f.SelectedPrefix = th.Add.SetString("[•] ")
	f.UnselectedPrefix = th.Dim.SetString("[ ] ")
	f.UnselectedOption = th.Text
	f.TextInput.Cursor = th.Accent
	f.TextInput.CursorText = ns()
	f.TextInput.Placeholder = th.Dim
	f.TextInput.Prompt = th.Accent
	f.TextInput.Text = th.Text
	f.Next = th.Accent

	t.Blurred = t.Focused
	b := &t.Blurred
	b.Base = f.Base.BorderStyle(lipgloss.HiddenBorder())
	b.Card = b.Base
	b.Title = th.Text
	b.SelectSelector = th.Dim.SetString("▸ ") // the choice stays visible
	b.MultiSelectSelector = ns().SetString("  ")
	b.NextIndicator = ns()
	b.PrevIndicator = ns()
	b.TextInput.Text = th.Dim
	return &t
}
