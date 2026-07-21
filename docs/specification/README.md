# rvw specification — split candidate

**Status:** Draft candidate.

This index and the five files listed below form the complete candidate specification. Every listed
file is required; no listed file overrides another. A conflict or duplicate normative rule between
files is a specification defect.

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

This indexed set is standalone. It does not incorporate a document outside the set, implementation
file, test, commit, plan, or historical decision.

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
