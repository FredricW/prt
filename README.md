# prt — PR triage in your terminal

prt ranks the PRs and branches that need you and splits each diff into
labelled, grouped change units, so you read the 40 lines that decide whether a
PR is safe instead of all 400. Judgments come from Jev, grouping from a local
embedding model, reviews from the cheapest LLM tier that can do the job, and
every number on screen traces back to a label, a rule or a git fact.

> Status: early development. M0 (a fast PR browser) and M1 (change units and
> Jev labels: Overview page, labelled Code page), M2 (grouped change units,
> Code page by change) and M3 (routed fast/deep LLM reviews, TL;DR, urgency
> ranking, Pipeline page) and M4 (review comments, AI drafts, review
> submission, create PR, "Since you looked") and M5 (search-and-add,
> command line, branch states, Architecture tab, side-by-side diffs, syntax
> colour, the design canvas look) and M5.1 (auto-review strategy, stop and
> resume, settings page, command-line completion) and M5.2 (measured label
> quality, instant open, capped Overview blocks, API / Database / Flags change
> blocks) and M5.3 (multi-repo workspaces, resizable sidebars) are done; M6
> (overlap view) is not started. See
> [`docs/spec.md`](docs/spec.md) for the design and
> [`CHANGELOG.md`](CHANGELOG.md) for what works today.

## Install

Prerequisites:

- Go 1.26 or newer
- A C toolchain (Xcode command line tools, `build-essential`, …): tree-sitter
  grammars need cgo
- [`gh`](https://cli.github.com/), logged in (`gh auth login`); prt reuses its token
- git
- Optional: [Ollama](https://ollama.com/) for local embeddings
  (`ollama pull embeddinggemma-2`)

```sh
CGO_ENABLED=1 go install github.com/fredricw/prt/cmd/prt@latest
```

Or from a checkout: `make build` (binary in `bin/prt`).

Optional environment (prt degrades gracefully without any of them):

| Variable | Used for |
| --- | --- |
| `JEV_API_KEY` | Jev labels via Vercel AI Gateway (an AI Gateway API key) |
| `JEV_BASE_URL` | override the gateway URL (default `https://ai-gateway.vercel.sh`) |
| `PRT_ANTHROPIC_API_KEY` | fast/deep review tiers (Anthropic) |
| `OLLAMA_HOST` | Ollama server (default `http://127.0.0.1:11434`) |

Secrets are only read from the environment and `gh`, never from config files.

## 30-second usage

```sh
cd your-repo
prt                 # picker on the left, the selected PR/branch on the right

prt --root ~/code   # every git repository under ~/code in one picker
```

- **Several repos at once.** `prt --root DIR`, or `prt` started in a
  directory that is not inside a git repository, finds every repository
  under it (two levels deep; `--depth N`, or `[workspace] depth` and
  `exclude = ["archive/**"]` in the global `$XDG_CONFIG_HOME/prt/prt.toml`;
  `node_modules`, `vendor` and dot directories are skipped) and shows them
  in one picker, grouped by repo: each header shows the repo, its item count
  and whether it is on GitHub. `z` folds a repo, `Z` folds all, `F` (or
  `:repo <name>`, `:repo all`) shows one repo. Urgency bars are on one scale
  across repos. Each repo keeps its own `.git/prt` store, log and config, so
  its own prt.toml (role, owned paths) applies to it; the header shows what
  the session has spent across repos. `/` and `:add` take repo-qualified
  refs: `acme/billing-svc#7`, `billing-svc#7`, `billing-svc:feat/x`.
- **Resize the sidebars.** Drag the picker's right border (or the Code
  page list's) with the mouse, or press `<` / `>` to move the focused pane's
  border. Widths are remembered in `$XDG_STATE_HOME/prt/ui.json`.

- `j`/`k` move through the picker, `enter` focuses the diff, `esc` goes back.
- `1` overview, `2` code; `]`/`[` jump between files in the diff.
- The Overview is a summary: every block shows a few rows and "+N more".
  `J`/`K` (or `]`/`[`) focus a block, `enter`/`space` (or a click on
  "+N more") open its full list, `enter` there jumps to the unit and `esc`
  closes. Headlines list only claims with P ≥ .60; "+N below .60 (unsure)"
  opens the full list, where the weaker claims show as `?` rows.
  The API endpoints, Database and Feature flags & config blocks list routes,
  schema changes, flags, env vars and config keys added, changed or removed.
  An item you opened before shows at once; while it is re-analysed the old
  analysis stays up with `↻ updating review · <stage>`.
- `o` opens the PR on GitHub, `c` checks it out (asks first if your tree is dirty).
- On the Code page: `n`/`p` next/previous unit, `J`/`K` next/previous file,
  `f` opens the filters bar (then `1`–`9`, `0` toggle filters; needs eyes,
  behaviour and mine are on by default), `z` expands collapsed noise, `L`
  shows the raw Jev labels of the current unit.
- `g` on the Code page switches between by file and by change: groups of
  units that belong together (calls, tests, moves, Jev-confirmed pairs) in
  review order, with a "why these belong together" list; a mechanical change
  shows one example and "×N verified identical". `M` on two groups merges
  them, `X` splits the group at the cursor; prt remembers your edits.
- Keys are rebindable under `[keys]` in `prt.toml` (actions as in the
  spec's keyboard table, plus `mode`, `merge_groups`, `split_group`, `stop`,
  `stop_all`, `resume`, `settings`; write the comma key as `comma`).
- Labels need `JEV_API_KEY`. Without it (or when Jev is unreachable) items
  show `labels unavailable`: the Overview shows git facts only and the Code
  page shows every unit with its diff, filters off.
- Each group is routed by the rules in `prt.routing.toml` to no review, the
  fast model or the deep model (needs `PRT_ANTHROPIC_API_KEY`; without it
  reviews are skipped with a note). Findings appear on the Code page under
  the line they are about; `R` re-reviews a group on the deep model.
- `P` opens the Pipeline page: stage timings, the routing table (why each
  group got its tier, cost, findings, escalations), totals against the
  per-item cost cap, and the rules. There `D` forces a group deep, `s` skips
  it, `r` re-runs, `e` edits the rules in `$EDITOR`, `c` shows the raw LLM
  calls and `$` raises the cost cap for this item. `w` on the Overview shows
  how the urgency score was computed.
- Review comments on the Code page: `C` comments on the line under the
  cursor, `V` selects a range of lines (then `C`), `ctrl-s` saves the draft
  (`esc` discards; quitting with a composer open saves it). AI findings show
  as dimmed drafts: `a` accepts one, `d` dismisses it, `!` dismisses it and
  marks a label wrong. `t` opens a GitHub thread in the side panel (type a
  reply and `ctrl-s` sends it; `ctrl-r` resolves). `tab` reaches the Comments strip at the bottom, which
  lists every draft and thread (with the strip focused, `a`/`d` act on the
  selected AI finding). `S` submits your drafts as one review (`a`
  approve, `A` request changes preselect the verdict); on a branch without
  a PR, `S` opens the create-PR form instead and posts the branch's drafts
  on the new PR (`a` and `A` need a PR). Push the branch first: prt refuses
  to open a PR while it has commits that are not on its upstream. The Overview's "Since you looked" lists what changed since
  your last review or visit and which of your comments look addressed.
- `/` searches local branches and open PRs (closed and merged ones with a
  leading `!`, or when nothing open matches): `enter` adds the result to the
  Added section (or jumps to it when it is already listed), `tab` previews
  its Overview without adding it; closing the prompt drops the preview. `x`
  removes an added item; `space` marks items for the overlap view (M6).
- `:` opens the command line (`tab` completes command names and arguments,
  pressing it again cycles; an unknown command prints a one-line hint):

  | Command | Does |
  | --- | --- |
  | `:add #482` / `:add feat/x` | add a PR or branch and analyse it now |
  | `:add acme/api#12` / `:add api:feat/x` | in a workspace: add from one repo |
  | `:repo <name>` / `:repo all` | in a workspace: show one repo / every repo |
  | `:auto manual\|needs-you\|needs-you+yours\|all` | set the auto-review strategy (saved to `.git/prt/prt.toml`) |
  | `:tier deep\|none\|rules` | set the review tier of the focused group (or every group) |
  | `:cap 1.00` / `:cap default` | set the selected item's review cost cap |
  | `:stop` / `:stop all` | stop the selected item's analysis / every analysis |
  | `:resume` / `:resume all` | resume the selected item / every stopped item |
  | `:settings` | open the settings page (also `,`) |
  | `:q` | quit (drafts are saved) |
- `3` opens the Architecture tab: the module graph around the diff (`h`/`j`/
  `k`/`l` move, `+`/`-` depth, `d` changed modules only, `enter` lists a
  module's files and jumps to one), with the contracts it crosses and its
  net effect.
- On the Code page `u` toggles side-by-side (needs a terminal of at least
  140 columns; narrower ones show unified), `e` opens the line in
  `$EDITOR` and `o` opens the unit on GitHub.
- Branches show their state in the picker and on the Overview: `WIP`,
  `ready for PR`, `spike`, `superseded by #N` (from Jev, with its
  probability) or `abandoned?` after 14 days without commits
  (`pipeline.abandoned_days`), plus unpushed commits and a suggested PR title.
- `r` re-runs analysis for the selected item; `?` shows every key and
  command; `q` quits.
- **Auto-review strategy.** Model calls (Jev labels, embeddings, LLM reviews,
  TL;DR, branch state and PR title) cost money, so prt only spends on its own
  for the items `[pipeline] auto_review` in `prt.toml` covers:

  | Strategy | Analysed without asking |
  | --- | --- |
  | `manual` | nothing |
  | `needs-you` (default) | PRs in Needs you |
  | `needs-you+yours` | PRs in Needs you or Yours |
  | `all` | everything, including branches |

  Everything else still gets the free stages (fetch, parse, structural
  links, the plain diff) and shows `not analysed · r to run`. Branches are
  never auto-reviewed unless the strategy is `all`. Asking explicitly always
  runs: `r`, `:resume`, adding with `/` or `:add`. Change it with `:auto` or
  on the settings page; it applies at once, even to queued work.
- **Stop and resume.** `ctrl+x` stops the selected item's analysis, `ctrl+k`
  stops all, `ctrl+o` (or `r` on a stopped item) resumes. A stopped item
  shows `stopped at <stage> · r to resume`; in-flight reviews show as
  stopped, not failed. Resume picks up from the last finished stage: work
  already done is read from the content-hash caches, so nothing is paid
  twice. Works in-process and through the daemon.
- **Settings page** (`,` or `:settings`): auto-review strategy, your role
  and owned globs, cost cap and deep slots, fast/deep models and embedder,
  urgency weights and thresholds. It saves to `.git/prt/prt.toml` (default)
  or the global `$XDG_CONFIG_HOME/prt/prt.toml` (only the fields you
  changed), keeping your comments and
  unrelated keys, and applies the change without a restart (re-ranking or
  re-routing as needed). It shows whether `JEV_API_KEY` and
  `PRT_ANTHROPIC_API_KEY` are set (never their values), whether Ollama is
  reachable and whether `gh` is logged in, and opens `prt.routing.toml` or
  `prt.questions.toml` in `$EDITOR` (question changes apply at the next
  daemon start). `tab`/`enter` move between fields, `pgdown`/`pgup` between
  sections, `esc` cancels.
- `prt daemon` keeps analysis warm in the background (one per repo, or
  `prt daemon --root DIR` for a workspace: one daemon, one worker pool and
  one cost ledger for every repo under DIR); `prt` connects to it
  automatically (inside a repo a workspace daemon serves, to that daemon),
  or runs everything in-process with `--no-daemon`. Only one process
  analyses a repo at a time; a second one that cannot attach says which
  process holds the repo.
- `prt --offline` never touches the network; `prt --snapshot` prints a few
  screens to stdout without a terminal.

Try it on the bundled fixtures, fully offline:

```sh
make demo       # interactive
make snapshot   # non-interactive

make demo-workspace       # two repos (portal and billing-svc) in one picker
make snapshot-workspace
```

Both run with `--offline` and replay the answers recorded in
`testdata/recordings` (`--jev-replay`, `--embed-replay`, `--review-replay`),
so no model is called but the fixture PRs show real labels, groups, reviews
and TL;DRs. They also pass `--drafts testdata/fixtures/drafts.json`, which
seeds a few local draft comments (drafts you already have are kept). Every
fixture item, the local branch included, has recorded answers, and every
routed group fits the default $0.40 per-item cost cap. Both pin the clock to
the recording time (`--now 2026-10-13T12:00:00Z`), because the branch-state
question's recorded request includes the branch's idle days; ages on screen
are counted from that time. `make demo` runs under
the default `needs-you` strategy: PRs in Needs you are analysed
automatically, while #102 (Yours), #106 (Involved) and the fixture branch
show `not analysed` until you press `r`.
`make snapshot` renders through a synchronous run that ignores the strategy,
so it analyses every item. In this replay mode only `.git/prt/` is read, so
the settings page saves to the repo scope only, and its GitHub line says the
client is the fixture (gh auth is not checked). The workspace targets use
`testdata/fixture-workspace` (built by `make fixtures`) with
`--fixtures testdata/fixtures-workspace/manifest.json`, which gives each
repo its own GitHub fixture; billing-svc has no recordings, so its items show
`labels unavailable`.

Configuration is optional: `prt.toml`, `prt.routing.toml` and
`prt.questions.toml` are looked up in `.git/prt/`, then `$XDG_CONFIG_HOME/prt/`.
See the spec's Configuration section for every key. `[workspace]` (`depth`,
`exclude`) is read from the global file only.

## Development

```sh
make build      # bin/prt
make test       # go test ./...
make lint       # golangci-lint v2
make check      # vet + test + lint; must pass before every commit
make fixtures   # regenerate testdata/fixture-repo and testdata/fixture-workspace
make quality    # label-quality scoreboard against testdata/groundtruth (docs/QUALITY.md)
make record-jev-dry  # count the Jev requests a recording would send (free)
make record-jev      # record Jev answers for the fixtures (JEV_API_KEY, $1 cap)
make record-review-dry  # count the review requests and their upper-bound cost (free)
make record-review      # record reviews for the fixtures (PRT_ANTHROPIC_API_KEY, $3 cap)
make record-embed    # record Ollama embeddings for the fixtures (local)
```

Layout and interfaces: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).
Design decisions: [`docs/DECISIONS.md`](docs/DECISIONS.md).

## Licence

MIT, see [LICENSE](LICENSE).
