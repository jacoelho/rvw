# Terminal presentation

## 12. Interaction

The normal view contains a file tree, diff, comments, and status. Focus is explicit and cycles
between visible panes. The active file, focused pane, cursor, selections, pending work, stale state,
errors, review marks, comment anchors, and source identity remain distinguishable in color and
monochrome modes.

Main default controls are:

| Keys | Action |
| --- | --- |
| `j` / `k`, arrows | Move down/up |
| `Ctrl-f` / `Ctrl-b`, Page Down/Up | Move one page |
| `Ctrl-d` / `Ctrl-u` | Move half a diff page |
| `g g` / `G` | First/last row |
| `Tab` / `Shift-Tab` | Cycle pane focus |
| `Enter` | Activate the selected file or row |
| `r` / `R` | Toggle current file/current hunk review state |
| `Space` | Toggle a file-tree directory |
| `] f` / `[ f` | Next/previous file |
| `] h` / `[ h` | Next/previous hunk |
| `] c` / `[ c` | Next/previous comment |
| `/`, `n`, `N` | Search; next/previous result |
| `c` / `C` | Comment at cursor/on active file |
| `v` / `V` | Select a line range/hunk |
| `e` / `d d` | Edit/delete focused comment |
| `w` / `t` | Toggle wrapping/diff layout |
| `Ctrl-r` | Reload source |
| `y`, `M`, `J` | Copy Markdown/write Markdown/write JSON |
| `s` | Open GitHub submission |
| `F2` | Open status details |
| `?` | Open effective help |
| `:` | Open command prompt |
| `q` / `Ctrl-c` | Quit/interrupt |

Comment editing uses `Ctrl-s` to save and `Esc` to cancel. An explicitly configured valid binding
takes precedence; otherwise `Tab` and `Shift-Tab` are delivered to the focused textarea.
Every accepted textarea update, including bracketed paste, is synchronized into the domain draft
before the next message. A rejected byte or domain limit restores the prior editor contents and
logical cursor while preserving the textarea's returned command. A paste above 64 KiB is rejected
before widget update, and a minimum-size or blocking overlay suppresses paste mutation of hidden
editors. Search and command prompts apply the same pre-widget paste bound and transactional byte
limit.
Multi-key chords time out after 750 milliseconds. Numeric repeats range from 1 through 9,999.
Chord/repeat state resets on context changes, invalid continuation, or timeout. Effective help
reflects user overrides. Search and command-prompt input are each limited to 64 KiB; reaching the
limit is visible and accepts no further input until the user deletes content or cancels.

In the comments pane, `j` and `k` change only the selected comment. `Enter` activates that
selection. `] c` and `[ c` select and activate the next or previous comment in one operation,
clamping at the first and last comment; they remain available when the comments pane is hidden.
Activation focuses the diff and reveals the comment's mapped file and line. A range targets its
inclusive end line. File comments reveal the file at its first patch row. Review comments and
unmappable or ambiguously mapped remote comments preserve the current diff and report why no jump
is possible. Stale local comments and outdated remote threads reveal the mapped file at its first
patch row and report that the exact line cannot be selected.

The command prompt accepts:

```text
:q
:q!
:reload
:diff
:copy
:export
:json
:submit
:ack-submit
:help
:filter <pattern>
:search-all <text>
:search-cancel
:context [non-negative-lines]
:old-line <positive-line>
:path <path>
:set wrap
:set nowrap
:set wrap!
:<positive-line>
```

Unknown commands and invalid arguments produce a visible warning without leaving the TUI.
`:ack-submit` may acknowledge a displayed publication fact but MUST NOT clear an unknown attempt,
release its retry fence, or manufacture a reconciled outcome.

Blocking layers include help, status details, confirmation, export selection, submission preflight,
and minimum-size fallback. A blocking layer owns input while active. Dirty quit, destructive
comment deletion, reload that would discard state, export destination, and GitHub publication use
explicit confirmation or selection.

When the terminal is below 20 columns or below 6 rows, only configured quit and interrupt bindings
are accepted. Resizing restores the prior interaction state.

Responsive tiers are:

| Tier | Geometry | Required composition |
| --- | --- | --- |
| Wide | at least 180 columns and 30 rows | file tree, dominant diff, comments, compact status/help |
| Normal | at least 120 columns and 24 rows | file tree and dominant diff; comments remain reachable as a pane or blocking layer |
| Compact | at least 80 columns and 18 rows | dominant diff plus one user-selected context pane; abbreviated status/help |
| Narrow | at least 20 columns and 6 rows | one task region, status, and effective task/quit controls |
| Below minimum | less than 20 columns or less than 6 rows | size requirement plus effective quit/interrupt controls only |

The first matching row wins. The unified diff requires 60 content columns. Side-by-side rendering
requires 100 content columns; below that width it temporarily renders unified with a visible
limitation while preserving the selected side-by-side preference. A visible file tree receives at
least 24 columns and a visible comments pane at least 32 columns. Optional labels, repeated help,
secondary metadata, comments, and the tree are removed in that order before the diff falls below
its useful geometry. Hidden panes retain selection, focus-return target, and scroll identity.

Old-side and new-side line numbers each have a fixed adjacent comment-anchor cell. The cell shows
`●` exactly when that source line has at least one saved, mapped comment: non-stale local line or
range comments and non-outdated remote line threads, including resolved threads. Range comments
anchor at their inclusive end. Drafts, file and review comments, stale local comments, outdated
remote threads, and missing or ambiguous remote mappings do not produce a line anchor. Wrapped
continuation rows reserve the cells but leave them blank. Anchor emphasis remains visible without
color. These fixed cells are rendering chrome and do not count toward the 60- and 100-column
content-width requirements.

## 13. Rendering

Frame construction and rendering are proportional to viewport cells plus fixed overscan, not total
repository, patch, line, comment, tree, or search-result size.

The visual thesis is a dense review workspace with the diff as the primary task locus, quiet tree
and comment context, one accent family, stable marker columns, and focus shown without geometry
changes. Space and at most one boundary between independently scrolling regions establish
hierarchy; nested decorative boxes are not used.

The renderer:

- clips before expensive text processing;
- never emits raw control, escape, or bidirectional-override text from external data;
- treats invalid UTF-8 paths as displayable escaped bytes while preserving identity;
- expands tabs to the next four-cell stop;
- preserves printable UTF-8, including wide characters, combining marks, and grapheme clusters;
- renders invalid bytes and ASCII controls as visible uppercase `\xNN` escapes and non-ASCII
  controls and bidirectional overrides as visible uppercase `\u{NNNN}` escapes;
- bounds work for a single pathological grapheme or line;
- keeps unified and side-by-side line identity, old/new numbers, hunks, gaps, comments, review
  state, and selections unambiguous;
- keeps syntax highlighting optional, bounded, cancellable, revision-bound, and off the event loop;
- degrades to plain text when highlighting is unavailable or limited;
- never uses cache or highlight readiness to change review semantics.

Syntax work and cache identity is the pair `(content identity, syntax selector)`. Results for
different selectors never share identity. A limited result is not retried automatically until that
identity changes.

Status presentation is deterministic and preserves simultaneous progress, warning, error, stale,
limited, cancellation, delivery, and publication facts in the status-details layer even when the
compact status bar can show only one summary.
