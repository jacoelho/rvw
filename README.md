# rvw

`rvw` is a terminal application for reviewing a local Git worktree, staged changes, a two-dot Git
range, or a GitHub pull request. It opens an interactive TUI by default and also supports
human-readable `--stdout` output and machine-readable `--json` output. Linux and macOS are
supported.

## Install

Install `rvw` with Go 1.26.5 or newer:

```sh
go install github.com/jacoelho/rvw/cmd/rvw@latest
```

The binary is written to `GOBIN`, or to `$(go env GOPATH)/bin` when `GOBIN` is unset. Ensure that
directory is in `PATH`.

`rvw` does not invoke the `git` or `gh` executables. Local repositories are read with a bounded,
read-only Go implementation, and GitHub data is acquired through authenticated HTTPS and smart
Git. Local and remote repositories must use SHA-1 object IDs; SHA-256 repositories are rejected.

GitHub pull-request review and publication require `GITHUB_TOKEN`. `GH_TOKEN`, `gh`
configuration/keychains, and anonymous GitHub access are not used. Repository inference recognizes
only these remotes, without ports, percent encoding, query strings, or fragments:

```text
https://github.com/owner/repository[.git]
git@github.com:owner/repository[.git]
ssh://git@github.com/owner/repository[.git]
```

## Usage

```sh
rvw diff                       # review the current worktree
rvw diff --staged              # review staged changes
rvw diff --range main..HEAD    # review a two-dot Git range
rvw pr 123                     # review a pull request for the current repository
rvw pr 123 --repo owner/repo   # review a pull request in another repository
rvw pr https://github.com/owner/repo/pull/123
```

Use `--repo` with a numeric selector to target another repository. A canonical pull-request URL
already supplies the repository; if `--repo` is also present, it must match the URL.

Pull-request filtering uses built-in defaults and explicit include/exclude patterns only;
`.gitignore` and `.rvwignore` do not apply. At most 3,000 filtered pull-request records are
retained. Worktree comparison supports Git text/EOL and `ident` clean conversion, but does not run
filter drivers, process filters, textconv, or external diff commands. Non-UTF-8 working-tree
encodings are rejected. The built-in deterministic histogram diff can choose different hunk
boundaries from native Git for ambiguous repeated lines while preserving semantic changes, counts,
modes, paths, line sides, and missing-newline state.

Run `rvw help`, `rvw help diff`, or `rvw help pr` for the complete command-line reference.

### Initialize comments from stdin

Pipe a JSON comment export into `rvw diff` or `rvw pr` to initialize ordinary local review
comments:

```sh
cat comments.json | rvw diff
cat comments.json | rvw diff --stdout
cat comments.json | rvw pr 123 --repo owner/repo
```

The document has this active-only shape; `target` may be `review`, `file`, `line`, or `range`:

```json
{
  "comments": [
    {
      "target": {"type": "line", "path": "internal/app.go", "side": "new", "line": 42},
      "binding": "active",
      "comment": "Handle the error here."
    }
  ]
}
```

File targets require `path`. Line targets require `path`, `side`, and `line`. Range targets require
`path`, `side`, `start`, and `end`, with `start < end`. Paths must identify an active file after
filtering, and line/range endpoints must be present on the stated old/new side under configured
diff context. Unknown or duplicate fields, stale/orphaned bindings, malformed or whitespace-only
input, over-limit comments, and inapplicable targets fail with stderr and exit status 1 before the
TUI or noninteractive output starts. Zero-byte piped stdin preserves normal behavior.

The default output still opens the TUI, reading keys from the controlling terminal. `--stdout` and
`--json` validate seeds but keep their existing output contracts. Seeded comments can be edited,
deleted, reloaded, and exported like comments created in the TUI; they are not automatically saved
or published. For a pull request, publication occurs only through the explicit submit flow.

## Configuration

`rvw` reads UTF-8 TOML configuration in this order, with later values taking precedence:

1. built-in defaults;
2. `$XDG_CONFIG_HOME/rvw/config.toml`, or `$HOME/.config/rvw/config.toml` when
   `XDG_CONFIG_HOME` is unset;
3. the first existing project file among `.rvw.toml` and `rvw.toml`;
4. command-line overrides.

Unknown fields and invalid values fail startup. Project configuration cannot define keybindings.

The configuration schema is:

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

`include` and `exclude` accept either one string or an array of strings. The top-level keys
`mouse`, `theme`, `theme_dark`, `theme_light`, and `appearance` are aliases for their `[ui]`
equivalents.

Publishing a GitHub review first synchronizes the selected comments as a pending draft. The submit
dialog then offers Comment, Approve, and Request changes; `submit_default` selects its initial
choice.

See the [full configuration and theme reference](docs/rvw-specification.md#4-configuration-and-themes)
for keybindings, custom themes, export-template placeholders, defaults, limits, and validation
rules. See the [TUI design contract](docs/tui-design.md) for focus, cursor, responsive layout,
dialogs, glyphs, contrast, and presentation evidence.
