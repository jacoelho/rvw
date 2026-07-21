# Product surface

## 2. Product and platform

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

The performance target is a review with at least 1,000 changed files of at least 10,000 lines each,
plus hundreds of comments. File metadata for the source may be retained, but patch, blob, and
syntax text MUST be materialized lazily and bounded to current work rather than loaded for every
file. Memory, concurrent work, retained data, queues, per-tick work, frame construction, and
rendering MUST remain bounded.

Patch or stdin-patch sources, additional output modes, review persistence, tabs, chat, agents,
plugins, and generic source registries are outside the product.

## 3. Command line

### 3.1 Accepted forms

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

### 3.2 Validation

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

## 4. Configuration and themes

### 4.1 Precedence and schema

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
submit_default = "comment" # comment, draft, approve, or request-changes
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

### 4.2 Keybindings

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
toggle-export-unmappable, export-selected, copy-markdown, export-markdown, export-json,
submit-recheck, toggle-stale-head-override, toggle-submit-exclusion,
toggle-submit-summary, submit-review, reload, toggle-file-panel,
toggle-comments, toggle-hidden-files
```

A configuration contains at most 4,096 keybinding entries. A chord contains one through four
strokes and at most 128 bytes. `Ctrl-c` is reserved. Unknown, ambiguous, prefix-conflicting,
unreachable, or incomplete maps fail startup. Required escape, confirmation, submission, and
minimum-size actions cannot be removed.

### 4.3 Themes and color

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
diff_context, pending, border_focused, status_bar_bg, cursor_line_bg
```

The optional keys are:

```text
diff_add_bg, diff_del_bg, diff_hunk_header, file_modified, file_renamed,
branch_name, message_warning_fg, message_warning_bg, syntax_theme
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
configured. Any configured theme value explicitly enables color. Monochrome mode retains focus,
selection, change, severity, and status distinctions using text and modifiers.

## 5. Noninteractive output

Opening for `--stdout` or `--json` MUST produce a complete snapshot. A GitHub files-API cap requires
recovery before output; a truncated snapshot must not be represented as complete.

### 5.1 Human output

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

### 5.2 JSON output

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
  head_ref: string,
  files_api_cap_reached: boolean
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
  head_oid: object-id string,
  files_api_cap_reached: boolean
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
