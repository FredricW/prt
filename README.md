# prt — PR triage in your terminal

prt ranks the PRs and branches that need you and splits each diff into
labelled, grouped change units, so you read the 40 lines that decide whether a
PR is safe instead of all 400. Judgments come from Jev, grouping from a local
embedding model, reviews from the cheapest LLM tier that can do the job, and
every number on screen traces back to a label, a rule or a git fact.

> Status: early development (milestone M0 in progress). See
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
- `r` re-runs analysis for the selected item; `q` quits.
- `prt daemon` keeps analysis warm in the background (one per repo); `prt`
  connects to it automatically, or runs everything in-process with `--no-daemon`.
- `prt --offline` never touches the network; `prt --snapshot` prints a few
  screens to stdout without a terminal.

Try it on the bundled fixtures, fully offline:

```sh
make demo       # interactive
make snapshot   # non-interactive
```

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
