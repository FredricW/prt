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
> submission, create PR, "Since you looked") are done. See
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
```

- `j`/`k` move through the picker, `enter` focuses the diff, `esc` goes back.
- `1` overview, `2` code; `]`/`[` jump between files in the diff.
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
  spec's keyboard table, plus `mode`, `merge_groups`, `split_group`).
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
  lists every draft and thread. `S` submits your drafts as one review (`a`
  approve, `A` request changes preselect the verdict); on a branch without
  a PR, `S` opens the create-PR form instead and posts the branch's drafts
  on the new PR. The Overview's "Since you looked" lists what changed since
  your last review or visit and which of your comments look addressed.
- `r` re-runs analysis for the selected item; `?` shows every key; `q` quits.
- `prt daemon` keeps analysis warm in the background (one per repo); `prt`
  connects to it automatically, or runs everything in-process with `--no-daemon`.
- `prt --offline` never touches the network; `prt --snapshot` prints a few
  screens to stdout without a terminal.

Try it on the bundled fixtures, fully offline:

```sh
make demo       # interactive
make snapshot   # non-interactive
```

Both run with `--offline` and replay the answers recorded in
`testdata/recordings` (`--jev-replay`, `--embed-replay`, `--review-replay`),
so no model is called but the fixture PRs show real labels, groups, reviews
and TL;DRs. They also pass `--drafts testdata/fixtures/drafts.json`, which
seeds a few local draft comments (drafts you already have are kept). Anything not recorded (the local branch) shows the `labels
unavailable` mode; most deep groups of the larger PRs stay queued over the
default $0.40 per-item cost cap.

Configuration is optional: `prt.toml`, `prt.routing.toml` and
`prt.questions.toml` are looked up in `.git/prt/`, then `$XDG_CONFIG_HOME/prt/`.
See the spec's Configuration section for every key.

## Development

```sh
make build      # bin/prt
make test       # go test ./...
make lint       # golangci-lint v2
make check      # vet + test + lint; must pass before every commit
make fixtures   # regenerate testdata/fixture-repo
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
