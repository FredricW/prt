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

- **Decision:** `testdata/fixture-repo` is built deterministically (fixed author, committer and dates; `GIT_CONFIG_GLOBAL=/dev/null`) by `internal/fixturegen` (CLI: `tools/fixturegen`) from committed sources: `testdata/fixtures/base/**` (initial tree on `main`) and `testdata/fixtures/prs/NN-name.patch` (one branch per patch; mbox patches via `git am`, plain diffs via `git apply --index`). The generated dir is gitignored; `make fixtures` regenerates, `make demo`/`make snapshot` regenerate it whenever a fixture source changes. Tests call `testutil.FixtureRepo`, which generates a fresh copy into `t.TempDir()` (never skips, never stale; needs only the git binary).
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
  - TOML: `github.com/BurntSushi/toml` (its `MetaData.Undecoded` lets config warn about unknown keys).
  - Globs for `owns`: `github.com/bmatcuk/doublestar/v4` (`**` support).
  - Intra-line diff: `github.com/sergi/go-diff/diffmatchpatch` (character diff + semantic cleanup).
  - TUI tests: `github.com/charmbracelet/x/exp/teatest` available; `tui.Snapshot` is the primary no-TTY check.
  - go-tree-sitter: `github.com/smacker/go-tree-sitter` pinned at `v0.0.0-20240827094217-dd81d9e9be82`. `go.mod` has no `exclude` (see "M0 review fixes, round 2"): the stray `github.com/smacker/go-tree-sitter/javascript v0.0.1` module is not in the build list, so the import path is not ambiguous, and an `exclude` would break `go install …@latest`.
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

## 2026-10-13 — M0 shared contracts for parallel work

- **Decision:** before splitting M0 into parallel packages the lead fixed these contracts:
  - `model.Item` gains `Draft`, `ViewerApproved`, `Additions`, `Deletions`, `Urgency`, `UrgencyQuartile` (0–4, relative to the other items; computed by the daemon) and `Why`, so the picker can sort and draw the urgency bar from `Items()` alone without loading every `Detail`.
  - `daemon.Client` gains `Checkout(ctx, proto.CheckoutParams) (proto.CheckoutResult, error)`: without `Force`, a dirty tree returns `Dirty=true` and does nothing, so the TUI can prompt (spec keybinding `c`). `proto` adds `MethodDiscover` and `MethodCheckout`.
  - Offline GitHub is `gh.Fixture` JSON (`testdata/fixtures/prs.json`) loaded by `gh.LoadFixture` into `gh.Fake`; `make demo` passes it with `--fixtures`. A PR's `headSha` may be empty; consumers resolve it from the local head ref.
  - The plain diff viewer is a separate package `internal/diffview` (component with `SetDiff/SetSize/Update/View/ScrollBy/GotoTop/GotoBottom/NextFile/PrevFile/CurrentFile`), consumed by `internal/tui`.
- **Why:** lets fixtures, GitHub, daemon/pipeline, diff viewer and TUI be built in parallel on disjoint directories.
- **Alternatives:** compute urgency quartiles in the TUI (rejected: daemon owns ranking, TUI never does maths); viewer inside `internal/tui` (rejected: one package per agent).

## 2026-10-13 — M0 urgency placeholder

- **Decision:** until M3, `aggregate` computes the spec's urgency formula with only the terms that need no labels — review requested, release path, CI red after approval, age, minutes (from changed lines at 0.1 min/line as a stand-in) and, for branches, unpushed commits; label terms are 0. "Why" is the top three positive contributions in words.
- **Why:** the spec allows a placeholder until M3 but the picker's urgency bar and "why" must render now; using the real formula with zeros means M3 only fills in terms.

## 2026-10-13 — Unit id normalisation

- **Decision:** `model.UnitID(path, hunk)` = sha256(path + "\n" + hunk with trailing whitespace stripped per line and leading/trailing blank lines dropped). The `+`/`-`/space prefixes are kept.
- **Why:** spec says sha256(path + normalised hunk) without defining "normalised"; this keeps ids stable across whitespace noise at hunk edges while distinguishing additions from deletions. Identifier normalisation is reserved for the shape hash.

## 2026-10-13 — Fixture set (M0)

- **Patch authoring:** patches are authored in a scratch repo built by `fixturegen` and exported with `git format-patch --zero-commit --no-signature`. The `.patch` files are the only source of truth; `fixturegen` has no authoring mode. To change one: generate, check out the branch, edit, commit with `--date`, re-export, then update SHAs and stats in `prs.json` (tests catch mismatches). `.gitattributes` marks `testdata/fixtures/prs/*.patch` as `-text` so nothing normalises them.
- **Identity and dates:** an mbox patch keeps its own author and Date; the committer is "Fixture Author" with committer date = author date. The base commit and plain-diff commits use 2026-10-01T09:00:00Z.
- **Dependabot message** has no `---` line, because `git am` would cut the message there.
- **Fake hashes:** `go.sum` h1 hashes and `package-lock.json` integrity hashes are plausible but not real; fixtures are never installed.
- **#104 is approved and CI red** (`viewerApproved=true`) to exercise the "CI red after approval" urgency term; #101 and #103 have `reviewRequested=true`.
- **Unimplemented bullet:** #101's body lists "Invalidate the profile cache on logout", which the diff does not implement (M3 description-check target).
- **`codeowners`** field in `prs.json` mirrors `base/CODEOWNERS`.
- **Guard:** `fixturegen` refuses output outside `testdata/` and a path whose last element is `testdata` itself.
- **Alternatives:** an authoring mode in `fixturegen` (more code for a rare task); plain diffs only (loses author/date per PR).

## 2026-10-13 — GitHub client behaviour (M0)

- **`ErrUnauthorized` wraps `ErrNoToken`**, so callers that fall back to local branches on a missing token do the same on a rejected one. Alternative: a separate error every caller checks.
- **"Viewer commented" is inferred:** true when the viewer's latest review is not PENDING, or they wrote one of the PR's last 20 comments. It is informational only (superseded for sections by the 2026-10-13 "Needs you membership" entry below). Alternatives: an extra `commenter:@me` search per refresh; the participants list (also counts the author).
- **Only user review requests count;** team requests neither add a reviewer nor set ReviewRequested. Team membership may come later.
- **Fake `is:closed`** matches closed and merged PRs, like GitHub; an empty State counts as open.
- **`type:pr` counts as `is:pr`** when building search queries.
- **Viewer login is cached** after the first success; failures are never cached, so the next refresh retries.

## 2026-10-13 — Daemon and discovery (M0)

- **Adding an existing item** returns it in its current section and queues analysis; it does not move a ranked item into Added. Alternative: convert it to source=added.
- **Staleness** (revised: items now also record `AnalysedSHA`, see "M0 review fixes, round 2"): an item becomes Stale if it was Ready or Error and its head SHA changed (note "new push since analysis"). Discovery leaves in-flight items alone, but when a run finishes it compares the head it analysed with the stored head and sets Stale instead of Ready if they differ.
- **Discovery computes urgency from metadata** with the M0 formula and GitHub's Additions/Deletions (when non-zero) so the picker can rank and queue before analysis. M3 must merge label terms from the stored ItemAgg (TODO in `setUrgency`).
- **Urgency terms by kind:** PR-only terms (requested, release, CI red) are omitted for branches; unpushed is omitted for PRs (spec: "Branches replace the PR-only terms"). Label terms are stored with value 0.
- **GitHub failure keeps cached PR items** (not pruned); their head ref/SHA still hide matching local branches.
- **Owned-path source** (revised, see "M0 review fixes"): issue search has no `path:` qualifier, so discovery lists up to 100 open PRs with their first 100 changed paths (`gh.Client.ListOpenPRs`, GraphQL `pullRequests(states: OPEN)` + `files`) and matches `user.owns` with doublestar locally. Errors are logged only.
- **Socket:** requests on one connection run concurrently (responses may be out of order, correlated by ID). Each subscriber has a 256-event buffer; events drop on overflow, as InProcess does. A live socket gives `ErrDaemonRunning`; a dead socket file is removed.
- **Periodic discovery:** `prt daemon` runs `Discover` at start and every `pipeline.refresh_seconds`, and after each pass queues every never-analysed item (`InProcess.AnalysePending`, urgency order) so the cache fills with no TUI attached; the TUI also runs it on the same interval (`tui.Options.RefreshEvery`), so a TUI on an in-process client stays fresh too. `Discover` runs are serialised by a mutex, so overlapping callers (daemon loop, TUI ticks, TUI start) never prune each other's results; a duplicate run is then cheap and idempotent. Alternative: only the daemon refreshes and pushes events (the TUI would then need a separate path for `--no-daemon`).

## 2026-10-13 — Diff viewer (M0)

- pgdown/pgup/space scroll a full page; ctrl+d/ctrl+u half a page.
- NextFile/PrevFile may scroll past the normal bottom so the last file's header is the top row; scrolling down never pulls that back, `SetSize`/`GotoBottom` clamp. PrevFile inside a file goes to that file's header first.
- Raw fallback when `diff.Parse` fails or finds no `diff --git` section.
- `Line()` returns 0 for a file header, the hunk's NewStart for a hunk header, and for a deleted line the nearest earlier new-side line (or max(1, NewStart)).
- Control characters in diff text render as `?` to block escape-sequence injection.
- Intra-line highlighting is skipped when a pair totals more than 4096 bytes; each line is cut at 4×width+64 bytes before styling.
- Gutter width comes from the largest line number in the whole diff so columns stay aligned; hunk headers are drawn at column 0.

## 2026-10-13 — TUI behaviour (M0)

- **Empty picker sections** show as a dim "Name (0)" header so absence is explicit.
- **Selection without colour:** the cursor row has a "▸" marker as well as bold, so selection is visible in snapshots.
- **Notes clear on the next key press**, not a timer (deterministic, no tick commands).
- **Detail loads:** only the newest load is applied (sequence number); no debounce delay.
- **Analysis start** runs in daemon order in one command, at most once per (item, head SHA), and only for never-analysed items (empty state). A Stale item waits for `r`, as the spec's state list says; `r` always forces.
- **Rebinding:** `[keys]` in `prt.toml` maps action → comma-separated keys (`down = "j,down"`). Actions: up, down, top, bottom, focus, back, next_focus, prev_focus, refresh, overview, code, open, checkout, page_down, page_up, half_page_down, half_page_up, next_file, prev_file, help, quit. An unknown action logs a warning and the defaults are used.
- **Branches secondary line:** "<n> unpushed · <n> ahead · age"; branch state arrives in M5.
- **Markdown without colour** uses glamour's "notty" style; with colour it picks dark/light from `HasDarkBackground()` read before the program starts.
- **tab / shift+tab** both toggle picker ↔ main pane until the bottom strip exists.
- **Snapshot degrades:** a failed Discover writes `# discover failed: …` and renders stored items instead of aborting.

## 2026-10-13 — CLI wiring (M0)

- **`--fixtures FILE`** loads `gh.LoadFixture` as `Deps.GH` and wins over the real client; it works with `--offline` (offline still disables Jev, LLMs and embeddings). `make demo` and `make snapshot` use `testdata/fixtures/prs.json`.
- **`--now RFC3339`** fixes the clock for ages, both in the TUI and in the pipeline (`Deps.Now`: urgency age term, quartiles, order); `make snapshot` passes `2026-10-13T12:00:00Z` so output is byte-stable. Alternative: a `PRT_NOW` env var (flags are more visible in the Makefile).
- **Opener for `o`:** `open` on macOS, `xdg-open` elsewhere, started without a shell and not waited on.

## 2026-10-13 — M0 review fixes

- **Needs you membership.** Only the spec's four triggers put a PR in Needs you: (1) the viewer is a requested reviewer (user requests only); (2) CI is red and the viewer's latest review approved it; (3) re-review after a push: the commit of the viewer's latest submitted, non-dismissed review (`viewerLatestReview.commit.oid`) differs from the head SHA; (4) a comment addressed to the viewer: one of the PR's last 20 conversation comments, written by someone else, @-mentions the viewer's login, and the viewer has not commented after it. PRs the viewer authored go to Yours, whatever else is true. Why: the spec's table. Alternatives: count every comment by the viewer (the old rule: every involved PR landed in Needs you); thread-level "reply in a thread you started" detection (needs review-thread comments per PR; possible later with `Threads`).
- **Involved section (deviation).** PRs a source returned that trigger nothing above (owned-path matches nobody asked you about, PRs you only commented on, approved PRs with no new push) are listed in a fifth section, "Involved", after Branches and before Added, hidden when empty. Why: the spec requires these sources but its section table has no row for them; putting them in Needs you made that section meaningless. Alternatives: drop them (loses the owned-path source), or rank them low inside Needs you (still dilutes it).
- **Added PRs get fresh metadata** from `GH.PR` on each discovery when no source returned them.
- **Fork PRs.** `isCrossRepository` is fetched. Local branches are hidden by a PR's head SHA, and by its head ref name only for same-repo PRs, so a fork's `patch-1` never hides your own `patch-1`. `c` on a fork PR checks out its head commit (detached) when it is local, otherwise it fails with "run `gh pr checkout N`". Alternative: fetch `refs/pull/N/head` (network git; not in gitx by design).
- **Comparison base.** Ahead counts and merge bases use `origin/<default>` when that remote-tracking ref exists (`gitx.CompareRef`), else the local branch; branch items show that ref as their base. A `CountAhead` error keeps the stored item instead of pruning it. A missing default branch skips only the branch source (EventLog note), PR discovery still runs.
- **`r` re-resolves the head** before re-running: the local ref for branches, `GH.PR` for PRs (metadata refreshed too). A failed lookup keeps the stored head and still re-runs.
- **Config.** Unknown keys in any TOML file are warnings (logged at start), not errors; syntax and type errors stay fatal. The spec's `models.jev_endpoint` is accepted and ignored with a warning (the spec value is a full endpoint, not a base URL, so it cannot map onto `jev_base_url`). A test loads the spec's sample `prt.toml` verbatim.
- **Picker secondary lines.** Needs you shows `~N min` (stand-in minutes until M3); breaking and needs-eyes counts need labels and arrive in M3. Yours shows `N open threads` (unresolved review threads, first 100) when non-zero, plus CI state and age.
- **Mouse.** Wheel over the picker moves the selection, over the Overview scrolls it, over Code goes to the diff viewer; a left click on a picker row selects that item; nothing else.
- **TUI start** batches a cache load with discovery, so stored items render before GitHub answers.
- **Socket protocol.** Lines have no size cap (`bufio.Reader`); a lost daemon connection shows "daemon connection lost; restart prt" in the status bar. Reconnect or fallback to in-process is not attempted (later).
- **Socket path length.** When `<gitDir>/prt/prtd.sock` is longer than 100 bytes, the socket lives at `$TMPDIR/prt-<sha256(gitDir)[:16 hex]>/prtd.sock`; the daemon refuses a socket directory owned by another user.
- **Checkout** runs `git checkout <ref> --` and refuses refs starting with `-`, so a ref is never read as a pathspec or option.
- **Lint and vet cover `//go:build live` tests** (`build-tags: [live]`, `go vet -tags live` in `make check`); they are compiled, never run.

## 2026-10-13 — M0 review fixes, round 2

- **Interrupted runs are not errors.** When a run fails because its context was cancelled (quit, daemon stop), `finish` resets the item instead of storing Error: back to "not analysed" (so it is queued again), or Ready/Stale when an earlier analysis is stored (`AnalysedSHA` equal to / different from the head). At start, `NewInProcess` applies the same reset to items left Fetching/Analysing by a killed process (SIGKILL, closed terminal), so no spinner runs forever. Why: an interruption says nothing about the item; Error would need `r` on each one. Alternatives: keep Error with a "cancelled" note (still needs `r`); catch SIGHUP (does not cover SIGKILL).
- **`Item.AnalysedSHA`** records the head the stored files, units and overview describe (set when a run finishes). `Detail` serves that head's cached diff first and falls back to `HeadSHA`, so a Stale item's Code tab keeps matching its Overview until `r`. Alternative: a per-item "latest fetch" pointer (same effect, one more document kind).
- **The daemon analyses on its own.** After each discovery pass `prt daemon` queues never-analysed items (`AnalysePending`, urgency order, deduplicated with TUI requests by the queue), matching the spec's "daemon fetches, analyses and caches". The TUI still starts never-analysed items itself, which also covers `--no-daemon`.
- **GitHub-disabled note.** `setup` records why there is no GitHub client (no token from `gh auth token`, no origin, origin not on GitHub, bad `github.repo`, `--offline`); each discovery without a client sends it as an EventLog note, so the status bar explains why only branches are listed.
- **Fewer item events.** Discovery stores and emits an item only when its stored form changed (times compared in UTC). Urgency drifts with the clock (age term), so in practice most items still change; the TUI therefore coalesces item/remove events into one list reload in flight plus at most one follow-up, instead of one full `Items()` per event.
- **Paging keys split.** `page_down` = pgdown/space and `page_up` = pgup scroll a full page; new actions `half_page_down` = ctrl+d and `half_page_up` = ctrl+u scroll half a page, in both the Code and Overview tabs (as the "Diff viewer (M0)" entry already said).
- **No `exclude` in go.mod**, so `go install github.com/fredricw/prt/cmd/prt@latest` works (`go install pkg@version` rejects modules with `exclude`/`replace`). `go mod tidy` and the build resolve the javascript grammar from the main go-tree-sitter module without it.
- **Jev/LLM recordings deferred to the start of M1 (deviation from the brief's "before M1").** Recordings are keyed by the exact request bytes (`jev.RequestKey`), and the request shape (state rendering, question catalogue wiring) is defined by M1's Jev integration; recording now would produce files no M1 request matches and spend part of the $5 budget twice. The first M1 step, before any label test, is to run the `//go:build live` recorder once against fixture PRs #101–#104 and commit `testdata/recordings/jev/*.json`; review recordings follow in M3 once the Reviewer request shape exists. Alternative: record a provisional shape now (rejected: wasted spend, stale files).
- **`m0` tag left where it is.** At the M0 release gate the annotated `m0` tag already existed locally and on origin at `7d27b45` (before the round-2 fixes). Moving a pushed tag rewrites published history, which the run rules forbid, so it stays; CHANGELOG.md names the "docs: changelog for M0" commit on `main` as the complete M0. Alternatives: delete and re-tag (rejected: force-updates a published ref); add an `m0.1` tag (not done unattended; cheap to add later if wanted).
- **TUI test helper.** `run` abandons only the event listener (detected through the fake's `Events()` call) and fails after 10 s on any other command, instead of silently dropping commands slower than 100 ms.
