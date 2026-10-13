# Blocked

Items that could not be finished in an unattended run, with what was tried.
Each one also has an entry in `docs/DECISIONS.md`.

## Branch fast review recording (M5) — open

**What is missing.** One recording: the fast review of the single change
group ("Add dark theme", 5 units) on the local branch `05-spike-dark-mode`,
recording key `fcadba168586e76b7938da93` (model
`anthropic:claude-haiku-5-5`, `max_tokens` 1024 = `ReviewMaxTokensFast`).
The branch's group title, TL;DR and suggested PR title are recorded, as are
its Jev answer and embeddings.

**Effect.** `make demo` / `make snapshot` show the branch's Pipeline page
with one failed fast review ("review: unavailable: review: no recording for
request fcadba1…"), and the Code page's group list marks the group "fast ·
failed". `TestRecordedBranchReviewReplay` still skips with "BRANCH REVIEW
CALLS NOT RECORDED (1 misses …)". Everything else replays, including
"Suggested PR title: Add dark theme".

**What was tried.**

- 2026-10-13 07:48 and 08:48: Anthropic refused every call with HTTP 400
  "credit balance is too low". **Resolved:** credit was added.
- 2026-10-13 10:31 UTC, `make record-review`: 5 calls, $0.0016. It wrote
  3 recordings (group title, TL;DR, PR title). The fast review failed
  twice with the same request (verbatim):
  `review fcadba168586e76b7938da93: review: anthropic:claude-haiku-5-5 (stop_reason max_tokens): review: unparseable response: no JSON object with findings or verdict in 1124 bytes`
  (the second attempt: `… in 887 bytes`).
- 2026-10-13 10:33 UTC, `make record-review` again: 2 calls, $0.0014,
  0 written. The same
  error came back, at `1010 bytes` and `1265 bytes`.
- That is four truncated answers in a row. The model spends most of the
  1024 output tokens on reasoning (see the note above the output caps in
  `internal/review/prompt.go`), so the JSON answer is cut off. More
  retries are unlikely to help, so none were made. Anthropic ledger:
  $0.8570 of the $3.00 review cap; all services $0.8877 of $5.00.

**To unblock.** This needs a code change, which this recording task did
not include. Either raise `ReviewMaxTokensFast` (for example to 2048),
which changes the request, and so the recording key, of every fast review
and means re-recording the 3 PR fast reviews too. Or retry a review that stopped on
`max_tokens` once with a larger cap; this keeps the existing keys, but the
replay must be able to reach the retry. Then run `make record-review` and
`make snapshot`, and commit `testdata/recordings/review/` and `SPEND.md`.
