# Interactive runtime and lifecycle

## 10. Bubble Tea application and work

The interactive program has exactly one `tea.Program`. Its root model is one pointer-owned value
with pointer-receiver `Update`; `Update` returns the same owner. Every interactive input, accepted
completion, short timer expiry, and lifecycle message routes through that one method.

`Update` is the sole observable-state mutation boundary. The model pointer is never exposed to or
captured by a command, worker, service, child, or view. A command receives an owned request,
immutable snapshot, or immutable dependency and returns a concrete typed message. Mutable result
payload ownership transfers once and the producer never accesses it again.

`View` is pure and complete. It performs no state repair, filesystem, network, subprocess,
clipboard, blocking wait, or unbounded CPU work. Terminal dimensions remain unknown until the
first valid `tea.WindowSizeMsg`. Every initial, loading, empty, error, minimum-size, modal, and
normal branch returns one complete `tea.View` with consistent v2 terminal fields.

For Bubble Tea `v2.0.8`, the complete `tea.View` contract is `Content`, `OnMouse`, `Cursor`,
`BackgroundColor`, `ForegroundColor`, `WindowTitle`, `ProgressBar`, `AltScreen`, `ReportFocus`,
`DisableBracketedPasteMode`, `MouseMode`, and `KeyboardEnhancements`. Shared fields are applied
once after branch-specific content is composed.

In `v2.0.8`, `OnMouse` is limited to bounded hit translation and returns only an immediate typed
application message; real work starts after that message reaches `Update`. The same raw mouse
message also reaches `Update`, so either `OnMouse` or root raw-message routing owns an action, never
both.

Concrete Bubbles own only meaningful local interaction such as text input, textarea, help, or a
bounded viewport. The root retains every returned child model and every returned command. Each key
has exactly one owner in this order: blocking layer, focused child, active view, reserved global
action. The same enabled binding definitions drive routing and visible help.

Ordinary `tea.Cmd` values may:

- immediately return a typed message;
- receive one coordinator readiness signal through a cancellable one-shot wait;
- run a short generation-fenced timer;
- run a documented child-Bubble command.

They do not own application filesystem, network, subprocess, unbounded CPU work, or work that must
be joined. `tea.Batch` is used only for a fixed, data-independent command count.

One application-owned coordinator handles accepted source I/O, search/highlighting, export
delivery, and publication operations. It has concrete submit methods, distinct I/O and CPU limits,
and no generic effect, task, or component registry. Submission from `Update` is nonblocking.

An accepted operation reserves request bytes, result-construction bytes, one terminal-result slot,
and the required execution class before launch. It binds a typed operation ID to source and
workspace generations. Every accepted operation resolves exactly once as success, failure,
cancelled, superseded, limited, or unknown. Required work uses FIFO with at most eight bypasses.
Latest-wins work coalesces before acceptance. Publication and reconciliation keep one reserved
admission, execution, and result slot that ordinary saturation cannot consume.

Replaceable progress retains only the latest value per operation and becomes ready at the bounded
cadence in [Required bounds and failure behavior](conformance.md#14-required-bounds-and-failure-behavior).
Terminal outcomes, cancellation, failure, shutdown, and coordinator closure have independently
reserved priority delivery and cannot be delayed by progress.

The coordinator retains every completed outcome and mutable payload in its reserved slot until the
model claims it. Its readiness signal carries no payload. `Update` arms one cancellable readiness
command while the coordinator is active. On `outcomeReadyMsg`, `Update` invokes a nonblocking
owner-local `ClaimNext`, transfers exactly one outcome, settles the visible operation, acknowledges
the slot, and re-arms the wait if appropriate. Readiness, cancellation of the receiver, and
coordinator closure are distinct messages.

Duplicate or stale outcomes cannot mutate the current review, but their resources are released and
external commit/ambiguity evidence is preserved. Coordinator closure is terminal, occurs only
after admission has stopped, workers are joined, and the retained outcome registry is empty, and
is never re-armed. Unexpected worker panic is a program failure unless that exact worker boundary
has a tested fault-isolation contract.

Publication uses a stable attempt ID and a linearized `PreCommit -> Committing` boundary.
Cancellation produces `cancelled` only when the owner proves pre-commit quiescence. Once committing
begins, the outcome is confirmed success, confirmed pre-commit failure, or unknown; cancellation,
stale model state, and shutdown cannot manufacture a safe retry.

## 11. Signals, subprocesses, and shutdown

The application owns termination notifications. `cmd/rvw` creates the Program with
`tea.WithContext(programContext)` and `tea.WithoutSignalHandler()`, registers `os/signal` exactly
once for `SIGINT` and `SIGTERM`, and releases that registration on shutdown. Bubble Tea retains its
versioned `SIGWINCH` registration for terminal resizing; `rvw` does not create a competing resize
handler. Raw-mode `Ctrl-c` is a key and follows the active modal/editing context; it is not treated
as an operating-system notification.

The first supported notification enters the same idempotent shutdown policy as an internal quit.
A repeated notification requests immediate process-group escalation from the subprocess owner but
does not use `os.Exit` while the terminal is active. Uncatchable termination cannot promise
cleanup.

Program, work, and subprocess-attempt cancellation form a parent-child `context.Context` tree.
Cancelling a parent cancels its Go children. A Go context governs `rvw` work and the direct external
command associated with it; it is not inherited by an operating-system descendant and is not an OS
process-containment mechanism.

Every subprocess is constructed with `exec.CommandContext(attemptContext, name, args...)`, starts
without a shell, and starts in a new Linux/macOS process group. Before `Start`,
`internal/subprocess` replaces the default direct-child cancellation with an idempotent, nonblocking
`Cmd.Cancel` hook. Its first invocation sends `TERM` to the owned group and returns without waiting.
The hook returns `nil` when group `TERM` is delivered, an error matching `os.ErrProcessDone` when the
owned group no longer exists, and the original error for any other signaling failure. The attempt
owner preserves signaling failures other than `ESRCH` in lifecycle classification.
The attempt owner independently starts the process-group `TERM` grace in
[Required bounds and failure behavior](conformance.md#14-required-bounds-and-failure-behavior) and
sends `KILL` to that group if cleanup has not completed. A repeated application termination request
sends immediate group `KILL`.

`Cmd.WaitDelay` is five seconds. After `attemptContext` is done, it is a last-resort bound for a
direct child that fails to exit; after direct-child exit, it bounds `os/exec` pipe-copy goroutines
left open. It does not bound an otherwise running command before either event, and it is not the
group-escalation timer or evidence of group or descendant quiescence.

`internal/subprocess` owns the structured arguments and pipes, owns the direct-child handle through
exactly one `Cmd.Wait` call, and retains the initial process-group ID until group cleanup and
lifecycle classification complete. One waiter starts after every successful `Start`; shutdown
signals and joins that existing attempt owner and never calls `Wait` separately. Stdout and stderr
are consumed concurrently within their byte contracts. A contained group that does not quiesce
within its bound produces an explicit non-graceful result.

After `Cmd.Wait` returns, a still-live initial group triggers the same `TERM`, 750-millisecond grace,
and `KILL` sequence even when `attemptContext` was not cancelled. After group `KILL`, one shared
five-second interval bounds both the direct-child waiter, when still pending, and initial-group
quiescence. Expiry produces the explicit non-graceful result.

A descendant can deliberately call `setsid` and leave the owned group. `rvw` does not promise to
discover, signal, wait for, or reap such an escaped process on Linux or macOS. Parent-child Go
contexts, process groups, and inherited descriptors MUST NOT be described as recursive descendant
tracking. The runner MUST NOT retain the lifetime-descriptor protocol as evidence of descendant
quiescence. A successful subprocess result proves only that the direct child exited acceptably and
the owned process group quiesced; it does not assert ancestry-wide quiescence. An independently
observed escaped descendant MAY be reported diagnostically, but remains outside the lifecycle
result and enforceable ownership boundary.

Program and work cancellation scopes are distinct. Controlled shutdown is:

1. stop coordinator admission and request work cancellation through an idempotent, owner-local,
   nonblocking call;
2. return `tea.Quit` for internal shutdown, or cancel the Program context for an outside
   notification;
3. let `tea.Program.Run` return after Bubble Tea's terminal-restoration attempt;
4. cancel the readiness receiver and wait for its bounded completion barrier before touching
   unclaimed outcomes;
5. call the coordinator's one bounded shutdown/join operation, which settles retained outcomes,
   escalates owned subprocess groups through `internal/subprocess`, and reaps every directly owned
   child;
6. write an immutable, format-tagged pending stdout export, then diagnostics, only after the
   Program is no longer active. The output boundary preflights terminal escaping and size before
   writing; JSON escaping must preserve valid, semantically equivalent JSON.

No acknowledgement, grace wait, worker join, pipe drain, child `Wait`, or output write blocks
`Update`, `View`, or the active terminal. The application reports failure when a worker, direct
child, or contained group does not quiesce within its bound.

Bubble Tea `v2.0.8` discards the terminal-restoration error on its ordinary shutdown path.
`Program.Run` returning proves the attempt completed, not that the terminal was physically
restored. Actual restoration is claimed only by a passing PTY contract. The program returns errors
through the restored outer boundary and does not call `os.Exit` below it.

`v2.0.8` can return directly after terminal initialization when initial size discovery or input
reader initialization fails. On a `Run` error that is not `tea.ErrProgramKilled`, the root adapter
calls `Program.ReleaseTerminal` before reporting the error and includes any release failure in the
process result. PTY tests MUST cover that fallback. This use of the Program's public release API
does not create a second terminal owner.
