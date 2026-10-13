# prt — PR triage in your terminal

prt ranks the PRs and branches that need you and splits each diff into
labelled, grouped change units, so you read the 40 lines that decide whether a
PR is safe instead of all 400. Judgments come from Jev, grouping from a local
embedding model, reviews from the cheapest LLM tier that can do the job, and
every number on screen traces back to a label, a rule or a git fact.

> Status: early development. M0 (a fast PR browser) and M1 (change units and
> Jev labels: Overview page, labelled Code page) are done; grouping comes in
> M2. See
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
- Labels need `JEV_API_KEY`. Without it (or when Jev is unreachable) items
  show `labels unavailable`: the Overview shows git facts only and the Code
  page shows every unit with its diff, filters off.
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

Both run with `--offline`, so Jev is never called and every fixture item
shows the `labels unavailable` mode (git facts, filters off). To see label
chips, gutter marks, filters and the full Overview, run `prt` with
`JEV_API_KEY` set.

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
```

Layout and interfaces: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).
Design decisions: [`docs/DECISIONS.md`](docs/DECISIONS.md).

## Licence

MIT, see [LICENSE](LICENSE).
