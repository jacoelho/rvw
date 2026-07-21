# Review improvement plan

## Charter v1

### Requirement Lock

- `R1 REQUIRED`: review the complete current codebase with independent maintainability, Go
  correctness, Go performance, Bubble Tea, and architecture-evolution passes.
- `R2 REQUIRED`: turn accepted findings into one repository-specific implementation plan.
- `R3 REQUIRED`: freeze production writes while independent reviewers challenge the plan; revise
  the plan until no actionable plan finding remains.
- `R4 REQUIRED`: implement the accepted plan with one writer, preserving unrelated work and the
  accepted product contract.
- `R5 REQUIRED`: rerun the independent review facets after implementation, repair accepted
  findings, and repeat until no actionable finding remains.
- `R6 REQUIRED`: keep Linux and macOS as the only supported platforms and retain exactly the four
  review sources and three top-level output modes.
- `R7 REQUIRED`: preserve lazy materialization, viewport-plus-overscan rendering, bounded retained
  data and work, terminal-event-loop isolation, explicit failure states, publication ambiguity
  fencing, and one Bubble Tea program/root update owner.
- `R8 REQUIRED`: run focused tests during implementation and finish with `make check`,
  `git diff --check`, focused `gopls` diagnostics, and the affected fixed-fixture benchmarks.
- `R9 REQUIRED`: do not claim Linux-specific process or PTY behavior from the darwin host.

### Preserved behavior and authority

- `P1 REQUIRED`: `docs/rvw-specification.md`, whose digest is pinned by
  `internal/architecture/architecture_test.go`, is the accepted product contract.
- `P2 REQUIRED`: `docs/specification/` remains an explicitly unaccepted draft candidate. It is not
  authority to change subprocess cancellation, escaped-descendant ownership, or publication
  behavior. Accepting or rejecting that proposal is a product-contract decision outside this plan.
- `P3 REQUIRED`: no dependency upgrade, new review source, new output mode, persistence layer,
  generic effect/source/component registry, second terminal loop, Windows path, or WebAssembly path.
- `P4 REQUIRED`: current public JSON fields, CLI grammar, configuration schema, and process-lifetime
  publication-safety behavior remain unchanged.
- `P5 INFERRED`: internal unexported APIs may be reshaped or removed when all current consumers and
  tests migrate atomically.

### Initial evidence snapshot

- Branch: `bootstrap`.
- HEAD: `5caafcfe0a00e32d7484113babea569ec19e184f`.
- Merge base with `main`: `11bf9931184455b511ee22d8f8862b2fe15c7b52`.
- Initial worktree: clean.
- Branch delta: 106 files, 30,972 insertions, 472 deletions.
- Go module: `github.com/jacoelho/rvw`, Go `1.26.5`.
- Bubble Tea `v2.0.8`, Bubbles `v2.1.1`, Lip Gloss `v2.0.5`.
- Baseline `make check`: exit 0.
- Baseline `gopls` workspace diagnostics: no diagnostics.
- Baseline coverage: total 55.9%; `internal/app` 46.0%. Publication, search, delivery,
  prompt, and several routing transitions include uncovered production paths.
- Baseline benchmark environment: darwin/arm64, Apple M2 Max.

### Definition of done

All plan steps and deletion obligations have current-tree evidence; focused and project-wide checks
pass; accepted benchmark changes are measured against fixed fixtures; independent reviewers report
no actionable correctness, maintainability, performance, Bubble Tea, or plan-conformance finding;
and no material code change lacks a requirement/decision/plan-step mapping.

## Baseline Dossier v1

### Current ownership and canonical paths

- `cmd/rvw` parses/composes, owns signals and the one `tea.Program`, then performs post-program
  settlement and output.
- `internal/config` discovers, parses, merges, and validates external configuration.
- `internal/review` owns pure validated review state and transitions.
- `internal/source` owns Git/GitHub acquisition, lazy materialization, reload, and publication I/O.
- `internal/delivery` owns destination permits and commit-state fencing.
- `internal/subprocess` owns structured process execution, process groups, capture, wait, and reap.
- `internal/app` owns the Bubble Tea model, rendering, key routing, bounded coordinator, and typed
  coordinator outcomes.

### Accepted findings

| ID | Severity | Finding | Evidence summary |
| --- | --- | --- | --- |
| F1 | High | Resize can leave focus on a hidden pane. | `model.go:289-297` does not call existing `clampFocus`; layout hides panes and routing follows stale focus. |
| F2 | Medium | Confirmation bypasses the below-`20x6` input owner. | View, update, and key-context priority put confirmation before minimum-size state. |
| F3 | High | Keybinding vocabulary and compatibility policy have two owners. | `config` and `app` duplicate context/focus/action types and matrices; `interactiveSettings` reparses validated strings. |
| F4 | High | Foreground/reload arbitration is a distributed state machine. | Independent reload booleans/IDs and competing deferred pointers are read across model, reload, search, syntax, and action code. |
| F5 | Medium | Coordinator `Outcome` permits mismatched discriminator/payload states. | Enum, ten payload fields, ten accessors, ID switch, and consumer probing duplicate one closed variant set. |
| F6 | Medium | Immutable snapshots are cloned merely for traversal. | `Snapshot.Files` clones at review construction and all-file search despite `AllFiles`/`File`. |
| F7 | Low | Immutable comment kinds are cloned on every review transition. | `Review.clone` clones constructor-owned, never-mutated kind metadata. |
| F8 | Medium | Ordinary frames rebuild effective bindings four times. | `actionSummary` calls allocating `effectiveBindings` once per action. |
| F9 | Medium | Nil/empty syntax still constructs iterator machinery per visible line. | `PreparedSyntax.Spans` defers nil discovery until `iter.Pull`. |
| F10 | Medium | Wrapping and final pane fitting repeat expensive width/style work. | Short fitting lines still call `lipgloss.Wrap`/split; pane rows are fitted at producer and composition layers. |
| F11 | Medium | Benchmarks omit maximum effective geometry and navigation-plus-frame latency. | Existing scale render uses `180x30`; navigation benchmark excludes the following frame. |
| F12 | High | High-risk interactive protocols lack direct transition coverage. | Coverage evidence shows app publication/search/delivery paths at 0% and accepted verification requires race, ambiguity, stale-result, and routing evidence. |

### Rejected or narrowed review claims

- `N1`: there are not two accepted subprocess contracts. The monolithic digest is the sole enforced
  contract; the split set labels itself a draft candidate. No subprocess redesign is authorized.
- `N2`: `NavigationDirection` cannot be deleted; `Review.AdjacentHunkLine` uses it. Only the
  test-only `MoveFileSelection` and its private `VisibleFiles` helper are deletion candidates.
- `N3`: `view.go` being 1,017 lines is not itself a defect under the repository contract. No
  shallow line-count split is planned.
- `N4`: the broader final-width refactor in F10 is not accepted without exact-cell tests and
  benchmark evidence. A measured no-change result is valid.

### Refactor Necessity Finding

Local patches alone fix F1, F2, F8, and F9, but preserve two canonical keymap policies, invalid
foreground combinations, and invalid coordinator outcome states. A bounded preparatory refactor is
therefore necessary for F3-F5. A larger coordinator/model redesign is not necessary: the existing
package owners, concrete submit methods, receiver protocol, and feature files remain coherent.

## Candidate comparison

### Candidate A — calibration: local correctness and allocation patches only

- Keeps current model fields, duplicate keymaps, and product-style `Outcome`.
- Lowest migration risk but fails F3-F5 and makes the next workflow/key action/outcome repeat the
  same change amplification.
- Rejected: it does not end in a single source of truth or make invalid states unrepresentable.

### Candidate B — coherent bounded refactor plus focused repairs

- Introduces one small keymap leaf shared by configuration and app composition.
- Replaces reload booleans and competing deferred foreground pointers with closed tagged state.
- Replaces coordinator discriminator-plus-parallel-payload storage with package-sealed variants.
- Applies narrow TUI, traversal, immutable-metadata, and measured render fixes.
- Adds missing protocol tests without introducing another runtime layer.
- Selected: it removes more concepts and duplication than it adds while keeping current package
  ownership and observable behavior.

### Candidate C — move all interactive workflow state into a new coordinator/domain owner

- Could centralize more state but would combine observable model transitions with worker ownership,
  add migration surface across most app files, and risk a second event/state abstraction beside
  Bubble Tea.
- Rejected: it violates conceptual economy and has no second present need beyond the bounded state
  ownership fixes in Candidate B.

### Strongest objection to Candidate B

The shared keymap leaf and tagged workflow states add named types and migrate many call sites. The
change is justified only if the old duplicate maps/constructors/fields are deleted in the same
sequence. Leaving adapters or parallel fields would make Candidate B worse than Candidate A and is
therefore a plan-gate failure.

## Accepted Design v1

### Decisions

- `D1`: create one dependency-free `internal/keymap` leaf that owns runtime key contexts, focuses,
  actions, chords, compatibility validation, required-action policy, and resolved bindings.
  `internal/config` maps TOML data into these validated values; `internal/app` consumes them
  directly. No string revalidation bridge remains in `cmd/rvw`.
- `D2`: keep feature methods in `reload.go`, `search.go`, and `syntax.go`, but put reload stage and
  foreground deferral invariants in closed model-owned state values. One deferred foreground intent
  may exist at a time. Syntax deferral remains distinct because it is replaceable presentation work.
- `D3`: represent coordinator terminal/progress results as a package-sealed interface implemented
  by concrete outcome values. One type switch applies results. There is no discriminator or
  parallel payload storage.
- `D4`: make minimum-size state the highest input/view priority. A pending confirmation survives
  below-minimum resize but receives no confirmation actions until geometry recovers; configured
  quit/interrupt remain the only accepted inputs.
- `D5`: clamp focus after every accepted resize, preserving `focusReturn` so a pane can regain focus
  only when visible again.
- `D6`: traverse snapshots through `AllFiles`/`File`; delete `Snapshot.Files`,
  `Review.MoveFileSelection`, and `Review.VisibleFiles` after all consumers migrate. Retain
  `NavigationDirection` for hunk navigation.
- `D7`: constructor ownership protects comment kinds; later immutable review clones share that
  slice. Tests must prove caller aliases cannot mutate it.
- `D8`: perform only evidence-backed frame optimizations: reuse one effective-binding result,
  bypass syntax iterators for nil/empty spans, render the static separator once, and benchmark-gate
  fitting-line and width-owner changes. No cache, pool, unsafe code, or custom width engine.
- `D9`: add deterministic tests for high-risk publication, search, delivery, foreground arbitration,
  responsive focus, and minimum-size ownership. Coverage percentage is not the target; explicit
  invariants and failure races are.
- `D10`: keep the accepted subprocess implementation unchanged except for deleting an argument-
  ignoring `requestTermGrace` wrapper if its callers can use `DefaultTermGrace` directly without
  changing behavior.

### Target architecture delta

| Component | Change | Role | Confidence |
| --- | --- | --- | --- |
| `internal/keymap` | add | single closed interaction vocabulary and policy owner | High |
| `internal/config` keybinding types/validation | migrate/delete duplicates | external parsing into shared validated domain values | High |
| `internal/app/settings.go` | migrate/delete duplicates | consume shared keymap values | High |
| `cmd/rvw/interactive.go` | remove conversion loop | compose already-validated bindings | High |
| `internal/app` reload/deferred fields | replace | explicit valid foreground state | High |
| `internal/app/coordinator.go` outcome wrapper | replace/delete | sealed concrete result variants | High |
| `internal/review/snapshot.go` | remove cloning accessor | immutable non-copying traversal | High |
| `internal/app` transition tests | add | prove accepted-operation and routing invariants | High |
| `internal/app` benchmarks/render helpers | amend | measure and reduce viewport-frame allocation | High |
| `internal/subprocess/runner.go` | optional direct constant simplification | delete identity wrapper only | Medium |

## Execution Plan v1

### E0 — baseline and guardrail capture

- Serves: R6-R9, P1-P4.
- Record current focused tests, `make check`, diagnostics, benchmark fixtures/results, and dirty-state
  boundary. Do not modify accepted specs or dependency pins.
- Completion evidence: command transcripts and unchanged contract digest.
- Rollback point: no production edits.

### E1 — write regression and invariant tests before structural changes

- Serves: F1-F5, F12, D2-D5, D9.
- Add table-driven tests for resize focus transfer/restoration; confirmation across minimum-size
  resize; reload/search/patch deferral ordering and cancellation; stale/duplicate coordinator
  outcomes; publication check/commit/unknown/reconciliation; delivery fencing; search cancellation
  and stale generations; and nil/empty syntax paths.
- Prefer direct `Update`, command, coordinator, and pure-domain tests with controlled contexts and
  barriers. No sleeps as ordering oracles.
- Completion evidence: each new test fails for the intended pre-change reason or documents an
  already-correct invariant, then passes only after its owning step.
- Rollback point: tests only.

### E2 — fix responsive focus and minimum-size ownership

- Serves: F1, F2, D4, D5.
- Clamp focus after storing size and resizing children. Put minimum-size view/routing/key context
  before every confirmation branch while retaining the pending confirmation for restoration.
- Remove the confirmation exception in minimum-size routing.
- Verification: responsive boundary matrix, hidden-pane immutability, pending-confirmation restore,
  focused app tests, race tests, diagnostics.
- Rollback point: E1 tests plus isolated TUI diff.

### E3 — establish one keymap contract and contract old copies

- Serves: F3, D1.
- Add the shared leaf, migrate config parsing/validation and app settings/routing/help, update the
  dependency-direction guardrail, then delete duplicate app/config vocabularies, compatibility
  matrices, constructors, and the `cmd/rvw` string conversion loop.
- No compatibility facade, type alias bridge, or duplicate map may remain.
- Verification: config validation matrix, app routing/help matrix, CLI composition tests,
  architecture tests, `gopls` references proving old symbols are gone.
- Rollback point: complete atomic keymap migration.

### E4 — make foreground and reload state explicit

- Serves: F4, D2.
- Introduce closed reload stage and deferred foreground intent values with validating transitions
  and action-availability queries. Migrate reload/search/patch arbitration mechanically, retain
  feature-specific files, then delete the old loose fields and cross-feature Boolean checks.
- Do not absorb syntax presentation work or coordinator execution ownership into this state.
- Verification: E1 transition matrix, stale/cancel/supersede paths, focused race tests, `gopls`
  references showing removed fields have no callers.
- Rollback point: one complete state migration; no dual-write phase may survive a checkpoint.

### E5 — seal coordinator outcome variants

- Serves: F5, D3.
- Give each concrete outcome an unexported operation-ID method, use the sealed interface in retained
  queues and claim results, apply with one type switch, and delete `OutcomeKind`, parallel payload
  fields, variant accessors, and duplicated ID extraction.
- Preserve terminal-before-progress priority, byte/accounting release, readiness, and exactly-once
  claim behavior.
- Verification: coordinator claim/admission tests, unknown internal variant test, race tests, stats
  and byte-credit assertions.
- Rollback point: atomic queue/result migration.

### E6 — remove immutable traversal and transition allocations

- Serves: F6, F7, D6, D7.
- Migrate all production and test traversal to `AllFiles`/`File`, remove test-only file-navigation
  operations, delete `Snapshot.Files`, and stop cloning immutable comment kinds after constructor
  validation/copy.
- Add 100,000-file construction/search allocation benchmarks and constructor-alias tests.
- Verification: caller references, order/stale/cancellation tests, benchmark before/after.
- Rollback point: snapshot API migration complete.

### E7 — reduce measured frame work without changing rendering semantics

- Serves: F8-F11, D8.
- Reuse a single effective-binding slice per `actionSummary`; make nil/empty syntax spans bypass
  `iter.Pull`; render the static pane separator outside the row loop.
- Add ordinary and maximum-geometry, navigate-plus-render, wrap on/off, side-by-side/unified, and
  stage construction benchmarks.
- Benchmark the short-line wrapping fast path with fitting ASCII, styled Unicode, wide grapheme,
  ANSI, and long-line fixtures. Keep it only if fixed-fixture results improve without making a
  material case worse.
- Investigate single ownership of final pane fitting using exact cell-width tests. Implement it only
  if the owner can be stated uniformly and benchmarks justify the change; otherwise record a
  measured no-change disposition.
- Verification: terminal text/grapheme/golden tests, structural viewport assertions, `-count=5`
  `benchmem` comparisons at `180x30` and `1000x300`.
- Rollback point: each isolated optimization and its benchmark record.

### E8 — delete incidental wrapper and close implementation validation

- Serves: D10, R8.
- Replace `requestTermGrace(request)` with the accepted constant at its callers and delete the
  wrapper only if focused subprocess tests prove identical behavior. Do not otherwise change
  cancellation or escaped-descendant semantics.
- Run `gofmt` on changed Go files, focused `gopls` diagnostics, focused tests/race tests, affected
  benchmarks, `make check`, and `git diff --check`.
- Record actual architecture delta and every planned deletion.
- Rollback point: wrapper deletion is independently reversible.

### E9 — independent release review and repair loop

- Serves: R1-R5 and Definition of done.
- Freeze writes. Give independent reviewers Charter v1, Accepted Design v1, this plan, amendments,
  current diff, and validation evidence.
- Require plan-to-code, code-to-plan, correctness, maintainability, performance, Bubble Tea, and
  behavior reconstruction. Each finding must cite evidence, risk, and the smallest coherent fix.
- Route baseline defects to exploration, target defects to design, plan defects to amendment, and
  implementation defects to the single writer. After repair, rerun affected checks and the full
  independent review. Stop only at zero actionable findings.

## Deletion ledger

- Duplicate key context/focus/action/chord types and compatibility matrices in `config` and `app`.
- `cmd/rvw` keybinding string revalidation loop.
- Loose reload stage/deferred foreground fields superseded by closed state.
- `OutcomeKind`, parallel `Outcome` payload fields, variant accessors, and duplicated ID switch.
- `Snapshot.Files`, `Review.MoveFileSelection`, and `Review.VisibleFiles`.
- Per-transition immutable kind clone.
- `requestTermGrace` if E8 verification passes.

## Amendment Ledger

- `A-001` (plan audit, accepted): D1/E3 proposed a dependency graph forbidden by the accepted
  specification. Replace the shared keymap package with a cross-boundary conformance guardrail;
  preserve `config` external validation, `app` runtime/effective-binding ownership, and `cmd/rvw`
  composition. Affects F3, D1, E3, deletion ledger. Result: Design/Plan v2.
- `A-002` (plan audit, accepted): D2/E4 omitted existing replacement and continuation semantics.
  Replace the single global deferred intent with closed reload, patch-continuation, and
  search-continuation states plus the transition table below. Affects F4, D2, E1, E4. Result:
  Design/Plan v2.
- `A-003` (plan audit, accepted): D3/E5 left nested publication and reload discriminator products.
  Make every stage a direct sealed outcome variant and delete both nested stage/product types.
  Affects F5, D3, E5, deletion ledger. Result: Design/Plan v2.
- `A-004` (plan audit, accepted): E1/E5 did not name decisive accepted-operation races. Add the
  requirement-to-test matrix below. Affects F12, D9, E1, E5, E9. Result: Plan v2.
- `A-005` (plan audit, accepted): D4/E2 did not define visible dirty-confirmation behavior below
  minimum size. Require visible resize-to-confirm state and idempotent quit/interrupt handling.
  Affects F2, D4, E2. Result: Design/Plan v2.
- `A-006` (plan audit, accepted): the predicted change surface lacked evidence and symbol-level
  precision. Replace it with the evidence-backed v2 table below. Affects plan traceability only.
- `A-007` (plan audit, accepted): E6 invented an impractical 100,000-file search seam and E7 used
  subjective benchmark gates. Replace them with pure traversal/construction evidence and fixed
  sample/metric/regression rules. Affects F6, F10, F11, E6, E7. Result: Plan v2.
- `A-008` (plan audit wave 2, accepted): D2 v2 could not represent the preserved post-reload search
  across reload stages and omitted reachable concurrent patch/search products. Replace the three
  loose values with one opaque `foregroundState` aggregate and the completion rules below. Affects
  F4, D2, E1, E4. Result: Design/Plan v3.
- `A-009` (plan audit wave 2, accepted): D1 v2 placed executable boundary coverage in a package that
  cannot call the unexported owners. Split conformance tests along existing package seams without
  exporting production internals. Affects D1, E3, predicted surface. Result: Plan v3.
- `A-010` (plan audit wave 2, accepted): D3/E5 omitted shutdown consumers of reload/publication
  outcomes. Add them to the atomic migration and direct-variant tests. Affects F5, D3, E5,
  predicted surface. Result: Design/Plan v3.
- `A-011` (plan audit wave 3, accepted): D1 v3 asked `cmd/rvw` tests to inspect unexported app
  settings. Extract and test an unexported conversion helper returning exported `app.Keybinding`
  values. Affects D1, E3, predicted surface. Result: Plan v4.
- `A-012` (plan audit wave 3, accepted): D2 v3 did not arbitrate patch completion against an active
  search continuation. Forbid an unreachable patch `begin-search`/active-search product and make
  search continuations consumable only by search completion. Affects D2, E1, E4. Result:
  Design/Plan v4.
- `A-013` (plan audit wave 3, accepted): D2/E4 omitted coordinator admission rejection between pure
  state decisions and effects. Add prepare/admit/reject tokens and rejection transitions for every
  coordinator-backed foreground start. Affects D2, E1, E4. Result: Design/Plan v4.
- `A-014` (plan audit wave 4, accepted): D2 v4 incorrectly made clear sticky against a newer
  replacement search. Restore current latest-wins behavior between clear and replacement while
  keeping patch context sticky. Affects D2, E1. Result: Design/Plan v5.
- `A-015` (plan audit wave 4, accepted): generation-bound single-use admission tokens add an
  unreachable protocol to a synchronous non-interleavable path. Replace them with plain typed start
  decisions and direct admitted/rejected successors. Affects D2, E1, E4. Result: Design/Plan v5.
- `A-016` (plan audit wave 5, accepted): the v4 predicted surface still named the superseded token
  API. Replace that row with the v5 decision/successor symbols below. Affects traceability only.

Material deviations must append an ID, reason, affected steps, evidence, approval status,
architecture effect, and resulting plan version; Charter v1 and Execution Plan v1 remain unchanged.

## Accepted Design v2

Design v2 inherits Design v1 except for the replacements below.

### D1 v2 — preserve the accepted keymap ownership boundary

`internal/config` continues to own parsing and external keybinding validation. `internal/app`
continues to own runtime actions, effective bindings, routing, and help. `cmd/rvw` remains the
explicit composition boundary between the two representations. Do not add `internal/keymap`, a
reverse dependency, aliases, or a generic binding layer.

Address F3 with an architecture conformance test derived from the digest-pinned accepted action and
context vocabulary. It must exercise every accepted context/focus/action combination through
configuration validation and `interactiveSettings`, then assert the resulting app binding routes
and appears in effective help. A specification amendment must change the digest and this test
together. The current conversion is retained because it is the required explicit boundary; remove
only redundant revalidation proven unnecessary without weakening either owner's invariant.

### D2 v2 — explicit state with preserved continuation policy

Use three closed model-owned values rather than one global intent:

- `reloadState`: `idle`, `waiting-for-patch`, `acquiring(operation)`, or
  `preparing(operation, attempts)`;
- `patchContinuation`: `none`, `retry-active(file)`, or `begin-search(query, scope)`;
- `searchContinuation`: `none`, `clear`, `replace-search(query, scope)`, or
  `apply-patch-context(context)`.

The only intentional coexistence is `reloadState=waiting-for-patch` with
`patchContinuation=begin-search`; it preserves the existing search-then-reload sequence. No other
combination may encode two deferred actions. Syntax remains an independent replaceable presentation
request.

| Active state | Incoming request | Cancellation and stored continuation | Observable result and terminal continuation |
| --- | --- | --- | --- |
| idle | reload | none; start reload only when coordinator has no accepted work | existing `reloading source` or existing busy/capacity warning |
| idle | search | none; start search | existing searching notice |
| idle | patch context | apply context and request active patch | existing context notice |
| idle | active-file patch | start patch | existing patch state |
| patch active | active-file patch | cancel active patch; latest file replaces `retry-active` | after last patch outcome, retry latest file |
| patch active | patch context | update context; cancel active patch; latest active file becomes `retry-active` | after last patch outcome, load latest context |
| patch active | search, no reload wait | cancel active patch; `begin-search` replaces `retry-active` | after last patch outcome, start latest search |
| patch active | reload | cancel active patch; enter `waiting-for-patch`; clear `retry-active`, preserve a pre-existing `begin-search` | after last patch outcome start reload; after reload commits and its active patch settles, start the preserved search |
| patch active with reload wait | search | no new cancellation or continuation | reject with existing `search is unavailable during reload` warning |
| search active | replacement search, no patch-context continuation | cancel active search; latest `replace-search` wins | after terminal search outcome, start replacement |
| search active | replacement search, patch-context already pending | cancellation remains requested; do not replace sticky patch context | patch context wins and replacement search is discarded, matching current priority |
| search active | patch context | cancel active search; `apply-patch-context` replaces replacement/clear | after terminal search outcome, clear search, apply context, request patch |
| search active | cancel/clear | clear both replacement variants; set `clear` | after terminal search outcome, clear visible search |
| search active | reload | no deferred reload | existing busy warning; search continues/cancels only through its own controls |
| search active | active-file patch | no search continuation change; patch may run concurrently under existing coordinator bounds | both settle independently; search generation fences stale patch data |
| reload waiting/acquiring/preparing | reload | none | existing `reload is already active` information |
| reload waiting/acquiring/preparing | search | none | existing unavailable-during-reload warning |
| reload waiting/acquiring/preparing | other gated foreground action | none | preserve existing action gating and notice |

Characterization tests must establish every row before field migration. Any implementation that
changes a row requires a new user-approved behavior amendment rather than a plan-only amendment.

### D3 v2 — direct sealed outcome variants at every stage

The sealed coordinator outcome set contains direct `PatchOutcome`, `SearchOutcome`, `SyntaxOutcome`,
`PublicationCheckOutcome`, `PublicationCommitOutcome`, `ReconciliationOutcome`,
`ReloadAcquiredOutcome`, `ReloadPreparedOutcome`, `FileTreeOutcome`, `WorkspaceIndexOutcome`,
`ExportOutcome`, `DeliveryOutcome`, and `Progress` variants. Every variant exposes only the
unexported identity/accounting behavior required by the coordinator.

Delete `OutcomeKind`, the outer product payloads/accessors, `PublicationStage`,
`PublicationOutcome`, `ReloadStage`, `ReloadOutcome`, and both nested stage switches. Producers
construct exactly one valid concrete variant; `Model.applyOutcome` has one concrete type switch.

### D4 v2 — minimum-size pending confirmation behavior

Minimum-size state is the highest input and view owner. If dirty quit or another confirmation is
already pending when the terminal becomes too small, retain it without accepting yes/no/cancel or
destructive actions. The minimum view must visibly state that a decision is pending and that the
terminal must be resized to confirm or cancel it. Repeated configured quit and interrupt inputs are
accepted but idempotently retain the pending decision; they do not silently discard state, close the
confirmation, or start another operation. After a valid resize, the original confirmation and its
focus resume. Complete `tea.View` terminal fields remain unchanged across every branch.

## Plan v2 test obligation matrix

| Invariant/race | Existing evidence | Required v2 evidence |
| --- | --- | --- |
| registration precedes immediate completion | `TestCoordinatorAdmissionRetainsOutcomeUntilClaim` | retain and extend to every direct sealed result family |
| accepted result retained until claim and credit released once | coordinator admission/stats tests cover the ordinary case | explicit success, cancelled, stale, duplicate, and shutdown release assertions for request/result bytes and accepted count |
| terminal result cannot be delayed by progress | `TestCoordinatorCoalescesProgressBehindTerminalPriority` | retain after sealed-variant migration and cover progress saturation plus quit/cancel |
| receiver cancellation and closure are distinct and not re-armed | `TestReadyReceiverStopIsABoundedCompletionBarrier` | explicit coordinator-closed, receiver-cancelled, duplicate-readiness, and no-rearm transitions |
| cancellation before publication commit is safely cancelled | domain publication transition tests are partial | controlled barrier before `PreCommit -> Committing`; prove quiescence and retry eligibility |
| cancellation at/after commit boundary cannot become safe retry | no direct app/runtime evidence | controlled committing barrier; produce committed or unknown only, preserve marker/target through stale model and shutdown |
| stale, late, or duplicate publication result preserves ambiguity | no direct app/runtime evidence | stale generation, duplicate terminal delivery, late physical completion, and exactly-once semantic settlement tests |
| reconciliation progresses under ordinary saturation | reserved-capacity admission test only | saturate ordinary admission/work/result capacity, then prove reconciliation is accepted, executes, and releases its reservation |
| delivery commit ambiguity remains fenced through cleanup | delivery manager tests cover destination state locally | app/coordinator outcome and shutdown test proving unknown destination cannot be reused |
| search replacement/context/cancel policy is deterministic | no direct transition matrix | every D2 v2 search-continuation row with barriers, stale result, limited result, and clear-after-cancel |
| shutdown settlement preserves unclaimed external facts | partial `runInteractive` and publication formatting tests | coordinator retained result plus model settlement merge for committed, failed-before-commit, and unknown outcomes |
| focus and blocking owner follow current geometry | responsive geometry and editor minimum tests | every width/height boundary with tree/comments focus and every confirmation kind, visible pending-decision text, non-owner keys rejected |

## Predicted change surface v2

| File or symbol | Change | Architectural role | Confidence | Evidence |
| --- | --- | --- | --- | --- |
| `internal/architecture/architecture_test.go` keymap conformance test | add | enforce accepted cross-boundary vocabulary without changing dependency graph | High | accepted spec graph and duplicated config/app values |
| `cmd/rvw.interactiveSettings` | retain/narrow | explicit config-to-app composition boundary | High | sole production conversion caller |
| `internal/app.Model.reloadOperation`, `reloadDeferred`, `reloadPreparing`, `reloadPreparationTries` | replace | closed reload lifecycle | High | gopls found 14 `reloadPreparing` and 6 `reloadDeferred` references across model/reload/search/syntax/tests |
| `internal/app.Model.deferredPatch`, `pendingSearch` | replace | closed patch continuation | High | gopls found 7 and 11 references across model/search |
| `internal/app.Model.pendingPatchContext`, `clearSearchAfterCancel` | replace | closed search continuation | High | `setPatchContext`, `cancelSearch`, and `finishSearchDeferrals` own the transitions |
| `internal/app.Outcome`, `OutcomeKind`, accessors, `operationID` | replace/delete | direct sealed coordinator results | High | coordinator definition plus 34 gopls outcome references |
| `internal/app.PublicationOutcome`, `PublicationStage` | split/delete | valid publication stage variants | High | current producer/consumer stage switches in publication runtime/model |
| `internal/app.ReloadOutcome`, `ReloadStage` | split/delete | valid reload stage variants | High | current producer/consumer stage switches in reload worker/model |
| `internal/app.Model.Update`, `keyContext`, `View`, `minimumView`, `clampFocus` | amend | geometry-correct focus and blocking-layer ownership | High | Bubble Tea review traced missing resize clamp and confirmation priority |
| `internal/review.Snapshot.Files` and three production callers | remove/migrate | non-copying immutable traversal | High | gopls found production calls in review construction/visible files/search |
| `internal/review.Review.MoveFileSelection`, `VisibleFiles` | delete | remove test-only file navigation surface | High | gopls found no production callers |
| `internal/review.Review.clone` kinds copy | remove | share constructor-owned immutable metadata | High | no mutating production API after construction |
| `internal/app.actionSummary`, `effectiveBindings` | amend | one binding derivation per frame | High | performance profile and direct call loop |
| `internal/app.Model.syntaxSpans`, `renderSyntaxContent` | amend | bypass empty syntax iterator | High | nil syntax profile attributes `iter.Pull` allocations |
| `internal/app.Model.wrappedLine`, pane fit ownership | benchmark-gated | reduce repeated terminal-width work | Medium | mechanism/profile verified; exact safe owner remains inferred |
| `internal/app/scale_test.go`, focused benchmark files | add/amend | ordinary/max geometry and stage attribution | High | current benchmark fixed at `180x30` and separates navigation from frame |
| `internal/subprocess.requestTermGrace` callers | simplify/delete | remove argument-ignoring wrapper only | Medium | direct implementation returns `DefaultTermGrace` |

## Execution Plan v2 replacements

All unmentioned Plan v1 steps remain effective.

### E1 v2 — characterize policy and add protocol guardrails

Replace E1 with the tests in the Plan v2 test obligation matrix. D2 v2 characterization tests run
before state migration and must pass against the current implementation. New defect regressions for
F1/F2 may fail before E2. Protocol tests may expose independent implementation defects; route those
back to planning instead of weakening their oracle. Use controlled barriers/contexts/channels, not
sleeps.

### E3 v2 — enforce, do not merge, the keymap boundary

Replace E3 with D1 v2. Add the cross-boundary conformance test while preserving the accepted
dependency graph and ownership. Do not add a production package or change allowed imports. Remove
only revalidation or conversion code proven redundant by both owners' invariants and the exhaustive
test; a justified no-production-change result is valid.

### E4 v2 — migrate explicit states under the locked transition table

Replace E4 with D2 v2. Introduce the three closed state values, migrate the table row by row, and
delete the superseded fields after characterization tests pass. No dual-write state survives a
checkpoint. After migration, rerun the same table plus stale/cancel/supersede/race tests.

### E5 v2 — seal outer and nested outcome variants

Replace E5 with D3 v2. Migrate queues, producers, model handlers, accounting, and tests atomically;
delete every outer and nested discriminator product. Complete the applicable Plan v2 protocol
matrix rows before passing the step.

### E6 v2 — prove traversal allocation removal without a benchmark-only service seam

Keep the API/deletion work from E6 v1. Replace the 100,000-file search benchmark with:

- a 100,000-file `NewReview` construction benchmark;
- a pure 100,000-file snapshot traversal benchmark comparing iteration/allocation only; and
- the existing fixed large-patch search benchmark for matching cost.

Do not introduce a source-service interface or subprocess fixture solely for measurement. Existing
all-file search behavior, order, cancellation, and progress tests remain required.

### E7 v2 — fixed optional-optimization gates

Keep the unconditional effective-binding, nil/empty syntax, static-separator, and benchmark additions
from E7 v1. For each optional wrap or pane-fit experiment:

1. use the same darwin/arm64 host, Go version, `GOMAXPROCS`, fixture, and binary revision apart from
   the isolated experiment;
2. record ten uncached samples for the targeted microbenchmark, ordinary `180x30` render, and
   maximum `1000x300` render with `ns/op`, `B/op`, and `allocs/op`;
3. include fitting ASCII, styled Unicode, ANSI, wide-grapheme, and long-overflow cases for wrap;
4. accept only if the targeted median improves by at least 5% `ns/op` or 10% `B/op`/`allocs/op`,
   neither end-to-end median regresses by more than 3% `ns/op`, and neither end-to-end allocation
   metric increases;
5. otherwise remove the experiment completely and append a measured no-change disposition with the
   samples. Do not combine two optional experiments in one comparison.

Timing thresholds are local decision gates, not portable performance claims. Structural viewport
and exact-cell correctness tests remain vetoes regardless of timing.

## Deletion ledger v2

The v1 keymap deletion entries are superseded: retain both accepted boundary representations and
their explicit composition. All other v1 entries remain, plus:

- `PublicationStage`, `PublicationOutcome`, and their stage switch;
- `ReloadStage`, `ReloadOutcome`, and their stage switch.

## Accepted Design v3

Design v3 inherits Design v2 except for D1, D2, and D3 refinements below.

### D1 v3 — conformance tests stay inside current owners

The accepted keymap ownership decision in D1 v2 is unchanged. Implement its guardrail at the
existing seams:

- `internal/architecture` continues to pin the accepted specification digest and dependency graph;
  it does not call application internals.
- `internal/config` exhaustively tests accepted/rejected external context, focus, chord, action,
  required-action, ambiguity, and prefix combinations using config-owned parsing/validation.
- `internal/app` exhaustively tests its runtime context/action matrix and proves the same effective
  binding values drive routing and visible help.
- `cmd/rvw`, whose tests share `package main`, uses a test fixture transcribed from the digest-pinned
  accepted vocabulary to pass every valid binding through config loading and `interactiveSettings`,
  and asserts the resulting app binding fields. The fixture is test evidence, not a third production
  policy owner; a specification digest amendment must update it.

Do not export production internals, invoke nested `go test`, inspect Go source text, or add an
integration facade solely for this guardrail.

### D2 v3 — one opaque foreground transition owner

`Model` holds one opaque `foregroundState`. Only its transition/query methods may read or mutate
the contained flows; feature files request transitions and perform the returned concrete effects.
The aggregate contains:

- a reload flow: `idle`, `waiting-for-patch(afterReload?)`, `acquiring(operation, afterReload?)`,
  or `preparing(operation, attempts, afterReload?)`;
- a patch flow: bounded active operation-to-file ownership plus `none`, `retry-active(file)`, or
  `begin-search(query, scope)` continuation;
- a search flow: optional active operation plus `none`, `clear`, `replace-search(query, scope)`, or
  `apply-patch-context(context)` continuation.

The aggregate's `valid` check permits concurrent patch and search activity because current behavior
does. It forbids mismatched reload operation/stage, more than one continuation per flow, a reload
after-search continuation outside a non-idle reload, and a patch `begin-search` continuation while
the same request is owned by reload. Construction and transition methods enforce these invariants;
callers never assign constituent fields.

Request transitions preserve the D2 v2 table with these clarifications:

- When reload is requested during patch work, move an existing patch `begin-search` continuation
  into `reload.afterReload`, clear patch retry state, cancel patch work, and enter
  `waiting-for-patch`.
- A new search while reload is non-idle is rejected and cannot replace `reload.afterReload`.
- Patch and search may be active concurrently. A new patch/retry updates only patch flow. A search
  replacement/clear/context request updates only search flow; applying a context may subsequently
  request/cancel patch work through the ordinary patch transition.

Completion transitions are deterministic for the product state, including either completion order:

| Completion | Required state transition and effect |
| --- | --- |
| non-final patch result | remove only that operation; run no continuation |
| final patch result with reload waiting | transfer patch `begin-search` to `reload.afterReload` if not already transferred, clear patch continuation, start reload |
| final patch result without reload | start patch `begin-search`; otherwise perform `retry-active`; otherwise idle |
| search result while patch is active | apply search continuation exactly as in D2 v2; a context-triggered patch request may cancel/replace patch flow through its transition API |
| patch result while search is active | settle patch flow independently; its continuation may start another search only if search flow has no active operation, otherwise it becomes the existing replacement-search request |
| reload acquired | move `acquiring` to `preparing`, retaining operation and `afterReload` |
| reload prepared successfully with an active file | commit replacement; move `afterReload` to patch `begin-search`; request the post-reload active patch; start search after that final patch result |
| reload prepared successfully with no active file or no admitted patch operation | commit replacement and start `afterReload` immediately |
| reload acquisition/preparation failure or cancellation | retain the old valid review and start `afterReload` immediately; report reload failure/cancellation and then the search admission result |
| stale reload result for a non-current operation | release only its document/resources; do not consume or replay the current reload continuation |
| shutdown | stop new transitions; preserve accepted-operation/external facts and discard only unaccepted continuations after reporting their cancellation in settlement evidence |

Resuming `afterReload` on a terminal reload failure corrects the current orphaned `pendingSearch`
state: the old review remains valid, so the already-visible deferred intent is either admitted and
settled or fails visibly rather than replaying after unrelated future work.

Characterization tests must cover concurrent patch/search with each continuation and both terminal
orders, plus every reload success/failure/cancel/stale row. Tests for the orphan correction must
first prove the old pending state remains stuck, then lock the v3 terminal behavior.

### D3 v3 — shutdown is a first-class outcome consumer

D3 v2's direct variants remain selected. `Coordinator.Shutdown`, `shutdownSettlementLocked`, and
all document/publication cleanup helpers are part of the atomic caller migration. Shutdown handles
each direct variant explicitly:

- acquired/prepared reload variants with a non-nil document transfer it to exactly one close path;
- publication check variants create no external settlement fact;
- publication commit and reconciliation variants preserve committed, failed-before-commit,
  cancelled-before-commit, or unknown facts and immutable target/marker evidence;
- unrelated variants release accounting without manufacturing external facts.

No direct variant may be dropped merely because the model did not claim it before shutdown.

## Predicted change surface v3 corrections

The v2 row placing keymap conformance in `internal/architecture` is superseded by:

| File or symbol | Change | Architectural role | Confidence | Evidence |
| --- | --- | --- | --- | --- |
| `internal/config` keybinding validation tests | extend | exhaustive external-schema owner evidence | High | config owns parsing/validation and tests share package internals |
| `internal/app` routing/help tests | extend | runtime action and visible-help parity | High | existing `TestEffectiveBindingsDriveRoutingAndVisibleHelp` calls unexported owner directly |
| `cmd/rvw` `interactiveSettings` tests | extend | exhaustive config-to-app boundary conversion | High | `package main` tests can call the unexported composition function |
| `internal/app.foregroundState` | add | sole opaque foreground transition owner | High | current fields/callers mapped in D2 v2 and gopls references |
| `internal/app.Coordinator.Shutdown`, `shutdownSettlementLocked` | migrate | close reload documents and preserve publication facts for direct variants | High | `shutdown.go` consumes current reload/publication accessors and stages |

## Execution Plan v3 replacements

All v2 steps remain effective except as replaced below.

### E1 v3 — complete foreground characterization

Add D2 v3 product-state tests to E1 v2: concurrent patch/search with retry, replacement, clear, and
patch-context continuations; both patch-first and search-first outcomes; reload with retained search
through acquisition, preparation, commit, no-active-file success, failure, cancellation, stale
result, and shutdown. Record the current orphan behavior as pre-refactor evidence and the v3 resume
behavior as an explicit correction.

### E3 v3 — split boundary guardrails by package

Replace E3 v2's single-test wording with D1 v3. Keep every test beside the unexported owner it
exercises. Production code and accepted dependency edges remain unchanged unless a local redundant
check can be deleted with proof that both boundary invariants remain enforced.

### E4 v3 — migrate through one opaque aggregate

Replace E4 v2's three separately stored values with D2 v3's `foregroundState`. Add the aggregate
and characterization tests, migrate every gopls-identified field read/write to transition/query
methods, then delete the loose fields. Feature methods execute concrete effects returned by state
transitions; the state owner does not call the coordinator, source, or Bubble Tea. Run `valid` after
every transition in tests and prove no direct constituent mutation remains.

### E5 v3 — include shutdown migration and cleanup proof

Add D3 v3 consumers to E5 v2. Direct-variant tests must prove both reload variants close a non-nil
document exactly once, check-only publication produces no shutdown fact, commit/reconciliation
preserve every terminal/unknown fact, and unclaimed outcomes release accounting exactly once.

## Deletion ledger v3

The v2 ledger remains effective. In addition, delete every direct read/write of the old foreground
constituents outside `foregroundState` transition/query methods. No temporary dual-state adapter may
survive E4.

## Accepted Design v4

Design v4 inherits Design v3 except for these refinements.

### D1 v4 — observable conversion seam without new production API

Extract the existing `cmd/rvw.interactiveSettings` keybinding loop into an unexported
`interactiveKeybindings([]config.Keybinding) ([]app.Keybinding, error)` helper. `interactiveSettings`
uses that helper unchanged in behavior. `cmd/rvw` package-main tests call the helper and inspect the
already-exported fields of each `app.Keybinding`; a separate `interactiveSettings` test proves the
converted slice is accepted by `app.NewSettings` through ordinary composition. No app accessor,
reflection, source inspection, test facade, or package edge is added.

### D2 v4 — continuation arbitration and admission protocol

D2 v3's opaque aggregate remains selected, with these additional invariants:

- patch `begin-search` is valid only when search has no active operation and no continuation;
- while search is active, every incoming search request is represented only by
  `searchContinuation`; patch flow cannot also own it;
- `apply-patch-context` and `clear` are sticky and cannot be overwritten by an older or newer
  replacement search; otherwise the latest `replace-search` wins;
- patch completion never consumes or mutates `searchContinuation`; only the matching search
  terminal transition may consume it;
- when patch completes while search is active, patch retry may proceed independently, but any new
  search request remains owned by search flow until that search settles;
- when search completes while patch is active, its continuation runs by the same rules; a context
  continuation may cancel/retry patch flow through a new ordinary patch request.

Characterization covers patch-first and search-first completion for `none`, `retry-active`,
`replace-search`, `clear`, and `apply-patch-context`. The old patch-first loss of a replacement
search is recorded as a defect; v4 makes completion order irrelevant to which search continuation
owns the request.

Every coordinator-backed foreground start uses a two-phase pure-state contract:

1. `foregroundState.prepareX` validates current state and returns an immutable, generation-bound
   start token plus the concrete request intent without committing active ownership.
2. The feature method prepares the review-domain transition and calls the concrete coordinator
   submit method synchronously inside `Update`.
3. On successful submission, `foregroundState.admitX(token, operation)` commits active ownership,
   consumes the matching continuation, and the model commits the prepared review state.
4. On domain or coordinator rejection, `foregroundState.rejectX(token)` returns the previous valid
   state plus the deterministic continuation disposition; the model resolves any begun review
   operation and emits the existing bounded rejection notice.

Tokens are unexported values, single-use, and rejected when their generation or expected source
state no longer matches. No `starting` state is externally representable and no message can
interleave between prepare and admit/reject because the submit call is synchronous within one
`Update` transition.

Rejection dispositions are:

| Start | Direct rejection | Deferred rejection |
| --- | --- | --- |
| patch | remain in prior flow; resolve prepared operation and show failure/limited notice | clear matching retry; if it was the post-reload patch, start `reload.afterReload` immediately against the committed review |
| search | remain without active search; apply existing failed-admission domain result and notice | consume only the matching search continuation and report its rejection; do not disturb sticky context/clear owned by another token |
| reload | remain idle; resolve prepared operation and show failure/limited notice | leave reload wait, preserve the old valid review, and start `reload.afterReload` immediately; report reload rejection followed by search admission/result notice |

Coordinator closure, saturation, operation-ID exhaustion, domain transition rejection, and request
validation failure all route through the applicable rejection rule. Tests cover direct and deferred
starts plus post-reload patch rejection.

## Predicted change surface v4 corrections

| File or symbol | Change | Architectural role | Confidence | Evidence |
| --- | --- | --- | --- | --- |
| `cmd/rvw.interactiveKeybindings` | extract/add unexported | executable config-to-app conversion seam | High | current loop is wholly inside `interactiveSettings` and returns exported `app.Keybinding` fields |
| `internal/app.foregroundState.prepare/admit/reject` methods | add | atomic pure-state/coordinator admission boundary | High | all current starts submit synchronously before committing active model fields |

## Execution Plan v4 replacements

### E1 v4 — characterize arbitration and admission rejection

Add to E1 v3:

- both patch/search completion orders for every D2 v4 continuation, proving patch completion cannot
  steal search continuation;
- direct and deferred patch/search/reload domain rejection, coordinator saturation, and coordinator
  closure;
- operation-ID exhaustion and post-reload patch admission rejection;
- token mismatch/reuse rejection and no phantom active operation/accounting.

### E3 v4 — extract and test the package-local conversion helper

Apply D1 v4 while retaining all D1 v3 owner-local tests. The extraction must delete the inlined
conversion loop from `interactiveSettings`; no forwarding duplicate remains.

### E4 v4 — migrate with prepare/admit/reject only

Apply D2 v4. No feature method may set active foreground state before its concrete coordinator
submission succeeds. Every submit call has adjacent admit/reject handling, and every continuation
is consumed only by the token prepared from that exact state generation. After migration, gopls
references must show no loose-state writes and tests must prove completion order and admission
failure leave one valid aggregate state.

## Accepted Design v5

Design v5 inherits Design v4 with these final D2 corrections.

### D2 v5 — preserve search ordering and keep admission direct

Patch-context continuation remains sticky until the matching search settles. Clear and replacement
search are mutually replacing user intents: clear removes an older replacement, and a later
replacement removes clear. This matches current `cancelSearch`/`startSearch` ordering. Patch
completion still cannot consume any search continuation.

Replace generation-bound/single-use tokens with an ordinary immutable `foregroundStart` decision.
`foregroundState.prepareX` validates the unchanged aggregate and returns the concrete request intent,
the expected start kind, and rejection disposition. It does not mutate state and has no lifecycle,
identity, generation, reuse rule, or retained registration.

Inside the same non-interleavable `Update` transition, the feature method prepares domain state and
submits synchronously. It then calls either:

- `foregroundState.admitted(decision, operation)`, which applies the direct admitted successor and
  consumes the matching continuation; or
- `foregroundState.rejected(decision)`, which applies the direct rejected successor/disposition.

These methods validate only that the current aggregate still permits the decision kind; that check
is a local invariant assertion, not a new admission protocol. No pending-start state is stored.
Production never reuses a decision, and tests need not manufacture token misuse. Direct/deferred
rejection, post-reload patch rejection, and no-phantom-operation behavior remain required.

## Execution Plan v5 replacements

### E1 v5 — final ordering and rejection matrix

Replace token mismatch/reuse tests from E1 v4 with both input orders for clear versus replacement
search. Retain every direct/deferred domain/coordinator rejection and completion-order test.

### E4 v5 — direct synchronous successor selection

Implement D2 v5. State stays unchanged while domain preparation and coordinator submission run;
the feature method selects the admitted or rejected successor immediately afterward. Do not add a
generation counter, single-use token registry, pending-start state, rollback adapter, or token-only
error path.

## Predicted change surface v5 correction

The v4 `foregroundState.prepare/admit/reject` row is superseded by:

| File or symbol | Change | Architectural role | Confidence | Evidence |
| --- | --- | --- | --- | --- |
| `internal/app.foregroundStart` | add closed short-lived value | concrete synchronous start decision with no stored lifecycle | High | current start paths prepare and submit without interleaving |
| `internal/app.foregroundState.prepareX`, `admitted`, `rejected` | add | select direct valid successor after synchronous submit result | High | D2 v5 removes token generation/reuse and keeps state unchanged during submission |

## Implementation Evidence

Append evidence only after the plan-review gate passes. Do not rewrite prior evidence to match the
implementation.

### Implementation checkpoint 1

- E2 completed: resize clamps focus, minimum-size state owns view/input/key context, pending
  confirmations remain visible but disabled, and every confirmation kind plus focus restoration has
  direct tests.
- E3 v4 completed without changing the accepted package graph: `interactiveKeybindings` is the
  package-local composition seam; config, app, and command tests exhaust the accepted vocabulary,
  focus/context compatibility, routing, and visible help.
- E4 v5 completed: `Model` has one `foregroundState`; all loose reload/patch/search fields were
  deleted. Pure transition tests cover both patch/search completion orders for none, retry,
  replacement, clear, and sticky patch-context continuations; reload success/failure/stale paths;
  direct/deferred rejection; operation-ID exhaustion; coordinator closure/saturation; and
  post-reload patch rejection. Production callers use only transition/query methods.
- E5 v3 completed: all coordinator terminal results are direct sealed variants. Direct-family
  tests prove retained accounting and exactly-once claim/release for patch, search, syntax,
  publication check/commit/reconciliation, both reload stages, file tree, workspace index, export,
  and delivery. Shutdown tests prove both reload variants remove their source workspaces and that
  publication facts are preserved exactly once, while check-only outcomes create no fact.
- E6 completed: traversal uses `AllFiles`/`File`; `Snapshot.Files`, `Review.MoveFileSelection`, and
  `Review.VisibleFiles` are deleted; immutable comment kinds are constructor-owned and shared by
  later transitions. On the fixed 100,000-file fixture, snapshot traversal retained 0 B/op and 0
  allocs/op across five samples; `NewReview` retained 128 B/op and 4 allocs/op.
- E7 unconditional work completed: effective bindings are derived once per summary, nil/empty
  syntax bypasses iterator construction, and the pane separator is rendered once per frame.
  Ordinary, maximum-geometry, navigation-plus-render, mode-matrix, construction-stage, wrap-case,
  100,000-file traversal, and large-search benchmarks now exist.
- E8 implementation completed: both callers use `DefaultTermGrace` directly and the
  argument-ignoring wrapper is deleted. The checkpoint `make check` after the structural migration
  passed all tests, race tests, vet, and build; a final run remains required after release-review
  repairs.

### Protocol defects exposed while implementing E1/F12

- `I-001`: a patch worker error resolved the generic operation but left the domain patch in
  `PatchPending`. `applyPatchOutcome` now validates/applies a bounded failure result so the patch and
  operation settle together; malformed terminal patch results fail closed the same way.
- `I-002`: a mismatched delivery destination deleted the matching active delivery before validation.
  Destination validation now precedes removal; success, failed-before-write, unknown, mismatch, and
  duplicate outcomes have direct tests.
- `I-003`: an invalid publication check/commit/reconciliation result cleared model ownership while
  leaving the domain publication in checking/publishing/reconciling. Check failures now settle as
  failed; invalid commit results become unknown and trigger reconciliation; invalid reconciliation
  remains unknown. Tests prove target/marker evidence and operation fencing survive.
- `I-004`: reload failure or deferred reload rejection could leave the old review's active patch
  cancelled or unavailable. Failure now requests the old review's active patch before resuming the
  retained search; both admissions remain explicit and may run concurrently under coordinator
  bounds.
- These are correctness repairs within F4/F12, D2/D3/D9, and the existing product contract. They do
  not add a source, output mode, package boundary, dependency, or new external behavior surface.

### Optional wrap experiment disposition

- Environment: darwin/arm64 Apple M2 Max, Go 1.26.5, `GOMAXPROCS=8`, identical fixed fixtures,
  `-benchtime=200ms -count=10 -benchmem`.
- Accepted implementation: a newline-free value whose byte length is no greater than the target
  width performs one cell-width check and bypasses `lipgloss.Wrap` when it already fits. The byte
  guard prevents double-scanning long overflow lines. Exact tests cover fitting ASCII, styled
  Unicode, ANSI, wide graphemes, embedded newlines, and long overflow.
- Fitting-ASCII median changed from 893.85 ns/op, about 597 B/op, 41 allocs/op to 318.9 ns/op,
  16 B/op, 1 alloc/op. The long-overflow median changed from 14,482.5 ns/op to 14,457 ns/op with
  unchanged 596 allocs/op.
- Ordinary `180x30` render median changed from 794,884 ns/op, about 95,597 B/op, 3,322 allocs/op to
  751,299 ns/op, about 63,058 B/op, 1,144 allocs/op: 5.5% lower time and materially lower
  allocation.
- Maximum `1000x300` render median changed from 15,740,130 ns/op, about 2,911,300 B/op, 36,673
  allocs/op to 15,421,670 ns/op, about 2,522,805 B/op, 12,960 allocs/op: 2.0% lower time and
  materially lower allocation. No end-to-end timing or allocation metric regressed, so the v2 gate
  accepts the experiment.
- Final pane fitting remains unchanged. The workspace compositor is the only uniform owner that
  pads every pane and clips empty/status/error/selected/styled rows before joining separators.
  Producers intentionally return variable-width semantic rows and only clip where local layout
  requires it. Moving final fitting into every producer would duplicate policy and change separator
  alignment; no single alternative owner exists, so no pane-fit experiment was admitted.

### Implementation checkpoint 2 — first release-review repair wave

- The requested final thermo-nuclear, Go correctness, Go performance, Bubble Tea, and architecture
  reviews inspected one frozen revision. Their distinct actionable findings were repaired before a
  new review wave: claimed outcome backing slots retained released payloads; the side-by-side
  separator was restyled per row; reload failure ordering dropped retained search; rejected
  foreground decisions were not revalidated; delivery reservation failures returned an untyped zero
  outcome; height-one minimum rendering hid the pending decision; the reload cleanup test observed a
  process-global temp namespace; modal contexts did not accumulate accepted multi-stroke bindings;
  and publication permit tests stopped short of the worker/service boundary.
- `Coordinator.ClaimNext` now uses `slices.Delete`, whose zeroing contract clears the vacated
  interface slot before result credit is released. A backing-array regression test reslices to
  capacity after claim and proves every vacated slot is nil.
- Side-by-side rendering now styles its invariant separator once per frame. On the same Apple M2
  Max benchmark configuration, side-by-side wrap retained 3,085 allocs/op and no-wrap retained
  2,168 allocs/op, approximately 81–82 fewer allocations per frame; maximum-geometry allocation
  remained about 2.523 MB and 12,959 allocs/op.
- Correction to I-004: failure recovery must start the retained search before requesting the old
  active patch. The earlier checkpoint's patch-before-search wording described the defective order.
  One helper now owns the corrected ordering for reload acquisition/preparation failure, settlement
  failure, and deferred reload rejection. Live-coordinator tests prove both operations are admitted
  and settle after acquisition failure and rejection.
- Those tests exposed a pre-existing capacity contradiction: the state machine permitted one search
  and one presentation patch concurrently, but their valid maximum result reservations require
  about 192 MiB while `MaxRetainedResultBytes` was 128 MiB. The bound is now 256 MiB, the smallest
  existing binary ceiling that represents the accepted concurrent state while remaining explicit
  and bounded. Dedicated saturation/accounting tests remain in force.
- `foregroundState.rejected` now re-prepares and compares the complete immutable decision before any
  mutation and returns an explicit success result. Negative tests prove stale patch-retry and
  replacement-search decisions cannot consume newer continuation intent; callers surface invariant
  violations.
- Every delivery return path now carries the requested destination and a typed commit state.
  Package and coordinator/model tests prove an ambiguous first clipboard write fences the
  destination, and its rejected retry settles as failed-before-write without retained operation or
  coordinator state.
- Minimum-size confirmation copy uses two priority lines when height permits and a combined first
  line at height one. Tests cover every height from 1 through 5 and the existing width-19 boundary.
  The reload workspace shutdown test now owns a private `TMPDIR` and inspects only that namespace.
- All key contexts now share one chord accumulator/timeout owner before modal input forwarding.
  Sequential `KeyPressMsg` tests prove two-stroke bindings execute in comment-edit, search, and
  command contexts without inserting their prefix into user text.
- Deterministic worker-boundary publication tests use a controlled fake `gh` process. They prove
  cancellation before permit claim produces `CancelledBeforeCommit` and permits a new preflight;
  a claimed commit can complete with its remote identity; and cancellation after claim produces
  an unknown shutdown fact whose immutable target and marker agree with the still-publishing model
  settlement. Repeated shutdown does not repeat the fact.
- Focused app/delivery tests pass. Command-line `gopls check` passes for every touched production and
  regression-test file. `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`, and
  `git diff --check` pass after this repair wave. The MCP workspace remains stale because it does not
  index the new untracked `foreground_state.go`; its deleted-symbol diagnostics are not used as
  validation evidence.

### Implementation checkpoint 3 — second release-review repair wave

- The second frozen thermo-nuclear, Go correctness, Go performance, Bubble Tea, and architecture
  wave returned one no-findings report and five distinct repairs: export work retained the entire
  review aggregate outside request accounting; protocol evidence was concentrated in two files over
  1,000 lines; stale chord timeouts could erase later repeats; reload preparation bypassed the shared
  chord resolver; and a pending prefix could cross focus ownership.
- Export work now carries one opaque immutable `review.ExportInput`, containing only configured kinds
  and export-relevant comment values. Its target projection stores anchored paths by value and has no
  patch, search, publication, operation, snapshot, or full-review field. Markdown and JSON worker
  paths consume this projection as the canonical implementation; synchronous `Review.Prepare*`
  methods are thin projection wrappers.
- `ExportInput.RetainedBytes` charges kind metadata/data and comment metadata, kind, body, and anchored
  path bytes. Explicit conservative metadata charges preserve the `internal/review` pure-import
  boundary; test-only compiled-layout assertions prevent under-accounting on supported targets.
  Output-equivalence tests prove projected Markdown/JSON match review wrappers while a source review
  retains both a prepared patch and search hits. The projection's retained bytes remain unchanged by
  those unrelated payloads. A coordinator test builds the maximum total comment body and proves
  repeated export admission saturates exactly through projection request-byte accounting.
- Pending chords are now one owned value containing strokes, key context, focus, and generation.
  Completion, mismatch, and explicit context transitions invalidate the generation. Owner changes
  invalidate the prefix before retrying the current stroke, and this occurs before numeric-repeat
  recognition. Direct message sequences prove old timeout messages cannot erase later repeats,
  reload preparation executes a two-stroke quit, and prefixes cannot cross mouse or resize-driven
  focus changes while same-focus chords still execute.
- Coordinator/scheduler/receiver tests moved from `model_test.go` to `coordinator_test.go`. The former
  mixed `protocol_test.go` was split into search, reload, delivery, publication, and a 14-line shared
  settlement-helper file. Test ownership now follows production ownership; the largest app test file
  is 709 lines and no logic was deleted during the move.
- The first full check rejected an `unsafe` production import through the architecture gate. The
  implementation removed it rather than weakening the accepted pure-domain import set. After that
  correction, `make check` passes dependency verification, all package tests, all race tests, vet,
  and the binary build. Command-line `gopls check` and `git diff --check` also pass.

### Amendment A-017 — export-owned projection boundary

- ID: `A-017`.
- Reason: the second release-review repair replaced whole-`Review` export work with an immutable,
  export-owned projection. That is a material ownership and canonical-execution-path decision, not
  only an implementation detail, and checkpoint 3 did not version it.
- Affected obligations: `R7` bounded retained memory and event-loop work; `E9` independent release
  review, repair, and same-revision convergence evidence.
- Evidence: `review.ExportInput` contains only configured kinds and export-relevant comment values;
  `app.ExportWork` owns that projection; Markdown and JSON workers consume its cancellable methods;
  synchronous `Review.Prepare*` methods are thin projection wrappers. Projection equivalence and
  request-saturation tests exercise the boundary.
- Approval status: accepted as the smallest coherent repair to the release-review finding that
  queued exports retained an entire review outside request accounting.
- Architecture effect: `internal/review` remains the domain owner of export selection/rendering and
  now also owns the opaque immutable worker input. `internal/app` owns admission, scheduling, and
  result fencing but cannot inspect or retain unrelated review state through export work. No new
  package, dependency edge, review source, output mode, or background protocol is added.
- Resulting version: Accepted Design v6 and Plan v6; Charter v1 and Execution Plan v1 remain
  unchanged.

### Accepted Design v6

Design v6 inherits Design v5 and adds one export ownership rule: queued export work carries only a
ready `review.ExportInput` plus, for Markdown, ready compiled `review.ExportTemplates`. Both values
own or conservatively account for every reachable allocation. Projection construction remains
bounded by the accepted comment limits on the terminal event loop; preparation/rendering remains
cancellable worker work. Application code may validate readiness and retained-byte totals but may
not inspect projection internals or reintroduce a whole-review export path.

### Final-plan delta v6

| Obligation | Final change | Enforcement evidence |
| --- | --- | --- |
| `R7` | Replace whole-review export retention with an opaque bounded projection; give validated comment strings exact backing ownership; give compiled templates independent readiness and retained-byte accounting. | projection equivalence/layout/accounting tests, maximum-comment coordinator saturation, caller-backing alias regressions, maximum-token template accounting |
| `E9` | Release reviews inspect the projection as the canonical path and require all asynchronous ownership/fencing findings to be repaired before another frozen-revision wave. | five-facet checkpoint findings, export/delivery outcome fencing tests, repeated no-findings gate |

### Implementation checkpoint 4 — third release-review repair wave

- The third frozen thermo-nuclear, Go correctness, Go performance, Bubble Tea, and architecture
  wave returned six actionable findings: focus transitions could leave a chord live if focus moved
  away and back before the next key; chord abandonment retained a numeric repeat; compiled template
  readiness was conflated with incomplete byte accounting; a mismatched export destination consumed
  model ownership before validation; validated comment strings could retain arbitrarily larger
  caller backing; and the export projection decision had not amended the accepted design.
- All production focus changes now use `setFocus`, which abandons pending chords and repeats
  synchronously whenever ownership actually changes. Completion and abandonment are separate chord
  operations: exact completion preserves a repeat for `takeRepeat`, while mismatch, timeout, context
  change, focus change, and modal transition abandon it. Direct message-sequence tests cover mouse
  and resize transitions away and back, focus changes with a repeat, mismatch to a single-stroke
  action, mismatch to a new prefix, exact completion, and stale timeout isolation.
- Compiled templates now own exact-length cloned source backing, clip token slices, expose readiness
  independently from retained bytes, and conservatively charge source plus token metadata. Test-only
  layout assertions, placeholder-only readiness, and the 4,096-token boundary prevent validity and
  accounting from being conflated again. Settings and coordinator admission use the corresponding
  predicates.
- `NewCommentKind` and `NewCommentBody` clone after validation, at the retained-state boundary;
  oversized-parent substring regressions prove neither value aliases caller backing. Export input
  construction remains a shallow bounded projection on the terminal event loop.
- Export outcomes now validate the tracked destination before deleting model ownership. The export
  protocol regression proves a mismatched outcome leaves the operation pending and a subsequent
  matching outcome settles it successfully.
- `A-017`, Accepted Design v6, and the final-plan delta record the export projection's domain owner,
  app dependency direction, canonical worker path, bounded construction rule, and `R7`/`E9`
  enforcement evidence without rewriting prior plan history.
- `go test ./internal/review ./internal/app`, command-line `gopls check` for all touched production
  and test files, the complete `make check` gate, and `git diff --check` pass after this repair wave.

### Implementation checkpoint 5 — fourth release-review repair wave

- The fourth frozen five-facet wave returned no performance or architecture findings and six
  actionable correctness/interaction findings: ready compiled templates could still be empty; the
  token ceiling missed a trailing 4,097th literal; reload-preparation actions did not consume a
  retained repeat; draft and remote-review strings did not own their bounded backing; and a deferred
  mouse action could focus a pane hidden since the originating render.
- `CompileExportTemplates` now rejects an empty Markdown or comment token sequence and checks the
  4,096-token ceiling before returning as well as during placeholder accumulation.
  `PrepareMarkdownCancellable` trusts the same opaque readiness predicate as settings and
  coordinator admission. Regressions cover both empty sides, exactly 4,096 placeholder tokens, and
  4,096 placeholders followed by a literal.
- Allowed reload-preparation actions now pass through `takeRepeat`; exact chord completion preserves
  the value only until that single consumer. A sequence covering acquiring repeat input,
  preparing-phase multi-stroke cancellation, and subsequent normal focus navigation proves the
  repeat cannot escape.
- Draft updates clone bounded bodies before storing them. Remote comment construction clones ID,
  author, body, creation time, and URL, while remote thread construction clones its ID. Test-only
  pointer comparisons against one-MiB parent substrings prove each retained value owns exact-length
  backing; remote paths already copy through `NewPath`.
- Mouse action admission now validates the current visibility of its typed focus owner after the
  existing review-state/context fences. Real `View.OnMouse` commands captured from visible tree and
  comments panes are delivered only after those panes are hidden; both are rejected without
  changing current focus, chord, or numeric-repeat ownership. Unknown focus values are rejected by
  the same closed switch.
- Focused app/review tests, command-line `gopls check`, the complete `make check` gate, and
  `git diff --check` pass after this repair wave.

### Implementation checkpoint 6 — fifth release-review repair wave

- The fifth frozen five-facet wave returned no performance or architecture findings. Go and Bubble
  Tea independently identified the same remaining repeat leak for a completed but disallowed
  reload-preparation binding; thermo-nuclear review found one remaining unowned remote failure-detail
  string.
- Reload-preparation routing now consumes the numeric repeat immediately after every exact action
  resolves, before checking whether that action belongs to the preparing-phase allowlist. The count
  is passed only to allowed quit/interrupt/cancel actions. A root `Update` sequence proves a retained
  repeat is consumed by a disallowed multi-stroke action, the action itself does not execute, and
  subsequent normal focus navigation advances once.
- `Snapshot.WithRemoteThreadFailure` now clones a validated `OperationDetail` before retaining it.
  The remote ownership regression extends its oversized-parent pointer checks to the summary detail,
  completing exact backing ownership for every string retained by the remote-review aggregate.
- Focused app/review tests, command-line `gopls check`, the complete `make check` gate, and
  `git diff --check` pass after this repair wave.

### Amendment A-018 — exact-owning operation detail value

- ID: `A-018`.
- Reason: the sixth release-review wave proved that cloning remote failure details locally did not
  repair the shared failure class. Operation history, search, syntax, publication, reconciliation,
  progress, and shutdown facts all retain the same previously primitive string value.
- Affected obligations: `R7` exact bounded retention and `E9` independent release-review repair
  convergence; checkpoint 6's local remote-detail repair is superseded by this shared boundary.
- Evidence: `OperationDetail` appears in every retained status aggregate and its logical byte length
  drives operation/progress limits. App and source truncation helpers previously returned borrowed
  substrings. Oversized-parent regressions now cover operation settlement, unknown settlement,
  search, publication, reconciliation, and remote failure retention.
- Approval status: accepted as the smallest coherent repair to the architecture finding; adding
  clones independently to each consumer was rejected because it would preserve an invalid public
  primitive and duplicated ownership obligations.
- Architecture effect: `internal/review` owns one opaque, comparable, validated, exact-owning
  `OperationDetail` constructed with `NewOperationDetail`. App/source boundary helpers normalize and
  bound external text before constructing it. Domain aggregates store the value directly and may
  query only `String`, `Bytes`, or `IsEmpty`; no consumer can create a borrowed non-empty detail.
- Resulting version: Accepted Design v7 and Plan v7; Charter v1 and Execution Plan v1 remain
  unchanged.

### Accepted Design v7

Design v7 inherits Design v6 and replaces primitive operation-detail strings with one opaque domain
value. Construction validates UTF-8 and the 8 KiB maximum and clones exact-length backing once.
Empty detail construction is explicit; there is no mutable exported sentinel. Operation-history and
progress accounting use the value's byte query, while search, syntax, publication, reconciliation,
remote-thread failure, and shutdown state share the immutable value without recloning. Boundary
normalization occurs before construction in `internal/app` and `internal/source`.

### Final-plan delta v7

| Obligation | Final change | Enforcement evidence |
| --- | --- | --- |
| `R7` | Make all retained operation/status detail text exact-owning at one shared constructor boundary; remove borrowed primitive construction and local clone exceptions. | constructor invalid-input tests; oversized-parent operation/search/publication/reconciliation/remote alias tests; existing status/progress capacity tests |
| `E9` | Treat a repeated backing-ownership defect as a shared-value architecture failure and require the next frozen five-facet wave to inspect every consumer. | sixth-wave converged findings and subsequent no-findings gate |

### Implementation checkpoint 7 — sixth release-review repair wave

- The sixth frozen five-facet wave returned no performance finding. Go and thermo-nuclear review
  independently found borrowed `OperationDetail` backing in operation history; architecture traced
  the same invalid primitive through search, publication, reconciliation, progress, and remote state.
  Bubble Tea found a separate terminal/progress readiness interleaving.
- `OperationDetail` is now an opaque comparable value with a validating, exact-cloning constructor
  and `String`, `Bytes`, and `IsEmpty` queries. The mutable-sentinel risk of a struct-valued exported
  variable is avoided by explicit empty construction. App and source boundary helpers normalize
  invalid UTF-8, truncate without splitting a rune, and construct the owned value before submitting
  it. Retained domain consumers share that immutable owner; capacity accounting uses `Bytes`.
- Constructor rejection tests cover invalid UTF-8 and over-8-KiB data. One-MiB parent-substring tests
  cover failed and unknown operation settlement, settling an unknown operation, search application,
  publication application, reconciliation application, and remote-thread failure summary retention.
  `A-018`, Accepted Design v7, and the final-plan delta record this shared ownership decision.
- `Coordinator.ClaimNext` now re-signals readiness after a terminal claim when either a terminal
  outcome or queued progress remains. A deterministic test models a progress notification occupying
  the capacity-one channel, a terminal completion losing its duplicate signal, terminal-priority
  claim, and immediate second readiness/claim of the retained progress.
- All package tests, command-line `gopls check` on the migration surface, the complete `make check`
  gate, and `git diff --check` pass after this repair wave.

### Amendment A-019 — bounded shared external-detail normalization

- ID: `A-019`.
- Reason: Design v7 centralized retained ownership but left identical lossy normalization in app and
  source. Both copies scanned and could expand an unbounded external string before applying the
  8 KiB ceiling, including on the terminal event loop.
- Affected obligations: `R7`, `E9`, and `A-018`'s boundary-normalization clause.
- Evidence: every seventh-wave actionable report identified the ordering defect. The repaired 1 MiB
  and 16 MiB alternating-invalid-input benchmarks allocate the same bounded bytes/op and allocs/op
  because only the first 8 KiB enters UTF-8 normalization.
- Approval status: accepted as the smallest shared-policy repair. Keeping two corrected copies was
  rejected because lossy validation semantics belong with the opaque value and would otherwise
  drift.
- Architecture effect: `internal/review` exposes preserving and trimming bounded constructors over
  one unexported normalization algorithm. It slices raw input to 8 KiB without scanning, normalizes
  only that prefix, reapplies the byte ceiling at a valid rune boundary after replacement expansion,
  optionally trims the bounded value, and exact-clones once. App/source helpers are thin policy-name
  adapters only.
- Resulting version: Accepted Design v8 and Plan v8; Charter v1 and Execution Plan v1 remain
  unchanged.

### Accepted Design v8

Design v8 inherits Design v7 and requires all lossy external operation-detail conversion to use the
shared bounded constructors. No caller may normalize or trim the unbounded original. Raw bounding
precedes UTF-8 replacement; normalized bounding follows it. App uses the trimming variant, source
uses the whitespace-preserving variant, and strict already-valid domain construction continues to
use `NewOperationDetail`.

### Final-plan delta v8

| Obligation | Final change | Enforcement evidence |
| --- | --- | --- |
| `R7` | Centralize bounded-prefix UTF-8 normalization and keep event-loop work/transient allocation independent of external error-string length. | large invalid-input semantic regression and benchmark; focused app/source/review tests |
| `E9` | Re-review normalization ordering and ownership together on one frozen revision. | seventh-wave convergence and subsequent no-findings gate |

### Implementation checkpoint 8 — seventh release-review repair wave

- The seventh frozen five-facet wave returned no performance finding. Go, thermo-nuclear, Bubble
  Tea, and architecture independently converged on one defect: app/source helpers duplicated lossy
  normalization and scanned or expanded the entire external string before applying the 8 KiB bound.
- `internal/review` now owns both preserving and trimming bounded constructors over one algorithm.
  It slices the raw string before any scan, normalizes only that prefix, reapplies the ceiling at a
  valid rune boundary after replacement expansion, optionally trims bounded text, and exact-clones
  once. The app and source helpers delegate directly to the named policy variant. `A-019`, Accepted
  Design v8, and its final-plan delta record this refinement.
- The semantic regression uses a bounded prefix followed by one MiB of invalid tail data and proves
  valid output of at most 8 KiB. Three benchmark samples at both 1 MiB and 16 MiB alternating-invalid
  input allocate identical 46,336 B/op and 4 allocs/op; observed time remains approximately 37–39 µs
  because only the first 8 KiB is inspected.
- Focused app/review/source tests, command-line `gopls check`, the complete `make check` gate, and
  `git diff --check` pass after this repair wave.

### Implementation checkpoint 9 — eighth release-review repair wave

- The eighth frozen five-facet wave returned exact no-findings reports from Go correctness,
  performance, Bubble Tea, and architecture. Thermo-nuclear review found one simplification: eight
  retained-state paths still rechecked the opaque `OperationDetail` representation's size/UTF-8
  invariants even though no public invalid construction remained.
- The private `valid` method and all consumer revalidation branches are deleted. Operation,
  search-result, publication, reconciliation, and remote-failure paths retain semantic transition
  checks only; required failure detail still uses `IsEmpty`. The white-box forged-invalid storage
  test is deleted, while strict and lossy constructor rejection/normalization tests remain the sole
  invariant evidence.
- Focused app/review/source tests, command-line `gopls check`, the complete `make check` gate, and
  `git diff --check` pass after this repair wave.

### Implementation checkpoint 10 — ninth release-review repair wave

- The ninth frozen five-facet wave returned exact no-findings reports from Go correctness,
  performance, Bubble Tea, and architecture. Thermo-nuclear review found one final unnecessary mode:
  an internal enum and conditional existed only to select trimming in the two public bounded-detail
  constructors.
- One private helper now returns only bounded normalized text. The whitespace-preserving wrapper
  constructs it directly; the trimming wrapper applies `strings.TrimSpace` to that already-bounded
  text before construction. The enum, constants, parameter, conditional, and unknown-mode behavior
  are deleted without duplicating normalization.
- Focused app/review/source tests, command-line `gopls check`, the complete `make check` gate, and
  `git diff --check` pass after this repair wave.

### Final convergence gate — tenth release-review wave

- Five independent facets inspected one frozen checkpoint-10 revision without edits between
  reports. Each returned its exact no-findings result: thermo-nuclear code quality, Go correctness,
  Go performance, Bubble Tea design, and architecture.
- No production or test code changed after the complete checkpoint-10 `make check`; this append-only
  entry records the resulting review evidence. The implementation/review loop exit condition is
  satisfied.
