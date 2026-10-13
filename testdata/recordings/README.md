# Recordings

Recorded responses from real external services, replayed by tests so nothing
touches the network. M0 makes no real API calls and ships no recordings; the
directories below are filled in by later milestones.

- `jev/<key>.json` (M1) — `jev.Recording` (request + response); `<key>` is
  `jev.RequestKey(request)`, so a recording is found again only if the request
  is byte-for-byte the same. Served by `jev.Replay`, written by `jev.Recorder`
  from `//go:build live` tests only.
- `review/<key>.json` (M3) — LLM review responses for the `fast`/`deep`
  Reviewer tiers, keyed the same way (format defined in M3).

Recordings are made against the fixture repo built by `make fixtures` from
`testdata/fixtures` (PRs #101–#104 in `testdata/fixtures/prs.json`). Its SHAs
are deterministic, so request keys stay stable; if a fixture patch changes,
the affected recordings must be re-recorded.

Total real-model spend is capped at $5 for the whole project; record once per
adapter, then rely on these files. Never commit keys or auth headers: the
recorder must strip them before writing.
