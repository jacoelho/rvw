# Goal: a Go-native `rvw`

**Status:** Draft design record. The product contract is not accepted until `docs/rvw-specification.md`
exists in this repository and is explicitly approved.

## Charter v1

### Requirement lock

- **R1 — REQUIRED:** Rewrite `rvw` in pure, idiomatic Go. Do not link Rust, require CGO, invoke an
  old `rvw` binary, or mechanically transliterate the Rust types and modules.
- **R2 — REQUIRED:** Use Bubble Tea v2 (`charm.land/bubbletea/v2`) for the interactive program and
  the compatible v2 Bubbles and Lip Gloss modules where they own real editing, help, viewport, or
  presentation behavior.
- **R3 — REQUIRED:** Preserve the product surface mandated by `AGENTS.md`: local worktree, staged,
  two-dot Git range, and GitHub pull-request sources; interactive, `--stdout`, and `--json` outputs;
  Linux and macOS only.
- **R4 — REQUIRED:** Keep external work off `Update` and `View`; bound retained bytes, queues,
  concurrency, result construction, per-message work, and frame construction. Remain usable with
  1,000+ changed files, 10,000+ line files, and hundreds of comments.
- **R5 — REQUIRED:** Keep unavailable, stale, cancelled, limited, failed, and externally ambiguous
  results distinct. Fence late results by operation and source generation. Reconcile an unknown
  GitHub write before retrying.
- **R6 — REQUIRED:** One owner controls the terminal and process signals. Controlled shutdown
  requests work cancellation without waiting, lets `tea.Program.Run` finish its terminal-
  restoration attempt, then joins workers while the subprocess owner escalates owned process groups
  and reaps every directly owned child within a bound. It reports failure when descendant
  quiescence cannot be proven.
- **R7 — REQUIRED:** Validate untrusted configuration, Git/GitHub output, paths, patches, and
  terminal text at their ingress boundary. Preserve raw Unix path bytes separately from escaped
  display text.
- **R8 — REQUIRED:** Build the complete product through independently testable vertical slices.
  Delete superseded paths in the same slice; no compatibility facade, second event loop, generic
  effect system, component registry, or silent fallback may remain.
- **R9 — INFERRED:** `../rvw-rewrite/docs/rvw-specification.md` is the proposed behavioral baseline,
  not an accepted specification or implementation blueprint. Reconcile it into this repository
  before production implementation.
- **R10 — INFERRED:** Go 1.26.5 from `go.mod` is the language baseline. The exact Bubble Tea,
  Bubbles, and Lip Gloss versions are currently **UNKNOWN** because no dependencies are resolved.
  Bootstrap must select compatible stable v2 releases and commit the resolved module graph before
  version-specific APIs are used.
- **R11 — OPTIONAL/UNKNOWN:** The proposed product contract requires unknown-publication
  reconciliation during one process lifetime but does not define restart behavior. G0 must obtain
  an explicit user decision between process-lifetime-only fencing and a complete cross-process
  safety contract based on durable state or proven remote idempotency. G6 is blocked until then;
  the selected package boundaries work for either result.

### Non-goals

No new source type, output mode, general review persistence, plugin system, generic source registry,
web UI, Windows support, or byte-for-byte/internal compatibility with the Rust implementation. No
durable publication journal is assumed unless R11 is explicitly accepted with its storage,
locking, corruption, permissions, privacy, and cleanup contract. Matching a Rust module count,
public API, scheduling protocol, or rendering algorithm is explicitly not a success criterion.

### Definition of done

The accepted Go specification is traceable to tests; all four sources and three outputs work; the
TUI satisfies the ownership, boundedness, lifecycle, and interaction rules below; `go test`,
`go test -race`, `go vet`, the repository architecture checks, and process/PTY contracts pass on
Linux and macOS; no Rust or replacement scaffolding remains in the runtime path.

## Baseline dossier and refactor finding

Verified on 2026-07-20:

- `rvw-go` has no implementation: its project files are `AGENTS.md` and `go.mod`, alongside a
  pre-existing `.AGENTS.md.swp` editor artifact. It is not a Git worktree and is missing the
  `docs/rvw-specification.md` that `AGENTS.md` declares authoritative.
- `rvw-rewrite` contains about 101,248 lines across 265 Rust source files. Its four crates separate
  pure review state, source effects, terminal runtime, and composition.
- The Rust terminal owns a custom event loop, scheduler, task admission/completion/retirement
  protocols, rendering, and shutdown. `ReviewApp` retains the workspace, source, view, syntax,
  terminal, task set, retirement worker, and multiple failure/deferred-cleanup queues.
- The useful invariants are explicit domain identity, effect isolation, lazy materialization,
  bounded resources, stale-result fencing, publication reconciliation, and terminal-first cleanup.
  The Rust protocol and its exported type surface are mechanisms, not product requirements.

**Refactor necessity finding:** a clean Go architecture is required. Translating the four crates
and custom scheduler would preserve the implementation's change amplification and compete with
Bubble Tea's native model. A single package would be smaller initially but would entangle review
semantics, process ownership, rendering, and lifecycle. The correct boundary is a pure review core,
concrete effect packages, one Bubble Tea application model, and one bounded application-owned work
coordinator only for work that must be cancelled and joined.

## Architecture decision

### Alternatives considered

1. **Four-package transliteration:** superficially preserves ownership, but imports Rust-specific
   task tokens, retirement ledgers, and orchestration into Go. Rejected by R1, R2, and R8.
2. **One direct Bubble Tea package:** minimal indirection, but source effects and publication
   invariants would become model methods and make noninteractive reuse, race tests, and process
   cleanup difficult. Rejected by R4–R7.
3. **Selected — functional core, concrete edges, Bubble Tea shell:** preserve semantic boundaries
   while using native Go and Bubble Tea flow. Add packages only for a present ownership boundary.

Strongest objection: a coordinator plus a pure domain package can recreate the Rust protocol under
new names. The design prevents that by keeping Bubble Tea messages concrete, keeping ordinary
synchronous transitions direct, and adding operation identity/accounting only to accepted work
that can overlap, be superseded, outlive the program, or commit externally.

### Target ownership

```text
cmd/rvw
  -> internal/config
  -> internal/delivery
  -> internal/output
  -> internal/source
  -> internal/review
  -> internal/app

internal/app -> internal/review, internal/source, internal/delivery, Bubble Tea/Bubbles/Lip Gloss
internal/delivery -> internal/subprocess
internal/source -> internal/review, internal/subprocess
internal/output -> internal/review, internal/source
```

- `cmd/rvw` parses CLI input, composes dependencies and separate program/work contexts, opens the
  initial snapshot, selects interactive versus noninteractive output, owns `tea.Program` and the
  one process-signal registration, and maps final errors to exit status. It contains no review
  policy.
- `internal/config` alone discovers, reads, validates, and merges bounded user/project
  configuration and themes. `internal/output` streams the noninteractive human and JSON contracts
  to an `io.Writer`; it never constructs the TUI.
- `internal/review` owns validated IDs, source/workspace generations, files, selection, marks,
  comments, drafts, reload reconciliation, export/publication state, and pure transitions. It
  imports no terminal, filesystem, network, process, clock, or concurrency package. It exposes
  concrete domain operations and snapshots, not an `Effect` interface.
- `internal/source` is the sole Git review-data, `gh`, snapshot, patch/blob, reload, search-input,
  and publication boundary. It discovers the Git root once and owns source-specific repository
  inputs including `.gitignore`, `.rvwignore`, and configured ignore files; it does not read general
  application configuration or themes. Start with one cohesive package split by files; create a
  subpackage only when it removes a demonstrated dependency or independent invariant.
- `internal/delivery` owns interactive destination permits and stdout/clipboard commit states. It
  returns immutable stdout payloads to the post-Program root adapter and runs clipboard delivery
  through `internal/subprocess`; it never writes to Bubble Tea's active terminal.
- `internal/subprocess` is justified by three current consumers: Git, `gh`, and clipboard delivery.
  It solely owns each live child, its pipes, and its process-group ID from start through `Wait`.
  Structured arguments, bounded concurrent stream handling, deadlines, and direct-child lifecycle
  remain inside that execution. Each child starts in a fresh Linux/macOS process group;
  cancellation sends group `TERM`, escalates to `KILL` after a bounded grace period, and calls
  `Wait` for the direct child. It never invokes a shell or claims to reap a descendant that is not
  its child.
- `internal/app` owns the root Bubble Tea model, concrete child Bubbles, resolved key bindings,
  responsive layout, semantic Lip Gloss styles, command constructors, and a closed coordinator for
  cancellable/joinable work. Keep rendering beside the model until a cohesive renderer boundary is
  demonstrated.

### Interactive flow and invariants

```text
coordinator retains outcome -> readiness signal -> one-shot tea.Cmd
                                                    |
                                                    v
tea.Msg/outcomeReadyMsg -> root Update -> ClaimNext -> new observable state + tea.Cmd

root state -> pure, complete tea.View
```

- The root is one pointer-owned model with pointer-receiver `Update`, returned as the same owner.
  `Update` is the sole observable-state mutation boundary. The model pointer is never exposed to or
  captured by a command, worker, service, or child. Commands receive owned requests or immutable
  snapshots and return typed messages; mutable result payload ownership transfers once.
- Use one root model with concrete children only for meaningful local behavior such as textarea,
  text input, help, and a bounded viewport. Retain every returned child model and command.
- Route each key exactly once: blocking layer, focused child, active view, then reserved global
  action. The same enabled binding definitions drive routing and visible help.
- Model mutually exclusive modes explicitly. Keep stable file/comment/row identity separate from
  cursors and reconcile it after filter, reload, replacement, and resize.
- Represent terminal size as unknown until the first valid size message. Every `View` branch
  returns complete v2 terminal fields. `View` performs no I/O or state repair.
- Visual thesis: a dense review workspace with the diff as the primary task locus, quiet tree and
  comment context, one accent family, stable marker columns, and focus shown without geometry
  changes. Narrow tiers remove optional context before task controls; below minimum shows only the
  size requirement and effective quit/interrupt actions.
- Materialize, wrap, style, and join only viewport rows plus fixed overscan. Bound source data,
  search results, syntax data, status history, caches, queued inputs/results, and peak construction
  bytes. Limitation is always visible.

### Work and shutdown

Ordinary `tea.Cmd` values may produce immediate typed messages, wait for one coordinator readiness
signal without taking payload ownership, run short generation-fenced timers, or execute documented
child-Bubble commands. They do not own application filesystem, network, subprocess, unbounded CPU,
or other work that must be cancelled or joined. One application-owned coordinator owns admission
and joins for source-I/O, search/highlighting, export-delivery, and publication operations; live
subprocess ownership remains inside `internal/subprocess`.

The coordinator has concrete submit methods and typed outcomes, separate I/O and CPU limits, finite
request/result byte budgets, and a result-delivery slot reserved at acceptance. Submission is
nonblocking from `Update`. Observable operation records live in the model. The coordinator owns
operation contexts, admission permits, job goroutines, and joins; a job calling
`internal/subprocess` does not regain ownership until that call has waited for its child. The
coordinator has only cancellation/escalation requests, never raw pipes, child handles, or
process-group IDs. Latest-wins work coalesces before acceptance; required work cannot starve;
publication/reconciliation capacity remains available under saturation.

Replaceable progress keeps only the latest value per operation and emits at a fixed bounded cadence.
Terminal outcomes, cancellation, failure, and shutdown have independently reserved delivery
capacity and priority, so progress cannot delay control. Producer count and pending progress bytes
remain bounded.

The coordinator retains each completed outcome and mutable payload in its reserved slot until the
model claims it. `Update` arms one cancellable command that receives only a readiness notification
and re-arms it only while the coordinator is active. Readiness, coordinator closure, and receiver
cancellation are different typed messages. On readiness, `Update` uses a nonblocking owner-local
`ClaimNext` operation to transfer one outcome, settle the matching model record, and acknowledge
the delivery slot; it never receives payload ownership in the command goroutine. Duplicate and
stale outcomes cannot update the current review, but still release their owned resources and
preserve external commit evidence. Coordinator closure is terminal, occurs only after its retained
outcome registry is empty, and is never re-armed.

Every accepted operation resolves once as success, failure, cancelled, superseded, limited, or
unknown. Publication has a stable attempt ID and an explicit pre-commit/committing boundary;
cancellation after that boundary cannot manufacture a safe retry during the process lifetime.
Cross-process behavior follows the explicitly accepted R11 decision rather than an assumed journal.

Normal coordinator closure occurs only after admission stops and every accepted operation has one
terminal outcome in its reserved delivery slot or post-Program settlement handoff. Abnormal
coordinator termination is an application failure: it resolves only operations for which the owner
has evidence, preserves any possible publication commit as unknown, and releases resources only
after physical ownership ends. Unexpected worker panic is a program failure unless that worker has
an explicit, tested fault-isolation contract.

Quit stops admission and requests cancellation without blocking, then returns `tea.Quit`. After
`Program.Run` returns, the root adapter cancels the UI readiness receiver and waits on its bounded
completion barrier before touching unclaimed outcomes. It then invokes one bounded coordinator
shutdown/join API. That API settles or releases retained outcomes and preserves publication commit
or ambiguity facts under the accepted R11 policy; it does not claim to mutate the discarded UI
model. Coordinator jobs request cancellation or escalation, while the active subprocess execution
alone signals its group and calls `Wait`; joined job completion proves child ownership ended. Only
then may the root print diagnostics or exit. A non-quiescent group is an explicit non-graceful
result. Program and work cancellation scopes are distinct.

The application, not Bubble Tea, owns `os/signal`; bootstrap must select the exact versioned option
that disables competing framework signal handling. The first supported OS notification enters the
same idempotent shutdown policy and cancels both outside scopes. A repeated notification requests
documented process escalation. Raw-mode `Ctrl-C` remains a separately routed key whose action
depends on the active editing/modal context.

Noninteractive `--stdout` and `--json` stream directly before any Program exists. An interactive
stdout export prepares an immutable bounded payload, initiates controlled quit, and the root adapter
writes it only after `Program.Run` returns. Clipboard delivery is a captured noninteractive
coordinator subprocess whose bounded stdin pipe contains only the immutable export payload. Its
stdout and stderr are null or bounded/captured, and it never inherits the active terminal streams.

## Execution plan v1

- **G0 — Contract gate:** reconcile `docs/rvw-specification.md` from the Rust draft and this charter,
  remove Rust-specific architecture rules, define exact limits and schemas, record the draft's
  content digest, and obtain explicit user acceptance of that exact revision. G1 is blocked until
  acceptance is recorded. Pin compatible v2 modules, verify the versioned signal/lifecycle APIs,
  resolve R11 explicitly, and add dependency/effect-direction checks without treating file creation
  as acceptance.
- **G1 — Domain contract and noninteractive vertical slice:** first implement the minimum
  `internal/review` types for immutable source identity, typed file/patch IDs, validated files, and
  the snapshot contract. Then implement validated CLI/config, typed source target, bounded
  subprocess execution, local worktree acquisition, and streaming human/JSON output. Add
  staged/range/PR sources without a second snapshot or output representation.
- **G2 — Interactive review core:** implement selection, marks, comments, drafts, explicit
  operation states, lazy patch contracts, filtering, reload reconciliation, pure snapshots, and
  deterministic transition tests on the G1 domain types.
- **G3 — TUI foundation:** compose the one Program, root model, bindings/help, initial/resize/error/
  minimum-size views, file activation, viewport-bounded unified diff, cancellation, coordinator
  closure, receiver-barrier handoff, coordinator-owned unacknowledged-outcome settlement, bounded
  joins, subprocess escalation/reaping, and PTY restoration. This is the first end-to-end
  interactive slice.
- **G4 — Review interaction:** add tree navigation, side-by-side layout, comments/drafts, visual
  selection, wrapping, status details, export preparation, and bounded clipboard/stdout delivery.
- **G5 — Async depth:** add reload, context expansion, bounded all-file search, syntax preparation,
  progress/limitation states, stale-generation fencing, progress coalescing, coordinator
  saturation, and adversarial lifecycle tests.
- **G6 — GitHub publication:** after R11 is resolved, add preflight, immutable target binding,
  included/unmappable comment policy, ambiguous commit state, the accepted process/restart
  reconciliation policy, and deterministic commit/cancel race tests.
- **G7 — Conformance:** hostile input/path/Unicode fixtures, scale counters and benchmarks,
  Linux/macOS direct-child, contained-process-group, escaped-descendant failure, and PTY tests, race
  tests, architecture checks, and removal of every temporary or duplicate path.

Each step must leave one working path and add the narrowest tests for its contracts. A failed
boundary assumption amends this record before implementation continues; it is not hidden behind an
adapter.

## Verification contract

Required repository checks are `gofmt`, `go test ./...`, `go test -race ./...`, `go vet ./...`,
`go build ./cmd/rvw`, architecture/dependency tests, and `git diff --check` once this directory is a
Git worktree. Add process tests for all source/output pairs and deterministic matrices for:

- root/child model retention and command capture;
- modal, focused-child, active-view, and global key ownership, with help generated from the same
  effective bindings;
- loading, empty, unavailable, stale, cancelled, limited, failed, unknown, and successful states;
- complete `tea.View` terminal fields in initial unknown-size and every early-return branch;
- every responsive breakpoint one cell below, at, and above, including long paths, Unicode, focus,
  selection, and monochrome cues;
- coordinator acceptance, immediate completion, receive re-arm, closure, cancellation, saturation,
  stale/duplicate results, readiness without payload transfer, `ClaimNext`, closure with outstanding
  work, worker faults, quit with a blocked receiver, receiver/result/cancellation races, the
  post-Run receiver barrier, unacknowledged settlement, reservation release, and join independence;
- progress cadence/coalescing, pending producer bounds, and control/completion latency under
  progress saturation;
- publication commit/cancel races and the accepted R11 restart contract;
- viewport/byte/worker structural bounds and isolated Linux/macOS PTY, signal, direct-child, and
  process-group contracts.

Capture and inspect `tea.View.Content` for representative normal, minimum, narrow, and breakpoint
states. Material visual approval also requires real terminal or PTY render inspection for light,
dark, limited-color/`NO_COLOR`, Unicode, focus, selection, error, and limitation cases. Mark each
hierarchy, alignment, spacing, color, focus, state, and responsive axis unverified until inspected.

Exact commands and exit statuses must be recorded; unavailable evidence remains unverified.

## Predicted change surface

- `docs/rvw-specification.md` — accepted Go product contract (**high confidence**).
- `cmd/rvw`, `internal/{config,delivery,output,review,source,subprocess,app}` — initial ownership
  boundaries (**high confidence**); file splits inside them are deliberately unspecified.
- `go.mod`, `go.sum` — pinned v2 terminal dependencies and any justified Unix support module
  (**high confidence**).
- Repository checks, fixtures, benchmarks, and Linux/macOS PTY helpers (**high confidence**).

No migration adapter is required because `rvw-go` has no implementation. Behavioral parity is
established contract-by-contract, not by sharing code or running the Rust binary in production.

## Amendment ledger

### A1 — G0 dependency and lifecycle evidence

**Date:** 2026-07-20. **Affected step:** G0. **Approval:** pending with explicit acceptance of
`docs/rvw-specification.md`.

Resolution against the previously unknown R10 API surface:

- pin `charm.land/bubbletea/v2 v2.0.8`, `charm.land/bubbles/v2 v2.1.1`, and
  `charm.land/lipgloss/v2 v2.0.5`;
- use `tea.WithoutSignalHandler()` for application ownership of `SIGINT`/`SIGTERM`; Bubble Tea
  retains `SIGWINCH` because v2.0.8 exposes no option to replace its resize registration;
- treat `Program.Run` return as completion of the ordinary restoration attempt, not proof of
  physical restoration, and require PTY evidence;
- on a startup error not wrapping `tea.ErrProgramKilled`, invoke the Program's public
  `ReleaseTerminal` fallback before reporting the error;
- use `make check-g0` before G1 creates the real `cmd/rvw`; `make check` continues to require the
  real binary and MUST NOT be satisfied by a placeholder;
- permit the unavoidable, unreachable `github.com/charmbracelet/x/windows` entry in the pinned
  upstream module graph while continuing to forbid direct/runtime Windows code or dependencies.

The Windows dependency interpretation and proposed process-lifetime R11 policy are not accepted by
file creation or by this amendment. They require the same explicit acceptance as the exact
specification candidate.

R10 remains unchanged as the immutable baseline statement of what was unknown when Charter v1 was
written. This A1 entry supersedes that unknown for implementation and records the resolving
evidence without rewriting the original charter.
