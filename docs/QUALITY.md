# Label quality

How good are prt's labels, aggregates and headlines on the fixture set, and
what did the M5.2 tuning change? This page holds the method, the metric
definitions and the scoreboards. The code is `internal/quality` (metrics,
strict ground-truth parser, offline runner) and `tools/quality` (the CLI).

## Method

1. **Ground truth.** `testdata/groundtruth/*.toml` holds one hand-written
   reference answer per fixture item (#101, #102, #103, #104, #106 and the
   branch `05-spike-dark-mode`). They were written blind, from the patches,
   the base tree and `prs.json` only; format, `firm`/`soft` certainty and the
   judgment calls are in `testdata/groundtruth/README.md`. The parser is
   strict: an undefined key, a value outside the spec's vocabularies, a bad
   line range or a malformed `also_at` is an error.
2. **Offline run.** The full pipeline (fetch → parse → link → embed → Jev →
   cluster → route → review → aggregate) runs over the fixture PRs and then
   the fixture branch in one environment, exactly like the replay tests:
   `pipelinetest.Open`, `RunAll`, `RunBranches`; Jev answers from
   `jev.Replay` (strict), vectors from `embed.Replay`, reviews from one
   `review.Replay` per tier named after the fixture repo's configured models,
   the default routing. A Jev or embedding request without a recording, or a
   failed stage, fails the run. Nothing touches the network.
3. **Scoring.** Each item's stored aggregate (`ItemAgg`) and units are
   compared with its ground truth. Review answers feed no metric (findings
   change hot-spot reasons, never scores), so `-review-misses` may tolerate
   missing review recordings without changing a number.

### Matching

A ground-truth entry has a path, an inclusive line range on one side (new,
or old for `side = "old"`, used only for deleted code) and optional
`also_at` locations on the same side. A pipeline unit **overlaps** an entry
when the paths are equal and the unit's *changed span* on that side
intersects the entry's lines or one of its `also_at` ranges. The changed
span runs from the first to the last added line (new side) or deleted line
(old side); a unit with no changed line on that side uses its whole range
there. So a unit never matches through its context lines alone.

`soft` entries are judgment calls: they never count as misses, and flagging
them is never a false positive.

### Metrics

Every metric is a count over a denominator (`num/den`). When the
denominator is 0 the metric does not apply to the item (`—`, JSON `null`)
and is left out of the totals, which pool numerators and denominators over
the items where it applies.

| Key | Column | Definition | Better |
| --- | --- | --- | --- |
| `eyes_recall` | Eyes recall | firm `needs_eyes` regions overlapped by at least one unit with attention *eyes* ÷ firm `needs_eyes` regions | higher; **firm** |
| `eyes_precision` | Eyes precision | eyes units that overlap any `needs_eyes` entry (firm or soft) ÷ eyes units | higher |
| — | Headlines | rows in `ItemAgg.Headlines`; "(+n unsure)" is `ItemAgg.HeadlinesUnsure`, the candidates left out below the threshold (`ItemAgg.HeadlineCandidates`, at most one per unit, none for a listed unit) | few |
| `headline_precision` | Headline precision | headline rows that match a `headlines` entry (firm or soft) ÷ headline rows. A row matches an entry when its unit overlaps the entry **and its kind claims what the entry says**: `breaking` needs `breaking = true`; a contract kind (api shape, schema/migration, env/flag, event format, signature) needs the same `contract`; a weakened-test or pii row needs an entry without a contract change. A row matching no headline entry is a false positive, whether it overlaps a `not_headlines` entry, an entry with another claim, or nothing; matching a headline entry wins over overlapping a `not_headlines` one | higher |
| `headline_recall` | Headline recall (firm) | firm `headlines` entries matched (as above, kind included) by at least one headline row ÷ firm `headlines` entries; one row recalls only the entries whose claim it makes | higher; **firm** |
| `hot_spots_top3` | Hot spots top 3 | the pipeline's `HotSpots[0:3]` whose unit overlaps a ground-truth hot spot of rank ≤ 3 (firm or soft) ÷ min(3, number of rank ≤ 3 entries); hits capped at the denominator, so a slot spent elsewhere is a miss | higher; **firm** |
| `fidelity` | Fidelity bullets | description bullets whose pipeline status (implemented / not found) equals the truth (`partial` accepts either) ÷ truth bullets; bullets matched by whitespace-normalised text, else by position; a bullet the pipeline lacks is wrong | higher; **firm** |
| `undocumented_recall` | Undocumented recall (firm) | firm `fidelity.undocumented` changes overlapped by a unit in `ItemAgg.Undocumented` ÷ firm undocumented changes | higher; **firm** |
| `unsure_labels` | Unsure labels | `ItemAgg.Unsure` (the confidence footer's count) ÷ the labels that count can include: the unit-scope labels of the item's units for the six questions it reads (depth, kind, behaviour, breaking, risk, covered; `aggregate.UnsureQuestions`) | lower |
| `unsure_labels_all` | Unsure labels (all) | every unit-scope and line label of the item's units with confidence below 0.60 (unknown answers included) ÷ every such label: the spec's "count of labels with confidence < 0.60", which the baseline's footer count used. It reads the labels only, so aggregate rules cannot move it | lower |
| `unsure_units` | Unsure units | `ItemAgg.UnsureUnits` (units kept expanded, never skip) ÷ units | lower |
| `lines_to_read` | Lines to read | changed lines (adds + dels) of eyes units ÷ the item's changed lines (`Added + Removed`); the spec's "Done means" target is below 1/3 | lower |
| `must_read` | Eyes lines in must_read ± tol | 1/1 when those eyes lines fall within [floor(must_read·(1−tolerance)), ceil(must_read·(1+tolerance))] from `[lines_that_matter]` | higher |
| `branch_state` | Branch state | 1/1 when the branch block's state is `[branch].state` or in `state_ok` | higher; **firm** |
| `file_kind` | File kind | truth files whose dominant unit kind label (most changed lines; unknown kinds ignored; ties by name) is `kind` or in `also` ÷ truth files | higher |

The **firm** metrics are the accuracy checks the tuning rule protects ("no
item's firm metric worse than at baseline") together with the regression
guard.

## Reproduce

```
go run ./tools/quality                  # markdown scoreboard (offline, about 10 s)
go run ./tools/quality -json            # the same board as JSON
go run ./tools/quality -explain         # per-item listing: units, headlines, hot spots, misses
go run ./tools/quality -review-misses   # tolerate missing review recordings (no metric changes)
```

Flags `-groundtruth`, `-recordings` and `-fixtures` point at other
directories. `TestScoreboardRegression` in `internal/quality` runs the same
board and compares it with `internal/quality/testdata/thresholds.toml`.

## Baseline: before (main at 5b2c280 + M5.2 lead prep)

Recordings and code as committed at 5b2c280 plus the lead's M5.2 prep (the
#106 fixture was not recorded yet, so it has no row). The branch's fast
review was not recorded (`docs/BLOCKED.md`), so this run used
`-review-misses`; no metric depends on it.

Re-scored at the M5.2 review fixes: the current `internal/quality` (kind-aware
headline matching, the two unsure-label shares) run on the pre-tuning code and
recordings (ff024a9, a scratch worktree, five ground-truth files). Every other
column is the same as first measured. Headline precision fell from 20/118 to
13/118: seven of #101's rows sat on an entry's lines with another claim than
the entry's. The baseline has
no `unsure_labels` value: its footer counted every label below 0.60, which is
the `unsure_labels_all` share.

| Item | Headlines | Eyes recall | Eyes precision | Headline precision | Headline recall (firm) | Hot spots top 3 | Fidelity bullets | Undocumented recall (firm) | Unsure labels | Unsure labels (all) | Unsure units | Lines to read | Eyes lines in must_read ± tol | Branch state | File kind |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| pr:101 | 97 | 4/6 (0.67) | 8/30 (0.27) | 8/97 (0.08) | 2/2 (1.00) | 0/3 (0.00) | 6/6 (1.00) | 0/1 (0.00) | — | 287/3012 (0.10) | 48/48 (1.00) | 309/398 (0.78) | no: 309 in 65–135 | — | 14/14 (1.00) |
| pr:102 | 20 | — | — | 4/20 (0.20) | 1/1 (1.00) | 0/2 (0.00) | 2/2 (1.00) | — | — | 125/1386 (0.09) | 50/50 (1.00) | 0/112 (0.00) | no: 0 in 2–6 | — | 20/20 (1.00) |
| pr:103 | 1 | — | — | 1/1 (1.00) | — | — | 3/3 (1.00) | — | — | 14/147 (0.10) | 7/7 (1.00) | 0/78 (0.00) | yes: 0 in 0–0 | — | 3/3 (1.00) |
| pr:104 | 0 | 0/1 (0.00) | — | — | — | 1/2 (0.50) | 3/4 (0.75) | — | — | 17/264 (0.06) | 6/6 (1.00) | 0/46 (0.00) | no: 0 in 3–9 | — | 4/4 (1.00) |
| branch:05-spike-dark-mode | 0 | 1/1 (1.00) | 2/2 (1.00) | — | — | 1/3 (0.33) | — | — | — | 19/285 (0.07) | 5/5 (1.00) | 24/30 (0.80) | no: 24 in 7–21 | 1/1 (1.00) | 3/3 (1.00) |
| **totals** | 118 | 5/8 (0.62) | 10/32 (0.31) | 13/118 (0.11) | 3/3 (1.00) | 2/10 (0.20) | 14/15 (0.93) | 0/1 (0.00) | — | 462/5094 (0.09) | 116/116 (1.00) | 333/664 (0.50) | 1/5 (0.20) | 1/1 (1.00) | 44/44 (1.00) |

What the baseline shows (from `-explain`):

- **Headlines are not a summary.** #101 lists 97 rows (35 breaking, 50
  contract, 14 pii) for 2 firm headlines; #102 lists 20 for 1. Jev answers
  `breaking` yes on tests, import lines and caller updates, and on #101 it
  almost never picks contract `none` (P(none) 0.01–0.05 on every unit,
  including tests, moved code and React components).
- **Every unit is unsure.** Each unit carries about 25 labels, and some
  risk-tag sub-boolean or line label is always below 0.60, so 116 of 116
  units are kept expanded and can never be skip.
- **Lines to read** is 0.78 on #101 and 0.80 on the branch (target < 1/3):
  Jev answers depth `read` for most of #101, tests included.
- **Hot spots** hit 2 of 10 slots: the hot-spot score multiplies by
  (1 − covered), and Jev says the #101 cache hook and `getUser` are covered
  by the PR's tests (P .85–.90), so they score low.
- Misses on firm entries: #101's server `toResponse`/`Routes` regions (Jev
  depth `skim`), #101's ProfilePage load-error removal (not undocumented),
  #104's `package.json` (depth `skim`) and its zod bullet (no unit claims it).

## After (M5.2 final)

Code and recordings at the end of M5.2: the aggregate rules below, the
reworded `breaking`, `contract`, `depth` and `pii_or_secret` questions
(re-recorded), #106 recorded (embeddings, Jev, reviews), every review
recorded with the raised output caps. Strict run: no review miss.
`TestScoreboardRegression` guards these values
(`internal/quality/testdata/thresholds.toml`, tolerance 0.02). Re-scored at
the M5.2 review fixes with kind-aware headline matching and the two
unsure-label shares (the aggregate output is unchanged; the four "unsure"
headline candidates are the same).

| Item | Headlines | Eyes recall | Eyes precision | Headline precision | Headline recall (firm) | Hot spots top 3 | Fidelity bullets | Undocumented recall (firm) | Unsure labels | Unsure labels (all) | Unsure units | Lines to read | Eyes lines in must_read ± tol | Branch state | File kind |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| pr:101 | 6 (+3 unsure) | 6/6 (1.00) | 11/26 (0.42) | 3/6 (0.50) | 2/2 (1.00) | 0/3 (0.00) | 6/6 (1.00) | 0/1 (0.00) | 52/288 (0.18) | 231/3012 (0.08) | 37/48 (0.77) | 221/398 (0.56) | no: 221 in 65–135 | — | 14/14 (1.00) |
| pr:102 | 3 | — | 2/3 (0.67) | 2/3 (0.67) | 1/1 (1.00) | 0/2 (0.00) | 2/2 (1.00) | — | 45/300 (0.15) | 110/1386 (0.08) | 45/50 (0.90) | 6/112 (0.05) | yes: 6 in 2–6 | — | 20/20 (1.00) |
| pr:103 | 1 | — | 0/1 (0.00) | 0/1 (0.00) | — | — | 3/3 (1.00) | — | 2/42 (0.05) | 13/147 (0.09) | 2/7 (0.29) | 20/78 (0.26) | no: 20 in 0–0 | — | 3/3 (1.00) |
| pr:104 | 0 | 0/1 (0.00) | — | — | — | 1/2 (0.50) | 3/4 (0.75) | — | 3/36 (0.08) | 17/264 (0.06) | 3/6 (0.50) | 0/46 (0.00) | no: 0 in 3–9 | — | 4/4 (1.00) |
| pr:106 | 5 (+1 unsure) | 5/5 (1.00) | 8/10 (0.80) | 3/5 (0.60) | 3/7 (0.43) | 0/3 (0.00) | 5/5 (1.00) | — | 15/150 (0.10) | 138/1737 (0.08) | 12/25 (0.48) | 109/212 (0.51) | no: 109 in 45–95 | — | 9/9 (1.00) |
| branch:05-spike-dark-mode | 0 | 1/1 (1.00) | 1/1 (1.00) | — | — | 2/3 (0.67) | — | — | 3/30 (0.10) | 18/285 (0.06) | 3/5 (0.60) | 15/30 (0.50) | yes: 15 in 7–21 | 1/1 (1.00) | 3/3 (1.00) |
| **totals** | 15 (+4 unsure) | 12/13 (0.92) | 22/41 (0.54) | 8/15 (0.53) | 6/10 (0.60) | 3/13 (0.23) | 19/20 (0.95) | 0/1 (0.00) | 120/846 (0.14) | 527/6831 (0.08) | 102/141 (0.72) | 371/876 (0.42) | 2/6 (0.33) | 1/1 (1.00) | 53/53 (1.00) |

### Before → after, per item

The five items of the baseline (#106 has no "before": it was not recorded).
Firm metrics in bold; none is worse than at baseline. Unsure labels are
compared as the all-labels share, the one definition both runs have; the
footer's own share (`unsure_labels`) is 105/696 (0.15) over these five items
after M5.2 and has no baseline value.

| Item | Headlines | **Eyes recall** | Eyes precision | Headline precision | **Headline recall** | **Hot spots** | **Fidelity** | Unsure labels (all) | Unsure units | Lines to read | must_read |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| pr:101 | 97 → 6 | 4/6 → 6/6 | 0.27 → 0.42 | 0.08 → 0.50 | 2/2 → 2/2 | 0/3 → 0/3 | 6/6 → 6/6 | 0.10 → 0.08 | 1.00 → 0.77 | 0.78 → 0.56 | no → no |
| pr:102 | 20 → 3 | — | — → 0.67 | 0.20 → 0.67 | 1/1 → 1/1 | 0/2 → 0/2 | 2/2 → 2/2 | 0.09 → 0.08 | 1.00 → 0.90 | 0.00 → 0.05 | no → yes |
| pr:103 | 1 → 1 | — | — → 0.00 | 1.00 → 0.00 | — | — | 3/3 → 3/3 | 0.10 → 0.09 | 1.00 → 0.29 | 0.00 → 0.26 | yes → no |
| pr:104 | 0 → 0 | 0/1 → 0/1 | — | — | — | 1/2 → 1/2 | 3/4 → 3/4 | 0.06 → 0.06 | 1.00 → 0.50 | 0.00 → 0.00 | no → no |
| branch | 0 → 0 | 1/1 → 1/1 | 1.00 → 1.00 | — | — | 1/3 → 2/3 | — | 0.07 → 0.06 | 1.00 → 0.60 | 0.80 → 0.50 | no → yes |
| **five items** | 118 → 10 | 5/8 → 7/8 | 0.31 → 0.45 | 0.11 → 0.50 | 3/3 → 3/3 | 2/10 → 3/10 | 14/15 → 14/15 | 0.09 → 0.08 | 1.00 → 0.78 | 0.50 → 0.39 | 1/5 → 2/5 |

Undocumented recall (0/1, #101), branch state (1/1) and file kind (all
files) did not change. The all-labels unsure share reads only Jev's answers,
so its small drop comes from the re-recorded questions (step 6), not from an
aggregate rule. #103's single row went from a correct removed-export row to a
`breaking` claim the truth rejects (see "Still short of target").

### What each tuning step changed

Measured with `go run ./tools/quality -review-misses` after each change
(reviews feed no metric; the strict run above is the same board). Steps 1–5
ran on the baseline Jev answers over the five baseline items; step 6
re-recorded the unit and line questions and added #106. The headline
precision and recall figures in this table were measured with the
location-only matching of the time (a row matched any entry it overlapped,
whatever its kind); the row counts are unaffected, and the boards above are
re-scored with kind-aware matching.

| Step | Rule | Effect |
| --- | --- | --- |
| 1 | (b) "Unsure" over depth, kind, behaviour, breaking, risk and covered only | unsure units 1.00 → 0.96. The footer's count then covers those six questions only (at most 6 per unit), so its share of all labels fell 0.09 → 0.03 by definition; it is now divided by those questions' labels (`unsure_labels`), and the all-labels share is a separate column |
| 2 | (c) test units cap at skim; unknown depth needs eyes only when risk ≥ 3 or behaviour changes (none on the fixtures) | #101 eyes precision 0.27 → 0.30, lines to read 0.78 → 0.57; totals 0.50 → 0.38 |
| 3 | (b) decision confidence: depth by its attention bucket's mass, risk by the threshold sides at 3 and 4 | unsure units 0.96 → 0.77; MinConfidence feeds hot spots: branch 1/3 → 2/3 |
| 4 | (a) headlines only at P ≥ 0.60 of the claim's own question, [0.5, 0.6) counted as unsure; no breaking or contract headline on tests, noise or dependency units | #101 97 → 60 (+27 unsure), #102 20 → 10, #103 1 → 0 |
| 5 | (a) a pii line headlines only in a unit that changes behaviour | #102 10 → 6 (precision 0.50) |
| 6 | Reworded `breaking`, `contract`, `depth`, `pii_or_secret`; Jev re-recorded (330 calls, $0.0333); #106 recorded | #101 60 → 31, no pii rows left on #101; branch lines 0.80 → 0.50; #106 recall 7/7 |
| 7 | (a) one row per unit: a breaking row subsumes the unit's contract claim; a non-breaking `signature` change is additive, no headline | #101 31 → 8 (precision 0.62), #102 6 → 3, #106 18 → 5 (precision 1.00, recall 7/7 → 6/7: its flag headline had matched a `signature` row on the same unit) |
| 8 | (a) external contract rows (API shape, schema, env/flag, event format) one per file and kind | #101 8 → 6 (precision 0.62 → 0.50: two duplicate correct rows merged) |
| 9 | (c) a unit headlined as breaking or as an external contract change needs eyes | eyes recall #101 4/6 → 6/6, #106 3/5 → 5/5; lines to read 0.34 → 0.42 (totals), #103 and #106 leave their must_read range |

### Still short of target

- **Lines to read** stays above the spec's 1/3 on #101 (0.56), #106 (0.51)
  and the branch (0.50); 0.42 over all items. Jev answers depth `read` for
  most of #101's feature code, and step 9 deliberately adds the units the
  Overview headlines. The ground truth's must_read for #101 is 100 lines;
  the pipeline asks for 221.
- **Hot spots** hit 3 of 13 top-three slots. The spec's formula multiplies
  by (1 − covered), and Jev says the code #101's tests touch is covered
  (P .85–.91), so the cache hook and `getUser` rank low; the formula stays
  as specified.
- **Headlines** on #101 are 6 rows, 3 of them false (an additive barrel
  export, a deleted closing brace and a moved helper that Jev still calls
  breaking at .75–.90). Matched by kind, #106 lists 2 of its 5 rows with the
  wrong claim: the migration (P .71) and the new preferences routes in
  `users.Routes` (P .62) as `breaking`, which the truth calls additive
  schema and API changes. Its `main.go` breaking row (P .85) recalls only
  the removed `/api/v1/health`, so the two new environment variables are
  missed, and so is the feature flag: Jev picks `signature` for the
  SettingsForm unit (its optional prop), and flags.ts's `env/flag` answer
  is .53 (now a "?" row in the Headlines list). Recall 3/7. #103's removed
  export is listed as `breaking` (P .75); nothing calls it, so the truth
  accepts it only as a non-breaking removed export, which is no headline
  since step 7.
- **Breaking answers on tests** (#106's `users_test.go`, #102's
  `client.test.ts`) no longer reach the urgency "breaking" term, which
  counts breaking headline rows; they still mark their group breaking for
  routing and the callers context.
- **Unsure units** are 0.72 over all items: breaking and covered answers
  near 0.5 keep many units unsure.
- **Undocumented recall** 0/1: #101's removed load-error branch is claimed
  by the bullet "ProfilePage uses the cache and shows save errors".
- **#104**: `package.json` is not marked eyes (depth `skim` .75) and the zod
  bullet is claimed by no unit.
