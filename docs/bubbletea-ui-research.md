# Bubble Tea UI research

This document records upstream evidence used by `rvw`. It is informative; normative presentation
rules live in [`tui-design.md`](tui-design.md).

The comparison used
[`charmbracelet/crush@e4175c52`](https://github.com/charmbracelet/crush/commit/e4175c52b8e14a4e158106ac5988fd42a8958457)
from 2026-07-23. Both projects resolved Bubble Tea `v2.0.8`, Bubbles `v2.1.1`, and Lip Gloss
`v2.0.5`.

## Transferable findings

- Bubble Tea's native `Msg -> Update -> Model + Cmd` flow keeps observable mutation in one owner.
- Bubbles binding/help components demonstrate the value of deriving matching and visible help from
  one binding definition.
- Bubbles text input owns Unicode editing, cursor, focus, and blink commands; `rvw` uses it for
  Find and command input.
- Lip Gloss styles are immutable values and its width, cut, wrap, placement, and compositor APIs
  operate on terminal cells and ANSI strings.
- Crush routes blocking dialogs before focused components, centralizes semantic styles, and keeps a
  coherent glyph language.

Primary references:

- [Bubble Tea v2](https://pkg.go.dev/charm.land/bubbletea/v2)
- [Bubbles v2](https://pkg.go.dev/charm.land/bubbles/v2)
- [Lip Gloss v2](https://pkg.go.dev/charm.land/lipgloss/v2)

## Rejected transfers

`rvw` does not copy Crush's root model, diff renderer, dialog registry, or caches. At the inspected
commit, those boundaries centralize unrelated workflows or do not provide `rvw`'s explicit entry,
byte, viewport, and scroll-depth bounds.

Generic Bubbles lists, tables, and viewports do not create an application virtualization boundary:
they retain or rebuild the content supplied to them. `rvw` keeps direct tree, diff, and comment
renderers because those renderers materialize review-domain windows before presentation.

`rvw` also keeps its bounded local comment editor. Its byte ownership, grapheme-safe projection,
and exact retained/construction accounting are product contracts rather than generic textarea
behavior.

Chroma or copied lexer grammars were rejected. A lexer would not replace revision identity,
cancellation, visible-line demand, byte accounting, multiline state, or safe fallback. The current
syntax pipeline prepares requested lines in bounded CPU work outside `Update` and `View`.

## Dependency decision rule

Use an installed companion component only when it deletes a complete local interaction
responsibility and preserves ownership, cancellation, byte, work, and viewport bounds. Keep direct
domain code when a wrapper would retain the same policy or create duplicate state ownership.
