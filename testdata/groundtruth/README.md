# Ground truth for the fixture PRs

Hand-written reference answers for prt's label-quality check. One TOML file
per fixture item:

| File | Item | Patch |
| --- | --- | --- |
| `101.toml` | PR #101 `feat(web): cached user profiles with team and avatar` | `fixtures/prs/01-user-profile-refactor.patch` |
| `102.toml` | PR #102 `refactor(web): rename fetchJSON to requestJSON` | `fixtures/prs/02-rename-fetchjson.patch` |
| `103.toml` | PR #103 `chore(billing): remove legacy finance export and dead helpers` | `fixtures/prs/03-remove-legacy-billing.patch` |
| `104.toml` | PR #104 `build(deps): bump the production group with 4 updates` | `fixtures/prs/04-bump-deps.patch` |
| `106.toml` | PR #106 `feat(server): user preferences API and ops health check` | `fixtures/prs/06-user-preferences-api.patch` |
| `05-spike-dark-mode.toml` | branch `05-spike-dark-mode` (no PR) | `fixtures/prs/05-spike-dark-mode.patch` |

The label-quality check that consumes them is `internal/quality` (`go run
./tools/quality`); `docs/QUALITY.md` defines its metrics and keeps the
scoreboards.

## Written blind

These files were written on 2026-10-13 by reading only the fixture sources
(`106.toml` in M5.2, before any Jev answer, embedding or review of #106 was
recorded):

- `testdata/fixtures/prs/*.patch` (the diffs),
- `testdata/fixtures/base/` (the tree the patches apply to, used to check
  callers, imports and dead code),
- `testdata/fixtures/prs.json` (PR bodies, CI state, review threads).

Nothing under `testdata/recordings/`, `internal/aggregate`, snapshot output or
any other pipeline output was opened while writing them. The point is a
reference that does not inherit the pipeline's mistakes. If the pipeline and
this file disagree, look at the diff before assuming either one is right, and
fix whichever is wrong in its own commit.

The answers are what a careful senior reviewer who owns `web/**` (the fixture
repo's `prt.toml` role) would conclude after reading the diff and the base
tree. Line numbers were counted from the hunk headers by hand.

## Conventions

- **Lines** are `[first, last]`, inclusive, on the **new** side of the diff,
  unless the entry says `side = "old"` (used only for deleted code, where no
  new side exists).
- **Paths** are repo-relative, as they appear in the patch (`web/src/...`).
- **Vocabularies** follow `docs/spec.md`, "Jev question catalogue", so a
  checker can compare labels directly:
  - `kind`: feature, bugfix, refactor, perf, test, config, docs, dependency,
    generated, formatting
  - `contract`: none, signature, api shape, schema/migration, env/flag, event
    format
  - `depth`: skip, skim, read, run locally
  - `fail_mode`: crash, wrong data, security, slowdown, cosmetic
  - `risk_tags`: auth, async/race, error path, data write, money, pii,
    io-in-loop, date/tz
  - `shape`: single-purpose, mixed, sweeping refactor, WIP
  - branch `state`: WIP, ready for PR, spike, abandoned, superseded
  - file `role`: core, glue, ui, test, config, generated
- **`certainty`** on an entry is `firm` or `soft`:
  - `firm`: a correct pipeline must agree. Missing a firm needs-eyes region,
    headline or hot spot is a miss.
  - `soft`: a judgment call. Flagging it or not are both acceptable; it is
    never counted as a miss, and flagging it is never a false positive.

## File format

```toml
item = "pr:101"            # "pr:<n>" or "branch:<name>"
title = "..."
patch = "testdata/fixtures/prs/01-user-profile-refactor.patch"
written_blind = true

[summary]
shape = "mixed"            # expected item `shape`
shape_ok = ["mixed"]       # every acceptable answer
risk_max = 4               # expected max unit risk, 1-5
risk_band = "HIGH"         # HIGH >= 4, MED 3, LOW <= 2 (spec Overview block 5)
risk_band_ok = ["HIGH"]
breaking = 1               # count of units that break existing callers
one_line = "..."           # a reviewer's TL;DR, for eyeballing the AI one

[lines_that_matter]
total_changed = 398        # additions + deletions in the patch
must_read = 100            # changed lines a reviewer must actually read (eyes)
skim = 260                 # changed lines worth a glance (tests, UI, caller updates)
skip = 38                  # verbatim moves, imports, lockfiles, mechanical repeats
tolerance = 0.35           # relative tolerance on must_read
est_minutes = 76           # spec formula: eyes 0.5/line, skim 0.1/line

[[needs_eyes]]             # regions to read; everything not listed is skim or skip
path = "web/src/hooks/useProfileCache.ts"
lines = [60, 81]
symbol = "useProfileCache"
depth = "read"
certainty = "firm"
reason = "..."

[[headlines]]              # contract-level items (spec Overview block 3)
path = "..."
lines = [66, 72]
symbol = "getUser"
contract = "signature"
breaking = true
certainty = "firm"
summary = "..."

[[not_headlines]]          # tempting false positives; flagging them is wrong
path = "..."
lines = [3, 12]
symbol = "ApiError"
why = "..."

[[hot_spots]]              # top real risks, most important first
rank = 1
path = "..."
lines = [67, 76]
symbol = "useProfileCache"
risk = 4
fail_mode = "wrong data"
risk_tags = ["async/race"]
certainty = "firm"
reason = "stale response overwrites profile"   # about five words, like the UI
detail = "..."

[fidelity]
applicable = true          # false for branches without a description
bullets_total = 6
implemented = 5
notes = "..."

[[fidelity.bullets]]
text = "Invalidate the profile cache on logout"
status = "not implemented" # implemented | partial | not implemented
evidence = "..."

[[fidelity.undocumented]]  # behaviour changes no bullet covers
path = "..."
lines = [12, 19]
symbol = "ProfilePage"
certainty = "firm"
what = "..."

[[files]]                  # expected file-level kind and role
path = "..."
kind = "feature"           # dominant kind by changed lines
also = ["refactor"]        # other kinds present in the file; also acceptable
role = "core"
```

Optional fields and sections:

| Name | Where | Meaning |
| --- | --- | --- |
| `side = "old"` | any located entry | `lines` are old-side (deleted code only) |
| `also_at` | any located entry | other `"path:first-last"` locations that count as the same finding |
| `fail_mode_ok` | `[[hot_spots]]` | every acceptable `fail_mode` when the choice is ambiguous |
| `breaking_ok` | `[summary]` | every acceptable breaking count |
| `needs_eyes_max` | `[summary]` | most needs-eyes units a correct pipeline should report |
| `undocumented_firm` | `[fidelity]` | count of `firm` undocumented changes |
| `[[coverage]]` | item | expected `covered` answer for a risky unit (`covered`, `by`) |
| `[[comments]]` | item | expected `comment_addressed` answer per review thread in `prs.json` |
| `[branch]` | branch items | expected branch `state`, `state_ok`, and a title it should resemble |

A missing section means "none expected": no `[[headlines]]` means the
Overview should say "no contract changes", and no `[[hot_spots]]` means
nothing should be reported with risk 3 or higher.

## Suggested scoring

The check that consumes these files decides its own thresholds; this is the
intent behind each section.

- **needs_eyes**: a pipeline unit matches an entry when the path is equal and
  the new-side ranges overlap. Recall is matched `firm` entries over all
  `firm` entries. Precision is pipeline needs-eyes units that overlap any
  entry (firm or soft) over all pipeline needs-eyes units. Low precision is
  the "everything needs eyes" failure; low recall is the dangerous one.
- **headlines**: every `firm` entry must appear; any pipeline headline that
  overlaps a `not_headlines` entry, or matches nothing, is a false positive.
  Headlines are expected to be few; an empty list is a valid answer.
- **hot_spots**: the pipeline's top three should overlap the `rank <= 3`
  entries. A top-three slot spent on something not listed here is a miss.
- **fidelity**: per bullet, the pipeline's implemented / not found must equal
  `status` (`partial` accepts either). Undocumented changes are matched like
  needs_eyes.
- **files**: the pipeline's dominant kind for the file must be `kind` or in
  `also`.
- **lines_that_matter**: the pipeline's eyes lines should fall within
  `must_read +/- tolerance`. Its "~N lines that matter" (effective lines,
  noise excluded) should sit between `must_read` and `must_read + skim`.

## Judgment calls

- Tests are `skim`, never `needs_eyes`, even when they are the only evidence of
  intent; gaps in tests are reported as hot spots on the code they fail to
  cover.
- A verbatim move (PR #101's `ApiError` and `authHeader` into `api/errors.ts`)
  is `skip`; the moved-and-reshaped error parsing (`readError`) is `skim`.
- Removed exports with no callers (PR #103) are a `soft` headline with
  `breaking = false`: the spec lists removed exports as contract changes, but
  nothing breaks.
- Dependency bumps (PR #104) are not headlines; the red CI on an approved PR
  is the hot spot.
- PR #102 colliding with PR #101 on the same lines is a real merge risk, but it
  is cross-PR knowledge the per-item pipeline cannot see, so it is `soft`.
