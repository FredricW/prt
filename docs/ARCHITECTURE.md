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
- Pure stage packages (`diff`, `parse`, `link`, `cluster`, `route`,
  `aggregate`, `questions`) do no I/O except through an interface passed in.
- `pipeline` is the only package that wires stages to boundaries.
- `daemon` wraps `pipeline` + `store` behind `daemon.Client`; `tui` talks only
  to `daemon.Client` and `proto` types, never to `store` or a boundary.
- `cmd/prt` builds real implementations from env/config and hands them down.
- `internal/deps` (build tag `tools`) pins every third-party module; add new
  modules there first so parallel work never runs `go get` concurrently.

## Packages

| Package | Owner | Purpose | Status |
| --- | --- | --- | --- |
| `cmd/prt` | M0 | flags (`--repo --no-daemon --offline --snapshot`), `daemon` subcommand, wiring from env | wiring done; daemon socket TODO |
| `internal/model` | M0 | domain types (spec "Data model"), `UnitID`, `GroupID`, `ContentHash`, aggregates | done |
| `internal/config` | M0 | `prt.toml`, `prt.routing.toml` (+ raw `prt.questions.toml`), lookup `.git/prt` → XDG → builtin, unknown keys rejected | done |
| `internal/gitx` | M0 | `Git` interface, `Exec` (git CLI), `Fake` | done |
| `internal/gh` | M0/M4 | `Client` interface, `API` (go-github + githubv4; token from `gh auth token`), `Fake`, `ParseRemote` | interface + fake done; API methods TODO |
| `internal/store` | M0 | `Store` interface, `SQLite` (JSON docs table + cache + runlog, `PRAGMA user_version` migrations), `OpenMemory` for tests | done |
| `internal/diff` | M0 | unified diff parser, hunk/line numbering, `IntraLine` char diff, `Pair` | done |
| `internal/proto` | M0 | NDJSON `Message`, `Event`, `Detail`, encoder/decoder | done |
| `internal/daemon` | M0 | `Client` interface, `InProcess`, `Serve`/`Dial` over the socket | InProcess done; socket TODO |
| `internal/tui` | M0→M5 | root model, picker, screens, `Snapshot` (no-TTY render) | skeleton: picker + plain diff |
| `internal/pipeline` | M0→M3 | stage orchestration, caching, RunLog events, degradation | fetch/parse/aggregate wired; others skipped |
| `internal/parse` | M1 | tree-sitter split at function boundaries, symbol/signature, shape hash, format detection, `Lang` | hunk fallback only |
| `internal/questions` | M1 | catalogue types, validation, `Wire()` to `jev.Question`, embedded `default.toml` | types done; catalogue partial |
| `internal/jev` | M1 | `Client`, `HTTP` (AI Gateway `/v1/evaluate`), `Fake`, `Replay`, `Recorder`, `RequestKey` | done (batching lives in pipeline) |
| `internal/aggregate` | M1/M3 | spec "Ranking and aggregation" arithmetic, urgency + "why" | line counts only |
| `internal/link` | M2 | structural links, embed kNN candidates, mechanical candidates | TODO |
| `internal/embed` | M2 | `Embedder`, `Ollama` (`/api/embed`), `ONNX` stub, `Fake`, `Cosine`, `FromSpec` | done (ONNX TODO) |
| `internal/cluster` | M2 | components, splitting, mechanical groups, review order | placeholder: one group per unit |
| `internal/route` | M3 | `when` expression parser/evaluator, `Route`, context decisions, slots/cap | placeholder |
| `internal/review` | M3 | `Reviewer`, `Tiers`, `Anthropic` adapter (`PRT_ANTHROPIC_API_KEY`), `Fake` | interface + fake done; adapter TODO |
| `internal/testutil` | all | `RepoRoot`, `Testdata`, `FixtureRepo` (skips if not generated), `RecordingsDir` | done |
| `tools/fixturegen` | pre-M1 | builds `testdata/fixture-repo` from `testdata/fixtures/{base,prs/*.patch}` with fixed dates | done |

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
    SubmitReview(ctx, number int, r ReviewSubmission) ([]model.Comment, error)
    Reply(ctx, number int, inReplyTo int64, body string) (model.Comment, error)
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
| No GitHub (no remote, no token, `--offline`) | `Deps.GH == nil` → local branches only |

## Testing

- No test touches the network. Boundaries have fakes; HTTP adapters are tested
  with `httptest`; `gitx.Exec` and `fixturegen` tests use throwaway local repos.
- Recorded real responses live in `testdata/recordings/<kind>/` and are served
  by `jev.Replay` (and the M3 review equivalent). Live recording happens only
  in `//go:build live` tests.
- `make snapshot` renders screens without a TTY; `tui.Snapshot` is also used
  in package tests to assert screen content.
