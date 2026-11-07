# rvw architecture

The architecture entry point. [The contract](contract.md) says what `rvw` must do;
[decisions.md](decisions.md) records why, and the alternatives rejected. This document says how the code is shaped
to do it and which rules keep it that way. Where it and the contract disagree, the contract wins.

## Shape

`rvw` is one Go process serving one review session to one browser page over loopback. An agent runs it and blocks; a
human reviews in the browser and finishes; `rvw` writes the review result to stdout and exits.

```text
agent ──runs──▶ cmd/rvw ─── lifecycle, signals, stdout
                  │
                  ├─ internal/source    git facts: snapshot, file list, patches, contents, gaps, content search
                  ├─ internal/review    the draft: comments, summary, viewed marks, lock, result
                  └─ internal/server    loopback HTTP: auth, translation, bounded file work
                        ├─ internal/highlight  syntax spans: one linear scanner per language
                        ├─ internal/worddiff   word changes in paired lines
                        └─ web (embedded dist) ◀── browser
```

Dependencies point one way: `cmd/rvw` → `server`, `review`, `source`, `web`; `server` → `review`, `source`,
`highlight`, `worddiff`; `review` → `source`, for the snapshot, its file facts and quoted lines. Nothing depends on
`server` or `cmd/rvw`. `highlight` and `worddiff` depend on nothing in the module.

## Ownership

Each fact has one owner. Other modules ask the owner; none recomputes its rules.

| Owner | Owns | Does not own |
| --- | --- | --- |
| `internal/source` | Every Git fact, through its single `git` runner: origin/HEAD's target (`OriginHead`), the snapshot's trees, the changed-file list and renames, patches and hunks, file contents, line counts, the large-file rule, context gaps and which lines are showable, vendored directories, generated Go files, content search: its text rules (`NewSearchText`), bounds, order, pathspec batching, which files it reads (through the mode-only predicate it shares with the diff) and each match's window, split at rune starts | Anything about comments or the session |
| `internal/review` | The draft and its one writer, `Session`: comment targets and their validation, limits, authorship, the viewed marks, the summary, the draft file and its lock, quoting and the omission note, result order and rendering in Markdown and JSON | Git, HTTP, presentation |
| `internal/server` | The HTTP boundary: token, Host and cross-origin checks, CSP, request-shape validation, translation between wire JSON and domain types, the file-request slots, the search endpoint and its slot (it only replaces invalid UTF-8 in the match windows), the background reads of line counts, generated files and whitespace-only files, the end of the session | Business rules: it asks `review` and `source` |
| `internal/round` | The review ledger, its store and lock, retention refs it names, round states, pre-set viewed marks, earlier comments and their statuses (see Review rounds) | Git commands, the draft, HTTP |
| `cmd/rvw` | Arguments, defaulting an omitted base ref to origin/HEAD's target, the session's start and end, signals, opening the browser, the stdout write, reading the ledger at start and recording a delivered round | Everything above |
| `web` | Layout and interaction. One client (`Api`) decodes every reply at the boundary; one store (`State`) holds every draft and status fact and changes only after the server saved it; one `CommentEditor` owns a comment's save rules. `State` alone says whether a comment is saved; an editor's row placement derives from it; the editor owns only the base its writes assume. The search palette owns query parsing, fuzzy file ranking and its transient query and results. The review strip (`strip.ts`) draws each file's viewed state from `State` in tree order and holds only which file is shown | Any rule the server owns: gaps, vendored or generated status, result order, quote limits, the omission note, search-text rules |

Consequences that keep ownership single:

- `source.Snapshot` alone decides how a changed file's sides are paired: the list's raw records. A file's patch and line
  counts are computed from its record with `git diff-pairs`, never by re-running the listing restricted to its paths.

- The UI derives nothing from patches or paths. The server sends numbered rows, gaps, each file's vendored directory,
  each comment's file index, target path and an opaque order key, and the quote limits.
- The UI creates a comment by file index (`review.Placement`), never by path; `review` alone picks a target's path from
  its side (`targetPath`), for UI creates and for the seed and draft path lookup alike. A target's path in a reply is
  for display and is never sent back.
- Comment validation and context expansion use the same gap rule from `source`, so a line the UI can show is exactly a
  line a comment can target.
- The Finish dialog and the result share `review.Quote.Note` for the omission note.

## Boundaries and validation

External data is validated once, where it enters, and converted to domain types:

- **`git` output** is parsed in `source` into typed records; malformed output is an error with the command and
  context, never a partial record.
- **Seed documents** are parsed strictly (unknown members rejected) and validated against the snapshot in `review`
  before anything listens.
- **HTTP requests** are checked for shape in `server` (400), then handed to `review`, which applies the domain rules
  (422). A `review.Placement` is data: the server checks only its kind and members, and the session checks it against
  the snapshot.
- **Search text** (`q`) is validated by `source.NewSearchText` before a slot is taken (400); the session document
  carries its length rules so the palette checks the server's values. **`git grep` output** is
  checked per record: a path among the batch's changed paths, line ≥ 1, column ≥ 1 and a matched span inside the
  line; anything else is an error, never a partial match.
- **Server replies** are decoded in the UI's `Api` into readonly types, including the `TextDiff | OtherDiff` union. `toSearchReply` checks each match's file index against the file
  list, that the file has the match's side, its line and a non-empty match.
  The lines inside diff blocks are trusted unchecked: checking them costs a pass over every shown line, and the
  binary that embeds the page sends them.

Inside a boundary, values are trusted; there is no re-validation.

## State and lifecycle

- **Snapshot.** Immutable once resolved. Line numbers in the draft and the result refer to the served snapshot, not to
  the live worktree. A refresh replaces it; nothing else does.
- **Change check and refresh** ([Q-57](decisions.md#q-57--change-notice-and-refresh), contract revision 10,
  accepted). `source.Snapshot.Changed` decides whether resolving the source again could give other trees; an
  uncommitted snapshot keeps its private index for it, which `Snapshot.Close` deletes, and `Repo.Close` deletes every
  live snapshot's. The server holds the served snapshot and what it derived from it as one `generation` (snapshot,
  round facts, content facts, the moved list, the sticky changed flag), replaced whole through an atomic pointer.
  - Requests: every handler that reads or changes the review is `bound`: it holds `refreshMu` shared and checks the
    `Rvw-Generation` header (400 when missing, 409 with the current id when another). The session document is served
    for whichever generation is current and names it. Cancel, status and the close notice are not gated.
  - Refresh: `POST /api/refresh` holds `refreshMu` exclusively, so it waits for bound requests in flight and holds new
    ones until the next generation is stored. It calls `Config.Refresh`, which `cmd/rvw`'s `drafts` owns: resolve the
    source again, `review.Session.Refresh` with an opener that opens and locks a store for a key, then close the
    replaced snapshot. The session is the one owner of the draft store: `review.Start` takes the store (closing it on
    failure), `Refresh` closes the store it replaces, and `Session.Close` closes the current one; `cmd/rvw` keeps no
    store. `Session.Refresh` is the sole decider of "unchanged":
    it keeps the generation only when the draft key and `source.Snapshot.SameFiles` both match. `SameFiles` includes
    per-file blob presence, so in a partial clone a blob arriving that completes a file's blobs advances the generation and open pages reload. A
    same key with a different listing is carried over in place into the current store and advances the generation; a
    new key's store comes from the opener, which `Refresh` calls at most once and closes on failure. The server stores
    `Refreshed.Snapshot`. Invariant: file indexes in session state and in a generation belong to exactly one listing
    (contract revision 11, REQ-SES-013).
    The change check also asks, for a listing built with blobs missing, whether any arrived (one `cat-file
    --batch-check` run, lazy fetch off), so the notice shows before the human refreshes.
    `Session.Refresh` carries the draft over without holding the
    session's mutex while it diffs and fails with `ErrBusy` if the draft changed meanwhile; on any error the session,
    its store and the generation are unchanged (a draft already under the new key may have been moved aside). The same draft key and the same file list keep the generation id, its facts and its moved list.
  - Content facts: `factRuns` starts each generation's background reads under `Run`'s context; a refresh stops the
    replaced generation's reads after the new one is stored, and `Run` waits for all of them before it returns.
  - Check: a status request with `check=1`, which the page sends only while visible, runs `Changed` when no check is
    running (`probeMu.TryLock`), no refresh holds the lock (`refreshMu.TryRLock`), the generation is still current and
    the last check ended at least 10 s ago. The result sticks in the generation until a refresh.
  - Page: a 409 naming a generation makes the page reload, or show a reload prompt while an editor or the Finish
    dialog is open; a status reply naming another generation does the same, so other tabs follow a refresh. The moved
    list shows until dismissed, per generation, in `sessionStorage`.
- **Draft.** `review.Session` is its only writer. Each mutation is validated, written to a temporary file, fsynced and
  renamed into place, and only then applied in memory, so the draft on disk is never behind what the UI was told.
- **Lock.** An exclusive `flock` per snapshot key makes one session the only writer. A second instance reports the
  first one's URL instead of starting. Startup runs in order: resolve the snapshot, parse the seed, take the lock,
  load the draft, serve. The lock records the URL only once serving starts.
- **Draft retention.** A resumed draft ignores the seed. A draft that cannot be restored is moved aside, not
  overwritten. At start, drafts unused for 30 days, and moved-aside drafts and temporary draft files older than
  30 days, are deleted.
- **Session end.** Exactly one outcome: finished, delivery failed, cancelled, interrupted, page closed or failed. Once finishing
  starts, mutations are refused. Of the outcomes, only a delivered result or a cancel deletes the draft. The finish
  request is detached from request cancellation, so a closing browser cannot abort delivery. Three owners:
  - `review.Session` owns the phase changes and the draft's fate. `Finish` returns a typed result: delivered (with a
    failed draft delete only as a warning), a `*DeliveryError` (ended, draft kept), `ErrEnded`, or a build error that
    reopens the session; once delivery starts the session never reopens. `End` reports what ended the session first:
    `End` itself, a finish that reached delivery, or a cancel. `Abandon` ends the session the same way but gives up on
    a delivery in progress, and reports the session's fate: interrupted (nothing written, draft kept), cancelled,
    delivered, or undelivered (delivery failed or abandoned; part may be written; draft kept). `End` and `Abandon`
    share one interrupt transition. `Finish` also returns a `*DeliveryError` with `ErrAbandoned` when its delivery was
    abandoned, even if deliver then succeeded.
  - `server.Run` turns the phase, a signal, the page presence and time into an `Outcome`, including the finish wait.
    When the wait runs out it abandons the delivery through the session (`Abandon`), so a write that completes later
    is still reported undelivered and keeps the draft ([Q-35](decisions.md#q-35--abandoned-deliveries)).
    `server.presence` owns the close grace timer; `Server.pageClosed` owns the reason, so `Run` and `Abandon` both
    report page closed when the grace ended a session whose fate is interrupted.
  - `cmd/rvw` owns the signals, the exit statuses and their single stderr report; a second signal asks the server,
    which asks the session (`Abandon`), so one chain decides the exit status.
  - Everything on stderr is a `log/slog` text record without a time: `level=… msg="…"` and named values, quoted
    when they hold spaces, `=` or quotes, so the review URL and draft paths are quoted. `cmd/rvw` builds the one
    logger and passes it to `source.Resolve` and `server.Config.Log`; the `log` package is denied by depguard.
- **Page presence** ([Q-42](decisions.md#q-42--closing-the-review-page-ends-the-session)). The page sends a close
  notice on `pagehide`; `server.presence` then arms one timer for the 10 s close grace. Any request that passes the
  Host, token and cross-origin checks disarms it, so a reload or another open tab keeps the session; when it fires,
  `Run` ends the session as page closed (exit 4, draft kept). A later notice restarts the grace. The page has one
  status poll loop; a restore from the back/forward cache or a sibling's close asks it to poll at once. `Run` stops the
  presence when it returns; after that, notices and requests do nothing. Failure modes: a crashed browser sends no
  notice, so Ctrl+C is needed; a sleeping machine sends neither notice nor polls, and its session survives; a frozen
  or cached sibling tab does not poll, so the session can end while it exists, keeping the draft; a closing tab's
  `BroadcastChannel` message can be lost under load, leaving the sibling's regular poll to keep the session, which
  a hidden sibling's throttled timer may not do within the grace (exit 4, draft kept).
- **Signals.** The first SIGINT or SIGTERM ends the session, but a delivery in progress decides the outcome, within
  the finish wait of 30 s; a delivery that does not complete in time is a delivery failure. A second signal ends the
  session at once and exits with its outcome: 0 if the result was already delivered; 1 if a delivery had started and
  not completed, or had failed (draft kept, REQ-SES-007); 3 after a cancel; otherwise the first signal's status with
  nothing written (REQ-SES-006), including after a serve failure unless `rvw` has already reported exit 1. Ending the
  session stops a result still being built from being delivered. SIGPIPE is ignored, so a closed stdout is a
  delivery failure, not a silent death.
- **Shutdown** ([Q-45](decisions.md#q-45--graceful-shutdown)). `server.Run` ends in one order once the outcome is
  known:
  1. It cancels the requests' base context, then calls `http.Server.Shutdown`, which stops accepting and waits for the
     requests in flight. Cancelling first is deliberate: once the session has ended, a read has no consumer, so
     shutdown does not wait on its Git work. A mutation started before the end holds the session's mutex, and the end
     waits for it, so the cancel never tears a draft write. The finish delivery is detached with
     `context.WithoutCancel`.
  2. `Shutdown` has a 5 s deadline; its error is returned and `Close` is the fallback. When the result was already
     delivered, the error goes only to stderr and the exit status stays the outcome's.
  3. The deferred `Session.Close` (the current store) and `repo.Close` (every live private index) run after `Run`
     returns, so the draft lock and the `cat-file` process
     are released only after every request has ended, in reverse order of their creation.
  4. Handlers neither sleep nor use `context.Background`. `context.WithoutCancel` appears only in the finish delivery,
     the shutdown deadline and the `cat-file` process, which `Repo.Close` cancels; each `cat-file` exchange is
     cancelled through `context.AfterFunc`.
  5. `cmd/rvw` reads signals from a channel that buffers two, so a second signal sent before the first is read is
     kept.
  6. A process killed between creating a temporary draft file and renaming it leaves the file; startup expiry removes
     temporary draft files older than 30 days.
  7. There is no readiness probe and no drain delay: one loopback page is the only client, and no load balancer
     routes to it.
- **Processes.** The long-lived `git cat-file --batch-command` and the background `numstat` and `grep` reads belong to the
  session. Ending the session cancels their contexts, which kills them; in-flight requests are cancelled the same way,
  so shutdown never waits on Git. Line counts stream: each record goes to `git diff-pairs` followed by a lone NUL, which
  makes it write that record's count while its stdin is still open, so each count is parsed, checked and published as
  it arrives, and the context is checked between counts.
- **Comment writes.** Each comment carries a version from a session counter; every create, seed and restore, and every
  edit that changes the body, assigns a new one, and versions are not persisted. A create sends `If-None-Match: *`; an
  edit changes only the body and sends `If-Match: "<version>"` with the version its editor loaded, so an edit never
  overwrites text it did not load nor recreates a deleted comment (412). A write whose target and body equal the stored
  comment returns it unchanged, so an editor resends a request whose reply was lost and adopts only its own reply
  ([Q-27, Q-37](decisions.md#q-27--comment-order-file-index-and-save-preconditions)). On a 412 the UI reloads the
  comments, and the editor shows the stored comment, or closes when that comment is gone or at another target
  ([Q-41](decisions.md#q-41--a-refused-write-loads-the-stored-comment)). A delete sends the version the page shows, so
  it never discards an edit the page has not loaded; on a 412 the UI reloads the comments. The summary and viewed marks
  are last-writer-wins ([Q-27](decisions.md#q-27--comment-order-file-index-and-save-preconditions)).
- **One open editor per page.** An editor is left (`CommentEditor.leave`: saved, or closed when blank) before another
  opens: Finish opens only after the diff view's editor is left, and the dialog leaves its editor before opening another.
  A failed save keeps that editor open with the reason, and nothing else opens. The dialog's editor saves only itself.
  `FileView.leaveEditor` is the single gate for leaving the diff view's open editor, whether to open another editor,
  to show another file (tree click, j/k, ]/[, a palette result, an automatic regroup reselect) or to open Finish
  ([Q-46](decisions.md#q-46--one-editor-transition-at-a-time)). While a leave is pending, every other transition is
  refused; the transition that started it decides what is shown next. A click on Edit while Finish is leaving the
  editor opens nothing, because that editor is still saving. A palette result in the shown file also goes through
  `leaveEditor`, then reveals its line without reloading the file.
- **Content search.** Each search's `git grep` runs belong to its request: they stop when the client goes away, the
  session ends, the match cap or output limit is reached or the 2 s budget is spent. The UI sends a search 200 ms after
  the last keystroke, aborts it on the next keystroke, close or mode change, and applies a reply only while it is the
  current search. The palette's cleanup on close runs synchronously, not on the dialog's later close event.
- **Saved comments and editors.** `State` alone says whether a comment is saved; an editor's row placement derives from
  it, so every rebuild, including the ones `State` triggers while a save is still in flight, shows one row per comment
  ([Q-39](decisions.md#q-39--whether-a-comment-is-saved)). The Finish dialog shows its open editor whatever its
  filter. The editor owns only the base its writes assume: none, so it creates, or the version and text it edits. It
  adopts a version only from its own reply or a 412 reload, and never keeps a create for an id `State` holds
  ([Q-41](decisions.md#q-41--a-refused-write-loads-the-stored-comment)). An edit whose text equals the base's text
  sends nothing ([Q-40](decisions.md#q-40--an-unchanged-editor-sends-nothing)).
- **Server-side state.** The current generation (snapshot, changed-file list, background facts), the draft. Nothing per file is
  retained after a reply.

## Bounds

Designed for at least 1,000 changed files of 10,000 lines each and up to 4,096 comments:

- A file's patch and contents are read only when the UI asks for it. Every request handler that runs `git` takes one of
  two file slots first: file and context views, line and range creates, quotes, content searches and the result preview. This bounds
  server memory to two files. A request still waiting for a slot when the session ends replies 503. Two paths take no
  slot: Finish, which the session phase serializes and which must not wait behind UI reads, and the change check,
  which runs one at a time and at most once per 10 s. A refresh takes no slot: it holds `refreshMu` exclusively, so no
  slot holder runs beside it. Quoting streams lines, so the quote limits bound its memory.
- Every one-shot `git` command runs through the runner's `stream`, which owns the stdout and stderr caps, stopping and
  reaping a process whose reader gave up, cancellation and the mapping of exit statuses to errors; every read of its
  output is therefore capped. The one exception is the long-lived `git cat-file --batch-command` (`objects.go`), which
  is written to and read from for the session's life; each reply is read to the size git announces for it, which its
  callers check first (the large-file rule).
- Pathspec arguments, used to find vendored directories and generated files and to search content, are batched to at
  most 64 KiB by one policy (`pathspecBatches`). Patches
  and line counts take no pathspecs: their records go to `git diff-pairs` on stdin.
- The UI polls the status at the interval the server sends: half the close grace, at most 5 s. Deriving it from the
  grace keeps a sibling tab's poll inside the grace, and the 5 s cap with the 10 s check interval keeps the change
  notice within REQ-SES-010's 20 s. Page presence holds one timer.
- Highlighting runs within a 2 s budget per side and falls back to plain text after it. The scanners in
  `internal/highlight` are hand-written and linear in the side's size, with no regular expressions: a failed search
  for a closer is not repeated, frame stacks hold at most 1,024 entries, fenced code embeds at most 4 levels deep,
  and cancellation is checked every 1,024 lines. At the 1 MiB large-file limit a side takes about 5–16 ms, so the
  budget guards against defects rather than routine input
  ([Q-52](decisions.md#q-52--project-owned-highlighting-scanners)). Only the lines the reply sends get spans (a file's
  hunk lines, or the requested context range), stored in chunks of at most 4,096 spans, so a side allocates O(spans
  kept / 4,096) objects plus one slice of line headers
  ([Q-53](decisions.md#q-53--json-yaml-and-toml-and-highlighting-only-the-lines-sent)). A line has at most 500 spans;
  the rest of a longer line is one plain span, so a minified line's reply and DOM stay bounded
  ([Q-56](decisions.md#q-56--markdown-front-matter-and-a-span-bound-per-line)).
- Content search keeps at most 500 lines, reads at most 16 MiB of `git grep` output through `stream`'s limit, runs
  for at most 2 s, and keeps a 496-byte window per line; its text is 3 characters to 256 bytes. Reaching a bound
  returns the matches found first with a truncation flag.
- Word diff gives up after 256 edits, and runs only on lines up to 1,000 characters.
- A file over 1 MiB or 20,000 lines is large: no context, no highlighting.
- Comments, bodies, the summary, seeds, drafts and quotes all have fixed limits, owned by `review`.
- The review strip draws on one fixed-size canvas, so its cost is bounded by its pixels, not by the file count; when
  files outnumber pixel columns, a column shows the last file drawn into it.
- The UI renders only visible rows plus 50 rows of overscan, in every list. Replacing a list's rows keeps the
  rows in view in place: each row has an id that outlives content changes, such as a comment becoming its
  editor, and the list scrolls so the first shown row still present stays where it was drawn. Finding it
  scans the new rows once, the same order of cost as laying them out. A shown row partly above the view
  that shrinks so far it would leave the view, such as a wrapped line when the window widens, keeps its
  bottom in place instead of its top.
- Diff rows are fixed-height unless Wrap lines is on (REQ-DIFF-013, Q-51). `fileview.ts` owns the setting, kept in
  the browser under `rvw.wrap`. Wrapped code rows become measured rows of the virtual list: each starts at an estimate,
  its widest half's width in columns divided by the code column's width in characters, and its measured height is
  cached under a key holding that width, so a resize re-estimates every row not in view instead of trusting heights
  measured at another width. While wrapping is on, neither view scrolls sideways and split's shared scrollbar is hidden.
- Startup reads content only as `git` needs to list changes. Line counts and generated files are read in the
  background after the UI is served.

## Failure behaviour

- Errors carry the failing command or operation and its context. `source` reports an unreviewable source as a plain
  error whose message names the reason (REQ-SRC-007); no caller branches on it, and `rvw` prints it and exits 1.
  `review` reports a rejected target or body as a typed error, and `source` an unavailable context range as a
  sentinel. The server maps those to status codes.
- Missing blobs (partial clones) degrade rather than fail. Rename detection is skipped, the content shows as
  unavailable, and the affected files get no line count. Finding vendored directories reads trees only, so it is
  unaffected.
- Rename detection runs only when every blob is present: the file list is built without `-M`, the blobs are checked,
  and only then is it re-listed with `-M -l1000`. The result (`full`, `missing_blobs`, `limit_exceeded`) is sent to the
  UI, which shows the two degraded cases.
- If computing a file's per-file decorations for `GET /api/file` fails (whitespace-only hunks, lines added since
  review), the failure is logged and the file is served without them: no lines are marked as added since review.
  Earlier comment placement degrades per comment instead: `round.Prior.Place` diffs each commented blob once (at most
  one per side per file, each bounded like a file's patch), and a failed diff or quote read leaves only the comments
  it serves unplaced, each with its `Cause`, which the server logs. Only a cancelled request fails. Copying an earlier
  comment fails while its own placement cannot be computed, rather than falling back to its file: the copy records
  `copied_from`, so a fallback copy could never be retried onto its lines.
- A delivery failure, a signal or a server failure keeps the draft, so re-running `rvw` resumes it.
- Mutations, finish and cancel after the session ended reply 410, and the UI shows that `rvw` has stopped. Reads
  answer until shutdown; requests cancelled by the end reply 503.

## Platform and isolation

- macOS only.
- Git is reached only through the `git` executable, run without a shell, from the worktree top level, with
  `--literal-pathspecs` and a minimum version. Its environment is the inherited one minus every variable that would
  redirect the repository, index or configuration or change patch output (`GIT_DIR`, `GIT_INDEX_FILE`,
  `GIT_CONFIG_*`, `GIT_EXTERNAL_DIFF`, …), plus `GIT_TERMINAL_PROMPT=0` and `GIT_NO_LAZY_FETCH=1`.
- No Git object parsing.
- `rvw` never changes refs, the index, worktree files or configuration (contract revision 3 proposes retention refs
  under `refs/rvw/`; see Review rounds below). Uncommitted changes are snapshotted through a
  private copy of the index.
- The only listener is `127.0.0.1` on a random port. Every API request needs the session token, and the page runs
  under a strict CSP. Code and comment bodies are set as text, never as HTML.
- Every request is checked in one order: the Host header against the listener's address (421), which stops DNS
  rebinding; then, for the API, the session token (401), which only the page holds; then the standard library's
  `http.CrossOriginProtection` (403), which refuses a state-changing request that Sec-Fetch-Site marks cross-site, or
  whose Origin host differs from Host when Sec-Fetch-Site is absent
  ([Q-44](decisions.md#q-44--cross-origin-checks-from-the-standard-library)).
- `web/dist` is committed and embedded, so `go install` needs no Node. `make check` fails when `web/dist` is stale.
- File-type icons are a curated subset of [Catppuccin's](https://github.com/catppuccin/vscode-icons) (MIT, licence
  beside them), vendored as one sprite and two lookup tables under `web/src/icons/`. `web/scripts/icons.mjs` owns the
  subset and regenerates all three from a checkout, at the commit it records in them; nothing else edits them.
  `fileicon.ts` alone maps a path to an icon (file name, then longest extension), and `app.css` alone gives the icons
  their colours. The sprite adds about 65 KB to the page, which loopback makes negligible.
- `web/src/favicon.svg` is the product mark: the favicon, and inlined by the build as the page's
  brand so the end page shows it after the server has gone. `favicon.png` is the same mark
  rasterised at 32px for Safari before 26; re-render it when the SVG changes.

## Review rounds (contract revision 3, proposed)

[Q-49](decisions.md#q-49--review-rounds) records the decision.

- **Ownership.** `internal/round` owns the review ledger: its JSON format (`format.go`, decoupled from the draft's and
  the wire's), the per-worktree file and its lock (`store.go`), merge, bounds and expiry (`ledger.go`), which
  retention refs the ledger names, and what a session derives from it (`prior.go`): each file's round state, the
  viewed marks a new draft starts with, the reviewed-unchanged rule (`round.Unchanged`; the web UI's
  `State.reviewedUnchanged` applies the same rule as Hide whitespace changes, and `State` alone reads the wire round
  state, through `changedSince` and `baseChanged`), whether a file has an interdiff and why not (`Prior.Since`,
  `round.Fallback`), and the earlier comments with their statuses and Still applies targets (`Prior.StillApplies`).
  `internal/source` owns the Git commands and which pairs of blobs compare as text (`readTextSides`: kinds and
  REQ-PERF-002 limits), shared by `Interdiff` and `ChangesFrom` (`-U0` hunk ranges from any blob to a file's side),
  `BlobLines`, `WhitespaceOnly` and `WhitespaceOnlyFiles` (`-w` patch and numstat through `git diff-pairs`), and
  `KeepTrees`, `UndoKeep`, `RetentionRefs` and `DeleteRefs` (`mktree`, `commit-tree`, `update-ref --stdin`,
  `for-each-ref`) with hooks, signing and reflogs disabled per command. Every retention ref write names the commit it
  expects the ref to hold, so a session never overwrites, undoes or deletes another session's concurrent write.
  `internal/review` keeps the draft, which holds each viewed mark's whitespace flag, the Hide whitespace setting and
  each comment's copied-from ID; `Session.CopyComment` takes the targets `round` orders and keeps the first it
  accepts. `internal/server` only translates (`rounds.go`): it maps `round` values to wire names and runs each round
  call that reads Git inside a file-request slot. Dependencies stay acyclic: `round` → `review`, `source`; `server`
  and `cmd/rvw` → `round`.
- **Lifecycle.** At start, `cmd/rvw` opens the draft store, then reads the ledger once under its lock (taken within 2
  s), bounded as of now. It builds the immutable `round.Prior` for the snapshot from it to seed `review.Start`
  (`Fresh`: pre-set marks and the Hide whitespace setting, used only for a new draft) and passes the ledger itself to
  `server.Config`; `server.New` builds its own `Prior` from the session's snapshot, so the server's round facts cannot
  describe another snapshot. Building it twice costs one pass over the changed-file list. Any ledger failure is
  reported on stderr and leaves the session without history; an invalid ledger is moved aside. After a `Finished`
  outcome, `cmd/rvw` records `round.Delivered(snapshot, final draft, now)`; `Store.Record` holds the lock for the one
  recording, within 10 s, in the order: read and merge, retention ref, atomic ledger write, stale-ref deletion. The
  retention ref is dated by the newest delivery that names its snapshot, so it is never older than what the ledger
  keeps. A failed lock, read or ref write records nothing; a ledger write that fails before its rename returns the ref
  to the exact commit it held, or deletes it, while it still holds the commit this recording wrote. Stale-ref deletion
  is one transaction over the refs as listed; a ref another worktree's session moved since the listing fails it, and
  it is retried once from a fresh listing. Recomputing the ref from the old ledger was rejected: the ref's date can be
  a superseded delivery's, newer than the old ledger implies. Recording failures are reported on stderr and never
  change the exit status. No ledger work happens for any other outcome.
- **Bounds.** Ledger: 20,000 reviewed versions from at most 256 snapshots, 30 days applied on read and write, 64 MiB
  on read and write (a larger write fails the recording), and the comment limits for earlier comments; retention refs:
  at most 256 per worktree. `Prior` is computed from the changed-file list alone, so opening a session reads no blob.
  Interdiffs, `-w` patches and earlier comment statuses run inside the file request's slot, one `git diff-pairs` per
  distinct pair of blobs. Whitespace-only files are found by one background `git diff-pairs -w --numstat` after the UI
  is served, and published whole when it ends, or as none with the error when it fails, as the generated marks are.
- **Known limits.** Retention refs are deleted only when a round is recorded in some worktree, so a repository whose
  reviews stop keeps up to 256 refs per worktree. `git push --mirror` sends them and `git log --all` shows them.
  A ref under `refs/rvw/` that `KeepTrees` could not have written (another name, or not a commit) is never listed,
  deleted or counted.

## Trade-offs and known limits

| Choice | Cost |
| --- | --- |
| Re-run `git diff-pairs -p` per file request instead of caching patches | Repeated Git work on revisits; buys flat memory |
| Highlight the whole side, not the requested window | Each request rescans both sides, up to about 16 ms at 1 MiB; a multi-line token can span any window. Spans are kept only for the lines sent |
| Buffer a patch whole, up to 64 MiB | A huge rewrite's reply is large; streaming is the next step if it matters |
| Seed validation reads each commented file's diff | A 300-comment seed over 150 files measured 6 s of startup |
| Snapshot uncommitted changes with `git add -A` into a private index | A large unignored, untracked tree is slow and fills the object database; the fix is to ignore it |
| Content search reruns `git grep` per query, with no index | Repeated blob reads per query; buys flat memory and no staleness |
| Search decides per searched side, without reading patches | A match may sit on a line the view cannot show (large by lines, other side large or binary); the file opens with a notice. Symlink targets are not searched |
| ASCII-only case folding in content search | `école` does not find `ÉCOLE` or `École`; `git` and rvw agree in every locale |
| The long-lived `cat-file` grew from 38 MB to 70 MB over 520 file views | Not yet explained |
