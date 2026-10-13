# M5.2: why opening an analysed PR shows "loading…" (measurements)

2026-10-13. A diagnosis only: nothing is fixed here. This answers the user's report:

> Opening a PR that has already been analysed still shows a loading bar and
> sometimes it takes quite some time. What is going on here? I expected the
> review to be cached. If there are updates and a new review is running, show
> the old with a small note somewhere that shows that we are updating the review.

## Short answer

- **The review is cached.** Opening an analysed item runs no pipeline stage and makes no model or GitHub call. The TUI makes one `Detail` call and one `MarkViewed` write. The idle TUI run confirms it: 25 opens made 25 `Detail` calls, 0 list reloads and 0 events.
- **"loading…" shows on every open, because the TUI keeps nothing.** `selectionChanged` throws away the shown detail (`m.detail = nil`). The pane shows the title and "loading…" until a full `Detail` comes back. That includes an item you looked at a second ago.
- **On the fixture PRs one open takes 4–25 ms, which you cannot see.** It becomes "quite some time" in three measured situations, which multiply each other:
  1. **The open item has a run in flight.** Every daemon event that names the open item starts a new full `Detail`. Loads are not merged and none is cancelled. One `r` on #101 sends 70 events and starts 70 loads, with up to 38 in flight at once. 63–66 of them are thrown away, and each load slows from 9 ms (19 ms over the socket) when alone to 304 ms (74 ms). Opening the item during such a run shows "loading…" 4–5× longer than idle (62–80 ms against 13–21 ms). On a large PR, a burst of 30 loads takes 4.0 s before the load that is actually shown lands.
  2. **Large PRs.** A `Detail` is mostly labels: the Labels read is 82–89% of the store time, and every load sends the item's whole label set. A PR 40× the fixture (2,000 units, 56k labels) costs 138 ms in-process, 287 ms over the daemon socket (14 MB of JSON), and 364 ms at p95 while analyses run.
  3. **A growing store.** The Labels query scans every label in the store, not just this item's. Labels are never deleted. As the store grows from 5k to 19k to 62k labels, #101's Labels read goes from 7.8 to 14.9 to 32.3 ms, although #101 itself did not change.
- **Smaller contributors:**
  - Analyses running on other items make `Detail` 2× slower at p50 and 3.7× at p95.
  - Each discovery tick rewrites every PR item, because urgency has an age term. That costs one reload of the open item per tick, and with a daemon there are two ticks a minute.
  - Run logs are never pruned.
- **Not causes:**
  - Rendering the detail: 5 ms for a fixture PR, 15 ms at 40×.
  - The list reload: 0.03 ms.
  - The pipeline: it does not run on open.

## What the user sees, in the code

**The placeholder.**
- `tui.Model.selectionChanged` (`internal/tui/model.go:747`) sets `m.detail = nil` and starts `loadDetail`.
- `viewMain` then draws the item title and a dim "loading…" (`model.go:1191`) until a `detailMsg` with the current sequence number arrives (`model.go:435`).
- There is no per-item cache. Moving back to an item re-reads all of it.

**Reloads on events.**
- `onEvent` (`model.go:710`): every event whose `ItemID` is the selected item (item, stage or patch; anything but remove) bumps `detailSeq` and starts another full `loadDetail`.
- Each load runs with `context.Background()` and is never cancelled. A load that finishes after a newer event has moved `detailSeq` on is dropped.
- List reloads are merged (`queueReload`), but detail reloads are not.

**Events per run.**
- `pipeline.runStage` (`internal/pipeline/pipeline.go:714`) emits a row when each stage starts and again when it ends.
- The daemon (`internal/daemon/inprocess.go:114`, `onStage` at `:204`) turns every row into a stage event. It also sends:
  - an item event for each stage start (`setState`);
  - a patch event for each finished stage;
  - a patch event for each review progress row ("k/n reviewed").

**`Detail` itself.**
- `InProcess.Detail` (`inprocess.go:335`) makes 13 store reads in sequence: item, fetch (diff), files, units, labels over every unit/file/item subject, overrides, links, groups, routing rows, comments, finding marks, aggregate, and all run logs.
- All of them use the store's single connection (`internal/store/sqlite.go:81`, `SetMaxOpenConns(1)`).
- `database/sql` hands a freed connection to a random waiter (`connRequests.TakeRandom` in Go 1.27). So the reads of concurrent `Detail`s interleave with each other and with the pipeline's reads and writes. The newest load, which is the only one the TUI uses, tends to finish last.

**The Labels query.**
- `listDocs` (`sqlite.go:182`) runs `SELECT body FROM docs WHERE kind=? AND parent IN (…) ORDER BY id`.
- SQLite plans it as `SEARCH docs USING INDEX sqlite_autoindex_docs_1 (kind=?)`. That is the primary key (kind, id), chosen to satisfy `ORDER BY id`. It walks every document of that kind and filters on parent.
- The plan is the same for 1, 63 and 2,801 parents, so every `listDocs` with parents scans the whole kind, Files and Units included. For labels this matters most: there are the most of them, and `RemoveItem` (`sqlite.go:258`) keeps them "as a cache". The label table only grows.

**The "loading bar" in the picker.** While an item is fetching or analysing, the picker shows a spinner and a one-line stage strip. That is the other thing the user may be calling a loading bar. Discovery and `AnalysePending` never re-run a Ready item: they only queue never-run items and not-analysed items the strategy now covers. These paths do re-run already-analysed items, and each run is one of the event storms measured below:
- **Settings saves:**
  - Role or owns, or a grouping knob: a full re-run of every analysed item the strategy allows (`control.go:365`).
  - Rank weights or `unsure_below`: an aggregate-only run of every analysed item.
  - A routing change: re-routes every analysed item that has a routing row not done (`review.go:195`). Under the default $0.40 cap, most deep groups of larger PRs stay queued, so those items re-run on every routing save.
- `r`, review actions (`D`/`R`/`s`), `:cap`.
- Overrides: an aggregate-only run of the item and of every other item that holds the unit.
- Submitting a review: an automatic re-run (`submit.go:119`).

## How to reproduce

```
go test ./internal/daemon -run TestOpenLatency    -count=1 -v -args -perf   # daemon/store side
go test ./internal/tui    -run TestOpenLatencyTUI -count=1 -v -args -perf   # through the real TUI Model
go test ./internal/daemon -run '^$' -bench Detail -benchtime 100x           # Detail benchmarks
```

**What the tests use.** Everything is offline:
- the generated fixture repo (PRs #101–#104 and the branch `05-spike-dark-mode`);
- the GitHub fixture;
- the recorded Jev, embedding and review answers;
- a file-backed store opened the way `prt` opens it (WAL, one connection);
- `auto_review = "all"`.

**What they measure.**
- `TestOpenLatency` (`internal/daemon/openlatency_test.go`) times `Detail` and each of its store reads, idle and over the socket. It also measures:
  - `Detail` while other items re-analyse;
  - bursts of concurrent loads;
  - the events each trigger sends;
  - larger synthetic items and store growth;
  - run-log growth.
- `TestOpenLatencyTUI` (`internal/tui/openlatency_test.go`) runs the real root `Model` the way Bubble Tea does: every command on its own goroutine, messages applied one at a time. It uses a colour renderer, over the in-process client and over the socket. It times how long "loading…" is visible and counts the `Detail` calls that are started, applied and dropped.

**Gating.** Both tests only log; they assert no budget. They are skipped unless `-perf` or `PRT_PERF=1` is given, so `make test` is unchanged.

**Machine and caveats.**
- Numbers are from one run on an Apple M3 Max (darwin/arm64, Go 1.27.2). Repeat runs varied by about ±10–15%.
- The "busy" writers re-analyse items in a loop with every cache from fetch on ignored (a forced `r` on the fetch row). That is CPU- and store-heavy, a worst case. A real first analysis spends most of its time waiting on Jev and LLM calls.
- "×k" items are the stored analysis of a fixture PR copied k times under a new id. They size the reads; the pipeline never ran on them.

## Measurements

### 1. One `Detail` of an analysed item, idle

| Item | Files | Units | Labels | JSON | In-process p50 / p95 | Socket p50 / p95 |
| --- | ---: | ---: | ---: | ---: | --- | --- |
| pr:101 | 14 | 48 | 3,026 | 811 KB | 8.4 / 9.0 ms | 18.6 / 19.7 ms |
| pr:102 | 20 | 50 | 1,406 | 446 KB | 6.1 / 6.6 ms | 12.0 / 12.7 ms |
| pr:103 | 3 | 7 | 147 | 60 KB | 3.5 / 4.0 ms | 4.1 / 4.8 ms |
| pr:104 | 4 | 6 | 268 | 79 KB | 3.7 / 4.2 ms | 4.6 / 5.2 ms |
| branch:05-spike-dark-mode | 3 | 5 | 285 | 78 KB | 3.4 / 4.0 ms | 4.8 / 5.4 ms |

- About 60 labels per unit: the question catalogue, plus line-level labels.
- JSON encode plus decode of #101's `Detail` costs 5.5 ms. That cost, plus the socket hop, is why the socket roughly doubles the time for the larger items.
- `Items()`, the list reload after each item event, takes 0.03 ms.

### 2. Where a `Detail` spends its store time (median per read)

| Read | pr:102 (idle) | ×10 copy (500 units) | ×40 copy (2,000 units) |
| --- | ---: | ---: | ---: |
| Labels (IN over all subjects) | **5.88 ms** (1,406 rows) | **36.8 ms** (14,060) | **125.4 ms** (56,240) |
| Units | 0.28 | 2.21 | 8.49 |
| Fetch (diff) | 0.09 (18 KB) | 0.62 (180 KB) | 1.99 (719 KB) |
| Groups / routing rows | 0.10 / 0.06 | 0.69 / 0.37 | 2.42 / 1.24 |
| Everything else | 0.76 | 1.04 | 2.12 |
| **Sum** | 7.2 ms | 41.8 ms | 141.7 ms |
| JSON size | 446 KB | 3.6 MB | 14.4 MB |
| `Detail` over the socket (p50) | 12.0 ms | 79 ms | 287 ms |

Labels account for 82% to 89% of the store time and most of the payload.

### 3. The Labels query scans the whole label table

The query plan, from `EXPLAIN QUERY PLAN` on the store file, is the same for 1, 63 and 2,801 parents: `SEARCH docs USING INDEX sqlite_autoindex_docs_1 (kind=?)`.

| Labels in the store | `Detail(pr:101)` p50 | pr:101's Labels read |
| ---: | ---: | ---: |
| 5,382 | 9.5 ms | 7.8 ms |
| 19,442 | 17.2 ms | 14.9 ms |
| 61,822 | 35.8 ms | 32.3 ms |

- pr:101 itself is unchanged throughout: 3,026 labels.
- The cost is about 0.45 µs per label anywhere in the store.
- One fixture-size PR adds 1.4k–3k labels. Extrapolating, a store that has seen 200 such PRs (about 500k labels) would spend about 0.2 s on every `Detail`'s Labels read alone, before decoding. This is an extrapolation, not a measurement.

### 4. `Detail(pr:101)` while other items' analyses write

| Writers (re-analysing other items) | p50 | p95 | max |
| --- | ---: | ---: | ---: |
| none (in-process) | 9.1 ms | 9.6 ms | 10.1 ms |
| 1 | 11.0 ms (×1.2) | 15.1 ms (×1.6) | 21.7 ms |
| 4, the worker pool's default concurrency | 18.2 ms (×2.0) | 35.6 ms (×3.7) | 40.9 ms |
| 4, over the socket | 33.7 ms | 49.2 ms | 57.2 ms |
| 4, reads on a **separate read handle** | 16.6 ms | 21.9 ms | 23.5 ms |
| ×40 copy, idle → 4 writers | 138 → 298 ms | 140 → 364 ms | 448 ms |

The separate read handle is a second `store.Open` of the same WAL file, so reads never wait for the pipeline's connection. It trims the tail by about a third (p95 35.6 → 21.9 ms) but not the median. The rest of the slowdown is CPU and GC shared with the writers.

### 5. Concurrent loads: what the TUI does when events arrive faster than loads finish

Each event starts one load and none is cancelled. "Last-issued" is the load the TUI actually shows.

| Burst of concurrent `Detail` | pr:101 idle | pr:101, 4 writers | ×10 copy idle | ×40 copy idle |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 9.0 ms | 32.8 ms | 39 ms | 138 ms |
| 10 | 77 ms | 103 ms | 382 ms | **1.32 s** |
| 30 | 223 ms | 297 ms | 1.09 s | **4.02 s** |

### 6. Events (and so full `Detail` loads) per trigger, with pr:101 open

| Trigger | Events | Naming the open item | Full `Detail` loads | List reloads requested |
| --- | ---: | --- | ---: | ---: |
| `r` (forced re-run) on the open item | 70 | 70 (13 item, 33 stage, 24 patch) | **70** | 13 |
| Aggregate-only run (override, gated re-aggregation) | 7 | 7 (4 item, 2 stage, 1 patch) | 7 | 4 |
| `r` on another item | 43 | 0 | 0 | 12 |
| Discovery pass, clock unchanged | 0 | 0 | 0 | 0 |
| Discovery pass, clock +60 s (one refresh tick) | 5 | 1 (item) | 1 | 5 |

- **The `r` run.** The 70 events span 643 ms. The median gap between them is 0.11 ms, and 62 of the 69 gaps are shorter than one idle load (9.2 ms). Nearly every load is overtaken before it lands.
- **The +60 s discovery pass.** With the clock moved by one refresh interval, every item is rewritten and announced. `discover.go:281` compares the whole item, and `Urgency`/`Why` change with the age term (`aggregate.Facts`, `log1p(age)`).
- **Two passes a minute with a daemon.** Under a daemon, the daemon loop (`cmd/prt/main.go:146`) and the TUI's own tick (`model.go:446`) both run discovery, each every `refresh_seconds` (default 60).

### 7. End to end through the TUI Model

The same run over both transports. "loading…" is the time from the selection until the detail is painted, including the render.

| Scenario | In-process | Socket |
| --- | --- | --- |
| Start-up: first item painted | 17.7 ms | 27.4 ms |
| Open an analysed item, idle (25 opens) | p50 4.9 · p95 12.9 ms | p50 5.7 · p95 21.8 ms |
| …pr:101 only | p50 12.8 ms | p50 20.9 ms |
| Open while pr:102 and pr:103 re-analyse (15 opens) | p50 9.5 · p95 20.6 ms | p50 10.5 · p95 47.3 ms |
| `r` on the open pr:101: `Detail` calls | 70, max **38** in flight | 70, max 15 in flight |
| …applied / dropped | 7 / **63** | 4 / **66** |
| …time per load (p50) | 304 ms (9 ms alone) | 74 ms (19 ms alone) |
| …total `Detail` time for one 1.3 s run | **13.6 s** | 5.7 s |
| Open pr:101 **while it re-runs** | "loading…" p50 **61.9 ms** (idle: 12.8) | p50 **79.9 ms** (idle: 20.9) |
| Render of an applied detail (×1 / ×10 / ×40) | 5.2 / 7.0 / 15.2 ms | 5.1 / 6.5 / 13.6 ms |

- **Fewer loads in flight over the socket.** Fewer loads overlap there, probably because events pass through two more buffers and a JSON hop before they reach the TUI. Each load is still about 4× slower than alone.
- **A start-up artifact, and what it shows.** A TUI started with a backlog of buffered events turns each one that names the selected item into a load. In a first, undrained run, 33 loads at start-up kept "loading…" up for 0.7 s. The test now drains the backlog, because a real session normally has none. The same storm happens whenever the TUI falls behind its event stream.

### 8. Run-log growth

Each run appends 9 run-log rows, which are never pruned, and `Detail` returns all of them.

| Extra runs of pr:101 | Rows | `Detail` JSON | RunLogs read |
| ---: | ---: | ---: | ---: |
| 0 | 9 | 811 KB | 0.03 ms |
| 100 | 919 | 992 KB | 1.2 ms |
| 1,000 | 9,019 | 2.6 MB | 12.6 ms |

## Root causes, ranked

1. **Detail reload storm: one full reload per event, not merged, not cancelled** (`tui/model.go:710`, plus the daemon's per-row events).
   - **Explains:** "sometimes it takes quite some time".
   - **Impact:** the biggest multiplier.
   - **Measured:** 70 loads per run of the open item, up to 38 in flight, 90–94% thrown away. Each load is about 4× slower than alone over the socket (74 ms against 19 ms) and about 33× in-process (304 ms against 9 ms). "loading…" lasts about 4–5× longer when an item is opened during a run.
   - **Large PRs:** 30 concurrent loads of a 2,000-unit PR take 4.0 s before the shown load lands.
   - **When it happens:** any re-run of an analysed item, not only `r`: settings saves, routing reloads, overrides, review actions, caps, submitting a review.
   - **The picker:** these same runs light up the stage strip, the picker's "loading bar".
2. **The TUI blanks the pane on every selection and keeps no per-item cache** (`model.go:747`, `:1191`).
   - **Explains:** "still shows a loading bar" on every open, even when nothing changed.
   - **Cost:** each open pays a full `Detail`: 13 reads, all labels, the diff, and a JSON round trip over the socket. That is 0.8 MB for #101 and 14 MB for a 2,000-unit PR.
3. **Every `Detail` carries the item's full label set**, which the Overview mostly does not need.
   - Labels are 82–89% of the store time and most of the payload.
   - `Detail` scales with PR size: 8 ms, 39 ms and 138 ms in-process, and 19, 79 and 287 ms over the socket, for 1×, 10× and 40× of #101.
4. **The Labels query (and every `listDocs` with parents) scans the whole kind** (`sqlite.go:182`).
   - The primary-key plan is chosen for `ORDER BY id`, and labels are never removed (`sqlite.go:258`).
   - **Effect:** every `Detail` slows as history grows, independent of the PR: 7.8 → 32.3 ms for the same item as the store goes from 5k to 62k labels.
5. **Contention with running analyses.**
   - **Where it comes from:** one store connection with random hand-off (`sqlite.go:81`), plus shared CPU and GC.
   - **Measured:** ×2.0 at p50 and ×3.7 at p95 with 4 writers. A separate read connection removes part of the tail only.
6. **Discovery churn.** Every refresh tick rewrites every PR item and sends an item event, because urgency has a continuous age term. That is one reload of the open item and N list reloads per tick, twice a minute with a daemon. Small, but it also wakes every client.
7. **Unbounded run log returned whole.** +12.6 ms and +1.8 MB per `Detail` after 1,000 runs. Small.

**Related correctness note (from reading the code, not measured).** While an item re-runs, `Detail` serves a mix:
- the run replaces files and units at parse, links and groups at cluster, the routing rows during review, and the aggregate only at the end;
- for a new head (a Stale item after `r`), the diff still comes from `AnalysedSHA` (`inprocess.go:344`) while files and units are already the new head's.

Each applied reload during a run therefore shows a different, partly inconsistent mix. This is the opposite of the user's ask: "show the old with a small note while the review updates".

## Fix proposals

These are in suggested order. Each can be checked with the two tests above.

1. **TUI: show what you have, then refresh. Add an "updating" note.** This is the user's request 6.
   - **Change:**
     - Keep the last `proto.Detail` per item: a small LRU, say 16 items, dropped on remove.
     - `selectionChanged` shows the cached detail at once and loads in the background.
     - Only an item never opened this session shows "loading…".
     - While a load is in flight, or the item is fetching or analysing, the tab bar's right-hand context shows a dim "updating…". It shows "updating · <stage>" when the stage is known.
   - **Expected:** re-opening a seen item costs one render (2–6 ms measured) and shows no placeholder. A large PR saves 140–290 ms per re-open.
   - **Check:** `TestOpenLatencyTUI` section 1, which should gain a "re-open" row.
2. **TUI: merge and cancel detail reloads.**
   - **Change:**
     - Mirror `queueReload`: one `Detail` in flight per item, plus a dirty flag that triggers a single follow-up when it lands.
     - Add a trailing debounce of about 150 ms.
     - Give each load a cancellable context and cancel it on selection change.
     - Do not reload on stage-start rows. The picker strip already takes the `RunLog` from the event, and a start row stores nothing. Reload on progress rows only while the Pipeline page is open.
   - **Expected for one `r` on #101:**
     - 70 loads drop to at most 9, one per finished stage, without the debounce, or about 3–5 with it.
     - Loads in flight drop from 38 to 1.
     - "loading…" during a run comes back to about one load (13–21 ms on fixtures).
   - **Check:** `TestOpenLatencyTUI` sections 3 and 4.
3. **Store: let `listDocs` use the parent index, and bound the label cache.**
   - **Change:**
     - Add migration 3 with `CREATE INDEX docs_kind_parent_id ON docs(kind, parent, id)`, or drop `ORDER BY id` for parent lists and sort in Go.
     - Delete labels and overrides whose subject no stored item references, on `RemoveItem` or in a periodic sweep.
   - **Expected:** the plan check in `TestOpenLatency` section 6 shows the parent index. #101's Labels read stays at about 7 ms whatever the store holds (now 32 ms at 62k labels).
4. **Daemon/proto: a lighter `Detail` for the Overview.**
   - **Change:** split it into an overview part and lazy parts.
     - Overview part: item, aggregate, groups, routing rows, comments, finding marks, the latest run's rows.
     - Lazy parts: labels, units, diff. The Code page, raw labels and the Pipeline page fetch these on demand, by item and head, and cache them per item in the TUI as in proposal 1.
   - **Expected:** opening the Overview of a 2,000-unit PR goes from 138 ms in-process and 287 ms over the socket (14 MB) to roughly the cost without Labels and Units: about 5–10 ms by the section 2 breakdown.
5. **Daemon: keep serving the last complete analysis while a run is in flight.**
   - **Change:**
     - Write a run's outputs keyed by (item, head) and switch the item's current analysis when aggregate lands. Alternatively, have `Detail` read only the snapshot of `AnalysedSHA`.
     - The TUI then needs one reload per finished run, plus the progress rows for the strip and the "updating · <stage>" note.
   - **Why:** this is the daemon half of the user's "show the old with a small note". It also removes the mid-run mixes described above.
6. **Store: a read-only connection pool for `Detail`, `Items` and Search.**
   - **Change:** a second `*sql.DB` on the same file with `mode=ro` and a few connections.
   - **Measured:** p95 with 4 writers falls from 35.6 to 21.9 ms; p50 is unchanged.
   - **Priority:** worth doing, but after proposal 2. Once the storms are gone, little contention is left.
7. **Daemon: stop the discovery churn.**
   - **Change:**
     - Skip the write and the event when only the age-driven `Urgency`/`Why` moved: compare `itemKey` without them, or round the age term.
     - Do not run two discoveries a minute when a daemon is attached: the TUI's tick could just reload `Items`.
   - **Expected:** 0 events per idle tick (5 now).
8. **Run log: return only the latest run's rows in `Detail`, and prune older ones** (keep the last N runs per item). The Pipeline page shows the latest run.
   - **Expected:** removes up to 12.6 ms and 1.8 MB per `Detail` at 1,000 runs.

Proposals 1 and 2 are TUI-only and small, and they remove almost all of what the user describes on fixture-size PRs. Proposals 3 and 4 make it hold for large PRs and a long-lived store. Proposal 5 is the clean form of "show the old review while the new one runs".
