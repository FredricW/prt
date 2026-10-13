package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/charmbracelet/log"

	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// defaultDiscoverConcurrency is ClientOptions.DiscoverConcurrency's default:
// at most this many repos run discovery (and its GitHub calls) at once.
const defaultDiscoverConcurrency = 4

// eventBuffer is the merged event channel's buffer, as InProcess's.
const eventBuffer = 256

// Member is one repository of a workspace and the client that serves it.
type Member struct {
	// Info describes the repo (Describe); Info.Key must be unique and
	// non-empty.
	Info proto.RepoInfo
	// Client serves the repo with repo-local item ids.
	Client daemon.Client
}

// ClientOptions configures New.
type ClientOptions struct {
	// Pool and Ledger are shared with the members (Workspace reports their
	// size and total); nil reports 0.
	Pool   *daemon.Pool
	Ledger *daemon.Ledger
	// Notes are discovery notes reported by Workspace.
	Notes []string
	// DiscoverConcurrency caps the members running Discover at once (≤ 0:
	// 4).
	DiscoverConcurrency int
	// Log receives member failures; nil discards them.
	Log *log.Logger
}

// Optional member capabilities (InProcess has all of them).
type (
	syncRunner interface {
		RunSync(ctx context.Context, itemID string) error
	}
	pendingAnalyser interface {
		AnalysePending(ctx context.Context) error
	}
	settingsReloader interface {
		ReloadSettings(ctx context.Context) (proto.SettingsInfo, error)
	}
	repoInfoer interface {
		RepoInfo() proto.RepoInfo
	}
)

// Client multiplexes the members' clients into one daemon.Client (see the
// package doc for the id mapping). It is safe for concurrent use; the
// members are fixed by New and kept in key order (the workspace order).
type Client struct {
	root    string
	members []Member
	byKey   map[string]int
	pool    *daemon.Pool
	ledger  *daemon.Ledger
	notes   []string
	discN   int
	log     *log.Logger

	// itemErrs holds each repo's last Items failure (guarded by mu).
	mu       sync.Mutex
	itemErrs map[string]error

	events     chan proto.Event
	done       chan struct{} // closed by Close: forwarders stop
	fwd        sync.WaitGroup
	eventsOnce sync.Once
	closeOnce  sync.Once
	closeErr   error
}

var _ daemon.Client = (*Client)(nil)

// New returns the workspace client of the repos under root. Members are
// ordered by key; their event streams are merged from now on.
func New(root string, members []Member, o ClientOptions) *Client {
	ms := append([]Member(nil), members...)
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Info.Key < ms[j].Info.Key })
	c := &Client{
		root: root, members: ms, byKey: make(map[string]int, len(ms)),
		pool: o.Pool, ledger: o.Ledger, notes: append([]string(nil), o.Notes...),
		discN: o.DiscoverConcurrency, log: o.Log, itemErrs: map[string]error{},
		events: make(chan proto.Event, eventBuffer), done: make(chan struct{}),
	}
	if c.discN <= 0 {
		c.discN = defaultDiscoverConcurrency
	}
	if c.log == nil {
		c.log = log.New(io.Discard)
	}
	for i, m := range ms {
		if _, dup := c.byKey[m.Info.Key]; !dup {
			c.byKey[m.Info.Key] = i
		}
	}
	c.startEvents()
	return c
}

// Members returns the members in workspace order.
func (c *Client) Members() []Member { return append([]Member(nil), c.members...) }

// label is how messages name a member: its label, else its key.
func label(m Member) string {
	if m.Info.Label != "" {
		return m.Info.Label
	}
	return m.Info.Key
}

// route splits a workspace item id into its member and repo-local id.
func (c *Client) route(id string) (Member, string, error) {
	key, local := model.SplitRepoItemID(id)
	if key == "" {
		return Member{}, "", fmt.Errorf("workspace: item id %q has no repo prefix", id)
	}
	i, ok := c.byKey[key]
	if !ok {
		return Member{}, "", fmt.Errorf("workspace: unknown repo %q", key)
	}
	return c.members[i], local, nil
}

// memberFor is the member a Repo field names: Resolve(repo), or the first
// member when repo is "".
func (c *Client) memberFor(repo string) (Member, error) {
	if repo != "" {
		return c.Resolve(repo)
	}
	if len(c.members) == 0 {
		return Member{}, errors.New("workspace: no repositories")
	}
	return c.members[0], nil
}

// info is a member's live description: its client's RepoInfo when it has
// one (live role and spend), with Key, Label, Status, Note, Root, GitDir and
// GHRepo taken from Member.Info where the live one is empty.
func (c *Client) info(m Member) proto.RepoInfo {
	ri, ok := m.Client.(repoInfoer)
	if !ok {
		r := m.Info
		r.SpentUSD = c.ledger.Repo(m.Info.Key)
		return r
	}
	r := ri.RepoInfo()
	fill := func(dst *string, src string) {
		if *dst == "" {
			*dst = src
		}
	}
	fill(&r.Key, m.Info.Key)
	fill(&r.Label, m.Info.Label)
	fill(&r.Note, m.Info.Note)
	fill(&r.Root, m.Info.Root)
	fill(&r.GitDir, m.Info.GitDir)
	fill(&r.GHRepo, m.Info.GHRepo)
	if r.Status == "" {
		r.Status = m.Info.Status
	}
	return r
}

// Workspace implements daemon.Client: Multi, the root, every repo in
// workspace order (a repo whose last Items call failed notes it), the
// shared pool's size, the ledger's total and the discovery notes.
func (c *Client) Workspace(_ context.Context) (proto.WorkspaceInfo, error) {
	repos := make([]proto.RepoInfo, 0, len(c.members))
	c.mu.Lock()
	errs := make(map[string]error, len(c.itemErrs))
	for k, v := range c.itemErrs {
		errs[k] = v
	}
	c.mu.Unlock()
	for _, m := range c.members {
		r := c.info(m)
		if err := errs[m.Info.Key]; err != nil {
			r.Note = "items: " + err.Error()
		}
		repos = append(repos, r)
	}
	slots := 0
	if c.pool != nil {
		slots = c.pool.Slots()
	}
	return proto.WorkspaceInfo{
		Multi: true, Root: c.root, Repos: repos, Concurrency: slots,
		SpentUSD: c.ledger.Total(), Notes: append([]string(nil), c.notes...),
	}, nil
}

// setItemsErr records (or clears, with nil) a repo's Items failure.
func (c *Client) setItemsErr(key string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		delete(c.itemErrs, key)
		return
	}
	c.itemErrs[key] = err
}

// Close implements daemon.Client: it closes every member once (in
// parallel), ends the merged event stream and returns the members' errors
// joined. Later calls return the same result.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		errs := fanOut(context.Background(), c.members, 0, func(_ context.Context, m Member) (struct{}, error) {
			return struct{}{}, m.Client.Close()
		}).errs(c.members)
		c.closeErr = errors.Join(errs...)
		c.fwd.Wait()
		c.closeEvents()
	})
	return c.closeErr
}

// fanResult is the outcome of fanOut: one value and one error per member,
// by index.
type fanResult[T any] struct {
	vals []T
	err  []error
}

// errs returns the non-nil errors, each prefixed with its member's label.
func (r fanResult[T]) errs(ms []Member) []error {
	var out []error
	for i, err := range r.err {
		if err != nil {
			out = append(out, fmt.Errorf("%s: %w", label(ms[i]), err))
		}
	}
	return out
}

// fanOut calls fn for every member in parallel, at most limit at once
// (≤ 0: no limit), and waits for all of them. A member still waiting for
// a slot when ctx ends gets ctx's error without being called.
func fanOut[T any](ctx context.Context, ms []Member, limit int, fn func(context.Context, Member) (T, error)) fanResult[T] {
	res := fanResult[T]{vals: make([]T, len(ms)), err: make([]error, len(ms))}
	if limit <= 0 {
		limit = len(ms)
	}
	sem := make(chan struct{}, max(limit, 1))
	var wg sync.WaitGroup
	for i, m := range ms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				res.err[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			res.vals[i], res.err[i] = fn(ctx, m)
		}()
	}
	wg.Wait()
	return res
}
