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

### M1 — units and labels

### M2 — groups

### M3 — review pipeline

### M4 — comments

### M5 — polish
