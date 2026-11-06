<!-- spec-lint: active -->
# rvw next: human review of agent changes

> The product contract for `rvw`. The repository owner accepted revision 1 on 2026-11-05 (SHA-256 of that revision's
> bytes: `3a24d829fd32f309c039a917f116426457e662d14fa76cb389ce0449b5a257d3`). It supersedes
> [the previous specification](https://github.com/jacoelho/rvw/blob/75309d2/docs/rvw-specification.md) and the TUI
> design. Decisions and their rejected alternatives live in [decisions.md](decisions.md); the architecture lives in
> [architecture.md](architecture.md).

Contract revision: 2, 2026-11-05, against workspace `main` at 75309d2. Proposed, not accepted: it adds vendored
directories (REQ-NAV-004, AC-NAV-001), generated Go files (REQ-NAV-005, AC-NAV-002) and unrestorable drafts
(REQ-DRF-005), counts a `vendor` directory as vendored only beside a `go.sum` (Q-23), removes the verdict (Q-31),
shows each comment's quoted code in the Finish dialog and edits it there (Q-32), moves the seed and result formats
from the former plan into [Formats](#formats), and changes REQ-OUT-001 (quoted bodies), REQ-DIFF-004 (context of a text file
with no hunks), REQ-DRF-003, REQ-CMT-003 and REQ-CMT-007 (format reference), AC-SES-001, AC-CMT-002, AC-DRF-001,
the viewed mark's key, the snapshot diff command (per-file hunks and counts from `git diff-pairs`, Q-33), and the
default base ref (Q-38: REQ-SRC-002, REQ-SRC-004, REQ-SES-009, AC-SRC-001, AC-SRC-003, the Base ref,
Snapshot and Unresolvable source definitions, and the JSON format), the cross-origin checks (Q-44: REQ-SEC-002,
AC-SEC-001), the page close (Q-42: new REQ-SES-012, and changes to REQ-SES-003, REQ-AGT-001 and AC-SES-001), and Copy
and finish (Q-43: new REQ-OUT-008, and a change to AC-CMT-002), line selection and a stable view (Q-48: new
REQ-CMT-008, REQ-DIFF-010, AC-CMT-003 and AC-DIFF-003), and the search palette (Q-47: new REQ-NAV-006,
REQ-NAV-007 and AC-NAV-003, a change to REQ-NAV-003 and the Excluded list, and **a change to REQ-PERF-003, which
revision 1 accepted: content search reads changed files' contents without a file request**), relative to revision 1. Revision 2 also raises
the minimum Git version from 2.44 to 2.50, which the repository owner accepted on 2026-11-05 (Q-33); the rest of
revision 2 remains proposed.
Decision owner: repository owner (jacoelho)
Effective scope: target version only. No deployed contract, persisted data or compatibility promise binds this
replacement (Q-03 in [decisions.md](decisions.md)).

## Intent and boundary

A coding agent finishes a change and needs a human decision before it continues. The agent runs one `rvw` command.
The human reviews the change in a browser, leaves line, range, file and summary comments, and finishes the review.
The command then exits and prints the review on stdout, so the agent can act on it. Usefulness is observed as an agent
receiving a complete, machine-readable review from a single blocking command.

Included:

- three review sources: the last commit, the commits on the current branch since a base ref, by default origin/HEAD's
  target, and uncommitted changes, alone or on top of the branch commits;
- an immutable snapshot of the reviewed change;
- a local browser UI with a project-owned diff renderer, file tree, comment editor and syntax highlighting;
- the review result on stdout, its exit status, and draft persistence across interruptions;
- seeding agent-authored comments from a file or stdin;
- a rewritten agent skill that drives the loop.

Excluded:

- GitHub of any kind: pull requests, comments, publication, tokens (Q-02);
- staged-only review and arbitrary `A..B` ranges (Q-01);
- the terminal UI and `--stdout`/`--json` diff outputs (Q-04);
- reloading a snapshot during a session, and carrying comments between review rounds (Q-17);
- line wrapping in the diff (Q-11);
- Linux, Windows and WebAssembly (Q-14);
- an MCP server (Q-05);
- symbol search; regular-expression, fuzzy or Unicode case-folding content search; searching removed lines (Q-47).

Preserved: none. Every current promise is replaceable (Q-03).

## Governing context and basis

Authorised decisions from the repository owner on 2026-11-04 and 2026-11-05 are recorded in [decisions.md](decisions.md).
The repository owner accepted every requirement on 2026-11-05; a `Basis` that says "proposed" names the decision-record
entry the acceptance adopted.

The workload target comes from [AGENTS.md](../AGENTS.md): at least 1,000 changed files of at least 10,000 lines
each, plus hundreds of comments.

`git` is the only Git implementation. The snapshot diff command (Definitions) is the oracle for changed files, hunks and
renames; `git add -A` is the oracle for ignore rules. Porcelain `git diff` is not an oracle, because user configuration
(`color.diff`, `diff.noprefix`, `diff.relative`, textconv and external diff drivers) changes its output. Executed
observation on Apple Git 2.54: each of these altered `git diff` output and not `diff-tree` with the flags below.
`diff-tree` still follows `diff.suppressBlankEmpty`, `core.quotePath` and `GIT_DIFF_OPTS`, so the command pins the first
two and INV-PLAT-005 clears the environment.

## Definitions

- **Repository**: the Git repository and worktree containing the current directory when `rvw` starts.
- **Tree ID**: a Git tree object ID.
- **Snapshot**: a pair of a base tree ID and a head tree ID, plus the source kind and the resolved source arguments that
  produced them: a defaulted base ref is recorded as the ref it resolved to. A snapshot never changes after the session
  starts.
- **Uncommitted tree**: the tree that `git write-tree` produces from a private copy of the repository index after
  `git add -A` has run against that copy. It includes tracked and untracked files and excludes ignored files.
- **Snapshot diff command**: `git -C <toplevel> --literal-pathspecs -c diff.suppressBlankEmpty=false -c core.quotePath=true
  diff-tree -r -z -M -l1000 --no-color --no-ext-diff --no-textconv --src-prefix=a/ --dst-prefix=b/ <base> <head>`,
  with `--raw` for the changed-file list. When any blob of the change is missing locally, `-M -l1000` is omitted
  (REQ-DIFF-009), because rename detection reads blob content. A changed file's hunks and line counts come from
  `git diff-pairs -z` with the same `-c` settings and output options, `-p` or `--numstat`, fed the file's raw record from
  the list, so Git never pairs a file's sides differently from the list.
- **Changed file**: one entry of the snapshot diff command's raw output. A changed file has an old path, a new path, or
  both when renamed.
- **Base ref**: the argument directly after `rvw branch` unless it is absent or one of rvw's options (`--uncommitted`,
  `--json`, `--seed`, `--seed=…`, `-h`, `--help`), so another argument starting with `-` is a ref; a ref named like an
  option is written in full, such as `refs/heads/--json`. When absent, the base ref is the target of
  `refs/remotes/origin/HEAD` as `git symbolic-ref --short` names it. It is any revision that
  `git rev-parse --verify --end-of-options <arg>^{commit}` resolves, such as a branch, remote-tracking branch, tag or
  commit ID.
- **Commentable path**: a path that is valid UTF-8 and contains no control characters. Other paths are shown escaped
  and accept only review comments.
- **Vendored file**: a changed file whose displayed path has a directory, not its own name, that is named exactly
  `node_modules`, or named exactly `vendor` with a regular file `go.sum` beside it (a Go module's vendor directory).
  `go.sum` is looked up in the displayed side's tree. Its vendored directory is the outermost such directory, so each
  module's `vendor` directory is its own vendored directory. The displayed path is the new path, or the old path when
  deleted, so a file renamed out of a vendored directory is not vendored and one renamed into it is.
- **Generated Go file**: a changed file that is not vendored, whose displayed path ends in `.go`, whose displayed side
  is a regular file, and whose displayed side has a line matching `^// Code generated .* DO NOT EDIT\.$` (a trailing CR
  allowed) before its first line starting `package` and a space or tab (the Go convention, go.dev/s/generatedcode).
  The displayed side is the new side, or the old side when deleted. A file with a missing blob on either side is not
  generated.
- **Large file**: a changed file whose old or new side exceeds the large-file limit in REQ-PERF-002.
- **Comment**: a non-empty body attached to one target. The targets are:
  - *review*: no path;
  - *file*: one changed file;
  - *line*: one line number on the old or new side of one changed file;
  - *range*: an inclusive start and end line on one side of one changed file, with start before end.
- **Author**: either `human` or `agent`.
- **Review result**: an optional summary, every comment, and the snapshot that the comments refer to.
- **Draft**: the comments, summary and viewed marks of an unfinished session.
- **Unresolvable source**: any of these: the current directory is not inside a Git worktree; `git` is older than 2.50;
  the base ref is omitted and origin/HEAD is not set; the base ref does not resolve; no merge base exists; `HEAD` is unborn for a source that needs a commit; the snapshot has no changed files.
- **Session**: one run of `rvw` from start until exit.
- **Viewed mark**: a per-file flag set by the human. It is keyed by the file's old and new paths, which are unique
  within the snapshot; the draft's key already fixes the snapshot's trees and so the files' blobs.

## Contract

### Platform and boundaries

#### INV-PLAT-001 — macOS only

Status: accepted
Statement: The rvw command shall build and run only on macOS.
Basis: Repository owner, 2026-11-04 ("macs support only is ok") (Q-14).

#### INV-PLAT-002 — Git through the git executable only

Status: accepted
Statement: The rvw command shall obtain every Git fact by running the `git` executable with structured arguments and without a shell.
Basis: Proposed in the pre-rewrite analysis; this replaces the native reimplementation (Q-06).

#### INV-PLAT-005 — Git invocation environment

Status: accepted
Statement: The rvw command shall run every `git` command from the worktree top level with `--literal-pathspecs`, with `GIT_TERMINAL_PROMPT=0` and `GIT_NO_LAZY_FETCH=1` set, and with `GIT_DIFF_OPTS`, `GIT_EXTERNAL_DIFF`, `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` and `GIT_CONFIG_PARAMETERS` removed from its environment except where rvw sets them itself.
Basis: Proposed (Q-06). A subdirectory start made root-relative pathspecs match nothing, glob characters in paths matched other files, and a partial clone would fetch missing objects over the network and might prompt for credentials. `git diff-pairs` (Definitions, snapshot diff command) sets the 2.50 floor, which also covers `GIT_NO_LAZY_FETCH` from 2.44. Executed observation: inherited `GIT_DIFF_OPTS=-u0` removed all context even with explicit `-U3`. The inherited Git location variables would point commands at another repository or index.

#### INV-PLAT-003 — No network beyond loopback

Status: accepted
Statement: The rvw command and its child processes shall not open network connections other than the rvw loopback listener and connections to that listener.
Basis: No GitHub, repository owner, 2026-11-05 (Q-02). Since the source no longer needs a remote, no other destination remains.

#### INV-PLAT-004 — Repository left unchanged

Status: accepted
Statement: The rvw command shall not change the repository's refs, index, worktree files or configuration.
Basis: Proposed (Q-07). Writing loose objects to the object database for an uncommitted tree is the only allowed write under the Git directory; see REQ-SRC-005.

### Source selection

#### REQ-SRC-001 — Last commit

Status: accepted
Statement: When `rvw last` starts, the rvw command shall review a snapshot whose head is the tree of `HEAD` and whose base is the tree of `HEAD`'s first parent, or the empty tree when `HEAD` has no parent.
Basis: Reviewing the last commit is decided by the repository owner, 2026-11-05 (Q-01). Using the first parent for merges and the empty tree for a root commit is proposed in Q-08.

#### REQ-SRC-002 — Branch commits

Status: proposed
Statement: When `rvw branch [<base-ref>]` starts, the rvw command shall review a snapshot whose base is the tree of the merge base of the base ref and `HEAD` and whose head is the tree of `HEAD`.
Basis: Repository owner, 2026-11-05 (Q-01, Q-38).

#### REQ-SRC-003 — Uncommitted changes

Status: accepted
Statement: When `rvw uncommitted` starts, the rvw command shall review a snapshot whose base is the tree of `HEAD`, or the empty tree when `HEAD` is unborn, and whose head is the uncommitted tree.
Basis: Repository owner, 2026-11-05 (Q-01).

#### REQ-SRC-004 — Branch commits plus uncommitted changes

Status: proposed
Statement: When `rvw branch [<base-ref>] --uncommitted` starts, the rvw command shall review a snapshot whose base is the tree of the merge base of the base ref and `HEAD` and whose head is the uncommitted tree.
Basis: Repository owner, 2026-11-05 (Q-01, Q-38).

#### REQ-SRC-005 — Uncommitted tree construction

Status: accepted
Statement: When the rvw command builds an uncommitted tree, the rvw command shall copy the file named by `git rev-parse --git-path index` to a private file outside the worktree, or create a new private file when that index does not exist, run `git -c core.splitIndex=false add -A` and `git write-tree` against the private file only, and delete it before the review UI is served.
Basis: Proposed (Q-07). Copying the index lets `git add -A` reuse its stat cache, so only changed files are hashed. Executed observation: on Apple Git 2.54 this left the real index unchanged and excluded ignored files. A copy placed inside the worktree was itself included in the tree, so the copy must live outside it. With `core.splitIndex=true`, the temp-index add wrote a `sharedindex` file into the real Git directory unless split index was disabled. `--git-path` resolves a linked worktree's own index; an unborn repository may have no index file.

#### REQ-SRC-007 — Unresolvable source

Status: accepted
Statement: If the requested source is unresolvable, then the rvw command shall exit with status 1 and a stderr message naming the condition, without serving the review UI.
Basis: Proposed (Q-08). An empty review gives the human nothing to decide.

### Session lifecycle

#### REQ-SES-001 — Serve and open

Status: accepted
Statement: When a snapshot is ready, the rvw command shall listen on a random port of 127.0.0.1, write the review URL to stderr, and ask macOS to open that URL in the default browser.
Basis: Proposed (Q-12).

#### REQ-SES-002 — Browser open failure

Status: accepted
Statement: If the default browser cannot be opened, then the rvw command shall keep serving and write the review URL and the failure to stderr.
Basis: Proposed. Sandboxed agents may be unable to run `open`, and the human can still paste the URL (Q-15).

#### REQ-SES-003 — Block until the human decides

Status: proposed
Statement: While a session has not been finished, cancelled, interrupted or ended by closing the review page, the rvw command shall keep running and write nothing to stdout.
Basis: Blocking return path, repository owner, 2026-11-05 (Q-05). Revision 2 adds the page close (REQ-SES-012, Q-42).

#### REQ-SES-004 — Finish

Status: proposed
Statement: When the human finishes the review, the rvw command shall write the review result to stdout, delete the draft, stop serving, and exit with status 0.
Basis: Writing the result and exiting on finish is decided by the repository owner, 2026-11-05 (Q-05). The exit status is proposed in Q-10. Revision 2 drops the verdict (Q-31).

#### REQ-SES-005 — Cancel

Status: accepted
Statement: When the human cancels the review and confirms the cancellation, the rvw command shall delete the draft, write nothing to stdout, stop serving, and exit with status 3.
Basis: Proposed (Q-10). The agent must be able to tell a cancelled review apart from a finished one.

#### REQ-SES-006 — Interruption

Status: accepted
Statement: When the rvw command receives SIGINT or SIGTERM before the review is finished or cancelled, the rvw command shall keep the draft, write nothing to stdout, and exit with status 130 for SIGINT or 143 for SIGTERM.
Basis: Proposed (Q-10). Shell convention for signal exits.

#### REQ-SES-007 — Result delivery failure

Status: accepted
Statement: If writing the review result to stdout fails, then the rvw command shall keep the draft, write the failure to stderr, and exit with status 1.
Basis: Proposed (Q-10). The human's work survives a broken pipe.

#### REQ-SES-008 — One session per snapshot

Status: accepted
Statement: If another session holds the draft lock of the same snapshot, then the rvw command shall exit with status 1 and write the other session's review URL to stderr.
Basis: Proposed (Q-13). A draft has one writer.

#### REQ-SES-010 — Moved HEAD notice

Status: accepted
Statement: While a session is open, the review UI shall show a notice within 10 seconds after `HEAD` resolves to a different commit than at session start.
Basis: Proposed (Q-17). Polling `git rev-parse HEAD` is cheap. Worktree edits are not detected, because detecting them means rebuilding the uncommitted tree.

#### REQ-SES-011 — Finish acknowledgement

Status: accepted
Statement: When the review UI requests finish, the rvw command shall write the review result to stdout and only then reply to that request, stating whether the write succeeded and, if it did, including the Markdown review result.
Basis: Proposed (Q-20). The human must not see "Review sent" when the agent got nothing (REQ-SES-007), and the page after Finish needs the Markdown once the server has stopped. The server stops after this reply is flushed.

#### REQ-SES-012 — Page closed

Status: proposed
Statement: When the review page sends a close notice and no review page of the session makes an authenticated request within 10 seconds, the rvw command shall keep the saved draft, write nothing to stdout, write to stderr that the review page was closed and the saved draft changes are kept, and exit with status 4.
Basis: Proposed (Q-42). A human who closes the tab has left the review; the agent should not wait until someone notices. A reload or another open tab makes a request within the grace and keeps the session.

#### REQ-SES-009 — Usage errors

Status: proposed
Statement: If the command line is invalid or names no source, then the rvw command shall exit with status 2 and write usage help to stderr.
Basis: Proposed (Q-09, Q-10). There is no default source; the branch base ref defaults to origin/HEAD (Q-38).

### Review result output

#### REQ-OUT-001 — Markdown result

Status: proposed
Statement: When the rvw command writes a review result without `--json`, the rvw command shall write Markdown containing the source description, the summary, and every comment with its author, its target as `path:line` or `path:start-end` with side, the target's quoted code lines, and its body, with the summary and each body written as a block quote.
Basis: Proposed (Q-16). An agent reads Markdown directly as instructions. Quoting the code lines lets it act on a comment without re-reading the snapshot. Revision 2 quotes the summary and bodies, so a body holding a heading, list or fence cannot change the structure of what follows (Q-26), and drops the verdict (Q-31).

#### REQ-OUT-002 — JSON result

Status: proposed
Statement: When the rvw command writes a review result with `--json`, the rvw command shall write one JSON document containing the source kind and arguments, the base and head tree IDs, the summary, and every comment with its author, target, quoted code lines and body.
Basis: Proposed (Q-16). Revision 2 drops the verdict (Q-31).

#### REQ-OUT-006 — Quoted code limit

Status: accepted
Statement: When the rvw command quotes a target's code lines in a review result, the rvw command shall quote at most the first 20 lines, cut each quoted line after 500 bytes at a UTF-8 boundary, and state how many lines and bytes were omitted.
Basis: Proposed (Q-16). This bounds quoted code to about 10 KiB per comment and 40 MiB at the comment limit, and the body limits bound the rest. Agent harnesses truncate long command output, so the skill also writes stdout to a file.

#### REQ-OUT-003 — Comment order

Status: accepted
Statement: When the rvw command writes a review result, the rvw command shall order comments as review comments first, then by changed-file order, then by side with old before new, then by start line, then by creation time.
Basis: Proposed (Q-16). A deterministic order makes the output reproducible.

#### REQ-OUT-004 — Line numbers refer to the snapshot

Status: accepted
Statement: The rvw command shall report every line target's numbers against the snapshot side named by that target.
Basis: Proposed (Q-16). A new-side line matches the current files only if the agent has not changed them since the snapshot.

#### REQ-OUT-005 — Stderr carries everything else

Status: accepted
Statement: The rvw command shall write the chosen source description, the review URL, progress and diagnostics to stderr only.
Basis: Proposed (Q-16). Stdout is reserved for the result an agent parses.

#### REQ-OUT-007 — Copy as Markdown

Status: proposed
Statement: When the human copies the review as Markdown, the review UI shall save every pending draft change and then put on the clipboard the Markdown review result for the saved draft, leaving stdout and the session unchanged.
Basis: Proposed (Q-20). The human gets a copy for a ticket or chat without a new output mode; the agent's format stays chosen by `--json`. Revision 2 drops the verdict that Copy waited for (Q-31).

#### REQ-OUT-008 — Copy and finish

Status: proposed
Statement: When the human chooses Copy and finish, the review UI shall save every pending draft change, put the Markdown review result for the saved draft on the clipboard, and only after the clipboard write succeeds finish the review (REQ-SES-004); if the clipboard write is refused, the review UI shall not finish and shall show the failure with the text selectable; if saving fails, the review UI shall neither copy nor finish and shall show the save failure.
Basis: Proposed (Q-43). The human who wants a copy and a finished review gets both from one click, and never a finished review without the copy the click promised.

### Comments

#### REQ-CMT-001 — Comment targets

Status: accepted
Statement: When the human submits a comment in the review UI, the rvw command shall accept it only if its body is non-empty after trimming whitespace and its target is a review, a changed file with a commentable path, a line that exists in the named side's file content, or a range whose start and end lines both exist in the same side's file content.
Basis: Proposed (Q-16). An old-side target names the old path and a new-side or file target names the new path, or the old path for a deleted file. A line outside the shown hunks is valid; the UI expands context to show it, except in a large file, where only lines inside hunks are valid.

#### REQ-CMT-002 — Comment editing

Status: accepted
Statement: While a session is open, the review UI shall let the human create, edit and delete any comment, whatever its author.
Basis: Proposed. The human curates agent comments (Q-18).

#### REQ-CMT-003 — Agent comment seeding

Status: accepted
Statement: When `rvw` starts with `--seed <file>`, the rvw command shall read at most 16 MiB from that file, or from stdin when the file is `-`, parse it as a comment seed document and add each comment with author `agent`.
Basis: Proposed (Q-18). An agent can review first and let the human curate. An explicit flag avoids blocking on an inherited pipe that never closes. The seed document format is defined in [Formats](#formats).

#### REQ-CMT-004 — Invalid seed

Status: accepted
Statement: If the seed exceeds 16 MiB, is malformed, has a comment with an invalid target, or would exceed the comment limits, then the rvw command shall exit with status 1 and a stderr message naming each rejected comment, without serving the review UI.
Basis: Proposed (Q-18). This keeps the current all-or-nothing seed behaviour.

#### REQ-CMT-005 — Comment limits

Status: accepted
Statement: If a comment would make the session exceed 4,096 comments, a body exceed 64 KiB, or all bodies together exceed 4 MiB, then the rvw command shall reject that comment and keep existing comments unchanged.
Basis: Proposed. These are the current product's limits, and they bound draft and result size.

#### REQ-CMT-006 — Comment overview

Status: proposed
Statement: When the human opens the Finish dialog, the review UI shall list every comment in the result's comment order with its author, target, the quoted code lines of REQ-OUT-006 for a line or range target, and body, offer a filter that shows only `agent` comments, and let the human edit or delete each one without leaving the dialog.
Basis: Proposed (Q-20). Revision 2 shows the quoted code and edits in place instead of showing the comment in its file, which closed the dialog (Q-32). Agent and human comments, scattered across files, must be curated as a whole before the agent receives them.

#### REQ-CMT-007 — Authorship on edit

Status: accepted
Statement: When the human saves an edit that changes the body of an `agent` comment, the rvw command shall set that comment's author to `human`.
Basis: Proposed (Q-18). The author tells the agent whose words a comment holds (REQ-AGT-001); an edited seed states the human's instruction. Bodies are compared after line endings are normalised to LF ([Formats](#formats)). Seed provenance is not kept in v1.

#### REQ-CMT-008 — Line selection

Status: proposed
Statement: When the human clicks a line number, the review UI shall select that line and open a comment editor on the selection; when the human shift-clicks a line number on the same side as the selected lines, it shall extend the selection to include that line and open the editor on the extended selection; while the human drags from a line number with no modifier key held, it shall select the lines from the pressed line to the line under the pointer, on the pressed side only, keeping the last line while the pointer is over a row without a line on that side, and scroll the diff while the pointer is held at its top or bottom edge; on release, inside or outside the diff, it shall open one comment editor on that selection; and a press and release on one line shall act as a click. Opening an editor on a selection while a new comment's editor is open shall move that editor, its text kept, to the selection, or, if the move is refused, say why and keep the editor's own lines; while another comment is being edited, opening one leaves that edit first, and while a save or leave is pending nothing opens (Q-46).
Basis: The repository owner asked for drag-select for multi-line ranges, 2026-11-06 (Q-48). Click and shift-click predate it and are recorded here with it.

#### REQ-DRF-001 — Draft persistence

Status: accepted
Statement: When the draft changes, the rvw command shall write the draft atomically to a file under `~/Library/Application Support/rvw/drafts` before acknowledging the change to the review UI.
Basis: Proposed (Q-13). An interruption or crash never loses acknowledged work.

#### REQ-DRF-002 — Draft identity

Status: accepted
Statement: The rvw command shall key each draft by the repository's common Git directory, the worktree path, the source kind, and the snapshot's base and head tree IDs.
Basis: Proposed (Q-13). The same change resumes its draft. Any different content starts a new review.

#### REQ-DRF-003 — Draft resume

Status: proposed
Statement: When a session starts for a snapshot that has a restorable draft, the rvw command shall restore that draft, ignore any seed and say so on stderr.
Basis: Proposed (Q-13). The draft already holds the earlier seed as curated by the human; merging would duplicate comments or restore deleted ones.

#### REQ-DRF-004 — Draft lock

Status: accepted
Statement: While a session is running, the rvw command shall hold an exclusive lock on a lock file beside its draft that is never renamed.
Basis: Proposed (Q-13). An atomic rename would replace a locked draft file, so the lock sits on a separate file.

#### REQ-DRF-005 — Unrestorable draft

Status: proposed
Statement: When a session starts for a snapshot whose draft is unreadable, of another format version, or holds a comment, summary, viewed mark or limit that is invalid for the snapshot, the rvw command shall move the draft aside under a name that does not match any snapshot's key, name its new path and the reason on stderr, start as if no draft existed, and delete the moved file once it is 30 days old.
Basis: Proposed (Q-25). Failing every start would strand the human on that snapshot until they find and delete the file; silently discarding it would lose their work. Moving it aside keeps the work recoverable by hand and lets the review continue. A Git or file system error while restoring is not a property of the draft and still fails the start.

### Diff presentation

#### REQ-DIFF-001 — Git patch is the diff

Status: accepted
Statement: The review UI shall present each changed file's hunks exactly as the snapshot diff command reports them.
Basis: Proposed (Q-06). Git is the reference users compare against, so there is no second diff engine.

#### REQ-DIFF-002 — Unified and split views

Status: accepted
Statement: When the human toggles the diff view, the review UI shall switch between a unified view and a side-by-side split view while keeping the same file and the nearest line in view.
Basis: Proposed (Q-04).

#### REQ-DIFF-003 — Word-level changes

Status: accepted
Statement: Where a hunk pairs deleted and added lines that are each at most 1,000 characters long, the review UI shall highlight the changed words within each pair.
Basis: Proposed (Q-11). The length cap bounds per-line work.

#### REQ-DIFF-004 — Context expansion

Status: proposed
Statement: When the human expands the context of a file that is not a large file, the review UI shall show additional unchanged lines from the snapshot's file contents between or around hunks, or from anywhere in a text file with no hunks, such as a pure rename.
Basis: Proposed (Q-11). Revision 2 adds a text file with no hunks, whose whole content is context.

#### REQ-DIFF-005 — Non-text changes

Status: accepted
Statement: When a changed file is binary, a submodule, or changes only its mode, the review UI shall show its status, paths and modes without a line diff.
Basis: Proposed.

#### REQ-DIFF-006 — Large files

Status: accepted
Statement: When a large file is selected, the review UI shall show its hunks without syntax highlighting or context expansion, and state that the file is large.
Basis: Proposed (Q-12).

#### REQ-DIFF-008 — Missing objects

Status: accepted
Statement: If a changed file's blob is missing from the local object database, then the review UI shall show that file as unavailable without a line diff.
Basis: Proposed (Q-06). Partial clones may lack blobs, and INV-PLAT-005 forbids fetching them.

#### REQ-DIFF-009 — Renames without local blobs

Status: accepted
Statement: If any blob of the snapshot's changed files is missing from the local object database, then the rvw command shall build the changed-file list without rename detection and report in the review UI that renames were not detected.
Basis: Proposed (Q-06). Executed observation by the critic review on Apple Git 2.54: in a blobless clone with `GIT_NO_LAZY_FETCH=1`, `diff-tree -M --raw` failed with exit 128, while the same command without `-M` succeeded. Missing blobs are found with `git cat-file --batch-check` over the raw list.

#### REQ-DIFF-007 — Viewed marks

Status: accepted
Statement: When the human marks a file as viewed, the review UI shall show the mark in the file tree and the diff header until the human clears it.
Basis: Proposed. The mark is part of the draft (REQ-DRF-001).

#### REQ-DIFF-010 — Stable view

Status: proposed
Statement: When the diff's rows change while a file stays shown, such as a comment deleted, an editor saved into its comment, a draft moved or context shown, the review UI shall keep the topmost row in view that is still shown at the same place on screen, unless the change itself reveals a row the human asked for: an opened comment editor, a toggled view (REQ-DIFF-002) or a revealed line; when another file is shown, it shall show that file from its first row, unless the navigation targets a line or a comment in it, which is then revealed; and when the open comment editor has the focus, a re-render the human did not start in that editor shall leave its focus, text, caret and selection unchanged.
Basis: The repository owner asked to fix the scroll jump, 2026-11-06 (Q-48). A shifting view loses the human's place, and an editor losing focus sends the next keys to the page shortcuts.

### File navigation

#### REQ-NAV-001 — File tree

Status: accepted
Statement: The review UI shall show every changed file in a tree with single-child directories collapsed into one row, each file showing its status, viewed mark, comment count and, once computed, its line counts.
Basis: Proposed (Q-04).

#### REQ-NAV-002 — File filter

Status: accepted
Statement: When the human types in the file filter, the review UI shall show only changed files whose path contains the filter text, ignoring case.
Basis: Proposed (Q-04).

#### REQ-NAV-003 — Keyboard operation

Status: proposed
Statement: The review UI shall support moving to the next and previous file, next and previous hunk, next and previous comment, starting a comment on the current line, toggling the viewed mark, toggling the diff view, focusing the file filter, opening the search palette and finishing the review from the keyboard.
Basis: Proposed (Q-04). Opening the search palette is proposed in revision 2 (Q-47). The key list is in [the README](../README.md#in-the-browser).

#### REQ-NAV-004 — Vendored directories

Status: proposed
Statement: The review UI shall show each vendored directory as its own tree row marked vendored with the number of files below it, collapsed until the human expands it, and expanded whenever the file filter changes to non-empty text until the human collapses it; it shall show no line counts for vendored files, in place of REQ-NAV-001's line counts, and leave them out of the line-count totals; moving to the next or previous file shall skip files in a collapsed vendored directory, and selecting such a file by any other means shall expand its directories.
Basis: The repository owner asked that `vendor/` and `node_modules/` not dominate the review and chose "collapse, skip counts", 2026-11-05 (Q-23). Vendored files stay in the file list, the comment targets and the review result.

#### REQ-NAV-005 — Generated Go files

Status: proposed
Statement: The review UI shall show the generated Go files directly in each directory under one tree row in that directory, marked generated with their number, collapsed until the human expands it, and expanded whenever the file filter changes to non-empty text until the human collapses it; it shall show no line counts for generated Go files, in place of REQ-NAV-001's line counts, and leave them out of the line-count totals; moving to the next or previous file shall skip files in a collapsed generated row, and selecting such a file by any other means shall expand it.
Basis: The repository owner asked that well-formed generated Go files be hidden like vendored ones and chose "group per directory", 2026-11-05 (Q-24). Generated files stay in the file list, the comment targets and the review result. Finding them reads changed files' content, so, like line counts under REQ-PERF-004, it runs after the UI is served: rows may regroup after the first paint, a file the human selected or interacted with stays shown, a generated file shown only because it was first, and not yet interacted with, is replaced by the first file outside a collapsed group (if any), and until then generated files have no line counts. If they cannot be found, no file is grouped, every file that is not vendored gets line counts, and the UI says generated files are not grouped.

#### REQ-NAV-006 — Search palette

Status: proposed
Statement: When the human presses ⌘K outside a comment editor or text area, the review UI shall open a search palette, which lists changed files regardless of the file filter, in which: empty input lists the first 50 changed files in tree order; other text lists up to 50 changed files whose path, or old path of a rename, fuzzy-matches it, best first; `<path>:<n>`, with n a positive decimal without leading zeros, opens the best-matching file at line n of its new side, or of its old side for a deleted file, so a path ending in `:<digits>` is read as a line; `:<n>` goes to line n of the shown file without reloading it; a line between hunks is shown by expanding context, and a line that cannot be shown opens the file with a notice saying so; choosing a result first leaves the open comment editor as any other navigation does (Q-46), and goes nowhere if that is refused; and Enter while results for a newer query are pending is ignored.
Basis: The repository owner asked for a ⌘K search palette for files, lines and text, 2026-11-05/06 (Q-47). Its query and results are transient UI state, not part of the draft.

#### REQ-NAV-007 — Content search

Status: proposed
Statement: When the palette's input starts with `#`, the review UI shall search the rest as text in each changed file's shown version, as stored in the snapshot (the new version, or the old version of a deleted file), and list the matching lines, one per line, in changed-file order then line order; the text must be valid UTF-8 of at least 3 characters and at most 256 bytes, with no NUL, CR or line feed, and the rvw command shall refuse other text; the match is a fixed string, ignoring ASCII case unless the text holds an ASCII uppercase letter; binary sides, symlink sides, regular files that change only their mode, missing blobs, submodules and sides over 1 MiB are not searched; the search keeps at most 500 lines and stops after 2 seconds or 16 MiB of `git grep` output, and when it stops early the UI says some files may not be listed, the kept matches being those found first (changed files with a new side in order, then deleted files in order); choosing a line shows it as in REQ-NAV-006.
Basis: The repository owner asked for text search in the palette, 2026-11-05/06 (Q-47). The bounds keep one search within one file slot's work; ASCII-only case folding gives `git` and rvw one rule in every locale.

### Syntax highlighting

#### REQ-HL-001 — Highlighted languages

Status: accepted
Statement: When a file that is not a large file and is Go, TypeScript, shell or Makefile source is shown, the review UI shall show syntax highlighting computed from the whole of each side's file content.
Basis: The repository owner chose these four languages if no low-impact tokenizer exists, 2026-11-05 (Q-19). Using Chroma as the tokenizer is proposed. Tokenizing whole files keeps multi-line strings and comments correct.

#### REQ-HL-002 — Highlighting never blocks review

Status: accepted
Statement: If highlighting a file fails or is unavailable for its language, then the review UI shall show the file without syntax highlighting.
Basis: Proposed (Q-19).

### Performance

#### REQ-PERF-001 — Viewport-bounded rendering

Status: accepted
Statement: The review UI shall keep the number of diff rows, file tree rows and Finish-dialog comment rows in the document at most the visible rows plus 50 overscan rows per scrolling region.
Basis: Proposed. AGENTS.md requires presentation work proportional to the viewport. With no wrapping, diff and file tree rows have a fixed height (Q-11); Finish-dialog comment rows vary with the body and use measured heights.

#### REQ-PERF-002 — Large-file limit

Status: accepted
Statement: The rvw command shall treat a changed file as large when either side exceeds 1 MiB or 20,000 lines.
Basis: Provisional value pending the measurement in Q-12.

#### REQ-PERF-003 — Lazy file content

Status: proposed
Statement: The rvw command shall read a changed file's patch and contents only when the review UI requests that file, or when a content search (REQ-NAV-007) reads the changed files' contents through `git grep`, and discard them after the response is sent.
Basis: Proposed. Server memory stays proportional to one request, not to the snapshot. Revision 2 adds the content-search clause to this requirement, which revision 1 accepted (Q-47); that change is proposed.

#### REQ-PERF-004 — Startup proportional to change

Status: accepted
Statement: When a session starts, the rvw command shall read file contents only for changed files and only as `git` itself does to build the snapshot and its changed-file list.
Basis: Proposed. Today's implementation copies every tracked worktree file at startup, as the pre-rewrite analysis found. Line counts need every changed file's content, so they are computed after the UI is served.

### Local security

#### REQ-SEC-001 — Session token

Status: accepted
Statement: When a session starts, the rvw command shall generate a 128-bit random token, put it only in the URL fragment of the review URL, and reject every API request that lacks it as a bearer token.
Basis: Proposed (Q-12). The fragment never reaches the server or its logs. A cookie is rejected because browsers share cookies across loopback ports.

#### REQ-SEC-002 — Host and origin checks

Status: proposed
Statement: If a request's Host header differs from the listener's address, or a state-changing request is marked as cross-site or cross-origin by its Sec-Fetch-Site header or, without Sec-Fetch-Site, carries an Origin header whose host differs from the listener's address, then the rvw command shall reject the request without effect.
Basis: Proposed (Q-12). This prevents DNS rebinding and cross-site request forgery. Revision 2 follows the browser's Sec-Fetch-Site header and accepts a request that carries neither header; the session token (REQ-SEC-001), which only the page holds, blocks forged requests (Q-44).

#### REQ-SEC-003 — Content security policy

Status: accepted
Statement: The rvw command shall serve the review UI with a content security policy that allows scripts and connections only from the review UI's own origin.
Basis: Proposed (Q-12). Repository content and comment bodies are rendered only as text.

### Agent integration

#### REQ-AGT-001 — Agent skill

Status: proposed
Statement: The rvw repository shall provide one agent skill that tells an agent when to request human review, to name the source explicitly, to run `rvw` as a background command with stdout redirected to a file and wait for it to exit, to address every comment in the review result read from that file, treating `human` comments as the human's instructions and `agent` comments as its own findings that the human kept, to treat exit status 3 as a cancelled review, and to treat exit status 4 as the page closed with the saved draft kept.
Basis: Proposed (Q-05, Q-15). The Claude Code foreground Bash timeout is at most 10 minutes, so a blocking review must run in the background. Revision 2 adds exit status 4 (Q-42).

### Formats

The agent-facing formats: the seed document REQ-CMT-003 reads and the results REQ-OUT-001 and REQ-OUT-002 write.
This section is normative.

#### Seed (`--seed <file|->`)

`--seed <file>`, `--seed=<file>` and `--seed -` (stdin) are accepted, once. An empty path, a repeated `--seed`, and
a separated value starting with `-` other than `-` itself (most likely a missing value, as in `--seed --json`) are
usage errors; `--seed=-x` or `--seed ./-x` names such a file.

The [rvw-human-review](../skills/rvw-human-review/SKILL.md) skill documents it for agents:

```json
{"comments": [
  {"target": "review", "body": "..."},
  {"target": "file", "path": "a.go", "body": "..."},
  {"target": "line", "path": "a.go", "side": "new", "line": 12, "body": "..."},
  {"target": "range", "path": "a.go", "side": "old", "start": 3, "end": 9, "body": "..."}
]}
```

Unknown fields and trailing data are rejected, as are fields that do not belong to the comment's target: `path` on a
review comment, `side` or lines on a file comment, `start`/`end` on a line comment and `line` on a range comment. Input over 16 MiB is rejected. A malformed comment and a comment whose
target is wrong for the snapshot are reported together, each by its position; the seed is validated before any draft
is loaded, so an invalid seed fails even when it would be ignored. Bodies are UTF-8 and checked against
REQ-CMT-005. The server normalises CRLF and CR to LF in every body, seeded or submitted, before storing it, so an editor
saved unchanged never counts as an edit (REQ-CMT-007).

#### Result (stdout, Markdown)

````markdown
# Review

Source: branch main..HEAD (base 1a2b3c4, head 5d6e7f8)

## Summary

> <summary text>

## Comments

### Review

(human)
> <body>

### `internal/source/git.go` (file)

(human)
> <body>

### `internal/source/git.go`:40-44 (new)

```
<code lines 40-44 of the new side, at most 20>
```

… 5 more lines; 12 bytes cut from long lines not shown

(agent)
> <body>
````

Comments are ordered as REQ-OUT-003 says; within one file, file comments come before line and range comments. The
omission note appears only when lines or bytes were cut (REQ-OUT-006). The summary and every body are block quotes:
each line is prefixed `> `, and an empty line is a bare `>`. A body therefore cannot end the comment early or open a
heading, list or fence that swallows what follows. Code fences carry no language tag. Code fences
and the path code span use more backticks than any run inside them.

#### Result (stdout, `--json`)

```json
{"source": {"kind": "branch", "branch": "main", "uncommitted": false},
 "base_tree": "…", "head_tree": "…",
 "summary": "…",
 "comments": [{"author": "human", "target": "range", "path": "internal/source/git.go",
               "side": "new", "start": 40, "end": 44, "code": ["…"],
               "code_omitted_lines": 5, "code_omitted_bytes": 12, "body": "…"}]}
```

`branch` is the resolved base ref: origin/HEAD's target when the base ref was omitted. `code`, `code_omitted_lines`
and `code_omitted_bytes` are present only for line and range comments, and the omission counts only when non-zero. Invalid UTF-8 in quoted code is replaced with U+FFFD.

## Acceptance

All acceptance checks are planned and none has been executed. Fixture repositories are built in tests by running `git`.
Repository-level results, such as the snapshot diff command's output for the same trees under an empty configuration
and environment, are the oracle.

### AC-SRC-001 — Sources resolve to the expected trees

Covers: REQ-SRC-001, REQ-SRC-002, REQ-SRC-003, REQ-SRC-004
Expected: For fixtures covering a linear history, a merge commit, a root commit, an unborn `HEAD`, a branch fork point, a tag and a remote-tracking branch as base ref, a linked worktree, untracked files and an ignored file, each source's base and head tree IDs equal the trees that the corresponding `git rev-parse`, `git merge-base` and temp-index `git write-tree` commands give. The ignored file is absent. `rvw branch --help` shows the help; `rvw branch -x` takes `-x` as a ref and fails with status 1 because it does not resolve. With origin/HEAD set, `rvw branch` reviews from the merge base with origin/HEAD's target; without it, the run exits 1 with a message saying origin/HEAD is not set and to name a base ref.
Verification: Planned Go integration test against fixture repositories.
Counterexample: Diffing the branch against its tip rather than the merge base fails the fork-point fixture.

### AC-SRC-002 — Uncommitted snapshot leaves the repository unchanged

Covers: REQ-SRC-005, INV-PLAT-004
Expected: The index file bytes, `git status --porcelain=v2` output, refs and config are identical before and after building the snapshot, including with `core.splitIndex=true` and in a linked worktree. Apart from loose objects, no file under the Git directory is added or changed, and no temporary file remains.
Verification: Planned Go integration test.
Counterexample: Running `git add -A` against the real index fails the index-bytes comparison.

### AC-SRC-003 — Invalid sources fail before serving

Covers: REQ-SRC-007, REQ-SES-009
Expected: Each failure exits with the stated status and stderr message, and nothing listens on a port. The failures
include `rvw branch` without a base ref when origin/HEAD is not set.
Verification: Planned process test.

### AC-SES-001 — Exit status distinguishes the outcomes

Covers: REQ-SES-003, REQ-SES-004, REQ-SES-005, REQ-SES-006, REQ-SES-007, REQ-SES-010, REQ-SES-012
Expected: Finish exits 0 with the result on stdout. Cancel exits 3 with empty stdout. SIGINT exits 130 and SIGTERM 143, each with empty stdout and the draft kept. A closed stdout exits 1 and keeps the draft. Closing the only review page exits 4 after the 10-second grace, with empty stdout, the page-closed line and the draft path on stderr, and the saved draft resumed by a rerun. Reloading the page, or closing one of two open pages, keeps the session running. Before any of these, stdout is empty. A commit made during the session shows the notice within 10 seconds.
Verification: Planned process test that drives the HTTP API.
Counterexample: Exiting 0 on cancel fails.

### AC-SES-002 — Browser open failure is not fatal

Covers: REQ-SES-001, REQ-SES-002
Expected: With the browser opener replaced by a failing one, the session keeps serving and stderr contains the URL.
Verification: Planned process test. Whether `open` works under Claude Code sandbox mode is a manual check (Q-15).

### AC-SES-002B — Second session on the same snapshot

Covers: REQ-SES-008, REQ-DRF-004
Expected: While one session runs, a second start for the same snapshot exits 1 and prints the first session's URL. A start for a different snapshot succeeds.
Verification: Planned process test.

### AC-OUT-001 — Result content

Covers: REQ-OUT-001, REQ-OUT-002, REQ-OUT-003, REQ-OUT-004, REQ-OUT-005, REQ-OUT-006
Expected: For a fixture review with each target type and both authors, the Markdown and JSON results contain every comment in the stated order, with the code lines that `git show <tree>:<path>` gives for each target. A 30-line range quotes 20 lines and states that 10 were omitted. A path containing a backtick renders inside a longer code span. Stderr holds the URL and source description.
Verification: Planned Go test with expectations written by hand from the fixture.

### AC-CMT-001 — Comment validation, seeding and limits

Covers: REQ-CMT-001, REQ-CMT-002, REQ-CMT-003, REQ-CMT-004, REQ-CMT-005, REQ-CMT-007
Expected: A whitespace-only body, a line beyond the side's file length, a line outside the hunks of a large file and a range with start not before end are rejected. A valid seed adds `agent` comments that the human can edit and delete. Editing an `agent` comment's body makes its author `human`; saving it unchanged keeps `agent`. An invalid or oversized seed fails before serving. A line comment outside the hunks is accepted and shown with expanded context. A file with a non-UTF-8 path accepts no file or line comment. The 4,097th comment is rejected and the existing 4,096 are unchanged.
Verification: Planned Go tests plus a browser test for editing.

### AC-CMT-002 — Comment overview and copy

Covers: REQ-CMT-006, REQ-OUT-007, REQ-OUT-008
Expected: For a draft with review, file, line and range comments by both authors, the Finish dialog lists them in the order of AC-OUT-001. Each line or range comment shows the code lines the result quotes for it. Editing or deleting one there changes the same comment in its file, and the draft, and leaves the dialog open at the same scroll position. Copying puts on the clipboard text equal to the stdout that finishing then produces without `--json`. The Agent filter shows only `agent` comments. A summary typed but not yet saved appears in the copied text. Copying writes nothing to stdout and leaves the session open. Copy succeeds in Safari and Chrome, and a refused clipboard write shows a failure with the text selectable. Copy and finish puts on the clipboard text equal to the stdout that finishing produces without `--json` and exits 0; a refused write or a failed save keeps the session open.
Verification: Planned browser test in Safari and Chrome.

### AC-CMT-003 — Line selection

Covers: REQ-CMT-008
Expected: Dragging across line numbers highlights the lines from the pressed line to the line under the pointer, on the pressed side only, keeps the last line while the pointer is over a row without one on that side, and opens one editor on that range on release; the click the release produces opens nothing else, and the next click is an ordinary one, also after a release outside the diff. A press and release on one line with the pointer jittering is a click, or with Shift an extension of the selection. A drag with Shift, ⌘, Ctrl or Alt held highlights nothing, opens no editor and leaves an open editor, its text and its highlighted lines unchanged. Holding a drag at the diff's bottom or top edge scrolls it, and the range extends to the lines scrolled into view without the pointer moving; the scrolling stops on release. Dragging upwards with a draft open moves the draft, its text kept, to the range from the lower line to the higher; a drag that cannot move the editor says why and shows the editor's own lines highlighted again.
Verification: Browser tests in `cmd/rvw`.

### AC-SES-003 — Finish acknowledgement

Covers: REQ-SES-011, REQ-SES-007
Expected: Finishing with stdout redirected to a file shows "Review sent", and the page's Copy gives the file's contents after the process has exited. Finishing with stdout closed shows that the result was not delivered and the draft was kept, and the process exits 1.
Verification: Planned process test with a browser.

### AC-DRF-001 — Draft survives interruption

Covers: REQ-DRF-001, REQ-DRF-002, REQ-DRF-003, REQ-DRF-005
Expected: After SIGINT, restarting the same source on the unchanged repository restores all comments, the summary and the viewed marks, and a repeated seed is ignored with a stderr notice. After a further commit, the same command starts an empty draft. `rvw branch`, `rvw branch main` and `rvw branch origin/main` naming the same base commit share one draft. A draft file replaced by invalid JSON, or holding a comment on a line outside the file, is moved aside on the next start: stderr names the moved file, which keeps the old bytes, and the session starts with the seed applied.
Verification: Planned process test.

### AC-DIFF-001 — Rendered hunks match git

Covers: REQ-DIFF-001, REQ-DIFF-005, REQ-DIFF-006, REQ-DIFF-008, REQ-DIFF-009, INV-PLAT-005
Expected: For fixtures covering a rename, a mode change, a binary file, a submodule, a file without a final newline, a path containing `[`, a large file and a blobless partial clone, started from a subdirectory with `color.diff=always`, `diff.noprefix`, `diff.relative`, `diff.suppressBlankEmpty`, `core.quotePath=false` and a textconv driver configured and `GIT_DIFF_OPTS=-u0` exported, the hunks and line numbers the UI's data API returns equal the snapshot diff command run under an empty configuration. The rename shows as a rename. The large file has no highlighting or expansion. In the blobless clone the session starts, renames are reported as not detected, the missing blob is shown as unavailable, and no fetch is attempted.
Verification: Planned Go test at the HTTP API.

### AC-DIFF-002 — Presentation behaviour

Covers: REQ-DIFF-002, REQ-DIFF-003, REQ-DIFF-004, REQ-DIFF-007, REQ-NAV-001, REQ-NAV-002, REQ-NAV-003
Expected: Toggling the view keeps the same line in view. A one-word edit highlights only that word. Expanding context shows lines that match the file contents. Each key in [the README](../README.md#in-the-browser)'s key list performs its action. The filter `TEST` matches `pkg/test_util.go`.
Verification: Planned browser test on a fixture session.

### AC-DIFF-003 — Stable view

Covers: REQ-DIFF-010
Expected: With the view scrolled into a file, deleting a comment above the view, showing context above it, saving an editor above it into a shorter comment and moving a partly hidden draft to a line below each leave the topmost line wholly in view at the same place on screen. Moving to another file from the tree shows it from its first row, wherever the previous one was scrolled. More context arriving or another comment being deleted while an editor is open leaves its text, caret and selection, and a bare `j` then types a letter rather than moving to the next file.
Verification: Browser tests in `cmd/rvw`.

### AC-NAV-001 — Vendored directories

Covers: REQ-NAV-001, REQ-NAV-004
Expected: In a fixture with `vendor/` beside a `go.sum`, a second module's `vendor/` beside its own `go.sum`, a `vendor/` with no `go.sum` beside it, nested `node_modules/`, a file named `vendor` replacing a `vendor/` directory, renames into and out of `vendor/`, and an escaped path below a `vendor/` directory, the session lists every changed file, names each vendored file's vendored directory as a prefix of its displayed path, and reports line counts only for files that are not vendored, also with a blob missing. In the UI, the vendored rows start collapsed, expand on click and while filtering, and `j`/`k` skip their files.
Verification: Go HTTP tests for the session and counts; planned browser test for the tree.

### AC-NAV-002 — Generated Go files

Covers: REQ-NAV-001, REQ-NAV-005
Expected: In a fixture with Go files whose marker precedes the package clause (also after a `//go:build` line and with CRLF line ends), a marker after the package clause, a near-miss marker, a deleted generated file, a renamed generated file, a generated file under `vendor/`, a non-Go file carrying the marker and a missing blob, the server reports exactly the generated Go files that are not vendored, before any line count, and reports line counts only for files that are neither vendored nor generated. If finding them fails, it reports the failure, groups nothing and counts every file that is not vendored. In the UI, generated rows appear once the marks arrive, each directory's generated row starts collapsed, expands on click and while filtering, shows the number of comments on its hidden files while collapsed, `j`/`k` skip its files, a file the human selected or interacted with stays shown, and a generated file shown at load and not yet interacted with is replaced by the first file outside a collapsed group, or, when every file is folded, stays shown with its row revealed.
Verification: Go tests for detection and the status stream; planned browser test for the tree.

### AC-NAV-003 — Search palette

Covers: REQ-NAV-003, REQ-NAV-006, REQ-NAV-007, REQ-PERF-003
Expected: ⌘K opens the palette with its input focused, and does nothing in a comment editor; Esc closes it, gives the focus back and leaves an editor behind it open. `util` lists `internal/util.go` before `pkg/test_util.go` before `pkg/util/other.go`; a camelCase query finds its file; a rename is found and highlighted through its old path; equal scores keep tree order; at most 50 rows show; a query matching nothing says so; an empty query lists the first files in tree order; a vendored or generated file opens with its group expanded; with the filter `long` applied, the empty palette lists the full tree order and a file hidden by the filter can be opened through it, and the filter is kept. `docs/a b:c.md` is a files query; `x:0`, `x:007`, `x:-1` and `x:1.5` are files queries; `long.txt:300` and `:12` are line queries. `long.txt:10` selects and centres the line; `long.txt:200` expands it as context; `long.txt:9999` and a large file's line outside its hunks open the file with a notice, also before the line counts arrive; `deleted.txt:3` selects the old side; an unknown path gives a message and Enter goes nowhere; `c` after a reveal comments on that line. `:N` in the shown file keeps context expanded earlier and does not reload the file. `#needle` rows each mark one match, and choosing a row shows its line, also outside hunks and on a deleted file's old side. Search requests wait 200 ms after the last keystroke, are aborted when superseded, and a superseded or late reply is never shown, nor is a reply after close-and-reopen or after switching to a files query; Enter while a reply is pending goes nowhere; short, over-long and files-mode queries send nothing; refused, unavailable, failed and malformed replies are shown inline and the palette stays usable. A search over a 600-line match file says it stopped early, and the rows in the document stay at most the visible rows plus 50. File text and paths with markup render as text. On the server, a search reads the snapshot trees, not the worktree, skips binary, symlink, missing, submodule and over-1 MiB sides and regular files that change only their mode, so a mode-only file never uses up the 500-line cap, folds ASCII case only, replaces invalid UTF-8, never sends a raw path, takes a file slot after validating its text, and its `git` process stops when the client goes away or the session ends.
Verification: Go tests in `internal/source` and `internal/server`; browser tests in `cmd/rvw`.

### AC-HL-001 — Highlighting

Covers: REQ-HL-001, REQ-HL-002
Expected: A Go file whose hunk sits inside a multi-line raw string highlights the hunk lines as string. A file in another language is shown without highlighting and without an error.
Verification: Planned Go test of the token API plus a browser check.

### AC-PERF-001 — Bounded work at the target workload

Covers: REQ-PERF-001, REQ-PERF-002, REQ-PERF-003, REQ-PERF-004
Expected: On a synthetic fixture of 1,000 changed files of 10,000 lines each, the number of diff rows in the document never exceeds the visible rows plus 50 while scrolling. Server memory after viewing 100 files stays within a fixed bound. Startup reads no unchanged file content. Latency figures are recorded as measurements, not pass/fail (Q-12).
Verification: Planned benchmark and browser test; startup reads checked with `fs_usage`.

### AC-SEC-001 — Local request protection

Covers: REQ-SEC-001, REQ-SEC-002, REQ-SEC-003, INV-PLAT-003
Expected: Requests without the token, with a foreign Host header, or with a foreign Origin on a state-changing request are rejected without effect. A state-changing request whose Sec-Fetch-Site is `cross-site` or `same-site` is rejected without effect, whatever its Origin. A state-changing request with the token and neither Sec-Fetch-Site nor Origin is accepted, and so is one whose Sec-Fetch-Site is `none`. The CSP header is present. During a full session the process and its `git` children connect only to the rvw listener.
Verification: Planned Go HTTP tests. Network use checked manually with `nettop` across the process tree during a process test.

### AC-PLAT-001 — Platform and Git boundary

Covers: INV-PLAT-001, INV-PLAT-002
Expected: The build constraints exclude non-darwin targets. Code inspection finds no Git object parsing outside the `git` process boundary.
Verification: Planned build check and review inspection.

### AC-AGT-001 — Agent loop

Covers: REQ-AGT-001
Expected: An agent following the skill runs `rvw` in the background, receives the result after the human finishes, and addresses each comment, following a `human` comment where it conflicts with an `agent` one. Cancelling makes it stop and ask.
Verification: Planned manual run in Claude Code.

## Implementation discretion

- The HTTP API shape, the frontend structure and whether a UI framework is used, as long as REQ-PERF-001 holds.
- How the TypeScript is built and embedded, provided `go install` produces a working binary.
- The word-diff algorithm, within REQ-DIFF-003's cap.
- Visual design, colours and theme, provided both light and dark system appearances are readable.
