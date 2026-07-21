# Nonfunctional conformance

## 14. Required bounds and failure behavior

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
| One terminal-safe error detail | 16 KiB |
| Captured subprocess stdout | 64 MiB |
| Captured subprocess stderr | 1 MiB |
| One external process attempt | 30 seconds |

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
| Concurrent subprocesses | 4 |
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
| Post-`KILL` direct-child reap and initial-group quiescence | 5 seconds |
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

## 15. Security and integrity invariants

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

## 16. Verification

Conformance requires all of the following:

| Area | Required verification |
| --- | --- |
| CLI | Process tests for every source/output pair, help forms, range and PR grammar, repeated options, global filters, invalid UTF-8, and exit behavior |
| Configuration | Precedence, aliases, unknown fields, byte limits, project keybinding rejection, keymap completeness, themes, paths, templates, and `NO_COLOR` |
| Output | Golden human and JSON schemas for all source and change variants, hidden files, non-UTF-8 paths, terminal escaping, and GitHub cap recovery |
| Domain | Mutation invariants, typed identities, comments, review marks, filtering, snapshots, stale revisions, reload reconciliation, and invalid transitions |
| Source | Git/GitHub fixture tests, hostile external data, process limits, cancellation, direct-child reaping, contained-group outcomes, the documented escaped-descendant non-ownership boundary, lazy materialization, and publication reconciliation |
| Runtime | Admission/claim/cancel/complete races, byte-credit release, readiness/receiver closure, deadlines, late results, retained-outcome settlement, delivery fencing, panic policy, and shutdown ordering |
| Terminal | Root/child model retention, command capture, key/help ownership, complete `tea.View` fields, every responsive boundary, viewport bounds, Unicode/path safety, monochrome semantics, signal ownership, startup-release fallback, and PTY restoration |
| Architecture | Go package dependency edges, effect ownership, pure-review imports, public-surface discipline, forbidden duplicate paths, supported-platform checks, dependency pins, and cohesive ownership |
| Scale | A combined 1,000-file by 10,000-line lazy-materialization fixture, counter-based tests for viewport-proportional work, and retained large-file, large-tree, many-comment, search, and syntax benchmarks |

Performance checks require fixed fixtures, recorded environment metadata, repeated samples, and
reported absolute results. A relative regression threshold is not meaningful without a named
baseline and therefore is not part of this specification.

`make check` MUST run formatting verification, `go test ./...`, `go test -race ./...`,
`go vet ./...`, `go build ./cmd/rvw`, and the architecture tests on the host. Linux and macOS
release evidence additionally requires the platform-specific process and PTY contracts on both
systems. Release validation MUST run `make check` and `git diff --check` after edits stop. A check
is reported as passed only when the command ran and exited successfully.
