# AGENTS.md

## Product and authorities

`rvw` reviews exactly four sources: a local worktree, staged changes, an explicit two-dot Git
range, or a GitHub pull request. It has exactly three outputs: an interactive TUI, `--stdout`, and
`--json`.

Do not introduce new review sources or output modes unless explicitly requested. Avoid unrelated
feature expansion.

[The product specification](docs/rvw-specification.md) is the detailed product and runtime
reference. [Its acceptance record](docs/rvw-specification.acceptance.md) identifies the exact
accepted revision. Do not describe a later revision as accepted without a matching acceptance
record.

[The TUI design](docs/tui-design.md) is the sole normative presentation authority. Keep visual
hierarchy, focus, cursor, glyph, responsive, dialog, pointer, contrast, and evidence changes
consistent with it.

## Non-functional requirements

- Support Linux and macOS only. Do not add project-owned or direct/runtime Windows code,
  dependencies, tests, fallbacks, or CI, and do not add WebAssembly support. The accepted pinned
  dependency graph's unreachable transitive Windows module metadata is the sole exception.
- Design for at least 1,000 changed files of at least 10,000 lines each, plus hundreds of comments,
  while keeping memory, CPU, concurrent work, retained data, queues, and per-tick work bounded
  through lazy materialization.
- Keep frame construction and rendering proportional to the viewport plus overscan. Bound retained
  presentation data and caches.
- Keep filesystem, network, subprocess, blocking waits, and unbounded CPU work off the terminal
  event loop.
- Validate external data at boundaries. Keep unavailable, stale, cancelled, limited, failed, and
  ambiguous outcomes explicit; reconcile an unknown GitHub write before retrying.

Consult the specification's [non-functional conformance
sections](docs/rvw-specification.md#nonfunctional-conformance) for detailed resource limits,
security and integrity invariants, and verification routes.
