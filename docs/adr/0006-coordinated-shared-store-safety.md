# ADR 0006: Coordinated shared-store safety

**Status:** Proposed — design only; not implementation or rollout approval.
**Date:** 2026-11-03
**Supersedes:** None while proposed. [ADR 0002](0002-frozen-shared-storage.md) and its cutover block remain in force.

## Decision

Develop one transaction-backed admission gate per canonical shared data domain, implemented by both TypeScript Hermes and native PiG Hermes. Storage critical sections are serialized; knowledge-database handles are opened and closed within the guard. The proposal covers Markdown publication and database recovery, not only the latest failing test.

The owner accepts bounded contention failures instead of taking ownership from a paused live holder, and a coordinated stop/upgrade/restart instead of mixed-version shared writing. This record selects a design direction, not a qualified protocol.

## Context

The [shared-storage contract](../designs/shared-storage.md#publication-safety-gate) requires preservation of accepted updates and recoverable partial outcomes. Existing lease rows do not make a later filesystem operation atomic with ownership checks. [Retained counterexamples](../compatibility/hermes-publication.md#deterministic-mixed-process-findings) demonstrate stale publication and recovery detaching accepted peer rows.

SQLite documents [one write transaction per database](https://www.sqlite.org/lang_transaction.html#read_transactions_versus_write_transactions) and warns that [replacing a database while another connection is open](https://www.sqlite.org/howtocorrupt.html#_unlinking_or_renaming_a_database_file_while_in_use) can leave processes using different files under one pathname. A separate recovery lease does not close those handles.

The bounded local in-place experiment found that both FTS rebuild and transactional derived-table recreation fail on the actual zeroed FTS shadow-root page with SQLite 3.53.2 / modernc 1.52.0. Failed attempts preserved identity and peer writes, but did not restore search. This rejects those two sequences for that fixture, not every possible native algorithm. Reproduction is retained locally at `.pb/probes/2026-11-03-native-recovery-inplace/findings.md`; durable publication of that evidence is a prerequisite to publishing this proposal.

## Rationale

### Shared guard and short-lived handles

- Both hosts acquire the same live SQLite write transaction before accessing participating knowledge databases or publishing shared documents. Acquisition is not a committed lease row followed by unlocked work: the transaction remains held through the critical section.
- The coordination database has a stable identity separate from replaceable knowledge databases. Live maintenance does not rename, recreate or directly open/close that coordination file outside SQLite; the latter avoids the [POSIX locking hazard documented by SQLite](https://www.sqlite.org/howtocorrupt.html#_posix_advisory_locks_canceled_by_a_separate_thread_doing_close_).
- Each admitted operation closes its knowledge handles and completes its filesystem work before releasing the guard. Raw handles do not escape into later callbacks, idle caches or background workers. The migration inventory includes reads, writes, schema setup, indexing, hit tracking, integrity scans, reconciliation, maintenance and recovery in both hosts.
- Model calls and plan preparation occur outside the guard. Publication reacquires it, reloads current authoritative state and revalidates the plan. Nested helpers share one admission rather than reacquiring it in a different order.
- A request cancellation signals the holder to drain; it does not release the guard beneath outstanding work. The implementation must avoid automatic SQL transaction rollback tied to the request context while filesystem work can still run. Cleanup that cannot establish a settled operation reports degraded state rather than claiming a clean release.
- A paused holder retains authority. Other operations use bounded, cancelable admission and return explicit busy/degraded outcomes. The proposal does not automatically kill an independent peer or use age alone to steal its authority.

These are proposed implementation obligations. Cross-runtime locking, cancellation and ownership behavior still require executable qualification; the SQLite documentation alone does not prove the composed protocol.

### Recovery and interrupted publication

Recovery obtains the same guard before opening the knowledge store. With participating handles scoped to admitted operations, replacement has no independently retained cooperating handle to detach. Nonparticipating or legacy handles remain outside this guarantee and are excluded by the rollout prerequisites, not fenced by a version marker.

File-set changes are not a cross-database transaction. The implementation specification must define a durable recovery intent before the first destructive change, interruption detection before ordinary admission, and bounded restore-or-complete behavior. A successor examines actual files and retained rollback material rather than treating a phase marker as proof that a rename completed. Ambiguous, unreadable or inconsistent intent blocks ordinary work without deleting preservation material.

The existing schema, knowledge/document authority, honest partial-success results and no-destructive-downgrade requirement remain. A backup alone is not proof that an accepted update remains reachable. Corruption of the admission mechanism itself fails closed; replacing that mechanism while participants are alive is not an automatic recovery path.

### Alternatives not selected

- **Current leases plus additional owner checks:** rejected by the retained check-then-act and open-handle counterexamples.
- **Concurrent admission, per-document gates and generation-managed cached handles:** not selected. It offers more concurrency but adds lock-ordering, generation and crash states; the owner chose the simpler shared gate.
- **The two tested native SQL repairs:** not a solution for the observed damaged-page case. Safe refusal preserves data but does not, by itself, complete useful recovery requirements.

## Consequences

The tradeoff is reduced storage concurrency within one data domain. A stalled live holder can cause bounded failures until it resumes or exits. This is preferable to forced progress that loses accepted updates. Independent domains can have independent gates; domain identity and membership need a canonical, matching definition in the implementation specification.

### Qualification before implementation acceptance

1. Inventory every TypeScript and Go shared-storage access path and remove bypasses, escaped handles and inconsistent domain mappings.
2. Specify guard lifetime, journal/timeout settings, recovery intent and crash reconciliation, including failures of acquisition, release, filesystem operations and shutdown. Exact interfaces and result codes belong in the implementation specification and generated code reference, not this ADR.
3. Extend the bounded two-writer model and deterministic real TypeScript/Go checks. Exercise paused holders, accepted peer writes, process death around each destructive step, cancellation, failed restoration and partial reconciliation without narrowing preservation assertions.
4. Prove eager integrity and maintenance worker admission, cancellation and joining before handle close. Preserve genuine maintenance failures even when cancellation is also present.
5. Verify schema/data interoperability, supported local-filesystem/platform behavior and disposable backup/rollback. Keep unqualified environments and nonparticipating writers explicitly unsupported.

### Separately approved rollout

1. Finish the reviewed implementation and disposable qualification before requesting live rollout authority.
2. Stop all old Pi and PiG writers, their children and background work. If their absence cannot be established, keep shared writing stopped; old processes do not become safe merely because a new version marker exists.
3. Take and verify a consistent backup while writers are stopped. Deploy pinned compatible implementations and matching domain/protocol configuration to both hosts.
4. Start the new participants only after compatibility and ownership checks pass. Native selection and live-data access remain separate owner approvals.
5. For rollback, stop and drain the new participants first. Preserve current data, intents and post-upgrade accepted writes; restoring an older backup is not an automatic substitute. Use a separately rehearsed, approved rollback or remain stopped.

Before adoption, reconcile ADR 0002, the coordination identities and publication/recovery requirements in `shared-storage.md`, the ownership flows in `architecture.md`, and the native recovery guide. This proposal does not silently change them. The failed peer-preservation check remains a blocker until a qualified replacement demonstrates the same invariant.

## Approved By

- Repository owner: design direction and operating tradeoffs only. Implementation, a revised baseline, publication and rollout remain unapproved.
