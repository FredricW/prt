package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/settings"
)

// M5.1 shared contracts (DECISIONS "M5.1 shared contracts"): stop/resume
// and the settings page's backend. cmd/prt calls SetSettingsDeps.

// ModelFactory builds the model clients for a configuration (the settings
// page changed fast, deep or embed). cmd/prt supplies one that honours
// --offline and the replay flags; it returns notes for the log ("fast
// review tier disabled: …"), never secrets.
type ModelFactory func(cfg config.Config) (pipeline.Models, []string)

// SettingsDeps are what Settings and SaveSettings need beyond pipeline.Deps.
// Every field may be zero: no Models means a save never swaps clients (the
// new model names apply after a restart, with a warning); no Prober means
// Ollama shows "not checked"; no Getenv means os.Getenv; GlobalDir ""
// means config.GlobalDir().
type SettingsDeps struct {
	Models    ModelFactory
	Prober    settings.Prober
	Getenv    func(string) string
	GlobalDir string
}

// SetSettingsDeps installs the settings collaborators (cmd/prt, tests).
// Call it before serving requests.
func (c *InProcess) SetSettingsDeps(sd SettingsDeps) {
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	c.sdeps = sd
}

func (c *InProcess) settingsDeps() SettingsDeps {
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	return c.sdeps
}

// Ensure targets of SettingsParams.
const (
	ensureRouting   = "routing"
	ensureQuestions = "questions"
)

// nothingChanged is SettingsInfo.Applied when a save or reload changed
// nothing live.
const nothingChanged = "nothing changed"

// Stop implements Client (DECISIONS "M5.1 stop / resume"). For one item it
// drops the item's waiting jobs and cancels its running run with cause
// pipeline.ErrStopped (finish then marks it StateStopped at the stage in
// flight); an item that was only queued becomes StateStopped ("queued")
// here. p.All does that for every running and waiting item; it changes
// neither the strategy nor discovery. Nothing to stop is not an error. The
// FromStage and Force of every job it catches are remembered for Resume
// (M5.2; a running run's when it ends, in finish).
func (c *InProcess) Stop(ctx context.Context, p proto.StopParams) (proto.StopResult, error) {
	if p.All {
		return c.stopAll(ctx), nil
	}
	if p.ItemID == "" {
		return proto.StopResult{}, errors.New("daemon: stop: no item")
	}
	if _, err := c.d.Store.Item(ctx, p.ItemID); err != nil {
		return proto.StopResult{}, fmt.Errorf("daemon: stop %s: %w", p.ItemID, err)
	}
	dropped, busy := c.q.drop(p.ItemID)
	if dropped != nil {
		c.rememberDropped(ctx, *dropped)
	}
	cancelled := c.cancelRunCause(p.ItemID, pipeline.ErrStopped)
	switch {
	case busy || cancelled:
		// finish (or analyse's stop-mark check) records the stage.
	case dropped != nil:
		c.setStoppedUnlessAnalysed(p.ItemID, stopQueued)
	default:
		return proto.StopResult{}, nil
	}
	return proto.StopResult{Stopped: []string{p.ItemID}}, nil
}

// stopAll stops every running and waiting item.
func (c *InProcess) stopAll(ctx context.Context) proto.StopResult {
	dropped, busy := c.q.dropAll()
	for _, id := range busy {
		c.cancelRunCause(id, pipeline.ErrStopped)
	}
	var waiting []string
	for _, j := range dropped {
		c.rememberDropped(ctx, j)
		if !slices.Contains(busy, j.itemID) {
			waiting = append(waiting, j.itemID)
		}
	}
	slices.Sort(waiting)
	for _, id := range waiting {
		c.setStoppedUnlessAnalysed(id, stopQueued)
	}
	ids := append(append([]string(nil), busy...), waiting...)
	slices.Sort(ids)
	return proto.StopResult{Stopped: slices.Compact(ids)}
}

// Resume implements Client: p.ItemID (in any state; a no-op while an
// explicit run of it is going; behind an automatic or stopped run it is
// queued to start after that run)
// or, with p.All, every StateStopped item is queued again as an explicit
// request with user priority, its head recorded as requested. The job
// carries the FromStage and Force of the jobs the stop caught (M5.2; none:
// a plain run); finished stages hit their caches. An unknown item is an
// error.
func (c *InProcess) Resume(ctx context.Context, p proto.ResumeParams) (proto.ResumeResult, error) {
	var items []model.Item
	if p.All {
		all, err := c.d.Store.Items(ctx)
		if err != nil {
			return proto.ResumeResult{}, fmt.Errorf("daemon: resume: %w", err)
		}
		for _, it := range all {
			if it.State == model.StateStopped {
				items = append(items, it)
			}
		}
	} else {
		if p.ItemID == "" {
			return proto.ResumeResult{}, errors.New("daemon: resume: no item")
		}
		it, err := c.d.Store.Item(ctx, p.ItemID)
		if err != nil {
			return proto.ResumeResult{}, fmt.Errorf("daemon: resume %s: %w", p.ItemID, err)
		}
		items = append(items, it)
	}
	var res proto.ResumeResult
	for _, it := range items {
		if c.q.covers(job{itemID: it.ID, explicit: true}) {
			continue // an explicit run of it is already going
		}
		// The job carries the record; it is forgotten before the job can
		// start (and be stopped again, writing a new one).
		j := c.resumeJob(ctx, it)
		c.forgetStop(ctx, it.ID)
		if err := c.pushExplicit(ctx, it, j); err != nil {
			return res, err
		}
		res.Resumed = append(res.Resumed, it.ID)
	}
	return res, nil
}

// Settings implements Client: the page's values, where they come from and
// go to, and the environment (presence only: no env value is ever copied
// into the result). p.Ensure creates the routing or questions file first.
func (c *InProcess) Settings(ctx context.Context, p proto.SettingsParams) (proto.SettingsInfo, error) {
	switch p.Ensure {
	case "":
	case ensureRouting:
		if err := c.ensureRoutingFile(); err != nil {
			return c.settingsInfo(ctx), err
		}
	case ensureQuestions:
		path := c.questionsPath()
		if path == "" {
			return c.settingsInfo(ctx), errors.New("daemon: settings: no config directory to write prt.questions.toml to")
		}
		if _, err := settings.EnsureQuestions(path); err != nil {
			return c.settingsInfo(ctx), fmt.Errorf("daemon: settings: %w", err)
		}
	default:
		return c.settingsInfo(ctx), fmt.Errorf("daemon: settings: unknown ensure %q (want %q or %q)", p.Ensure, ensureRouting, ensureQuestions)
	}
	return c.settingsInfo(ctx), nil
}

// settingsInfo describes the live configuration for the settings page.
func (c *InProcess) settingsInfo(ctx context.Context) proto.SettingsInfo {
	cfg, rt := c.cfg(), c.p.Routing().Config
	sd := c.settingsDeps()
	getenv := sd.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return proto.SettingsInfo{
		Values: settings.FromConfig(cfg, rt),
		Source: cfg.Source, RoutingSource: rt.Source,
		RepoDir: c.repoDir(), GlobalDir: c.globalDir(),
		RoutingPath: c.routingPath(), QuestionsPath: c.questionsPath(),
		Env:      settings.Env(ctx, getenv, sd.Prober, c.gitHubProbe()),
		Warnings: append(slices.Clone(cfg.Warnings), rt.Warnings...),
	}
}

// gitHubProbe says whether a GitHub client is wired, with the reason when
// it is not.
func (c *InProcess) gitHubProbe() proto.Probe {
	if c.d.GH != nil {
		c.evMu.RLock()
		note := c.ghNote
		c.evMu.RUnlock()
		return proto.Probe{OK: true, Note: cmp.Or(note, "gh auth token ok")}
	}
	return proto.Probe{Note: c.gitHubNote()}
}

// repoDir is the repo scope's directory: the first config dir
// (config.RepoDir of the git dir); "" without config dirs.
func (c *InProcess) repoDir() string {
	if len(c.d.ConfigDirs) == 0 {
		return ""
	}
	return c.d.ConfigDirs[0]
}

// globalDir is the global scope's directory.
func (c *InProcess) globalDir() string {
	if d := c.settingsDeps().GlobalDir; d != "" {
		return d
	}
	return config.GlobalDir()
}

// questionsPath is the questions file the page edits: the active one, else
// prt.questions.toml in the repo scope ("" without config dirs).
func (c *InProcess) questionsPath() string {
	if p := config.Find(c.d.ConfigDirs, config.QuestionsFile); p != "" {
		return p
	}
	if d := c.repoDir(); d != "" {
		return filepath.Join(d, config.QuestionsFile)
	}
	return ""
}

// SaveSettings implements Client (DECISIONS "M5.1 settings page"): it
// validates and writes the values to the scope's files, reloads prt.toml
// and prt.routing.toml from the config dirs (on a reload error the old
// configuration stays and the error is returned), applies what changed
// live (apply) and tells every TUI with an EventSettings. Calls are
// serialised.
func (c *InProcess) SaveSettings(ctx context.Context, p proto.SaveSettingsParams) (proto.SettingsInfo, error) {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	oldCfg, oldRt := c.cfg(), c.p.Routing().Config
	// Only the keys the save changes are validated: a value config.Load
	// accepts (concurrency = 0, a blank model spec) that the save writes
	// back unchanged never blocks it (DECISIONS "M5.1 review fixes 2").
	oldVals := settings.FromConfig(oldCfg, oldRt)
	if err := settings.ValidateChanged(oldVals, p.Values); err != nil {
		return c.settingsInfo(ctx), err
	}
	dir, err := c.scopeDir(p.Scope)
	if err != nil {
		return c.settingsInfo(ctx), err
	}
	// A new repo prt.toml shadows the active (global) one, so it starts as a
	// copy of it. A new global file is shadowed by the repo one instead, so
	// it never copies repo-only keys such as [github] repo.
	seedMain := ""
	if dir == c.repoDir() {
		seedMain = oldCfg.Source
	}
	if err := settings.SaveSeededChanged(dir, oldVals, p.Values, seedMain, oldRt.Source); err != nil {
		return c.settingsInfo(ctx), fmt.Errorf("daemon: save settings: %w", err)
	}
	return c.reloadLocked(ctx, oldVals, c.shadowWarnings(p.Scope))
}

// reloadLocked is the tail of SaveSettings and ReloadSettings (c.saveMu
// held): it reloads prt.toml and prt.routing.toml from the config dirs (on
// an error the previous configuration stays and the error is returned),
// applies what changed against oldVals, the live values before the save,
// and tells every TUI with an EventSettings. notes are extra warnings for
// the result.
func (c *InProcess) reloadLocked(ctx context.Context, oldVals proto.SettingsValues, notes []string) (proto.SettingsInfo, error) {
	newCfg, err := config.Load(c.d.ConfigDirs)
	if err != nil {
		return c.settingsInfo(ctx), fmt.Errorf("daemon: reload prt.toml (previous settings kept): %w", err)
	}
	newRt, err := config.LoadRouting(c.d.ConfigDirs)
	if err != nil {
		return c.settingsInfo(ctx), fmt.Errorf("daemon: reload prt.routing.toml (previous settings kept): %w", err)
	}
	ch := settings.Diff(oldVals, settings.FromConfig(newCfg, newRt))
	applied, warns, err := c.apply(ctx, ch, newCfg, newRt)
	info := c.settingsInfo(ctx)
	info.Warnings = append(append(info.Warnings, notes...), warns...)
	info.Applied = applied
	c.send(proto.Event{Type: proto.EventSettings})
	return info, err
}

// scopeDir is the directory a save to scope writes.
func (c *InProcess) scopeDir(scope proto.SettingsScope) (string, error) {
	var dir string
	switch scope {
	case proto.ScopeRepo, "":
		dir = c.repoDir()
	case proto.ScopeGlobal:
		dir = c.globalDir()
		// Replay mode (make demo) reads only .git/prt: a global save would
		// write the user's real config and change nothing here.
		if len(c.d.ConfigDirs) > 0 && !slices.Contains(c.d.ConfigDirs, dir) {
			return "", fmt.Errorf("daemon: save settings: the global config (%s) is not read in this mode; save to the repo scope", dir)
		}
	default:
		return "", fmt.Errorf("daemon: save settings: unknown scope %q (want %q or %q)", scope, proto.ScopeRepo, proto.ScopeGlobal)
	}
	if dir == "" {
		return "", fmt.Errorf("daemon: save settings: no %s config directory", cmp.Or(scope, proto.ScopeRepo))
	}
	return dir, nil
}

// shadowWarnings warns, after a global save, about repo files that take
// precedence (config lookup takes the first file found, no merge).
func (c *InProcess) shadowWarnings(scope proto.SettingsScope) []string {
	if scope != proto.ScopeGlobal || c.repoDir() == "" {
		return nil
	}
	var out []string
	for _, name := range []string{config.MainFile, config.RoutingFile} {
		if p := config.Find([]string{c.repoDir()}, name); p != "" {
			out = append(out, fmt.Sprintf("global %s is shadowed by %s", name, p))
		}
	}
	return out
}

// apply makes a reloaded configuration live and queues what its changes
// need (DECISIONS "M5.1 settings page": applied live). Every job it queues
// is automatic, so the strategy gates it when it starts. It returns the
// summary for SettingsInfo.Applied and save warnings.
func (c *InProcess) apply(ctx context.Context, ch settings.Changes, cfg config.Config, rt config.Routing) (string, []string, error) {
	var parts, warns []string
	c.p.SetConfig(cfg)
	// Weights, release branches and the abandoned threshold move urgency:
	// the next discovery pass stores every urgency that changed.
	c.resetUrgencyDays()
	if ch.Models {
		if f := c.settingsDeps().Models; f != nil {
			m, notes := f(cfg)
			c.p.SetModels(m)
			for _, n := range notes {
				c.log.Info("models reloaded", "note", n)
			}
			// The page shows them: a tier disabled by a typo in its spec is
			// a degradation the user must see (spec: failure handling).
			warns = append(warns, notes...)
			parts = append(parts, "models swapped")
		} else {
			warns = append(warns, "model changes apply after restart")
		}
	}
	if ch.Concurrency {
		c.q.setSlots(slotsFor(cfg))
		parts = append(parts, fmt.Sprintf("concurrency %d", c.q.slotCount()))
	}
	var errs []error
	if ch.Routing {
		if err := c.p.SetRouting(rt); err != nil {
			errs = append(errs, fmt.Errorf("daemon: apply routing: %w", err))
		} else {
			n, err := c.reroute(ctx)
			errs = append(errs, err)
			parts = append(parts, "re-routed "+plural(n, "group"))
		}
	}
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		return strings.Join(parts, " · "), warns, errors.Join(append(errs, fmt.Errorf("daemon: apply settings: %w", err))...)
	}
	parts = append(parts, c.requeue(ch, items)...)
	if len(parts) == 0 {
		parts = append(parts, nothingChanged)
	}
	return strings.Join(parts, " · "), warns, errors.Join(errs...)
}

// requeue queues the runs a settings change needs and describes them:
// re-runs (identity, grouping) or re-aggregations (weights, unsure_below)
// of analysed items, and a strategy change's newly allowed items.
func (c *InProcess) requeue(ch settings.Changes, items []model.Item) []string {
	auto := c.strategy()
	var jobs []job
	rerun, reagg := 0, 0
	for _, it := range items {
		if it.Source == model.SourcePreview || it.AnalysedSHA == "" {
			continue
		}
		switch {
		case (ch.Identity || ch.Grouping) && auto.Allows(it):
			jobs = append(jobs, job{itemID: it.ID, urgency: it.Urgency, rerun: true})
			rerun++
		case ch.Identity || ch.Grouping || ch.Aggregate:
			jobs = append(jobs, job{itemID: it.ID, urgency: it.Urgency, rerun: true, aggOnly: true})
			reagg++
		}
	}
	queued := 0
	if ch.Strategy {
		for _, id := range c.pendingIDs(items) {
			if it := findItem(items, id); it != nil && auto.Allows(*it) {
				jobs = append(jobs, job{itemID: id, urgency: it.Urgency})
				queued++
			}
		}
	}
	c.q.push(jobs...)
	var parts []string
	if rerun > 0 {
		parts = append(parts, "re-ran "+plural(rerun, "item"))
	}
	if reagg > 0 {
		parts = append(parts, "re-aggregated "+plural(reagg, "item"))
	}
	if queued > 0 {
		parts = append(parts, plural(queued, "item")+" queued")
	}
	return parts
}

func findItem(items []model.Item, id string) *model.Item {
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
	}
	return nil
}
