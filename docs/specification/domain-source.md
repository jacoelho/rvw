# Domain and source

## 6. Architecture and ownership

`rvw` is a pure Go program. It MUST NOT link Rust, require CGO, invoke a prior `rvw` executable, or
mechanically reproduce the Rust crate, task, event-loop, or renderer structure.

The interactive application uses:

- `charm.land/bubbletea/v2` at `v2.0.8`;
- `charm.land/bubbles/v2` at `v2.1.1`;
- `charm.land/lipgloss/v2` at `v2.0.5`.

The three versions above form the specified dependency baseline. A dependency upgrade is an
architecture change and MUST reverify the version-specific
[runtime and signal contracts](runtime-lifecycle.md).

The pinned upstream modules declare `github.com/charmbracelet/x/windows` in their module metadata.
`rvw` does not import that package, compile its Windows files, add Windows behavior, or support a
Windows target. Requiring Bubble Tea v2 makes that unreachable transitive module metadata
unavoidable. The Linux/macOS-only constraint forbids direct or runtime Windows code and
dependencies; it does not require rewriting the upstream module graph.

The Go ownership boundaries are:

```text
cmd/rvw
  -> internal/config
  -> internal/delivery
  -> internal/output
  -> internal/source
  -> internal/review
  -> internal/app

internal/app -> internal/review, internal/source, internal/delivery
internal/delivery -> internal/subprocess
internal/output -> internal/review, internal/source
internal/source -> internal/review, internal/subprocess
```

`cmd/rvw` parses CLI input, composes dependencies and distinct program/work contexts, opens the
initial snapshot, selects interactive or noninteractive output, owns the one `tea.Program` and the
one application termination-signal registration, and maps final errors to exit status. It contains
no review policy.

`internal/config` alone discovers, reads, validates, and merges bounded user/project configuration
and themes. `internal/output` streams the human and JSON contracts to an `io.Writer`; it never
constructs or starts a TUI.

`internal/review` owns validated identities, source and workspace generations, files, selection,
marks, comments, drafts, reload reconciliation, export/publication state, and pure transitions. It
imports no terminal, filesystem, network, subprocess, clock, or concurrency package. It exposes
concrete domain operations and immutable snapshots, not a generic effect interface.

`internal/source` is the sole Git review-data, `gh`, snapshot, patch/blob, reload, search-input, and
GitHub publication boundary. It discovers the Git root once and owns source-specific repository
inputs including `.gitignore`, `.rvwignore`, and configured ignore files. It does not read general
application configuration or themes.

`internal/delivery` owns destination permits and stdout/clipboard commit states. It returns an
immutable stdout payload to the post-Program root adapter. Clipboard delivery uses
`internal/subprocess` and never inherits the active terminal streams.

`internal/subprocess` solely owns each live child, its captured pipes, and its process-group ID from
start through `Wait`. It accepts structured executable/argument requests, never invokes a shell,
and implements the Linux/macOS
[process-group contract](runtime-lifecycle.md#11-signals-subprocesses-and-shutdown).

`internal/app` owns the one root Bubble Tea model, concrete child Bubbles, effective key bindings,
responsive layout, semantic Lip Gloss styles, command constructors, and the closed coordinator
described in [Bubble Tea application and work](runtime-lifecycle.md#10-bubble-tea-application-and-work).
Rendering remains beside the model until a demonstrated independent invariant justifies another
boundary.

Reverse edges, same-level cycles other than the listed edges, and imports that bypass the effect
owner are forbidden. Public APIs expose domain operations and validated values, not internal
storage, component registries, queues, worker handles, process handles, or renderer caches. A new
abstraction requires a current producer and consumer. Compatibility facades, duplicate execution
paths, generic source registries, generic effect systems, component registries, and a second event
loop are forbidden.

Go source files and packages MUST be cohesive. Line count alone is neither a conformance limit nor
justification for splitting one cohesive implementation into shallow files.

## 7. Review model

One review owns:

- immutable changed-file identity and metadata for one source revision;
- active file and navigation state;
- per-file and per-hunk reviewed marks;
- review-, file-, and line-anchored comments;
- comment drafts;
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

Filtering hides files without deleting them from identity, comments, review marks, counts, JSON, or
reload reconciliation.

## 8. Source acquisition and materialization

Local sources are rooted in a discovered Git repository. Worktree and staged review resolve the
`HEAD` tree, or the empty tree when `HEAD` is unborn. Worktree review compares that base with a
synthesized tree for the worktree and includes untracked files by default. Staged review compares
that base with the index tree. Range review compares the supplied base and head. Pull-request review
resolves immutable base/head object IDs and metadata through `gh`.

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

## 9. Export and publication

Markdown and JSON export are prepared from an immutable review snapshot. Every comment is eligible
for export except that unmappable comments require the explicit export option. Comment-export JSON
objects contain `target`, `binding`, and `comment`; there is no category or type field. Preparation
is bounded and cancellable.

Interactive copy/write actions are in-session deliveries, not additional top-level output modes.

Destinations are clipboard or stdout. Destination delivery is separate from export preparation. A
delivery permit prevents overlapping or reordered writes. A destination that may have committed
before reporting failure remains fenced through physical worker cleanup. If the outcome remains
unknown, that destination remains unavailable for the process lifetime. Reconciliation applies
only to GitHub publication.

GitHub submission always uses an explicit preflight that shows:

- event: comment, draft, approve, or request changes;
- summary body;
- included and excluded comments;
- unmappable-comment policy;
- source/head staleness;
- any explicit stale-head override.

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

### 9.1 Publication restart scope

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

GitHub publication implementation remains blocked until this specification set is accepted.
Durable cross-process fencing is outside this specification set and MUST NOT be added implicitly.
Adding it requires an explicit contract for storage location, private permissions, interprocess
locking, atomicity and directory durability, versioning, corruption, bounded retention, privacy,
and cleanup.
