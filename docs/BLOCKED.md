# Blocked

Items that could not be finished in an unattended run, with what was tried.
Each one also has an entry in `docs/DECISIONS.md`.

## Branch review recordings (M5) — open

**What is missing.** The offline replay has no Anthropic recordings for the
local branch `05-spike-dark-mode`: 8 calls (group titles, a fast review, the
TL;DR and the suggested PR title; upper bound $0.1983). The branch's Jev
answer (spike .66) and embeddings are recorded.

**Effect.** `make demo` / `make snapshot` show "Suggested PR title: none —
title failed: review: no recording …" and one failed fast review on the
branch's Pipeline page. The branch review replay test skips with "BRANCH
REVIEW CALLS NOT RECORDED". Everything else replays.

**What was tried.**

- 2026-10-13 07:48 (M5 build): `make record-review` — Anthropic answered
  HTTP 400 `invalid_request_error`: "Your credit balance is too low to access
  the Anthropic API." No spend.
- 2026-10-13 08:48 (M5 release gate): `make record-review-dry` (11 calls to
  record, upper bound $0.1983), then `make record-review` — the same HTTP 400
  "credit balance is too low" on every call; 0 recordings written, $0.0000
  spent (row added to `testdata/recordings/SPEND.md`).

**To unblock.** Add credit to the Anthropic account behind
`PRT_ANTHROPIC_API_KEY`, then run `make record-review` (stays well inside the
$3.00 review cap; Anthropic ledger $0.8540, all services $0.8847 of $5.00)
and `make snapshot`; commit `testdata/recordings/review/` and `SPEND.md`.
