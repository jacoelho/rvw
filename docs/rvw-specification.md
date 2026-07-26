# rvw specification

**Status:** Draft candidate for explicit acceptance.

**Scope:** Product behavior, architecture, source handling, runtime lifecycle, resource bounds,
failure semantics, and verification for `rvw`, with presentation requirements incorporated from
[`tui-design.md`](tui-design.md).

This specification owns product and runtime contracts. [`tui-design.md`](tui-design.md) is its sole
incorporated presentation authority. Neither document incorporates an implementation file, commit,
plan, or historical decision. Their exact contents remain proposals until explicitly accepted.

**Reconciliation provenance:** This Go-native revision was reconciled from the Rust draft at
`../rvw-rewrite/docs/rvw-specification.md`. The source draft's SHA-256 digest is
`ae2dea93fa1e3b2562250c1df91637ee4554ad2b20c1c20a203d77222c1bfb80`. The digest identifies input
evidence only; the Rust draft is neither an accepted contract nor an implementation blueprint.

## 1. Interpretation

`MUST`, `MUST NOT`, `SHOULD`, and `MAY` identify proposed normative requirements.

The following rules resolve ambiguity:

1. Correctness, data integrity, explicit failure, bounded work, and safe ownership outrank
   compatibility or convenience.
2. Required observable behavior is stated here. Missing requirements cannot be inferred from an
   implementation or test.
3. Unavailable, stale, cancelled, limited, failed, and ambiguous outcomes are distinct states.
4. A verification route is not evidence that the corresponding check has passed.
5. Internal organization MAY change without changing this contract, provided ownership, dependency,
   boundedness, lifecycle, and observable behavior remain conformant.

## Product surface

### 2. Product and platform

`rvw` is a terminal application for reviewing changes from exactly four source kinds:

1. the current worktree, including untracked files by default;
2. staged changes;
3. an explicit two-dot Git range;
4. a GitHub pull request.

It has exactly three output modes:

1. an interactive terminal UI, which is the default;
2. human-readable noninteractive output selected by `--stdout`;
3. machine-readable JSON selected by `--json`.

Linux and macOS are supported. Windows and WebAssembly are not supported.

The product MUST remain usable with at least 1,000 changed files, files of at least 10,000 lines,
and hundreds of comments. Memory, concurrent work, retained data, queues, per-tick work, frame
construction, and rendering MUST remain bounded.

Patch or stdin-patch sources, additional output modes, review persistence, tabs, chat, agents,
plugins, and generic source registries are outside the product.

### 3. Command line

#### 3.1 Accepted forms

```text
rvw diff [--staged | --range <base..head>] [common options]
rvw pr <positive-number | canonical-pr-url> [--repo <owner/repository>] [common options]

```

Help is accepted as `rvw help`, by `-h` or `--help` globally, and by `-h` or `--help` for either
subcommand.

Common options are:

```text
--stdout
--json
--no-mouse
--theme <name>
--appearance <dark|light|system>
--include <pattern>
--exclude <pattern>
```

`--include` and `--exclude` are repeatable and may occur before or after the subcommand and its
arguments. `--stdout` and `--json` select the output mode; when repeated, the last occurrence wins.

`diff` without `--staged` or `--range` selects the worktree. `--staged` and `--range` are mutually
exclusive.

#### 3.2 Validation

A range MUST contain exactly one usable `..` separator and MUST NOT contain `...`. Base and head
must both be nonempty, neither may begin with `-` or contain NUL, base may not end in `.`, and head
may not begin with `.` or contain another `..`.

A pull-request selector MUST be a decimal integer in the inclusive range
`1..18_446_744_073_709_551_615` or an exact URL of this form:

```text
https://github.com/<owner>/<repository>/pull/<positive-u64>
```

An owner contains only ASCII letters, digits, and `-`, is nonempty, and does not begin or end with
`-`. A repository contains only ASCII letters, digits, `-`, `_`, and `.`, is nonempty, and is not
`.` or `..`. `--repo` uses exactly `<owner>/<repository>`.

Theme names are 1 through 255 UTF-8 bytes, contain no NUL, `/`, or `\`, and are not exactly `.` or
`..`. Other dots are valid.

Arguments other than filesystem path bytes MUST be valid UTF-8. Invalid selectors, ranges,
repositories, missing values, unknown options, and invalid configuration MUST fail before source
or terminal composition. Help exits successfully. Other startup failures write terminal-safe text
prefixed by `rvw:` to stderr and exit with status 1.

#### 3.3 Comment initialization from stdin

When stdin is not a terminal, `rvw diff` and `rvw pr` inspect it before source acquisition. Zero
bytes mean no comment initialization and preserve ordinary terminal admission. Any nonempty input
MUST be one UTF-8 JSON document of at most 16 MiB with this exact top-level shape:

```json
{"comments":[{"target":{"type":"review"},"binding":"active","comment":"Review body"}]}
```

Objects reject duplicate, unknown, missing, and target-inapplicable fields. Trailing JSON documents
are rejected; trailing JSON whitespace and object-field order are insignificant. `binding` MUST be
`active`; `stale` and `orphaned` exports are not valid initialization state. Comment bodies use the
ordinary non-whitespace, UTF-8, per-body, aggregate-body, and count limits.

Accepted targets are:

- `{"type":"review"}`;
- `{"type":"file","path":<path>}`;
- `{"type":"line","path":<path>,"side":"old|new","line":<positive-u32>}`;
- `{"type":"range","path":<path>,"side":"old|new","start":<positive-u32>,"end":<positive-u32>}`,
  where `start < end`.

Exported path strings decode with the inverse of the JSON export's raw-byte-preserving encoding.
Paths MUST exactly match raw `File.IdentityPath` bytes and MUST identify an active file after
filtering. A line or range endpoint MUST be present on the stated side of the patch materialized
with configured diff context. Each referenced patch is materialized at most once during
initialization. Missing, filtered, unavailable, limited, cancelled, or otherwise inapplicable
targets fail atomically before output or TUI startup.

Initial comments append after imported pending-review comments and are ordinary dirty local
comments with local IDs and no GitHub binding. They are editable, deletable, reloadable, exportable,
and eligible as additions in an explicit pull-request publication. Initialization itself performs
no local persistence and no GitHub write.

`--stdout` and `--json` perform the same parsing and applicability validation but retain their
existing bytes and schema. For default interactive output with a nonempty seed document, terminal
stdout and `TERM` admission still apply and the program MUST open the controlling terminal for TUI
input before source work. Failure to open it fails startup. Help never consumes stdin. Every invalid
or inapplicable nonempty input writes a descriptive stderr diagnostic, produces no review output,
and exits with status 1.

### 4. Configuration and themes

#### 4.1 Precedence and schema

Configuration is UTF-8 TOML. Each configuration file is limited to 1 MiB. Unknown fields and
invalid values fail startup.

Precedence, lowest to highest, is:

1. built-in defaults;
2. `$XDG_CONFIG_HOME/rvw/config.toml`, or `$HOME/.config/rvw/config.toml`;
3. the first existing project file among `.rvw.toml` and `rvw.toml`;
4. command-line overrides.

The top-level keys `mouse`, `theme`, `theme_dark`, `theme_light`, and `appearance` are aliases for
their `[ui]` equivalents. When both forms occur in one file, `[ui]` wins. The complete table schema
is:

```toml
[ui]
mouse = true
theme = "gruvbox-dark"
theme_dark = "gruvbox-dark"
theme_light = "gruvbox-light"
appearance = "system" # dark, light, or system

[diff]
wrap = true
view = "unified" # unified or side-by-side
context_lines = 3
include_untracked = true

[filter]
builtin_defaults = true
gitignore = true
use_gitignore = true # alias of gitignore; gitignore wins when both occur
rvwignore = true
ignore_file = ".rvwignore"
include = ["src/**"]
exclude = ["**/*.generated.rs"]

[github]
submit_default = "comment" # comment, approve, or request-changes
fetch_threads = true

[export]
markdown_template = "# Review ({{comment_count}})\n\n{{comments}}"
comment_template = "## {{target}}\n\n{{comment}}"
```

`include` and `exclude` each accept either one string or an array of strings.

Defaults are mouse enabled, wrapping enabled, unified view, three context lines, untracked files
included, built-in filtering enabled, `.gitignore` enabled, `.rvwignore` enabled, remote threads
fetched, and submission event `comment`.

Filter patterns are nonempty, contain no NUL, and are at most 4,096 UTF-8 bytes each. At most 4,096
include/exclude patterns are accepted across the merged configuration and command line.
`context_lines` is an integer from 0 through 10,000. `ignore_file`, when set, is a single path
component that cannot escape the repository root. Independent hidden reasons accumulate.
Filtering changes presentation, not membership of the source snapshot.

Export templates use `{{name}}` placeholders, are each at most 256 KiB, and compile to at most 4,096
literal-or-placeholder tokens. Invalid, unknown, unmatched, or unterminated placeholders fail
configuration rather than rendering literally.

`markdown_template` accepts:

- `comments`: selected comments rendered by `comment_template`, separated by newlines, or
  `_No comments._` when none are selected;
- `comment_count`: the number of selected comments.

`comment_template` accepts:

- `index`: one-based selected-comment index;
- `target`: `review`, an escaped file path, or `<path>:<line-or-range>`;
- `path`: the escaped target path, empty for a review-level comment;
- `line_or_range`: a new-side line or inclusive range, or the same prefixed by `~` for the old
  side; empty for review- and file-level comments;
- `comment`: the Markdown-escaped comment body.

The defaults are:

```toml
markdown_template = "I reviewed your code and have the following comments. Please address them.\n\n{{comments}}\n"
comment_template = "{{index}}. `{{target}}` - {{comment}}"
```

#### 4.2 Keybindings

Keybindings are allowed only in user configuration. Project keybindings are rejected. Each
operation has a context, optional focus, chord, and exactly one of `action` or `unbind = true`:

```toml
[[ui.keybindings]]
context = "normal"
focus = "diff"
chord = "J"
action = "move-down"
```

Accepted contexts are:

```text
normal, normal.tree, normal.diff, normal.comments,
visual, comment-edit, search, command, help, status-details, confirm,
export-picker, submit-preflight, minimum-size
```

Only `normal` accepts `focus`, whose values are `file-tree`, `diff`, or `comments`. The inline
`normal.*` forms already supply focus and cannot also use the `focus` key.

Accepted action names are:

```text
quit, focus-next-pane, focus-previous-pane, move-up, move-down,
scroll-left, scroll-right, first, last, page-up, page-down,
half-page-up, half-page-down, next-file, previous-file, next-hunk,
previous-hunk, next-comment, previous-comment, next-search-hit,
previous-search-hit, comment-at-cursor, file-comment, summary-comment,
edit-comment, delete-comment, expand-context, toggle-file-reviewed,
toggle-hunk-reviewed, activate, accept, toggle-wrap, toggle-view,
align-top, align-center, align-bottom, toggle-directory, expand-directory,
collapse-directory, visual-line, visual-lines, visual-hunk,
comment-selection, command, filter, help, status-details, search,
open-submit, cancel, interrupt, delete-backward, confirm-yes, confirm-no,
save-comment, insert-newline,
extend-up, extend-down, next-help-page, previous-help-page, help-line-up,
help-line-down, help-page-up, help-page-down, help-home, help-end,
toggle-export-unmappable,
export-selected, copy-markdown, export-markdown, export-json,
submit-recheck, previous-submit-event, next-submit-event,
submit-review, reload, toggle-file-panel,
toggle-comments, toggle-hidden-files
```

A configuration contains at most 4,096 keybinding entries. A chord contains one through four
strokes and at most 128 bytes. `Ctrl-c` is reserved. Unknown, ambiguous, prefix-conflicting,
unreachable, or incomplete maps fail startup. Required escape, confirmation, submission, and
minimum-size actions cannot be removed.

#### 4.3 Themes and color

Curated built-in UI aliases are:

```text
dark, light, ansi16-dark, tokyo-night-storm, tokyo-night-day,
gruvbox-dark, gruvbox-light, catppuccin-latte, catppuccin-frappe,
catppuccin-macchiato, catppuccin-mocha
```

`default` resolves to `gruvbox-dark` for dark/system appearance and `gruvbox-light` for light
appearance. System appearance resolves as dark.

The exact embedded syntax-theme identifiers below are also valid theme names and use the selected
appearance's fallback UI palette:

```text
base16-ocean.dark, base16-eighties.dark, base16-mocha.dark,
base16-ocean.light, InspiredGitHub, Solarized (dark), Solarized (light)
```

Resolution order is curated alias, embedded syntax-theme identifier, then the user theme file.
A built-in name therefore cannot be shadowed by a user file.

A user theme named `mine.v2` is loaded from
`$XDG_CONFIG_HOME/rvw/themes/mine.v2.toml`, or `$HOME/.config/rvw/themes/mine.v2.toml`. The theme
file and an optional syntax theme file are each limited to 1 MiB.

The required theme keys are:

```text
panel_bg, fg_primary, fg_secondary, fg_dim, diff_add, diff_del,
diff_context, pending, border_focused, status_bar_bg, cursor_line_bg,
cursor_line_fg
```

The optional keys are:

```text
diff_add_bg, diff_del_bg, diff_hunk_header, file_modified, file_renamed,
branch_name, message_warning_fg,
message_warning_bg, syntax_theme
```

Colors are `#RRGGBB` or one of:

```text
reset, black, red, green, yellow, blue, magenta, cyan, gray, grey,
darkgray, darkgrey, lightred, lightgreen, lightyellow, lightblue,
lightmagenta, lightcyan, white
```

Hyphens, underscores, spaces, and ASCII case are ignored in named colors. `syntax_theme` is a
nonempty relative path beneath the user themes directory, contains no NUL, and contains no parent,
root, or platform-prefix component. It names a TextMate theme.

A nonempty `NO_COLOR` disables color only when no `theme`, `theme_dark`, or `theme_light` value is
configured. Any configured theme value explicitly enables color. Required monochrome distinctions
are defined in [`tui-design.md`](tui-design.md#color-and-contrast).

Custom themes are validated across the supported TrueColor, ANSI256, and ANSI16 projections using
the contrast requirements and diagnostics defined in
[`tui-design.md`](tui-design.md#color-and-contrast).

### 5. Noninteractive output

Opening for `--stdout` or `--json` MUST produce a complete snapshot. A GitHub files-API cap requires
recovery before output; a truncated snapshot must not be represented as complete.

#### 5.1 Human output

Local output begins:

```text
<file-count> changed files
```

Pull-request output begins:

```text
#<number> <file-count> changed files
```

Following lines contain the display path of each visible file in snapshot order. Hidden files count
toward the total but are not listed. When stdout is a terminal, controls, directional overrides,
and unsafe path bytes are escaped.

#### 5.2 JSON output

JSON is one object followed by a newline. It contains every file, including hidden files. When
stdout is a terminal, unsafe control and bidirectional Unicode scalars are emitted as JSON escapes.

The top-level schema is:

```text
{
  source: "diff" | "pr",
  source_id: positive integer,
  source_details: LocalSource | PullRequestSource,
  file_count: non-negative integer,
  files: File[],
  github?: GitHub
}
```

`source_id` and `patch_handle` are public v1 JSON fields, not coordinator task identities.
`source_id` identifies the one source snapshot represented by the document. A `patch_handle`
identifies a file's lazily materializable patch within that snapshot. Their numeric values are
opaque outside the document and MUST NOT be reused to infer identity across invocations.

`LocalSource` is:

```text
{
  kind: "local-worktree" | "local-staged" | "local-range",
  repo: string | null,
  base: string | null,
  head: string | null
}
```

For worktree, `head` is `"worktree"`; for staged, `head` is `"index"`; for a range, base and head are
the supplied revisions.

`PullRequestSource` is:

```text
{
  kind: "github-pr",
  owner: string,
  repo: string,
  number: positive integer,
  title: string,
  base: object-id string,
  head: object-id string,
  base_ref: string,
  head_ref: string
}
```

For pull requests, `github` is also present:

```text
{
  owner_repo: string,
  number: positive integer,
  title: string,
  url: string,
  state: "OPEN" | "CLOSED" | "MERGED",
  is_draft: boolean,
  author: string | null,
  base_ref: string,
  base_oid: object-id string,
  head_ref: string,
  head_oid: object-id string
}
```

Each `File` is:

```text
{
  id: zero-based integer,
  old_path: UTF-8 string | null,
  new_path: UTF-8 string | null,
  old_path_display: escaped string | null,
  new_path_display: escaped string | null,
  old_path_bytes_base64: Base64 string | null,
  new_path_bytes_base64: Base64 string | null,
  change: Change,
  flags: Flags,
  patch_handle: {
    id: one-based integer,
    source_id: positive integer,
    file_id: zero-based integer
  }
}
```

UTF-8 path fields are null for non-UTF-8 paths. Display fields preserve readable Unicode and escape
backslashes, invalid bytes, controls, and bidirectional overrides. Base64 fields preserve exact
path identity.

`Change` is one of:

```text
{ kind: PathKind, classification: "unclassified", additions: integer, deletions: integer }
{ kind: PathKind, additions: integer, deletions: integer, hunks: integer, mode: Mode | null }
{ kind: "binary" | "rename-only" | "mode-only", mode: Mode | null }
{ kind: "submodule", old_commit: string | null, new_commit: string | null }
{
  kind: "object-type-changed",
  additions: integer,
  deletions: integer,
  hunks: integer,
  old: GitObject,
  new: GitObject
}
```

`PathKind` is `added`, `deleted`, `modified`, `renamed`, or `copied`. Unmerged and ordinary
type-changed paths use `modified` unless classified as `object-type-changed`.

`Mode` is `{ old: six-digit octal string, new: six-digit octal string }`. `GitObject` is
`{ kind: "gitlink" | "blob", oid: string, mode: six-digit octal string }`.

`Flags` is:

```text
{
  generated: boolean,
  vendored: boolean,
  lock_file: boolean,
  minified: boolean,
  too_large: boolean,
  hidden_reasons: HiddenReason[]
}
```

`HiddenReason` is `builtin-default`, `gitignore`, `rvwignore`, `include`, or `exclude`.

All JSON numeric fields use a base-10 JSON integer token with no sign for nonnegative values, no
fraction, and no exponent:

| Field | Inclusive range |
| --- | ---: |
| PR selector and every PR `number` | 1 through 18,446,744,073,709,551,615 |
| `source_id`, `patch_handle.id`, `patch_handle.source_id` | 1 through 18,446,744,073,709,551,615 |
| `file_count` | 0 through 100,000 |
| `File.id`, `patch_handle.file_id` | 0 through 99,999 |
| `additions`, `deletions` | 0 through 1,000,000 |
| `hunks` | 0 through 100,000 |

The full-width identifiers exceed IEEE-754's exact integer range. Consumers MUST use an
arbitrary-precision or unsigned-64-bit JSON number representation; v1 does not encode them as
strings. Values outside the table fail validation before output.

## Domain and source

### 6. Architecture and ownership

`rvw` is a pure Go program. It MUST NOT link Rust, require CGO, invoke a prior `rvw` executable, or
mechanically reproduce the Rust crate, task, event-loop, or renderer structure.

The interactive application uses:

- `charm.land/bubbletea/v2` at `v2.0.8`;
- `charm.land/bubbles/v2` at `v2.1.1`;
- `charm.land/lipgloss/v2` at `v2.0.5`.

The resolved module graph in `go.sum` is part of the accepted dependency baseline. A dependency
upgrade is an architecture change and MUST reverify the version-specific lifecycle and signal
contracts in sections 10 and 11.

The pinned upstream modules declare `github.com/charmbracelet/x/windows` in their module metadata.
`rvw` does not import that package, compile its Windows files, add Windows behavior, or support a
Windows target. Requiring Bubble Tea v2 makes that unreachable transitive module metadata
unavoidable. Acceptance of this document interprets the Linux/macOS-only constraint as forbidding
direct or runtime Windows code and dependencies, not as rewriting the upstream module graph.

The Go ownership boundaries are:

```text
cmd/rvw
  -> internal/config
  -> internal/delivery
  -> internal/output
  -> internal/source
  -> internal/change
  -> internal/tui

internal/tui -> internal/change, internal/coordinator, internal/projection, internal/source,
                internal/delivery
internal/coordinator -> internal/change, internal/source
internal/projection -> internal/change
internal/output -> internal/change, internal/source
internal/source -> internal/change, go-git plumbing, GitHub HTTPS
```

`cmd/rvw` parses CLI input, composes dependencies and distinct program/work contexts, opens the
initial snapshot, selects interactive or noninteractive output, owns the one `tea.Program` and the
one application termination-signal registration, and maps final errors to exit status. It contains
no review policy.

`internal/config` alone discovers, reads, validates, and merges bounded user/project configuration
and themes. `internal/output` streams the human and JSON contracts to an `io.Writer`; it never
constructs or starts a TUI.

`internal/change` owns validated identities, source and workspace generations, files, selection,
marks, comments, drafts, reload reconciliation, export/publication state, and pure transitions. It
imports no terminal, filesystem, network, subprocess, clock, or concurrency package. It exposes
concrete domain operations and immutable snapshots, not a generic effect interface.

`internal/source` is the sole Git review-data, GitHub HTTPS/smart-Git, snapshot, patch/blob, reload,
search-input, and GitHub publication boundary. It never invokes `git` or `gh`. A
`repositorySession` owns local repositories and immutable pull-request object layers; a
`githubClient` owns bounded REST, GraphQL, authentication, pagination, and publication. Source
configuration is passed explicitly through `ServiceOptions`; `GITHUB_TOKEN` is the only GitHub
credential admitted by the command boundary. `Service.Open` returns immutable `Opened` facts and
a separate opaque `Revision` resource owner. Retiring and closing that owner does not invalidate
the opened facts. Patch and syntax acquisitions are shared one-shot leases: copying a lease cannot
consume or close its payload twice.

`internal/delivery` owns the clipboard permit and its commit state. Its private bounded helper may
execute only `pbcopy`, `wl-copy`, or `xclip`; source acquisition, stdout, and publication cannot use
it. The TUI returns a prepared stdout value after `tea.Program` restores the terminal.

`internal/projection` owns source-bound immutable file/path indexes and construction-time retained
byte accounting. `internal/coordinator` owns source revision transfer, required-work fairness, and
CPU admission. The remaining concrete queue orchestration stays adjacent to the TUI while its
feature-specific work types still depend on workspace projection values.

`internal/tui` owns the one root Bubble Tea model, concrete child Bubbles, effective key bindings,
responsive layout, semantic Lip Gloss styles, command constructors, and the closed coordinator
described in section 10. `internal/tui/render` owns terminal-safe text cells, tab expansion, and
bidirectional-control escaping; feature-specific viewport composition remains beside the model.

Reverse edges, same-level cycles other than the listed edges, and imports that bypass the effect
owner are forbidden. Public APIs expose domain operations and validated values, not internal
storage, component registries, queues, worker handles, process handles, or renderer caches. A new
abstraction requires a current producer and consumer. Compatibility facades, duplicate execution
paths, generic source registries, generic effect systems, component registries, and a second event
loop are forbidden.

### 7. Review model

One review owns:

- immutable changed-file identity and metadata for one source revision;
- active file and navigation state;
- per-file and per-hunk reviewed marks;
- review-, file-, and line-anchored comments;
- comment drafts and configured kinds;
- reload, search, export, and publication state;
- explicit notices and failure states.

IDs are opaque typed identities. A file, hunk, comment, task, source revision, workspace revision,
export, delivery, or publication identity is not interchangeable with another.

Domain mutation enters through concrete pure review operations. Interactive observable-state
mutation enters only through the root Bubble Tea `Update` method; noninteractive composition uses
the same domain operations directly. Domain operations return updated values or explicit domain
results, not runtime effects. Rendering observes immutable bounded snapshots and cannot mutate
review state or perform effects.

Invalid targets, stale revisions, exhausted capacity, duplicate completion, and illegal state
transitions fail explicitly. A stale or late async result cannot mutate current review state.

Comments require non-whitespace bodies. Line comments preserve old/new side and source line.
Comments that cannot map to a GitHub review position remain valid local comments but are explicitly
unmappable for publication.

Comment initialization is one cancellable, source-bound atomic review transition. It allocates one
combined comment state, preserves pending-first ordering, enforces pending-plus-initial count and
body limits, assigns ordinary local IDs and canonical paths, rebuilds anchor and retained-byte
accounting, and marks every unbound initial comment dirty. Failure leaves the original review
unchanged.

Local filtering hides files without deleting them from local identity. Pull-request filtering is
an acquisition projection: omitted paths are absent from identity, comments, marks, counts, TUI,
stdout, JSON, export, reload, inline-comment mapping, and publication.

### 8. Source acquisition and materialization

Local sources are rooted by descriptor-based parent traversal and read with go-git plumbing plus a
minimal read-only filesystem adapter over pinned roots. Worktree and staged review resolve the
`HEAD` tree, or the empty tree when `HEAD` is unborn. Worktree review compares that base with a
bounded projection of the live index and rooted worktree; staged review compares the base with
ordinary stage-zero index entries and retains unmerged paths explicitly. Range review compares the
supplied base and head. Local and advertised remote object formats must be SHA-1.

Pull-request review fetches commit ancestry, trees, and selected blobs through filtered smart Git.
It uses depth 1,000, computes all best common ancestors, and deterministically chooses the smallest
raw SHA-1 when more than one is best. PR filters are built-in defaults plus explicit include and
exclude patterns only; `.gitignore` and `.rvwignore` are disabled. No more than 3,000 filtered
records or 6,000 selected blob OIDs are retained.

An open pull request whose metadata base differs from the advertised base branch is stale. Closed
and merged pull requests instead use their recorded base and head object IDs, including when the
base branch has advanced or been removed. Historical acquisition MUST require reachable-object
fetch support and MUST NOT substitute the current base branch tip.

GitHub REST and GraphQL requests use one bounded native HTTP client. `GITHUB_TOKEN` is required;
`GH_TOKEN`, `gh` configuration/keychains, anonymous access, and ambient credential discovery are
forbidden. Repository inference recognizes only canonical GitHub HTTPS and `git`-user SSH remotes
without ports, escaping, query data, or fragments.

External bytes are validated at acquisition boundaries. Paths preserve raw byte identity and have
a separately escaped display form. Object IDs, modes, refs, URLs, process results, GitHub payloads,
diff structure, and remote comment data are validated before entering review semantics.

Snapshot acquisition obtains file metadata before patch bodies. Patch text, rows, expanded context,
blobs, syntax results, search results, and review-tree segments are materialized lazily and in
bounded segments. Startup and every semantic active-file selection request materialization for
exactly the active file. Moving only the tree cursor does not activate a file, and adjacent-file
prefetch is forbidden. A terminal materialization outcome is retried only after explicit activation
or retry input.

The current visible viewport plus fixed overscan determines row and syntax preparation. A single
very long line, huge hunk, hostile Unicode sequence, or large hidden file cannot force an unbounded
scan or allocation in `Update` or `View`.

Search traverses all changed files in bounded batches, can be cancelled, and reports incomplete or
limited results explicitly. Tree construction and filter application are bounded and preserve
stable file identity.

Reload acquires a new source revision, reconciles surviving paths, comments, marks, selection, and
view anchors where unambiguous, and explicitly reports stale or unmappable state. The old revision
remains usable until replacement commits. Failure or cancellation leaves the prior review intact.
Replacement cannot commit across an observable export lease or an unresolved publication attempt.
Context changes serialize behind active search work that uses the same patch acquisition boundary.

### 9. Export and publication

Markdown and JSON export are prepared from an immutable review snapshot. Export options select
whether unmappable comments are included. Preparation is bounded and cancellable.

Interactive copy/write actions are in-session deliveries, not additional top-level output modes.

Interactive export targets are clipboard or stdout. Clipboard delivery is separate from export
preparation and owns the only delivery permit. A clipboard write that may have committed before
reporting failure remains fenced through physical worker cleanup and unavailable for the process
lifetime. Stdout is a prepared post-program result and has no delivery state. Reconciliation
applies only to GitHub publication.

GitHub publication first creates or synchronizes a pending review. It then requires a separate
submission confirmation with a selector for `comment`, `approve`, or `request changes`.
`github.submit_default` chooses only the selector's initial value. GitHub submission always uses
an internal validation check. The submission UI shows:

- the repository and pull-request target;
- the selected final event: comment, approve, or request changes;
- whether the next step synchronizes the pending draft or submits it;
- included and excluded comment counts;
- concise pending-review, conflict, unmappable-comment, and stale-head warnings.

Only an open pull request exposes a GitHub publication target. Closed and merged pull requests
remain available for local review and export, and publication preflight MUST reject a pull request
that becomes closed or merged after its source revision was opened.

Interactive publication includes every eligible comment, excludes comments that cannot be mapped
safely, and rejects a stale pull-request head. It does not expose canonical request JSON, payload
byte ranges, operation markers, or database identifiers in the submission UI.

Publication distinguishes:

1. rejected or failed before a remote commit;
2. committed successfully;
3. unknown after a commit may have occurred.

An unknown result blocks retry. Reconciliation checks remote state using a stable publication
marker. Only confirmed absence permits retry; confirmed presence completes locally without
republishing. Cancellation cannot erase an unknown result or bypass reconciliation.

Every attempt marker has this exact form:

```text
<!-- rvw-publication:v1:<32-lowercase-hex-digits> -->
```

The value is 128 bits generated from the operating system cryptographic random source before the
pre-commit boundary. Publication includes the marker in the GitHub review body, including when the
visible summary is empty. Reconciliation enumerates every review for the immutable repository and
pull-request target and compares the exact marker. Incomplete pagination, rate limiting,
unavailable data, malformed remote data, or an ambiguous match leaves the attempt unknown.

#### 9.1 Publication restart scope

This draft proposes **process-lifetime publication safety**:

- Publication attempt identity, commit-boundary state, unknown-result fencing, and reconciliation
  state exist only for the lifetime of one `rvw` process.
- An unknown GitHub publication blocks retry for the same destination and immutable target until
  that process confirms the marker's presence or confirms its absence.
- `rvw` does not promise duplicate prevention after process exit, crash, host restart, or loss of
  memory state. Before exiting with an unresolved attempt it reports the immutable target and that
  a later equivalent publication may duplicate the review.
- A later invocation performs a new preflight and MUST NOT claim that restart made the prior
  attempt safe or reconciled. The user is responsible for inspecting remote state before
  confirming equivalent content.
- No durable publication journal is created.

Explicit acceptance of this exact document accepts that restart limitation. Until then GitHub
publication implementation is blocked. Durable cross-process fencing would require a separate
accepted contract for storage location, private permissions, interprocess locking, atomicity and
directory durability, versioning, corruption, bounded retention, privacy, and cleanup. It MUST NOT
be added implicitly.

## Interactive runtime and lifecycle

### 10. Bubble Tea application and work

The interactive program has exactly one `tea.Program`. Its root model is one pointer-owned value
with pointer-receiver `Update`; `Update` returns the same owner. Every interactive input, accepted
completion, short timer expiry, and lifecycle message routes through that one method.

`Update` is the sole observable-state mutation boundary. The model pointer is never exposed to or
captured by a command, worker, service, child, or view. A command receives an owned request,
immutable snapshot, or immutable dependency and returns a concrete typed message. Mutable result
payload ownership transfers once and the producer never accesses it again.

`View` is pure and complete. It performs no state repair, filesystem, network, subprocess,
clipboard, blocking wait, or unbounded CPU work. Terminal dimensions remain unknown until the
first valid `tea.WindowSizeMsg`. Every initial, loading, empty, error, minimum-size, modal, and
normal branch returns one complete `tea.View` with consistent v2 terminal fields.

For Bubble Tea `v2.0.8`, the complete `tea.View` contract is `Content`, `OnMouse`, `Cursor`,
`BackgroundColor`, `ForegroundColor`, `WindowTitle`, `ProgressBar`, `AltScreen`, `ReportFocus`,
`DisableBracketedPasteMode`, `MouseMode`, and `KeyboardEnhancements`. Shared fields are applied
once after branch-specific content is composed.

In `v2.0.8`, `OnMouse` is limited to bounded hit translation and returns only an immediate typed
application message; real work starts after that message reaches `Update`. The same raw mouse
message also reaches `Update`, so either `OnMouse` or root raw-message routing owns an action, never
both.

Concrete Bubbles own only meaningful local interaction such as text input, textarea, help, or a
bounded viewport. The root retains every returned child model and every returned command. Each key
has exactly one owner in this order: blocking layer, focused child, active view, reserved global
action. The same enabled binding definitions drive routing; their visible-help presentation is
defined in
[`tui-design.md`](tui-design.md#implementation-basis-and-dependency-decisions).

Ordinary `tea.Cmd` values may:

- immediately return a typed message;
- receive one coordinator readiness signal through a cancellable one-shot wait;
- run a short generation-fenced timer;
- run a documented child-Bubble command.

They do not own application filesystem, network, subprocess, unbounded CPU work, or work that must
be joined. `tea.Batch` is used only for a fixed, data-independent command count.

One application-owned coordinator handles accepted source I/O, search/highlighting, export
delivery, and publication operations. It has concrete submit methods, distinct I/O and CPU limits,
and no generic effect, task, or component registry. Submission from `Update` is nonblocking.

An accepted operation reserves request bytes, result-construction bytes, one terminal-result slot,
and the required execution class before launch. It binds a typed operation ID to source and
workspace generations. Every accepted operation resolves exactly once as success, failure,
cancelled, superseded, limited, or unknown. Required work uses FIFO with at most eight bypasses.
Latest-wins work coalesces before acceptance. Publication and reconciliation keep one reserved
admission, execution, and result slot that ordinary saturation cannot consume.

Replaceable progress retains only the latest value per operation and becomes ready at the bounded
cadence in section 14. Terminal outcomes, cancellation, failure, shutdown, and coordinator closure
have independently reserved priority delivery and cannot be delayed by progress.

The coordinator retains every completed outcome and mutable payload in its reserved slot until the
model claims it. Its readiness signal carries no payload. `Update` arms one cancellable readiness
command while the coordinator is active. On `outcomeReadyMsg`, `Update` invokes a nonblocking
owner-local `ClaimNext`, transfers exactly one outcome, settles the visible operation, acknowledges
the slot, and re-arms the wait if appropriate. Readiness, cancellation of the receiver, and
coordinator closure are distinct messages.

Duplicate or stale outcomes cannot mutate the current review, but their resources are released and
external commit/ambiguity evidence is preserved. Coordinator closure is terminal, occurs only
after admission has stopped, workers are joined, and the retained outcome registry is empty, and
is never re-armed. Unexpected worker panic is a program failure unless that exact worker boundary
has a tested fault-isolation contract.

Publication uses a stable attempt ID and a linearized `PreCommit -> Committing` boundary.
Cancellation produces `cancelled` only when the owner proves pre-commit quiescence. Once committing
begins, the outcome is confirmed success, confirmed pre-commit failure, or unknown; cancellation,
stale model state, and shutdown cannot manufacture a safe retry.

### 11. Signals, delivery helpers, and shutdown

The application owns termination notifications. `cmd/rvw` creates the Program with
`tea.WithContext(programContext)` and `tea.WithoutSignalHandler()`, registers `os/signal` exactly
once for `SIGINT` and `SIGTERM`, and releases that registration on shutdown. Bubble Tea retains its
versioned `SIGWINCH` registration for terminal resizing; `rvw` does not create a competing resize
handler. Raw-mode `Ctrl-c` is a key and follows the active modal/editing context; it is not treated
as an operating-system notification.

The first supported notification enters the same idempotent shutdown policy as an internal quit.
A repeated notification requests immediate cancellation of any delivery-local clipboard helper but
does not use `os.Exit` while the terminal is active. Uncatchable termination cannot promise
cleanup. Git and GitHub source operations are native Go operations and are cancelled through their
contexts; they own no child process.

Program and work cancellation scopes are distinct. Controlled shutdown is:

1. stop coordinator admission and request work cancellation through an idempotent, owner-local,
   nonblocking call;
2. return `tea.Quit` for internal shutdown, or cancel the Program context for an outside
   notification;
3. let `tea.Program.Run` return after Bubble Tea's terminal-restoration attempt;
4. cancel the readiness receiver and wait for its bounded completion barrier before touching
   unclaimed outcomes;
5. call the coordinator's one bounded shutdown/join operation, which settles retained outcomes and
   stops any delivery-local clipboard helper;
6. write an immutable pending stdout export, then diagnostics, only after the Program is no longer
   active.

No acknowledgement, grace wait, worker join, pipe drain, child `Wait`, or output write blocks
`Update`, `View`, or the active terminal. The application reports failure when a worker, direct
child, or contained group does not quiesce within its bound.

Bubble Tea `v2.0.8` discards the terminal-restoration error on its ordinary shutdown path.
`Program.Run` returning proves the attempt completed, not that the terminal was physically
restored. Actual restoration is claimed only by a passing PTY contract. The program returns errors
through the restored outer boundary and does not call `os.Exit` below it.

`v2.0.8` can return directly after terminal initialization when initial size discovery or input
reader initialization fails. On a `Run` error that is not `tea.ErrProgramKilled`, the root adapter
calls `Program.ReleaseTerminal` before reporting the error and includes any release failure in the
process result. PTY tests MUST cover that fallback. This use of the Program's public release API
does not create a second terminal owner.

## Terminal presentation

[`tui-design.md`](tui-design.md) is the sole normative authority for visual hierarchy, focus and
cursor presentation, glyphs, responsive composition, dialog geometry, pointer hit regions, color
contrast, and reproducible presentation evidence. This section retains interaction and rendering
behavior that is part of the product contract.

### 12. Interaction

The normal view provides file tree, diff, comments, and status interactions. Focus cycles between
visible panes. Their visual hierarchy and distinguishability are defined in
[`tui-design.md`](tui-design.md#focus-and-input-ownership) and
[`tui-design.md`](tui-design.md#application-states).

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

Comment editing uses `Ctrl-s` to save, `Tab` and `Shift-Tab` to change kind, and `Esc` to cancel.
Multi-key chords time out after 750 milliseconds. Numeric repeats range from 1 through 9,999.
Chord/repeat state resets on context changes, invalid continuation, or timeout. Effective-help
presentation is defined in
[`tui-design.md`](tui-design.md#implementation-basis-and-dependency-decisions). Search and
command-prompt input are each limited to 64 KiB; reaching the limit is visible and accepts no
further input until the user deletes content or cancels.

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
explicit confirmation or selection. Exact dialog geometry and context-preservation rules are
defined in [`tui-design.md`](tui-design.md#dialogs-and-workflows).

While the [Below minimum mode](tui-design.md#responsive-modes) is active, only configured quit and
interrupt bindings are accepted. Resizing restores the prior interaction state.

Responsive modes, pane allocation, single-pane overrides, focus fallback, and side-by-side
eligibility are defined in [`tui-design.md`](tui-design.md#responsive-modes). Hidden panes retain
selection, focus-return target, and scroll identity.

### 13. Rendering

Presentation rendering and its viewport, sanitization, Unicode, identity, syntax-fallback, and
event-loop bounds are defined in [`tui-design.md`](tui-design.md#rendering-bounds).

Syntax work and cache identity is the pair `(content identity, syntax selector)`. Results for
different selectors never share identity. A limited result is not retried automatically until that
identity changes.

Required status presentation and simultaneous-fact preservation are defined in
[`tui-design.md`](tui-design.md#application-states).

## Nonfunctional conformance

### 14. Required bounds and failure behavior

Default externally relevant limits are:

| Boundary | Limit |
| --- | ---: |
| One configuration file | 1 MiB |
| One UI theme file | 1 MiB |
| One TextMate theme file | 1 MiB |
| Changed files per source | 100,000 |
| Bytes per path | 4,096 |
| Total raw path bytes per source | 64 MiB |
| Patch bytes per file | 64 MiB |
| Blob bytes per file | 16 MiB |
| Patch hunks per source operation | 100,000 |
| Patch lines per source operation | 1,000,000 |
| Filter patterns | 4,096 |
| One filter file | 256 KiB |
| Search hits retained | 100,000 |
| Search-hit bytes retained | 16 MiB |
| Comments in one review | 4,096 |
| One comment body | 64 KiB |
| Total comment body bytes | 4 MiB |
| One export template | 256 KiB |
| One prepared export body | 16 MiB |
| One stdin comment-export document | 16 MiB |
| One terminal-safe error detail | 16 KiB |
| Clipboard-helper stdout | 64 KiB |
| Clipboard-helper stderr | 1 MiB |
| One clipboard-helper attempt | 30 seconds |
| Encoded index copy | 96 MiB |
| Aggregate index paths | 64 MiB |
| Aggregate index extensions | 16 MiB |
| One regular worktree file | 128 MiB |
| One symlink target | 16 KiB |
| Retained pull-request records after filtering | 3,000 |
| Selected pull-request blob OIDs | 6,000 |
| Pull-request blob requests | 12 |
| Pull-request workspace | 5 GiB |

Reaching a limit produces a typed limited or failed result with enough context to identify the
operation and bound. It must not truncate silently, panic, corrupt state, or fall back to an
unbounded path.

Interactive runtime limits are:

| Boundary | Limit |
| --- | ---: |
| Accepted operations, including retained outcomes | 64 |
| Queued request bytes | 32 MiB |
| Retained/result-construction bytes | 128 MiB |
| I/O workers | 8 |
| CPU workers | `max(1, min(GOMAXPROCS, 8))` |
| Concurrent clipboard helpers | 1 |
| Reserved publication/reconciliation capacity | 1 operation, worker, and outcome slot |
| Required-operation bypasses | 8 |
| Latest progress per operation | 8 KiB |
| Total retained progress | 256 KiB |
| Global progress readiness cadence | at most 20 per second |
| Claimed terminal outcomes per `Update` | 1 |
| File/tree records processed per `Update` | 2,048 |
| Result bytes applied per `Update` | 1 MiB |
| Search input per CPU batch | 256 KiB or 4,096 logical lines, whichever comes first |
| Viewport overscan | 8 terminal rows per visible scrolling region |
| Effective render geometry | 1,000 columns by 300 rows |
| Retained patch/blob/syntax presentation cache | 128 MiB |
| Retained status records | 256 records and 512 KiB |
| Readiness-receiver completion barrier | 1 second |
| Process-group `TERM` grace | 750 milliseconds |
| Post-`KILL` direct-child reap bound | 5 seconds |
| Coordinator shutdown/join | 10 seconds |

The effective render geometry clips only presentation; the model retains the actual terminal size
and reports the presentation limit when it applies. A large reload/result is retained within the
result budget and applied in bounded chunks; the replacement becomes observable atomically only
after validation and reconciliation complete.

Source commands MAY stream stdout through a bounded parser rather than retaining it. The 64 MiB
stdout limit applies when output is captured as a result; streaming parsers instead charge their
domain records, partial token, and error context against the applicable source limits. No streaming
path retains an unbounded line or token.

All retained collections, channels, caches, operation sets, worker pools, cleanup queues, and
per-message drains have the listed capacity or a smaller type-specific capacity. Memory ownership
is charged before admission and released exactly once on completion, cancellation, rejection, or
cleanup. Large inputs and results are not duplicated merely to cross ownership boundaries.

External failures preserve the last valid review state wherever possible. Error details are
bounded and terminal-safe. Retrying is explicit when an operation may have committed externally.

### 15. Security and integrity invariants

- External text cannot inject terminal control sequences.
- Raw path bytes round-trip independently of their display form.
- Repository-relative configuration cannot traverse outside the repository.
- Theme and syntax paths cannot traverse outside the user theme directory.
- Git and GitHub invocations use structured arguments, bounded output, deadlines, and no shell
  interpolation.
- Publication targets immutable repository and head identities established by preflight.
- Stale revisions cannot mutate current state.
- Cancellation cannot convert an ambiguous external commit into a safe retry.
- Terminal restoration does not depend on background cleanup succeeding.
- Unsupported platforms and source/output modes fail at composition rather than activating a
  fallback.

### 16. Verification

Conformance requires all of the following:

| Area | Required verification |
| --- | --- |
| CLI | Process tests for every source/output pair, help forms, range and PR grammar, repeated options, global filters, invalid UTF-8, and exit behavior |
| Configuration | Precedence, aliases, unknown fields, byte limits, project keybinding rejection, keymap completeness, themes, paths, templates, and `NO_COLOR` |
| Output | Golden human and JSON schemas for all source and change variants, hidden files, non-UTF-8 paths, terminal escaping, and GitHub cap recovery |
| Domain | Mutation invariants, typed identities, comments, review marks, filtering, snapshots, stale revisions, reload reconciliation, and invalid transitions |
| Source | Git/GitHub fixture tests, hostile external data, process limits, cancellation, direct-child reaping, contained-group and escaped-descendant outcomes, lazy materialization, and publication reconciliation |
| Runtime | Admission/claim/cancel/complete races, byte-credit release, readiness/receiver closure, deadlines, late results, retained-outcome settlement, delivery fencing, panic policy, and shutdown ordering |
| Terminal | Root/child model retention, command capture, key/help ownership, complete `tea.View` fields, Unicode/path safety, signal ownership, startup-release fallback, PTY restoration, and the [TUI presentation evidence matrix](tui-design.md#reproducible-conformance-evidence) |
| Architecture | Go package dependency edges, effect ownership, pure-review imports, public-surface discipline, forbidden duplicate paths, supported-platform checks, dependency pins, and source-file cohesion |
| Scale | Counter-based tests for viewport-proportional work plus retained large-file, large-tree, many-comment, search, and syntax benchmarks |

Performance checks require fixed fixtures, recorded environment metadata, repeated samples, and
reported absolute results. A relative regression threshold is not meaningful without a named
baseline and therefore is not part of this specification.

`make check` MUST run formatting verification, `go test ./...`, `go test -race ./...`,
`go vet ./...`, `go build ./cmd/rvw`, and the architecture tests on the host. Linux and macOS
release evidence additionally requires the platform-specific process and PTY contracts on both
systems. Before G1 creates the real `cmd/rvw` vertical slice, `make check-g0` runs every listed host
check except that impossible build; it MUST NOT be satisfied by a placeholder command. Release
validation MUST run `make check` and `git diff --check` after edits stop. A check is reported as
passed only when the command ran and exited successfully.
