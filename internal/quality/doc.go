// Package quality measures label quality: it runs the full pipeline
// offline over the fixture items (recorded Jev answers, embeddings and
// reviews) and scores the aggregate against the hand-written ground truth
// in testdata/groundtruth (format in its README.md). The result is a
// deterministic scoreboard (Board), printed by tools/quality and guarded
// by a regression test; docs/QUALITY.md explains every metric and keeps
// the before/after tables.
//
// Matching. A ground-truth entry has a path, an inclusive line range on
// one side (new unless side = "old") and optional also_at locations on the
// same side. A pipeline unit overlaps an entry when the paths are equal
// and the unit's changed span on that side intersects the entry's lines or
// one of its also_at ranges. The changed span runs from the first to the
// last added line (new side) or deleted line (old side); a unit with no
// changed line on that side uses its whole range there. Soft entries are
// never counted as misses, and flagging them is never a false positive.
//
// Metrics (every one a Ratio; Den 0 means "does not apply"):
//
//   - eyes_recall: firm needs_eyes entries overlapped by a unit with
//     Attention eyes ÷ firm needs_eyes entries.
//   - eyes_precision: eyes units overlapping any needs_eyes entry (firm or
//     soft) ÷ eyes units.
//   - headline_precision: Overview headline rows whose unit overlaps any
//     headline entry ÷ headline rows; a row matching no headline entry
//     (whether it overlaps a not_headlines entry or nothing) is a false
//     positive.
//   - headline_recall: firm headline entries matched by a headline row ÷
//     firm headline entries.
//   - hot_spots_top3: the pipeline's HotSpots[0:3] whose unit overlaps a
//     rank ≤ 3 entry ÷ min(3, number of rank ≤ 3 entries).
//   - fidelity: bullets whose implemented / not found equals the truth
//     ("partial" accepts either) ÷ truth bullets.
//   - undocumented_recall: firm undocumented changes overlapped by a unit
//     in ItemAgg.Undocumented ÷ firm undocumented changes.
//   - unsure_labels: ItemAgg.Unsure ÷ the item's unit-scope and line
//     labels; unsure_units: ItemAgg.UnsureUnits ÷ units.
//   - lines_to_read: changed lines (adds + dels) of eyes units ÷ the
//     item's changed lines (spec "Done means": below 1/3).
//   - must_read: 1/1 when those eyes lines fall within
//     [floor(must_read·(1−tolerance)), ceil(must_read·(1+tolerance))].
//   - branch_state: 1/1 when the branch block's state is state or in
//     state_ok.
//   - file_kind: truth files whose dominant unit kind label (by changed
//     lines) is kind or in also ÷ truth files.
package quality
