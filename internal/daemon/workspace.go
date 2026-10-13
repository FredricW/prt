package daemon

// M5.3 shared contracts (multi-repo workspaces; DECISIONS "M5.3 shared
// contracts"). A workspace runs one InProcess per repo, each with its own
// store, log and config under its own .git/prt, behind the workspace client
// in internal/workspace. The pieces here let those InProcess clients share
// one worker pool (Pool) and one cost ledger (Ledger), and let the daemon of
// a workspace live outside any repo (WorkspaceStateDir, WorkspaceSocketPath).
//
// The lead fixed these signatures. The shared worker pool is in pool.go,
// the spend counting rule in spend.go, and NewInProcessWith (the one
// constructor) in inprocess.go.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/settings"
)

// Ledger sums the model spend (RunLog.Cost of finished stage rows: Jev,
// embeddings, reviews, TL;DR and titles; each stage counted once, see
// spendMeter) of every run of the clients that share it, per repo key,
// since the process started. Safe for concurrent use. It is session
// accounting for the UI; per-item cost caps are still enforced by each
// repo's pipeline.
type Ledger struct {
	mu     sync.Mutex
	total  float64
	byRepo map[string]float64
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger { return &Ledger{byRepo: map[string]float64{}} }

// Add records usd spent by repo (ignored when usd ≤ 0).
func (l *Ledger) Add(repo string, usd float64) {
	if l == nil || usd <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byRepo == nil { // a zero Ledger works too
		l.byRepo = map[string]float64{}
	}
	l.total += usd
	l.byRepo[repo] += usd
}

// Total is the spend of every repo.
func (l *Ledger) Total() float64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

// Repo is the spend of one repo key.
func (l *Ledger) Repo(key string) float64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byRepo[key]
}

// Options configures NewInProcessWith (M5.3).
type Options struct {
	// Pool is the worker pool shared with the workspace's other repos; nil
	// keeps a private queue sized by Config.Pipeline.Concurrency.
	Pool *Pool
	// Ledger receives this client's spend under Repo.Key; nil gives the
	// client a private ledger.
	Ledger *Ledger
	// Repo is what Workspace reports for this client (Key "" in single-repo
	// mode). cmd/prt fills it (label, root, git dir, GitHub status, role).
	Repo proto.RepoInfo
}

// wsState is the M5.3 part of InProcess (guarded by its own mutex).
type wsState struct {
	mu     sync.Mutex
	repo   proto.RepoInfo
	pool   *Pool
	ledger *Ledger
}

// SetRepoInfo replaces what Workspace reports about this client's repo
// (cmd/prt calls it once the GitHub status is known).
func (c *InProcess) SetRepoInfo(r proto.RepoInfo) {
	c.ws.mu.Lock()
	c.ws.repo = r
	c.ws.mu.Unlock()
}

// RepoInfo returns this client's repo description with its current role
// and spend.
func (c *InProcess) RepoInfo() proto.RepoInfo {
	c.ws.mu.Lock()
	r, l := c.ws.repo, c.ws.ledger
	c.ws.mu.Unlock()
	if r.Role == "" {
		r.Role = c.cfg().User.Role
	}
	r.SpentUSD = l.Repo(r.Key)
	return r
}

// Workspace implements Client: a single-repo client describes its one repo
// (Multi false).
func (c *InProcess) Workspace(_ context.Context) (proto.WorkspaceInfo, error) {
	r := c.RepoInfo()
	c.ws.mu.Lock()
	pool, l := c.ws.pool, c.ws.ledger
	c.ws.mu.Unlock()
	slots := c.q.slotCount()
	if pool != nil {
		slots = pool.Slots()
	}
	return proto.WorkspaceInfo{Root: r.Root, Repos: []proto.RepoInfo{r}, Concurrency: slots, SpentUSD: l.Total()}, nil
}

// ReloadSettings re-reads prt.toml and prt.routing.toml from the client's
// config dirs and applies what changed live, exactly as SaveSettings does
// after writing (M5.3: a global save made through one repo of a workspace
// is applied to the others with this). An EventSettings follows. It writes
// nothing and is serialised with SaveSettings. Applied says what changed
// ("nothing changed" when nothing did); on a load error the previous
// configuration stays and the error is returned. A client without config
// dirs (its configuration was not read from files) has nothing to reload:
// it answers "nothing changed" and sends no event.
func (c *InProcess) ReloadSettings(ctx context.Context) (proto.SettingsInfo, error) {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	if len(c.d.ConfigDirs) == 0 {
		info := c.settingsInfo(ctx)
		info.Applied = nothingChanged
		return info, nil
	}
	return c.reloadLocked(ctx, settings.FromConfig(c.cfg(), c.p.Routing().Config), nil)
}

// Quartile maps an urgency score to 0–4 urgency-bar cells relative to the
// top score (the rule Items uses), so the workspace client can rank the
// items of every repo on one scale.
func Quartile(u, top float64) int { return quartile(u, top) }

// SortItems orders items the way Items does: section, then urgency
// (descending), then most recently updated, then id.
func SortItems(items []model.Item) { sortItems(items) }

// WorkspaceStateDir is the state directory of the workspace rooted at root:
// <config.StateDir()>/ws-<first 16 hex of sha256(clean absolute root)>
// (falls back to the temp dir when no state dir is known). The workspace
// daemon keeps its socket and the workspace log there; each repo's own
// store, log and config stay under its .git/prt.
func WorkspaceStateDir(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	base := config.StateDir()
	if base == "" {
		base = filepath.Join(os.TempDir(), "prt-state")
	}
	return filepath.Join(base, "ws-"+hex.EncodeToString(sum[:8]))
}

// WorkspaceSocketPath is the workspace daemon's socket:
// WorkspaceStateDir(root)/prtd.sock, or, when that path is too long for a
// Unix socket, $TMPDIR/prt-ws-<hash>/prtd.sock. Daemon and TUI derive it the
// same way.
func WorkspaceSocketPath(root string) string {
	p := filepath.Join(WorkspaceStateDir(root), "prtd.sock")
	if len(p) <= maxSocketPath {
		return p
	}
	return filepath.Join(os.TempDir(), "prt-"+filepath.Base(filepath.Dir(p)), "prtd.sock")
}
