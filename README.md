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

GitHub pull-request review requires the GitHub CLI (`gh`) to be installed and authenticated.

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

Run `rvw help`, `rvw help diff`, or `rvw help pr` for the complete command-line reference.

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
submit_default = "draft" # draft, comment, approve, or request-changes
fetch_threads = true

[export]
markdown_template = "# Review ({{comment_count}})\n\n{{comments}}"
comment_template = "## {{target}}\n\n{{comment}}"
```

`include` and `exclude` accept either one string or an array of strings. The top-level keys
`mouse`, `theme`, `theme_dark`, `theme_light`, and `appearance` are aliases for their `[ui]`
equivalents.

See the [full configuration and theme reference](docs/rvw-specification.md#4-configuration-and-themes)
for keybindings, custom themes, export-template placeholders, defaults, limits, and validation
rules.
