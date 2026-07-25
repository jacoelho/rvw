# TUI design

This document is the normative presentation authority for `rvw`. The product, review, output,
configuration, lifecycle, and resource contracts remain in
[`rvw-specification.md`](rvw-specification.md). Upstream research is recorded in
[`bubbletea-ui-research.md`](bubbletea-ui-research.md). A presentation rule stated elsewhere is
informative if it conflicts with this document.

## Audience and thesis

`rvw` serves engineers reviewing large diffs from a terminal. It assumes keyboard fluency but does
not assume color vision, a particular terminal palette, or a wide display.

The visual thesis is:

> A dense review workspace with the diff as the primary task locus, quiet tree and comment
> context, one accent family, stable marker columns, and one unmistakable input owner.

Position and space establish hierarchy first. Text emphasis, borders, glyphs, and color reinforce
that hierarchy without changing geometry during focus or cursor movement.

## Focus and input ownership

Exactly one context owns input:

1. below-minimum recovery;
2. one blocking dialog or full-screen workflow;
3. the focused editor or prompt;
4. one visible workspace pane.

The input-owning pane, dialog, or blocking workflow has `▌` in its title. Quiet workspace headers
reserve the same two title cells so focus changes do not move content. Closing a dialog restores
the prior valid focus owner; resizing reconciles hidden panes and retains the return owner.

Only an input-owning cursor row receives strong cursor fill. Tree, diff, comments, and Find retain
their current position with `>` when another context owns input. Retained positions never receive
cursor fill.

Find tabs and GitHub publication choices are controls, not cursor rows. Their selected state is
bold and underlined in the focused foreground with no fill.

## Glyph and emphasis language

The glyph vocabulary is closed:

| Glyph | Meaning |
| --- | --- |
| `>` | Active or retained current position; only the active position receives fill |
| `│` | Every terminal segment of a non-endpoint visual-range row |
| `▌` | Title of the sole input-owning pane, dialog, or blocking workflow |
| `•` | Active file |
| `✓` | Reviewed file or hunk |
| `●` | Comment anchor or selected publication choice |
| `▸`, `▾` | Collapsed and expanded disclosure only |

The first terminal segment of an active or retained row contains `>`; wrapped continuations leave
that marker cell blank. Visual-range endpoints use `>` and intermediate rows use `│` on every
wrapped segment. Source text containing `>` or `│` is ordinary content and is never normalized or
interpreted as presentation chrome.

Active-row styling owns every cell, including marker columns, line-number gutters, badges,
anchors, unified or side-by-side content, the side separator, wrapped continuations, and trailing
padding. The semantic glyph remains, but syntax and diff foregrounds are suppressed on the active
row. A visual-range rail uses `border_focused` on an explicit `panel_bg` marker cell and does not
fill the row.

Monochrome active rows use Reverse. Color active rows use `cursor_line_fg` on `cursor_line_bg`.
Color is never the only cue.

## Responsive modes

Modes are ordered; the first match wins:

| Mode | Exact condition | Composition policy |
| --- | --- | --- |
| Wide | `width >= 160 && height >= 30` | File tree, dominant diff, and comments when enabled |
| Normal | otherwise `width >= 120 && height >= 24` | File tree and dominant diff; comments when explicitly shown and feasible |
| Compact | otherwise `width >= 80 && height >= 18` | Dominant diff plus at most one feasible context pane |
| Narrow | otherwise `width >= 20 && height >= 6` | One task region, status, and effective controls |
| Below minimum | everything smaller | Size requirement and safe exit or pending-decision cancellation |

Pane allocation is deterministic:

- Wide tree width is `min(max(width/4, 24), 40)` when visible.
- Wide comments width is `min(max(width/5, 32), 40)` unless hidden.
- Normal tree width is `min(max(width/5, 24), 40)` when visible.
- Normal comments use 32 cells only when explicitly shown and the diff keeps at least 62 cells.
- Compact comments use 32 cells when explicitly shown and feasible. Otherwise a visible tree uses
  24 cells only above 84 columns.
- One separator cell appears between visible panes. The diff receives the remainder.
- A hidden tree or comments pane remains reachable through its single-pane override. The override
  fills the workspace and preserves the pane's selection.
- When resize hides the focused pane, focus falls back to the diff and records the return owner.
  When that pane becomes visible again, focus returns.

Side-by-side eligibility is based on the allocated diff-pane width, not terminal width. It requires
at least 102 cells: 100 content cells plus two anchor cells. At 101 or fewer cells the renderer
temporarily uses unified view, reports the limitation, and retains the side-by-side preference.

## Dialogs and workflows

Help, Confirmation, Export, and Find use one pure layout and compositor with distinct absolute
outer and inner rectangles.

- Below `20×6`, the minimum-size view owns the screen. A pending confirmation remains visible as a
  disabled decision with cancellation; caret and mouse interaction are disabled.
- A supported viewport below a dialog's border threshold uses a borderless full-screen surface.
- Help, Confirmation, and Export use a bordered dialog at `width >= 24 && height >= 10`.
- Find uses a bordered dialog at `width >= 48 && height >= 12`.
- Preferred inner sizes are Help `72×22`, Confirmation `40×7`, Export `52×9`, and Find `96×20`.

A bordered dialog is centered, preserves at least one workspace cell on every side, uses
`NormalBorder`, fills its interior with `panel_bg`, colors the border with `border_focused`, and
puts `▌` in its title. All four dialogs preserve workspace context.

Find caret and pointer coordinates derive from the absolute inner rectangle. Border and outside
clicks are inert. Status details and publication remain full-screen blocking workflows because
their operative state can require the available area.

## Application states

Loading, ready, empty, unavailable, stale, cancelled, limited, failed, ambiguous, and successful
states remain explicit. The workspace retains prior valid content during refresh where the product
contract permits it and labels that content as retained. A compact status line may summarize one
fact, but Status details retains simultaneous operation, failure, limitation, and recovery facts.

Errors and limitations include the next valid action when one exists. Disabled decisions explain
what must change. Publication failure, reconciliation, retry fencing, and ready state remain
distinct; presentation never implies that an unknown GitHub write is safe to retry.

## Pointer behavior

Pointer hit testing uses geometry from the last rendered frame. The handler performs bounded hit
translation only and returns a typed message; real work begins in `Update`. A hit has one action
owner.

Pane separators, dialog borders, dialog exterior cells, padding, empty result rows, and unrelated
motion are inert. Wheel input affects only the pane or Find result region under the pointer. A
resize or review revision invalidates stale hits before they can change selection.

## Color and contrast

The following ratios are terminal baselines inspired by the W3C
[non-text contrast](https://www.w3.org/WAI/WCAG22/understanding/non-text-contrast.html) and
[focus visibility](https://www.w3.org/WAI/WCAG22/Understanding/focus-visible) criteria. They are
not a browser-conformance claim.

Across the TrueColor, ANSI256, and ANSI16 projections:

- ordinary reachable text is at least `4.5:1` against its surface;
- `cursor_line_fg` is at least `4.5:1` against `cursor_line_bg`;
- `cursor_line_bg` is at least `3:1` against `panel_bg`;
- `border_focused` is at least `4.5:1` against `panel_bg` and `status_bar_bg`.

Configuration validates custom themes and reports the theme path, role pair, terminal profile,
actual ratio, and required ratio. Bundled themes generate cursor pairs deterministically after
panel, status, and diff surfaces are final. Monochrome and `NO_COLOR` retain the glyph and modifier
cues above.

## Rendering bounds

`View` is pure and complete. It materializes, sanitizes, wraps, styles, and joins only the viewport
plus the fixed overscan. File tree nodes, diff rows, comments, Find results, and help entries are
windowed before styling.

Frame work cannot grow with total file count, total patch lines, scroll depth, or full blob size.
Filesystem, network, subprocess, blocking waits, syntax preparation, and unbounded CPU work stay
off the terminal event loop. The installed
[Bubble Tea v2](https://pkg.go.dev/charm.land/bubbletea/v2) message/update/view contract and
[Lip Gloss v2](https://pkg.go.dev/charm.land/lipgloss/v2) cell-aware measurement contract are the
implementation baseline.

## Reproducible evidence scenarios

The conformance review records results for these stable scenarios:

| ID | Required evidence |
| --- | --- |
| E01 | Dark and light TrueColor at `160×30`, all three workspace focus owners, active versus retained cursors |
| E02 | ANSI16 and `NO_COLOR` at `120×24`, glyph/modifier cues and at most one active fill |
| E03 | `80×18`, `120×24`, and `160×30`, plus one cell below/at/above every width and height breakpoint |
| E04 | Unified and side-by-side at allocated diff widths 101, 102, and 103; wrapped rows, anchors, padding, and visual-range endpoints/rails |
| E05 | Help, Confirmation, Export, and Find at every independent border-threshold axis and at preferred sizes |
| E06 | `19×6` and `20×6`, including a pending confirmation, disabled caret, and disabled pointer |
| E07 | Pointer focus, row click, wheel, Find inner hit, border hit, outside hit, stale revision, and stale resize |
| E08 | Loading, empty, failure, retained failure, retry, success, publication choice, publication failure, reconciliation, and ready |

Each review classifies hierarchy, contrast, action ownership, constrained adaptation, pointer
mapping, recovery, and rendering bounds as pass, finding, or unverified. Findings use
Finding/Evidence/Risk/Smallest correct fix and are repaired before the report records no findings.
