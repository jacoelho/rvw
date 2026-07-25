# TUI conformance review

Review date: 2026-07-25

## Reviewed tree identity

- Baseline `HEAD`: `97aca1ed6fcee53623e841c42d3742e54d3a3b06`
- Git object ID of `git diff --binary HEAD`, excluding this report:
  `c13941bb4d6e6cc9e10b97efd599c3253fca9857`
- Reviewed untracked files, sorted by path:

| Path | Blob object ID |
| --- | --- |
| `docs/bubbletea-ui-research.md` | `50c0eb78aa670682b204010cd4a2bbe365677d2e` |
| `docs/tui-design.md` | `cbd9b3495bcd3715fc3f2dc0076f5aacc7dab119` |
| `internal/app/settings_presentation_test.go` | `ee66b0b77f95a1ac4c297a20b33e7ae08a6e904c` |

No commit is implied by this identity.

## Repair review

### Finding 1

- Finding: Tagged PTY evidence still encoded `▸` and `›` as cursor and focus states.
- Evidence: The first `go test -count=1 -tags=e2e ./e2e` run failed six readable
  layout goldens and timed out in focus predicates that searched for `▸`.
- Risk: The executable could satisfy the new glyph contract while the end-to-end suite either
  failed or tested the retired contract.
- Smallest correct fix: Regenerate only affected readable goldens, replace the draft-row cursor
  expectation with `>`, and make focus predicates inspect the monochrome Reverse attribute.
- Result: Repaired. Tagged E2E passed twice after the repair, including through `make check`.

### Finding 2

- Finding: A constrained reload confirmation could remove its `▌` input-owner title.
- Evidence: The confirmation overflow branch replaced the titled body with untitled prompt lines
  when prompt plus controls exceeded the inner height.
- Risk: The only input owner became visually ambiguous at the smallest supported geometry.
- Smallest correct fix: In the overflow form, make the complete prompt itself the titled `▌`
  body and retain the existing bounded controls.
- Result: Repaired. The `20×6` confirmation tests now require both the complete prompt and `▌`.

The second review wave found no remaining hierarchy, contrast, action-ownership, constrained
adaptation, pointer-mapping, recovery, or rendering-bound findings.

## Scenario evidence

| ID | Result | Evidence |
| --- | --- | --- |
| E01 | Pass | `TestDeterministicANSIRenderAudit` exercises dark and light themes, TrueColor, all workspace focus owners, active rows, retained positions, and wide layouts. |
| E02 | Pass | The ANSI audit projects frames through ANSI16; monochrome app and PTY tests require `>`, Reverse, semantic glyphs, and a single active fill. |
| E03 | Pass | `TestResponsiveLayoutUsesBothWidthAndHeightBoundaries` covers mixed and exact tier boundaries; PTY goldens cover `80×24`, `120×40`, and `160×50`. |
| E04 | Pass | `TestSideBySideEligibilityUsesAllocatedDiffPaneWidth`, diff renderer tests, and `TestVisualRangeMarkersCoverWrappedSegmentsWithoutConsumingSourceGlyphs` cover allocated widths 101/102/103, unified and side-by-side wrapping, endpoints, rails, anchors, padding, and literal `>`/`│`. |
| E05 | Pass | `TestDialogLayoutThresholdAxesAreIndependent`, dialog context tests, Find caret tests, and Find pointer tests cover each shared and Find threshold axis, bordered context preservation, absolute inner coordinates, and inert border/exterior cells. |
| E06 | Pass | Minimum-view tests cover `19×6` and `20×6`, pending decisions, exact recovery controls, disabled hidden cursors, and minimum-size input ownership. |
| E07 | Pass | Mouse translation, stale-review, hidden-pane, Find inner-hit, border-hit, exterior-hit, caret, and focus restoration tests cover pointer ownership and stale geometry. |
| E08 | Pass | ANSI scenarios and protocol tests cover loading, empty, unavailable, limited, failed, retained, retry, success, publication choices, publication failure, reconciliation, and ready states. |

## Validation evidence

| Command or check | Result |
| --- | --- |
| `gofmt` on touched Go files | Pass |
| Focused `gopls go_diagnostics` on changed Go files | No error or warning diagnostics |
| `go test ./internal/config ./internal/app ./cmd/rvw` | Pass |
| `go test -count=1 -tags=e2e ./e2e` | Pass |
| `git diff --check` | Pass |
| `make check` | Pass: format, dependency verification, lint, normal tests, race tests, vet, build, and tagged E2E |
