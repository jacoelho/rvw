# TUI design

This document is the sole normative presentation authority for `rvw`. It owns the TUI's visual
hierarchy, focus and cursor presentation, glyphs, responsive composition, dialog geometry,
pointer hit regions, color contrast, rendering bounds, upstream presentation rationale, and
conformance scenarios. The product, review, output, configuration, lifecycle, and resource
contracts remain in [`rvw-specification.md`](rvw-specification.md).

Other documents MUST link here instead of restating presentation rules. A presentation statement
elsewhere is informative and this document wins on conflict. TUI plans, research notes, and
conformance reports MUST NOT become parallel design authorities; durable rationale and evidence
mappings belong here.

## Audience and thesis

`rvw` serves engineers reviewing large diffs from a terminal. It assumes keyboard fluency but does
not assume color vision, a particular terminal palette, or a wide display.

The visual thesis is:

> A dense review workspace with the diff as the primary task locus, quiet tree and comment
> context, one accent family, stable marker columns, and one unmistakable input owner.

Position and space establish hierarchy first. Text emphasis, borders, glyphs, and color reinforce
that hierarchy without changing geometry during focus or cursor movement.

## Implementation basis and dependency decisions

`rvw` follows Bubble Tea's `Msg -> Update -> Model + Cmd` ownership model: observable mutation has
one owner, blocking contexts route before focused components, and effects remain outside `View`.
The exact module versions and upgrade contract remain in the product specification's
[architecture and ownership](rvw-specification.md#6-architecture-and-ownership) section. The
installed Bubble Tea, Bubbles, and Lip Gloss modules provide this presentation basis:

- [Bubble Tea v2](https://pkg.go.dev/charm.land/bubbletea/v2) defines the message, update, command,
  and view lifecycle.
- [Bubbles v2](https://pkg.go.dev/charm.land/bubbles/v2) supplies focused interaction components
  where a component deletes a complete local responsibility. `rvw` uses its text input behavior
  for Find and command input, including Unicode editing, cursor, focus, and blink commands.
- [Lip Gloss v2](https://pkg.go.dev/charm.land/lipgloss/v2) supplies immutable styles and
  terminal-cell-aware measurement, wrapping, placement, and composition.

Key matching and visible help derive from the same enabled binding definitions. There is no
separate help-state copy that can advertise an unavailable or remapped action.

The design was also compared with
[`charmbracelet/crush@e4175c52`](https://github.com/charmbracelet/crush/commit/e4175c52b8e14a4e158106ac5988fd42a8958457).
Its routing of blocking dialogs before focused components, centralized semantic styles, and
coherent glyph language are transferable. Its root model, diff renderer, dialog registry, and
caches are not: those boundaries combine unrelated workflows or do not enforce `rvw`'s entry,
byte, viewport, and scroll-depth bounds.

Generic list, table, and viewport components do not provide application virtualization; they
retain or rebuild the content supplied to them. `rvw` therefore keeps direct tree, diff, and
comment renderers that materialize review-domain windows before presentation. It also keeps the
bounded local comment editor because byte ownership, grapheme-safe projection, and retained-data
accounting are product contracts rather than generic textarea behavior.

A copied lexer or Chroma does not replace revision identity, cancellation, visible-line demand,
byte accounting, multiline state, or safe fallback. Syntax preparation remains requested-line,
bounded CPU work outside `Update` and `View`.

Use an installed companion component only when it deletes a complete local interaction
responsibility while preserving ownership, cancellation, byte, work, and viewport bounds. Keep
direct domain code when a wrapper would retain the same policy or create duplicate state
ownership.

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

The semantic-marker vocabulary is closed:

| Glyph | Meaning |
| --- | --- |
| `>` | Active or retained current position; only the active position receives fill |
| `│` | Reserved marker cell on every terminal segment of a non-endpoint visual-range row |
| `▌` | Title of the sole input-owning pane, dialog, or blocking workflow |
| `•` | Active file |
| `✓` | Reviewed file or hunk |
| `●` | Comment anchor or selected publication choice |
| `▸`, `▾` | Collapsed and expanded disclosure only |

The first terminal segment of an active or retained row contains `>`; wrapped continuations leave
that marker cell blank. Visual-range endpoints use `>` and intermediate rows use `│` in the
reserved marker column on every wrapped segment. Structural borders and pane or side-by-side
separators may also use `│`; they are geometry, not semantic markers. Source text containing `>`
or `│` is ordinary content and is never normalized or interpreted as presentation chrome.

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
- A tree or comments pane omitted by responsive allocation remains reachable through its
  single-pane override unless the user explicitly hid it. The override fills the workspace and
  preserves the pane's selection.
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
fact, but Status details retains simultaneous progress, warning, error, stale, limited,
cancellation, delivery, and publication facts.

The active file, input owner, cursor, selection, pending work, stale state, errors, review marks,
comment anchors, and source identity remain distinguishable in color and monochrome modes.

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

The renderer:

- clips before expensive text processing;
- never emits raw control, escape, or bidirectional-override text from external data;
- displays invalid UTF-8 paths as escaped bytes while preserving identity;
- handles tabs, wide characters, combining marks, and grapheme boundaries deterministically;
- bounds work for a single pathological grapheme or line;
- keeps unified and side-by-side line identity, old/new numbers, hunks, gaps, comments, review
  state, and selections unambiguous;
- keeps syntax highlighting optional, bounded, cancellable, revision-bound, and off the event
  loop;
- degrades to plain text when highlighting is unavailable or limited; and
- never uses cache or highlight readiness to change review semantics.

Frame work cannot grow with total file count, total patch lines, scroll depth, or full blob size.
Filesystem, network, subprocess, blocking waits, syntax preparation, and unbounded CPU work stay
off the terminal event loop. The framework contracts named in [Implementation basis and dependency
decisions](#implementation-basis-and-dependency-decisions) are the implementation baseline.

## Reproducible conformance evidence

The implementation MUST keep these stable evidence scenarios executable:

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

The current executable evidence map is explicit about gaps; a required scenario remains required
when its status is partially verified:

| Scenario | Status | Primary evidence |
| --- | --- | --- |
| E01 | Partially verified | `TestDeterministicANSIRenderAudit` exercises dark and light themes, TrueColor, all workspace focus owners, active rows, retained positions, and a `160×50` wide layout. Exact `160×30` audit coverage remains required. |
| E02 | Partially verified | The ANSI audit projects frames through ANSI16, and monochrome application and PTY tests require `>`, Reverse, semantic glyphs, and a single active fill. Exact ANSI16 coverage at `120×24` remains required. |
| E03 | Partially verified | `TestResponsiveLayoutUsesBothWidthAndHeightBoundaries` covers mixed and exact boundary cases, and PTY goldens cover representative compact, normal, and wide layouts. Exact `80×18` and the complete one-cell-above matrix remain required. |
| E04 | Verified | `TestSideBySideEligibilityUsesAllocatedDiffPaneWidth`, diff renderer tests, and `TestVisualRangeMarkersCoverWrappedSegmentsWithoutConsumingSourceGlyphs` cover allocated widths 101/102/103, wrapping, endpoints, rails, anchors, padding, and literal `>`/`│`. |
| E05 | Verified | `TestDialogLayoutThresholdAxesAreIndependent`, dialog-context tests, Find caret tests, and Find pointer tests cover each threshold axis, context preservation, absolute inner coordinates, and inert border/exterior cells. |
| E06 | Partially verified | Minimum-view tests cover `19×6` and `20×6`, pending decisions, exact recovery controls, disabled hidden cursors, and minimum-size input ownership. An explicit disabled-pointer assertion remains required. |
| E07 | Verified | Mouse translation, stale-review, hidden-pane, Find inner-hit, border-hit, exterior-hit, caret, and focus-restoration tests cover pointer ownership and stale geometry. |
| E08 | Verified | ANSI scenarios and protocol tests cover loading, empty, unavailable, limited, failed, retained, retry, success, publication choices, publication failure, reconciliation, and ready states. |

Each design review classifies hierarchy, contrast, action ownership, constrained adaptation,
pointer mapping, recovery, and rendering bounds as pass, finding, or unverified. An actionable
finding records Finding, Evidence, Risk, and Smallest correct fix. Repair every supported finding
and repeat the review until no findings remain.

Before accepting a TUI presentation change, run `gofmt` on touched Go files, focused `gopls`
diagnostics, focused package and tagged E2E tests, `git diff --check`, and `make check` as applicable
to the change. Record transient run results in the change review, not in another normative design
document.
