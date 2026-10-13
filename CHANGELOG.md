# Changelog

All notable changes to this project are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); milestones follow
`docs/spec.md` (M0 → M5).

## [Unreleased]

### M0 — skeleton (released 2026-10-13)

Release gate: `go build ./...`, `go vet ./...`, `make lint` (0 issues),
`make test` and `make snapshot` all green on `a3062d3`+; `make demo` runs the
TUI offline against `testdata/fixture-repo` with `testdata/fixtures/prs.json`.

- **Shipped:** module and tooling, boundary interfaces with fakes (git,
  GitHub, Jev, embeddings, reviewer, store), diff parser, daemon + socket
  protocol, discovery and urgency placeholder, picker / Overview / Code TUI,
  fixture repo with the four synthetic PRs plus a local branch.
- **Deferred:** Jev/LLM recordings under `testdata/recordings/` move to the
  first M1 step (request shape is defined by M1; see DECISIONS.md). ONNX
  embedder is a stub with a TODO; the Ollama adapter is the working path.
- **Blocked:** nothing.
- **Tag note:** the `m0` tag was created (and pushed) at `7d27b45`, before
  the round-2 review fixes below. It was not moved; `main` at the
  "docs: changelog for M0" commit is the complete M0.

#### Added

- Go module `github.com/fredricw/prt`, Makefile (`build`, `test`, `lint`,
  `check`, `demo`, `fixtures`, `snapshot`), golangci-lint v2 config, MIT
  licence, README.
- Architecture for M0–M5 (`docs/ARCHITECTURE.md`) and initial decisions
  (`docs/DECISIONS.md`).
- Domain model (`internal/model`) with content-based unit and group ids.
- Boundary interfaces with fakes: `gitx.Git` (git CLI implementation),
  `gh.Client`, `jev.Client` (AI Gateway HTTP client, replay and recorder),
  `embed.Embedder` (Ollama adapter, ONNX stub), `review.Reviewer`,
  `store.Store` (SQLite).
- Config loading (`prt.toml`, `prt.routing.toml`) with lookup order and
  built-in defaults; question catalogue validation.
- Unified diff parser with intra-line character diff.
- NDJSON protocol types, in-process daemon client, pipeline skeleton
  (fetch → parse → aggregate) with RunLog events.
- Skeleton TUI (picker + plain diff) and `--snapshot` mode.
- Deterministic fixture repo generator (`tools/fixturegen`).
- Fixture set: a "portal" monorepo (TS/TSX, Go, Python) with five branches —
  mixed refactor+feature with a breaking `getUser` signature change, a
  20-file mechanical rename, deletions only, a dependency bump, and a local
  spike branch — plus `testdata/fixtures/prs.json` for the fake GitHub client.
- GitHub client: `Viewer`, `SearchPRs`, `PR`, `PRDiff` and `CODEOWNERS` over
  GraphQL/REST; 401 maps to `gh.ErrUnauthorized`; `gh.LoadFixture`.
- Daemon: `prt daemon` serves NDJSON over `.git/prt/prtd.sock` with event
  fan-out and periodic discovery; `daemon.Dial` client; discovery into
  Needs you / Yours / Branches / Added with stale detection; an
  urgency-ordered worker pool; `git checkout` with a dirty-tree check.
- Pipeline falls back to the GitHub PR diff when the head is not local;
  aggregate computes the M0 urgency placeholder and "why".
- Plain diff viewer (`internal/diffview`): lazy row rendering, file
  navigation, intra-line highlighting, escape-sequence-safe output.
- TUI: picker with urgency bars and per-section secondary lines, Overview and
  Code tabs, help overlay, rebindable keys (`[keys]` in `prt.toml`), checkout
  prompt, `o` opens the PR in the browser, periodic refresh.
- `--fixtures` and `--now` flags; `make demo` and `make snapshot` run fully
  offline against the fixture repo.

#### Fixed (M0 review)

- Needs you holds only the spec's triggers (review requested, CI red after
  your approval, re-review after a push, a comment addressed to you); other
  involvement moves to a new "Involved" section.
- The owned-paths source works: open PRs are listed with their changed
  files and matched against `user.owns` locally (issue search has no `path:`).
- A push during analysis leaves the item `stale`, not `ready` with an empty
  diff; stale items wait for `r`, and `r` re-resolves the head first.
- Overlapping discoveries are serialised and can no longer prune each
  other's items; a missing default branch only skips local branches.
- Ahead counts and merge bases use `origin/<default>` when it exists; fork
  PRs no longer hide same-named local branches, and `c` checks out their
  head commit.
- Config: unknown keys (including the spec's `models.jev_endpoint`) warn
  instead of stopping prt.
- TUI starts against the cache; mouse wheel and row clicks; `~N min` on
  Needs you and open-thread counts on Yours.
- Socket: no 64 MiB line cap, a short fallback path for deep repos, a
  visible note when the daemon connection drops; `git checkout <ref> --`.
- Tests: the fixture pipeline test generates its own repo (never skips);
  `cmd/prt` has tests; live-tagged tests are vetted and linted.

#### Fixed (M0 review, round 2)

- Quitting (or killing) prt mid-analysis no longer leaves items in `error`
  or spinning forever: interrupted runs reset, and busy states are recovered
  at start.
- `prt daemon` analyses never-analysed items itself, so the cache fills with
  no TUI attached.
- A stale item's Code tab keeps the analysed diff (new `Item.AnalysedSHA`)
  instead of "(no diff yet)".
- The status bar says why GitHub is disabled (no token, not a GitHub remote,
  `--offline`).
- `--now` also fixes the pipeline's clock, so `make snapshot` is stable.
- pgdown/space scroll a full page and ctrl+d/ctrl+u half a page.
- Discovery emits item events only for changed items, and the TUI coalesces
  list reloads.
- `go install github.com/fredricw/prt/cmd/prt@latest` works (no `exclude` in
  go.mod).
- Jev/LLM recordings are scheduled for the start of M1 (see DECISIONS.md).

### M1 — units and labels (released 2026-10-13)

Release gate: `go build ./...`, `go vet ./...` (also `-tags live`),
`make lint` (0 issues), `make test` and `make snapshot` all green on
`72b2d60`; `make demo` runs the TUI offline against `testdata/fixture-repo`
with `testdata/fixtures/prs.json`. The Jev recordings were made
once against the real endpoint (218 calls, $0.0197, see
`testdata/recordings/SPEND.md`), and `TestRecordedReplay` replays them
offline over fixture PRs #101–#104.

- **Shipped:** tree-sitter change units, the full question catalogue,
  stage-4 Jev labels with a SQLite label cache, aggregate arithmetic, the
  Overview page and Code page mode A, the Jev recorder; review fixes (see
  DECISIONS "M1 review fixes" 1 and 2): formatting runs, unique duplicate
  unit ids, position-keyed line labels, input-hashed cached answers,
  unknown-aware chips and filters, a Jev circuit breaker and per-unit partial
  failures, user questions merged over the built-ins, clickable minimap cells.
- **Deferred:** catalogue rows without an M1 consumer (marked `deferred`,
  never sent) wait for M2–M5; the logout-bullet check needs caller links
  (M2); `effect`/`reversible` are asked but first consumed in M3; "has
  comments", "has findings" and "changed since last look" filters are
  placeholders until M3/M4; `make demo`/`make snapshot` stay offline and show
  the labels-unavailable mode; the ONNX embedder is still a stub.
- **Blocked:** nothing.

#### Added

- tree-sitter parse (Go, TS, TSX, JS, Python): each hunk is split at
  function boundaries into change units. Each unit gets a qualified symbol,
  a signature, a git kind, a shape hash with identifier normalisation and
  pure-formatting detection. Moved code is paired across files. Files that
  can't be parsed fall back to one unit per hunk.
- Question catalogue: every row of the spec table is built in, with
  criteria, dynamic description-bullet options and `deferred` markers.
  Loading validates strictly (unknown keys and inputs, `feeds = []`, bad
  ids). Only questions with an M1 consumer are sent.
- Stage-4 labels (`internal/labels`): one Jev request per unit, plus line
  batches of 20 added lines. Requests respect token limits (the diff is cut,
  then questions are split). Answers map to labels with P/confidence; when
  Jev is unreachable, labels are `unknown`.
- Pipeline: labels are cached by unit id in SQLite. An outage never
  overwrites known answers. Ownership comes from `user.owns` or CODEOWNERS,
  overrides reach the aggregate, and the picker shows "labels unavailable"
  plus breaking / needs-eyes counts. The daemon streams patch events as
  stages land.
- Aggregate arithmetic: noise, effective lines, attention, minutes,
  composition, risk_max, risk_any, test gap, hot spots, fidelity,
  undocumented, unsure, gutter line marks, the file list and Your slice.
- Overview page: title, composition bar, lines/minutes/needs-eyes summary,
  headlines, check before approving, risk with tag chips and test gap,
  description fidelity, minimap, Your slice and the confidence footer.
  Probabilities show as `.86`, with a `?` below .60. `enter`, `L` and a
  click jump into Code.
- Code page mode A (`internal/codeview`): file list (owned first, a "not
  yours" divider, minimap cells), unit chips, gutter marks `◆ ~ ⋯ ·`, noise
  collapsed with `z`, a filters bar (`f`, 1–9), `n/p`, `J/K` and a `L`
  raw-labels overlay.
- `tools/recordjev` with `make record-jev` (live, capped at $1.00, with a
  spend ledger) and `make record-jev-dry` (a free count of requests).

#### Known limitations

- With real answers the Headlines block lists every breaking or contract
  unit (about 85 rows on #101), and nearly every unit is "unsure". Both
  follow the spec; see DECISIONS "Spec consequences seen with real labels".
- Checking whether the #101 logout bullet is really implemented needs
  caller links (M2).
- `make snapshot` / `--offline` run without Jev, so they show "labels
  unavailable" and git facts only.

### M2 — groups (integrated 2026-10-13)

Gate: `make check` (vet, vet `-tags live`, tests, lint 0 issues) and
`make snapshot` green. Embeddings recorded once from local Ollama
(embeddinggemma-2, 111 texts, $0); M2 Jev answers recorded once (276 calls,
$0.0096; cumulative $0.0293 of the $1.00 cap). `TestRecordedReplay` replays
both strictly and asserts the grouping outcomes over #101–#104.

- **Shipped:** stage 2 structural links, stage 3 embeddings and candidates,
  stage 4 Jev pair confirmation, stage 5 clustering with mechanical groups,
  Jev group names and split suggestions, user merge/split, Code page mode B,
  the Pipeline page's grouping data (`ItemAgg.Grouping`), and an offline
  demo/snapshot that replays the recorded answers.
- **Deferred:** three-word titles for groups Jev cannot name (fast LLM, M3;
  placeholders until then); the Pipeline page itself (M3); routing tier and
  review status in the group list (M3); the ONNX embedder (still a stub).
- **Blocked:** nothing.

#### Added

- `internal/link`: call, import, test, moved (including symbol-level moves
  across files) and same-symbol links from tree-sitter, with a repo symbol
  index cached per base SHA; embed kNN candidates (cosine ≥
  `embed_min_cosine` or top 3) and mechanical candidates by shape hash.
- `internal/embed`: replay and recorder for embeddings; vectors cached by
  unit id; no embedder → stage 3 skipped with a status note.
- `internal/judge`: Jev `relation` for candidate pairs (kept when p ≥
  `keep_pair_p` and not unrelated), `name_bullet` for groups, `separate_pr`
  per file; request truncation within the 32k/64k limits.
- `internal/cluster`: connected components over structural and confirmed
  links; oversized components split at their weakest Jev links, then at
  structural bridges; mechanical groups (count ≥ 3, shape-hash verified,
  non-matching members ejected); review order interface → callers → tests;
  placeholder names; user edits.
- Aggregate: a description bullet counts as implemented only when a
  claiming unit is wired (called or imported by non-test code).
- Code page mode B (`g`): groups in review order with counts and bullet,
  ungrouped units and split suggestions at the bottom, a "why these belong
  together" block, mechanical groups as one example plus "×N verified
  identical"; `M` merges and `X` splits groups, stored and re-run by the
  daemon.
- `prt --jev-replay DIR --embed-replay DIR`; `make demo`/`make snapshot`
  replay `testdata/recordings`; `make record-embed`.

#### Known limitations

- embeddinggemma-2 cosines are compressed; the fixture repo uses
  `embed_min_cosine = 0.88` (see DECISIONS). The spec default 0.72 makes
  nearly every pair a candidate with this model; relation requests are
  capped at `max_pairs_per_unit` (default 3) × units per item, highest
  cosine first.
- Large star-shaped groups (one helper and all its updated callers, e.g.
  #102's 17-unit `requestJSON` group) stay whole above `max_group_units`.
- The snapshot's status bar does not show the replay source note (the
  interactive TUI does).

### M3 — review pipeline

### M4 — comments

### M5 — polish
