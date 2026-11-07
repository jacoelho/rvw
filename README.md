# rvw

`rvw` is the human checkpoint in an agent loop. An agent runs one blocking command. You review the
change in your browser and finish the review. `rvw` then prints your review on stdout for the
agent and exits.

macOS only. Requires `git` 2.50 or newer on `PATH`.

## Install

```sh
go install github.com/jacoelho/rvw/cmd/rvw@latest
```

The web UI is embedded in the binary; no Node.js is needed to install.

## Usage

```sh
rvw last                                # the last commit
rvw branch [<base-ref>]                 # the commits on this branch since <base-ref>
rvw branch [<base-ref>] --uncommitted   # those commits plus uncommitted changes
rvw uncommitted                         # uncommitted changes, untracked files included
```

There is no default source. `branch` diffs from the merge base of `<base-ref>` and `HEAD`; without
a base ref it uses the target of `origin/HEAD`, and fails when that is not set. The base ref goes
directly after `branch`. Any argument there other than one of rvw's options is the base ref, even
if it starts with `-`; write a ref named like an option in full (`refs/heads/--json`). Ignored
files are never shown. Uncommitted changes are snapshotted with `git add -A` into a private index, so a large
untracked directory that is not ignored makes startup slow: add it to `.gitignore`.

Options:

| Option | Effect |
| --- | --- |
| `--json` | print the result as JSON instead of Markdown |
| `--seed <file\|->` | add agent comments from a JSON seed document; `-` reads stdin |
| `-h`, `--help` | show help |

`rvw` listens on a random port of `127.0.0.1`, prints the review URL on stderr and opens it in
your default browser. If the browser cannot be opened, paste the URL yourself.

### In the browser

- The file tree shows each changed file's status, line counts, comment count and viewed mark.
- Vendored directories (`node_modules/`, and `vendor/` beside a `go.sum`) start collapsed. Generated Go files
  (`// Code generated … DO NOT EDIT.`) are grouped per directory, collapsed. Neither gets line counts.
- A pure rename has no line changes; its whole content can be expanded as context and commented on.
- The diff is shown unified or side by side, with changed words highlighted and syntax highlighting
  for Go, Go templates, JavaScript/JSX, TypeScript/TSX, shell, Makefile, Markdown (including fenced code and YAML front matter), Rust,
  Terraform/HCL, JSON, YAML, TOML, Python, Dockerfile, CSS and SQL. Wrap lines wraps long lines instead of scrolling sideways.
- Click a line number to comment on it; shift-click, or drag across line numbers, to select a
  range. Comment on a whole file from its header, or on the whole review from the Finish dialog.
- **Finish** opens the Finish dialog: write a summary, curate every comment (including the
  agent's), each shown with the code it is on and edited in place, copy the review as Markdown,
  and send it, or copy and send in one step with Copy and finish. Cancel is in the same dialog.

Keys:

| Key | Action |
| --- | --- |
| `j` / `k` | next / previous file |
| `n` / `p` | next / previous hunk |
| `]` / `[` | next / previous comment |
| `c` | comment on the current line or selection |
| `v` | toggle viewed and go to the next unviewed file |
| `s` | toggle unified / split view |
| `w` | toggle Wrap lines |
| `←` / `→` | scroll the split view sideways |
| `/` | focus the file filter |
| `⌘K` | search: files, `path:line`, `:line`, `#text` |
| `⌘↵` | save the comment; in the Finish dialog, send; elsewhere, open Finish |
| `Esc` | close the editor, else the dialog |

In the search palette, plain text finds changed files by fuzzy path; `path:12` opens the best match at line 12;
`:12` goes to line 12 of the shown file; `#text` searches the changed files' contents (at least 3 characters, a fixed
string, ignoring ASCII case unless the text has an ASCII uppercase letter).

### Exit status

| Status | Meaning | stdout |
| --- | --- | --- |
| 0 | review finished | the review |
| 1 | error, including a failed write of the result (the draft is kept) | empty |
| 2 | usage error | empty |
| 3 | review cancelled (the draft is deleted) | empty |
| 4 | the review page was closed (saved draft changes are kept) | empty |
| 130 / 143 | interrupted by SIGINT / SIGTERM (the draft is kept) | empty |

Everything other than the review goes to stderr.

### Drafts

Every change you make is saved before the UI acknowledges it, under
`~/Library/Application Support/rvw/drafts`. Running the same command on the same change resumes
the draft; any different content starts a new review. Only one session can review a given change
at a time. Drafts unused for 30 days are deleted.

### Seeding agent comments

```sh
rvw branch main --seed findings.json
```

```json
{"comments": [
  {"target": "review", "body": "..."},
  {"target": "file", "path": "a.go", "body": "..."},
  {"target": "line", "path": "a.go", "side": "new", "line": 12, "body": "..."},
  {"target": "range", "path": "a.go", "side": "old", "start": 3, "end": 9, "body": "..."}
]}
```

Seeded comments have author `agent`; editing one makes it `human`. An invalid seed fails before
anything is served. A seed is ignored when a draft is resumed.

## Result

Markdown by default:

````markdown
# Review

Source: branch main..HEAD (base 1a2b3c4, head 5d6e7f8)

## Summary

> Please split the parser change out.

## Comments

### `internal/source/git.go`:40-44 (new)

```
<the quoted code lines>
```

(agent)
> This retries without a bound.
````

With `--json`, one JSON document with the source, tree IDs, summary and comments
(author, target, quoted code lines, body). Line numbers refer to the reviewed snapshot.

## Agent skill

[`skills/rvw-human-review`](skills/rvw-human-review/SKILL.md) tells an agent when to ask for
review, how to run `rvw` in the background, and how to act on the result.

## Guarantees

- `git` is only run as a subprocess; `rvw` never changes refs, the index, worktree files or
  configuration. Uncommitted changes are snapshotted through a private copy of the index.
- The only network connection is the loopback listener. API requests need a random token from the
  URL fragment; the Host header, cross-origin writes (Sec-Fetch-Site and Origin) and a content
  security policy are enforced.

## Development

```sh
make check   # format, lint, web build freshness, tests, race tests, vet, build
make web     # rebuild web/dist after changing web/src (needs Node.js)
```

`web/dist` is committed and embedded.

Documentation:

- [docs/contract.md](docs/contract.md): requirements; its header names the accepted revision.
- [docs/decisions.md](docs/decisions.md): rationale and rejected alternatives.
- [docs/architecture.md](docs/architecture.md): ownership, boundaries, lifecycle, bounds and trade-offs.
