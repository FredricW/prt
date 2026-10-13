# Changelog

All notable changes to this project are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); milestones follow
`docs/spec.md` (M0 → M5).

## [Unreleased]

### M0 — skeleton

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

### M1 — units and labels

### M2 — groups

### M3 — review pipeline

### M4 — comments

### M5 — polish
