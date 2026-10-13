# Recordings

Recorded responses from real external services, replayed by tests so nothing
touches the network. They are made by running the real pipeline over the
fixture PRs with `tools/recordjev` (DECISIONS "M1 Jev recordings flow"), so
request bytes match the replay by construction.

- `jev/<key>.json` (M1, M2) — `jev.Recording` (request + response); `<key>` is
  `jev.RequestKey(request)`, so a recording is found again only if the request
  is byte-for-byte the same. Served by `jev.Replay`, written by `jev.Recorder`
  through `tools/recordjev --live` (`make record-jev`). M2 adds pair
  (`relation`), group (`name_bullet`) and file (`separate_pr`) requests.
- `embed/<key>.json` (M2) — `embed.Recording`
  `{"key","model","text","vector"}`; `<key>` is
  `embed.TextKey(model, text)` where text is `link.UnitText(unit)` and model
  is `ollama:embeddinggemma-2`. Served by `embed.Replay`, written by
  `embed.Recorder` through `tools/recordjev --embed live` (local Ollama,
  `OLLAMA_HOST`, default `http://127.0.0.1:11434`; free, logged in
  `SPEND.md` as service `ollama` with cost 0).
- `review/<key>.json` (M3) — LLM review responses for the `fast`/`deep`
  Reviewer tiers, keyed the same way (format defined in M3).

Pair requests depend on the embedding candidates, so the Jev recording run
and the replay must use the same embedder: `tools/recordjev` defaults to
`--embed replay` whenever `embed/` holds recordings, and the replay tests use
`embed.Replay` over the same directory. Record embeddings first, then Jev:

```
go run ./tools/recordjev --embed live      # embeddings only (dry-run Jev, $0)
go run ./tools/recordjev --embed replay    # dry run: counts, "0 to record" embeddings
go run ./tools/recordjev --live            # Jev (needs JEV_API_KEY; $1.00 cap)
```

M5 adds the fixture's local branch without a PR, `05-spike-dark-mode`, run
after the PRs in the same store (`--branches`, any mode): its unit labels,
pairs, names and the branch-scope `state` request (Jev), and its reviews,
TL;DR, group titles and suggested PR title (`--review`):

```
go run ./tools/recordjev --branches --embed live            # branch embeddings ($0)
go run ./tools/recordjev --live --embed replay --branches   # Jev
go run ./tools/recordjev --live --review --embed replay --branches   # reviews
```

Recordings are made against the fixture repo built from `testdata/fixtures`
(PRs #101–#104 in `testdata/fixtures/prs.json`). Its SHAs are deterministic,
so request keys stay stable; if a fixture patch, `link.UnitText`, a request
shape or the question catalogue changes, the affected recordings must be
re-recorded.

Total real-model spend is capped at $5 for the whole project (Jev at $1.00
cumulative, enforced by `tools/recordjev`); record once per adapter, then rely
on these files. Never commit keys or auth headers: the recorders never see
them.
