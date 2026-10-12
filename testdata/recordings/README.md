# Recordings

Recorded responses from real external services, replayed by tests so nothing
touches the network:

- `jev/<key>.json` — `jev.Recording` (request + response); key is
  `jev.RequestKey(request)`. Served by `jev.Replay`, written by `jev.Recorder`
  from `//go:build live` tests only.
- `review/` — LLM review responses (format defined in M3).

Total real-model spend is capped at $5 for the whole project; record once per
adapter, then rely on these files.
