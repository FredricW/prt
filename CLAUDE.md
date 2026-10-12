# prt — PR triage TUI

Terminal app that ranks PRs/branches needing review and splits each diff into labelled, grouped change units. Full spec: `docs/spec.md`.

## Source of truth

1. `docs/spec.md` wins.
2. Then `docs/DECISIONS.md`. When the spec is silent or ambiguous, make a call, record it there (date, decision, why, alternatives), and keep going.
3. Do not edit `docs/spec.md`. If something in it looks wrong, note it in `docs/DECISIONS.md` and follow the spec anyway.

## This run is unattended

- Nobody is watching. Do not stop to ask questions; decide, record the decision in `docs/DECISIONS.md`, continue.
- Work milestone by milestone (M0 → M6 in the spec). Each milestone must build, pass tests and lint before starting the next.
- Commit small and often on `main` with clear messages. Never force-push, never `git reset --hard`.
- If a command is denied by permissions, find an allowed route or record the blocker in `docs/DECISIONS.md` and move on. Do not loop on it.
- Only delete files inside this repo (`rm -rf ./...`).
- Never print or commit secrets (`JEV_API_KEY`, `PRT_ANTHROPIC_API_KEY`, `gh auth token`). Read them from the environment at runtime.

## Stack

- Go 1.23+, module `github.com/fredricw/prt`, one binary: `prt` (TUI) and `prt daemon`.
- TUI: Bubble Tea, Bubbles, Lip Gloss; Glamour for markdown, Huh for forms.
- Parsing: `github.com/smacker/go-tree-sitter` (TS/TSX/JS/Go/Python grammars) — needs cgo.
- Embeddings: onnxruntime_go with a small code model; fallback to Ollama `/api/embed` (`OLLAMA_HOST`, default `http://127.0.0.1:11434`; local model `embeddinggemma-2`).
- Judgments: thin HTTP client for Jev, reached through Vercel AI Gateway (this resolves the spec's open question on Jev's endpoint):
  - `POST ${JEV_BASE_URL}/v1/evaluate`, `JEV_BASE_URL` defaults to `https://ai-gateway.vercel.sh`.
  - Header `Authorization: Bearer $JEV_API_KEY` (the key is an AI Gateway API key).
  - Body `{"model": "typesafe-ai/jev", "state": "...", "questions": {"<name>": {"type": "boolean" | "choice" | "score", "instructions": "..."}}}`; response `answers` keyed by question name, plus `usage` and `providerMetadata.gateway.cost`.
  - Limits: 64,000 tokens per request; 32,000 for state plus the longest question. No streaming.
  - Full reference, including choice/score question shapes and fallbacks: `docs/vendor/ai-gateway-decision.md`.
- Review LLMs: `Reviewer` interface with `fast`/`deep` tiers. Read the key from `PRT_ANTHROPIC_API_KEY`, never `ANTHROPIC_API_KEY` (that name would switch Claude Code itself to API billing).
- GitHub: token from `gh auth token`; `github.com/google/go-github/v66` (REST), `github.com/shurcooL/githubv4` (GraphQL).
- Store: `modernc.org/sqlite` (pure Go), one file per repo under `.git/prt/`.
- Transport: daemon ↔ TUI over a Unix socket, newline-delimited JSON; `--no-daemon` runs in-process.
- Logging: `charmbracelet/log` to `.git/prt/prt.log`.

## Commands

Create a `Makefile` with these targets in M0 and use them from then on:

| Target | Runs |
| --- | --- |
| `make build` | `CGO_ENABLED=1 go build -o bin/prt ./cmd/prt` |
| `make test` | `CGO_ENABLED=1 go test ./...` |
| `make lint` | `golangci-lint run ./...` |
| `make check` | `go vet ./...` + test + lint; must pass before every commit |

golangci-lint is v2 (2.14.0): any `.golangci.yml` must start with `version: "2"` and use the v2 schema (`linters.default`, `formatters` section), not v1 keys.

Tests must not hit the network or real APIs: use fakes behind interfaces and fixtures under `testdata/`. Live-API tests go behind a `//go:build live` tag.

Additional targets:

| Target | Runs |
| --- | --- |
| `make fixtures` | regenerate `testdata/fixture-repo` (gitignored) from `testdata/fixtures/{base,prs/*.patch}` via `tools/fixturegen`; deterministic SHAs |
| `make demo` | build, generate fixtures if missing, run `bin/prt --no-daemon --offline` on the fixture repo (interactive) |
| `make snapshot` | same, but `--snapshot`: prints a few screens to stdout with no TTY and no colour; use it to check UI changes |

Running by hand: `bin/prt [--repo DIR] [--no-daemon] [--offline] [--snapshot --width N --height N --max-items N]`, and `bin/prt daemon [--repo DIR]`. Per-repo state lives in `.git/prt/` (`prt.db`, `prt.log`, `prtd.sock`, optional `prt*.toml`).

Environment: the local toolchain is Go 1.27; `go.mod` says `go 1.26.0` (forced by deps, see DECISIONS.md). In this sandbox Bash runs simple commands only (no `&&`, `;`, `cd`): use `go -C /Users/fredric/code/prt …`, `git -C …`, `make -C …`, and `go fmt ./...` for formatting.

## Layout

Full map, dependency rules and interfaces: `docs/ARCHITECTURE.md`. Summary:

- `cmd/prt` — flags, `daemon` subcommand, wiring real clients from env/config.
- `internal/model` — domain types and content-hash ids; imports nothing from prt.
- Boundaries (each an interface + real impl + `Fake`): `gitx` (git CLI), `gh` (GitHub), `jev` (AI Gateway `/v1/evaluate`, plus `Replay`/`Recorder`), `embed` (Ollama; ONNX stub), `review` (Anthropic, fast/deep `Tiers`), `store` (SQLite).
- Pure stages: `diff` (unified diff parse, intra-line), `parse` (M1, tree-sitter units), `questions` (catalogue), `link` (M2), `cluster` (M2), `route` (M3, rule language), `aggregate` (M1/M3 arithmetic).
- `pipeline` wires stages to boundaries and emits `RunLog` events; `daemon` exposes it as `daemon.Client` (`InProcess` now, socket later); `proto` is the NDJSON wire format; `tui` renders and talks only to `daemon.Client`.
- `internal/deps` (`//go:build tools`) pins every third-party module: add new modules there, run `go mod tidy` once, so parallel work never needs `go get`.
- `internal/testutil` — `RepoRoot`, `Testdata`, `FixtureRepo`, `RecordingsDir`. `testdata/recordings/{jev,review}` hold recorded real responses.

Rule of thumb: spec wins, then `docs/DECISIONS.md`, then `docs/ARCHITECTURE.md`. Record every non-trivial call in DECISIONS.md and add a CHANGELOG.md line per milestone.
