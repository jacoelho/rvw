# rvw specification acceptance

The user explicitly amended the previously accepted `docs/rvw-specification.md` on 2026-07-20:

- 1,000 lines is not a Go source-file limit;
- the performance target is 1,000 files with 10,000 lines each;
- terminal text rendering covers tabs and other characters.

The user explicitly amended the resulting specification again on 2026-07-21 by removing comment
categories from the review model, configuration, terminal interface, exports, and GitHub
publication.

Acceptance includes the document's process-lifetime publication-safety contract in section 9.1:
there is no durable cross-process publication journal, and duplicate prevention is not promised
after process exit.

Acceptance also includes the dependency interpretation in section 6: Linux and macOS remain the
only supported platforms; project-owned and direct/runtime Windows code or dependencies remain
forbidden; the pinned Bubble Tea dependency graph's unavoidable, unreachable transitive Windows
module metadata is permitted.

This record does not alter or supplement the product contract.
