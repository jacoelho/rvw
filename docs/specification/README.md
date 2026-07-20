# rvw specification — split candidate

**Status:** Draft candidate for explicit exact-byte acceptance.

This index and the five files listed below form the complete candidate specification. Every listed
file is required; no listed file overrides another. A conflict or duplicate normative rule between
files is a specification defect.

[`manifest.sha256`](manifest.sha256) records the exact candidate file set in canonical path order
and excludes itself. Acceptance must cite the SHA-256 digest of those manifest bytes; accepting one
content file or this index alone does not accept the set.

The candidate preserves the accepted product contract except for the user-selected amendment
recorded in [Signals, subprocesses, and shutdown](runtime-lifecycle.md#11-signals-subprocesses-and-shutdown).

## Reading order

1. This index and [Interpretation](#1-interpretation).
2. [Product surface](product-surface.md), sections 2–5.
3. [Domain and source](domain-source.md), sections 6–9.1.
4. [Interactive runtime and lifecycle](runtime-lifecycle.md), sections 10–11.
5. [Terminal presentation](terminal-presentation.md), sections 12–13.
6. [Nonfunctional conformance](conformance.md), sections 14–16.

## Scope

The specification covers product behavior, architecture, source handling, runtime lifecycle,
terminal presentation, resource bounds, failure semantics, and verification for `rvw`.

It does not incorporate an implementation file, test, commit, plan, or historical decision unless
an indexed file says so explicitly.

## Reconciliation provenance

The accepted Go specification was originally reconciled from the Rust draft at
`../rvw-rewrite/docs/rvw-specification.md`, whose SHA-256 digest was
`ae2dea93fa1e3b2562250c1df91637ee4554ad2b20c1c20a203d77222c1bfb80`. That digest identifies input
evidence only; the Rust draft is neither a contract nor an implementation blueprint.

This candidate mechanically decomposes the accepted Go specification along its existing top-level
boundaries, then applies only the external-process amendment explicitly requested on 2026-07-20.

## 1. Interpretation

`MUST`, `MUST NOT`, `SHOULD`, and `MAY` identify normative requirements.

The following rules resolve ambiguity:

1. Correctness, data integrity, explicit failure, bounded work, and safe ownership outrank
   compatibility or convenience.
2. Required observable behavior is stated in this indexed set. Missing requirements cannot be
   inferred from an implementation or test.
3. Unavailable, stale, cancelled, limited, failed, and ambiguous outcomes are distinct states.
4. A verification route is not evidence that the corresponding check has passed.
5. Internal organization MAY change without changing this contract, provided ownership, dependency,
   boundedness, lifecycle, and observable behavior remain conformant.
