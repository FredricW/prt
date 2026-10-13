# prt architecture

Source of truth order: `docs/spec.md` → `docs/DECISIONS.md` → this file.
This file maps the spec onto Go packages so milestones M0–M5 can be built in
parallel on disjoint packages. Each package lists its **owner milestone**,
what it may import, and its status.

## Process model

```
             ┌──────────── prt (TUI) ────────────┐
             │ internal/tui  ── daemon.Client ───┼──► InProcess (--no-daemon, tests, snapshot)
             └───────────────────────────────────┘        │
                         │ NDJSON over .git/prt/prtd.sock │
                         ▼                                ▼
             prt daemon ── daemon.Serve ──► pipeline.Pipeline ──► store.Store (.git/prt/prt.db)
                                              │
                    ┌──────────┬──────────┬───┴─────┬───────────┬───────────┐
                  gitx.Git  gh.Client  jev.Client  embed.Embedder  review.Reviewer
                  (local)   (GitHub)   (AI Gateway) (Ollama/ONNX)   (Anthropic)
```

Every arrow at the bottom is an interface with a fake in the same package.
Nothing outside those five packages makes a network call or runs `git`.

**Workspace (M5.3).** `prt --root DIR` (or prt started outside a git
repository) discovers the repositories under DIR (`workspace.Discover`, a
filesystem walk) and builds one `daemon.InProcess` per repo, exactly as in
single-repo mode (its own store, log and config under its own `.git/prt`),
sharing one `daemon.Pool` and one `daemon.Ledger`. `workspace.Client`
multiplexes them into a single `daemon.Client` with repo-qualified item ids
(`<repoKey>::<localId>`), so the TUI and `daemon.Serve` use it like a
single-repo client:

```
prt (TUI) ── daemon.Client ──► workspace.Client ──┬─► InProcess (repo a) ──► .git/prt of a
             NDJSON over $XDG_STATE_HOME/prt/      ├─► InProcess (repo b) ──► .git/prt of b
             ws-<hash>/prtd.sock (prt daemon --root)└─► … one shared Pool and Ledger
```

## Dependency rules

- `internal/model` imports nothing from prt. Everything may import it.
- Boundary packages (`gitx`, `gh`, `jev`, `embed`, `review`, `store`) import
  only `model` (and `config` types where noted). They never import each other.
- Pure stage packages (`diff`, `parse`, `labels`, `link`, `cluster`, `route`,
  `aggregate`, `questions`) do no I/O except through an interface passed in.
- Label encoding (values, P, scores, sub-questions, subjects) is the shared
  contract in `internal/model/labels.go`.
- `pipeline` is the only package that wires stages to boundaries.
- `daemon` wraps `pipeline` + `store` behind `daemon.Client`; `tui` talks only
  to `daemon.Client` and `proto` types, never to `store` or a boundary.
- `workspace` (M5.3) composes `daemon.Client`s: it imports `daemon`, `proto`,
  `model`, `config` and `gh` (for `ParseRemote` only), never `store`,
  `pipeline` or another boundary, and runs no `git` (discovery reads `.git`
  entries from the filesystem). `tui` still sees only `daemon.Client`.
- `cmd/prt` builds real implementations from env/config and hands them down.
- `internal/deps` (build tag `tools`) pins every third-party module; add new
  modules there first so parallel work never runs `go get` concurrently.

## Packages

| Package | Owner | Purpose | Status |
| --- | --- | --- | --- |
| `cmd/prt` | M0 | flags (`--repo --no-daemon --offline --fixtures --snapshot --now --jev-replay --embed-replay --review-replay --drafts`), `daemon` subcommand (socket + periodic discovery), wiring from env (reviewers, `Deps.ConfigDirs`), browser opener; M5.1 `buildModels` (startup clients and the daemon's settings `ModelFactory`), `SetSettingsDeps`; M5.2 `tui.Options.ReloadOnTick` when attached to a daemon; M5.3 workspace mode (`workspace.go`: `--root`, `--depth`, a directory outside any repo; per-repo `setup` on a shared pool and ledger, one `gh auth token`, `--fixtures` manifest, `prt daemon --root` on `WorkspaceSocketPath`, workspace log in the state dir), `runTUI` with the persisted pane widths (`uistate`), single-repo snapshot golden | M5.3 |
| `internal/model` | M0 | domain types (spec "Data model"), `UnitID`, `GroupID`, `ContentHash`, aggregates; M5.2 `ChangeSurface`/`SurfaceEntry` (`ItemAgg.Surface`), `ItemAgg.HeadlinesUnsure` and `HeadlineCandidates`; M5.3 `RepoIDSep`, `RepoItemID`/`SplitRepoItemID`, `CleanRepoKey`, `Item.Repo` (set by the workspace client, never stored) | done |
| `internal/config` | M0 | `prt.toml`, `prt.routing.toml` (+ raw `prt.questions.toml`), lookup `.git/prt` → XDG → builtin, unknown keys warned about (`Config.Warnings`); M5.3 `[workspace] depth/exclude` (global file only), `StateDir()` | done |
| `internal/gitx` | M0 | `Git` interface, `Exec` (git CLI), `Fake`; M5.3 `ErrNotWorkTree` | done |
| `internal/gh` | M0/M4 | `Client` interface, `API` (go-github + githubv4; token from `gh auth token`), `Fake`, `ParseRemote` | `Viewer`, `SearchPRs`, `ListOpenPRs` (with changed paths, for owned globs), `PR` (with the viewer's latest review commit and time), `PRDiff`, `CODEOWNERS` done; fake + `LoadFixture`; M4 `Threads` (GraphQL, with original line/commit), `SubmitReview` (REST, then a best-effort id lookup), `Reply`, `ResolveThread`, `CreatePR` done |
| `internal/store` | M0 | `Store` interface, `SQLite` (JSON docs table + cache + runlog, `PRAGMA user_version` migrations), `OpenMemory` for tests; M5.2 `docs_kind_parent_id` index (migration 3), a pool of 4 read-only connections in WAL mode, `ItemSubjects`/`PruneLabels`, `DeleteReview`, `CurrentRunLogs`/`PruneRunLogs`, `CacheDelete` | M5.2 |
| `internal/diff` | M0 | unified diff parser, hunk/line numbering, `IntraLine` char diff, `Pair` | done |
| `internal/proto` | M0 | NDJSON `Message`, `Event`, `Detail`, encoder/decoder; M5.2 `DetailParams{ItemID, NoLabels}`, `MethodLabels`/`LabelsParams`, `Detail.LabelsOmitted`, `Detail.Run *RunStatus{Stage, Snapshot, SnapshotSHA, Reviews}`; M5.3 `MethodWorkspace` → `WorkspaceInfo{Multi, Root, Repos, Concurrency, SpentUSD, Notes}`, `RepoInfo`, `RepoStatus`, `Repo` on search, settings, routing params and events | done |
| `internal/daemon` | M0 | `Client` interface, `InProcess` (discovery, worker pool, checkout, group edits, review actions, routing ensure/reload, call log, per-item cap; M4 `SaveComment`, `CommentAction` (accept/dismiss/reply/resolve), `SubmitReview` on the units' head, `CreatePR` with draft attachment, `MarkViewed`, pending-draft recovery), `Serve`/`Dial` over the socket; M5 `Search` (local/open/closed), `Preview`/`RemovePreview`, aggregate-only `Override`, Detail with overrides applied and the branch block aged; M5.1 auto-review gate (explicit vs automatic jobs, FreeOnly runs, `RequestedSHA`), `Stop`/`Resume` (cancel cause `pipeline.ErrStopped`), `Settings`/`SaveSettings` with live reload; M5.2 `DetailOf` (lean Detail without labels) and `Labels` (per unit, overrides applied), in-memory snapshot of the last complete analysis served during a re-run (`Run.Snapshot`, at most 8), stop records that keep `FromStage`/`Force` for `Resume`, label pruning on remove and at start, discovery that skips age-only rewrites, run-log pruning (5 runs); M5.3 `Client.Workspace`, `NewInProcessWith(deps, Options{Pool, Ledger, Repo})`, `Pool` (one worker pool shared by a workspace's queues, most urgent head of any repo first; `pool.go`), `Ledger` (session spend per repo; counting rule in `spend.go`), `SetRepoInfo`/`RepoInfo`, `ReloadSettings`, `Quartile`/`SortItems`, `WorkspaceStateDir`/`WorkspaceSocketPath` | M5.3 |
| `internal/diffview` | M0 | plain unified diff viewer component (lazy row rendering, file navigation, intra-line highlight) | done |
| `internal/tui` | M0→M5 | root model, picker (stage strip while analysing), Overview (TL;DR line, `w` weights overlay)/Code tabs, Pipeline page (`P`, via `pipePane`), help overlay, rebindable keys, `$EDITOR` hook, `Snapshot` (no-TTY render); M4 Overview "Since you looked" block, submit-review / create-PR forms (Huh), comment ops to the client, draft count in the status bar; M5 design shell (header, Queue box, tabs, hints), `/` search-and-add with `tab` preview (discarded through `RemovePreview`), `x` remove, `space` marks, `:` command line, Architecture tab (`3`), branch-state picker lines and Overview box, `u`/`e`/`o` wiring; M5.1 stop/resume keys, not-analysed/stopped states, settings page (`,`, `TabSettings`); M5.2 Detail loader (16-item LRU, one call per item in flight, 150 ms debounce with 1 s max wait, cancellation, stale-while-revalidate with the `↻ updating review` note), labels on demand (per item/head/unit cache), tab-bar hit-test (`tabbar.go`), capped Overview blocks with block focus (`J`/`K`, `]`/`[`) and the full-list overlay, API endpoints / Database / Feature flags & config boxes from `surface.Rows`, coloured composition bar and minimap legends, `Options.ReloadOnTick`; M5.3 repo-grouped picker in a workspace (`pickerws.go`: repo headers with counts and GitHub status, folds `z`/`Z`, filter `F`/`:repo`, repo card), workspace header and active-repo routing of settings/`:auto`/routing (`workspace.go`), resizable picker and Code list (`resize.go`: border drag, `<`/`>`, clamps, `Options.Layout`/`SaveLayout`), workspace snapshot screens | M5.3 |
| `internal/settings` | M5.1 | settings page backend: `FromConfig`, `Diff`, `Validate`, comment-preserving `Save` of prt.toml / prt.routing.toml, `EnsureQuestions`, environment presence (`Env`, `HTTPProber` for Ollama) | M5.1 |
| `internal/settingsview` | M5.1 | settings page component (Huh form over `proto.SettingsValues`, scope choice, env lines, edit-file actions); M5.3 workspace line, per-repo/global scope labels, "saves to" line, per-repo weights note | M5.3 |
| `internal/searchview` | M5 | the `/` prompt component: per-source `Searcher` calls merged as they land, 250 ms debounce for GitHub sources, result rows, `PendingChoice` (enter add / tab preview); M5.3 a hit's repo in its tail | M5.3 |
| `internal/cmdline` | M5 | `:` command line: `Parse` (`:add`, `:tier`, `:cap`, `:q`; M5.1 `:auto`, `:stop`, `:resume`, `:settings`), `Complete` (tab), `Help`, input with history, `PendingCommand`; M5.3 `:repo` with repo-name completion (`SetRepos`), `HelpEntry.Workspace` for the workspace-only forms | M5.3 |
| `internal/workspace` | M5.3 | multi-repo mode: `Discover` (filesystem walk with depth, excludes, skipped node_modules/vendor/dot dirs, worktree dedupe, symlink-loop safety), `Describe` (repo key, label, GitHub status), `ParseQualified`/`Resolve` (`owner/name#N`, `repo:ref`), `Client` multiplexing one `daemon.Client` per repo (id prefixing, merged ranking and events, fan-out stop/search/settings, `RunSync`, `AnalysePending`) | M5.3 |
| `internal/uistate` | M5.3 | per-user TUI layout state: `Layout{SidebarWidth, CodeListWidth}` in `$XDG_STATE_HOME/prt/ui.json`, `Load` (missing or bad file → automatic widths), atomic `Save` | M5.3 |
| `internal/archview` | M5 | Architecture tab component: layered module graph (or hop list), Contracts and net-effect boxes, depth `+`/`-`, diff-only `d`, file list and `PendingJump` | M5 |
| `internal/arch` | M5 | `Build`: module graph (modules = source directories) from `link.ImportGraph` at base and head, hop distance, contracts, code-written net effect | M5 |
| `internal/highlight` | M5 | tree-sitter syntax classes for one line (`Line`, LRU-cached) or a whole file (`Lines`) | M5 |
| `internal/theme` | M5 | palette roles from the design canvas (adaptive colours, 256/16-colour fallbacks), `Box`, `Divider`, `UrgencyBar`, `Chips`, `Tab`, `Hints`; M5.2 composition kind colours (`Palette.Kind*`, `KindColour`, `Kind`) | M5.2 |
| `internal/pipeview` | M3 | Pipeline page component: stage strip + per-stage log, routing table, totals/rules/grouping/calibration panels, call log and cap prompt overlays; actions D s r e c $ via `PendingAction`; `StageStrip` for the picker; M5.2 live `Run.Reviews` rows and an "updating · <stage>" line during a snapshot | M5.2 |
| `internal/pipeline` | M0→M4 | stage orchestration, caching, RunLog events, degradation; stages 6–8 (route with user overrides, planned review dispatch per tier with retry, escalation and calibration, AI group titles, TL;DR, review totals); M4 thread loading and merge, re-anchoring (with `Item.UnitsSHA`), ai-draft sync, "Since you looked" (changed units, `comment_addressed`); M5 branch block (`branch.state`, abandoned? default, `AgeBranchInfo`, SupersededBy, suggested PR title), aggregate-only runs, calibration rates, module graph; M5.1 per-run config/model snapshot (`SetConfig`/`SetModels`), `FreeOnly` runs with guard models, user stop (`ErrStopped` → `StageStopped`/`ReviewStopped`); M5.2 stage-8 change surface (`stageFuncs.surface`, `surface.Build` over the units, contract labels and `Git.Show`), one retry of a truncated review with `review.Grow` (own recording key, reserved at `EstimateCost`) | M5.2 |
| `internal/parse` | M1 | tree-sitter split at function boundaries, symbol/signature, shape hash, format detection, `Lang` | hunk fallback only |
| `internal/questions` | M1 | catalogue types, validation, `Wire()` to `jev.Question`, embedded `default.toml` (M5.2: `breaking`, `contract`, `depth` and `pii_or_secret` reworded with criteria) | types done; catalogue partial |
| `internal/jev` | M1 | `Client`, `HTTP` (AI Gateway `/v1/evaluate`), `Fake`, `Replay`, `Recorder`, `RequestKey` | done (batching lives in `labels`) |
| `internal/labels` | M1 | stage 4 labels: per-unit and per-line-batch Jev requests from the catalogue, token-limit splitting, answer → `model.Label` mapping, unknown on `ErrUnavailable` | M1 |
| `internal/codeview` | M1/M3 | Code page component: mode A by file (file list, chips, gutter marks, noise collapse, filters, `L` overlay, inline findings); mode B by change (groups, why links, mechanical example, merge/split, routing meta, findings block, `R` re-review); M4 comments layer: composer (C/V), inline drafts, ai-drafts (a/d/!), thread blocks and side panel (t, reply, resolve), Comments strip; M5 side-by-side (`u`, ≥140-column terminal), syntax colour, `e`/`o` open requests, design alignment; M5.2 labels on demand (`PendingLabels`/`SetLabels`/`LabelsFailed`, "loading labels…"); M5.3 resizable list (`SetListWidth`/`ListWidth`, border drag with `Dragging`/`PendingListWidth`, `ListWidthStep`) | M5.3 |
| `tools/recordjev` | M1/M3 | runs the pipeline over fixture PRs with a recording, budget-guarded Jev client (or, with `--review`, budget-guarded Anthropic reviewers over replayed Jev); writes `testdata/recordings/{jev,review}/` and `SPEND.md`; shares `pipelinetest.FixtureRun`, `Problems`, `Expectations` and `ReviewExpectations` with the replay tests; M5.2 truncated responses recorded and counted as billed, a failed routing row fails the run, `--unused [--prune]` lists or deletes recordings nothing reads | M5.2 |
| `internal/aggregate` | M1/M3 | spec "Ranking and aggregation" arithmetic, urgency (label terms from an `ItemAgg`, `Input.Prior` for discovery) + "why", hot-spot reasons from findings, group kind/hot spot/findings; M5.2 headlines at P ≥ 0.60 (one row per unit, external contracts merged per file; candidates at 0.50–0.60 in `HeadlineCandidates`, one per unlisted unit), the urgency breaking term from breaking headline rows, unsure over the attention/risk labels at decision confidence (`UnsureQuestions`), attention mapping tuned against the ground truth | M5.2 |
| `internal/surface` | M5.2 | structured change record for the Overview: `Build(ctx, Input) *model.ChangeSurface` detects API endpoints (Go net/http/chi/gin/echo/gorilla/fiber, express/hono/koa/fastify/Next.js, flask/fastapi/django, with router prefixes), database changes (SQL DDL, ORM migrations, schema files) and flags/env/config keys, merged with Jev `contract` labels at P ≥ .60; `Source` (file reader, `gitx.Git` fits), `Rows`, `Empty`; per-framework fixtures under `testdata/<framework>/cases.txt` | M5.2 |
| `internal/quality` | M5.2 | label-quality scoring: strict ground-truth parsing (`testdata/groundtruth/`), per-item metrics (needs-eyes precision/recall, headline count and kind-aware precision/recall, hot spots in top 3, fidelity, the footer's and the all-labels unsure shares, lines to read), deterministic markdown/JSON board, `Explain`; runs the pipeline offline through `pipelinetest` with strict replay; `TestScoreboardRegression` against `testdata/thresholds.toml` | M5.2 |
| `tools/quality` | M5.2 | `make quality`: prints the scoreboard (`-json`, `-explain`, `-groundtruth`, `-recordings`, `-fixtures`, `-review-misses`) | M5.2 |
| `internal/shape` | M2 | mechanical class key: shape hash + literal edit signature (`Key`, `EditSig`), shared by link, pipeline and cluster | M2 |
| `internal/link` | M2 | structural links (symbol index per base SHA), embed kNN candidates, mechanical candidates, `UnitText` | M2 |
| `internal/judge` | M2 | non-unit Jev questions: pair `relation` (+ `Confirm`), group `name_bullet` (+ `ApplyNames`), file `separate_pr` (+ `Suggestion`); M4 `CommentAddressed` (`comment_addressed` per earlier comment, `AddressedCtx` cache key); answers are labels on pair/group/file/comment subjects | M4 |
| `internal/embed` | M2 | `Embedder`, `Ollama` (`/api/embed`), `ONNX` stub, `Fake`, `Cosine`, `FromSpec`, `Replay`/`Recorder` over `testdata/recordings/embed` | M2 (ONNX TODO) |
| `internal/cluster` | M2 | components, splitting (weakest Jev links, then structural bridges), mechanical groups, review order, placeholder names, user edits | M2 |
| `internal/route` | M3 | `when` lexer/parser and Kleene evaluator, `Compile`, lazy group `Env`, `Route` (first match, prompts, automatic context), `Gather` (24 KiB context blocks), `Plan`/`Budget` (hot-spot order under the cost cap) | done |
| `internal/review` | M3 | `Reviewer`, `Tiers`, `Anthropic` Messages adapter (`PRT_ANTHROPIC_API_KEY`), prompt builders (review, TL;DR, title), `ParseReview`/`AnchorFindings`/`CleanTLDR`/`CleanTitle`, price table and estimates (published prices, see DECISIONS "Confirmed model prices"), `Replay`/`Recorder` over `testdata/recordings/review`, `Fake`; M5.2 output caps fast/deep 4096, TL;DR 2048, titles 256, `ErrTruncated` on an unusable `max_tokens` stop, `Grow` (cap doubled, ceiling 8192), truncations recorded and replayed | done |
| `internal/comments` | M4 | pure comment logic: anchor/re-anchor by unit id, merge GitHub threads, findings → ai-drafts, submission selection, view marks and "Since you looked" arithmetic | done (M4 lead) |
| `internal/testutil` | all | `RepoRoot`, `Testdata`, `FixtureRepo` (generates a fresh copy into `t.TempDir()`), `RecordingsDir`; M5.3 `FixtureWorkspace`, `FixtureWorkspaceManifest` | done |
| `internal/fixturegen` | pre-M1/M4 | `Generate`: builds a fixture repo from `testdata/fixtures/{base,prs/*.patch}` with fixed dates; earlier heads from `history/<branch>.<label>.patch` at `refs/fixture/<branch>/<label>`; M5.3 `GenerateWith`/`Options{OriginURL}`, optional `base-message.txt`, `Manifest`/`LoadManifest`/`PRsFor`, `GenerateWorkspace` (repos, plain dirs, decoy repos, symlinks) | done |
| `tools/fixturegen` | pre-M1 | CLI over `internal/fixturegen` for `testdata/fixture-repo`; M5.3 `-workspace manifest.json` for `testdata/fixture-workspace` | done |

## Core interfaces (abridged; see code for docs)

```go
// gitx
type Git interface {
    Root() string
    GitDir(ctx) (string, error)
    DefaultBranch(ctx) (string, error)
    RemoteURL(ctx, remote string) (string, error)
    Refs(ctx, remotes bool) ([]Ref, error)
    ResolveRef(ctx, rev string) (string, error)
    MergeBase(ctx, a, b string) (string, error)
    Diff(ctx, base, head string) (string, error)
    CountAhead(ctx, base, head string) (int, error)
    Log(ctx, base, head string, limit int) ([]Commit, error)
    Show(ctx, rev, path string) ([]byte, error)
    ListFiles(ctx, rev string) ([]string, error)
    IsDirty(ctx) (bool, error)
    Checkout(ctx, ref string) error
}

// gh
type Client interface {
    Viewer(ctx) (string, error)
    SearchPRs(ctx, query string, limit int) ([]PR, error)
    PR(ctx, number int) (PR, error)
    PRDiff(ctx, number int) (string, error)
    Threads(ctx, number int) ([]Thread, error)
    SubmitReview(ctx, number int, r ReviewSubmission) (SubmittedReview, error)
    Reply(ctx, threadID, body string) (model.Comment, error)
    ResolveThread(ctx, threadID string) error
    CreatePR(ctx, p NewPR) (PR, error)
    CODEOWNERS(ctx, ref string) (string, error)
}

// jev
type Client interface { Evaluate(ctx, Request) (Response, error) }

// embed
type Embedder interface {
    Embed(ctx, texts []string) ([][]float32, error)
    Model() string
}

// review
type Reviewer interface {
    Review(ctx, Request) (Result, error)
    Model() string
}

// store: items, fetch cache, per-item outputs (files, units, links, groups,
// agg), unit-keyed labels/embeddings/overrides, group-keyed reviews,
// comments, append-only run log, generic CacheGet/CachePut. See store.go.

// daemon
type Client interface {
    Discover(ctx) error
    Items(ctx) ([]model.Item, error)
    Detail(ctx, itemID string) (proto.Detail, error)
    DetailOf(ctx, proto.DetailParams) (proto.Detail, error)  // M5.2: NoLabels; Run/Snapshot
    Labels(ctx, proto.LabelsParams) ([]model.Label, error)   // M5.2: labels on demand
    Refresh(ctx, proto.ItemParams) error
    Add(ctx, query string) (model.Item, error)
    Remove(ctx, itemID string) error
    Checkout(ctx, proto.CheckoutParams) (proto.CheckoutResult, error)
    EditGroups(ctx, proto.GroupEditParams) error
    ReviewAction(ctx, proto.ReviewActionParams) error
    Routing(ctx, proto.RoutingParams) (proto.RoutingInfo, error)
    CallLog(ctx, proto.CallLogParams) ([]model.ReviewCall, error)
    SetCap(ctx, proto.CapParams) error
    SaveComment(ctx, proto.CommentParams) (model.Comment, error)
    CommentAction(ctx, proto.CommentActionParams) (model.Comment, error)
    SubmitReview(ctx, proto.SubmitParams) (proto.SubmitResult, error)
    CreatePR(ctx, proto.CreatePRParams) (proto.CreatePRResult, error)
    MarkViewed(ctx, proto.ViewedParams) error
    Override(ctx, proto.OverrideParams) error
    Search(ctx, proto.SearchParams) (proto.SearchResult, error)
    Preview(ctx, query string) (model.Item, error)
    RemovePreview(ctx, itemID string) error
    Stop(ctx, proto.StopParams) (proto.StopResult, error)        // M5.1
    Resume(ctx, proto.ResumeParams) (proto.ResumeResult, error)  // M5.1
    Settings(ctx, proto.SettingsParams) (proto.SettingsInfo, error)        // M5.1
    SaveSettings(ctx, proto.SaveSettingsParams) (proto.SettingsInfo, error) // M5.1
    Events() <-chan proto.Event
    Close() error
}
```

## Failure handling (where it lives)

| Spec rule | Implementation |
| --- | --- |
| Jev unreachable → `unknown`, confidence 0, route deep, picker "labels unavailable" | `jev.ErrUnavailable` (no key, network, 401/429/5xx) → pipeline writes `model.LabelUnknown` labels, sets `ItemAgg.LabelsMissing` |
| Embedding model missing → skip stage 3 | `embed.ErrUnavailable` or nil `Deps.Embedder` → RunLog `skipped` with note |
| LLM error/timeout (30 s fast, 120 s deep) → failed, retry once, then `R` | `review.FastTimeout/DeepTimeout`, `Review.Attempts`, `ReviewFailed` |
| No GitHub (no remote, no token, `--offline`) | `Deps.GH == nil` → local branches only; a GitHub error (incl. `gh.ErrUnauthorized`) during discovery keeps cached PR items and emits "GitHub unavailable: …" |

## Testing

- No test touches the network. Boundaries have fakes; HTTP adapters are tested
  with `httptest`; `gitx.Exec` and `fixturegen` tests use throwaway local repos.
- Recorded real responses live in `testdata/recordings/<kind>/` and are served
  by `jev.Replay` (and the M3 review equivalent). Live recording happens only
  in `//go:build live` tests.
- `make snapshot` renders screens without a TTY; `tui.Snapshot` is also used
  in package tests to assert screen content.
