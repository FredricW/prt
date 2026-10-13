package tui

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// Workspace mode (M5.3, DECISIONS "M5.3 user requests", "M5.3 lead calls:
// workspace, picker and resizing"). The TUI asks the daemon what it serves
// (daemon.Client.Workspace) at start, on the refresh tick, on EventSettings
// and on an EventLog naming a repo. A single repo (WorkspaceInfo.Multi
// false) changes nothing. In a workspace:
//
//   - the picker groups items by repo (pickerws.go); z folds a repo, Z every
//     repo, F and `:repo` filter to one repo; on a repo header the main pane
//     shows a repo card and no Detail is loaded;
//   - the header names the workspace root, the repo count, the shown repo's
//     role, the filter and the session's spend;
//   - per-repo calls (Settings, SaveSettings, Routing, `:auto`) carry the
//     active repo: the selected item's, else the header's under the cursor,
//     else the filter's; the Code page's `e` opens files under that repo's
//     root; search is limited to the filtered repo; an unqualified `:add`
//     is qualified with the filter;
//   - notes that name an item say which repo ("acme/portal #482").

// Workspace messages: the Workspace answer and a repo's settings (strategy
// and role) read for the Overview's strategy line and the header.
type (
	workspaceMsg struct {
		info proto.WorkspaceInfo
		err  error
	}
	repoSettingsMsg struct {
		repo string
		info proto.SettingsInfo
		err  error
	}
)

// repoCache holds each repo's live settings (read with Settings{Repo}),
// shared by the copies of the root model like the Detail cache. pending
// marks a read in flight; again asks for one more read when it lands.
type repoCache struct {
	live    map[string]proto.SettingsValues
	pending map[string]bool
	again   map[string]bool
}

func newRepoCache() *repoCache {
	return &repoCache{live: map[string]proto.SettingsValues{}, pending: map[string]bool{}, again: map[string]bool{}}
}

// loadWorkspace asks the daemon what it serves.
func (m Model) loadWorkspace() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		info, err := c.Workspace(context.Background())
		return workspaceMsg{info: info, err: err}
	}
}

// onWorkspace applies a Workspace answer. A single repo (or a daemon that
// cannot answer) keeps the single-repo screens; a failed reload keeps the
// last workspace.
func (m Model) onWorkspace(msg workspaceMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil || !msg.info.Multi {
		if msg.err == nil && !m.ws.Multi {
			m.ws = msg.info
		}
		return m, nil
	}
	var cmd tea.Cmd
	if m.applyWorkspace(msg.info) {
		cmd = m.selectionChanged()
	}
	return m, batch(cmd, m.loadRepoSettings(m.activeRepo(), false))
}

// applyWorkspace records a workspace and groups the picker by repo; it
// reports whether the selection changed.
func (m *Model) applyWorkspace(info proto.WorkspaceInfo) bool {
	m.ws = info
	changed := m.picker.setWorkspace(info.Repos)
	m.cmd.SetRepos(repoNames(info.Repos))
	return changed
}

// repoNames are the names `:repo` completes: every key and label, in
// workspace order, without repeats.
func repoNames(repos []proto.RepoInfo) []string {
	var out []string
	for _, r := range repos {
		for _, n := range []string{r.Key, r.Label} {
			if n != "" && !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	return out
}

// loadRepoSettings reads repo's settings unless they are cached (force
// reads them again) or a read is in flight; nil outside a workspace.
func (m *Model) loadRepoSettings(repo string, force bool) tea.Cmd {
	if !m.ws.Multi || repo == "" {
		return nil
	}
	rc := m.repos
	if rc.pending[repo] {
		if force {
			rc.again[repo] = true
		}
		return nil
	}
	if _, ok := rc.live[repo]; ok && !force {
		return nil
	}
	rc.pending[repo] = true
	c := m.client
	return func() tea.Msg {
		info, err := c.Settings(context.Background(), proto.SettingsParams{Repo: repo})
		return repoSettingsMsg{repo: repo, info: info, err: err}
	}
}

// onRepoSettings caches a repo's settings and redraws the Overviews that
// name its strategy. A failed read leaves the strategy unknown (the line is
// omitted, as in a single repo).
func (m Model) onRepoSettings(msg repoSettingsMsg) (tea.Model, tea.Cmd) {
	rc := m.repos
	delete(rc.pending, msg.repo)
	if msg.err == nil {
		m.takeRepoSettings(msg.repo, msg.info.Values)
	}
	if rc.again[msg.repo] {
		delete(rc.again, msg.repo)
		return m, m.loadRepoSettings(msg.repo, true)
	}
	return m, nil
}

// takeRepoSettings caches repo's live settings and redraws what shows them.
func (m *Model) takeRepoSettings(repo string, v proto.SettingsValues) {
	m.repos.live[repo] = v
	if m.detail != nil && m.detail.Item.Repo == repo {
		m.redrawOverview()
	}
	if m.preview != nil && m.preview.Item.Repo == repo {
		m.renderPreview()
	}
}

// takeSettings records settings read or saved for repo: the live
// single-repo values, or that repo's cache in a workspace.
func (m *Model) takeSettings(repo string, v proto.SettingsValues) {
	if !m.ws.Multi {
		m.setLiveSettings(v)
		return
	}
	if repo == "" {
		repo = m.activeRepo()
	}
	if repo != "" {
		m.takeRepoSettings(repo, v)
	}
}

// reloadRepoSettings re-reads the settings of repo, or of every cached
// repo when repo is "" (a global save), plus the active repo's.
func (m *Model) reloadRepoSettings(repo string) tea.Cmd {
	var cmds []tea.Cmd
	if repo != "" {
		cmds = append(cmds, m.loadRepoSettings(repo, true))
	} else {
		keys := make([]string, 0, len(m.repos.live))
		for k := range m.repos.live {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			cmds = append(cmds, m.loadRepoSettings(k, true))
		}
	}
	return batch(append(cmds, m.loadRepoSettings(m.activeRepo(), false))...)
}

// activeRepo is the repo per-repo calls act on: the selected item's, else
// the repo header's under the cursor, else the repo filter's; "" outside a
// workspace.
func (m Model) activeRepo() string {
	if !m.ws.Multi {
		return ""
	}
	if r := m.picker.cursorRepo(); r != "" {
		return r
	}
	return m.picker.filter
}

// shownRepo is the repo of the item the main pane shows (its loaded
// detail's), else the active repo.
func (m Model) shownRepo() string {
	if !m.ws.Multi {
		return ""
	}
	if m.detail != nil {
		return m.detail.Item.Repo
	}
	return m.activeRepo()
}

// repoInfo returns the workspace's repo key.
func (m Model) repoInfo(key string) (proto.RepoInfo, bool) {
	for _, r := range m.ws.Repos {
		if r.Key == key {
			return r, true
		}
	}
	return proto.RepoInfo{}, false
}

// repoLabelOf is repo key's label (the key for a repo the workspace does
// not list).
func (m Model) repoLabelOf(key string) string {
	r, ok := m.repoInfo(key)
	if !ok {
		r = proto.RepoInfo{Key: key}
	}
	return repoLabel(r)
}

// strategyFor is the auto-review strategy the Overview names for an item of
// repo: the live one in a single repo, the repo's cached one in a
// workspace ("" while unknown).
func (m Model) strategyFor(repo string) string {
	if !m.ws.Multi {
		return m.strategy
	}
	return m.repos.live[repo].AutoReview
}

// repoRole is repo's role: its live settings' when read and set, else the
// workspace's.
func (m Model) repoRole(repo string) string {
	if v, ok := m.repos.live[repo]; ok && v.Role != "" {
		return v.Role
	}
	r, _ := m.repoInfo(repo)
	return r.Role
}

// headerRole is the header's "role:": the configured one in a single repo,
// the active repo's in a workspace ("" hides it).
func (m Model) headerRole() string {
	if !m.ws.Multi {
		return m.opts.Role
	}
	if r := m.activeRepo(); r != "" {
		return m.repoRole(r)
	}
	return ""
}

// repoRoot is the work tree the Code page's `e` opens files under: the
// shown item's repo root in a workspace, else Options.Root.
func (m Model) repoRoot() string {
	if m.ws.Multi {
		if r, ok := m.repoInfo(m.shownRepo()); ok && r.Root != "" {
			return r.Root
		}
	}
	return m.opts.Root
}

// itemName is itemShort for notes: "acme/portal #482" in a workspace.
func (m Model) itemName(it model.Item) string {
	return m.withRepo(it.Repo, itemShort(it))
}

// itemTitle is itemLabel for notes and titles: "acme/portal #482 Title" in
// a workspace.
func (m Model) itemTitle(it model.Item) string {
	return m.withRepo(it.Repo, itemLabel(it))
}

// hitName is hitLabel for notes, with the hit's repo in a workspace.
func (m Model) hitName(h proto.SearchHit) string {
	return m.withRepo(h.Repo, hitLabel(h))
}

// withRepo prefixes s with repo's label in a workspace.
func (m Model) withRepo(repo, s string) string {
	if !m.ws.Multi || repo == "" {
		return s
	}
	return m.repoLabelOf(repo) + " " + s
}

// workspacePickerKey handles the workspace picker keys: z folds or unfolds
// the cursor's repo, Z every repo, F cycles the repo filter, enter on a
// repo header folds or unfolds it. moved reports a changed selection; ok is
// false for any other key.
func (m *Model) workspacePickerKey(k tea.KeyMsg) (moved, ok bool) {
	km := m.keys
	switch {
	case key.Matches(k, km.FoldRepo):
		return m.picker.toggleFold(m.picker.cursorRepo()), true
	case key.Matches(k, km.FoldAll):
		return m.picker.foldAll(), true
	case key.Matches(k, km.RepoFilter):
		return m.cycleFilter(), true
	case m.picker.header != "" && key.Matches(k, km.Focus):
		return m.picker.toggleFold(m.picker.header), true
	}
	return false, false
}

// cycleFilter is F: every repo → each repo in workspace order → every repo.
func (m *Model) cycleFilter() bool {
	next := ""
	keys := make([]string, 0, len(m.ws.Repos))
	for _, r := range m.ws.Repos {
		keys = append(keys, r.Key)
	}
	switch i := slices.Index(keys, m.picker.filter); {
	case m.picker.filter == "" && len(keys) > 0:
		next = keys[0]
	case i >= 0 && i+1 < len(keys):
		next = keys[i+1]
	}
	return m.filterTo(next)
}

// filterTo sets the repo filter ("" every repo) and says so.
func (m *Model) filterTo(key string) bool {
	moved := m.picker.setFilter(key)
	m.note = "repo: all"
	if key != "" {
		m.note = "repo: " + m.repoLabelOf(key)
	}
	return moved
}

// repoCommand is `:repo <name>|all` (M5.3).
func (m Model) repoCommand(arg string) (Model, tea.Cmd) {
	if !m.ws.Multi {
		m.note = "not a workspace"
		return m, nil
	}
	key := ""
	if arg = strings.TrimSpace(arg); arg != "" && arg != "all" {
		k, err := m.resolveRepo(arg)
		if err != nil {
			m.note = err.Error()
			return m, nil
		}
		key = k
	}
	if m.filterTo(key) {
		return m, m.selectionChanged()
	}
	return m, nil
}

// resolveRepo finds the repo arg names: a key or label, exactly, then
// ignoring case, then a unique base name ("billing-svc" for
// "acme/billing-svc").
func (m Model) resolveRepo(arg string) (string, error) {
	repos := m.ws.Repos
	for _, same := range []func(a, b string) bool{func(a, b string) bool { return a == b }, strings.EqualFold} {
		for _, r := range repos {
			if same(arg, r.Key) || (r.Label != "" && same(arg, r.Label)) {
				return r.Key, nil
			}
		}
	}
	var hits []string
	for _, r := range repos {
		if strings.EqualFold(arg, path.Base(r.Key)) || (r.Label != "" && strings.EqualFold(arg, path.Base(r.Label))) {
			hits = append(hits, r.Key)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", fmt.Errorf("unknown repo %q — repos: %s", arg, strings.Join(m.repoLabels(nil), ", "))
	}
	return "", fmt.Errorf("repo %q is ambiguous: %s", arg, strings.Join(m.repoLabels(hits), ", "))
}

// repoLabels are the labels of keys (every repo's when keys is nil).
func (m Model) repoLabels(keys []string) []string {
	var out []string
	for _, r := range m.ws.Repos {
		if keys == nil || slices.Contains(keys, r.Key) {
			out = append(out, repoLabel(r))
		}
	}
	return out
}

// qualifyAdd qualifies an unqualified `:add` with the repo filter: "482"
// and "#482" become "<key>#482", a ref "<key>:<ref>". A query that names a
// repo already ("acme/x#12", "x:feat/y") and any query without a filter
// are kept.
func (m Model) qualifyAdd(q string) string {
	t, repo := strings.TrimSpace(q), m.picker.filter
	if !m.ws.Multi || repo == "" || t == "" || strings.Contains(t, ":") || strings.Index(t, "#") > 0 {
		return q
	}
	if n := strings.TrimPrefix(t, "#"); n != "" && strings.Trim(n, "0123456789") == "" {
		return repo + "#" + n
	}
	return repo + ":" + t
}

// workspaceLogNote is the header note for an EventLog from a repo: none
// when it repeats the repo's GitHub status (the repo header shows it),
// else "<label>: <note>".
func (m Model) workspaceLogNote(e proto.Event) (string, bool) {
	r, _ := m.repoInfo(e.Repo)
	if repeatsStatus(r, e.Note) {
		return "", false
	}
	return m.repoLabelOf(e.Repo) + ": " + e.Note, true
}

// repeatsStatus reports whether note only says what a repo's non-GitHub
// status says: its own note, "GitHub disabled …", an offline note, or
// "…; local branches only".
func repeatsStatus(r proto.RepoInfo, note string) bool {
	if r.Status == "" || r.Status == proto.RepoGitHub {
		return false
	}
	n := strings.ToLower(strings.TrimSpace(note))
	return (r.Note != "" && note == r.Note) || strings.Contains(n, "github disabled") ||
		strings.HasPrefix(n, "offline") || strings.HasSuffix(n, "local branches only")
}

// viewRepoCard is the main pane on a repo header (M5.3): the repo's label,
// root, GitHub status and note, its items per section, its spend and the
// keys that unfold or filter it.
func (m Model) viewRepoCard(key string, w int) string {
	st := m.st
	r, ok := m.repoInfo(key)
	if !ok {
		r = proto.RepoInfo{Key: key}
	}
	field := func(name, value string) []string {
		lines := strings.Split(ansi.Wordwrap(value, max(10, w-8), ""), "\n")
		out := []string{st.dim.Render(padRight(name, 8)) + lines[0]}
		for _, l := range lines[1:] {
			out = append(out, strings.Repeat(" ", 8)+l)
		}
		return out
	}
	lines := []string{st.title.Render(repoLabel(r))}
	if r.Root != "" {
		lines = append(lines, st.dim.Render(tildePath(r.Root, m.home)))
	}
	lines = append(lines, "")
	status := orDash(repoStatusWords(r.Status))
	if r.Note != "" {
		status += " — " + r.Note
	}
	lines = append(lines, field("status", status)...)
	lines = append(lines, field("items", m.repoCounts(key))...)
	if r.Role != "" || m.repoRole(key) != "" {
		lines = append(lines, field("role", m.repoRole(key))...)
	}
	spent := "none this session"
	if r.SpentUSD > 0 {
		spent = strings.TrimSuffix(spentText(r.SpentUSD), " spent") + " this session"
	}
	lines = append(lines, field("spent", spent)...)
	km := m.keys
	hint := km.RepoFilter.Help().Key + " filters by repo"
	if g, ok := m.picker.group(key); ok && m.picker.isFolded(g) {
		hint = km.Focus.Help().Key + " or " + km.FoldRepo.Help().Key + " unfolds · " + hint
	} else if ok && g.n == 0 {
		hint = "no items in this repo · " + hint
	}
	lines = append(lines, "", st.dim.Render(hint))
	return strings.Join(lines, "\n")
}

// repoCounts is "N PRs · N branches — Needs you 1 · Yours 0 · Branches 2 ·
// Added 0" for repo key (Involved and other sections only when they have
// items).
func (m Model) repoCounts(key string) string {
	count := map[model.Section]int{}
	prs, branches := 0, 0
	for _, it := range m.picker.items {
		if it.Repo != key {
			continue
		}
		s := it.Section
		if sectionRank(s) == len(sections) {
			s = "other"
		}
		count[s]++
		if it.Kind == model.KindBranch {
			branches++
		} else {
			prs++
		}
	}
	var parts []string
	for _, s := range append(append([]model.Section(nil), sections...), "other") {
		if count[s] == 0 && (hideWhenEmpty[s] || s == "other") {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d", sectionTitle(s), count[s]))
	}
	nb := fmt.Sprintf("%d branches", branches)
	if branches == 1 {
		nb = "1 branch"
	}
	return plural(prs, "PR") + " · " + nb + " — " + strings.Join(parts, " · ")
}

// spentText is the header's spend: "$1.23 spent", "<$0.01 spent" below a
// cent.
func spentText(usd float64) string {
	if usd < 0.01 {
		return "<$0.01 spent"
	}
	return fmt.Sprintf("$%.2f spent", usd)
}

// tildePath shows p with the home directory as "~" ("–" for "").
func tildePath(p, home string) string {
	switch {
	case p == "":
		return "–"
	case home == "" || home == string(filepath.Separator):
		return p
	case p == home:
		return "~"
	case strings.HasPrefix(p, home+string(filepath.Separator)):
		return "~" + p[len(home):]
	}
	return p
}

// homeDir is the user's home directory, "" when unknown.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return ""
	}
	return filepath.Clean(h)
}
