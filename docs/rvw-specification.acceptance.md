# rvw specification acceptance

The user explicitly accepted `docs/rvw-specification.md` on 2026-07-20 with SHA-256 digest:

```text
eb71f3a3574598598690cb9953064c462c31af0172381e87bd4cc221223a51e0
```

Acceptance includes the document's process-lifetime publication-safety contract in section 9.1:
there is no durable cross-process publication journal, and duplicate prevention is not promised
after process exit.

Acceptance also includes the dependency interpretation in section 6: Linux and macOS remain the
only supported platforms; project-owned and direct/runtime Windows code or dependencies remain
forbidden; the pinned Bubble Tea dependency graph's unavoidable, unreachable transitive Windows
module metadata is permitted.

This record does not alter or supplement the product contract. The accepted specification is the
exact byte sequence identified by the digest above.
