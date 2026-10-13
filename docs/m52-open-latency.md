# M5.2: why opening an analysed PR shows "loading…" (measurements)

2026-10-13. A diagnosis, followed by the daemon and store half of the fixes. The sections below keep the original "before" tables; each now has an "after" table measured the same way once proposals 3–8 landed in `internal/daemon` and `internal/store` (see "After the daemon and store fixes" at the end of the short answer). The TUI half (proposals 1 and 2) is measured through the TUI in section 7. This answers the user's report:

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

### After the daemon and store fixes

- **Opening is a lean `Detail`.** `DetailOf` with `NoLabels` reads no label and no override. For #101 it takes 0.97 ms in-process (full `Detail` before: 8.8 ms) and 3.6 ms over the socket (18.5 ms), and its JSON is 206 KB instead of 811 KB. A 2,000-unit PR opens in 13.6 ms in-process and 40 ms over the socket (137 ms and 272 ms before). Labels come separately, per unit: 0.07 ms for one unit of #101.
- **The label read no longer depends on the store.** Parent lists use the new `docs(kind, parent, id)` index. #101's Labels read stays at about 5.2 ms whether the store holds 7k or 64k labels (before: 8.8 ms → 31.9 ms). Unreferenced labels are pruned on remove and at start-up.
- **A re-run serves the last complete analysis.** While the open item re-runs, `Detail` serves its snapshot (`Run.Snapshot`) in 0.17 ms (lean 0.11 ms), with live comments, and switches when the new aggregate lands.
- **Reads have their own connections.** With 5 analyses writing, the lean `Detail` of #101 takes 2.3 ms at p50 and 7.5 ms at p95. The full one takes 14.1 / 17.4 ms (before: 25.9 / 52.2 ms).
- **An idle refresh tick sends nothing.** It sent 6 events with the six fixture items; now it sends 0. An item's age term is rewritten once a day.
- **The run log is bounded.** `Detail` returns the current run only (10 rows for #101 however long the log is), and finishing a run keeps the last 5 runs.
- **What stayed for the TUI:** the reload storm (one full load per event: 70–99 events per `r`) and the empty pane on open. Proposals 1 and 2 address them in the TUI: one `r` on #101 now makes 3 `Detail` calls with at most 1 in flight, and a re-opened item paints from the cache in about 1.7 ms without "loading…" (section 7).

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
- a file-backed store opened the way `prt` opens it (WAL, one connection; since the fixes, one write connection plus a read-only pool);
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

**The "after" runs.**
- Same machine, test and benchmark. Since then the fixture set gained #106, so there are six items and five writers in the busy rows. Where that changes a number, the before figure was measured again on the same tree before the fixes ("before, re-run").
- The "after" numbers come from one run each of `TestOpenLatency` and of the benchmarks, on the tree with the daemon and store fixes.
- `TestOpenLatency` now logs both forms of each `Detail`: "full" (every part, what the old TUI loaded) and "lean" (`DetailOf` with `NoLabels`, what screens load on open since M5.2). It also logs `Labels` for one unit and for all subjects.
- A new section times `Detail` while the open item's own re-run is held in its review stage. The test's reviewers sit behind a gate that can hold review calls.
- During these runs another work package was re-recording the review prompts, so the fixture's review replays missed and those reviews failed quickly. This changes the review stage's events (more progress rows, see section 6) and makes the busy writers somewhat lighter. The read paths measured here do not depend on it.

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

**After.** "Full" is `Detail`; "lean" is `DetailOf` with `NoLabels`. `Labels` is the separate labels call, for one unit and for all of the item's subjects.

| Item | JSON full / lean | In-process full p50 / p95 | In-process lean p50 / p95 | Socket full p50 / p95 | Socket lean p50 / p95 | `Labels` one unit / all |
| --- | --- | --- | --- | --- | --- | --- |
| pr:101 | 778 / 206 KB | 5.9 / 6.5 ms | **0.97 / 1.45 ms** | 15.8 / 21.8 ms | **3.6 / 7.6 ms** | 0.07 / 4.9 ms |
| pr:102 | 439 / 165 KB | 3.3 / 3.5 ms | 0.83 / 1.26 ms | 9.0 / 13.1 ms | 2.6 / 2.8 ms | 0.07 / 2.5 ms |
| pr:103 | 60 / 30 KB | 0.52 / 0.90 ms | 0.23 / 0.46 ms | 1.2 / 1.7 ms | 0.58 / 0.96 ms | 0.05 / 0.28 ms |
| pr:104 | 79 / 28 KB | 0.64 / 1.01 ms | 0.21 / 0.34 ms | 1.6 / 1.9 ms | 0.56 / 0.82 ms | 0.07 / 0.45 ms |
| pr:106 (new) | 337 / 84 KB | 2.7 / 3.1 ms | 0.48 / 0.88 ms | 5.9 / 6.3 ms | 1.3 / 1.7 ms | 0.19 / 2.3 ms |
| branch:05-spike-dark-mode | 77 / 24 KB | 0.66 / 1.08 ms | 0.19 / 0.43 ms | 1.7 / 2.1 ms | 0.58 / 0.99 ms | 0.06 / 0.46 ms |

- **pr:106, before (re-run):** 334 KB, 6.0 / 6.3 ms in-process, 9.5 / 9.9 ms over the socket.
- **The full `Detail` got faster too.** Small items drop from about 4 ms to under 1 ms, because their Labels read no longer scans every label in the store. #101 drops from 8.8 to 5.9 ms.
- **What the lean `Detail` still carries.** The JSON of #101's lean `Detail` (206 KB) is mostly the units' hunks, the diff, the aggregate and the routing rows. Encode plus decode costs 1.05 ms (full: 5.3 ms).
- **The rest is negligible.** `Items()` takes 0.03 ms. The routing info `Detail` builds each time costs under 0.01 ms, so it is not cached.

`go test -bench Detail -benchtime 100x` (ns/op as ms; "before" re-run on the same tree before the fixes):

| Benchmark | Before | After, full | After, lean |
| --- | ---: | ---: | ---: |
| idle/branch:05-spike-dark-mode | 4.16 | 0.62 | 0.21 |
| idle/pr:101 | 9.27 | 4.84 | **0.92** |
| idle/pr:102 | 6.97 | 2.64 | 0.84 |
| idle/pr:103 | 3.96 | 0.44 | 0.24 |
| idle/pr:104 | 4.08 | 0.60 | 0.22 |
| idle/pr:106 | 5.87 | 2.78 | 0.50 |
| busy/pr:101 (5 writers) | 30.1 | 11.3 | 2.26 |
| socket/pr:101 | 17.6 | 10.7 | 3.10 |
| labels1/pr:101 (`Labels`, one unit) | — | 0.06 | — |

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

**After.** These are the same reads, in the order `Detail` makes them. The lean `Detail` skips the Labels and Overrides reads.

| Read | pr:102 (idle) | ×10 copy (500 units) | ×40 copy (2,000 units) |
| --- | ---: | ---: | ---: |
| Labels (IN over all subjects; full only) | 2.56 ms (1,406 rows) | 27.3 ms (14,060) | 105.0 ms (56,240) |
| Overrides (full only) | 0.05 | 0.65 | 2.72 |
| Units | 0.19 | 2.10 | 8.13 |
| Fetch (diff) | 0.07 (18 KB) | 0.58 (180 KB) | 2.08 (719 KB) |
| Groups / routing rows | 0.07 / 0.04 | 0.67 / 0.31 | 2.47 / 1.12 |
| Everything else | 0.49 | 0.59 | 1.26 |
| **Sum, full** | 3.47 ms | 32.2 ms | 122.7 ms |
| **Sum, lean** | **0.86 ms** | **4.27 ms** | **15.1 ms** |
| JSON full / lean | 439 / 165 KB | 3.6 MB / 855 KB | 14.4 / 3.3 MB |
| `Detail` over the socket (p50), full / lean | 9.0 / 2.6 ms | 70 / **11.1 ms** | 272 / **39.7 ms** |

- **Labels is still most of a full `Detail`.** What remains is decoding the item's own labels, not scanning the store's (section 3). The lean `Detail` leaves that read out.
- **`Labels` for one unit** of the ×10 and ×40 copies takes 0.07–0.09 ms.

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

**After.** Migration 3 replaces `docs_parent (kind, parent)` with `docs_kind_parent_id (kind, parent, id)`. A parent list has no `ORDER BY` any more; the store sorts those rows by id itself. The plan is now `SEARCH docs USING INDEX docs_kind_parent_id (kind=? AND parent=?)` for 1, 63 and 2,801 parents. A store test asserts this plan, on a fresh store and on a version-2 store file migrated on open.

| Labels in the store | `Detail(pr:101)` p50 before (re-run) | after | pr:101's Labels read before (re-run) | after |
| ---: | ---: | ---: | ---: | ---: |
| 7,132 | 9.2 ms | 6.8 ms | 8.8 ms | **5.2 ms** |
| 21,192 | 15.2 ms | 6.4 ms | 13.4 ms | **5.2 ms** |
| 63,572 | 35.6 ms | 6.5 ms | 31.9 ms | **5.2 ms** |

- **Flat.** The read now costs what #101's own 3,026 labels cost to read and decode.
- **Bounded.** The label cache is bounded by what the store references:
  - Removing an item deletes the labels of its units, files, item, branch-state and group subjects, and of the pairs among its units, unless another stored item still references them.
  - A sweep at client start-up deletes every label no stored item references.
  - Overrides are never deleted.
- **What pruning costs.** The start-up sweep over the 64k-label store took 16 ms, with nothing to prune. Removing the ×40 copy took 148 ms, most of it deleting its 56k labels. Removing the ×10 copy took 52 ms.

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

**After.** A file store in WAL mode now has a read-only pool of 4 connections. Every store read uses it; writes keep the single write connection.

| Writers (re-analysing other items) | Full p50 | Full p95 | Full max | Lean p50 | Lean p95 | Lean max |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| none (in-process) | 5.97 ms | 6.24 ms | 6.49 ms | 0.95 ms | 1.43 ms | 1.46 ms |
| 1 | 7.67 ms (×1.3) | 8.61 ms (×1.4) | 8.76 ms | 1.06 ms | 1.94 ms | 2.83 ms |
| 5 (all other items) | 14.1 ms (×2.4) | 17.4 ms (×2.8) | 20.0 ms | **2.26 ms** | **7.48 ms** | 10.4 ms |
| 5, over the socket | 32.6 ms | 37.6 ms | 39.5 ms | 7.20 ms | 9.54 ms | 16.1 ms |
| ×40 copy, idle → 5 writers | 120 → 436 ms | 122 → 556 ms | 698 ms | 13.6 → 57.7 ms | 14.0 → 76.1 ms | 90.9 ms |

- **Before, re-run with five writers:**
  - full in-process: 25.9 / 52.2 / 86.4 ms (p50 / p95 / max);
  - over the socket: 41.4 / 72.1 / 92.5 ms;
  - on a second read handle: 19.6 / 29.0 / 33.3 ms;
  - ×40 copy with writers: 311 / 407 ms.
- **The pool removes the wait for the write connection.** The full `Detail`'s p95 with five writers falls from 52.2 to 17.4 ms. What is left is CPU and GC shared with the writers. The ×40 full `Detail` (120 ms of mostly JSON decoding) suffers most from that. Each busy writer now also reads its item's analysis once before it runs (the snapshot), which adds to the load.
- **The lean `Detail` is what screens open with.** For #101 under five writers it stays at 2.3 ms p50 in-process and 7.2 ms over the socket.

### 5. Concurrent loads: what the TUI does when events arrive faster than loads finish

Each event starts one load and none is cancelled. "Last-issued" is the load the TUI actually shows.

| Burst of concurrent `Detail` | pr:101 idle | pr:101, 4 writers | ×10 copy idle | ×40 copy idle |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 9.0 ms | 32.8 ms | 39 ms | 138 ms |
| 10 | 77 ms | 103 ms | 382 ms | **1.32 s** |
| 30 | 223 ms | 297 ms | 1.09 s | **4.02 s** |

**After.** The same bursts, full / lean. The busy column now has 5 writers. In the "1" row of the copies, the burst is a single in-process `Detail`.

| Burst of concurrent `Detail` | pr:101 idle | pr:101, 5 writers | ×10 copy idle | ×40 copy idle |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 8.0 / 1.6 ms | 16.2 / 3.8 ms | 31.3 / 3.9 ms | 120 / 13.6 ms |
| 10 | 21.7 / **2.1 ms** | 55.6 / **11.0 ms** | 78 / **14.2 ms** | 306 / **35.5 ms** |
| 30 | 58.0 / **4.8 ms** | 162 / **20.4 ms** | 189 / **24.5 ms** | 739 / **90.2 ms** |

- **Bursts still cost.** The daemon cannot stop them; the TUI's coalescing and cancelling (proposal 2) does. Each load of the burst is cheaper, though: the last-issued lean load of a 30-load burst on the ×40 copy lands in 90 ms (before: 4.0 s for full loads).
- **Before, re-run:** pr:101 idle 9.2 / 84 / 244 ms; with 5 writers 51 / 178 / 309 ms; ×40 idle 1.30 s and 3.95 s for 10 and 30 loads.

**During the open item's own re-run (new).** `r` on the Pipeline page's review row of #101 starts a re-run; the measurement holds it in its review stage. `Detail` then serves the analysis as it was before the run (`Run.Snapshot`, `Run.Stage` "review"), with the item, comments, finding marks, run log and the live routing rows (`Run.Reviews`) read from the store.

| `Detail(pr:101)` | Full p50 / p95 | Lean p50 / p95 |
| --- | ---: | ---: |
| idle (from the store) | 6.69 / 7.21 ms | 1.10 / 1.69 ms |
| during its re-run (from the snapshot) | **0.17 / 0.62 ms** | **0.11 / 0.14 ms** |
| during its re-run, over the socket | — | 2.19 / 2.72 ms |

- **What the run costs.** The snapshot is taken from the store once, just before the run writes anything: one full read of the analysis, about the cost of one idle full `Detail`.
- **Before, by comparison.** `Detail` served the store's mix of old and new parts while the run went. It cost as much as an idle `Detail` or more (the run writes on the same connection).

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

**After.** "Full `Detail` loads" still counts what the pre-M5.2 TUI would start: one per event that names the open item.

| Trigger | Events | Naming the open item | Full `Detail` loads (old TUI) | List reloads requested |
| --- | ---: | --- | ---: | ---: |
| `r` (forced re-run) on the open item | 99 | 99 (12 item, 48 stage, 39 patch) | 99 | 12 |
| Aggregate-only run | 7 | 7 (4 item, 2 stage, 1 patch) | 7 | 4 |
| `r` on another item | 43 | 0 | 0 | 12 |
| Discovery pass, clock unchanged | 0 | 0 | 0 | 0 |
| Discovery pass, clock +60 s (one refresh tick) | **0** | 0 | 0 | 0 |
| Discovery pass, clock +1 h | **0** | 0 | 0 | 0 |
| Discovery pass, clock +1 day | 6 | 1 (item) | 1 | 6 |

- **Discovery churn is gone.** A pass now compares each item without its urgency fields (`Urgency`, `Why`, `UrgencyQuartile`). When only those moved, the item is rewritten only if its age or idle time crossed a whole day since discovery last wrote it in this process. An idle tick therefore writes nothing and sends nothing: 0 events (before: one per item, 5 in the original run and 6 with #106). The stored urgency, and so the picker order, lags the clock by less than a day. A settings save forgets those days, because weights move urgency too, so the next pass stores whatever moved. A unit test asserts the 0 writes and 0 events for +60 s and +1 h, and one write per item after a day.
- **Run events are unchanged; the TUI work package coalesces them.** The daemon still sends a stage event per row, a patch per finished stage and per review progress row, an item event per state change, and the aggregate-done item event. 99 events per `r` instead of 70 is not a daemon change. In these runs the fixture's review replays missed (see "The 'after' runs"), so every routed review failed, and each failure adds a progress row.
- **The TUI's own discovery tick** is the TUI package's change.

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

**After** (proposals 1 and 2 in `internal/tui`, on top of the daemon and store fixes; same harness, `go test ./internal/tui -run TestOpenLatencyTUI -count=1 -v -args -perf`, M3 Max, ±10–15%):

| Scenario | Before in-process | After in-process | Before socket | After socket |
| --- | --- | --- | --- | --- |
| Start-up: first item painted | 17.7 ms | 7.4 ms | 27.4 ms | 9.7 ms |
| First open of an item ("loading…") | p50 4.9 · p95 12.9 | p50 2.9 · p95 5.8 (n=4) | p50 5.7 · p95 21.8 | p50 3.1 · p95 6.5 |
| Re-open, painted from the cache (no "loading…") | — | p50 1.7 · p95 4.4 | — | p50 1.6 · p95 3.7 |
| Re-open, until revalidated | — | p50 4.6 · p95 10.4 | — | p50 4.6 · p95 11.5 |
| …pr:101 only | p50 12.8 | p50 3.4 | p50 20.9 | p50 2.5 |
| Open while pr:102/103 re-analyse: painted | p50 9.5 · p95 20.6 | p50 2.7 · p95 3.9 | p50 10.5 · p95 47.3 | p50 2.6 · p95 5.7 |
| …revalidated | — | p50 7.4 · p95 11.1 | — | p50 6.9 · p95 16.6 |
| `r` on the open pr:101: `Detail` calls / max in flight | 70 / 38 | **3 / 1** | 70 / 15 | **3 / 1** |
| …applied / dropped | 7 / 63 | 3 / 0 | 4 / 66 | 3 / 0 |
| …time per load (p50) | 304 ms | 1.2 ms | 74 ms | 5.3 ms |
| …total `Detail` time for one run | 13.6 s | 4.6 ms | 5.7 s | 16.7 ms |
| Open pr:101 while it re-runs | "loading…" p50 61.9 | painted p50 3.0, no "loading…"; revalidated p50 7.6 | p50 79.9 | painted 3.4; revalidated 12.0 |
| Render ×1 / ×10 / ×40 | 5.2 / 7.0 / 15.2 | 3.2 / 6.1 / 16.0 | 5.1 / 6.5 / 13.6 | 4.1 / 5.9 / 15.4 |

- **The storm is gone.** For one `r` on #101 the loader started 3 loads, applied 3 and dropped none. Stage rows never reload; patches reload only on done/cached/failed rows; item events reload only when the analysis changed; event-triggered reloads are debounced (150 ms trailing, 1 s maximum wait) and at most one call per item is in flight.
- **Re-opening never shows "loading…".** A re-opened item is painted from the TUI's 16-item cache and revalidated in the background. During a re-run the previous analysis stays on screen with "↻ updating review · <stage>" until the new aggregate lands.
- **Where the gain comes from.** The faster time per load is mostly the lean Detail (no labels) and the daemon and store work; the drop from 70 calls to 3, with at most 1 in flight, is the TUI's coalescing.

**Re-run after integration** (2026-10-13, every M5.2 package committed, the re-recorded reviews in place; same machine, nothing else running). Both harnesses agree with the tables above within the stated noise:

| Measurement | In-process | Socket |
| --- | --- | --- |
| TUI start-up: first item painted | 8.4 ms | 11.6 ms |
| TUI re-open from the cache (no "loading…") | p50 1.9 · p95 3.1 ms | p50 1.9 · p95 4.0 ms |
| TUI `r` on #101: Detail calls / max in flight / dropped | 3 / 1 / 0 | 3 / 1 / 0 |
| TUI open #101 while it re-runs: painted / revalidated | p50 3.6 / 8.6 ms | p50 4.1 / 13.8 ms |
| Daemon lean Detail of #101 | p50 1.10 · p95 1.52 ms (217 KB) | p50 3.30 · p95 3.51 ms |
| Daemon full Detail of #101 | p50 6.35 · p95 6.82 ms (789 KB) | p50 15.0 · p95 15.7 ms |
| Daemon lean Detail of #101 with 5 writers | p50 2.72 · p95 5.24 ms | p50 7.38 · p95 9.16 ms |
| Daemon Detail of #101 during its own re-run (snapshot) | full 0.25 · lean 0.19 ms | lean 2.12 ms |
| Events per forced `r` on #101 | 69 (item 12, stage 33, patch 24) | — |
| Idle discovery tick (+60 s, +1 h) | 0 events | — |

- The forced `r` now sends 69 events instead of the 99 in section 6: the review replays hit again (the fixture reviews were re-recorded after the output-cap change), so no failed review adds progress rows. The TUI still makes 3 Detail calls for it.

### 8. Run-log growth

Each run appends 9 run-log rows, which are never pruned, and `Detail` returns all of them.

| Extra runs of pr:101 | Rows | `Detail` JSON | RunLogs read |
| ---: | ---: | ---: | ---: |
| 0 | 9 | 811 KB | 0.03 ms |
| 100 | 919 | 992 KB | 1.2 ms |
| 1,000 | 9,019 | 2.6 MB | 12.6 ms |

**After.**
- **`Detail` returns only the current run.** `Store.CurrentRunLogs` returns the rows from the last fetch row on (every row when there is none), as the Pipeline page's `currentRun` reads them. It walks the log newest first and stops at that fetch row.
- **The log keeps the last 5 runs per item.** Finishing a run prunes the older rows, whatever the outcome. Runs are delimited by fetch rows, and aggregate-only rows belong to the run they follow.
- **`Store.RunLogs` still returns every stored row.**

The worst case appends rows without the prune, as the test does:

| Extra runs of pr:101 (unpruned) | Rows `Detail` returns | `Detail` JSON | Current-run read | `Detail` p50 |
| ---: | ---: | ---: | ---: | ---: |
| 100 | 10 (the current run: 9 stages and one aggregate-only row) | 778 KB | 0.02 ms | 6.05 ms |
| 1,000 | 10 | 778 KB | 0.03 ms | 6.14 ms |

- **Pruning that log to 5 runs** deleted 9,978 rows in 5.9 ms and left 50.
- **The Pipeline page's stage log** now shows only the current run (it showed every stored run before). That is the accepted cost of this fix.

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

**Fixed since:**
- **During a run, the snapshot is served** (proposal 5).
- **Outside a run, the diff matches the stored units.** `Detail` reads the fetch of `Item.UnitsSHA`, the head stage 1 stored the units for, before `AnalysedSHA` and `HeadSHA`.

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
   - **Done:**
     - Migration 3 adds the index and drops `docs_parent`. Parent lists have no `ORDER BY` and are sorted in Go.
     - `Store.ItemSubjects` reads an item's label subjects from document ids alone.
     - `Store.PruneLabels` prunes on `Remove` (not for previews) and in a sweep at client start-up.
     - Overrides are kept: they are user data and feed calibration.
     - #101's Labels read stays at 5.2 ms from 7k to 64k labels (section 3).
4. **Daemon/proto: a lighter `Detail` for the Overview.**
   - **Change:** split it into an overview part and lazy parts.
     - Overview part: item, aggregate, groups, routing rows, comments, finding marks, the latest run's rows.
     - Lazy parts: labels, units, diff. The Code page, raw labels and the Pipeline page fetch these on demand, by item and head, and cache them per item in the TUI as in proposal 1.
   - **Expected:** opening the Overview of a 2,000-unit PR goes from 138 ms in-process and 287 ms over the socket (14 MB) to roughly the cost without Labels and Units: about 5–10 ms by the section 2 breakdown.
   - **Done, labels only.**
     - `DetailOf` with `NoLabels` reads no label and no override.
     - `Labels` reads only the subjects it is asked for, with overrides applied as `Detail` applies them.
     - Units and the diff stay in `Detail`, because the shared protocol keeps them there.
     - The 2,000-unit copy opens in 13.6 ms in-process and 40 ms over the socket (3.3 MB).
5. **Daemon: keep serving the last complete analysis while a run is in flight.**
   - **Change:**
     - Write a run's outputs keyed by (item, head) and switch the item's current analysis when aggregate lands. Alternatively, have `Detail` read only the snapshot of `AnalysedSHA`.
     - The TUI then needs one reload per finished run, plus the progress rows for the strip and the "updating · <stage>" note.
   - **Why:** this is the daemon half of the user's "show the old with a small note". It also removes the mid-run mixes described above.
   - **Done, as an in-memory snapshot.**
     - When a full run of an item with a complete analysis starts, the daemon holds that analysis in memory (at most 8, oldest dropped first).
     - While the run goes, `Detail`, `DetailOf` and `Labels` serve it, with `Run{Stage, Snapshot, SnapshotSHA, Reviews}` set.
     - The snapshot is dropped when the aggregate lands, before that row's events go out.
     - A stopped or failed run keeps its snapshot (served with `Run` nil) until a run completes or the daemon restarts.
6. **Store: a read-only connection pool for `Detail`, `Items` and Search.**
   - **Change:** a second `*sql.DB` on the same file with `mode=ro` and a few connections.
   - **Measured:** p95 with 4 writers falls from 35.6 to 21.9 ms; p50 is unchanged.
   - **Priority:** worth doing, but after proposal 2. Once the storms are gone, little contention is left.
   - **Done.**
     - A file store in WAL mode reads through a read-only pool of 4 connections (`mode=ro`). Writes keep the single connection.
     - `OpenMemory` keeps one connection.
     - A store test shows that a read does not wait behind an open write transaction.
     - Full `Detail` p95 with 5 writers: 52.2 → 17.4 ms (section 4).
7. **Daemon: stop the discovery churn.**
   - **Change:**
     - Skip the write and the event when only the age-driven `Urgency`/`Why` moved: compare `itemKey` without them, or round the age term.
     - Do not run two discoveries a minute when a daemon is attached: the TUI's tick could just reload `Items`.
   - **Expected:** 0 events per idle tick (5 now).
   - **Done in the daemon.** It skips writes where only the age moved within a whole day: 0 events per idle tick (section 6). The TUI's second tick is the TUI package's change.
8. **Run log: return only the latest run's rows in `Detail`, and prune older ones** (keep the last N runs per item). The Pipeline page shows the latest run.
   - **Expected:** removes up to 12.6 ms and 1.8 MB per `Detail` at 1,000 runs.
   - **Done.** `Detail` serves `Store.CurrentRunLogs`. Finishing a run keeps the item's last 5 runs (section 8).

Proposals 1 and 2 are TUI-only and small, and they remove almost all of what the user describes on fixture-size PRs. Proposals 3 and 4 make it hold for large PRs and a long-lived store. Proposal 5 is the clean form of "show the old review while the new one runs".
