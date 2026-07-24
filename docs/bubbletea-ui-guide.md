# Bubble Tea UI guide for `rvw`

This guide records the parts of Charm's `crush` UI that are useful to `rvw`, the parts that do not
fit `rvw`'s contracts, and the local design rules that follow. The comparison used
[`charmbracelet/crush@e4175c52`](https://github.com/charmbracelet/crush/commit/e4175c52b8e14a4e158106ac5988fd42a8958457)
from 2026-07-23. Both projects resolve Bubble Tea `v2.0.8`, Bubbles `v2.1.1`, and Lip Gloss
`v2.0.5`, so API comparisons do not require version translation.

## Conclusions

Use `crush` as evidence for interaction patterns, not as a component library or an architecture to
copy. Its strongest transferable ideas are:

- bindings carry matching keys and user-facing help together;
- keys are routed to a blocking dialog before the focused editor or view, then to global actions;
- concrete interactive components own local focus and input state;
- styles use semantic roles and one theme source rather than view-local colors;
- lexer and style construction are kept out of ordinary rendering where possible.

Do not copy its root model, diff renderer, or caches. At the inspected commit, the root UI file is
4,842 lines, the diff renderer walks from the first hunk to the viewport offset, and lexer, style,
and per-diff highlight caches have no explicit entry or byte bound. Those choices conflict with
`rvw`'s ownership and scale requirements.

## Native Bubble Tea flow

Keep one observable-state transition boundary:

```text
tea.Msg -> Model.Update -> Model + tea.Cmd
                              |
                              v
                     typed result message

Model.View -> complete tea.View
```

`Update` owns state changes. A command receives owned values or immutable snapshots and returns a
typed message. `View` observes state and constructs the complete terminal declaration; it does not
repair state, load data, tokenize files, start work, or wait.

`rvw` already follows this shape. Preserve its application-owned coordinator for bounded source,
patch, search, syntax, export, and publication work. Do not add an event bus, generic effect layer,
component registry, or second terminal owner.

## Component boundaries

Use a Bubbles component when it removes a complete interaction problem and its work remains
bounded. Use a direct concrete model when the behavior is small or has stronger product invariants
than the Bubble provides.

| Need | Decision in `rvw` | Reason |
| --- | --- | --- |
| Search and command input | Keep Bubbles `textinput` | It owns cursor, editing, focus, and v2 commands. |
| Effective key help | Keep Bubbles `help` | It renders the same active binding definitions used for matching. |
| Review-comment editor | Keep the local concrete editor | It enforces byte ownership, grapheme-safe editing, bounded row projection, and exact retained/construction accounting that `textarea` does not provide as an application contract. It still reuses `textarea.DefaultKeyMap` for editing conventions. |
| File tree, diff, comments | Keep direct models | They carry review identity, lazy materialization, and viewport bounds; generic list or viewport components would not remove those domain responsibilities. |
| Blocking decisions | Keep explicit overlays | One enum and one top-layer route make mutual exclusion and focus restoration visible. A dialog registry would add invalid states. |

Every updated child model must be retained and every child command must be captured. Hidden or
unfocused children do not receive ordinary keys. A child command is suppressed only when stopping
an inactive command chain is part of the child contract.

## Keybindings and focus

One binding definition must own four facts:

1. the context and optional focused pane where the binding is active;
2. the normalized chord used for matching;
3. the domain action;
4. the user-facing chord and action description shown in help.

Route a key once in this order:

1. minimum-size or blocking overlay;
2. focused editor or input;
3. focused pane in the active view;
4. deliberately reserved global action.

Do not forward a printable key through an editor and reinterpret it globally. Keep focus as one
closed value, reconcile it when responsive layout hides a pane, and restore the prior valid owner
when a modal closes.

`crush/internal/ui/model/keys.go` demonstrates the value of `key.Binding`: matching and visible help
share data. Its root handler also routes open dialogs before ordinary focused views. `rvw` should
keep those properties while retaining its richer requirements: user overrides, multi-stroke chords,
numeric repeats, focus-specific scopes, completeness validation, and deterministic ordering.

The configuration package and application package must not independently enumerate contexts,
actions, or allowed context/action pairs. A small shared keymap domain package is the canonical
owner. Configuration merges and validates user input through that package; the application compiles
the validated result into its runtime lookup table.

## Visual composition

The visual thesis is:

> A dense review workspace with the diff as the primary task locus, quiet tree and comment context,
> one accent family, stable marker columns, and focus shown without geometry changes.

Apply it as follows:

- Give each visible pane a stable header. Reserve the same focus-marker cells for focused and
  unfocused states so `Tab` never moves content.
- Put the active path in the diff header. Put source/workspace revision and capacity facts in status
  details, not in the primary title.
- Keep the footer to current activity plus at most four context-valid actions. The full overlay
  owns the rest of the keymap.
- Render friendly descriptions such as `Enter open` and `? show keybindings`; never expose
  configuration slugs such as `toggle-file-reviewed` as primary UI copy.
- Use position, reserved markers, bold/underline, and text labels as non-color cues. Color is an
  enhancement, not the only representation of focus, selection, modification, or severity.
- Keep one separator between independently scrolling panes. Do not add nested boxes.
- At compact and narrow sizes, remove secondary panes and metadata before reducing the diff below
  its useful width.

## Rendering and performance

Frame work must be proportional to the terminal viewport plus fixed overscan.

- Window tree nodes, diff rows, comments, search results, and help entries before styling them.
- Clip hostile or pathological external text before expensive grapheme and ANSI processing.
- Never build a full styled document and then ask a viewport to discard most of it.
- Keep `tea.Batch` size independent of repository or result size.
- Precompute ordered effective scopes and fixed footer summaries when compiling settings; project
  only the visible help entries per frame and never rebuild or sort the complete keymap there.
- Bound retained content, construction scratch, concurrent work, queues, progress cadence, and
  cache bytes separately.

`crush/internal/ui/diffview` is not suitable for reuse here. Its rendering loop skips rows by
walking from the beginning to `yOffset`, and its syntax cache grows once per distinct line and
background. `rvw` already has the correct structural boundary: patch rows and requested syntax
lines are materialized around the viewport before render-time styling.

## Syntax highlighting

Keep the current bounded syntax pipeline for now. Do not add Chroma and do not copy a lexer from it.

The existing pipeline has properties required by `rvw`:

- syntax work runs in the CPU coordinator, not `Update` or `View`;
- requests contain only visible old/new line numbers;
- results are revision- and content-identity-bound;
- result spans, retained bytes, construction bytes, line bytes, and requested lines are capped;
- cancellation is polled during blob scans and long lines;
- unavailable or limited highlighting degrades to plain sanitized text;
- rendering stops pulling spans after the visible content boundary.

The current lexer is intentionally shallow and language-agnostic: it recognizes common keywords,
quotes, numbers, and `//` comments. Chroma would improve language accuracy, but the import would not
replace the coordinator, identity, accounting, or visible-span projection. It would add a large
generated lexer registry and would require whole-source, stateful tokenization or a new bounded
incremental grammar contract. `crush`'s Chroma caches are also unbounded and cannot be copied.

Copying selected Chroma lexers is worse: it creates a forked grammar and update/security burden
without reducing the required boundary code. Add a syntax dependency only after the product names
specific languages and accuracy cases, and only with benchmarks and an explicit design for:

- grammar identity in the cache key;
- bounded tokenizer input, output, scratch, and cache bytes;
- multiline lexical state at arbitrary visible windows;
- cancellation latency;
- binary-size and build-time cost;
- safe fallback for unknown, generated, or malformed files.

The useful near-term syntax improvement is presentation, not a new lexer: keep syntax colors in the
semantic theme, preserve diff backgrounds, and ensure syntax never obscures selection, comments,
or addition/deletion meaning.

## Dependency versus local code

Use this decision order for UI work:

1. Is the behavior already supplied by the exact installed Bubbles/Lip Gloss version?
2. Does using it delete a complete local responsibility rather than wrap it?
3. Can it satisfy `rvw`'s ownership, cancellation, byte, work, and viewport bounds?
4. Is the dependency's semantic and binary surface smaller than the local code it replaces?
5. If copying code, can `rvw` own its correctness and updates without tracking an upstream fork?

Import when the dependency removes more policy and edge cases than it introduces. Keep direct local
code when the behavior is domain-specific or the dependency cannot enforce required bounds. Do not
copy substantial upstream code merely to avoid a module entry.

## Review checklist

- Does every key have one owner, and do matching and help use the same binding?
- Is focus visible without relying on hue or changing geometry?
- Does the diff remain the dominant region at every usable size?
- Are child models retained and child commands returned?
- Is every `tea.View` branch complete and consistent?
- Are blocking, focused, active-view, and global routes explicit in that order?
- Is frame construction bounded by viewport plus overscan?
- Are background work, result bytes, cache bytes, and shutdown bounded?
- Did a new dependency delete enough local code and policy to pay for itself?
- Were minimum, compact, normal, wide, focused, modal, empty, dense, Unicode, and monochrome states
  rendered and inspected?
