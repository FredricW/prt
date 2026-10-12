# prt — PR triage TUI spec

Oct 13, 2026 · @Fredric

## Goals and scope

**The problem.** PRs are getting bigger because much of the code in them is AI-generated, but the reviewer is still one person with one brain. A 400-line PR today is typically 60% refactor, renames and boilerplate, and 40 lines that actually decide whether it is safe. Reading all 400 to find those 40 is the cost prt removes: it filters the noise out and leaves the signal in, so the human reads only what needs a human.

prt is a terminal app that ranks the PRs and branches that need you, and makes each one reviewable in minutes rather than tens of minutes. It does this by splitting every diff into change units, labelling each unit with Jev (fast, structured judgments), grouping related units with a local embedding model, and sending each group to the cheapest LLM tier that can review it well. The UI shows the result as a ranked picker, a label-driven overview, a code page with two reading modes, and a pipeline page that exposes every decision the system made.

**Signal vs noise, concretely**

| Noise (hidden or collapsed by default) | Signal (always shown) |
| --- | --- |
| Formatting, whitespace, generated files | Behaviour changes |
| Moved code, verified identical | Contract and API changes, especially breaking ones |
| Mechanical repeats (a rename ×23 shows once) | Risky chunks: auth, async, data writes, money |
| Dependency bumps with green CI | Risky chunks that no test exercises |
| Code outside your modules, unless it calls into them | Undocumented changes the PR description does not mention |
| Chunks the fast reviewer passed with high confidence | Anything a model labelled with low confidence |

**In scope for v1**

- Picker: ranked PRs and local branches, plus search-and-add for any branch or PR in the repo.
- Overview page: summary built by aggregating per-unit labels; an LLM only writes the one-line TL;DR.
- Code page: unified and side-by-side diff; two modes, *by file* (labels and filters) and *by change* (semantic groups); review comments that post to GitHub.
- Pipeline page: every stage, the routing table, rules, cost and calibration, inspectable live.
- Architecture tab: module graph coloured by attention, as in the v2 design.

**Non-goals for v1**

- No GitLab or Bitbucket; GitHub only via `gh` and the REST/GraphQL API.
- No merging, rebasing or pushing from the TUI beyond `checkout`; comments and approvals are the only writes.
- No multi-repo view; one repo per process.
- Overlap view across PRs stays a v2 feature; the data model leaves room for it.

**Principles**

- Jev supplies judgments, code does the arithmetic, the LLM writes prose. Never ask a model for a number you can compute.
- Every number in the UI is traceable to a label, a rule or a git fact; `L` on any item shows the raw labels behind it.
- Nothing runs twice: labels, embeddings and reviews are cached by content hash.
- The user can overrule any label (`!`), and those overrides feed calibration.

## Architecture

Two processes: a long-running daemon `prtd` that fetches, analyses and caches, and a thin TUI `prt` that renders daemon state and sends commands. The split means the TUI starts instantly against the cache, and a second TUI (or a CI job) can reuse the same analysis.

&#91;embedded content: system architecture · sources, daemon stages, screens, external services\]

The daemon fetches diffs and PR metadata, runs the eight stages top to bottom per item, and writes every intermediate result to SQLite; the TUI subscribes to a state stream and never calls a model itself.

**Stack**

| Part | Choice | Why |
| --- | --- | --- |
| Language | Go 1.23, one binary: `prt` (TUI) and `prt daemon` | Single static binary, easy install, goroutines fit the parallel pipeline |
| TUI | Bubble Tea (Elm-style model/update/view), Bubbles for lists, viewports and text inputs, Lip Gloss for layout and style | The screens are a tree of models; Bubbles covers the picker list, diff viewport and comment composer |
| Markdown and forms | Glamour renders comment bodies and PR descriptions; Huh builds the submit-review form and filter pickers | Same look as the rest of the Charm stack |
| Parsing | go-tree-sitter with TS/TSX/JS/Go/Python grammars (cgo) | Function-boundary change units, call edges, syntax colour |
| Embeddings | onnxruntime\_go with a small code embedding model (\~130M params); fallback to a local Ollama `/api/embed` if ONNX is unavailable | Sub-second for a PR, nothing leaves the machine |
| Judgments | Thin HTTP client for Jev's `systemone` endpoint (no Go SDK yet); typed request/response structs generated from the question catalogue | 70–500 ms per call, many questions per request |
| Review LLMs | Provider adapters behind one `Reviewer` interface; `fast` and `deep` tiers set in config | Tiers, not vendors, are what routing decides |
| GitHub | token from `gh auth token`; go-github for REST, githubv4 for search and review threads | Reuses the user's existing login |
| Store | SQLite via modernc.org/sqlite (pure Go), one file per repo under `.git/prt/` | Content-hash cache; cgo stays limited to tree-sitter and ONNX |
| Transport | daemon ↔ TUI over a Unix socket, newline-delimited JSON; the TUI can also run the pipeline in-process with `--no-daemon` | Inspectable with `nc`; single-process mode for CI and tests |
| Logging | charmbracelet/log to `.git/prt/prt.log`, and the same events feed the Pipeline page | One event stream for humans and the UI |

**Data flow per item**

1. `fetch`: diff against the merge base, PR metadata, review comments, CI status. Cached by head SHA.
2. Stages 1–8 run as a pipeline; each stage writes its output keyed by the hash of its input so a rebase only re-runs what changed.
3. `aggregate` writes the overview record and the urgency score; the picker re-sorts.
4. The TUI receives a `patch` event per stage so screens fill in as results land, including a live Pipeline page.

**Bubble Tea structure.** A root model holds the picker and the active item; the main pane is one of four child models (overview, code, architecture, pipeline) that receive `tea.Msg` patches from the daemon client. Long operations never run in `Update`; they are `tea.Cmd`s that return messages. The diff viewport is a custom Bubbles viewport that renders lines lazily so a 5,000-line PR stays responsive.

The stage contracts in the Pipeline section are language-neutral; if a stage later moves out of Go (for example embeddings into a sidecar), the socket protocol is the boundary.

## Data model

Everything hangs off an **Item** (a PR or a branch) and its **ChangeUnits**. Labels, groups, reviews and comments all reference units by a content hash, so a rebase keeps everything that did not change.

| Entity | Key fields | Notes |
| --- | --- | --- |
| `Item` | `id`, `kind` (pr \| branch), `number?`, `ref`, `headSha`, `baseSha`, `title`, `body`, `author`, `reviewers[]`, `ci`, `labels[]`, `pinned`, `source` (ranked \| added) | `added` items come from search-and-add and stay until removed |
| `File` | `itemId`, `path`, `role` (core \| glue \| ui \| test \| config \| generated), `owned` (bool), `added/deleted/renamed` | `role` from Jev, `owned` from `prt.toml` globs + CODEOWNERS |
| `ChangeUnit` | `id` = sha256(path + normalised hunk), `fileId`, `range` (old/new line spans), `symbol?` (enclosing function), `diff`, `kind` (added \| deleted \| modified \| moved) | tree-sitter splits hunks at function boundaries; moved units carry `movedFrom` |
| `Label` | `unitId`, `question`, `value`, `p` (probability), `confidence`, `model`, `overridden?` | one row per Jev answer; `overridden` holds the user's correction |
| `Link` | `fromUnit`, `toUnit`, `kind` (calls \| tests \| imports \| moved \| same-symbol \| embed-neighbour), `weight`, `source` (structural \| embed \| jev) | structural links are exact; embed links carry cosine; Jev confirms or rejects |
| `Group` | `id`, `itemId`, `unitIds[]`, `name`, `descBullet?`, `mechanical` (bool, with `exampleUnit` and `count`), `order` | the unit of review and of the *by change* view |
| `GroupAgg` / `ItemAgg` | composition %, effective lines, est. minutes, max risk, risk tags, test gap, breaking\[\], hot spots\[\], confidence stats | computed, never stored as truth; recomputed from labels |
| `Review` | `groupId`, `tier` (none \| fast \| deep), `prompt`, `context[]`, `findings[]`, `verdict`, `confidence`, `ms`, `cost` | findings carry `unitId` + line so the UI can anchor them |
| `Comment` | `id`, `itemId`, `unitId?`, `path`, `line`, `side` (old \| new), `body`, `author`, `source` (user \| ai-draft \| github), `state` (draft \| pending \| posted \| resolved), `ghId?` | drafts live locally until the user submits the review |
| `Override` | `unitId`, `question`, `value`, `at` | feeds calibration and re-aggregation |
| `RunLog` | `itemId`, `stage`, `startedAt`, `ms`, `calls`, `cost`, `cacheHit` | what the Pipeline page shows |

**Identity rules**

- A unit's id is content-based, so the same hunk in two PRs has the same id; that is what makes cross-item overlap possible later.
- Comments anchor to `path + line + side` as GitHub requires, and additionally to `unitId` so they survive a rebase that shifts line numbers.
- Overrides are keyed by unit and question, not by item, so a corrected label follows the code if it moves to another PR.

## Picker (sidebar)

The picker is a ranked list of items in three sections, plus a way to pull in anything the ranking did not surface. It is always visible; the main pane follows the cursor.

**Sources, auto-populated on start and every 60 s**

- PRs where you are a requested reviewer, author, or have commented (`gh api graphql` search: `involves:@me is:open`).
- PRs that touch your owned paths, even if nobody asked you (search by `path:` globs from config).
- Local branches with commits not on the default branch (`git for-each-ref` + `git rev-list --count`), matched to PRs by head SHA.
- Items the user added by hand (persisted in SQLite with `source = added`).

**Sections and sort**

| Section | Membership | Secondary line |
| --- | --- | --- |
| Needs you | review requested, re-review after a push, a comment addressed to you, CI red on something you approved | breaking count, needs-eyes count, est. minutes, "why" |
| Yours | PRs you authored and branches you own | open comments count, CI state, staleness |
| Branches | local branches without a PR | state (ready for PR, WIP, spike, abandoned?, superseded), unpushed count, age |
| Added | search-and-add results | same as the kind it is |

Within a section, sort by urgency score (see Ranking); `s` cycles urgency → age → size → relevance. The one-line "why" is the top three weighted terms of the score, in words.

**Urgency bar**: four cells, `▮` filled per quartile of the score. Colour-free so it reads in any terminal theme.

**Search and add (`/`)**

`/` opens a prompt at the bottom of the sidebar. As the user types, results stream from three sources in parallel and merge into one list:

1. Local refs: `git for-each-ref --format` filtered by substring (instant).
2. Open PRs: GitHub search `repo:owner/name is:pr is:open <query>` (title, number, branch, author). A bare number jumps straight to that PR.
3. Closed and merged PRs if the query starts with `!` or no open result matched, so old context can be pulled in.

Each result shows kind, number or ref, title, author and age. `enter` adds it to the Added section and starts analysis immediately; `tab` on a result previews its overview without adding. Added items persist until `x` removes them. A PR added by hand also becomes a candidate for the Overlap view later.

**Selection**

- `j`/`k` move the cursor; the main pane shows that item.
- `space` toggles multi-select for Overlap (v2); the header shows the count.
- `enter` focuses the main pane; `esc` returns.

**States an item can be in**: `fetching`, `analysing <stage>` (shown as a spinner with the stage name), `ready`, `stale` (new push since analysis; `r` refreshes), `error` (reason on the secondary line). Analysis runs in urgency order so the top of the list is ready first.

## Overview page

The overview is assembled from aggregated labels and git facts; the only model-written text on it is the one-line TL;DR, and that is generated *from the aggregates*, not from the raw diff. Every block links to the units behind it (`enter` jumps to the first).

**Layout, top to bottom**

1. **Title line**: number, title, author, age, base ← head. Then the TL;DR: one sentence, max 160 characters, produced by the fast LLM from the aggregate record (composition, headlines, hot spots). Marked `ai·from labels`.
2. **Composition bar**: chunk kinds weighted by changed lines (refactor, feature, bugfix, test, config, noise). Under it: `+added −removed → ~N lines that matter · ~M min · K of U chunks need eyes`.
3. **Headlines**: every unit labelled breaking, contract change, migration, env or flag change, listed with its confidence. Empty state says "no contract changes" so absence is explicit.
4. **Check before approving**: the top three hot spots by `risk × relevance × (1 − coverage) × confidence`, each with file:line and a five-word reason taken from the review finding or the label that fired.
5. **Risk**: `max` chunk risk as the headline (HIGH ≥ 4, MED 3, LOW ≤ 2), risk-tag chips with counts, and the test gap: "N of M risky chunks untested".
6. **Description fidelity**: bullets implemented / not found / undocumented changes, plus a coherence verdict (single-purpose, mixed, sweeping, WIP).
7. **Minimap**: one row per file, one cell per unit, shaded by attention (eyes, skim, skip) with an outline for low confidence. Files you own come first. Clicking or `enter` on a cell opens that unit in Code.
8. **Your slice**: unit counts per owned module; a line for the rest ("server/\* is 14 chunks — skip unless curious").
9. **Since you looked**: units changed since your last review of this item, and which of your comments were addressed (a Jev Noul per comment against the new diff).
10. **Confidence footer**: count of low-confidence labels; those units are kept expanded in Code.

**Rendering rules**

- Numbers are computed by `aggregate` and cached with the item; the page never does maths.
- Every probability shows as two decimals in dim text next to its claim (`.86`). Below `0.60` the claim is prefixed `?` and the row is marked unsure.
- Blocks that would be empty are collapsed to a single dim line rather than omitted, so the reader knows the check ran.
- The TL;DR is regenerated only when the aggregate record changes; `r` forces it.

**Branches** get the same page with PR-specific blocks (headlines from the description, since-you-looked) replaced by a branch state block: ready for PR / WIP / spike / abandoned? / superseded by #N, each a Jev Choice with its probability, plus unpushed commits and the suggested PR title (fast LLM, from the group names).

## Code page

One page, two reading modes over the same units, one diff renderer with two layouts, and a comment layer that works in all of them. The two modes are different *orderings and groupings* of the same unit list, so labels, filters, gutter marks and comments are shared; switching with `g` keeps the cursor on the same unit.

**Mode A, by file (v2)**

- Left list: files sorted by attention (sum of needs-eyes units), owned files first, then a `not yours` divider. Each file shows its minimap cells.
- Right pane: units of the selected file in line order. Each unit has a header strip of label chips: kind, behaviour ✓/✗, breaking, description bullet, tests ✓/✗, risk tags, attention verdict, and `?` for low confidence.
- Gutter marks per line: `◆` suspicious (shade by score), `~` value tweak, `⋯` leftover, `·` nothing.
- Collapsed by default: formatting, moved (with a link to origin), generated, and mechanical repeats; `z` expands.

**Mode B, by change (v3)**

- Left list: groups in review order (interface units first, then callers, then tests), each with unit and file counts, description bullet, tier and review status. Ungrouped units and a split suggestion sit at the bottom.
- Right pane: group header (name, bullet, chips, routing), a *why these belong together* block listing the links that bound them, then the group's units in order, then the review findings for the group.
- Mechanical groups render as one example plus `×N verified identical`; units that differ from the pattern are promoted out of the group.

**Filters (both modes)**

Toggle chips in a bar under the tabs; `f` opens them, number keys toggle. Default on: needs eyes, behaviour changes, mine. Available: risk ≥ N, untested, undocumented, unsure, has comments, has findings, changed since last look. Filters hide units, never groups or files; a group with all units hidden shows as a dim one-liner with its count.

**Diff rendering**

- `u` toggles unified and side-by-side. Side-by-side splits the pane into old and new columns aligned by hunk; lines are wrapped, not truncated, and the gutter marks sit on the new side. Below \~140 columns side-by-side degrades to unified with a status note.
- Intra-line highlighting from a character-level diff of paired lines; no third model call.
- Syntax colour from tree-sitter captures; falls back to plain for unknown languages.
- `e` opens the file at the line in `$EDITOR`; `o` opens the unit on GitHub.

**Review comments**

- `C` on a line (or a visual selection `V` then `C`) opens an inline composer under the line. Multi-line bodies, markdown. `esc` discards, `ctrl-s` saves a draft.
- Drafts are local and listed in a `Comments` strip at the bottom of the page: count, pending/posted, and `enter` to jump. Drafts survive restarts.
- `S` submits the review: a picker for approve / request changes / comment, an optional summary, then one GitHub review containing all drafts as line comments (REST `POST /repos/{o}/{r}/pulls/{n}/reviews` with `comments[]` using `path`, `line`, `side`).
- AI findings appear as `ai-draft` comments with a dim border: `a` accepts (becomes a draft in your name, editable), `d` dismisses, `!` dismisses and files a label override. Nothing posts without an explicit accept.
- Existing GitHub comments and threads load with the item and anchor to units via `path + line`; unresolved threads show in the gutter as `❱`, `t` opens the thread in a side panel for reply or resolve.
- On a branch with no PR, comments are stored locally and attached when `P` creates the PR.

**Keyboard, Code page only**

| Key | Action |
| --- | --- |
| `g` | toggle by file / by change |
| `u` | unified / side-by-side |
| `n` / `p` | next / previous unit (respects filters) |
| `J` / `K` | next / previous file or group |
| `f`, `1–9` | open filters, toggle one |
| `z` | expand collapsed noise in this file or group |
| `C`, `V` | comment on line, start a line selection |
| `a` / `d` / `!` | accept / dismiss AI finding, dismiss and override label |
| `t` | open thread panel |
| `S` | submit review |
| `L` | show raw labels for the unit |
| `⇧M`, `X` | merge groups, split a group (by change only) |
| `R` | re-review this group on the deep tier |

## Pipeline page

`P` from any item opens the pipeline page for that item. It exists so the system is inspectable: every routing decision, model call, cache hit and cost is visible, and most of them can be overridden from here.

**Layout**

1. **Stage strip**: the eight stages as boxes with state (done, running, queued, failed, skipped-cache), unit counts in and out, and wall time. `enter` on a stage opens its log below.
2. **Routing table**: one row per group: name, tier, the rule that fired (in words, from the rules file), context attached (callers, types, tests, config), status, time, findings count. Escalations show as `fast → deep` with the fast confidence that triggered them.
3. **Totals**: tier mix bar, elapsed vs. an estimate of all-deep sequential, cost vs. the per-PR cap, Jev call count and cost.
4. **Rules panel**: the active `prt.routing.toml` rendered as `if … → tier` lines; `e` opens it in `$EDITOR`, and saving re-routes unreviewed groups.
5. **Grouping panel**: units → links (structural, embed candidates, Jev kept by relation) → groups, and the mechanical collapse count.
6. **Calibration panel**: fast-tier miss rate over 30 days (from sampled double reviews), label override counts by question, and the last sample's agreement.

**Actions**

| Key | Action |
| --- | --- |
| `D` | force the selected group to the deep tier |
| `s` | skip review for the selected group (marks it none, logged) |
| `r` | re-run the selected stage and everything after it, ignoring cache |
| `e` | edit routing rules |
| `c` | open the raw call log for the selected row (prompt, context, response, tokens) |
| `$` | raise the cost cap for this item only |

**Live behaviour**: the page subscribes to `RunLog` events and updates in place; a running review shows elapsed seconds. The stage strip is also shown as a one-line status in the picker while an item is analysing, so you do not need to open this page to know what is happening.

## Pipeline internals

Each stage is a pure function from its inputs to a stored output, keyed by a hash of those inputs. Stages run in order per item; independent items run in parallel up to a concurrency limit. Target for a 400-line PR on a laptop: labels and groups within 2 s, reviews within 10 s.

**1. parse** — input: unified diff, head and base trees. Split each hunk at function boundaries using tree-sitter; attach the enclosing symbol and signature. Normalise whitespace and identifiers (`$1`, `$2`…) to get a *shape hash* used later for mechanical detection. Mark pure formatting units by comparing token streams. Output: `ChangeUnit[]`.

**2. link** — input: units, both trees. Exact links only: call edges between changed symbols (tree-sitter queries plus a symbol index of the repo cached per base SHA), imports, test-to-code by filename and symbol name, moved code (identical normalised bodies deleted in one place and added in another), same-symbol edits across files. Output: `Link[]` with `source = structural`.

**3. embed** — input: units. Text per unit = path + signature + diff lines (added and removed). Local ONNX model, batched. Store the vector. kNN over units of the same item (and, for overlap, across selected items), cosine ≥ 0.72 or top 3, whichever is larger. Output: candidate `Link[]` with `source = embed, weight = cosine`. Units with identical shape hashes are linked directly as `mechanical` candidates without embedding.

**4. Jev pairs and labels** — two Jev passes, parallelised:

- Pairs: for each embed candidate, a Choice `relation ∈ {same intent, caller update, test for, same mechanical pattern, unrelated}`. Keep links with `p ≥ 0.6` and not unrelated. Structural links are never sent; they are already true.
- Labels: per unit, one request carrying the question catalogue (next section) with the unit's diff, signature, path, the PR description bullets, the user's role and owned globs, and the list of tests touched. Per-line questions run on the unit's added lines in one request each, batched 20 lines per call.

Output: `Label[]`, confirmed `Link[]` with `source = jev`.

**5. cluster** — connected components over structural + confirmed links, then split any component larger than `max_group_units` (default 8) by cutting its weakest embed links. Mechanical candidates form their own group when `count ≥ 3`; code verifies each member matches the example's shape hash, and non-matching members are ejected. Name each group with a Jev Choice over the description bullets (plus `none`); groups with `none` get a three-word title from the fast LLM. Order: units labelled `interface` first, then callers, then tests. Output: `Group[]`.

**6. route** — evaluate `prt.routing.toml` rules top to bottom against the group's aggregate labels; first match wins. Also decide context: callers when any unit is breaking, types when a signature changed, tests when the group has untested risky units, config when an env or flag label fired. Respect `deep_slots` and the cost cap; groups over the cap queue in hot-spot order. Output: `Review` rows in state `queued`.

**7. review** — one LLM call per group with the group's units, the attached context and the labels as structured hints. The prompt asks for findings only (file, line, severity, one-sentence claim, suggested fix), a verdict, and a confidence. Fast tier results with `confidence < escalate_below` re-run on the deep tier. Specialist prompts (security, performance, tests) are appended when the matching tag fired. Calibration: 5% of fast-routed groups also run deep; disagreement is logged.

**8. aggregate** — compute `GroupAgg` and `ItemAgg` from labels, reviews and git facts (formulas in Ranking); generate the TL;DR from the aggregate record with the fast tier; compute the urgency score; emit the overview record. This stage is cheap and re-runs on any override.

**Caching and invalidation**

- Keyed by unit id for labels, embeddings and per-line marks; by sorted unit-id list for groups and reviews; by head SHA for fetches.
- A new push re-parses, then only units with new ids go through stages 3–7; groups whose membership did not change keep their reviews.
- An override invalidates aggregate only, and marks the affected review as `stale-labels` without re-running it.

**Failure handling**

- Jev unreachable: units get `label = unknown, confidence = 0`; routing treats unknown as deep; the picker shows `labels unavailable`.
- Embedding model missing: skip stage 3; groups come from structural links only, with a status note.
- LLM error or timeout (30 s fast, 120 s deep): the review row is marked failed, retried once, then left for `R`.
- Nothing blocks the UI; every stage emits partial results as they land.

## Jev question catalogue

Questions are defined once in `prt.questions.toml` and sent as a batch per unit (or per line batch, per group, per pair). Types: **N** Noul (yes probability), **C** Choice (one option + confidence), **S** Score (ordered rubric + confidence). Each question names the inputs it needs so the daemon attaches only those.

| Scope | Id | Type | Question / options | Inputs beyond the unit | Feeds |
| --- | --- | --- | --- | --- | --- |
| line | `fmt` | N | formatting or whitespace only | — | collapse |
| line | `value_tweak` | N | changes a literal (timeout, limit, URL, threshold) | — | gutter `~` |
| line | `leftover` | N | debug log, commented-out code, TODO/FIXME | — | gutter `⋯` |
| line | `swallow` | N | ignores or swallows an error | — | risk |
| line | `pii_or_secret` | N | may log or expose personal data or a secret | — | risk, headline |
| line | `suspicious` | S 1–5 | how likely to be a bug | — | gutter `◆` |
| unit | `kind` | C | feature, bugfix, refactor, perf, test, config, docs, dependency, generated, formatting | — | composition, collapse |
| unit | `behaviour` | N | changes runtime behaviour | — | filters |
| unit | `effect` | C | none, ui, api response, stored data, performance | — | overview |
| unit | `desc_bullet` | C | which description bullet it implements, or none | PR body bullets | fidelity, group names |
| unit | `breaking` | N | breaks existing callers | callers list | headlines, context |
| unit | `contract` | C | none, signature, api shape, schema/migration, env/flag, event format | — | headlines |
| unit | `reversible` | N | can be reverted without a migration or data fix | — | risk |
| unit | `risk_tags` | N ×8 | auth, async/race, error path, data write, money, pii, io-in-loop, date/tz | — | chips, routing |
| unit | `fail_mode` | C | crash, wrong data, security, slowdown, cosmetic | — | hot spots |
| unit | `risk` | S 1–5 | overall risk if wrong | — | everything |
| unit | `covered` | N | exercised by tests changed in this PR | changed test units | test gap |
| unit | `test_weakened` | N | a test was skipped, deleted or made weaker | — | headline |
| unit | `follows_pattern` | N | follows the module's existing pattern | 2 neighbouring functions | quality |
| unit | `duplicates_helper` | N | re-implements an existing helper | 3 candidate helpers by embed | quality |
| unit | `interface` | N | defines something other units in the PR call | link list | group order |
| unit | `relevance` | S 1–5 | relevance to the user's role and owned modules | role, globs | ranking, your slice |
| unit | `depth` | C | skip, skim, read, run locally | — | attention, minutes |
| unit | `needs_context` | N ×4 | needs callers / types / tests / config to review | — | routing context |
| unit | `reasoning` | S 1–5 | how much reasoning a reviewer needs | — | routing tie-break |
| file | `role` | C | core, glue, ui, test, config, generated | — | file list |
| file | `separate_pr` | N | should be its own PR | group list | split suggestion |
| group | `name_bullet` | C | description bullets + none | PR body | group name |
| pair | `relation` | C | same intent, caller update, test for, same pattern, unrelated | both units | links |
| pair | `semantic_conflict` | C | none, same, contradictory, order-dependent | both units | overlap (v2) |
| item | `shape` | C | single-purpose, mixed, sweeping refactor, WIP | aggregates + body | coherence |
| item | `desc_sufficient` | N | description alone would let a reviewer judge it | body + headlines | fidelity |
| item | `flagged` | N | behind a feature flag or safe to ship dark | flag labels | risk |
| item | `rollback` | C | trivial, revert, needs migration, one-way | contract labels | risk |
| item | `needs_other_reviewer` | C | none, security, backend, data | tag counts | overview |
| item | `comment_addressed` | N (per comment) | this review comment is addressed by the latest push | comment + new diff | since you looked |
| branch | `state` | C | WIP, ready for PR, spike, abandoned, superseded | log, age, open PR titles | picker |

**Rules for adding a question**

- One factor per question; combine in code. Jev is weak on multi-factor prompts.
- Fixed option lists only; no free text.
- Every question declares what UI element it feeds. A question nothing consumes is removed.
- Overrides via `!` are stored against the question id, so renaming a question needs a migration.

## Ranking and aggregation

All of this is plain arithmetic over labels and git facts, implemented in `aggregate` and covered by unit tests with fixture PRs. Weights live in `prt.toml` and are shown on the Pipeline page.

**Per unit**

- `noise` = kind ∈ {formatting, generated} or moved-verified or fmt-only lines.
- `effective_lines` = changed lines of non-noise units; mechanical groups count their example once.
- `attention` = `depth` mapped to eyes (read, run) / skim / skip; a unit with any label `confidence < 0.60` is never skip.
- `minutes` = eyes 0.5/line (min 1), skim 0.1/line, skip 0.

**Per item**

- `composition[k]` = effective lines of kind k ÷ total effective lines.
- `risk_max` = max unit `risk`; `risk_any` = 1 − ∏(1 − p\_i) over units with risk ≥ 4.
- `test_gap` = count of units with risk ≥ 3 and `covered < 0.5`.
- `hot_spot(u)` = risk × relevance × (1 − covered) × min(confidence), top 3 shown.
- `fidelity` = implemented bullets ÷ bullets; `undocumented` = units with `desc_bullet = none` and `behaviour = yes`.
- `unsure` = count of labels with confidence < 0.60.

**Urgency score** (the picker sort), computed as a weighted sum so the top three terms can be shown as the "why":

```latex
U = w_r\,[\text{review requested}] + w_b\,\text{breaking} + w_k\,\text{risk\_max} + w_t\,\text{test\_gap} + w_y\,\text{relevance} + w_p\,[\text{release path}] + w_c\,[\text{CI red after approval}] + w_a\,\log(1 + \text{age}_\text{days}) - w_s\,\text{minutes}
```

Default weights: `w_r 3.0, w_b 2.0, w_k 0.8, w_t 0.6, w_y 0.5, w_p 2.0, w_c 2.5, w_a 0.4, w_s 0.02`. Each term is also stored, so the picker's "why" line is the three largest contributions in words ("asked you · release · risk×untested").

**Branches** replace the PR-only terms with `w_u × unpushed commits` and a staleness bonus when `state = ready for PR`.

**Calibration metrics** (Pipeline page)

- Fast-tier miss rate = sampled double reviews where deep found a severity ≥ medium finding that fast did not, over sampled count, trailing 30 days.
- Override rate per question = overrides ÷ labels, trailing 30 days. A question over 15% gets flagged on the page as a candidate for rewording.

## Keybindings and navigation

Vim-style, mode-free where possible; a `?` overlay lists the bindings for the focused pane. Bindings are rebindable in `prt.toml`.

| Key | Scope | Action |
| --- | --- | --- |
| `j` / `k`, `g` / `G` | picker | move, top, bottom |
| `/` | picker | search and add any branch or PR |
| `x` | picker | remove an added item |
| `s` | picker | cycle sort |
| `space`, `esc` | picker | multi-select, clear |
| `r` | item | refresh (re-fetch and re-run changed stages) |
| `1` `2` `3` | item | Overview, Code, Architecture tabs |
| `P` | item | Pipeline page |
| `tab`, `shift-tab` | global | cycle focus picker → main pane → bottom strip |
| `enter` | overview | jump to the unit behind the focused block |
| `L` | overview, code | show raw labels for the focused item or unit |
| `!` | code | mark a label wrong (opens a picker of the unit's labels) |
| `w` | overview | show urgency weights and this item's term contributions |
| `c` | item | `git checkout` the head branch (prompts if the tree is dirty) |
| `a`, `A` | item | approve (opens submit form), request changes |
| `o` | item | open on GitHub |
| `:` | global | command line: `:add #482`, `:add feat/x`, `:tier deep`, `:cap 1.00`, `:q` |
| `?` | global | help overlay |
| `q`, `ctrl-c` | global | quit (drafts are saved) |

Code-page bindings are listed in that section. Mouse is supported for scrolling and clicking list rows, nothing else.

## Configuration

Three TOML files, all optional with sane defaults, looked up in `.git/prt/`, then `$XDG_CONFIG_HOME/prt/`, then built-in.

**`prt.toml`** — identity and weights

```toml
[user]
role = "frontend"            # free text, sent to Jev with relevance questions
owns = ["web/**", "packages/ui/**"]

[github]
repo = "acme/portal"         # default: origin remote
release_branches = ["release/*"]

[rank]
w_requested = 3.0
w_breaking = 2.0
w_risk = 0.8
w_test_gap = 0.6
w_relevance = 0.5
w_release = 2.0
w_ci_red = 2.5
w_age = 0.4
w_minutes = 0.02

[pipeline]
max_group_units = 8
embed_min_cosine = 0.72
keep_pair_p = 0.60
unsure_below = 0.60
concurrency = 4

[models]
embed = "onnx:models/code-embed-small.onnx"   # or "ollama:nomic-embed-code"
jev_endpoint = "https://api.<jev-domain>/v1/systemone"
fast = "anthropic:claude-haiku-5-5"
deep = "anthropic:claude-opus-5-5"
```

**`prt.routing.toml`** — first matching rule wins, evaluated per group

```toml
deep_slots = 3
cost_cap_usd = 0.40
escalate_below = 0.60
calibration_sample = 0.05

[[rule]]
when = "mechanical || kind in [formatting, moved, generated, dependency]"
tier = "none"

[[rule]]
when = "risk >= 4 || tag.auth || tag.money || tag.async"
tier = "deep"
prompts = ["security"]        # appended when tag.auth or tag.pii
context = ["callers", "tests"]

[[rule]]
when = "breaking"
tier = "fast"
context = ["callers"]

[[rule]]
when = "kind == test"
tier = "fast"
prompts = ["tests"]
context = ["code_under_test"]

[[rule]]
when = "true"
tier = "fast"
```

The `when` expression language is small: label ids, `tag.<name>`, `kind`, numbers, `&& || !`, `in [...]`. It is evaluated by a 200-line parser in the daemon and shown verbatim on the Pipeline page.

**`prt.questions.toml`** — the catalogue from the Jev section, one `[[question]]` per row with `scope`, `id`, `type`, `prompt`, `options`, `inputs`, `feeds`. Users may add questions; anything with `feeds = []` is rejected at load.

**Secrets**: `JEV_API_KEY`, provider keys, and the GitHub token come from the environment or `gh`; never from the TOML files.

## Milestones

Each milestone ships something usable on its own; the model-driven parts come after the plain diff viewer works, so the pipeline always has a fallback UI.

1. **M0 — skeleton (1 week).** Go module, Bubble Tea root model, picker from `gh` and local git, plain unified diff viewer, SQLite cache of fetches. No models. Usable as a fast PR browser.
2. **M1 — units and labels (1–2 weeks).** tree-sitter parse into change units, Jev client, question catalogue, label chips and filters in by-file mode, gutter marks, noise collapsing. Overview page with composition, headlines, risk, test gap and minimap (no TL;DR yet).
3. **M2 — groups (1–2 weeks).** Structural links, local embeddings, Jev pair confirmation, clustering, mechanical collapse, by-change mode, split suggestions.
4. **M3 — review pipeline (1–2 weeks).** Routing rules, fast/deep adapters, escalation, cost cap, Pipeline page, findings shown in Code, TL;DR from aggregates, urgency ranking with weights.
5. **M4 — comments (1 week).** Inline composer, drafts, AI-draft accept/dismiss, GitHub review submission, thread panel, since-you-looked.
6. **M5 — polish (1 week).** Side-by-side diff, search-and-add with closed PRs, branch states, Architecture tab, calibration sampling, `!` overrides feeding re-aggregation.
7. **M6 — overlap (later).** Multi-select, cross-item embeddings, semantic conflict pairs, merge-order suggestion.

**Done means**: a 400-line PR of mixed refactor and feature opens with labels in under 2 s and reviews in under 10 s on a laptop; the reviewer reads fewer than a third of the lines to reach the same verdict as a full read on the fixture set; every number on screen can be traced with `L` or the Pipeline page.

## Open questions

- [ ] Jev's real endpoint, rate limits and request size cap: confirm against TypeSafe's docs before fixing the batch sizes in stage 4.
- [ ] Which embedding model: a \~130M code model via ONNX gives sub-second batches, but which one, and does its licence allow bundling in a binary release?
- [ ] go-tree-sitter grammar coverage for the repos you review (TS/TSX, Python, Go confirmed; Swift, Kotlin, SQL?).
- [ ] Side-by-side at narrow widths: degrade to unified below \~140 columns, or allow horizontal scroll?
- [ ] Should AI findings ever post to GitHub without an explicit accept (for example a "breaking change" headline as a review summary)? Default is no.
- [ ] Branch states for branches with no upstream: is `abandoned?` after 14 days of no commits the right default?
- [ ] Do weights need per-repo overrides, or is one global `prt.toml` enough to start?
- [ ] Calibration storage: keep per-user locally, or allow a team to share overrides and miss rates via a file in the repo?
