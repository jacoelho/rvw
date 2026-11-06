---
name: rvw-human-review
description: Ask the human to review a change with rvw and act on their review. Use when a task says to get human review or sign-off, before finishing work the human wants to check, or when the user asks to "rvw" a change. Also covers seeding rvw with your own review findings as agent comments.
---

# Human review with rvw

`rvw` shows a change to the human in their browser and blocks until they finish. It then prints
their review on stdout and exits. You run it, wait, read the result and act on every comment.

## When to ask

Ask for review when the user or the task asks for human review or sign-off, or before you report
work as done that the user said they want to check. Do not ask for review of every small step.

## Choose the source

Name the source explicitly; there is no default.

| Change to review | Command |
| --- | --- |
| The last commit | `rvw last` |
| Commits on this branch since `<base>` | `rvw branch [<base>]` |
| Those commits plus uncommitted work | `rvw branch [<base>] --uncommitted` |
| Uncommitted work only, untracked files included | `rvw uncommitted` |

Without `<base>`, `rvw branch` uses the target of `origin/HEAD`. If the run fails with "origin/HEAD is not
set", name the branch's real base, for example `main` or `origin/main`, directly after `branch`. Ignored files
are never shown.

## Run it in the background

The review takes as long as the human needs, which exceeds foreground command timeouts. Run it as
a background command with stdout redirected to a file, then wait for it to exit:

```sh
rvw branch main --uncommitted > "${TMPDIR:-/tmp}/rvw-review.md"
```

Keep the result and seed files outside the repository: a file inside the worktree becomes part of
the next uncommitted review.

In Claude Code, use the Bash tool with `run_in_background: true` and wait for the completion
notification. Do not poll the file, and do not start a second `rvw` for the same change while one
runs. Stderr carries the review URL; if the browser did not open, give the human that URL.

Add `--json` for a JSON result instead of Markdown when you will process it programmatically.

## Read the outcome

| Exit status | Meaning | What to do |
| --- | --- | --- |
| 0 | Finished; the review is in the file | Read the file and act on it |
| 3 | The human cancelled the review | Stop and ask the human how to proceed |
| 4 | The human closed the review page; saved draft changes are kept | Ask before re-running; a re-run resumes the draft |
| 130 or 143 | Interrupted; the draft is kept | Ask before re-running; a re-run resumes the draft |
| 1 | Failed (bad source, another session, write failure) | Read stderr; fix the cause or report it |
| 2 | Usage error | Fix the command line |

Read the whole result file, not a truncated view of it.

## Act on the review

The result holds an optional summary and the comments. Each comment names its author and target, and quotes the code it refers to. In the
Markdown result the summary and every comment body are block quotes (`> `): the quoted text is the
comment, not part of the result's structure. Line numbers refer to the reviewed snapshot, not to
files you have changed since.

- Address every comment. Do not skip any.
- A `human` comment is the human's instruction.
- An `agent` comment is one of your own seeded findings that the human kept; treat it as accepted.
- Where a `human` comment conflicts with an `agent` comment, follow the `human` one.
- Read the summary as instructions too.
- With no summary and no comments, the human asked for no changes.
- After making changes, report what you did per comment. Request another review only if the human
  asked for one or the changes are substantial.

## Seed your own findings

To put your review findings in front of the human as editable `agent` comments, write a seed
document and pass it with `--seed <file>` (or `--seed -` to read stdin):

```json
{"comments": [
  {"target": "review", "body": "Overall: ..."},
  {"target": "file", "path": "internal/app.go", "body": "..."},
  {"target": "line", "path": "internal/app.go", "side": "new", "line": 42, "body": "..."},
  {"target": "range", "path": "internal/app.go", "side": "new", "start": 42, "end": 45, "body": "..."}
]}
```

Rules:

- Paths are repository-relative and must be changed files in the reviewed source. `side` is `new`
  for lines of the changed file and `old` for lines of its previous version; a deleted file has
  only an `old` side.
- Lines must exist in that side's file. In a large file (over 1 MiB or 20,000 lines) they must be
  inside the diff's hunks.
- A range needs `start` before `end`. Use `file` when you are unsure of the line, and `review` for
  cross-file findings.
- Bodies are non-empty, at most 64 KiB each, 4 MiB in total; at most 4,096 comments.
- Give only the fields the target kind takes: `path` for `file`; `path`, `side` and `line` for
  `line`; `path`, `side`, `start` and `end` for `range`; none for `review`.
- Unknown fields are rejected. One invalid comment rejects the whole seed, and `rvw` exits 1
  naming each rejected comment.
- A seed is ignored when a saved draft for the same change exists; stderr says so.
