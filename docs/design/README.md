# prt design canvas

These files are the user's **"PR Triage TUI"** design canvas, copied verbatim
from https://claude.ai/artifact/CfkgUyQ6M2NWxcGE4BcQGo (`canvas.json` is the
board layout; each `*.dc.html` is one board).

| Version | Boards | What it shows |
| --- | --- | --- |
| v1 (first pass) | `Main.dc.html` (Overview), `Code.dc.html`, `Architecture.dc.html`, `Overlap.dc.html` | the first sketch of the four screens |
| v2 (overview from per-chunk labels) | `v2/Overview.dc.html`, `v2/Code.dc.html`, `v2/Architecture.dc.html`, `v2/Overlap.dc.html` | the label-driven Overview, Code by file with chips/filters/gutter, the module graph coloured by attention, semantic overlap |
| v3 (semantic groups and routed review) | `v3/Groups.dc.html`, `v3/Pipeline.dc.html` | Code by change (groups in review order) and the Pipeline page |

## How to read them

They are HTML mockups of **terminal screens**: one monospace cell grid
(JetBrains Mono 13px/20px), no proportional layout. Shared conventions:

- **Palette (dark):** background `#0e1014`, text `#d4d4cf`, dim `#8b929c`,
  bold/bright `#f2f2ec`, accent (amber) `#f0a63a`, blue `#79b8ff`,
  add (green) `#7ee2a8`, del (red) `#ff9b9b`; box borders `#2b313c`,
  selection background `#1b2130` with an accent left bar.
- **Boxes with titled borders:** a thin border with its title set into the
  top edge (`┌─ Queue · ranked ──┐` in a terminal).
- **Header line:** `prt · <owner/repo> · N PRs · N branches · role: <role>`
  on the left, sync/model status dim on the right.
- **Picker:** a "Queue · ranked" box with `── NEEDS YOU ──`-style section
  dividers, a four-cell urgency bar `▮▮▯▯` (filled cells in accent, empty
  ones dim), bold number + title, a secondary line, and a dim
  `why: …` line on the selected item.
- **Tabs:** `1 Overview  2 Code  3 Architecture`, the active one inverted on
  the accent colour.
- **Key hint bar:** one bottom line of `key` + action pairs.
- **Chips:** small inverse-video labels (`feature`, `⚠ breaking .86`,
  `tests ✗`), warm for risk, red for missing tests, blue outline for unsure.
- **Gutter:** `◆` suspicious (shaded by score), `~` value tweak, `⋯`
  leftover, `·` nothing.

## Precedence

`docs/spec.md` wins, then `docs/DECISIONS.md`, then these designs for layout
and visual detail (labels, glyphs, colour roles, box titles, ordering of
blocks). Where a design shows something the spec does not have (for example
v1's "Overlaps" box or "synced 1m ago · gh ✓ · model ✓"), follow the spec
and keep the design's look. The terminal build must also degrade: in
16-colour and no-colour terminals every meaning carried by colour is also
carried by a glyph or word, and `--snapshot` output stays colour-free.

Overlap (`Overlap.dc.html`, `v2/Overlap.dc.html`) is M6 and out of scope for
v1 (see DECISIONS "Design canvas adopted (M5)").
