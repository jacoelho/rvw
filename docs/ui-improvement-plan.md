# UI architecture and visual improvement plan

Status: `Execution Plan v4 — implemented, validated, and independently reviewed`

Base revision: `5dc46e6`

Initial worktree state: `.golangci.yml`, five terminal golden files, and
`docs/bubbletea-ui-guide.md` were already modified or untracked. They are preserved as pre-existing
work and may be reconciled only where this plan requires them.

## Charter v1

### Requirement lock

| ID | Strength | Requirement |
| --- | --- | --- |
| R1 | REQUIRED | Research reusable UI/component patterns in Bubbles, Crush internals, and Lip Gloss from primary sources. |
| R2 | REQUIRED | Make the review UI materially easier to scan and less visually plain at normal and wide terminal sizes. |
| R3 | REQUIRED | Decide which responsibilities should become reusable components and reduce complexity without introducing a component registry or duplicate state ownership. |
| R4 | REQUIRED | Keep key matching, availability, and visible help derived from the same configurable keybinding source. |
| R5 | REQUIRED | Improve diff readability and syntax-highlight presentation while preserving lazy, bounded materialization. |
| R6 | REQUIRED | Preserve Linux/macOS support, `--stdout`, `--json`, current review sources, and current interaction behavior except for intentional presentation changes. |
| R7 | REQUIRED | Keep `Update` as the sole observable-state mutation boundary and keep `View` proportional to viewport rows plus fixed overscan. |
| R8 | REQUIRED | Review this plan with two adversarial reviewers until no findings remain; do the same after implementation. |
| R9 | INFERRED | Preserve custom theme compatibility; new visual roles must have safe fallbacks when omitted. |
| R10 | INFERRED | Monochrome and `NO_COLOR` modes must retain non-color focus, selection, diff-kind, and status cues. |
| R11 | OPTIONAL | Add a third-party syntax lexer only if it replaces enough local policy and can prove bounded multiline windowing. |

### Non-goals

- No new review source, output mode, terminal owner, event bus, effect abstraction, component
  registry, or generic pane interface.
- No Bubble Tea, Bubbles, or Lip Gloss upgrade.
- No Bubbles list, table, or viewport around the file tree or diff.
- No Chroma dependency or copied lexer grammar in this change.
- No whole-document render cache, prewarming, or styling work in `Update`/`View`.
- No redesign of the coordinator, patch index, comment editor ownership, or persistence contracts.

### Architecture invariants

1. `Model.Update` is the only mutation boundary; rendering receives current state and returns a
   complete `tea.View`.
2. The root model owns review identity, focus, overlay, selection, scroll, and asynchronous
   operation identity. Rendering components borrow immutable frame inputs only.
3. Keys route once: blocking state, focused input/component, active review context, then reserved
   global action. The compiled keymap remains canonical for routing and help.
4. Patch and syntax work remains outside the terminal loop. A frame requests and renders only the
   viewport plus `viewportOverscan`.
5. Focus, selection, add/delete meaning, comment anchors, reviewed state, limitation, warning, and
   error remain distinguishable without hue.
6. Theme colors are semantic values with validated custom-theme fallbacks; no component owns an
   independent palette.

### Definition of done

- Normal and wide captures have a clear diff-first hierarchy, unmistakable focus and cursor row,
  structured pane chrome, compact contextual key help, and visibly distinct hunk/add/delete rows.
- Syntax token styling preserves the row's diff and selection background rather than erasing it.
- File rows expose change/review/active state through stable marker columns.
- No new frame path scales with total files, total patch rows, scroll depth, or full blob size.
- Focus/key matrices, view geometry, syntax boundaries, monochrome output, lint, unit/race tests,
  vet, build, and e2e pass.
- Two independent plan reviewers and two independent final reviewers report no findings.

## Baseline dossier

### Dependency and responsibility map

- The repository resolves Bubble Tea `v2.0.8`, Bubbles `v2.1.1`, and Lip Gloss `v2.0.5`.
- `internal/app.Model` owns the complete interaction state and coordinator boundary.
- Installed Bubbles already own search/command text input and full help rendering. The bounded local
  comment editor owns review-specific byte, cursor, and construction limits.
- `internal/keymap` is now the single vocabulary/default/validation owner. `internal/app` compiles
  it into focus/context scopes and runtime chord lookups.
- `internal/app/view.go` currently combines root view dispatch, mouse mapping, tree/comments
  presentation, workspace composition, chrome, and generic text fitting. `view_diff.go` owns lazy
  unified/side-by-side row materialization and syntax-span composition.
- `internal/review/syntax.go` prepares only requested source lines under explicit line/span/byte
  limits, and the coordinator runs it off the terminal loop.

### Verified visual problems

1. Pane headings improved orientation but share nearly all visual weight with body content; only a
   one-cell glyph distinguishes the focused pane.
2. The active diff row is indicated only by `▸`; the row itself is not visually anchored.
3. Bundled themes define optional addition/deletion background roles but do not populate them, so
   diff semantics are predominantly foreground color and prefix punctuation.
4. File rows reserve multiple marker cells but do not show the file's change kind.
5. Footer bindings are flattened to an unstyled string, losing the useful key/description hierarchy
   already represented by the canonical keymap.
6. Diff kind, syntax token, and selection styles have no explicit precedence owner. A future row
   background can be lost at ANSI resets unless syntax runs inherit it deliberately.
7. `semanticStyles` is declared in `view.go` while constructed in `styles.go`; workspace and diff
   visual policy is therefore split across owners.

### Upstream evidence

- Bubbles `key.Binding` and `help.Model` demonstrate one binding definition for matching, enabled
  state, and help. The current rvw keymap preserves this property while adding configurable
  multi-stroke, context, and focus scopes.
- Bubbles viewport retains complete supplied content; list and table retain their item/row sets and
  may rebuild viewport content. They are not virtualization boundaries for rvw's scale.
- Lip Gloss styles are immutable values and its width/join functions are ANSI/cell-aware. They are
  appropriate after rvw has materialized a bounded frame, not before.
- Crush centralizes semantic styles, glyphs, contextual bindings, layout regions, and modal-first
  routing. Those ideas transfer.
- Crush's root UI, list cache, dialog stack, diff conversion, scroll traversal, and syntax cache do
  not transfer: they centralize unrelated workflows or lack rvw's work/retention bounds.

Primary-source links and the detailed transfer/rejection notes are preserved in
`docs/bubbletea-ui-guide.md`.

### Refactor necessity finding

A styling-only patch can improve color but cannot establish one owner for pane chrome, structured
binding hints, or diff-style precedence. Stateful Bubble children for tree/diff/comments would move
shared review/workflow state across new boundaries and amplify migration risk. A bounded render
component refactor is necessary: it centralizes repeated presentation policy while leaving mutable
domain and navigation state in the existing owner.

## Candidate comparison

### Candidate A — style existing functions in place

- Add stronger Lip Gloss styles and update goldens without changing ownership.
- Strength: smallest transition and easiest rollback.
- Failure: preserves split visual policy and ad hoc row precedence; does not reduce complexity or
  produce reusable pane/key-hint composition. Rejected because it incompletely satisfies R3/R5.

### Candidate B — immutable render components over the existing model (accepted)

- Keep `Model`, `Update`, coordinator, selection, scroll, review, and keymap ownership unchanged.
- Add small concrete frame values for pane chrome/body fitting and contextual binding hints.
- Move the semantic style type and state-precedence rules into `styles.go`.
- Give diff row composition one function that selects gutter/content/syntax styles from diff kind,
  selection, color mode, and focus.
- Populate existing optional diff-background roles for bundled true-color themes; preserve custom
  theme and ANSI/monochrome fallbacks.
- Strength: one canonical state path, bounded mechanical migration, reusable presentation for three
  panes, and explicit visual precedence without duplicating domain state.
- Strongest objection: render-only values can become wrappers that merely move code. They are
  accepted only where they delete repeated width/focus/padding/help logic from at least two callers.

### Candidate C — stateful tree, diff, and comments Bubble children

- Move cursor/scroll/visibility/focus behavior into three child models and route messages through
  them.
- Strength: stronger local ownership if each pane were an independent application feature.
- Failure: all three depend on shared review identity, reload reconciliation, overlay modes,
  asynchronous patch/syntax state, and cross-pane navigation. Migration would either duplicate
  state or create a broad parent callback surface. Rejected as concept multiplication.

### Candidate D — Bubbles list/table/viewport plus third-party lexer

- Rejected at the hard gate. These components retain or rebuild complete content, and a generic
  lexer does not replace rvw's identity, cancellation, visible-line demand, byte accounting, or
  multiline-window contract.

## Accepted Design v1

### Visual thesis

> A dense review workspace with the diff as the primary task locus, quiet tree and comment context,
> one accent family, stable marker columns, and focus shown without geometry changes.

### Component and ownership delta

1. `workspacePane` is an immutable per-frame rendering value: title, width, focus, and already
   bounded body lines. It owns header treatment, full-width fitting, and pane-local empty chrome.
   It owns no review state and handles no messages.
2. `bindingBar` renders canonical `keymap.Binding` values into styled key/description pairs and
   drops trailing hints at cell boundaries. It does not duplicate key matching.
3. `diffRowStyles` is a derived style set, not a model. It owns precedence:
   selected row background > add/delete row background > panel background; syntax foreground and
   emphasis inherit the chosen row background; focus remains a separate stable marker.
4. Existing `help.Model`, text inputs, and local comment editor remain the only interactive child
   components. No generic component interface is introduced.

### Visual behavior

- Pane headers use a quiet shared bar. The focused pane uses the reserved `▌` marker, stronger
  heading, and accent; width never changes on focus movement.
- Tree rows show a stable change-kind badge (`A`, `D`, `M`, `R`, or a conservative fallback), active
  file marker, review check, and disclosure marker without shifting the label.
- The selected diff row gets a full-width cursor background while `▸` remains the non-color focus
  cue. Addition/deletion rows get subtle semantic backgrounds when a true-color bundled/custom
  theme supplies them.
- Line-number gutters are quiet and stable. Hunk headers use a full-width accent band; metadata is
  de-emphasized. Side-by-side applies add/delete backgrounds only to the applicable side.
- Syntax keywords gain emphasis, comments remain quiet, and every syntax run inherits its diff-row
  background. Syntax preparation, kinds, selectors, limits, and dependencies do not change.
- The footer separates operation/notice state from contextual bindings. Keys are emphasized,
  descriptions are muted, and lower-priority hints disappear before state text when width is tight.

### Deletion and contraction ledger

- Delete `normalStatusSummary` and `commentEditorSummary` flattened strings if structured canonical
  bindings replace them.
- Delete duplicated pane-header and per-row padding branches replaced by `workspacePane`.
- Delete any now-unused generic `stylePatchLine` path after diff-row styling is centralized.
- Do not retain old and new footer or diff styling paths behind flags.

## Predicted change surface

| File or symbol | Change | Architectural role | Confidence |
| --- | --- | --- | --- |
| `internal/app/styles.go` / `semanticStyles` | Change/move | Semantic theme and combined-state precedence owner | High |
| `internal/app/view.go` | Change/contract | Root view dispatch, frame composition, shared bounded text utilities | High |
| `internal/app/view_workspace.go` | Add | Concrete immutable pane and binding-bar rendering | Medium |
| `internal/app/view_diff.go` | Change | Unified/side row composition through canonical row styles | High |
| `internal/app/settings.go` | Change/contract | Preserve structured status bindings instead of flattened copies | High |
| `internal/app/model_keys.go`, `help_keymap.go` | Change | Expose canonical contextual bindings to renderers | High |
| `internal/config/theme.go` | Change | Populate optional subtle diff backgrounds for bundled true-color themes | High |
| `cmd/rvw/interactive.go` | Likely unchanged | Existing theme-to-palette bridge already exposes required roles | High |
| app/config/e2e tests and goldens | Change | Geometry, precedence, canonical help, theme, and rendered-state evidence | High |
| `docs/bubbletea-ui-guide.md` | Reconcile | Primary-source research record | High |

## Execution Plan v1

### P0 — baseline checkpoint

- Record exact dependency versions, base revision, dirty paths, current normal/wide goldens, focused
  tests, and the existing `MaterializedRows` bound.
- Verification: focused app/config tests pass before task-owned production edits.
- Rollback: no production change.

### P1 — behavior-preserving presentation ownership refactor

- Move `semanticStyles` beside its constructor.
- Introduce `workspacePane` and structured binding selection/rendering.
- Migrate tree/diff/comments workspace composition and comment-editor/footer hints.
- Remove flattened status/comment summaries and duplicated padding/header code.
- Verification: key-routing matrix, help consistency, view geometry, and existing goldens before
  intentional visual changes.
- Rollback: revert P1 independently; no domain state or external contract changed.

### P2 — semantic theme expansion

- Populate existing optional addition/deletion background roles for bundled true-color themes using
  deterministic subtle colors; ANSI16/custom themes without those roles retain foreground-only
  behavior.
- Add header, gutter, selected-row, hunk, syntax-emphasis, warning, and footer style roles in the
  single semantic style owner.
- Verification: all bundled themes resolve; custom themes with no optional keys still validate;
  monochrome roles preserve non-color cues.

### P3 — pane and status hierarchy

- Apply stable pane headers, full-width selected rows, file change badges, and structured status/help
  composition.
- Ensure compact/narrow layouts remove hidden pane chrome and keep focus clamped to a visible owner.
- Verification: exact widths at compact/normal/wide boundaries, one cell below/at/above; focus
  geometry invariant; long/Unicode paths; empty tree/comments; notice and operation states.

### P4 — diff and syntax presentation

- Centralize unified and side-by-side row styling.
- Apply semantic backgrounds to content and gutters, full-width selected row and hunk treatment,
  muted metadata, and syntax styles inheriting the active row background.
- Preserve horizontal clipping, wrapping, comment anchors, cursor/review markers, truncation, and
  side applicability.
- Delete `stylePatchLine` if all callers use the canonical row path.
- Verification: addition/deletion/context/hunk/metadata/no-newline, selected/unselected,
  focused/unfocused, syntax/no syntax, Unicode graphemes, hostile ANSI, wrap/no-wrap,
  unified/side-by-side, and viewport-bound span pulling.

### P5 — rendered evidence and performance checkpoint

- Update readable monochrome goldens intentionally.
- Capture and inspect normal and wide color PTY frames plus compact/narrow/minimum monochrome frames.
- Confirm materialized rows remain bounded by viewport plus overscan; add a structural assertion if
  the new render components expose a previously unmeasured path.
- Run focused benchmarks only if measured allocation/work materially regresses.

### P6 — repository verification

- `gofmt` changed Go files.
- Focused app/config/keymap tests.
- `gopls` diagnostics on changed Go files.
- `make check`: format, dependencies, lint, unit tests, race tests, vet, build, and e2e.
- Direct PTY smoke: launch `./bin/rvw diff`, traverse every visible pane, open/close help, toggle
  unified/side-by-side where configured, and exit cleanly.

### P7 — independent audit and repair loop

- Freeze writes and give two read-only adversaries Charter v1, Accepted Design v1, this plan, the
  actual diff, rendered captures, and validation output.
- Reviewer A reconstructs Bubble Tea ownership, focus/key routing, component necessity, viewport
  bounds, and plan conformance.
- Reviewer B attacks Go simplicity, theme/config compatibility, diff/syntax precedence, responsive
  behavior, and tests.
- Route findings to baseline/design/plan/implementation as required, repair with one writer, rerun
  affected validation, and repeat both reviews until neither reports a finding.

## Verification traceability

| Requirement | Plan steps | Required evidence |
| --- | --- | --- |
| R1 | P0 | Pinned upstream file links and transfer/rejection record |
| R2 | P2–P5 | Inspected color and monochrome frames at responsive sizes |
| R3 | P1, P7 | Deleted duplication, one owner per visual rule, adversarial architecture audit |
| R4 | P1, P3, P6 | Routing/help matrix and canonical binding tests |
| R5 | P2, P4–P6 | Row-precedence tests, syntax boundary tests, structural work bound |
| R6 | P0, P6 | Existing CLI/e2e contracts and full repository checks |
| R7 | P1, P4–P7 | Pure render components, unchanged update boundary, materialization assertions |
| R8 | P7 | Two clean plan reviews and two clean final reviews |

## Amendment ledger

### Plan Review Wave 1

Both independent reviewers vetoed `Accepted Design v1` and `Execution Plan v1`. The root
adjudication accepts every material finding:

1. `workspacePane` does not delete an existing duplicated policy; `workspaceView`,
   `workspaceHeaderLine`, and `paneHeader` already centralize the relevant composition.
2. Structured footer bindings need a fixed semantic subset selected at settings compilation, not
   the complete effective binding slice.
3. Diff styling needs a per-cell side/applicability contract, not a row-only contract.
4. VT-cell style assertions and observed structural bounds are mandatory rather than conditional.
5. `NO_COLOR` must preserve the existing explicit-theme override contract.
6. Responsive tests must cover height, visibility, separator, hit-map, and derived diff-width
   boundaries, not only the named width breakpoints.
7. Deletions, rollback points, R9–R11 traceability, and file-badge semantics must be exact.

### Amendment A1 — render-component and proof correction

- Reason: clear two plan-review vetoes before production edits.
- Affected plan: `Accepted Design v1`, P1–P7, deletion ledger, predicted change surface, and
  verification traceability.
- Architecture effect: remove `workspacePane`; retain the current direct workspace composition;
  narrow the new presentation concepts to a fixed-size `bindingBar` and per-cell diff style
  derivation, both justified by present repeated consumers.
- Approval: root adjudication; no charter or external-contract waiver.
- Result: `Accepted Design v2` and `Execution Plan v2` below supersede v1. Charter v1 remains
  unchanged except that R10 is clarified, not broadened, by the existing configuration contract.

## Accepted Design v2

### Preserved owners

- `Model` remains the only owner of mutable focus, selection, scroll, overlay, review, and
  operation state.
- `workspaceView`, `workspaceHeaderLine`, `paneHeader`, and the parallel mouse-hit slices remain the
  direct workspace composition path. Domain empty/unavailable/failed/limited states remain in
  `fileTreeLines`, `commentLines`, and `diffLines`.
- Existing Bubbles help/text input and the bounded local comment editor remain the only interactive
  child components. The component analysis therefore concludes that additional stateful pane
  components would increase coupling; they will not be created.

### New bounded presentation concepts

1. `bindingBar` is a pure renderer over at most four precompiled bindings. It measures terminal
   cells, styles chord and description separately, and drops only the lowest-priority trailing
   pair. It is used by the normal workspace footer and comment editor, so it replaces two string
   formatting paths.
2. `diffCellStyles(kind, selected, side, applies, colorEnabled)` derives gutter, marker, content,
   padding, and syntax base styles for one visible cell. Unified mode supplies one applicable cell;
   side-by-side derives old and new cells independently.
3. Style precedence is explicit:
   - selected background overrides add/delete and panel backgrounds on both visible cells;
   - otherwise add/delete background applies only to the side where the patch line applies;
   - syntax foreground/emphasis inherits the selected cell background;
   - gutter and trailing padding receive the same cell background explicitly;
   - focus, reviewed, comment-anchor, and diff-kind glyphs remain stable non-color cues.

### Clarified configuration matrix

- `NO_COLOR` without an explicit theme disables chromatic styling.
- An explicit `--theme` continues to override `NO_COLOR`.
- `ansi16-dark` receives no generated RGB addition/deletion backgrounds.
- A custom theme omitting optional `diff_add_bg`/`diff_del_bg` remains foreground-only.
- A custom theme supplying optional backgrounds preserves exactly those validated values, whether
  RGB or named ANSI colors.
- Bundled true-color themes receive deterministic subtle backgrounds derived during theme
  construction; tests must verify dark and light palettes rather than assuming contrast.

### File change badges

The stable file marker column uses this closed mapping:

| Change kind | Badge |
| --- | --- |
| added | `A` |
| deleted | `D` |
| modified | `M` |
| renamed / rename-only | `R` |
| copied | `C` |
| binary | `B` |
| mode-only / object-type-changed | `T` |
| submodule | `S` |

An unreachable unknown value renders `?`; it does not silently reuse another semantic state.
Letters remain visible in monochrome. Added/deleted use their semantic foreground; other changes
use the primary or pending role without introducing new theme keys.

### Exact deletion ledger

After the structured binding migration, delete:

- `compiledKeyScope.normalStatusSummary` and `commentEditorSummary`;
- `compiledKeybindings.normalStatusSummary` and `commentEditorSummary`;
- `Model.normalStatusBindingSummary` and `commentEditorBindingSummary`;
- `formatActionSummary`;
- the sole `stylePatchLine` call and `stylePatchLine` if P4 leaves it unused.

Retain and repurpose `normalStatusActions` and `commentEditorSummaryActions` to precompile fixed
structured binding slices. Retain `bindingSummary` and `formatBindingSummary` for minimum/help/
status/export/submit overlays; they are a distinct full-context summary contract.

### Corrected predicted change surface

`internal/app/view_workspace.go` and `workspacePane` are removed from the forecast. Workspace
changes remain in `view.go`. `help_keymap.go` may own `bindingBar` because it already bridges the
canonical keymap to visible help. All other v1 predictions remain.

## Execution Plan v2

### P0 — baseline checkpoint (complete)

- Dependency versions, revision, dirty paths, normal/wide goldens, current ownership, and upstream
  evidence are recorded.
- `go test ./internal/app ./internal/config ./internal/keymap` passed before production edits.

### P1 — structured bounded binding hints

- At settings compilation, resolve the existing ordered normal actions to at most four bindings per
  focus and comment-editor actions to at most three bindings.
- Store immutable clipped slices on the compiled scope; preserve complete effective bindings for
  routing and full help.
- Render those fixed slices with `bindingBar`; measure only the fixed set and drop trailing hints at
  cell boundaries.
- Perform every mandatory deletion in the exact deletion ledger except `stylePatchLine`, which
  belongs to P4.
- Tests: default and overridden focus bindings, action priority independent of configuration order,
  zero/one/full subsets, and one cell below/at/above each hint-fit boundary. Assert the renderer
  never receives more than four bindings.
- Rollback: revert P1 without changing routing or complete overlay help.

### P2 — semantic theme expansion

- Move `semanticStyles` beside `newSemanticStyles`; define header, footer key/description, gutter,
  change badge, cell background, hunk, metadata, selection, and syntax-emphasis roles centrally.
- Populate optional add/delete backgrounds only for bundled true-color themes. Do not synthesize
  them for `ansi16-dark` or custom themes.
- Tests: the full clarified configuration matrix, exact preservation of custom optional values,
  dark/light bundled background presence and contrast, and no-color non-chromatic styles.
- Rollback: theme derivation and semantic style expansion revert independently of P3/P4.

### P3 — direct workspace hierarchy

- Keep direct `workspaceView` composition. Improve `paneHeader` and full-width tree selection in
  place; add the specified file change badge; render the structured binding bar in the footer and
  comment editor.
- Preserve exact mouse-hit rows and focus clamping.
- Tests: below/at/above width and height boundaries for compact/normal/wide; file/comments
  visibility combinations; separator widths summing exactly to terminal width; hit boundaries;
  hidden chrome absence; derived side-by-side threshold; long/Unicode labels; empty and notice
  states; no-color markers.
- Rollback: P3 reverts independently; structured binding data can temporarily render through the
  old plain formatter only in the rollback commit, not the final architecture.

### P4 — per-cell diff and syntax presentation

- Derive old/new/unified styles with `diffCellStyles`; style gutter, content, syntax runs, and
  trailing padding through the same cell background.
- Apply selected background to both visible side-by-side cells; apply add/delete background only to
  the applicable unselected cell. Hunk and metadata treatment remains viewport-local.
- Fit/pad the cell before its full-cell background is finalized; never rely on an outer ANSI style
  surviving nested syntax resets.
- Delete `stylePatchLine` and its sole caller once the canonical path is complete.
- Tests: deterministic VT cells for foreground, background, emphasis, gutter, content, and padding
  in unified and side-by-side add/delete/context/hunk/metadata rows, selected and unselected, with
  and without syntax spans. Assert neutral non-applicable cells, hostile ANSI sanitization,
  grapheme boundaries, wrapping, clipping, comment anchors, reviewed markers, and no-color cues.
- Rollback: P4 reverts independently without changing patch/syntax acquisition or identity.

### P5 — mandatory structural and rendered proof

- Use worst-supported fixtures and deep scroll offsets.
- Assert tree windows and produced tree/comment/diff rows are viewport-bounded; patch demand is at
  most `height + viewportOverscan`; syntax span iterators stop at the visible content boundary;
  binding traversal is at most four; unified/side-by-side wrap/no-wrap output is height-bounded.
- Do not use `MaterializedRows` as the sole oracle; keep it only as an existing aggregate guard.
- Inspect normal/wide color PTY frames plus compact/narrow/minimum monochrome frames. Record focus,
  selection, diff-kind, status, spacing, and hierarchy as pass/finding/unverified.
- Update goldens only after production and deterministic style tests pass.
- Rollback: capture/golden changes are separable from production logic.

### P6 — repository verification

Unchanged from v1, plus `git diff --check` and an explicit dependency diff proving no syntax or UI
dependency was added or upgraded.

### P7 — independent audit and repair loop

Unchanged from v1. Reviewers receive both plan versions and Amendment A1.

## Corrected verification traceability

| Requirement | Plan steps | Required evidence |
| --- | --- | --- |
| R1 | P0 | Pinned upstream links and transfer/rejection record |
| R2 | P2–P5 | Deterministic VT cells and inspected responsive captures |
| R3 | P1, P3, P7 | Only earned binding/diff concepts; no pane wrapper; deletion evidence |
| R4 | P1, P3, P6 | Fixed semantic subsets plus routing/full-help matrices |
| R5 | P2, P4–P6 | Per-cell precedence, syntax boundary, and work-bound tests |
| R6 | P0, P6 | Existing CLI/e2e contracts and full repository checks |
| R7 | P1, P4–P7 | Mandatory observed traversal/materialization bounds |
| R8 | P7 | Two clean plan reviews and two clean final reviews |
| R9 | P2, P6 | Custom themes with omitted and supplied optional roles |
| R10 | P2–P5 | Exact `NO_COLOR`/explicit-theme/ANSI16/monochrome matrix |
| R11 | P0, P4, P6 | No dependency delta; unchanged bounded syntax acquisition contract |

### Plan Review Wave 2

Reviewer A reported no findings. Reviewer B found three execution-contract defects, all accepted:

1. The proposed diff style function used positional booleans prohibited by the repository and had
   no explicit unified-cell state.
2. P1 and P3 both claimed binding-bar migration, making rollback and deletion claims inconsistent.
3. Syntax inheritance named only color backgrounds and did not explicitly preserve monochrome
   reverse/selection modifiers through token and padding cells.

### Amendment A2 — type, ownership, and monochrome correction

- Reason: clear Plan Review Wave 2 before implementation.
- Affected plan: Accepted Design v2 items 1–3, P1, P3, P4, and predicted file ownership.
- Approval: root adjudication; no charter amendment or behavioral waiver.
- Result: `Execution Plan v3` consists of v2 plus the binding corrections below.

#### Typed diff-cell state

Replace the invalid positional signature with one named state value:

```go
type diffCellSide uint8

const (
	diffCellUnified diffCellSide = iota + 1
	diffCellOld
	diffCellNew
)

type diffCellSelection uint8

const (
	diffCellUnselected diffCellSelection = iota + 1
	diffCellSelected
)

type diffCellApplicability uint8

const (
	diffCellNeutral diffCellApplicability = iota + 1
	diffCellApplicable
)

type diffCellColorMode uint8

const (
	diffCellMonochrome diffCellColorMode = iota + 1
	diffCellColor
)

type diffCellState struct {
	kind          review.PatchLineKind
	side          diffCellSide
	selection     diffCellSelection
	applicability diffCellApplicability
	colorMode     diffCellColorMode
}
```

The exact unexported names may change if a smaller equally explicit closed representation emerges,
but every call passes one named value; unified/old/new are representable without sentinel booleans;
unknown enum values produce neutral safe styles. The helper is `diffCellStyles(diffCellState)`.

#### Binding-bar ownership

- P1 exclusively owns: fixed semantic binding selection, `bindingBar`, both workspace/comment-editor
  consumer migrations, and every summary deletion listed in v2.
- `internal/app/help_keymap.go` is the exact owner of `bindingBar`, beside the existing canonical
  keymap-to-Bubbles help bridge.
- P3 contains no binding migration and no rollback through a deleted formatter. It changes only
  direct pane header/tree/status hierarchy using the already-migrated bar.
- P1 rollback restores both old summary data and consumers as one unit. P3 rollback leaves the P1
  binding bar intact.

#### Complete cell-base inheritance

“Cell background” in v2 is replaced by “complete cell base.” In color mode this includes the chosen
background plus applicable foreground defaults. In monochrome it includes reverse/selection and
other non-color modifiers. Gutter, marker, content, every syntax run, and trailing padding inherit
the complete chosen base.

P4 must add a deterministic VT test for a selected `NO_COLOR` row containing syntax spans. It must
prove token cells and trailing padding retain the selected modifier, while keyword/comment
emphasis remains distinguishable.

### Plan Review Wave 3

Reviewer B reported no findings. Reviewer A found that independent side/applicability fields still
allowed a known invalid `unified + neutral` combination. The finding is accepted.

### Amendment A3 — make diff-cell placement valid by construction

- Reason: clear the remaining Plan Review Wave 3 finding.
- Affected plan: Amendment A2 typed diff-cell state and P4 tests.
- Approval: root adjudication; no charter amendment or behavioral waiver.
- Result: `Execution Plan v4` consists of v3 with the replacement below.

Replace `diffCellSide` plus `diffCellApplicability` with one closed placement:

```go
type diffCellPlacement uint8

const (
	diffCellUnified diffCellPlacement = iota + 1
	diffCellOldApplicable
	diffCellOldNeutral
	diffCellNewApplicable
	diffCellNewNeutral
)

type diffCellState struct {
	kind      review.PatchLineKind
	placement diffCellPlacement
	selection diffCellSelection
	colorMode diffCellColorMode
}
```

Unified cells are necessarily applicable. Old/new cells state applicability in the same closed
value, so no sentinel or contradictory tuple exists. Unknown placement values use a neutral safe
style and are covered as invalid-state defense, while every known placement is covered by an
exhaustive table test.

### Plan Review Wave 4

Both independent reviewers reported no findings. `Execution Plan v4` is accepted for
implementation.

## Implementation and final audit

Implementation followed `Execution Plan v4`:

- `bindingBar` renders the fixed, canonical keybinding subsets already compiled for the footer and
  comment editor. It drops whole trailing hints rather than clipping a partial binding.
- `semanticStyles` and the validated `diffCellState` now own cell precedence. Panel, semantic,
  hunk, and selection bases reach gutters, markers, syntax runs, missing-side anchors, wrapped
  continuation chrome, content, and padding in unified and side-by-side layouts.
- Bundled true-color themes receive deterministic subtle add/delete backgrounds. ANSI16 and custom
  themes without optional backgrounds remain foreground-only; custom supplied values are retained.
- Direct workspace composition remains the owner. Pane headers, full-width selections, file-change
  badges, footer/status treatment, and tree window backfilling were improved without stateful pane
  children or a generic component layer.
- Patch demand remains exactly viewport height plus fixed overscan. Deep-offset tests cover tree,
  comment, unified, and side-by-side windows with wrapping both enabled and disabled.
- No Go module dependency changed.

Final evidence:

- Deterministic VT tests cover panel continuity, metadata, hunk bands, add/delete applicability,
  colored and monochrome selection, real syntax inheritance, absent anchors, wrapped continuation
  gutters, and trailing padding.
- `BenchmarkRenderReviewAtScale`: 1.79 ms/op, 69,440 B/op, 1,943 allocs/op on an Apple M2 Max.
- `BenchmarkRenderReviewAtMaxGeometry`: 28.56 ms/op, 2,266,968 B/op, 19,777 allocs/op at
  1,000×300 on the same machine.
- `make check` passed: formatting, module verification, lint, unit tests, race tests, vet, build,
  and all e2e tests.
- After repair loops, both independent final reviewers reported `No findings` against the latest
  implementation and tests.
