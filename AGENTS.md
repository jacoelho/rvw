# AGENTS.md

The best architecture has the fewest entangled concepts, clear boundaries, complete functionality,
and no way to represent invalid states.

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
- Design for at least 1,000 changed files of at least 10,000 lines each, plus hundreds of comments,
  while keeping memory and CPU usage bounded through lazy materialization.
- Keep frame construction and rendering proportional to the viewport plus overscan. Bound retained
  data, queues, concurrent tasks, and per-tick work.
- Keep filesystem, network, subprocess, blocking waits, and unbounded CPU work off the terminal
  event loop.
- Validate external data at boundaries. Keep unavailable, stale, cancelled, limited, failed, and
  ambiguous outcomes explicit; reconcile an unknown GitHub write before retrying.
- Prefer cohesive Go packages and files. Line count is not a conformance limit and does not justify
  shallow splits.
- Do not use positional Boolean parameters. Use named domain values or an options value so every
  call site states what each choice means.
- For a closed set of independent flags, prefer an opaque validated bitset with named members and
  membership queries. Do not expose raw bitwise construction or permit unknown bits.

## Go tooling

- At the start of a Go code task, discover the `gopls` MCP tools if they are not already visible and
  use `go_workspace` before semantic exploration.
- Use `go_search` for symbol discovery, `go_file_context` for cross-file dependencies,
  `go_package_api` for package contracts, `go_symbol_references` for references,
  `go_rename_symbol` for renames, and `go_diagnostics` for focused diagnostics.
- Do not use `rg`, `grep`, or manual multi-file inspection to approximate Go symbol search,
  references, semantic renames, package APIs, or diagnostics while `gopls` is available and
  succeeds.
- Use `rg` for literal text, filenames, comments, documentation, generated content, non-Go files,
  or when `gopls` cannot answer the query. State the reason when falling back.
- Treat `gopls` diagnostics as focused validation, not as a replacement for the required project
  checks.

