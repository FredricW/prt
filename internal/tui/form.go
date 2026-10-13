package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// formKind says what an open form submits.
type formKind int

const (
	// formReview submits a review on a PR (S, a, A).
	formReview formKind = iota
	// formCreatePR creates a PR from a branch item (S or a on a branch
	// without a PR; DECISIONS "M4 shared contracts").
	formCreatePR
)

// formValues are the values a form edits. The root model holds them by
// pointer so the huh fields' bindings survive copies of the model.
type formValues struct {
	Event proto.ReviewEvent
	Body  string
	Title string
	Base  string
	Draft bool
}

// formSpec describes the form to build.
type formSpec struct {
	kind   formKind
	item   model.Item
	drafts draftCount
	width  int
}

// dialog is a form drawn in place of the main pane; it captures every key
// while open. The default wraps a huh.Form; tests substitute a scripted one
// (Model.newDialog) so values are submitted deterministically.
type dialog interface {
	Init() tea.Cmd
	// Update handles a key or one of the form's own messages.
	Update(msg tea.Msg) tea.Cmd
	View() string
	// State is huh.StateNormal while editing, StateCompleted once
	// submitted and StateAborted once cancelled (esc).
	State() huh.FormState
}

// openForm is the root model's open form.
type openForm struct {
	spec   formSpec
	values *formValues
	dlg    dialog
	err    string // validation error shown above the form
}

// huhDialog adapts *huh.Form to dialog.
type huhDialog struct{ f *huh.Form }

func (d huhDialog) Init() tea.Cmd        { return d.f.Init() }
func (d huhDialog) View() string         { return d.f.View() }
func (d huhDialog) State() huh.FormState { return d.f.State }

func (d huhDialog) Update(msg tea.Msg) tea.Cmd {
	_, cmd := d.f.Update(msg)
	return cmd
}

// newHuhDialog builds the Huh form for s, bound to v. esc cancels.
func newHuhDialog(s formSpec, v *formValues) dialog {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))
	g := reviewGroup(s, v)
	if s.kind == formCreatePR {
		g = createPRGroup(s, v)
	}
	f := huh.NewForm(g).WithKeyMap(km).WithTheme(huh.ThemeBase()).WithWidth(max(20, s.width)).WithShowHelp(true)
	return huhDialog{f: f}
}

// reviewGroup is the submit-review form: verdict, optional summary and the
// confirm line saying how many drafts go out as line comments.
func reviewGroup(s formSpec, v *formValues) *huh.Group {
	return huh.NewGroup(
		huh.NewSelect[proto.ReviewEvent]().Title("Verdict").Options(
			huh.NewOption("Approve", proto.EventApprove),
			huh.NewOption("Request changes", proto.EventRequestChanges),
			huh.NewOption("Comment", proto.EventComment),
		).Value(&v.Event),
		huh.NewText().Title("Summary (optional)").Value(&v.Body).Validate(func(body string) error {
			w := *v
			w.Body = body
			return validateForm(formReview, w, s.drafts)
		}),
		huh.NewNote().Title(s.drafts.confirmLine()),
	).Title("Submit review · " + itemLabel(s.item))
}

// createPRGroup is the create-PR form for a branch: title, body, base and
// a draft toggle; the branch's drafts are attached as one review.
func createPRGroup(s formSpec, v *formValues) *huh.Group {
	return huh.NewGroup(
		huh.NewInput().Title("Title").Value(&v.Title).Validate(func(t string) error {
			w := *v
			w.Title = t
			return validateForm(formCreatePR, w, s.drafts)
		}),
		huh.NewText().Title("Body").Value(&v.Body),
		huh.NewInput().Title("Base").Value(&v.Base),
		huh.NewConfirm().Title("Open as draft PR?").Affirmative("Draft").Negative("Ready").Value(&v.Draft),
		huh.NewNote().Title(fmt.Sprintf("%s will be attached as one review", plural(s.drafts.sendable, "draft"))),
	).Title("Create PR · " + itemLabel(s.item))
}

// confirmLine is the submit form's "N drafts will be posted" line.
func (d draftCount) confirmLine() string {
	s := plural(d.sendable, "draft") + " will be posted as line comments"
	if d.outdated > 0 {
		s += fmt.Sprintf(" (%d outdated stay drafts)", d.outdated)
	}
	return s
}

// validateForm checks a form's values before anything is sent: a request
// for changes or a comment needs a summary or at least one draft; a PR
// needs a title.
func validateForm(k formKind, v formValues, d draftCount) error {
	switch k {
	case formCreatePR:
		if strings.TrimSpace(v.Title) == "" {
			return errors.New("a PR needs a title")
		}
	case formReview:
		if v.Event != proto.EventApprove && d.sendable == 0 && strings.TrimSpace(v.Body) == "" {
			return errors.New("nothing to send: write a summary or add drafts")
		}
	}
	return nil
}

// openSubmit handles S, a and A: the submit form with ev preselected on a
// PR, the create-PR form on a branch (S and a; A needs a PR).
func (m *Model) openSubmit(ev proto.ReviewEvent) tea.Cmd {
	it, ok := m.picker.selected()
	if !ok {
		m.note = "no item selected"
		return nil
	}
	d := m.draftsFor(it.ID)
	if it.Kind == model.KindBranch && it.Number == 0 {
		if ev == proto.EventRequestChanges {
			m.note = "no PR yet — " + m.keys.Submit.Help().Key + " creates one"
			return nil
		}
		return m.showForm(formSpec{kind: formCreatePR, item: it, drafts: d},
			&formValues{Title: it.Title, Base: it.BaseRef})
	}
	return m.showForm(formSpec{kind: formReview, item: it, drafts: d}, &formValues{Event: ev})
}

// showForm opens the form for s with values v and returns its Init command.
func (m *Model) showForm(s formSpec, v *formValues) tea.Cmd {
	_, mw, _ := m.dims()
	s.width = mw - 2
	mk := m.newDialog
	if mk == nil {
		mk = newHuhDialog
	}
	d := mk(s, v)
	m.form = &openForm{spec: s, values: v, dlg: d}
	cmd := d.Init()
	m.sizeForm()
	return cmd
}

// sizeForm tells the open form the main pane's size (below the tab bar).
func (m *Model) sizeForm() {
	if m.form == nil {
		return
	}
	_, mw, bh := m.dims()
	m.form.dlg.Update(tea.WindowSizeMsg{Width: mw - 2, Height: max(1, bh-2)})
}

// updateForm hands msg to the open form and acts on its outcome.
func (m Model) updateForm(msg tea.Msg) (Model, tea.Cmd) {
	f := m.form
	cmd := f.dlg.Update(msg)
	switch f.dlg.State() {
	case huh.StateAborted:
		m.form = nil
		m.note = "cancelled"
		return m, nil
	case huh.StateCompleted:
		return m.finishForm()
	}
	return m, cmd
}

// finishForm validates the submitted values and sends them; invalid values
// reopen the form with the error shown and nothing sent.
func (m Model) finishForm() (Model, tea.Cmd) {
	f := m.form
	if err := validateForm(f.spec.kind, *f.values, f.spec.drafts); err != nil {
		cmd := m.showForm(f.spec, f.values)
		m.form.err = err.Error()
		return m, cmd
	}
	m.form = nil
	c, v, id := m.client, *f.values, f.spec.item.ID
	if f.spec.kind == formCreatePR {
		m.note = "creating PR…"
		p := proto.CreatePRParams{ItemID: id, Title: strings.TrimSpace(v.Title), Body: v.Body,
			Base: strings.TrimSpace(v.Base), Draft: v.Draft}
		return m, func() tea.Msg {
			res, err := c.CreatePR(context.Background(), p)
			return prCreatedMsg{res: res, err: err}
		}
	}
	m.note = "submitting review…"
	p := proto.SubmitParams{ItemID: id, Event: v.Event, Body: strings.TrimSpace(v.Body)}
	return m, func() tea.Msg {
		res, err := c.SubmitReview(context.Background(), p)
		return submittedMsg{res: res, err: err}
	}
}

// viewForm renders the open form (with its validation error) for the main
// pane.
func (m Model) viewForm() string {
	s := m.form.dlg.View()
	if m.form.err != "" {
		s = m.st.prompt.Render("✗ "+m.form.err) + "\n" + s
	}
	return s
}
