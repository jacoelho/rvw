# AGENTS.md

## Product and authorities

`rvw` is the human checkpoint in an agent loop. An agent runs one blocking command; a human reviews
the change in a local browser UI and finishes the review; `rvw` prints the review on stdout and
exits.

It reviews exactly three sources: the last commit (`rvw last`), the commits on the current branch
since a base ref (`rvw branch [<base-ref>]`), and uncommitted changes (`rvw uncommitted`), the last
two combinable as `rvw branch [<base-ref>] --uncommitted`. It has exactly one output: the review result
on stdout, as Markdown or, with `--json`, as JSON.

Do not introduce new review sources or output modes, GitHub access, or an MCP server unless
explicitly requested. Avoid unrelated feature expansion.

[The contract](docs/contract.md) is the product and runtime authority. Its header names the accepted
revision; do not describe a later revision as accepted unless the repository owner explicitly accepts
it. [The decision record](docs/decisions.md) holds the rationale and rejected alternatives.
[The architecture](docs/architecture.md) is the architecture entry point; update it when ownership,
boundaries, lifecycle, bounds or trade-offs change.

## Non-functional requirements

- Support macOS only. Do not add Linux, Windows or WebAssembly code, build tags, tests or CI.
- Obtain every Git fact from the `git` executable through the single runner in `internal/source`.
  Do not parse Git objects or reimplement Git behaviour.
- Open no network connection other than the loopback review listener.
- Leave the repository's index, worktree files, configuration and refs unchanged. The one exception, if contract
  revision 3 is accepted, is the retention refs under `refs/rvw/` (INV-PLAT-004).
- Design for at least 1,000 changed files of at least 10,000 lines each, plus hundreds of comments.
  The server reads a file's patch and contents only when the UI asks for it and retains nothing
  per file; the UI renders only visible rows plus bounded overscan.
- Validate external data at boundaries: seed documents, HTTP requests, and `git` output.
- The web UI is TypeScript without a runtime framework under `web/`. `web/dist` is committed and
  embedded; rebuild it with `make web` after changing `web/src`. A union type is a named type
  alias, never written inline, except `T | null` or `T | undefined` and `Pick`/`Omit` key lists;
  `make lint` enforces it (`web/scripts/check-unions.mjs`).

## Checks

`make check` runs formatting, lint, the `web/dist` freshness check, tests, race tests, vet and the
build; it must pass before a change is done. Browser tests in `cmd/rvw` drive Chrome and skip without
it, except under `CI=true`.
