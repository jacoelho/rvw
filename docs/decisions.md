# rvw next: decision record

Decision record for [contract.md](contract.md). It is not normative; the contract is. Entries under "Proposed, then
accepted" were proposals that the repository owner accepted with contract revision 1 on 2026-11-05.

## Decided by the repository owner

### Q-01 — Review sources

Decided 2026-11-05: last commit, branch commits, uncommitted changes. GitHub pull requests were not selected. Staged-only
review and arbitrary `A..B` ranges are dropped: an agent's work is either the last commit, the branch, or the worktree,
and `rvw branch <base-ref> --uncommitted` covers "branch plus what I have not committed yet".

Rejected: keeping all four current sources. Each extra source multiplies snapshot, identity and test work for a workflow
that does not use it.

### Q-02 — No GitHub

Decided 2026-11-05 ("no github comments"). Consequence: no remote fetch, tokens, publication, pending-review
reconciliation or rate-limit handling. This deletes the largest and riskiest part of the current code
(`internal/source` GitHub and smart-Git paths, `internal/change` publication and pending state).

### Q-04 — Browser UI replaces the TUI; project-owned renderer

Decided 2026-11-04 (web UI) and 2026-11-05 ("avoid pierre libraries and implement ourselves"). The diff renderer, file
tree and virtual scroller are project code. Reference ideas from Pierre (`@pierre/diffs`, `@pierre/trees`) only as
design input:

- rows of fixed height;
- the git patch as input, not a second diff;
- a flattened directory tree.

Rejected:

- **Pierre packages.** These were rejected by the owner's decision.
- **Keeping the TUI.** Two presentation stacks for one product would be the "parallel implementation" AGENTS.md design
  stance rejects. The browser gives split view, mouse selection of ranges and rich comment editing cheaply.

Consequence: [tui-design.md](https://github.com/jacoelho/rvw/blob/75309d2/docs/tui-design.md) is superseded on acceptance. That includes its rejection of Chroma,
which rested on terminal byte budgets and per-frame cost. Neither applies to a server-side tokenizer that sends
per-line spans for viewed files.

Refinement, decided by the repository owner 2026-11-05 ("could we have the backend do the calculations of the
hunks?"): the server derives everything about a file's diff — line numbers, change blocks and their pairing, word
changes and expandable gaps — and the browser only lays out rows. Word changes use Myers' O(ND) algorithm over tokens
(`internal/worddiff`), capped at 256 edits per pair, with ranges in UTF-16 code units.

Rejected:

- **Deriving rows in the browser.** It duplicated patch knowledge across Go and TypeScript (numbering, pairing,
  gap bounds) and ran a quadratic LCS per pair on the main thread. One owner in Go is testable with the rest of the
  diff model and keeps the UI a renderer.

### Q-05 — Blocking return path, no MCP

Decided 2026-11-05. The agent runs `rvw`, the browser opens, and `rvw` exits when the human finishes, printing the
result. No MCP server: one blocking process needs no session discovery, no extra protocol, and works with any agent
that can run a command.

Constraint: Claude Code's foreground Bash timeout is at most 10 minutes. The skill therefore runs `rvw` with
`run_in_background` and waits for the exit notification (REQ-AGT-001).

Rejected: an MCP server with `wait_for_review`. That needs a long-lived daemon and session selection, and it ties rvw
to one agent protocol.

### Q-14 — macOS only

Decided 2026-11-04. Linux support is dropped; that narrows AGENTS.md's "Linux and macOS" and must be changed there on
acceptance. Consequences: `open` launches the browser, drafts go to `~/Library/Application Support/rvw`, and
`//go:build darwin` applies to the command.

### Q-19 — Syntax highlighting

Owner direction, 2026-11-05: use an existing tokenizer if its impact is small; otherwise hand-write lexers for Go,
TypeScript, shell and Makefile.

Measured on this machine (executed observation, Go 1.26.5, chroma v2.27.0):

| Check | Result |
| --- | --- |
| `lexers.Get` for go, typescript, tsx, bash, makefile | all present |
| `lexers.Match` for `x.go`, `x.ts`, `x.sh`, `Makefile`, `GNUmakefile` | all match |
| Hello-world binary size | 2.43 MB without Chroma, 8.19 MB with it (+5.8 MB; all lexers are embedded) |
| `govulncheck ./...` | no vulnerabilities found |
| New module dependencies | `github.com/dlclark/regexp2` |

Proposal: use Chroma, pure Go, on the server, restricted to the four languages so the language list stays a product
decision rather than whatever Chroma ships. Tokenize the whole file side once per request (REQ-HL-001) and send spans.

Rejected:

- **Shiki or another browser tokenizer.** It adds WASM or a large JS payload, conflicts with the strict CSP
  (REQ-SEC-003), and duplicates file content transfer.
- **Hand-written lexers now.** About 1–2k lines of lexer code and tests that Chroma already provides. Keep them as the
  fallback if the +5.8 MB size proves unacceptable.

Sub-question resolved 2026-11-05: the owner accepted the +5.8 MB.

## Proposed, then accepted 2026-11-05

### Q-03 — No compatibility obligations

Proposal: treat the current CLI, `--stdout`/`--json` output, persisted pending-review state and config file as
replaceable without migration. Basis: the owner's "blank canvas" framing (2026-11-04) and the user-level design stance
of preferring deletion over shims. Existing drafts on disk are ignored, not migrated.

### Q-06 — `git` executable as the only Git implementation

Proposal: replace the go-git plumbing and native diff kernel with `git` subprocesses.

Rationale:

- `git` is already required for the user's workflow.
- It is the oracle users compare against: renames, attributes, filters, ignore rules and submodules come for free.
- It removes about 20k lines of reimplementation.

Cost: process spawn per request, mitigated by `cat-file --batch` for file contents and one `diff --raw` for the file
list.

Minimum version: Git 2.50, accepted by the repository owner on 2026-11-05 for `git diff-pairs` (Q-33). It also
covers `GIT_NO_LAZY_FETCH` (2.44, INV-PLAT-005) and `rev-parse --end-of-options`. Checked at startup with a clear
error. Verified only on Apple Git 2.54.

Porcelain `git diff` is not used: user configuration changes its output. Executed observation by the critic review on
Apple Git 2.54: `color.diff=always`, `diff.noprefix`, `diff.relative` and a textconv driver each changed `git diff`
output, and a root-relative pathspec run from a subdirectory matched nothing. The contract fixes one plumbing command
line ("snapshot diff command") and runs everything from the top level with literal pathspecs. User configuration is
not discarded wholesale, because `core.excludesFile` must still shape `git add -A`.

### Q-07 — Uncommitted snapshot via a private index

Proposal: copy the index named by `git rev-parse --git-path index` (a linked worktree has its own) to a temp file under
`os.TempDir()`, or start from a new file when there is no index yet, run
`GIT_INDEX_FILE=<copy> git -c core.splitIndex=false add -A`, then `git write-tree`. Executed observation by the critic
review: with split index enabled, the temp add wrote a `sharedindex` file into the real Git directory.

Executed observation on Apple Git 2.54:

- the real index was unchanged;
- untracked files were included and ignored files excluded;
- a copy placed inside the worktree ended up in the tree, so the copy must be outside the worktree;
- the copy must keep the index's modification time. Git rereads a file whose cached stat data is as new as the index
  (racy-git protection); a copy with a fresh time made a same-size edit, saved in the index's last second, look
  unchanged, and the snapshot missed it. Observed by the test review on 2026-11-05.

Trade-off: `git add -A` writes blob and tree objects into the repository's object database. They are unreachable and
`git gc` prunes them. INV-PLAT-004 allows this explicitly.

Rejected alternatives:

- **`git stash create`.** It omits untracked files.
- **Diffing the live worktree.** It gives no stable identity, so a draft could not be keyed and line numbers would
  drift while the human reviews.

Skip-worktree and assume-unchanged entries: `git add -A` honours them, so such a file shows its index content. Accepted
as documented behaviour, not worked around.

### Q-08 — Source edge cases

Proposals:

- **Merge commit, `rvw last`:** diff against the first parent, as `git show --first-parent` does. An agent's merge
  commit usually brings in the base branch, and the first-parent diff shows what the merge changed on this branch.
- **Root commit:** diff against the empty tree.
- **Unborn `HEAD`:** `rvw uncommitted` uses the empty tree as base. `last` and `branch` fail.
- **Empty snapshot:** exit 1, "nothing to review". An agent should not block on an empty review.
- **Detached `HEAD`:** works for all sources.

### Q-09 — No default source

Proposal: bare `rvw` is a usage error (exit 2); the skill always names the source.

Rejected: defaulting to uncommitted changes when the worktree is dirty, otherwise the last commit. Any untracked,
non-ignored file (a scratch note, a generated file) silently switches the review away from the agent's commit. This
repository at 75309d2 has two such files. The agent knows what it just did; the tool would be guessing.

### Q-10 — Exit status

| Status | Meaning | stdout |
| --- | --- | --- |
| 0 | finished | review result |
| 1 | error, including unresolvable source, invalid seed, lock held, stdout write failure | empty |
| 2 | usage error | empty |
| 3 | human cancelled | empty |
| 130 / 143 | SIGINT / SIGTERM | empty |

Exit status 0 means the human finished; what they asked for is in the comments and summary (Q-31).

### Q-11 — Presentation scope for v1

Proposals:

- no line wrapping: horizontal scroll keeps rows fixed-height, which keeps virtualization trivial (REQ-PERF-001);
- word-level highlight only for paired lines of at most 1,000 characters;
- context expansion only for files that are not large.

Wrapping can come later with measured row heights.

### Q-12 — Local server, security, limits

Proposals:

- **Listener:** bind `127.0.0.1:0`. Executed observation: binding works in the sandboxed shell.
- **Token:** keep it in the URL fragment, move it to `sessionStorage`, and send it as `Authorization: Bearer`. A cookie
  is rejected because cookies are not port-isolated, so any other localhost app's page could ride on it.
- **Request checks:** check Host against the listener (DNS rebinding) and check Origin on writes. Serve a strict CSP
  with no inline script. Send no CORS headers.
- **Large-file threshold:** 1 MiB or 20k lines, provisional.
- **Latency:** time to first diff, file switch and scroll frame time are measured in AC-PERF-001, not pass/fail, until
  numbers exist.

### Q-13 — Drafts

Proposals:

- **Location:** `~/Library/Application Support/rvw/drafts/<key>.json`, with `<key>.lock` beside it.
- **Key:** SHA-256 of the common Git directory, the worktree path, the source kind, the base tree ID and the head tree
  ID. A branch source's base ref is not part of it (Q-38).
- **Writes:** write to a temp file, fsync, then rename.
- **Lock:** `flock` on the `.lock` file, because a rename replaces the inode a lock on the draft file would hold. The
  lock file records the session URL so a second instance can print it.
- **Expiry:** drafts unused for 30 days are deleted at startup.

Same content resumes, and a seed given on resume is ignored with a stderr notice, because the draft already holds the
human-curated seed. Any content change is a new review. There is deliberately no re-anchoring of comments across
snapshots in v1 (Q-17).

### Q-15 — Browser opening under agent sandboxes

Unverified: whether `open` works when Claude Code runs `rvw` under its sandbox. REQ-SES-002 makes failure harmless,
since the URL is on stderr and the agent can show it to the human. Check this manually.

### Q-16 — Result format

Proposal: Markdown by default because agents read it directly, and `--json` for scripted use. Each comment quotes up to
20 code lines from the snapshot so the agent need not re-read files that may have changed (REQ-OUT-006). Agent
harnesses truncate long command output, so the skill redirects stdout to a file and reads it.

Targets: old-side line targets name the old path, others the new path (the old path for a deleted file). Lines outside
the shown hunks are commentable, except in large files. Paths that are not valid UTF-8 or contain control characters
accept only review comments; this avoids a byte-preserving path encoding for a rare case. The Markdown shape is in
[the contract's Formats section](contract.md#formats).

### Q-17 — Out of scope for v1

- Reloading the snapshot during a session. If `HEAD` moves, the UI shows a notice (REQ-SES-010); the human finishes or
  cancels and the agent re-runs. Worktree edits are not detected: that would mean rebuilding the uncommitted tree
  repeatedly, writing objects each time.
- Carrying comments into a second review round. The agent addresses comments, re-runs `rvw`, and the human reviews the
  new snapshot fresh. Re-anchoring is a candidate for v2 once real use shows it is needed.

### Q-18 — Agent comment seeding

Proposal: keep seeding so an agent can pre-review and let the human curate, through an explicit `--seed <file|->`
flag capped at 16 MiB (the current limit). Implicit stdin reading is rejected: an inherited pipe that never closes
would block startup. Seed comments carry author `agent`, and the human can edit or delete them. An edit that changes the body makes the author `human` (REQ-CMT-007), because the author tells the agent whose words it is reading: the skill follows `human` comments over its own `agent` findings (REQ-AGT-001), and keeping `agent` would misattribute the human's rewrite. An `edited` flag that keeps seed provenance was rejected for v1 as an extra result field with no consumer. The seed schema is new
and simpler ([the contract's Formats section](contract.md#formats)); the current `{"target":{"type":…},"binding":…,"comment":…}` schema is not kept, and
the skill rewrite changes its output. The current product's comment limits are kept.

### Q-20 — Comment overview and copy

Proposal, from the repository owner's request on 2026-11-05: the Finish dialog lists every comment with edit and
delete (REQ-CMT-006; show-in-file was replaced by Q-32), and offers "Copy as Markdown" (REQ-OUT-007), also on the page shown after Finish. An Agent filter narrows the list to
`agent` comments, which excludes seeds the human has edited (REQ-CMT-007). Copy and Finish both save pending edits first, so the copy equals what stdout then receives. Finish
replies only after stdout is written (REQ-SES-011), so "Review sent" is never shown for an undelivered result.

Rejected: choosing the output format (Markdown, text, JSON) in the browser. The agent parses stdout, so the reader of
stdout must choose its format, through `--json`. A plain-text format adds nothing, because the Markdown already reads
as text.

### Q-21 — Reporting skipped rename detection

Decided while fixing review findings on 2026-11-05. The snapshot diff command fixes `-l1000`, so the contract promises
no rename detection beyond Git's limit; the session only reports when the limit was hit (`rename_detection:
"limit_exceeded"`). rvw detects it from the `-M` listing's stderr, run with `LC_ALL=C`, by the substring `rename
detection was skipped`, which both Git's older and newer wordings contain. A Git that says neither degrades to no
notice.

Rejected: computing the limit from the counts of unpaired added and deleted files. It duplicates Git's rule, which
excludes exact renames and has changed between versions, so the arithmetic could disagree with what Git did.

### Q-22 — Line counts with missing blobs

Superseded by Q-33: counts now come from the list's records, so only a file whose own blob is missing is uncounted.

Decided on 2026-11-05. Without blobs, `diff-tree --numstat` over the whole snapshot fails on the first missing blob and
loses every count. rvw runs it over the files whose blobs are present, as literal pathspecs batched under 64 KiB of
arguments. Without rename detection each file has one path, but a literal pathspec also names every path below it: a
file that replaced a directory, or was replaced by one, would read the blobs under that directory. Such a file is left
uncounted when one of those blobs is missing.

Rejected: one `--numstat` per file, which spawns up to one process per changed file (1,000 or more); and skipping counts
altogether when a blob is missing, which drops counts that are available.

### Q-23 — Vendored directories

Proposed on 2026-11-05 for contract revision 2; not accepted. The repository owner chose to keep vendored files in the
list but collapse their directories and show no line counts for them. A vendored directory is a directory named exactly
`node_modules` at any depth, or named exactly `vendor` with a regular file `go.sum` beside it, looked up in the
displayed side's tree. The repository owner asked for the `go.sum` condition on 2026-11-05: a bare `vendor` name also
matched directories that are not Go module vendoring. Every module's `vendor` directory qualifies on its own, so a
repository with several modules hides each. `internal/source` applies the rule when it resolves the snapshot, with one
`git ls-tree` per tree over the candidate `go.sum` paths, and the server sends the outermost such directory, so one
owner decides and the UI only renders. Case is significant: `Vendor/` is not vendored.

`diff-tree --numstat` still runs over the whole snapshot and the server drops vendored files' counts, so Git spends
the CPU on them. Restricting the command with pathspecs would change which renames Git detects: excluding a large
`node_modules` churn can bring the remaining candidates under `-l1000`, and a rename across the boundary loses its
other side. Counts would then fail to match the snapshot's file list.

Rejected: hiding vendored files, which would leave their changes and any agent comments on them unreviewable; honouring
`linguist-vendored` from `.gitattributes`, which needs a `git check-attr` pass over every changed path and a choice of
which tree's attributes apply; matching only a top-level `vendor/`, which misses nested modules and workspaces;
requiring `go.mod` instead of `go.sum`, which the repository owner did not choose; and a
`--numstat` pathspec filter, for the rename reason above.

### Q-24 — Generated Go files

Proposed on 2026-11-05 for contract revision 2; not accepted. The repository owner asked that well-formed generated
Go files be hidden like vendored ones and chose to group them per directory: one collapsed row per directory, no
line counts. The rule is the Go convention that `go/ast.IsGenerated` applies: a line `// Code generated … DO NOT
EDIT.` before the package clause. Git reads the blobs: one `git grep -m 1` per tree for either the marker or
`^package `, over the changed `.go` paths as literal pathspecs in 64 KiB batches; a file is generated when its first
match is the marker. The server reads only each record's path and the first 18 bytes of the matched line. A vendored
file is never also generated: its directory row already hides it, and skipping `vendor/` keeps the grep small.

It runs in the background after the UI is served, alongside the line counts, and the marks reach the UI through
`/api/status`. One owner, `contentFacts` in the server, holds both facts under one lock: counts found before the
marks wait until the marks land, so no generated file's count is ever published and a client polling from 0 gets the
marks with or before its first count. The marks are sent only in replies to `since=0`; a client whose cursor moved
past 0 already has them. A Git failure is reported as `generated_error`: nothing is grouped and every non-vendored
file is counted. The cost is that rows regroup after first paint, so `j`/`k` order can change under the human; the
UI keeps a file the human selected or has scrolled or clicked in shown and in place; a generated file shown only
because it sorted first, and not yet touched, gives way to the first file outside a collapsed group, so groups still
start collapsed. Measured at the target workload (1,000 changed files of 10,000 lines), the grep takes 0.7–2 s,
about as long as the line counts; `-m 1` does not stop Git inflating each blob.

Rejected: running it before the URL is printed, so rows never regroup. REQ-PERF-004 and AGENTS.md allow startup to
read file contents only as Git does to build the snapshot, and the 0.7–2 s delay would block the review URL. Rejected:
running it before the line counts in sequence, which delays counts by the grep's whole duration. Rejected: reading
each blob's first bytes through the server's `cat-file` reader and classifying in Go, which streams whole blobs
through one single-threaded pipe into the server: 8.4 s against `git grep`'s 2 s on deep delta chains, though 0.4 s
on a pack without deltas; a path or name rule
such as `*.pb.go` or `zz_*`, which misses most generators and marks hand-written files; and any language other than
Go, which the owner did not ask for.

Approximations: a line starting `package` inside a leading block comment ends the search early, so such a file is
not marked generated; `go/ast` parses the file, and a regular expression cannot without reimplementing Go's lexer. A
file with a missing blob on either side is not read, since `ChangedFile.Missing` is per file and `git grep` fails on
an unreadable blob. Symlinks and submodules are not Go source.

### Q-25 — Unrestorable drafts

Proposed on 2026-11-05 for contract revision 2 (REQ-DRF-005); not accepted. The repository owner chose to move a
draft that cannot be restored aside and say so, rather than fail. A draft is unrestorable when it is oversized, not
JSON, of another format version, or holds anything the snapshot rejects: a comment whose target or body fails the
rules a new comment must meet, a duplicate ID or sequence number, an over-limit summary or comment
total, or a viewed mark naming no changed file. It is renamed to `<key>.discarded-<unix nanoseconds>.json` in the
drafts directory, its modification time set to now, and deleted by the same 30-day expiry as other drafts. The seed
is validated first, so an invalid seed still fails the start. A Git or file system error during restore fails the
start: it says nothing about the draft, and moving the draft aside would lose work over a transient fault.

The draft format is version 2 and owns its own types; its target shape is not the agent's. Viewed marks are keyed by
path (old and new), since the draft key already fixes the trees. Restoring re-validates every comment against the
snapshot: one patch run per commented file and, for a file with line comments, the sides' line facts. Restored
sequence numbers are renumbered 0..n-1 in their saved order: they only order comments, so numbering then never grows
across restarts, cannot overflow, and stays exact as a JavaScript number in the UI's order key. Its cost at the
target workload is unmeasured.

Rejected: failing the start, which strands the human until they find the file; deleting the draft, which loses work;
migrating version 1, which had no deployed users (Q-03); and one moved-aside name per key, which would overwrite an
earlier moved-aside draft.

### Q-26 — Quoted bodies in the Markdown result

Proposed on 2026-11-05 for contract revision 2 (REQ-OUT-001); not accepted. The summary and each comment body are
written as block quotes, one `> ` per line and a bare `>` for an empty line, after an `(author)` line. A body is
free text from a human or an agent; unquoted, a body line `## Comments` or an unclosed fence would change how an agent
reads every comment after it.

Rejected: escaping Markdown metacharacters, which changes the text the agent reads and is easy to get wrong; and
fencing bodies, which needs a fence longer than any backtick run and turns prose into code.

### Q-27 — Comment order, file index and save preconditions

Decided on 2026-11-05 (implementation; [architecture.md](architecture.md#ownership)). The review package owns the result order (REQ-OUT-003)
and which file a comment targets. Each comment reply carries the file index, the target with its path, a version and
an opaque order key derived from the comment alone, so a reply is never stale and the UI neither resolves paths nor
re-implements the order.

- **Create by file index.** A create names its changed file by index in a placement
  (`{"target": kind, "file", "side", "line" | "start", "end"}`). The server checks the request's shape (the kind, and
  only the members that kind takes) and answers 400; it builds a `review.Placement`, which is unchecked data.
  `CreateComment` alone checks it against the snapshot (file index, side, lines, range order) through the `Target` it
  resolves to, and answers 422, so a placement's rules and a target's are one set of rules. `review` picks the path
  from the side, so the UI no longer duplicates the side-to-path rule. The seed and the result keep their path-based
  target format.
- **Edit by version.** A create sends `If-None-Match: *`; an edit sends `If-Match: "<version>"` and changes only the
  body. An edit that changes the body assigns a new version. A create or edit whose resolved target and normalised
  body equal the stored comment returns it unchanged, at any version: the write's result is already stored, so a
  request resent after its reply was lost succeeds without a second write. 412 therefore means the stored comment
  differs: a stale editor with other text, or an edit of a deleted comment, so two editors cannot silently overwrite
  each other. A missing precondition is 428; both headers, `If-Match: *` or a malformed version are 400. The server
  sends no `ETag`: comments arrive in bulk, so the JSON `version` is the entity tag.
- **Delete by version.** A delete sends `If-Match: "<version>"` too. A comment saved since that version is not deleted
  (412), so a page that has not seen another page's edit cannot discard it; the UI reloads the comments and shows the
  newer text. Deleting an absent comment succeeds, so a resent delete does not fail. Missing and malformed
  preconditions answer as for an edit.
- **Summary and viewed marks are last-writer-wins.** Each is one value the human sets in one place, and a later write
  is the human's latest intent; a version would need a merge or reload flow for a free-text field that two pages
  rarely edit together.

Rejected: sending the full comment order with every save, an O(n) reply on every keystroke-save; refetching the
session after each save, an extra round trip and a race window; a POST for create with PUT for edit, where an edit
of a missing comment would reply 404, which is indistinguishable from an unknown route; `If-Match: *`, which checks
only that the comment exists and let a stale editor overwrite newer text; persisting versions in the draft, since no
reader of a version outlives the process; `ETag` response headers, since comments arrive in bulk in the session reply;
and `file` as a sibling of `target` in the request, which allows a file and a target that disagree. Also rejected:
a new version for every save, an unchanged body included, under which a resent request whose first attempt was saved
replied 412 and the UI had to guess whether its own write had landed; and comparing on the client after a 412, which
re-implements body normalisation and target resolution in the UI.

### Q-28 — Context ranges

Decided on 2026-11-05 (implementation). A context request names new-side lines that must lie inside one gap; the
reply numbers each line on both sides, so the UI does no old-side arithmetic. One function decides where a file's
gaps are, for both the file and the context replies. There is no line cap: a gap of a file that is not large is at
most 20,000 lines (REQ-PERF-002), and the file reply already reads and highlights the whole file.

Rejected: a 5,000-line request cap, which made "Show all" unavailable on long gaps and duplicated the large-file rule.

Line numbers have one owner: `source` numbers each patch line on both sides from its hunk header, and
`FileDiff.Context` numbers context lines and splits them on LF, as a side's line count does. The server only renders
them.

### Q-29 — HEAD checks for the moved-HEAD notice

Decided on 2026-11-05 (implementation). The UI polls `/api/status` every 5 s and the server caches the HEAD check for
2 s, so a move shows within 7 s, inside REQ-SES-010's 10 s. A failed check is cached like a successful one and logged
once; the notice keeps its last known answer. A broken repository then costs one `git` run per 2 s, not one per poll. A `HEAD` that no longer resolves to any
commit (an unborn branch) also shows the notice: REQ-SES-010 requires it for a different commit and does not forbid
it otherwise, and the human's `HEAD` no longer matches the reviewed one either way.

### Q-30 — Review reads the snapshot directly

Decided on 2026-11-05 (implementation). The review package validated lines and quoted code through a `Files`
interface the server implemented. It now asks `source.Snapshot` directly, so one owner decides which lines accept a
comment (REQ-CMT-001) and the large-file rule is not restated in the server. The cost is test coverage: no test can
deterministically hold a Finish's code quoting in flight, so the property that building the result holds no session
lock is covered for validation (through a `git` shim) and delivery, not for quoting.

Rejected: keeping a narrow hook for Finish alone, a second path that production never takes.

### Q-31 — No verdict

Proposed on 2026-11-05 for contract revision 2; not accepted. The repository owner removed the verdict ("approved" or
"changes requested") as noise: the agent acts on the comments and summary, and a result with neither asks for nothing.
Finish and Copy no longer wait for a choice, and the Markdown and JSON results, the draft and the HTTP API carry no
verdict. The draft format version stays 2, since no released build wrote it; a local draft that still holds a
`verdict` member fails strict decoding and is moved aside (REQ-DRF-005).

Rejected: keeping the verdict optional. An optional field the human may skip still has to be read, rendered and
explained, and an agent would have to decide what its absence means.

### Q-32 — Quoted code and in-place editing in the Finish dialog

Proposed on 2026-11-05 for contract revision 2; not accepted. Show closed the dialog and scrolled to the comment in
its file, which the repository owner found distracting. The dialog now shows each line or range comment's quoted code,
the same lines as the result (REQ-OUT-006), and clicking a comment's text edits it in place. The UI reads quotes from
`GET /api/quotes` for the rows it mounts, at most 128 IDs a request and one request per render pass, and caches them
only while the dialog is open. A quote block's height is fixed by the target's span before the lines arrive, so a reply
never moves the list. No pagination: the list is already virtualized and the session holds at most 4,096 comments.

Rejected: quoting in `GET /api/session`, which would read every commented file at load for a dialog that may never
open; and keeping Show beside in-place editing, which leaves the jump the change removes.

### Q-33 — Per-file patches and counts from the list's records

Proposed on 2026-11-05 for contract revision 2 (Definitions, snapshot diff command); not accepted as part of that
revision. The repository owner accepted raising the minimum Git from 2.44 to 2.50 for `diff-pairs` on 2026-11-05.

The changed-file list decides how each file's sides are paired, and the patch and line counts must agree with it. A
file's patch came from the snapshot diff command run again, restricted to the file's paths, and line counts with
missing blobs from runs restricted by pathspec. A literal pathspec `foo` also matches `foo/x`, so Git could pair the
sides differently: executed on Apple Git 2.54, the full list gave `R100 foo/x bar` and `A foo`, and the run restricted
to `foo` gave `R099 foo/x foo`, so `foo`'s diff failed. A restricted run also has fewer rename candidates, so it can
detect renames the list skipped past `-l1000`.

`source.Snapshot` keeps each file's raw record exactly as the list printed it, score and paths included, and feeds it
to `git diff-pairs -z` (Git 2.50) with the pinned options: `-p` for one file's patch, `--numstat` for the counts of
every file whose blobs are present, in one command. `diff-pairs` takes no pathspecs and detects no renames, prints one
result per record in input order, honours `.gitattributes` binary marks, and splits a type change into two patch
sections. Executed on Apple Git 2.54. A file whose own blob is missing gets no count; a file that replaced a directory
holding a missing blob is now counted (REQ-DIFF-008).

Counts stream. Each record on `diff-pairs`' stdin is followed by a lone NUL, which ends a batch: `diff-pairs` writes
that record's count and then echoes the NUL. Executed on Apple Git 2.54: the output for a record arrived while stdin
was still open. `source` parses one count at a time, checks it against its file, yields it at once and checks the
context before the next, so the UI shows counts as they are computed. A yielded count is final; an error can still
follow it, and the UI keeps the counts it has.

Rejected: restricted re-runs, for the reproduction above; keeping 2.44 by re-running unrenamed files with
`--no-renames` and their exact path, which still leaves a renamed file whose new path was a directory in the base
unavailable; a record field on `ChangedFile`, which would break its value equality and give the same facts a
second form; and one buffered numstat run, parsed after it ends, under which no count appears until every file is
counted.

### Q-34 — File slots for every request that runs Git

Decided on 2026-11-05 (implementation). Two file slots bound concurrent `git` reads from the UI. Every request handler
that runs `git` takes one, including the result preview, which reads quoted code. A request whose wait ends with the
session replies 503 with a reason, not an empty 200. Finish takes no slot: the session phase already serializes it, and
it must not queue behind file views. The status HEAD check takes none either: it is cached and runs at most once per
interval.

Rejected: a slot for Finish, which can stall the human's last action behind two slow file views.

### Q-35 — Abandoned deliveries

Decided on 2026-11-05 (implementation; [architecture.md](architecture.md#state-and-lifecycle)). After a signal, Run
waits the finish wait for a delivery in progress. When the wait runs out, it calls `Session.Abandon`, which
moves a delivery still in progress to `abandoned`; Run then reports a delivery failure. If the stuck write later
completes, `Finish` returns a `*DeliveryError` with `ErrAbandoned` and keeps the draft, so the finish reply, Run's
outcome and the draft agree that the review was not delivered, and the next run resumes it. A delivery that completed
as the wait ran out is reported as `Finished`.

Rejected: reporting a delivery failure without telling the session, under which a write that completed late deleted
the draft and replied "delivered" after `rvw` had reported it undelivered.

Decided on 2026-11-05: the second signal settles the session through `Abandon`; `End` and `Abandon` share one
interrupt transition; the first signal decides the signal status; a second signal after a serve failure exits with the
signal status. `Server.Abandon` maps the session's fate to an `Outcome`, and `cmd/rvw` reports that outcome on stderr
once, whether the main path or the second signal gets there first. The contract does not specify a second signal or
mixed signals; these choices follow REQ-SES-006 and REQ-SES-007 and await the owner's review.

Rejected:

- A "writing" flag in `cmd/rvw`. It duplicated the session phase, reported a delivered review as failed and a cancel
  as interrupted, and did not stop a result still being built from being delivered.
- Mapping every not-delivered state to the signal status. After a failed write part of the result may be on stdout,
  which breaks REQ-SES-007.
- Clearing the running review before reporting its outcome. A second signal after a delivered review would then race
  `rvw` into exit 130 while the main path returns 0.

### Q-36 — Empty comment bodies

Decided on 2026-11-05 (implementation). REQ-CMT-001's "whitespace only" is Unicode White_Space as Go's
`unicode.IsSpace` has it, owned by `review`: U+0009–000D, U+0020, U+0085, U+00A0, U+1680, U+2000–200A, U+2028,
U+2029, U+202F, U+205F and U+3000. U+FEFF is not whitespace, so a body of it alone is accepted. The UI checks only
ASCII whitespace to decide whether a blank editor may close without saving; every other rejection is the server's
422, whose reason the editor shows.

Rejected: mirroring the server's set in the UI, a second owner of the rule.

### Q-37 — Resending a comment write whose reply was lost

Decided on 2026-11-05 (implementation). When a create or edit gets no reply (a network error), the editor keeps the
request it sent and, on the next save, resends that exact request with the same precondition before anything else;
because a repeated write returns the stored comment (Q-27), the resend succeeds whether or not the first attempt
landed. The editor adopts a version only from the reply to its own request, or, after a 412, from the reloaded
comment it then shows (Q-41). Until a lost create is settled, the
editor keeps its target: moving it to another line shows a notice instead, since the same id with another target
would be refused.

Rejected: adopting the comment found by reloading the session, which cannot tell the editor's write from another
writer's; and resending with a new target, which turns a lost create into a 412.

### Q-38 — Default base ref from origin/HEAD

Decided by the repository owner on 2026-11-05: `rvw branch` without a base ref reviews the commits since
origin/HEAD's target. Proposed for contract revision 2 (REQ-SRC-002, REQ-SRC-004, REQ-SES-009,
AC-SRC-001, AC-SRC-003, AC-DRF-001); not accepted with it.

The argument directly after `branch` is the base ref unless it is absent or one of rvw's options (`--uncommitted`,
`--json`, `--seed`, `--seed=…`, `-h`, `--help`); `cmd/rvw` keeps that list in one predicate, which the option parsing
also uses. Any other argument starting with `-` is still a ref, so `rvw branch -x` fails to resolve (exit 1), and a
ref named like an option is written in full (`refs/heads/--json`). A ref after an option (`rvw branch --uncommitted
main`) is the generic "unexpected argument" usage error; the usage line documents the order.

`internal/source` owns the Git fact: `Repo.OriginHead` runs `git symbolic-ref --quiet --short
refs/remotes/origin/HEAD` through the single runner and returns its output, `origin/main`, or `remotes/origin/main`
when a local branch shares the short name. Exit 1, which Git gives silently for a missing and for a non-symbolic
origin/HEAD (executed on Apple Git 2.54), is `ErrOriginHeadUnset`; any other failure, cancellation included, is
wrapped with its cause. `cmd/rvw` owns the defaulting policy: it asks `OriginHead` before resolving, turns
`ErrOriginHeadUnset` into "origin/HEAD is not set; name a base ref: rvw branch <base-ref>" (exit 1, REQ-SRC-007),
and otherwise builds `source.Branch` from the returned name. Everything after that sees an ordinary branch source,
so the resolved ref appears in the snapshot's source, the stderr description, the UI's source label, the JSON
`"branch"` member and the Markdown source line. A dangling origin/HEAD fails in `Resolve` as a base ref that does
not resolve. In an unborn repository without origin/HEAD the origin/HEAD error comes first, because `OriginHead`
runs before `Resolve`.

The draft key stays as REQ-DRF-002 defines it, without the base ref (owner, 2026-11-05). The base tree already
identifies the base, so `rvw branch`, `rvw branch main` and `rvw branch origin/main` share one draft whenever they
name the same base and head trees, and so do other spellings such as `refs/heads/main` or a commit ID.

Rejected:

- Falling back to `main` or `master` when origin/HEAD is not set (owner): a guess can review against the wrong base.
- Requiring the base ref (owner).
- A `source.Request` type resolved inside `Resolve`: it changes 15 `Resolve` call sites across 10 files and the tests
  that build `review.Result`, and adds no invariant beyond what `Branch` with a non-empty ref already guarantees.
- Running `git` from `cmd/rvw`: it bypasses the single runner (INV-PLAT-002, INV-PLAT-005).
- The CLI hint in `source`: command syntax belongs to the CLI, so `source` returns the sentinel.
- Keying the draft by the base's full refname from `git rev-parse --symbolic-full-name`: it needs another Git call,
  has no value for a commit-ID base, and differs from the ref the human sees.
- Adding the base ref as written to the draft key (owner): identical trees reviewed under another spelling of the
  same base, such as the origin/HEAD default and `main`, would start an empty draft, and the human would have to
  match spellings to resume their work.

### Q-39 — Whether a comment is saved

Decided on 2026-11-05 (implementation). `State` alone says whether a comment is saved (`CommentEditor.saved` asks it).
Where an editor's row goes derives from that: a saved comment's editor takes its comment's row, in the diff view and in
the Finish dialog, and the Finish dialog shows its open editor whatever its filter. An editor whose comment is deleted,
or whose id names another target's comment, closes (Q-41). The editor keeps only the base its writes assume (Q-37,
Q-41), from which it picks its request and whether it may still close as blank. Before, the hosts placed the editor from
saved-ness the editor derived from its own precondition, which `State` notifies ahead of: `State.put` notifies inside
the create, and a 412 reloads the comments, both while the editor still holds "create". A save that failed after either
showed a comment row and a new-comment editor for one comment.

Rejected:

- `State` notifying only after the editor adopts its reply: it couples `State` to editors.
- Rebuilding the rows when a save fails: it treats the symptom and leaves the two owners.

### Q-40 — An unchanged editor sends nothing

Decided on 2026-11-05 (implementation). An editor whose text equals the text it loaded or last stored, with no lost
request to resend, saves and leaves without a request. It compares its textarea's value with the value the textarea
held when that text was set, since a textarea normalises line endings. The server rule is unchanged: an edit with an
equal body returns the stored comment, and a stale version is refused only when the bodies differ.

Rejected:

- Always sending: a no-op request, which another tab's edit turns into a "changed" refusal that blocks leaving.
- Comparing with `State`'s body: another tab's edit is not in this tab's `State`, and `State` is not the editor's
  precondition.

### Q-41 — A refused write loads the stored comment

Decided on 2026-11-05 (implementation). After any 412, `State` reloads the comments, and the editor either loads the
stored comment it was writing or closes when the stored comment is not the one it was writing:

- an edit that finds the comment changed, or a create that finds its id at its own placement, shows the stored text,
  edits the stored version from then on, and keeps the human's refused text in its error;
- an edit that finds the comment deleted closes and reports it;
- a create that finds its id at another target closes and reports that another comment uses the id;
- a create that finds no comment (another writer created the id and deleted it) stays open, and its next save creates
  the comment.

Hosts close an editor on one rule, `CommentEditor.stale`: an edit whose comment `State` no longer holds, or a create
whose id `State` holds at another target. Invariant: once a write has settled, a saved editor that is not stale has a
base; it never keeps a create for an id `State` holds. Before, the editor kept its create, or its old version, after
the refusal while `State` held the comment, so every save, and every leave, was refused again until Close.

What is guaranteed: the human's refused text shows in the error until the editor closes. Leaving a rebased editor
without further edits (Save, ⌘↵, opening Finish, Send) sends nothing and discards that text. Replacing the textarea
value also clears its undo history. The target-mismatch close drops the human's text; it happens only when another
writer reuses a page-generated UUID, since an edit never changes a target and a lost create fixes its target. In that
case, a reload caused by another editor's write also closes this one, without a toast. Closing an editor (Esc, Close)
whose lost request the resend finds refused closes it as asked: the reply settled the request.

This does not contradict Q-37's rejection of adopting the comment found by reloading: that rejection is about a lost
reply, where the editor cannot tell its own write from another writer's. After a 412 the stored text is known to
differ, and it is shown before any further write.

Rejected:

- Closing the editor on every 412: it loses the text and the focus.
- Keeping the human's text over the newer version: the next implicit leave would overwrite text the human has not
  chosen to replace, against Q-27. The refusal shows the other version, but an implicit leave is not a choice.
- Blocking the first leave after a rebase: it brings back a refused leave, the defect being fixed, for text that is
  already shown.
- Adopting the stored target on a mismatch: the diff view would then hold an editor for a comment at another place or
  in another file.

### Q-42 — Closing the review page ends the session

Decided by the repository owner on 2026-11-05. Proposed for contract revision 2 (new REQ-SES-012, and changes to
REQ-SES-003, REQ-AGT-001 and AC-SES-001); not accepted with it.

Before this, a human who closed the review tab left `rvw` running until someone pressed Ctrl+C, and the agent
waited. Now the page sends a close notice on `pagehide`: `POST /api/page-closed`, a `keepalive` fetch with the
session token. It is same-origin, so it passes the checks of Q-44 without a preflight. The server then ends the
session as page closed after a 10-second grace, unless some page makes an authenticated request first. `rvw` keeps
the saved draft, writes nothing to stdout, writes `rvw: the review page was closed; saved draft changes are kept at
<path>` to stderr and exits 4.

The presence rule is that any authenticated request after the notice keeps the session. A reload sends the notice
and then loads the session again. Another open tab polls the status. Two things keep that tab's request inside the
grace:

- The server sets the poll interval to half the grace, at most 5 s, and sends it in `GET /api/session` as
  `status_interval_ms`.
- The closing tab posts `closing` on a `BroadcastChannel`, and each tab that receives it polls 1 s later.

A notice sent while a page enters the back/forward cache counts as a close, because the page may never return. A
page restored from the cache polls at once, inside the grace.
`TestBrowserBackForwardCache` restores a page from the cache in Chrome and checks both halves.

A restore or a sibling's close asks the page's one poll loop for an immediate poll. `Page.pollRun` owns which run is
live; the timer is only its schedule. A superseded run stops before and after its fetch, and does not report
`isGone`; the live run reports it, at most one interval later. A superseded run's fetch may still settle and apply its
reply, which `State` applies idempotently. Before this, every restart started a second loop when a fetch was in flight,
and the sibling's message had its own status path. `TestBrowserPollRestartKeepsOneLoop` covers a restart with a fetch
in flight, triggered both by a synthetic `pageshow` and by a sibling message. A real cache restore with a fetch in
flight is still untested in Chrome.

Known limits:

- A request already in flight from the closing tab may count as presence. The session then keeps running until
  Ctrl+C or the next notice.
- A sibling tab that is frozen or in the back/forward cache gets no `BroadcastChannel` message and does not poll, so
  the session can end with exit 4 while that tab still exists. Its draft is kept.
- Chrome under heavy load can tear the closing tab down before its `closing` message leaves, even though the
  message is posted before the notice. The sibling then depends on its regular poll, scheduled half the grace or 5 s
  apart. That keeps the session only while the browser runs the sibling's timer on time. A visible sibling's timer
  runs on time. A hidden sibling's timer may be delayed beyond the grace (see the rejected "Polling alone"), and the
  session can then end with exit 4 while that tab still exists. Its draft is kept.
- Text not yet saved in an editor is lost. The stderr line and the exit-4 wording say "saved draft changes".
- A crashed browser sends no notice, so Ctrl+C is still needed.

Rejected:

- A heartbeat timeout that ends the session when polls stop: a sleeping laptop or a throttled background tab would
  end the session.
- `navigator.sendBeacon`: it cannot set `Authorization`, so it would need a second way to authenticate.
- A registry of page IDs that ends the session when the last page leaves: a crashed tab never deregisters, so the
  session would never end.
- Polling alone for several tabs: browsers may delay a hidden tab's timers beyond the grace. Chrome's intensive
  throttling allows one wake-up a minute for chained timers in a tab hidden for 5 minutes. Whether it applies to this
  poll, whose timer is set from a fetch continuation rather than from a timer callback, is not verified. The
  `closing` message is an event, not a timer, so it reaches a hidden tab that is not frozen.
- Sending the grace to the UI instead of the interval: the UI would duplicate the rule that derives the interval.
- Saving pending editor text at unload: a keepalive save would race the notice, and its failure has no one to report
  to.
- A fixed 5 s interval with a compile-time check against the grace: the check covers only the default grace, not the
  shorter ones tests configure.
- An in-flight flag plus a "poll again" flag instead of the run counter: if a fetch frozen in the back/forward cache
  never settles, polling stops for good and the close grace ends the session.
- Aborting the superseded run's fetch: it adds an `AbortSignal` path through `State.syncStatus` and `Api.status` and
  gives no extra guarantee.

### Q-43 — Copy and finish

Decided by the repository owner on 2026-11-05. Proposed for contract revision 2 (new REQ-OUT-008, and a change to
AC-CMT-002); not accepted with it.

The Finish dialog gains a Copy and finish button between Copy as Markdown and Send review. It saves what the dialog
shows, puts the Markdown result for the saved draft on the clipboard, and finishes only after the clipboard write
succeeds. A refused write shows the selectable text and keeps the session open; a failed save shows the save failure
and neither copies nor finishes.

One path copies and one path finishes. `FinishDialog.copyResult` starts the clipboard write before any await, so
the click's user activation still holds; the save and the result fetch run inside the `ClipboardItem`'s promise. It
resolves to the copied text. `FinishDialog.send` finishes and compares the delivered Markdown with that text. Both
are rendered by `review.RenderMarkdown`, so they are equal unless the draft changed between the copy and the finish,
for example from another tab. The end page then says "The review changed after it was copied; copy it again below",
and its Copy button holds the delivered text. Otherwise the end page is titled "Review sent and copied".

Rejected:

- Finishing inside the `ClipboardItem` promise: the clipboard's refusal is only known after the review has
  finished.
- A draft-revision `If-Match` on finish, so the finish fails when the draft changed after the copy: it adds a
  session-wide concept for a rare race that the stale end page already makes visible.
- Copying the finish reply: by the time it arrives no user activation remains, so Safari refuses the write.
- Treating every rejected write as a refusal: when the save failed, the fallback would show text that was not saved
  as if it were the result.

### Q-44 — Cross-origin checks from the standard library

Decided by the repository owner on 2026-11-05. Proposed for contract revision 2 (REQ-SEC-002, AC-SEC-001); not
accepted with it.

Every request passes three checks in `internal/server`, in this order:

1. The Host header must equal the listener's address (421). This stops DNS rebinding: a page on another name that
   resolves to 127.0.0.1 sends its own name as Host, and the next check compares Origin with that very Host.
2. An API request must carry the session token (401, REQ-SEC-001). Only the page holds it, from the URL fragment.
3. `http.CrossOriginProtection.Check` (403). GET, HEAD and OPTIONS pass. Otherwise Sec-Fetch-Site `same-origin` or
   `none` passes and any other value is refused; without Sec-Fetch-Site, an Origin whose host differs from the Host
   header is refused, so `null` is refused; a request with neither header passes.

The rule compares only the Origin's host, because the standard library compares only the host. The listener serves
plain HTTP on a loopback address, so a page on another scheme at the same host and port cannot exist as another
site. A request with neither header is accepted: browsers send Sec-Fetch-Site on every request since 2023, so such a
request comes from a non-browser client, and the token already stops a forged one.

Rejected:

- Keeping the hand-written Origin comparison: it is custom security code and ignores Sec-Fetch-Site.
- Also refusing writes that carry no Origin: that brings the custom code back, and the token already blocks such
  requests.
- `CrossOriginProtection` without the Host check: it does not cover DNS rebinding.
- Comparing the Origin's scheme: it needs custom code and protects nothing on a loopback HTTP listener.

### Q-45 — Graceful shutdown

Decided on 2026-11-05 (implementation), from an audit of the shutdown path against the usual rules for a Go HTTP
server. [The architecture's Shutdown entry](architecture.md#state-and-lifecycle) records the resulting order.

- Stop accepting, finish the requests in flight, then release resources: met by `Shutdown` with a 5 s deadline, then
  `Close`, then the deferred `store.Close` and `repo.Close`, in reverse order of creation. One deliberate deviation:
  `Run` cancels the requests' base context before `Shutdown`, because once the session has ended no reply to a read
  has a consumer. A mutation started before the end holds the session's mutex and `End` waits for it, so the cancel
  never tears a draft write; the finish delivery is detached with `context.WithoutCancel`.
- `Shutdown`'s error is checked; `Close` is the fallback. When the result was already delivered, the error is reported
  on stderr and the exit status stays the outcome's.
- Context-aware work: no handler sleeps or uses `context.Background`. `context.WithoutCancel` appears only in the
  finish delivery, the shutdown deadline and the `cat-file` process, which `Repo.Close` cancels.
- Signals: `cmd/rvw` keeps its own channel, now buffering two signals instead of one, so a second signal sent before
  the first is read is not dropped.
- The one gap found: a process killed between `CreateTemp(".draft-*")` and `Rename` left the temporary file forever.
  Startup expiry now removes temporary draft files older than 30 days; a live session's temporary file exists for
  milliseconds.
- A readiness probe and a drain delay do not apply: the only client is one loopback page, behind no load balancer.

Rejected:

- `signal.NotifyContext`: its cause is an unexported error type, so telling SIGINT (130) from SIGTERM (143) would
  compare strings; and its `stop` restores the default action, so a second SIGINT would kill the process without
  `Abandon` or the outcome report, against REQ-SES-007.
- Cancelling the base context only after `Shutdown` fails: shutdown would wait up to 5 s on Git work whose reply
  nobody reads.
- A drain delay before `Shutdown`: nothing routes new requests away from a loopback listener, so it only delays exit.

### Q-46 — One editor transition at a time

Decided on 2026-11-05 (implementation), from an architecture review.

Leaving the diff view's open editor is one gate, `FileView.leaveEditor`, for every transition: opening another
editor, showing another file and opening Finish. While a leave is pending, every other transition is refused; the
transition that started the leave decides what is shown next. A failed save keeps the editor open with the reason and
releases the gate.

Root cause of the defect: the rule "an editor switch is pending" was enforced by one caller, Finish, through a
`switchingEditor` check, while `show` never checked it. A file navigation during an editor switch therefore awaited
the same in-flight save as the switch, and both resumed: the file changed and the switch's editor was dropped. Esc
stays ungated: it can only close the editor, which the pending transition then finds closed.

Consequence: `Page.regroup`'s automatic reselect is dropped when it fires during a pending leave. Before, it
navigated after the save; now the human's transition wins, and the generated file stays shown until the human moves.

Rejected:

- `show` rechecks after its await and also leaves the newly opened editor: it chains a second save with a second
  failure path, and the human's Edit click silently becomes a navigation.
- Queueing the navigation to run after the switch: hidden deferred work, and it is unclear which click wins.
- A `switchingEditor` check in each caller: that duplicated policy caused the defect.

## Supersession on acceptance

Done with the acceptance of contract revision 1 (2026-11-05):

- [rvw-specification.md](https://github.com/jacoelho/rvw/blob/75309d2/docs/rvw-specification.md) and its acceptance
  record are superseded and deleted;
- [tui-design.md](https://github.com/jacoelho/rvw/blob/75309d2/docs/tui-design.md) is deleted, including its Chroma rejection;
- [AGENTS.md](../AGENTS.md) is rewritten for three sources, one output, and macOS only;
- the pre-rewrite analysis's sections on Pierre, PR fetching, GitHub publication and MCP are superseded by Q-02,
  Q-04 and Q-05; the analysis is since deleted.
