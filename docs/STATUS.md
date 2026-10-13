# Status — overnight handover (2026-10-13)

## Summary

M0–M5 are built per `docs/spec.md`. On top of that is the M5.1 and M5.2 you asked for:
- M5.1: an auto-review strategy, default `needs-you` (branches are never auto-reviewed unless the strategy is `all`); stop/resume; a settings page; command-line completion.
- M5.2: measured and tuned label quality (`make quality`), instant open of analysed items with stale-while-revalidate, the tab-click fix, capped Overview blocks with a full-list overlay, a coloured composition bar, API / Database / Flags change blocks, a sixth fixture PR, and the truncated fast review fixed and re-recorded.

M6 (overlap) is deliberately deferred because the spec marks it "later". The repo is `github.com/fredricw/prt` (`main`, pushed). `make check` (vet, also `-tags live`; tests; golangci-lint v2) is green at HEAD. Real model spend so far is **$1.4941 of the $5.00 cap**. Nothing is blocked.

Details: `CHANGELOG.md` (per milestone), `docs/DECISIONS.md` (every call made), `docs/ARCHITECTURE.md` (packages).

## Milestones

**Tag quirk:** tags `m0` (`7d27b45`) and `m5.1` (`56357ac`) were created and pushed *before* their final review-fix commits. They were not moved, to avoid rewriting pushed refs. The complete M0 is the "docs: changelog for M0" commit (`68555ad`). The complete M5.1 is `main` at `a88d1e0`. Tags `m1`–`m5` sit on their final changelog commits.

### M0: skeleton (fast PR browser)
- Done:
  - Go module, Makefile, golangci-lint v2 config.
  - Boundary interfaces with fakes: `gitx`, `gh`, `jev`, `embed`, `review`, `store` (SQLite under `.git/prt/`).
  - Unified diff parser.
  - Daemon on a Unix socket (NDJSON), plus `--no-daemon`.
  - Discovery into Needs you / Yours / Involved / Branches / Added, with stale-on-push.
  - Picker, plain diff viewer, Overview/Code tabs, rebindable keys, `--snapshot`, `--offline`, `--fixtures`, `--now`.
  - Deterministic generated fixture repo: "portal" monorepo with PRs #101–#104 and a local spike branch.
- Partial/deferred: the ONNX embedder is a stub (`internal/embed/onnx.go`, `TODO(embed-onnx)`). Ollama is the working path.

### M1: units and labels
- Done:
  - tree-sitter change units (Go/TS/TSX/JS/Python), with shape hash, formatting detection and moved code.
  - Full Jev question catalogue.
  - Stage-4 labels with a SQLite cache, a circuit breaker and per-unit partial failures.
  - Aggregate arithmetic.
  - Overview page: composition, headlines, risk, test gap, fidelity, minimap, Your slice.
  - Code page mode A: chips, gutter marks, filters, noise collapse, `L` raw labels.
  - Jev recorder with a $1 cap.
- Partial:
  - Some catalogue questions are marked `deferred` and never sent.
  - With real answers, Headlines listed about 85 rows on #101 and nearly every unit was "unsure" (spec followed literally). M5.2 tuned this: 6 rows on #101, unsure units 0.72 overall (see `docs/QUALITY.md`).

### M2: groups
- Done:
  - Structural links: call, import, test, moved, same-symbol.
  - Ollama embeddings with kNN candidates; Jev pair confirmation.
  - Clustering with split of oversized components.
  - Verified mechanical groups ("×N verified identical").
  - Jev group names and `separate_pr` split suggestions.
  - Code page mode B (`g`) with `M`/`X` merge/split, which are persisted.
- Partial:
  - `embed_min_cosine = 0.88` for the fixture repo (embeddinggemma-2 cosines are compressed).
  - Large star-shaped groups stay whole.
  - Batched `git cat-file` reads are deferred (performance only).

### M3: review pipeline
- Done:
  - `prt.routing.toml` with a hand-written `when` language.
  - Anthropic fast/deep reviewer: specialist prompts, escalation, calibration sampling, retry once, review cache.
  - Per-item cost cap (default $0.40).
  - Pipeline page (`P`).
  - Findings on the Code page, TL;DR, AI group titles.
  - Urgency ranking with the `w` weights overlay; picker `s` sort.
- Partial:
  - The cap bounds *expected* cost, not worst case. One deep call can cost more than its reservation.
  - (Resolved in M5.2: the old "most deep groups stay queued" note predated the confirmed prices. Every routed group of every fixture item now runs under the $0.40 cap.)

### M4: comments
- Done:
  - Local drafts, anchored and re-anchored after pushes.
  - Composer `C`, ranges `V`, Glamour bodies.
  - AI-drafts: `a` accepts, `d` dismisses, `!` dismisses and marks a label wrong.
  - GitHub threads (`t` reply/resolve), Comments strip.
  - `S` submits one GitHub review; create-PR on branches.
  - "Since you looked" with Jev `comment_addressed`.
- Partial:
  - **No write was ever made against real GitHub.** All writes were tested against `gh.Fake` only.
  - Without colour, Glamour shows literal `**`.

### M5: polish
- Done:
  - Design canvas look (`docs/design/`).
  - Search-and-add (`/`), including closed/merged PRs with a leading `!`; tab preview.
  - `:` command line.
  - Branch states (WIP / ready / spike / superseded / `abandoned?`), with a suggested PR title.
  - Architecture tab (`3`).
  - Side-by-side diffs (`u`, needs at least 140 columns) and syntax colour; `e`/`o` on the Code page.
  - `!` overrides re-run only the aggregate and mark reviews "⚑ stale labels".
  - v3 Pipeline page with per-question override rates.
- Deferred:
  - The overlap view: `space` only marks items (M6).
  - Colour cycling on the Architecture tab.
  - (Branch review recordings: done in M5.2.)

### M5.1: your requested changes
- Done:
  - `[pipeline] auto_review = manual | needs-you (default) | needs-you+yours | all` gates every model-backed stage, on every automatic path. Each path is tested with fakes that fail if they are called.
  - Uncovered items get only the free stages and show `not analysed · r to run`.
  - Explicit `r`, `:resume`, `/`, `:add` always run.
  - Stop/resume, in the TUI and over the socket.
  - Settings page: comment-preserving saves, repo or global scope, applied live.
  - `:auto`, `:stop [all]`, `:resume [all]`, `:settings` with tab completion.
  - Two rounds of review fixes.
- Partial:
  - (Fixed in M5.2: resuming after a stopped Pipeline-page `r` keeps `FromStage`/`Force`.)
  - `prt.questions.toml` edits apply only at the next daemon start.
  - `stopped at <stage> · r to resume` is cut short in the 36-column picker.
- No model spend, no GitHub writes.

### M5.2: your requested changes
- Done:
  - **Label quality, measured.** `make quality` runs the pipeline offline over the six fixture items and scores it against hand-written, blind ground truth (`testdata/groundtruth/`). Tuning: headlines only at P ≥ 0.60 (118 → 10 headline rows over the five baseline items; precision 0.17 → 0.60), "unsure" over the attention and risk labels only (unsure units 1.00 → 0.78 on the baseline items), attention mapping (eyes recall 5/8 → 7/8), four reworded questions. No item's firm metric is worse than its baseline. Method and boards: `docs/QUALITY.md`.
  - **Instant open.** The TUI paints a re-opened item from a 16-item cache (about 1.7 ms p50) and never shows "loading…" for it; labels load on demand. One `r` on #101 now makes 3 Detail calls instead of 70. During a re-run the old analysis stays up with `↻ updating review · <stage>`. The daemon serves a lean Detail (#101: 0.97 ms, was 8.8 ms), with an indexed label query, a read pool, label and run-log pruning, and no discovery churn. Numbers: `docs/m52-open-latency.md`.
  - **Tab clicks** hit the tab you click, tested per tab at five widths.
  - **Overview blocks** are capped with "+N more"; `J`/`K` (or `]`/`[`) focus a block and enter opens its full list.
  - **Coloured composition bar and minimap legend**; plain output unchanged without colour.
  - **API endpoints, Database, Feature flags & config** blocks from a new detector package (`internal/surface`) plus Jev contract labels.
  - **Fixture PR #106** with a migration, endpoints and a flag; all its answers recorded.
  - **Truncated fast review** fixed: higher output caps plus one retry with a doubled cap; all reviews re-recorded; the branch replay test passes.
  - Resume keeps `FromStage`/`Force`.
- Partial:
  - Lines to read is above the spec's 1/3 on #101 (0.56), #106 (0.51) and the branch (0.50). Hot spots hit 3 of 13 top-three slots. #101 keeps 3 false headline rows; #106 misses its flag headline.
  - The $0.40 cap is unchanged: it does not leave deep groups queued on the fixtures.
- Spend: Jev $0.0333, Anthropic $0.5731, Ollama $0. No GitHub writes.

### Not done
- **M6: overlap.** Deferred per the spec. The data model leaves room for it: content-based unit ids, and `semantic_conflict` is reserved. A design exists in `docs/design/Overlap.dc.html` and `docs/design/v2/Overlap.dc.html`.
- **ONNX embedder.** Still a stub that returns `ErrUnavailable`. `onnx:` specs fall back to Ollama.
- **Spec "Done means" timing targets.** Labels under 2 s and reviews under 10 s on a 400-line PR are not measured. "Reads under 1/3 of lines" is now measured by `make quality` against hand-written ground truth, and is not met on #101, #106 or the branch (see M5.2).

## Blocked

Nothing. The M5 branch-review blocker was resolved in M5.2 and `docs/BLOCKED.md` is deleted.

Also not a blocker but missing: the ONNX embedder is a stub, and Ollama is used instead.

## Spend (`testdata/recordings/SPEND.md`)

| Service | Calls | Spend | Service cap |
| --- | --- | --- | --- |
| Jev (AI Gateway) | 845 | $0.0640 | $1.00 |
| Anthropic | 105 | $1.4301 | $3.00 |
| Ollama (local) | 6 | $0.0000 | — |
| **Total** | | **$1.4941** | **$5.00 overall** |

The early Anthropic rows were priced at assumed rates and over-count somewhat (CHANGELOG M3).

## How to run

**Prerequisites:**
- Go 1.26+ (`go.mod` says `go 1.26.0`).
- A C toolchain, because tree-sitter needs cgo.
- git.
- `gh` logged in (`gh auth login`); prt reuses `gh auth token`.
- golangci-lint 2.x for `make lint`.
- Optional: Ollama with `ollama pull embeddinggemma-2`.

**Make targets:**
- `make build`: builds `bin/prt`.
- `make test`.
- `make lint`.
- `make check`: vet, tests and lint; must pass before every commit.
- `make demo`: interactive, offline, replays the recordings.
- `make snapshot`: prints screens to stdout (six items).
- `make quality`: prints the label-quality scoreboard.
- `make fixtures`.
- `make record-{jev,review,embed}[-dry]`.

**Against a real repo:** `cd your-repo; prt`. Environment variables (each is optional; prt degrades without it):
- `JEV_API_KEY`: an AI Gateway key for Jev labels. `JEV_BASE_URL` overrides the default `https://ai-gateway.vercel.sh`.
- `PRT_ANTHROPIC_API_KEY`: the fast/deep reviews. It is deliberately **not** `ANTHROPIC_API_KEY`, because that name would switch Claude Code itself to API billing on this machine.
- `OLLAMA_HOST`: the embedding server, default `http://127.0.0.1:11434`.
- GitHub uses `gh auth token`.
- Secrets are read only from the environment and `gh`, never from config.

**Daemon vs in-process:**
- `prt daemon` keeps analysis warm, one per repo, on `.git/prt/prtd.sock`. `prt` connects to it automatically.
- `prt --no-daemon` runs everything in-process.
- `--offline` never touches the network.
- Store, log and config live under `.git/prt/`.

**Key commands** (from README and the keymap; `?` lists them all):
- Navigation: `j`/`k` move the picker, `enter`/`esc`, `1`/`2`/`3` switch Overview/Code/Architecture, `P` opens the Pipeline page, `r` re-runs or resumes, `q` quits.
- Overview: `J`/`K` (or `]`/`[`) focus a block, `enter`/`space` open its full list, `esc` closes; `L` shows raw labels.
- Code page: `g` switches by file / by change, `n`/`p` and `J`/`K` move, `f` filters, `z` expands noise, `L` shows labels, `u` toggles side-by-side, `C` comments, `S` submits.
- Stop/resume:
  - `ctrl+x` stops the selected item.
  - `ctrl+k` stops all.
  - `ctrl+o` (or `r`) resumes.
- Settings page: `,` or `:settings`. `tab`/`enter` move between fields, `pgdown`/`pgup` between sections, `esc` cancels.
- `:` commands (`tab` completes):
  - `:add #N|branch`
  - `:auto manual|needs-you|needs-you+yours|all` (saved to `.git/prt/prt.toml`)
  - `:tier deep|none|rules`
  - `:cap 1.00|default`
  - `:stop [all]`
  - `:resume [all]`
  - `:settings`
  - `:q`

## Decisions worth your review

1. **Fixture repo is generated, not committed.** `internal/fixturegen` builds it deterministically from `testdata/fixtures/{base,prs}` because a nested `.git` can't be committed. Tests generate a fresh copy per test.
2. **Jev goes through Vercel AI Gateway** (`/v1/evaluate`, model `typesafe-ai/jev`). The spec's `models.jev_endpoint` became `models.jev_base_url`.
3. **The Anthropic key is `PRT_ANTHROPIC_API_KEY` only**, with no fallback to `ANTHROPIC_API_KEY`, because of the billing switch.
4. **`auto_review` defaults to `needs-you`.** This overrides the spec's "analyse everything in urgency order", and branches are reviewed only under `all`. It is recorded as your override in DECISIONS.
5. **M6 is deferred.** The scope was M0–M5 plus M5.1 and M5.2.
6. **Embeddings use Ollama `embeddinggemma-2`, with ONNX stubbed.** Because this model's cosines are compressed, `embed_min_cosine` is 0.88 for the fixtures (spec default 0.72).
7. **Label tuning narrows the spec (your M5.2 request).** Headlines need P ≥ 0.60 and give one row per unit; "unsure" reads only the attention and risk labels. The ground truth was written blind before tuning, but it covers six fixture items only, so the tuning may still fit them too closely.
8. **Per-item cost cap stays at $0.40 and is charged at expected output length.** Every routed group runs on the fixtures; a single deep call can cost more than its reservation.
9. **Change blocks merge code detection with Jev.** A Jev-only entry needs P ≥ 0.60; test files never count; client calls (`fetch`, axios) are not endpoints.

## Three things I would do next

1. **Validate end-to-end on a real repo and measure "Done means".** Run it on a real repo with real PRs and both keys set, and time a 400-line mixed PR: labels under 2 s and reviews under 10 s. Add a benchmark or timing log for this.
   - Write ground truth for a few real PRs and run `make quality`-style scoring on them, to check the tuning beyond the fixtures.
   - Make the first real GitHub review submission on a test PR, since only `gh.Fake` was exercised.
   - This is the biggest unknown: everything so far ran on a six-item fixture.
2. **Bring lines to read under 1/3 and fix hot-spot ranking.** #101, #106 and the branch are at 0.50–0.56 lines to read, mostly feature code Jev calls `read`; the attention mapping is the lever. Hot spots hit 3 of 13 slots because the spec's formula multiplies by (1 − covered), which ranks covered but risky units low; that needs a spec-level call.
3. **Implement the ONNX embedder, then M6 overlap.**
   - Pick a licence-compatible ~130M code model (a spec open question) to remove the Ollama dependency and get sub-second batches.
   - Then build the overlap view from `docs/design/v2/Overlap.dc.html` on the existing content-based unit ids: multi-select is already marked with `space`, plus cross-item embeddings, `semantic_conflict` pairs and merge order.
