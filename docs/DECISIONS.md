# Decisions

Order of authority: `docs/spec.md`, then this file, then `docs/ARCHITECTURE.md`.
Each entry: date, decision, why, alternatives considered.

## 2026-10-13 — Anthropic key env var is `PRT_ANTHROPIC_API_KEY`

- **Decision:** the Anthropic adapter reads `PRT_ANTHROPIC_API_KEY` only (`review.AnthropicKeyEnv`).
- **Why:** CLAUDE.md requires it; the overnight brief said `ANTHROPIC_API_KEY`, but that name would switch Claude Code itself to API billing on this machine.
- **Alternatives:** `ANTHROPIC_API_KEY` (brief), or reading both (rejected: the fallback would reintroduce the billing problem).

## 2026-10-13 — Scope is M0–M5; M6 deferred

- **Decision:** build M0 → M5; overlap (M6) is out of scope. The data model keeps room for it (content-based unit ids, `semantic_conflict` question reserved).
- **Why:** the spec marks M6 "later"; the brief says M0 → M5.
- **Alternatives:** start M6 if time remains (rejected: not requested).

## 2026-10-13 — Jev via Vercel AI Gateway

- **Decision:** `POST ${JEV_BASE_URL:-https://ai-gateway.vercel.sh}/v1/evaluate`, `Authorization: Bearer $JEV_API_KEY`, model `typesafe-ai/jev`. Spec type mapping: Noul (N) → `"boolean"`, Choice (C) → `"choice"` with a criteria record (option → description), Score (S) → `"score"` with a criteria array ordered lowest → highest. The spec's `prt.toml` `models.jev_endpoint` is replaced by `models.jev_base_url` (env `JEV_BASE_URL` wins). Limits: 64k tokens per request, 32k for state + longest question; no streaming.
- **Why:** resolves the spec's open question on Jev's endpoint (CLAUDE.md + `docs/vendor/ai-gateway-decision.md`).
- **Alternatives:** TypeSafe-compatible endpoint `/typesafe` (keeps `noul` naming) — not needed since we own the client.
- **Detail:** "confidence" of an answer = max(p, 1−p) for booleans, chosen option's probability for choices, top rung's probability for scores (`jev.Answer.Confidence`). 401/429/5xx and network errors map to `jev.ErrUnavailable` (degrade); other 4xx are bugs and surface as errors.

## 2026-10-13 — Embeddings: Ollama first, ONNX stub

- **Decision:** `embed.Ollama` calls `${OLLAMA_HOST:-http://127.0.0.1:11434}/api/embed` with model `embeddinggemma-2` (default `models.embed = "ollama:embeddinggemma-2"`). `embed.ONNX` is a stub returning `ErrUnavailable` with a TODO. If neither works, stage 3 is skipped per spec.
- **Why:** brief + CLAUDE.md; ONNX needs a model whose licence allows bundling (spec open question) and the onnxruntime shared library.
- **Alternatives:** ship ONNX now (blocked on model choice).

## 2026-10-13 — Fixture repo is generated, not committed

- **Decision:** `testdata/fixture-repo` is built deterministically (fixed author, committer and dates; `GIT_CONFIG_GLOBAL=/dev/null`) by `tools/fixturegen` from committed sources: `testdata/fixtures/base/**` (initial tree on `main`) and `testdata/fixtures/prs/NN-name.patch` (one branch per patch; mbox patches via `git am`, plain diffs via `git apply --index`). The generated dir is gitignored; `make fixtures` regenerates, `make demo`/`make snapshot` generate on demand. Tests that need it call `testutil.FixtureRepo`, which skips when absent.
- **Why:** a nested `.git` cannot be committed; deterministic SHAs keep cache keys and recordings stable.
- **Alternatives:** git submodule (needs a second remote), a bundle file (opaque in review), committing with `.git` renamed (fragile).

## 2026-10-13 — Real model spend capped at $5

- **Decision:** at most $5 total on real model calls, only to validate recordings once per adapter. Recordings live in `testdata/recordings/{jev,review}/` and tests use them via `jev.Replay` (and an M3 equivalent).
- **Why:** brief.

## 2026-10-13 — Paths under `.git/prt/`

- **Decision:** socket `.git/prt/prtd.sock`, store `.git/prt/prt.db`, log `.git/prt/prt.log`, repo-local config `.git/prt/prt*.toml`.
- **Why:** spec says one store per repo under `.git/prt/`; keeping all per-repo state together means nothing is ever committed by accident.

## 2026-10-13 — Review models and transport

- **Decision:** fast `anthropic:claude-haiku-5-5`, deep `anthropic:claude-opus-5-5` (spec defaults), through a thin HTTP client for the Anthropic Messages API (`POST https://api.anthropic.com/v1/messages`). Model specs are `provider:model`; `review.FromSpec` builds adapters.
- **Why:** spec defaults; no SDK dependency needed for one endpoint.
- **Alternatives:** anthropic-sdk-go (heavier, not needed).

## 2026-10-13 — Go version and dependency choices

- **Decision:** `go.mod` declares `go 1.26.0`, not 1.23. Forced by current dependencies: `modernc.org/sqlite` v1.60.1, `golang.org/x/oauth2` v0.37.0 and `golang.org/x/sys` v0.48.0 require 1.26; `bubbletea` v1.3.10 / `bubbles` v1.0.0 require 1.24. The local toolchain is Go 1.27.2.
- **Why:** pinning everything back to 1.23-compatible versions would mean old, unmaintained versions of the storage and TUI stack for no user benefit.
- **Alternatives:** pin old versions to keep `go 1.23` (rejected).
- **Other choices:**
  - Bubble Tea **v1** (`github.com/charmbracelet/bubbletea` v1.3.10) with bubbles v1.0.0, lipgloss v1.1.x, glamour v1.0.0, huh v1.0.0, charmbracelet/log v1.0.0 — huh and glamour v1 are built on Bubble Tea v1; v2 would split the stack.
  - TOML: `github.com/BurntSushi/toml` (its `MetaData.Undecoded` lets config reject unknown keys).
  - Globs for `owns`: `github.com/bmatcuk/doublestar/v4` (`**` support).
  - Intra-line diff: `github.com/sergi/go-diff/diffmatchpatch` (character diff + semantic cleanup).
  - TUI tests: `github.com/charmbracelet/x/exp/teatest` available; `tui.Snapshot` is the primary no-TTY check.
  - go-tree-sitter: `github.com/smacker/go-tree-sitter` pinned at `v0.0.0-20240827094217-dd81d9e9be82`; `go.mod` excludes the stray `github.com/smacker/go-tree-sitter/javascript v0.0.1` module, which otherwise makes the import path ambiguous.
  - All third-party modules are pinned via `internal/deps/deps.go` (`//go:build tools`) so parallel agents never run `go get`.

## 2026-10-13 — Store layout: JSON documents in SQLite

- **Decision:** one `docs(kind, id, parent, body)` table holding JSON-encoded model structs, plus a `cache(ns, key, val)` table (embeddings as little-endian float32, generic content-hash cache) and an append-only `runlog`. Migrations are an ordered list tracked by `PRAGMA user_version`. One SQL connection (SQLite serialises writes; needed for `:memory:`).
- **Why:** the model will churn through M1–M5; JSON docs keep migrations trivial while keeping every cache key from the spec (unit id, group id, head SHA).
- **Alternatives:** a normalised schema per entity (more migrations while the model is unstable; can be introduced later behind the same `Store` interface).

## 2026-10-13 — Daemon fallback and offline flag

- **Decision:** `prt` tries to dial the daemon socket and silently falls back to in-process mode if no daemon is running (logged). `--offline` disables GitHub, Jev, LLMs and embeddings; `make demo` and `make snapshot` use `--no-daemon --offline` against the fixture repo.
- **Why:** the TUI must start instantly even without a daemon; demo and CI must not need keys or network.
- **Alternatives:** auto-spawn the daemon (may come later; not needed for M0).

## 2026-10-13 — `--snapshot` mode

- **Decision:** `prt --snapshot [--width N --height N --max-items N]` discovers items, runs the pipeline synchronously, then prints the root view (no colour, renderer bound to a non-TTY) for each of the first N items, separated by `=== screen: … ===` headers. `make snapshot` runs it on the fixture repo.
- **Why:** lets agents and CI verify screens without a terminal.

## 2026-10-13 — Lint configuration

- **Decision:** golangci-lint v2 with `default: standard` plus bodyclose, copyloopvar, errorlint, misspell (no locale), nolintlint, noctx, revive (selected rules; `exported` and `blank-imports` off), unconvert, usestdlibvars; formatters gofmt and goimports (local prefix `github.com/fredricw/prt`).
- **Why:** strict on correctness, quiet on style. misspell has no US locale because the spec and label ids use British spelling (`behaviour`, `catalogue`, `normalise`).

## 2026-10-13 — Unit id normalisation

- **Decision:** `model.UnitID(path, hunk)` = sha256(path + "\n" + hunk with trailing whitespace stripped per line and leading/trailing blank lines dropped). The `+`/`-`/space prefixes are kept.
- **Why:** spec says sha256(path + normalised hunk) without defining "normalised"; this keeps ids stable across whitespace noise at hunk edges while distinguishing additions from deletions. Identifier normalisation is reserved for the shape hash.
