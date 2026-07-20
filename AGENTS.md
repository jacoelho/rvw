# AGENTS.md

The best architecture has the fewest entangled concepts, clear boundaries, complete functionality,
and no way to represent invalid states.

## Architecture specification

- `docs/rvw-specification.md` is the sole product and architecture specification.
- The exact document with SHA-256 digest
  `eb71f3a3574598598690cb9953064c462c31af0172381e87bd4cc221223a51e0` was explicitly accepted on
  2026-07-20. `docs/rvw-specification.acceptance.md` records that acceptance without modifying the
  accepted bytes.
- Do not silently resolve a conflict by relying on a deleted or historical planning artifact.

## Product

`rvw` is being built as a terminal application for reviewing a local worktree, staged changes, an
explicit Git range, or a GitHub pull request. Its required outputs are an interactive TUI,
`--stdout`, and `--json`.

Do not introduce new review sources or output modes unless explicitly requested. Avoid unrelated
feature expansion.

## Constraints

- Support Linux and macOS only. Do not add project-owned or direct/runtime Windows code,
  dependencies, tests, fallbacks, or CI, and do not add WebAssembly support. The accepted pinned
  dependency graph's unreachable transitive Windows module metadata is the sole exception.
- Design for 1,000+ changed files, 10,000+ line files, and hundreds of comments while keeping memory
  and CPU usage bounded.
- Keep frame construction and rendering proportional to the viewport plus overscan. Bound retained
  data, queues, concurrent tasks, and per-tick work.
- Keep filesystem, network, subprocess, blocking waits, and unbounded CPU work off the terminal
  event loop.
- Validate external data at boundaries. Keep unavailable, stale, cancelled, limited, failed, and
  ambiguous outcomes explicit; reconcile an unknown GitHub write before retrying.
- Cancellation and shutdown must terminate and reap child-process trees. Restore the terminal
  before waiting on background work during shutdown.
- Prefer cohesive Go packages. The repository gate rejects source files over 1,000 lines; do not
  use shallow splits to evade it.

## Specialized workflows

- Use `$evolve-architecture` for material changes to ownership, boundaries, dependencies, protocols,
  data flow, or execution paths.
- Use `$bubbletea-design` for material changes to terminal lifecycle, input, layout, widgets,
  rendering, or TUI performance.
- Delegate bounded, independent exploration and review when it materially improves quality. Prefer
  read-only delegation; do not let agents concurrently edit overlapping files.
- Delegate tests and checks when subagents are available so raw logs stay out of the main context.
  The primary agent selects and assesses the checks; the subagent returns each command, exit status,
  and concise failure summary.
- If a skill or subagent is unavailable, use the best available workflow and report the limitation.

## Validation

Run focused tests for affected behavior. After edits stop, delegate the final checks to a validation
subagent when available. Before the real `cmd/rvw` vertical slice exists, run:

```sh
make check-g0
```

After G1 creates `cmd/rvw`, before completing code or build changes, run:

```sh
make check
git diff --check
```

Documentation-only changes require `git diff --check`. Report each command and exit status plus any
material failure; never report an unrun check as passed.
