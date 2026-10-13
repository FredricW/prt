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
- `cmd/prt` builds real implementations from env/config and hands them down.
- `internal/deps` (build tag `tools`) pins every third-party module; add new
  modules there first so parallel work never runs `go get` concurrently.

## Packages

| Package | Owner | Purpose | Status |
| --- | --- | --- | --- |
| `cmd/prt` | M0 | flags (`--repo --no-daemon --offline --fixtures --snapshot --now --jev-replay --embed-replay --review-replay`), `daemon` subcommand (socket + periodic discovery), wiring from env (reviewers, `Deps.ConfigDirs`), browser opener | M3 (review replay) |
| `internal/model` | M0 | domain types (spec "Data model"), `UnitID`, `GroupID`, `ContentHash`, aggregates | done |
| `internal/config` | M0 | `prt.toml`, `prt.routing.toml` (+ raw `prt.questions.toml`), lookup `.git/prt` → XDG → builtin, unknown keys warned about (`Config.Warnings`) | done |
| `internal/gitx` | M0 | `Git` interface, `Exec` (git CLI), `Fake` | done |
| `internal/gh` | M0/M4 | `Client` interface, `API` (go-github + githubv4; token from `gh auth token`), `Fake`, `ParseRemote` | `Viewer`, `SearchPRs`, `ListOpenPRs` (with changed paths, for owned globs), `PR`, `PRDiff`, `CODEOWNERS` done; fake + `LoadFixture`; M4 write methods (`Threads`, `SubmitReview`, `Reply`, `ResolveThread`, `CreatePR`) TODO |
| `internal/store` | M0 | `Store` interface, `SQLite` (JSON docs table + cache + runlog, `PRAGMA user_version` migrations), `OpenMemory` for tests | done |
| `internal/diff` | M0 | unified diff parser, hunk/line numbering, `IntraLine` char diff, `Pair` | done |
| `internal/proto` | M0 | NDJSON `Message`, `Event`, `Detail`, encoder/decoder | done |
| `internal/daemon` | M0 | `Client` interface, `InProcess` (discovery, worker pool, checkout, group edits, review actions, routing ensure/reload, call log, per-item cap), `Serve`/`Dial` over the socket | M3 |
| `internal/diffview` | M0 | plain unified diff viewer component (lazy row rendering, file navigation, intra-line highlight) | done |
| `internal/tui` | M0→M5 | root model, picker (stage strip while analysing), Overview (TL;DR line, `w` weights overlay)/Code tabs, Pipeline page (`P`, via `pipePane`), help overlay, rebindable keys, `$EDITOR` hook, `Snapshot` (no-TTY render) | M3 |
| `internal/pipeview` | M3 | Pipeline page component: stage strip + per-stage log, routing table, totals/rules/grouping/calibration panels, call log and cap prompt overlays; actions D s r e c $ via `PendingAction`; `StageStrip` for the picker | M3 |
| `internal/pipeline` | M0→M3 | stage orchestration, caching, RunLog events, degradation; stages 6–8 (route with user overrides, planned review dispatch per tier with retry, escalation and calibration, AI group titles, TL;DR, review totals) | all 8 stages wired |
| `internal/parse` | M1 | tree-sitter split at function boundaries, symbol/signature, shape hash, format detection, `Lang` | hunk fallback only |
| `internal/questions` | M1 | catalogue types, validation, `Wire()` to `jev.Question`, embedded `default.toml` | types done; catalogue partial |
| `internal/jev` | M1 | `Client`, `HTTP` (AI Gateway `/v1/evaluate`), `Fake`, `Replay`, `Recorder`, `RequestKey` | done (batching lives in `labels`) |
| `internal/labels` | M1 | stage 4 labels: per-unit and per-line-batch Jev requests from the catalogue, token-limit splitting, answer → `model.Label` mapping, unknown on `ErrUnavailable` | M1 |
| `internal/codeview` | M1/M3 | Code page component: mode A by file (file list, chips, gutter marks, noise collapse, filters, `L` overlay, inline findings); mode B by change (groups, why links, mechanical example, merge/split, routing meta, findings block, `R` re-review) | M3 |
| `tools/recordjev` | M1/M3 | runs the pipeline over fixture PRs with a recording, budget-guarded Jev client (or, with `--review`, budget-guarded Anthropic reviewers over replayed Jev); writes `testdata/recordings/{jev,review}/` and `SPEND.md`; shares `pipelinetest.FixtureRun`, `Problems`, `Expectations` and `ReviewExpectations` with the replay tests | M3 |
| `internal/aggregate` | M1/M3 | spec "Ranking and aggregation" arithmetic, urgency (label terms from an `ItemAgg`, `Input.Prior` for discovery) + "why", hot-spot reasons from findings, group kind/hot spot/findings | M3 |
| `internal/shape` | M2 | mechanical class key: shape hash + literal edit signature (`Key`, `EditSig`), shared by link, pipeline and cluster | M2 |
| `internal/link` | M2 | structural links (symbol index per base SHA), embed kNN candidates, mechanical candidates, `UnitText` | M2 |
| `internal/judge` | M2 | non-unit Jev questions: pair `relation` (+ `Confirm`), group `name_bullet` (+ `ApplyNames`), file `separate_pr` (+ `Suggestion`); answers are labels on pair/group/file subjects | M2 |
| `internal/embed` | M2 | `Embedder`, `Ollama` (`/api/embed`), `ONNX` stub, `Fake`, `Cosine`, `FromSpec`, `Replay`/`Recorder` over `testdata/recordings/embed` | M2 (ONNX TODO) |
| `internal/cluster` | M2 | components, splitting (weakest Jev links, then structural bridges), mechanical groups, review order, placeholder names, user edits | M2 |
| `internal/route` | M3 | `when` lexer/parser and Kleene evaluator, `Compile`, lazy group `Env`, `Route` (first match, prompts, automatic context), `Gather` (24 KiB context blocks), `Plan`/`Budget` (hot-spot order under the cost cap) | done |
| `internal/review` | M3 | `Reviewer`, `Tiers`, `Anthropic` Messages adapter (`PRT_ANTHROPIC_API_KEY`), prompt builders (review, TL;DR, title), `ParseReview`/`AnchorFindings`/`CleanTLDR`/`CleanTitle`, price table and estimates, `Replay`/`Recorder` over `testdata/recordings/review`, `Fake` | done (prices assumed) |
| `internal/comments` | M4 | pure comment logic: anchor/re-anchor by unit id, merge GitHub threads, findings → ai-drafts, submission selection, view marks and "Since you looked" arithmetic | done (M4 lead) |
| `internal/testutil` | all | `RepoRoot`, `Testdata`, `FixtureRepo` (generates a fresh copy into `t.TempDir()`), `RecordingsDir` | done |
| `internal/fixturegen` | pre-M1/M4 | `Generate`: builds a fixture repo from `testdata/fixtures/{base,prs/*.patch}` with fixed dates; earlier heads from `history/<branch>.<label>.patch` at `refs/fixture/<branch>/<label>` | done |
| `tools/fixturegen` | pre-M1 | CLI over `internal/fixturegen` for `testdata/fixture-repo` | done |

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
