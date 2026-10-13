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

## 2026-10-13 — M1 label conventions (shared contract)

- **Decision:** `model.Label` encodes Jev answers as follows (doc comment in `internal/model/labels.go`):
  - Boolean (N): `Value` = `yes` when P ≥ 0.5 else `no`; **`P` = probability of yes** (not of Value); `Confidence` = max(P, 1−P).
  - Choice (C): `Value` = the option exactly as in the catalogue; `P` = `Confidence` = chosen option's probability; `Probs` = all options.
  - Score (S): `Value` = most probable rung, 1-based decimal ("1".."5"); `Score` = Jev's interpolated 0-based score + 1; `Probs` keyed by 1-based rung; `P` = `Confidence` = top rung's probability. Thresholds ("risk ≥ 4", "risk ≥ 3") compare the integer rung (`model.ScoreLevel`); products (hot spot) use `Score`.
  - "N ×k" rows (`risk_tags`, `needs_context`) are one boolean per option; label id `<id>.<sub>` (`risk_tags.auth`); catalogue options are written `"<sub>: <description>"`. Risk tag sub-ids: auth, async, error_path, data_write, money, pii, io_in_loop, date_tz (routing refers to `tag.<sub>`).
  - Line labels: `UnitID` = unit id, `Line` = new-side line number of an added line.
  - Jev unreachable: `Value = unknown`, P 0, Confidence 0, no Probs, Model "". Unknown labels are not a cache hit: the next run asks again.
  - `desc_bullet` options are `b1..bN` (the i-th `model.DescBullets(body)` item, description = bullet text) plus `none`.
  - `risk_any` uses p_i = P(risk ≥ 4) = Probs["4"] + Probs["5"] over units whose risk rung ≥ 4. `covered < 0.5` uses P(yes).
- **Why:** aggregate, labeller and TUI are built in parallel; they must agree on encoding. P(yes) for booleans makes `covered < 0.5` and the overview's `.86` next to "breaking" direct reads.
- **Alternatives:** P = probability of Value for booleans (the M0 doc comment; makes every consumer flip on `no`); 0-based score rungs (Jev's wire form; off-by-one against the spec's "S 1–5").

## 2026-10-13 — M1 shared contracts for parallel work

- **Decision:**
  - `model.ItemAgg` grows the fields the Overview and Code page render without arithmetic: `NoiseLines`, `UnitsSkim/Skip`, `Bullets []BulletAgg` (fidelity), `ShapeP`, `UnsureUnits`, `Files []FileAgg` (Code page file list, owned first then attention), `Slice []SliceRow` ("Your slice"). `UnitAgg` carries every chip/filter fact (kind, behaviour, breaking, contract, covered, risk, relevance, risk tags, fail mode, desc bullet, undocumented, untested, unsure) and per-line `LineMark`s (gutter `◆ ~ ⋯ ·`, precedence suspicious > value > leftover). The TUI reads `Agg` for numbers and `Labels` only for the `L` overlay.
  - `model.Item` gains `LabelsUnavailable`, `Breaking`, `NeedsEyes` (copied from the aggregate by the pipeline) for the picker.
  - Stage 4 labels live in a new package `internal/labels` (`labels.Run(ctx, jev.Client, labels.Input) (labels.Result, error)`); the pipeline owns caching by unit id and passes only units that need labels.
  - Code page mode A is a component package `internal/codeview` (like `diffview`), owned separately from `internal/tui`, which wires it.
  - `questions.Question.Deferred` (TOML `deferred = "M2"`) marks catalogue rows whose consumer is not built yet: defined and validated, never sent (`Catalogue.Live`). User-added rows are live.
  - `jev.Recording.Synthetic` / `jev.Recorder.Synthetic` mark heuristic recordings used only if live recording is blocked.
  - `aggregate.Input` gains `Owns` and `LabelsMissing`.
- **Why:** lets parse, labels, aggregate, codeview, tui and pipeline be built in parallel on disjoint directories.
- **Alternatives:** keep labelling inside `internal/pipeline` (ARCHITECTURE's M0 note; one package would then hold orchestration and Jev request shaping, and could not be built alongside the pipeline wiring); mode A inside `internal/tui` (two agents in one package).

## 2026-10-13 — M1 live questions

- **Decision:** M1 sends only questions with an M1 consumer: line `fmt`, `value_tweak`, `leftover`, `swallow`, `pii_or_secret`, `suspicious`; unit `kind`, `behaviour`, `effect`, `desc_bullet`, `breaking`, `contract`, `reversible`, `risk_tags`, `fail_mode`, `risk`, `covered`, `test_weakened`, `relevance`, `depth`. Every other spec row is in the built-in catalogue with `deferred` set: `follows_pattern`, `duplicates_helper` (need neighbour/helper inputs from M2 embeddings), `interface` (M2 group order), `needs_context`, `reasoning` (M3 routing), file `role`, `separate_pr` (M2 split suggestion; role is not shown in M1), group/pair (M2), item `shape`, `desc_sufficient`, `flagged`, `rollback`, `needs_other_reviewer` (M3; `shape` needs aggregates as input), `comment_addressed` (M4), branch `state` (M5). The overview shows the coherence verdict only when an item `shape` label exists.
- **Why:** spec rule "a question nothing consumes is removed"; asking unconsumed questions also spends budget and makes M1 recordings larger.
- **Alternatives:** send everything now (wasted spend; recordings would not change when consumers land anyway since the request shape is the same).

## 2026-10-13 — Shape hash normalisation

- **Decision:** the shape hash covers only the unit's `+`/`-` lines (context excluded), tokenised by tree-sitter (regex tokenizer for unknown languages), with whitespace dropped, identifiers renamed `$1, $2, …` in order of first appearance within the unit, and string/template literals replaced by `$s`; numbers and keywords/punctuation kept. The `+`/`-` markers are part of the stream.
- **Why:** spec says "normalise whitespace and identifiers" for mechanical detection; in fixture #102 the same rename (`import { fetchJSON } from "./client"` vs `"../api/client"`) differs only in a path literal, which should not split the pattern. Numbers stay, so a changed timeout is not "the same change".
- **Alternatives:** keep literals (splits #102's imports into per-directory shapes); hash context lines too (context differs per file, nothing would match).

## 2026-10-13 — M1 Jev recordings flow

- **Decision:** recordings are produced by running the real pipeline (fetch → parse → labels → aggregate) over fixture PRs #101–#104 with `Deps.Jev` = `jev.Recorder{Live: budget-guarded jev.HTTP}` (tool `tools/recordjev`, `make record-jev`, requires `--live` and `JEV_API_KEY`). Pipeline tests replay with `jev.Replay` over the same inputs, so request bytes match by construction. The tool skips requests already recorded, appends each run's cost to `testdata/recordings/SPEND.md`, and stops before a call once cumulative Jev spend + the largest single-call cost seen (or $0.01 before the first call) would exceed $1.00. If the live call fails, the exact error goes in `docs/BLOCKED.md` and `--synthetic` writes heuristic recordings marked `"synthetic": true`. The offline replay test skips with a loud message only while `testdata/recordings/jev/` holds no recordings at all; any missing key once recordings exist fails the test and names the key.
- **Why:** one code path for recording and replay; no hand-assembled requests that drift from the pipeline.
- **Alternatives:** a recorder that builds requests itself (drifts); recording in a `//go:build live` test (works, but a tool can keep a ledger and a budget across runs more naturally).

## 2026-10-13 — Parse: symbols and nesting (M1)

- **Decision:**
  - Inside a function body only named declarations become their own symbols (TS/JS `function` declarations and Python `def`, qualified `Outer.inner`). Arrow functions / function expressions bound to a const and object-literal methods count only at module or class level. Callbacks, closures and JSX handlers stay with the enclosing function; a class nested in a function opens a new naming scope.
  - Naming: Go methods use the receiver type without `*` or type parameters (`UserStore.Get`); TS/JS class members are `Class.member`, an anonymous class bound to a const takes the const's name; methods of a module-level `const api = {…}` are `api.m`; Python methods are `Class.method`. Doc comments directly above a declaration (no blank line) belong to it; decorators and `export` are part of the declaration's lines, and `export` appears in its signature.
  - Signature = declaration header up to the body, whitespace collapsed, at most 200 runes.
- **Why:** React components and hooks define many local handlers, splitting them gives tiny units that hurt grouping and labelling; reviewers expect qualified names; a doc-comment edit belongs to its function.
- **Alternatives:** every bound closure its own symbol (too fragmented); top-level declarations only (loses Python nested defs); bare names; comments as module level.
- **Known limitations:** `describe`/`it` test callbacks and other anonymous callbacks are module level (Symbol ""); multi-line parameter lists keep a space after "(" in signatures; a rename across extensions parses each side with its own grammar; identical units in one file with identical context would share an id (content-based, as specified; unique on all fixtures).

## 2026-10-13 — Parse: hunk splitting (M1)

- **Decision:**
  - Context between two segments stays with the preceding segment while its head-side symbol is still that segment's symbol; leading context goes to the first unit, trailing context to the last.
  - A blank changed line takes the symbol of the previous changed line in the hunk (the next one for leading blanks), so it never forms its own unit.
  - Rename merge: within one block of adjacent changed lines, if every deleted line is in one base declaration and every added line in one head declaration with a different name, and the block rewrites both header lines, both sides form one modified unit with the head symbol.
  - An empty span starts at the last line before the unit on that side (git's `-N,0` convention). A hunk with no changed lines produces no unit.
- **Why:** keeps the "concatenation equals hunk" and "≥1 changed line" invariants (asserted in tests for every case and fixture PR) and avoids noise units; without the rename merge a renamed function splits into deleted + added units.
- **Alternatives:** split context at the next symbol's boundary (same result in practice).

## 2026-10-13 — Parse: fallback rules (M1)

- **Decision:** a supported language with unusable source (Src nil, Show error, a syntax error anywhere, a side over 2 MiB, or source lines that do not match the diff text) gives one unit per hunk for the whole file, Symbol = hunk section, no signature. Unknown languages (`.md`, `.json`, `go.mod`) give one unit per hunk with Symbol "" (fixture #104 needs go.mod/package.json without a symbol; their sections are meaningless). Binary files and files without hunks produce no units. Only context cancellation fails `Split`. The fixture test asserts no supported fixture file falls back, so a grammar regression fails loudly.
- **Why:** never attribute wrong symbols; never fail an item over a source problem.
- **Alternatives:** tolerate partial syntax errors (risk of wrong attribution).

## 2026-10-13 — Parse: tokens, format and moved detection (M1)

- **Decision:**
  - Shape stream: tree-sitter leaves bucketed by row; string/template/regex/rune literals and comments are single tokens. Named leaves whose type ends in "identifier" become `$1, $2, …` (first appearance across the unit's +/- lines); literals `$s`, comments `$c`; numbers, keywords and punctuation verbatim; token-less lines dropped; each line is its +/- marker followed by its tokens. Files without a usable tree use a fallback tokenizer (identifiers, numbers, quoted strings, `//`, `/* */`, `#` comments, single-rune punctuation) in which keywords are renamed like identifiers, so a fallback hash can differ from a parsed hash of the same edit. This refines "Shape hash normalisation" (comments become `$c`, so comment-only edits across files still group).
  - `Format` is true when the unit has both deletions and additions and the concatenated raw token texts of deleted and added lines are equal: line breaks ignored (reflow is formatting), comment whitespace collapsed, string literals compared verbatim. In Python each non-blank line must keep its indentation and its own token stream (a reindent can change behaviour).
  - `DetectMoved`: compares +/- lines with whitespace collapsed and blank lines ignored, needs ≥3 non-blank lines; only Kind deleted with Adds==0 and Kind added with Dels==0 qualify; deleted units in slice order pair with the earliest unmatched added unit with the same key; moves within one file allowed; returns a copy.
- **Why:** follows the spec's identifier normalisation while keeping Python semantics; deterministic pairing.
- **Alternatives:** drop comments from shapes / keep comment text; always ignore indentation (wrong for Python).

## 2026-10-13 — Jev wire format and question catalogue (M1)

- **Decision:**
  - Wire names: `<id>`; `<id>__<sub>` for N ×k sub-questions (label `<id>.<sub>`); `<name>__lNN` for line questions, NN = the line's slot in `state.lines`, at least 2 digits; the instructions name the slot and new-side line number. Catalogue ids and sub-ids must match `[a-z0-9_]+` with no `__` and no leading/trailing `_`, so names cannot collide.
  - The catalogue adds an optional `criteria` table (choice option → description; boolean true/false) and `dynamic = "pr_bullets"`. A dynamic choice lists no options; only its `none` option may be described; the ≥2-options rule is waived for it. Label values stay the spec's options word for word.
  - `questions.Parse` rejects unknown TOML keys and unknown `inputs` names (a typo such as `defered` would otherwise quietly make a question live and spend budget). A user `prt.questions.toml` with a typo therefore fails at load.
  - State JSON (fixed field order): `unit` {path, symbol, signature, change, diff}; `pr` {title, bullets} only if a question declares pr_title/pr_bullets; `reviewer` {role, owns} only for role/owned_globs; `tests_touched` sorted, `[]` when asked and none; `lines` [{slot, line, text}] for line requests. The state carries the union of inputs of the live questions of that pass (unit or line), not per split chunk. `callers` is declared but not produced until M2.
  - Token check: EstimateTokens(json(state)) + EstimateTokens(`"name":` + json(question)) ≤ 32k; the whole request ≤ 64k. Over the limit the diff is cut deterministically (binary search for the largest k kept lines: ceil(k/2) head, floor(k/2) tail, `… N diff lines elided …` between); then questions are split across requests in name order; if one question still cannot fit, `Run` errors naming the path. A single line is capped at 2000 bytes (rune-safe, ending "…").
  - Batching: one request per unit for unit questions; added lines in batches of 20 per request.
  - Answers: missing, mistyped or out-of-range answers become unknown labels without an error and without setting Unavailable. Choice Probs keep only catalogue options; a score's argmax picks the lowest rung on ties; if `score` is missing or out of range, Score falls back to argmax+1.
  - When the PR has no bullets, `desc_bullet` is not sent; its label is `none`, P 1, Confidence 1, Probs {none:1}, Model `prt:no-bullets`, kept even when Jev is unavailable (a one-option choice tells us nothing).
  - `jev.HTTP` returns `ctx.Err()` on cancellation, not `ErrUnavailable`, so quitting never labels units unknown. 401/429/5xx/network still map to `ErrUnavailable`. Any other `labels.Run` error stops new calls and is returned with the labels already received.
  - `semantic_conflict` is deferred to M6 (its only consumer is overlap, v2/M6). Pair `relation` uses the catalogue table's wording `same pattern`, not stage 4's `same mechanical pattern` (the spec is inconsistent; the TOML follows the table).
- **Why:** deterministic, collision-free, safe JSON keys; slot-based names stay stable between batches; failures never fail an item; budget is not spent on typos.
- **Alternatives:** line numbers in names; dots as separators; options written "value: description" (label values would differ from the spec); warn on unknown keys; a separate state per chunk; error on over-long lines; error on a bad answer.

## 2026-10-13 — Aggregate arithmetic (M1)

- **Decision:**
  - A unit with no labels has MinConfidence 0 and is unsure (never skip); it adds nothing to the item's Unsure label count but is listed in UnsureUnits.
  - `UnitAgg.Kind` is the kind label's value as stored (`unknown` when Jev was unreachable, "" when never asked); composition files both under `unknown` so ratios add up to 1.
  - The 1-minute floor for eyes units applies only when the unit has effective lines; noise units and mechanical repeats take 0 minutes. A mechanical group without ExampleUnit counts its first member as the example.
  - When no label is a real answer (none, or all unknown), `ItemAgg.Minutes` and the urgency minutes term use `StandInMinutes(Added+Removed)`; otherwise the sum of unit minutes (else an unlabelled item would cost 5× and sink).
  - Risk tags = `risk_tags` sub-labels with P ≥ 0.5, plus `error_path` when a line swallows an error and `pii` when a line may expose personal data or a secret; deduplicated, in `model.RiskTags` order.
  - One "pii or secret" headline per unit, at the flagged line with the highest P (earliest on ties). Other headlines anchor at the unit's first added line (first deleted old-side line for pure deletions); Text = symbol, else signature, else path, cut to 60 runes.
  - Hot-spot reason: "<first risk tag>, <fail mode>, untested" leaving out absent parts, in tag words (data-write, date/tz), cut to five words; fallback "risk N of 5". Units scoring 0 are excluded; ties keep unit order.
  - Fmt-only noise checks every added line found by numbering the unit's diff from `Range.New.Start`; with no added lines in the diff, the fmt-labelled line count must be ≥ Adds.
  - Your slice: owned units matching no Owns glob group by top-level directory (`dir/*`) as owned rows; owned glob rows first in Owns order, then owned directory rows, then the rest; outside glob rows, order by unit count desc then module name. Unit paths missing from Input.Files still get a FileAgg (not owned).
  - `GroupAgg.Unsure` counts labels below `unsure_below`, like `ItemAgg.Unsure`; group members not among the item's units are ignored. Empty collections are non-nil; `Bullets` is nil when the body has no bullets. Item-scope labels feed only Shape/ShapeP.
- **Why:** deterministic, label-derived output; no unit dropped; consistent JSON.
- **Alternatives:** MinConfidence 1 for unlabelled units; drop unlabelled lines from composition; floor every eyes unit; always sum unit minutes; one headline per flagged line; drop units outside Files.

## 2026-10-13 — Code page mode A (M1)

- **Decision:**
  - Below 60 columns the file list is hidden; the pane takes the full width, its title shows "i/n", J/K still move between files. The right pane has a fixed title line (path, +/− from Detail.Files, "(not yours)").
  - Unit header chips wrap onto extra `│ ` lines at the pane width; the next line reads `L10–15 · +3 −1 · signature` (range first; the signature is what gets truncated).
  - A boolean chip shows the probability of the claim (P for ✓, 1−P for ✗), with "?" below 0.60.
  - With `Agg.LabelsMissing`, chips show only non-Jev facts (git kind, noise reason), gutter marks are blank and filters are off ("— filters off (labels unavailable)" in the bar, which stays available); minimap cells and noise collapsing still use the Agg. While the Agg is pending, files are listed owned first then by path, no minimap cells or gutter marks, every unit expanded.
  - `FocusUnit` reveals a unit hidden by filters until filters change, expands it if collapsed, and may scroll past the bottom so the header sits at the top.
  - When some units of a file are hidden, a trailing dim "N units hidden by filters (f)" line is shown. The "not yours" divider shows whenever not-owned files exist. The minimap shows every unit (not only filtered ones); more than (listW−2)/3 cells end in "+". "has comments"/"has findings" only show a note until M4/M3. The noise chip is skipped when equal to the Jev kind.
  - `g` is bound to Top for now; M2's by file / by change switch needs it revisited.
- **Why:** keep the selected file and hidden work visible; never let label-derived chips make claims Jev did not make; Overview jumps must land on their unit.
- **Alternatives:** stack the list above the pane; truncate chips; always show P(yes); "unknown" chips; refuse jumps or clear filters; filtered minimap.

## 2026-10-13 — TUI wiring and Overview (M1)

- **Decision:**
  - The Code tab sits behind an internal `codePane` interface; the root model holds a pointer adapter around `codeview.Model` (tests check key routing with a recording fake). ctrl+c is hard-wired to quit before any routing; while codeview or the raw-labels overlay captures keys, `q` goes to them. Otherwise codeview gets the first look at each key and unused keys fall through to global keys. Filter toggles 1–9 are not rebindable; codeview's Close reuses `back`. New rebindable actions: `next_unit` (n), `prev_unit` (p), `filters` (f), `expand` (z), `raw_labels` (L); `next_file`/`prev_file` default to `J,]` / `K,[`.
  - Composition bar: up to 40 cells, largest-remainder widths, ≥1 cell per segment; each kind drawn with the first free letter of its name, noise with ░; legend "r refactor 50% · f feature 32% · ░ noise 10%", largest first, ties by name; per-segment rounding may sum to 99–101.
  - An unsure claim (p < .60) gets "? " in a two-cell row gutter. Hot spots have no probability: their score shows dim with one decimal, never '?'.
  - Overview anchors for `enter`/`L`: composition → first unit of `Agg.Files[0]`; summary → first eyes unit; headlines/hot spots → their unit; risk headline → first unit at RiskMax; risk chips → first unit with a tag; test gap → first untested unit; bullets → `UnitIDs[0]`; undocumented rows and minimap rows → their unit / the file's first unit; confidence footer → `UnsureUnits[0]`; slice, coherence and description have none. A left click on a linked line opens that unit in Code.
  - The Overview drops the M0 facts lines; with labels missing, git facts are "+A −R · N files · U chunks" plus a Files list; with no aggregate, a failed item shows "not analysed — <reason>". `totals()` prefers Agg counts. The Overview minimap shows the "not yours" divider only when both owned and other files exist.
  - Picker: "labels unavailable" right after the state text in every section; Needs you reads author · +a −d · CI · N breaking · N need eyes · ~M min · why.
  - Snapshot: unchanged layout (Overview + Code per item, plus help).
- **Why:** testable routing; spec's "enter jumps to the first unit behind a block"; bar readable without colour.
- **Alternatives:** keep `codeview.Model` by value; let `q` quit while capturing; nine `filter_N` actions; anchor block headers only; keep facts lines.

## 2026-10-13 — Pipeline, daemon and recorder (M1)

- **Decision:**
  - Cache: a unit's stored labels are a hit when every live unit-scope label id (risk_tags expanded) has a known label and, if the unit adds lines, every live line question has at least one known line label (the labeller may skip lines). Unknown placeholders never overwrite a known stored label.
  - A `labels.Run` error (not `ErrUnavailable`) fails stage jev with the error in `RunLog.Err`, sets LabelsMissing, and the item still aggregates; `RunHead` returns nil unless the context was cancelled.
  - Overrides apply to unit-scope labels only: Value = override, Confidence 1, P 1 except P(yes) = 0 for a boolean overridden to "no", Score = rung for numeric values, Probs = {value: 1} when the label had Probs; an override with no stored label adds one.
  - Ownership: `user.owns` globs, else CODEOWNERS at the head commit (`.github/CODEOWNERS`, `CODEOWNERS`, `docs/CODEOWNERS`, first present wins), GitHub patterns approximated with doublestar, last matching rule wins, owners compared case-insensitively with `@`+viewer; only when GH is set and Viewer succeeds (cached on success).
  - Test paths for `tests_touched`: `*_test.go`, `test_*.py`, `*_test.py`, `*.test.*`/`*.spec.*` for ts/tsx/js/jsx/mjs/cjs, and any path under `__tests__`, `tests` or `test`.
  - The daemon sends a patch event for every stage row finishing done, cached or failed, and an item event after aggregate.
  - recordjev layers guard → Recorder → HTTP so existing recordings are served before the Recorder and never rewritten; in `--live` synthetic recordings are re-recorded for real; in `--synthetic` existing ones are reused. Budget: prior ledger spend + this run's spend + reserve × (in-flight + 1) > cap refuses with `ErrBudget`, reserve = largest single-call cost this run ($0.01 before the first priced call); a ledger row is appended after every live run, also a failed one.
  - Replay assertions live in `pipelinetest.Expectations` (no testing import) so `TestRecordedReplay` and recordjev share them. `FixtureRun` items copy author, reviewers, CI, labels, URL, dates, draft and review flags from prs.json.
- **Why:** an outage must not erase answers; nothing blocks an item; concurrent calls must not overshoot the cap.
- **Alternatives:** require every added line × question for a cache hit; hard-fail the run; P = 1 always for overrides; guard inside the Recorder; assertions only in tests.

## 2026-10-13 — M1 integration

- **Decision:** `--offline` (and so `make snapshot`/`make demo`) runs with `Jev` nil: every item shows "labels unavailable" and the Overview shows git facts only. The pipeline uses `questions.Question.LabelIDs()` instead of its own copy. `make record-jev` runs `tools/recordjev --live`; `make record-jev-dry` is the free dry run.
- **Why:** snapshot must stay offline and deterministic; one implementation of label ids.
- **Alternatives:** feed recordings into the snapshot (request keys depend on the fixture config and would silently miss outside `pipelinetest`).

## 2026-10-13 — M1 Jev recordings: result and the logout expectation

- **Decision:** `make record-jev` ran once against the real endpoint: 218 calls, $0.0197 (ledger in `testdata/recordings/SPEND.md`); no synthetic recordings and no `docs/BLOCKED.md` needed. One replay expectation failed: Jev mapped the new `clearProfileCache()` helper in #101 to the bullet "Invalidate the profile cache on logout" (P .70). The fixture's trap is that no logout handler calls the helper, and seeing that needs caller links (M2). Judged one unit at a time, the answer is reasonable. `pipelinetest.Expectations` now requires that bullet to be present and claimed by no unit except `useProfileCache.ts`/`clearProfileCache`. The other expectations hold on the real answers (#101 getUser breaking and signature, #102 shared shape and refactor 100%, #103 all deleted, #104 dependency 100%).
- **Why:** an expectation the M1 pipeline cannot meet by design would just force re-recording until the model happens to agree; the narrowed check still fails if any real call site claims the bullet.
- **Alternatives:** change the `desc_bullet` prompt to "fully implements" and re-record (spends again; a prompt tweak to pass one fixture); drop the check.
- **Follow-up (M2):** once `callers` links exist, restore "not implemented" for the logout bullet (an unused helper does not implement a behaviour).

## 2026-10-13 — Spec consequences seen with real labels (noted, spec followed)

- **Observation 1 (headlines):** spec "every unit labelled breaking, contract change, …" lists every unit with breaking P ≥ 0.5 or contract ≠ none. On the real #101 answers that is about 35 breaking rows and 50 contract rows, so the block stops working as a summary. The spec is followed; candidate fix for a later milestone: only rows with P ≥ 0.60 by default (others behind the unsure filter), or group by kind with counts.
- **Observation 2 (unsure):** spec "a unit with any label confidence < 0.60 is never skip". Each unit carries about 25 labels (8 risk-tag booleans alone), so with real answers nearly every unit is unsure: all minimap cells show □, all units stay expanded, and #101 reports 287 low-confidence labels. The spec is followed; candidate fix: compute unsure over the attention-driving labels only (`depth`, `kind`, `behaviour`), not risk-tag sub-booleans.
- **Decision (small UI fix within the spec):** a headline whose unit has no symbol or signature (Text falls back to the path) shows "(top level)" instead of repeating the path next to `path:line`.
- **Why:** CLAUDE.md says to follow the spec and record what looks wrong.

## 2026-10-13 — M1 review fixes

- **Formatting runs.** `Format` (refines "Parse: tokens, format and moved detection") now compares token streams run by run: a run is a maximal block of adjacent +/- lines, and context lines end a run. Every run that has tokens must have both deletions and additions with equal streams (Python: per line, indentation kept). A statement moved past a context line is therefore a real change, not layout. In JS/TS an added line that ends in `return`, `throw`, `break`, `continue` or `yield` is never formatting, because automatic semicolon insertion changes its meaning. *Why:* the unit-wide comparison hid reordered statements (validate/write, lock/unlock) as noise. *Alternatives:* LCS alignment of tokens (more code; same answer on every case we have); keep the unit-wide stream (wrong).
- **Duplicate unit ids.** Two units in one item with the same path and diff body (the same edit in two functions) used to share a content id, so the store kept one and the Code page showed it twice. The first keeps `model.UnitID`; the n-th (n ≥ 2) gets `model.DuplicateUnitID(id, symbol, n)`. Ids are unique per item, which the parse invariants now assert. *Why:* storage, labels and the Code page key units by id; the spec's content id stays for every unit that is not a duplicate, so it still carries over between items. *Alternatives:* a storage key of itemID/Order (labels and Agg would still collide); mixing the symbol into every id (changes every id and drops cross-item reuse of moved hunks).
- **Line labels by position (supersedes "Line labels: `Line` = new-side line number" in "M1 label conventions").** A line label's `Line` is the 1-based position of the added line among the unit's `+` lines. `model.AddedLineNumbers` maps positions to new-side numbers; aggregate (marks, fmt noise, pii headlines) and the raw-labels overlay convert, so everything shown is still a file line number. Jev requests are unchanged (they still name the new-side line), so the recordings replay. *Why:* the spec keys "labels, embeddings and per-line marks" by unit id, and the unit id has no line numbers in it; with absolute numbers a unit that moved (rebase, same hunk in another item) was a cache hit whose marks pointed at the old lines. *Alternatives:* treat a moved range as a cache miss (re-asks unchanged code on every rebase); hash of the line text (duplicate lines in one unit collide).
- **Cached answers with item-level inputs.** `desc_bullet` (PR title and bullets), `covered` (changed tests) and `relevance` (role, owned globs) depend on the item and the user, not only on the unit. Each label now carries `Ctx` = `labels.InputHash` of the inputs its question declares ("" for unit-only questions). A stored label counts as cached only when its `Ctx` matches the current item's; aggregate turns labels with another `Ctx` into unknowns. The store keeps one label per (unit, question, line), so two items that share a hunk under different inputs re-ask those units when they run in turn. The unit is then relabelled as a whole (all questions), like any other miss. The raw-labels overlay shows the stored label as is (the last run's). A store migration drops the old label cache (it had neither positions nor `Ctx`). *Why:* a branch analysed before its PR stored `desc_bullet = none` (no bullets), and the PR reused it forever: every bullet "not found", every unit "undocumented". Edited bodies, new tests and config changes went stale the same way. *Alternatives:* key labels by (item, unit) for these questions (more rows; overrides and cross-item reuse get harder); ask only the stale questions (needs a per-question subset in `labels.Run`; worth doing if re-labelling on description edits costs too much — Jev calls cost about $0.0001 each); include `Ctx` in the store key (an item's detail would then hold several variants).
- **Unknown booleans claim nothing.** `UnitAgg` gains `BehaviourUnknown`, `BreakingUnknown` and `CoveredUnknown`, set when the label has no real answer. Missing or malformed answers become unknown without setting LabelsMissing, so filters stay on. The behaviour filter lets such units through, and chips read `behaviour ?` and `tests ?` (no breaking chip). `Untested` needs a known `covered`. *Why:* P(yes) = 0 for an unknown label read as a confident "no": the default behaviour filter hid units Jev never judged (unsure units, which the spec says are never skipped), and chips showed `behaviour ✗ 1.0`. *Alternatives:* NaN/pointer probabilities (break JSON and every consumer); always show unsure units (also overrides the user's other filters).
- **Filter combination and "mine" without owned files.** Active filters combine with AND (a unit must pass every one; this was the behaviour, now recorded). When the item has no owned file (no `user.owns` and no CODEOWNERS match, the default config), "mine" stays on but is ignored, and the bar shows `[3 mine ✓ (no owned files, ignored)]`. *Why:* the spec's default "mine" would otherwise hide every unit of every PR for a user who has not configured ownership. *Alternatives:* OR semantics (the defaults would then show nearly everything); turning "mine" off by default (differs from the spec's defaults even when ownership is configured).
- **Jev circuit breaker.** After 3 consecutive `ErrUnavailable` failures in one `labels.Run`, no new request is sent; the remaining questions become unknown labels and `Unavailable` is set. Any success resets the count. *Why:* against an endpoint that times out, every request waited the 30 s HTTP timeout and labels were stored only at the end, so an item sat in "analysing jev" for minutes before it showed "labels unavailable". *Alternatives:* trip on the first failure (one 429 would mark the item unavailable); a breaker shared across items (state in the daemon; not needed yet).
- **Token estimate.** `jev.EstimateTokens` is 2.5 bytes per token, rounded up (was 4). Every one of the 218 recorded requests used 2.54–3.93 bytes of compact request JSON per reported input token, so 4 under-counted all of them, and a request packed to the 32k/64k limits could be rejected; a rejected request is a fatal `labels.Run` error for the item. A test checks the estimate against every recording. No recorded request changes (the largest is about 15k estimated tokens). *Alternatives:* 3 bytes per token (the smallest recorded ratio is 2.54); pack to 90% of the limits (no guarantee). Follow-up: a 4xx caused by one unit could mark only that unit unknown instead of failing the item.
- **`effect` and `reversible` stay live (corrects "M1 live questions").** That entry lists both as M1 consumers, but nothing in M1 reads them: `effect` feeds an Overview block and `reversible` feeds risk/rollback (M3). They stay live anyway: the unit request carries all live unit questions, so deferring them changes every recorded request, and re-recording spends budget to save two questions per unit (about $0.0001 per unit). They are stored and visible in the raw-labels overlay. When the M3 rollback/risk work lands it consumes them; if a re-recording happens before then, defer both at that time. *Alternatives:* defer now and re-record (spend, no user-visible gain); add a placeholder chip (UI that the spec does not ask for in M1).
- **"changed since last look" filter.** It is the tenth filter on key `0`, a placeholder like "has comments" and "has findings": selecting it shows "no data yet" until M4's since-you-looked marks exist. *Why:* the spec lists it among the available filters; it was missing without a note.

## 2026-10-13 — M1 review fixes 2

- **Untracked files.** `internal/labels/inputs.go` (`InputHash`/`InputHashes`) and `internal/jev/estimate_test.go` were called/cited from 4732c37 on but never committed, so a clean checkout of main did not build. Both are committed now; a fresh `git worktree` of HEAD was vetted to confirm. *Alternatives:* rewrite history to fold them into 4732c37/d4dec45 (CLAUDE.md forbids force-push).
- **Partial Jev failure is not an outage (refines "Jev circuit breaker").** `ItemAgg.LabelsMissing` (Overview git facts only, filters off, picker `labels unavailable`) is set only when Jev is nil, `labels.Run` failed, no call answered, or the circuit breaker tripped. When some calls fail with `ErrUnavailable`, the affected labels are unknown (the units show as unsure), the jev RunLog note says "N of M Jev requests failed", and the next run asks those units again. `jev.HTTP` retries a 429 or 5xx once, after `Retry-After` (≤ 10 s) or 1 s. No automatic re-run is queued: the next `r`, push or daemon re-analysis picks them up. *Why:* one 429 among hundreds of calls switched the whole item to git facts until the user pressed `r`; spec failure handling gives unknown labels to the affected units. *Alternatives:* more retries with exponential backoff (longer runs against a real outage; the breaker already handles that); auto-queue items with unknown labels (risk of a loop against a persistent failure; revisit with a back-off in M3).
- **Line-label cache needs every line (supersedes the cache rule in "Pipeline, daemon and recorder (M1)").** A unit is a cache hit only when every live line question has a current, known label at every added-line position (`model.AddedLineNumbers`), like unit questions. The labeller asks about every `+` line, so a gap means a failed call or malformed answer. *Why:* one known line satisfied the old check, so a failed line batch stayed unknown forever (no gutter marks, unit permanently unsure) and the next run reported a cache hit, clearing `labels unavailable`. *Alternatives:* treat only stored unknowns as incomplete (misses lines never stored).
- **Discovery keeps label-based minutes.** `setUrgency` (discovery and add) uses the stored `ItemAgg.Minutes` when the pass did not move the head, the item is not stale, and it was analysed at this head (or is on its first analysis); otherwise the line-count stand-in. Label urgency terms are still 0 until M3, so minutes are the only aggregate term to keep. *Why:* every poll reset `~N min` and the sort to the stand-in and rewrote the item. *Alternatives:* store a "minutes from labels" flag on the item (another field to keep in sync).
- **User questions merge over the built-ins.** `questions.Load` parses `prt.questions.toml` and merges it over the built-in catalogue by (scope, id): a new id is appended, a repeated id replaces the built-in row (so `deferred = "off"` on a built-in id switches it off). *Why:* spec "Users may add questions"; a file with one added question used to drop kind, risk, depth and every other built-in, quietly degrading aggregate, chips and filters. *Alternatives:* keep replace semantics and reject a catalogue missing the M1-consumed ids (users would have to copy the whole catalogue to add one row).
- **`risk ≥ N` uses N = 3, fixed.** It matches the spec's own "risky" threshold (`test_gap` counts units with risk ≥ 3), so the filter shows exactly the units the test-gap number talks about. *Alternatives:* cycle N 2–5 on repeated toggles or a config key (no spec key exists; revisit if users ask).
- **Minimap cells are click targets.** A click on a minimap cell opens that cell's unit; a click elsewhere on the row, and `enter`/`L` (which act on rows), still open the file's first unit. *Why:* spec "Clicking or enter on a cell opens that unit in Code"; keyboard cell selection would need a cursor inside the Overview, which has none. *Alternatives:* left/right cell selection (new focus state in a scroll-only page).
- **Demo stays offline.** `make demo`/`make snapshot` keep `--offline` (see "M1 integration"); the README now says they show the labels-unavailable mode only. *Alternatives:* a `--jev-replay` flag (request keys depend on the fixture config and would silently miss outside `pipelinetest`).

## 2026-10-13 — M2 shared contracts for parallel work

- **Decision:** before splitting M2 the lead fixed these contracts:
  - **Packages.** Stage 2 + stage 3 candidates in `internal/link` (`Structural(ctx, link.Input)`, `EmbedCandidates(units, vecs, EmbedOptions)`, `MechanicalCandidates(units)`, `UnitText`); stage 5 in `internal/cluster` (`Cluster(cluster.Input) cluster.Result`, pure); the non-unit Jev questions in a new `internal/judge` (`Pairs`/`Confirm`, `Names`/`NameCtx`/`ApplyNames`, `SeparatePR`/`SplitCtx`/`Suggestion`); `pipeline` wires them. `parse.Grammar(lang)` is exported so `link` runs its own tree-sitter queries.
  - **Link directions** (doc comment on `model.Link`): calls caller → callee unit; tests test → code under test; imports importer → defining unit; moved origin → destination; same-symbol and embed-neighbour lower Order → higher; mechanical example → member (a star, not all pairs). Structural Weight 1, embed Weight = cosine. A Jev-confirmed link keeps Kind, Weight and endpoints and gets Source jev, `Relation` and the new `Link.P`.
  - **UnitText** = path, signature, then only the `+`/`-` lines (context dropped), newline-joined (spec: "path + signature + diff lines (added and removed)"; the M0 stub also embedded context lines). Embedding recordings are keyed by this text.
  - **Cached Jev answers for pairs, groups, files are labels** on new subjects `model.PairSubject(a, b)` (sorted ids), `model.GroupSubject(groupID)` (content-based) and the existing `model.FileSubject`; `Ctx` carries the title/bullets hash (names) or group-list hash (separate_pr). "Nothing runs twice" with no new store kind.
  - **Group model.** `Group` gains `NameSource` (`bullet` | `placeholder`), `NameP`, `Links` (the links that bind it: "why these belong together"), `Ejected` (mechanical non-matches), `UserEdited`; `UnitIDs` are in review order. Every unit is in exactly one group; a unit no kept link binds is its own group with `Ungrouped` set, listed last.
  - **User edits.** `model.GroupEdit{ItemID, Op merge|split|reset, Units, At}` keyed by unit ids (not group ids, which change with membership), persisted by new `Store.AppendGroupEdit`/`GroupEdits` (reset clears; removed with the item), applied in order by `cluster` after clustering. TUI → `daemon.Client.EditGroups(proto.GroupEditParams)` (`MethodEditGroups`), which stores the edit and queues a non-forced re-run (cached stages are hits, clustering re-runs).
  - **Grouping record and split suggestion** live in `ItemAgg.Grouping` (`model.GroupingStats`: units → structural by kind, embed and mechanical candidates, pairs asked, Jev kept by relation, rejected, cut, groups, mechanical collapse count, ejected, ungrouped, user edits, embed skip note) and `ItemAgg.Split` (`model.SplitSuggestion`), both set by the pipeline after `aggregate.Item` — the Pipeline page (M3) reads them as is. `aggregate.Input` gains `Links` for the fidelity rule below.
  - **Catalogue.** Pair `relation`, group `name_bullet` and file `separate_pr` are live (separate requests; no unit request changes, so the M1 recordings still replay). `relation` gains criteria text per option. Unit `interface` stays deferred (now `M3`): making any unit question live changes every recorded unit request, and its "link list" input would make labels depend on stage 2. Cluster reads the label when present and otherwise derives "interface" structurally (a unit that another changed unit calls or imports). `follows_pattern`, `duplicates_helper` and file `role` move to `M5` for the same re-recording reason.
  - **Replay degrades.** `jev.Replay.MissUnavailable` turns a missing recording into an error wrapping both `ErrNoRecording` and `ErrUnavailable`, so a demo/snapshot replay miss shows "labels unavailable" instead of failing a stage. Tests keep it false (a miss stays loud). This replaces "Demo stays offline" for M2.
  - **Group names until M3.** Groups whose `name_bullet` is `none` (or unknown, or the PR has no bullets) and ungrouped units keep a code-derived placeholder (`NamePlaceholder`) until the fast LLM writes three-word titles in M3. Ungrouped singletons are not sent to Jev for a name.
- **Why:** lets link, cluster, judge, pipeline and the Code page be built in parallel on disjoint directories without anyone touching `go.mod` or shared packages.
- **Alternatives:** a new store kind per Jev answer type (more interface churn); group-id-keyed edits (break on every re-cluster); storing the grouping record in `Detail` (needs a store kind and daemon wiring for the same data); making `interface` live now (re-record every unit request for an ordering hint the call graph already gives).

## 2026-10-13 — Link: repo symbol index (M2)

- **Decision:** the index is built from `Src.ListFiles(BaseSHA)`: supported files (`parse.Lang != ""`), sorted, at most **5000**, each at most 2 MiB; a file whose `Show` fails or that has a syntax error is skipped. Per file it stores declared names (top-level declarations, `Class.member`, object-literal methods `obj.m`, Go `Recv.M`, Go type/const/var, Python module assignments), import statements and local `export { a }` names. Cached as JSON under Cache ns `symindex`, key `<BaseSHA>:v1`; a failed listing is not cached. Declarations and imports of the item's changed files (both sides) are overlaid on every run.
- **Why:** spec stage 2 "symbol index of the repo cached per base SHA"; a sorted cap is deterministic; a transient error must not stick.
- **Alternatives:** index only the changed files' directories (misses cross-package callees); no cap (unbounded first run on monorepos).

## 2026-10-13 — Link: calls, imports, moves, same-symbol (M2)

- **Calls.** Call sites come from a unit's own rows (head rows; base rows for pure deletions), context rows included. The callee resolves to one file in this order: a namespace qualifier (`import * as x`, Go package, Python module); the calling file; a named import (relative TS/JS specifiers try `.ts .tsx .js .jsx .mjs .cjs` and `/index.*`); Go: the caller's package directory; namespace/star imports that declare the name; a unique declaration in the index. Two or more candidate files → no link. Targets are the changed units of that file whose Symbol equals the name, ends in it, or is a member of it (`ApiError.constructor` for `new ApiError`, because parse never makes a class itself a symbol). `export … from` re-exports are followed up to 3 hops. *Alternatives:* only `+` lines (misses callers whose call line did not change); every same-named unit (noisy).
- **Imports.** TS/JS: a changed named specifier of `import {s}`/`export {s} from` links to the defining units of `s`. Go, Python `import mod` and TS `import * as x`: a changed import line links to the changed units the file calls as `alias.Name(…)`. Python `from x.y import z` resolves `x/y.py` or `x/y/__init__.py` (relative to the file, then the repo root; `.mod` from the package).
- **Symbol-level moves.** A top-level declaration that vanishes from file A and appears in file B (≠ A) with the same name and equal normalised text (whitespace collapsed, blank lines dropped, leading `export `/`export default ` ignored) links every unit with a `−` row in the old declaration to every unit with a `+` row in the new one. In #101 this adds the `ApiError` class-line units and `authHeader` (which gained `export`) on top of parse's `DetectMoved`.
- **Same-symbol.** A star from the lowest-Order unit with that Symbol; members from the root's own hunk are skipped.
- **Errors.** `Structural` fails only on cancellation; Show/ListFiles errors, syntax errors and big files just give fewer links. With `Src == nil` it returns moved, same-symbol and test links.

## 2026-10-13 — Link: test links, symbol first and filename as fallback (M2)

- **Decision:** a test unit links (kind `tests`) to every code unit whose symbol it mentions as a whole word (last Symbol segment, ≥ 4 runes; class name for constructors), anywhere in the item. Only when a test unit mentions **no** changed symbol does the filename rule apply: it links to the units of the code file with the same stem (test markers `.test`, `.spec`, `_test`, `test_` stripped) in its directory, or anywhere under the parent of a `__tests__`/`test`/`tests` directory.
- **Why:** spec "test-to-code by filename and symbol name". With both rules OR-ed, every test unit of `client.test.ts` linked to every unit of `client.ts`, and #101 became one 38-unit component (the M2 integration snapshot). A test that names what it tests is the stronger signal; the filename still attaches tests that mention nothing changed.
- **Alternatives:** filename AND symbol (drops tests whose bodies do not name the unit); both rules OR-ed (the integrator's first snapshot: whole PRs collapse).

## 2026-10-13 — Link: embed and mechanical candidates (M2)

- **Decision:** `EmbedCandidates` skips units with a missing or all-zero vector and pairs of different lengths; pairs sharing a non-empty ShapeHash are removed **before** ranking, so top-K slots go to pairs Jev must judge. Neighbours are `max(|cos ≥ MinCosine|, TopK)` nearest, ties by Order; each pair once, From = lower Order, Weight = cosine. `MechanicalCandidates` is a star from the lowest Order of each shape class of ≥ 2. Defaults exported as `link.DefaultMinCosine` (0.72) and `link.DefaultTopK` (3).

## 2026-10-13 — embed_min_cosine 0.88 for the fixture repo (M2)

- **Decision:** the spec default `embed_min_cosine = 0.72` stays the config default. The fixture repo's `prt.toml` (`testdata/fixtures/prt.toml`) sets `0.88`.
- **Why:** embeddinggemma-2 cosines are compressed: within a fixture PR the median is .74–.80 (#102's lowest pair is .717), so 0.72 made almost every pair a candidate — 1802 pair requests, and the Pipeline page's "embed candidates" number meant nothing. At 0.88 (about the top 10 % of pairs) plus top-3 the fixtures send 216 pairs. Embedding recordings are text-keyed and unaffected; the Jev pair recordings were made after this setting.
- **Alternatives:** change the default for everyone (spec value); a relative threshold (top-K plus a z-score over the item's pairs; better for any model, but a spec change — revisit with M3 data).

## 2026-10-13 — Pipeline stages 2–5 (M2)

- **Pair selection.** Pairs judged by `relation` = every embed candidate + the mechanical candidates of shape classes smaller than `mechanical_min`, minus pairs with a structural link either way; deduplicated by `PairSubject` (highest weight kept) and sent sorted. A stored known relation is a cache hit. Cluster gets structural + confirmed + **all** mechanical candidates (it verifies classes ≥ `mechanical_min` by shape hash). *Why:* spec stage 4 "structural links are never sent"; larger mechanical classes need no Jev, and two-member classes could never group otherwise.
- **Soft failures after parse.** link error → row failed, run continues, `aggregate.Input.Links` nil (M1 fidelity rule); no embedder or `embed.ErrUnavailable` → row skipped with a note (copied to `GroupingStats.EmbedNote`), other embed errors → row failed, structural-only groups; the pair pass never fails the row or sets LabelsMissing; Jev failures in naming/split keep placeholders/no split and are noted. An unknown answer never overwrites a known stored label.
- **Skip status.** A stage marks itself skipped with `skipStage(rl, note)`; `runStage` keeps that status. Stage functions are fields of `stageFuncs` on the Pipeline (tests swap them per Pipeline; no globals).
- **Links nil vs empty.** `aggregate.Input.Links` is non-nil whenever stage 2 succeeded (empty when nothing links), nil only when it failed.
- **Names and split.** `name_bullet` is asked only for real groups whose label is unknown or has another Ctx (`judge.NameCtx(title, bullets)`); ungrouped groups never. `separate_pr` only when the item has ≥ 2 real groups (one request per file with units; Ctx `judge.SplitCtx(summaries)`); otherwise `Agg.Split` is nil.
- **Breakers.** Each judge call has its own 3-strike breaker (the fixed API cannot share one). *Follow-up (M3):* skip pairs/names/split when unit labelling came back Tripped/Unavailable.

## 2026-10-13 — Judge request shapes and truncation (M2)

- **State.** Fixed-order structs; unit state as `labels.UnitState`, diff lines capped at 2000 bytes on a rune boundary. Pair `{a, b}` with `a` the smaller id (no cosine, so keys do not move when embeddings change); name `{pr{title}, group{units}}` with bullets only as the question's criteria; split `{file{path, units}, groups[{name, files, units}]}` without group ids or title.
- **Truncation.** If a request exceeds 32k (state + longest question) or 64k (request), a binary search finds one shared per-diff line cap; cut diffs keep head and tail with "… N diff lines elided …". If even empty diffs do not fit, the call errors naming the subject before any request.
- **Ctx hashes.** `NameCtx = ContentHash("name_bullet", title, join(bullets, "\n"))[:16]`, `SplitCtx = ContentHash("separate_pr", json(groups with sorted files))[:16]`; pair labels have Ctx "".
- **Apply helpers.** `Confirm` (keepP ≤ 0 → `judge.DefaultKeepPairP` 0.6) matches by `PairSubject`; `ApplyNames` caps names at 60 runes; `Suggestion` reads paths from the label subject. No bullets → no name calls; groups get a decided `none` with Model `judge.ModelNoBullets` ("prt:no-bullets").

## 2026-10-13 — Jev per-request overhead in token estimates (M2)

- **Decision:** `jev.RequestOverheadTokens = 400` and `jev.EstimateRequestTokensLen(n) = EstimateTokensLen(n) + 400` bound a whole request's input tokens; `TestEstimateTokensCoversRecordings` and the recorder's dry-run total use it. Batching and truncation (labels, judge) keep the body-only estimate.
- **Why:** the M2 pair/name requests (~1 KB) were counted up to ~330 tokens above 2.5 bytes per token of their body: the gateway adds fixed input per request, which large M1 unit requests hid under the per-byte margin. Limits are 32k/64k, so the overhead does not change any batching decision on realistic requests.
- **Alternatives:** lower the per-byte ratio (over-counts large requests badly); add the overhead to every limit check (changes M1 batching in tests with tiny limits for no real gain).

## 2026-10-13 — Cluster: oversized components (M2)

- **Decision:** a component over `max_group_units` is first split by cutting Jev-confirmed links, weakest first (lowest Weight, then P, then the last link by order). A piece with no Jev link left is split at structural **bridges** (Girvan–Newman style): remove the unit pair with the highest edge betweenness (ties: looser kind first — tests, imports, same-symbol, calls, moved — then the later pair) until the piece falls apart; a removal that would split off a single unit is put back and never tried again; only links crossing the final pieces count as cut. A piece where no such split exists (a star: one helper and its callers) stays whole.
- **Why:** spec stage 5 says to cut "its weakest embed links" and is silent once none are left. Keeping every structural component whole left #101 as one 33-unit group joined by test units that exercise several features. Never shedding single units keeps stars (a rename's call sites, #102's 17-unit `requestJSON` group) intact instead of turning them into a group of 8 plus ungrouped leaves.
- **Alternatives:** keep structural components whole (the cluster package's first version); cut structural links by kind priority (arbitrary); min-cut/spectral partitioning (heavier, harder to keep deterministic).

## 2026-10-13 — Cluster: mechanical groups, roles, names, edits (M2)

- **Mechanical.** Classes are mechanical links grouped by From in example Order. Verified members = the example (non-empty hash) plus targets with the same hash; a unit already in an earlier mechanical group is skipped. Ejections are recorded only when the group forms. Members leave the general graph; Source-embed mechanical links never bind outside a formed group; Source-jev mechanical links of a failed class act as ordinary edges.
- **Roles.** A test unit (test path, or From of a `tests` link) stays a test even when called or labelled interface. Interface = known `interface` label (P ≥ 0.5) or To of a calls/imports link from another member. Group order: any interface → callers → all tests → mechanical → ungrouped; ties by smallest unit Order.
- **Placeholder names.** Base = Symbol of the first unit, else the file name: `"<base> (+n−1)"`, mechanical `"<example base> ×<Count>"`, ungrouped `"<base>"`.
- **Edits.** Applied in order after clustering; merge and split behave the same: the edit's units leave their groups and form one `UserEdited` group; groups that lose members are `UserEdited` too. A non-mechanical single-unit group is always Ungrouped (also a unit the user split out). A mechanical group that loses members stays mechanical only with ≥ `mechanical_min` left; if its example was removed, the lowest-Order member becomes the example and the old star links are dropped. Stats are counted over the final groups.

## 2026-10-13 — Aggregate: fidelity needs a wired unit (M2)

- **Decision:** with `Input.Links` non-nil, a bullet is Implemented only if at least one claiming unit is wired. A unit is unwired when `Dels == 0` (added or moved-in), it has a Symbol, it is not a test, and no calls/imports link from a non-test unit of the item points to it (`tests` links and callers outside the item do not wire). For mechanical groups only the example's lines count. `Links == nil` keeps the M1 rule.
- **Why:** M1 follow-up "an unused helper does not implement a behaviour": #101's logout bullet is claimed only by the uncalled `clearProfileCache`; the restored `Expectations` check now holds on the recordings.
- **Alternatives:** require every claimer to be wired (a bullet often spans a helper and its caller).
- **Note:** the test-path rule is copied in pipeline, cluster, aggregate and link; a shared `model.IsTestPath` is a follow-up.

## 2026-10-13 — Code page mode B (M2)

- **Keys.** `g` (new `[keys]` action `mode`) toggles by file / by change on the Code page; the Code page's Top drops the Mode keys, so it is `home` by default (`gg` not implemented); picker and Overview keep `g` as top. This supersedes "g is bound to Top for now" in "Code page mode A (M1)". New actions `merge_groups` (`M`) and `split_group` (`X`); the `[keys]` action list is now the M0 list plus `mode`, `merge_groups`, `split_group`.
- **No groups.** `g` without groups stays by file with the note "no groups yet" and resets the requested mode; by change is a preference kept across items while their details have groups.
- **Cursor.** `g` keeps the same unit at the same offset (on a first unit, the group/file top is shown); a mechanical member lands on its example; a same-item reload after a regroup keeps the cursor on the same unit.
- **Merge/split.** `M` marks a group, `M` on another merges (union in list order); `M` again or `esc` cancels. `X` splits from the cursor unit to the end of the group (on the first unit only that unit); refused on ungrouped, mechanical and one-unit groups. `PendingEdit()` hands the edit to the root model, which calls `client.EditGroups` ("regrouping…" / "regroup failed: …").
- **Rendering.** Placeholder names drawn `‹name›` with a "placeholder name" line; bullet chip `bN .82`; list meta "3u 2f · b2 .82 · — · not reviewed" (tier and review status are M3 placeholders); "── ungrouped (N) ──" and "── split suggestion ──" sections (split rows are mouse targets); a "why these belong together" block (`calls  A.tsx A → client.ts getUser`, `jev same intent .82 · cos .79  a → b`, `same shape ×N (verified)`); mechanical groups show the example, "×N verified identical", a members line and "promoted out" lines; "findings: none yet (review lands in M3)". The snapshot writes a `picker + code-by-change` screen per item.

## 2026-10-13 — Demo and snapshot replay recorded answers (M2)

- **Decision:** `prt --jev-replay DIR` answers Jev from recordings (`jev.Replay{MissUnavailable: true}`: a miss means "labels unavailable", never a failed stage) and `--embed-replay DIR` embeds from recordings (`embed.Replay`; a miss skips stage 3 with a note). Either flag reads config only from the repo's `.git/prt` (never XDG), because request keys depend on config, exactly as the recorder runs. The daemon reports a standing source note ("Jev: replay of … · embeddings: replay of … (offline)") as an EventLog on every discovery. `make demo` and `make snapshot` pass both flags over `testdata/recordings`; new `make record-embed`; `record-jev`/`record-jev-dry` pass `--embed replay`. This supersedes "Demo stays offline" (M1 review fixes 2).
- **Why:** M2 asks for an offline demo that shows labels and groups; a key mismatch degrades instead of failing.
- **Alternatives:** keep the demo unlabeled; a separate demo binary.

## 2026-10-13 — M2 recordings (result)

- 111 Ollama embedding recordings (all fixture units, 4 calls, $0) and 276 live Jev requests for pairs, names and splits ($0.0096; cumulative Jev spend $0.0293 of $1.00). `TestRecordedReplay` replays strictly (any miss fails and names its key) and asserts `GroupExpectations`: #102's rename forms one mechanical group with Count ≥ 5 (#102 also has legitimate mechanical classes of 3–4, so "exactly one mechanical group" is read as "exactly one with Count ≥ 5"), #101's `getUser` change groups with its caller updates, the moved `ApiError` code is linked as moved, and test units link to the code under test.

## 2026-10-13 — M2 review fixes

- **Group edits during a run re-run the item.** `EditGroups` queues a `rerun` job: like a forced job it is kept while the item is in flight and starts after that run, but it is not forced (cached stages stay hits). *Why:* the queue dropped non-forced jobs for in-flight items, and `clusterStage` reads the edits once, so an ⇧M/X made after that read (a second merge while the first regroup ran, or an edit during discovery's run) was stored but never applied. *Alternatives:* make edit jobs forced (refetches and re-asks nothing new, but bypasses the cache meant for this path); re-read edits at the end of the cluster stage (a race remains).
- **Stage 2 failure keeps the other links (refines "Soft failures after parse" and "Links nil vs empty").** Clustering gets the Jev-confirmed and mechanical links even when stage 2 failed; only `aggregate.Input.Links` stays nil then (the M1 fidelity rule), via `groupOut.noStructural`. *Why:* the old `keptLinks` returned nil without structural links, so stages 3–4 were thrown away and every unit became ungrouped. *Alternatives:* none worth keeping.
- **`separate_pr` Ctx is per file (supersedes `SplitCtx` in "Ctx hashes").** A label's Ctx is `judge.FileCtx(SplitCtx(groups), units of the file)` = `ContentHash("separate_pr", splitCtx, sorted unit ids)[:16]`. Unit ids are content hashes, so a push that rewrites a file asks again even when names, paths and counts are unchanged; other files keep their answers. *Why:* spec "cached by content hash"; the group-list hash alone reused a stale answer. *Alternatives:* add group ids to `SplitCtx` (any change anywhere re-asks every file).
- **Embed skip note shown by change.** The Code page's bar line shows `Agg.Grouping.EmbedNote` (e.g. "no embedding model: groups from structural links only") by change, joined with "labels unavailable — filters off" when both apply; not by file, where groups are not shown. *Why:* spec failure handling "groups come from structural links only, with a status note"; the note only reached the RunLog and the log file until the M3 Pipeline page. *Alternatives:* a daemon EventLog per skipped row (one more toast per item and run, gone once dismissed).
- **Mechanical repeats collapse by file.** In mode A a non-example member of a mechanical group is drawn as one line, "mechanical repeat of <example file symbol> (×N) · +a −d (z to expand)"; `z` on the file expands it, like noise. Unsure units stay expanded (same rule as noise). By change nothing changes (the group already shows only its example). *Why:* spec Code page mode A "Collapsed by default: … and mechanical repeats". *Alternatives:* a `mechanical` NoiseReason from aggregate (would also mark the units as noise in minutes and attention, where the spec's mechanical rule already zeroes repeats separately).
- **Mechanical verification checks the literal edit (refines "Cluster: mechanical groups").** A member needs the example's shape hash **and** its edit signature: the multiset of tokens its `-` lines have that its `+` lines lack, and the reverse (words, whole quoted strings, single punctuation; whitespace ignored). A rename `getUser` → `fetchUser` at call sites with different arguments still verifies; `"k": "old"` → `"new"` and `"k": "alt"` → `"neu"` (same shape, every literal mapped to `$s`) do not, and the second is ejected. *Why:* verification compared the same shape hash that produced the candidates, so ejection could never happen and unrelated literal edits (most visibly in JSON/YAML, which have no grammar) were collapsed as "×N verified identical" and their minutes zeroed. *Alternatives:* compare full literal-preserving token streams (would eject renames whose call sites differ in arguments); never collapse grammar-less files (loses real repeats such as a version bump across manifests).
- **ONNX falls back to Ollama (refines "Embeddings: Ollama first, ONNX stub").** `embed.FromSpec("onnx:…")` checks `ONNX.Ready()` once; when not ready (always, until the adapter exists) it returns the Ollama adapter with `DefaultOllamaModel`, so `Model()` and the cache keys name the model that actually answers. *Why:* spec stack "fallback to a local Ollama /api/embed if ONNX is unavailable"; with the spec's sample `models.embed = "onnx:…"` stage 3 was skipped on every run. *Alternatives:* a per-call fallback wrapper (Model() is read before Embed, so cached vectors of two models could mix in one item).
- **Symbol index: failed file reads are not cached (refines "Link: repo symbol index").** A `Show` failure still skips the file for this run, but the index is then not cached for the base SHA, so the next run rebuilds it. *Deferred:* reading blobs through one `git cat-file --batch` process and a per-base-SHA singleflight (performance only; the first run on a large repo is slow but correct). *Alternatives:* cache anyway (a transient error removes the file's calls and imports for that base forever).
- **Socket edit test.** `TestSocketEditGroups` sends ⇧M over Socket → Serve → InProcess and checks units, item, `At` and a RemoteError for a bad edit.

## 2026-10-13 — M2 review fixes, round 2

- **Mechanical classes are partitioned by literal edit (supersedes "Mechanical" in "Cluster: mechanical groups" and refines "Mechanical verification checks the literal edit" and "Pair selection").** The key is `shape.Key` = shape hash + edit signature (new package `internal/shape`, shared by link, pipeline and cluster). `link.MechanicalCandidates`: per shape class, a star per key partition from its lowest-Order unit, plus a link from the class root to every other partition root. `cluster`: a family is a connected component over mechanical links; every key partition with ≥ `mechanical_min` units is a group with its own lowest-position example (example → member links are taken from the input, or made by code when the candidate star did not propose that pair); family units in no group are listed as ejected on the family's first group. `pairCandidates` skips only mechanical pairs inside one key partition of ≥ `mechanical_min`; cross-partition pairs and small partitions go to Jev. *Why:* verification compared every member with the class's lowest-Order unit only, so when that unit made a different literal edit (a `"name"` change next to five version bumps, or two different renames of one shape) every real repeat was ejected, never sent to Jev, and kept apart from embed kNN (same shape) — all ungrouped. *Alternatives:* retry with the first ejected unit as example (repeats per extra edit, same result); choose the most common signature as example (still loses a second real class).
- **A merge edit needs two surviving units (refines "Edits").** Unit ids not in the item are dropped as before, but a merge left with fewer than two units is skipped and not counted; splits still apply with one. *Why:* a merge {a, x} after a push rewrote x only tore a out of its group into an ungrouped singleton, the opposite of what was asked. *Alternatives:* skip a merge when any unit is missing (a push that touches one of five merged units would drop the whole merge); keep (the reported fragmentation).
- **Stage 5 failure clears the stored groups (refines "Soft failures after parse").** When clustering fails (not cancelled) the pipeline stores no groups and this run's kept links, so Detail does not serve the previous head's groups next to an aggregate without grouping. A store error in that clean-up is only logged. *Alternatives:* a fallback of one ungrouped group per unit (the TUI already makes singletons for units without a group).
- **Structural split is bounded and cancellable (refines "Cluster: oversized components").** Betweenness runs on slices indexed by local position and is recomputed every `1 + pairs/64` removals instead of after every removal (parts under 64 pairs keep the exact Girvan–Newman order, so all existing results are unchanged); each removal still checks connectivity and the put-back rule. `cluster.ClusterContext(ctx, in)` checks ctx in the split loop and in `refine`, and the pipeline uses it. *Why:* a 400-unit structural component took ~116 s and could not be cancelled; it now takes under 1 s (`TestStructuralSplitScales`). *Alternatives:* a size cap above which parts stay whole (fast, but leaves huge groups); Tarjan bridges only (misses splits that need two cuts).
- **Pair requests are capped per item (`max_pairs_per_unit`, default 3; not in the spec).** After the candidate pairs are deduplicated, at most `max_pairs_per_unit × units` are judged, highest cosine first (mechanical pairs weigh 1), ties by subject; dropped pairs are removed from the candidates too, and the jev row notes "(N over the cap not asked)". *Why:* with the shipped embedder (embeddinggemma-2) the spec's `embed_min_cosine = 0.72` makes nearly every pair a candidate (1802 requests for the four fixture PRs), one request each; top-K alone already yields about 3 pairs per unit, so the cap keeps that budget while still allowing threshold neighbours. `TestRecordedReplay` and its group expectations still pass on the fixture recordings (0.88). *Alternatives:* a model-aware cosine default (hides the spec value and depends on model names); a hard per-item constant (does not scale with PR size).
- **OLLAMA_HOST is read like Ollama's client (refines "Embeddings").** `embed.OllamaBaseURL`: scheme defaults to http, host to 127.0.0.1, port to 11434 when no scheme is given (80/443 with an explicit http/https), path kept. *Why:* `OLLAMA_HOST=0.0.0.0` (common for the server) became `http://0.0.0.0` on port 80, so stage 3 was skipped on every run. *Alternatives:* none.
- **`ListFiles` uses `ls-tree -z`.** *Why:* `core.quotepath=off` already keeps non-ASCII names verbatim (the review's café example was not affected), but git still C-quotes names with a quote, backslash, tab or newline, which then dropped out of the symbol index. *Alternatives:* none.
