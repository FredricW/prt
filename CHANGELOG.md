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

### M2 — groups (released 2026-10-13)

Release gate: `go build ./...`, `go vet ./...`, `make lint` (0 issues),
`make test` and `make snapshot` all green on `4742b15`; `make demo` runs the
TUI offline against `testdata/fixture-repo` with `testdata/fixtures/prs.json`,
replaying `testdata/recordings/{jev,embed}`. Embeddings recorded once from local Ollama
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
  review status in the group list (M3); the ONNX embedder (still a stub;
  falls back to Ollama); batched `git cat-file` reads and a per-base-SHA
  singleflight for the symbol index (performance only).
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

#### Fixed (M2 review, rounds 1 and 2; details in DECISIONS)

- Group edits made mid-run re-run the item; a merge needs two surviving
  units; a stage 5 failure clears stale groups.
- Stage 2 failure keeps Jev-confirmed and mechanical links.
- `separate_pr` answers are cached per file content.
- Mechanical groups verify and partition by the literal edit, so unrelated
  literal edits are no longer collapsed as "×N verified identical"; mode A
  collapses mechanical repeats by file.
- Structural splits are bounded and cancellable (400 units: ~116 s → <1 s);
  pair requests are capped at `max_pairs_per_unit` × units.
- `onnx:` embed specs fall back to Ollama; `OLLAMA_HOST` is parsed like
  Ollama's client; the embed skip note shows on the Code page.
- Symbol index: failed reads are not cached; `ls-tree -z` keeps quoted
  file names.

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

### M3 — review pipeline (released 2026-10-13)

Release gate: `go build ./...`, `go vet ./...` (also `-tags live`),
`make lint` (0 issues), `make test` and `make snapshot` all green on
`606d86e`; `make demo` runs the TUI offline against `testdata/fixture-repo`
with `testdata/fixtures/prs.json`, replaying
`testdata/recordings/{jev,embed,review}`. Reviews were recorded against the
real Anthropic API (latest run 22 calls, $0.3858; Anthropic ledger $0.8540 of
the $3.00 cap, all services $0.8833 of $5.00; earlier rows were priced at the
old assumed rates and over-count). `TestRecordedReviewReplay` replays them
strictly and `ReviewExpectations` hold, including "#101 mostly reviewed with
findings".

- **Shipped:** stage 6 routing (`prt.routing.toml`, the `when` language,
  first match, context decisions, hot-spot queueing under `deep_slots` and
  the cost cap), stage 7 reviews (fast/deep Anthropic adapter, specialist
  prompts, escalation, calibration, retry once, review cache, call log),
  stage 8 aggregate (urgency label terms, TL;DR, AI group titles, review
  totals), the Pipeline page, findings on the Code page, the Overview TL;DR
  line and `w` weights overlay, the picker `s` sort cycle and the Pipeline
  page's Urgency panel.
- **Deferred:** the calibration panel's per-question override-rate flag
  (needs per-question counts in `CalibrationStats`); `:cap` (no command line
  yet; `$` on the Pipeline page covers it); old-side (deleted-line) findings
  anchor against new-side numbers (needs a side on `model.Finding`, M4);
  callers of a breaking symbol from the repo symbol index as review context
  (M4/M5); the ONNX embedder (still a stub).
- **Blocked:** nothing. (Model prices, blocked earlier in M3, were confirmed
  from the published pricing page; see DECISIONS "M3 review fixes, round 2".)

#### Added

- `internal/route`: hand-written lexer/parser and three-valued evaluator for
  `when` (label ids, `tag.<name>`, `kind`, numbers, `&& || !`, comparisons,
  `in [...]`), `Compile` with rule-indexed errors and unknown-identifier
  warnings, `Route` (labels unknown → deep), `Gather` (24 KiB context
  blocks), `Plan`/`Budget`.
- `internal/review`: Anthropic Messages adapter (`PRT_ANTHROPIC_API_KEY`
  only; 30 s / 120 s timeouts), review/TL;DR/title prompts, tolerant findings
  parsing and anchoring, price table and estimates, replay and recorder.
- Pipeline stages 6–8 with user overrides (D/R/s), `FromStage` re-runs, live
  "k/n reviewed" progress, escalation (`escalate_below`) and deterministic
  calibration sampling (`calibration_sample`), cost cap and per-item `$`.
- Daemon methods `review_action`, `routing` (ensure/reload), `call_log`,
  `set_cap`; discovery keeps label-based urgency terms.
- `internal/pipeview`: the Pipeline page (`P`): stage strip and per-stage
  log, routing table with escalations and notes, totals, rules (`e` opens
  `$EDITOR`, save re-routes), grouping and calibration panels, call log
  (`c`), cap prompt (`$`).
- Code page: routing tier/status per group, findings by change and inline by
  file, "has findings" filter, `R` re-review on deep; AI titles as `ai·Name`.
- Overview TL;DR line (`ai·from labels`) and `w` urgency weights overlay;
  the picker shows a stage strip while an item is analysing.
- `prt --review-replay DIR`; `make demo`/`make snapshot` replay reviews;
  `make record-review` / `record-review-dry`.

#### Fixed (integration)

- The adapter no longer sends `temperature` (the API rejects it for the
  configured models); TL;DR and title calls get output headroom because the
  models reason before answering.
- The recorder exits non-zero when any live call fails.
- The cost cap counts cached reviews at their estimate and cached titles at
  their cost, so a refresh no longer reviews groups the cap had queued and
  replays do not depend on the store's history.

#### Fixed (M3 review, rounds 1 and 2; details in DECISIONS)

- A rules reload re-routes only unreviewed groups; reviewed groups keep their
  decision and findings.
- Retries are charged to the cost cap; the cap is charged at an expected
  output length with confirmed prices, so all 15 of #101's deep groups fit the
  default $0.40 cap.
- Cached findings follow their unit after a push; the review cache misses on
  a different model, prompt version or specialist prompts.
- `D`/`R` re-review on deep with a fresh call instead of a cache hit.
- Routing: a known max settles comparisons despite unknown units; choice
  labels are `mixed` on known disagreement; specialist prompts follow their
  triggers; a none-routed group forced deep gets the stage-6 context.
- Mechanical groups send only their example unit; cached TL;DR and titles are
  served without a key; Pipeline page `r` re-fetches only on the fetch stage.

#### Known limitations

- The cap bounds expected, not worst-case, cost: a response that runs to
  max_tokens can overshoot it by up to ~$0.031 per deep call.
- Group titles are only as good as three words allow ("Switch to
  requestJSON").

### M4 — comments (released 2026-10-13)

Release gate: `go build ./...`, `go vet ./...` (also `-tags live`),
`make lint` (0 issues), `make test` and `make snapshot` all green on
`8fc7718`; `make demo` runs the TUI offline against `testdata/fixture-repo`
with `testdata/fixtures/prs.json` and `testdata/fixtures/drafts.json`,
replaying `testdata/recordings/{jev,embed,review}`. Two real Jev
`comment_addressed` answers were recorded ($0.0001; Jev ledger $0.0294 of
$1.00). No GitHub write was made against real GitHub: every write is
exercised against `gh.Fake`.

- **Shipped:** local drafts (SQLite, anchored to path/line/side and unit id,
  re-anchored after pushes), the Code page comments layer (line cursor, `C`
  composer, `V` ranges, Glamour bodies, ai-drafts with `a`/`d`/`!`, GitHub
  threads with `❱` and the `t` panel to reply/resolve, Comments strip), `S`
  submit form producing one GitHub review, create-PR on branches (`S`),
  GitHub review threads loaded with the item, and "Since you looked" with
  Jev `comment_addressed`.
- **Deferred:** re-aggregation after a label override is a re-run only (full
  wiring M5); a comment lock shared by pipeline and daemon (see DECISIONS
  "Pipeline: threads, AI drafts and Since you looked").
- **Blocked:** nothing.

#### Added

- `internal/comments`: ids, anchoring and re-anchoring, GitHub merge,
  ai-drafts and their sync, accept, submission selection, view marks, Since
  helpers. `judge.CommentAddressed`; `comment_addressed` is a live question.
- `internal/gh`: real `Threads` (GraphQL, paginated), `SubmitReview` (REST,
  exact payload), `Reply`/`ResolveThread` (GraphQL), `CreatePR`;
  `ErrRejected`, `ErrBadPR`; the fake records every write.
- Store: finding marks and view marks. Model: comment lifecycle fields,
  `Finding.Side`, `Item.ViewerReviewedSHA`, `ItemAgg.Since`.
- Pipeline: threads in the fetch stage, re-anchoring after stage 1, ai-draft
  sync after stage 7, Since in stage 8 (`since-units` cache); old-side
  findings get `Side` old and rebase by the old span.
- Daemon methods `comment`, `comment_action`, `submit_review`, `create_pr`,
  `viewed`, `override`; Detail carries the item's finding marks.
- TUI: Huh submit and create-PR forms (`S`, `a`, `A`), tab cycle through the
  Comments strip, `MarkViewed`, Overview "Since you looked", draft count.
- `prt --drafts FILE`; `make demo`/`make snapshot` seed
  `testdata/fixtures/drafts.json`. The snapshot adds `overview-since`,
  `code-draft`, `code-ai-draft` and `code-thread` screens.
- Fixtures: #101's earlier head (`refs/fixture/…/v1`), three review threads,
  `viewerReviewedSha`; two local drafts.

#### Fixed (integration)

- A finding with an ai-draft (or accepted/dismissed) is no longer also drawn
  as an inline finding row by file.

#### Fixed (M4 review; details in DECISIONS)

- A V range from a deleted line to an added line is kept and sent as a
  LEFT→RIGHT range (was collapsed or refused), and re-anchors each end on
  its own side.
- "Since you looked" judges each comment against the diff from the commit it
  was made on, so a fix pushed earlier still counts.
- The pipeline takes the daemon's per-item comment lock, so a run can no
  longer revert a just-posted draft (double post) or an edit.
- Pending comments left by a crash mid-submission are drafts again at
  start-up.
- Outdated GitHub threads are no longer drawn under an unrelated unit;
  replies keep their thread's side.
- `ctrl+c` with the composer open saves the text as a draft before quitting.
- README covers the M4 keys and `--drafts`.

#### Fixed (M4 review, round 2; details in DECISIONS)

- Drafts are posted on the commit their lines describe (`Item.UnitsSHA`);
  submit and create-PR skip drafts from another commit, and create-PR
  refuses unpushed branches.
- Comment offsets count from the unit's first non-blank line; off-diff
  ai-drafts are outdated; posted copies without a GitHub id match by
  content; "Since you looked" prefers a newer GitHub review.
- `!` dismisses label-less ai-drafts and the Comments strip can
  accept/dismiss; `a` on a branch needs a PR; submit forms wait for the
  item's detail.

#### Known limitations

- Without colour Glamour shows bold as literal `**` (ai-draft bodies start
  with "**low**").
- Over the socket a partly failed create-PR returns only the error; the TUI
  re-reads items.
- The local branch `05-spike-dark-mode` has no Jev recording, so the offline
  demo/snapshot shows it as "labels unavailable" (replay miss degrades, by
  design).

### M5.1 — user-requested changes (released 2026-10-13)

Release gate: `make check` (vet, tests, lint: 0 issues) and `make snapshot`
green. No model calls were made for this milestone (no spend) and nothing
was written to GitHub. These changes are the user's direct instructions and
override the spec's "analysis runs in urgency order" (see DECISIONS).

#### Added

- Auto-review strategy: `[pipeline] auto_review = "manual" | "needs-you" |
  "needs-you+yours" | "all"` (default `needs-you`) gates every model-backed
  stage. Uncovered items get only the free stages and show
  `not analysed · r to run`; branches are auto-reviewed only under `all`.
  `r`, `:resume`, `/` and `:add` are explicit requests and always run. Every
  automatic path (daemon start, the discovery tick, first load, tab preview,
  stale-on-push, routing reload, override follow-ups, settings jobs,
  calibration sampling) is tested with fakes that fail when called.
- Stop and resume: `ctrl+x` stops the selected item, `ctrl+k` stops all,
  `ctrl+o` (or `r`) resumes; also `:stop [all]`, `:resume [all]` and the
  `stop`/`resume` socket methods. A stop marks the stage `stopped`, the item
  `stopped at <stage>` and in-flight reviews `stopped` (not failed, not
  cached); resume re-uses every finished stage from the caches.
- Settings page (`,` or `:settings`): strategy, role and owned globs, cost
  cap and deep slots, models and embedder, urgency weights and thresholds.
  Saves to the repo or global `prt.toml` (and `prt.routing.toml`) with a
  comment-preserving line editor, applies live (re-rank, re-route, queue
  resize, model swap), shows key/Ollama/gh presence (never values) and opens
  the routing and questions files in `$EDITOR`.
- Command line: `:auto <strategy>`, `:stop`, `:stop all`, `:resume`,
  `:resume all`, `:settings`; tab completion of names and arguments with
  cycling; one-line errors for unknown commands and arguments; the `?`
  overlay lists every command.
- Pipeline page and picker show `■ stopped` stages and reviews.

Final gate: `go build ./...`, `go vet ./...` (also `-tags live`),
`make lint` (0 issues), `make test` and `make snapshot` all green on
`fd59a1a`; `make demo` runs the TUI offline (`--no-daemon --offline`, replay
flags) against `testdata/fixture-repo` with `testdata/fixtures/prs.json` and
`testdata/fixtures/drafts.json`, now under the default `needs-you` strategy.

- **Shipped:** auto-review strategy gate, stop/resume, settings page and the
  M5.1 command line (below), plus two rounds of review fixes.
- **Deferred:** `Resume` after a stopped Pipeline-page `r` on a stage (or a
  forced `r`) does not keep its `FromStage`/`Force`; it continues from the
  caches (press `r` on the stage again). The ONNX embedder is still a stub.
- **Blocked:** nothing new (the M5 Anthropic recording blocker in
  `docs/BLOCKED.md` still stands).
- **Tag note:** the `m5.1` tag was created (and pushed) at `56357ac`, before
  the review fixes below. It was not moved; `main` at the "docs: changelog
  for M5.1" commit is the complete M5.1.

#### Fixed (M5.1 review, rounds 1 and 2; details in DECISIONS)

- An explicit request (Add, `:resume`, `r`) is never swallowed by an
  automatic run that may be gated to free stages only.
- A gated run never replaces a stored analysis: Stale items outside the
  strategy are only re-aggregated, and `AnalysedSHA` never moves.
- A repo `prt.toml` save seeds from the active config, so global `[keys]`,
  `[github]` and other knobs are not dropped; replay mode refuses global
  saves; saves validate only the keys they change, so `:auto manual` no
  longer fails on an unrelated value.
- A stop that arrives after a run completed keeps the analysis; stopping
  queued or aggregate-only work leaves an analysed item Ready/Stale instead
  of `stopped`; "stopped at" names the stage the run log marked.
- `s` (skip) on a Stale item is automatic, not a request to analyse the new
  head.
- Discovery queues newly covered not-analysed items (also in
  `--no-daemon`); a push on a not-analysed item re-runs its free stages.
- Settings page: model-factory notes (a disabled tier) show as warnings; the
  GitHub line names the fixture file with `--fixtures`.

#### Known limits

- In the 36-column picker, `stopped at <stage> · r to resume` is cut short.
- Changes to `prt.questions.toml` apply at the next daemon start.
- Resume drops a stopped run's `FromStage`/`Force` (see Deferred).

### M5 — polish (released 2026-10-13)

Release gate: `go build ./...`, `go vet ./...`, `make lint` (0 issues),
`make test` and `make snapshot` all green on `7df3e65`; `make demo` runs the
TUI offline against `testdata/fixture-repo` with `testdata/fixtures/prs.json`
and `testdata/fixtures/drafts.json`, replaying
`testdata/recordings/{jev,embed,review}`. Branch-state Jev answers and
embeddings were recorded ($0.0013; all services $0.8847 of $5.00).

- **Shipped:** the design canvas look, search and add (`/`), the command
  line (`:`), branch states with suggested PR titles, the Architecture tab,
  side-by-side diffs with syntax colour, `e`/`o` on the Code page,
  aggregate-only label overrides with "⚑ stale labels" reviews, and the v3
  Pipeline page with per-question override rates.
- **Deferred:** the overlap view for multi-selected items (`space` marks
  only; M6); colour cycling on the Architecture tab; the ONNX embedder
  (still a stub, falls back to Ollama).
- **Blocked:** the 8 Anthropic calls for the local branch
  `05-spike-dark-mode` are not recorded (Anthropic: "credit balance is too
  low", retried at the release gate with the same answer, no spend). The
  demo shows "title failed: review: no recording …" and one failed fast
  review for that branch; see `docs/BLOCKED.md`.

#### Added

- The design canvas look (`internal/theme`, `docs/design/`): header with
  repo, counts and role; a "Queue · ranked" picker box with urgency bars and
  why-lines; tabs `1 Overview  2 Code  3 Architecture`; titled boxes on the
  Overview (two columns from 100 columns); key hints at the bottom.
- Search and add (`/`): local branches, open PRs and closed/merged PRs
  (leading `!`, or when nothing open matches) merged as they land; `enter`
  adds to the Added section or jumps to a listed item, `tab` previews the
  item's Overview without adding it. `x` removes an added item; `space`
  marks items (the overlap view is M6).
- Command line (`:`): `:add`, `:tier deep|none|rules`, `:cap`, `:q`, with
  history.
- Branch states: Jev's `branch.state` (WIP, ready for PR, spike,
  superseded by #N) with its probability, and `abandoned?` after
  `pipeline.abandoned_days` (14) without commits; unpushed commits, the
  commit list and a suggested PR title on the Overview; ready branches
  climb with `w_ready_stale`.
- Architecture tab (`3`): the module graph around the diff (new, changed,
  removed and context modules, hop depth `+`/`-`, changed-only `d`), the
  contracts it crosses and its net effect; `enter` lists a module's files
  and jumps to one on the Code page.
- Code page: side-by-side diffs (`u`, from a 140-column terminal), syntax
  colour, `e` opens the line in `$EDITOR`, `o` opens the unit on GitHub.
- Label overrides (`!`) re-run only the aggregate; reviews of groups whose
  labels changed are marked "⚑ stale labels" with the change in the note.
- Pipeline page: the v3 layout, per-question override rates (flagged over
  15%) in the Calibration panel.

#### Fixed (M5 review; details in DECISIONS)

- Closing the search prompt after `tab` on an item already in the picker no
  longer deletes it (and its reviews and group edits); a late preview reply
  after `enter` no longer deletes the just-added item. Previews are
  discarded through the new `RemovePreview`, which only removes previews.
- The Code page shows label overrides (chips, filters, `L`, the `!` picker)
  instead of Jev's original answer; `L` says "! (was …)" with Jev's value.
- `abandoned?` now appears on its own as an idle branch ages: discovery and
  the Overview re-apply the age rule to the stored state.
- Branches added from search get their unpushed count after analysis.
- Closed and merged PRs added from search are no longer sent to Jev as open
  PRs or named as superseding a branch.
- Side-by-side needs a 140-column terminal, not a 140-column Code pane.

#### Fixed (M5 review, round 2; details in DECISIONS)

- Added and previewed items are analysed before automatic jobs; removing an
  item cancels its run, so a discarded preview no longer pays for reviews.
- A label override re-aggregates every item that shares the unit (a branch
  and its PR, stacked PRs).
- Remote-tracking refs report no unpushed commits; `w_unpushed` uses the
  run's own count.
- Clearing a fresh deep override is a compare-and-set (fixes a `-race`
  flake); the TUI runs one Preview call at a time.
- The branch Overview keeps Headlines; the Architecture legend lists box
  colours and fact codes.
