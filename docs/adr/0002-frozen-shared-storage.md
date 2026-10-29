# ADR 0002: Frozen shared-storage baseline with a safety gate

Status: Accepted — the owner explicitly chose to block cutover rather than authorize a coordinated writer patch.

## Context

Pi's TypeScript Hermes and native PiG Hermes must share knowledge safely. The baseline uses distinct mutation, consolidation and recovery coordination identities. Its coordinator permits time-based stale takeover and acknowledges that an ownership check followed by a filesystem operation is not atomic.

Existing feasibility probes do not prove complete publication or recovery safety. Native code alone cannot repair a race in an independently running TypeScript writer.

## Decision

Freeze the exact baseline source plus the retained asynchronous-integrity patch. Match all operation-specific coordination identities. Require bounded concurrency modeling and deterministic mixed-writer tests of publication/recovery invariants before native cutover. Preserve no-lost-update and stale-publication requirements; do not narrow them to make qualification pass.

If a counterexample requires changing the TypeScript writer, stop qualification and obtain separate approval for a coordinated safety patch, new baseline and rollout. Disposable development remains allowed; live writes, activation and migration are not authorized.

## Alternatives and consequences

| Alternative | Tradeoff and outcome |
| --- | --- |
| Assume matching lease rows proves safe publication | Cheap but misses check-then-act, filesystem and peer-open-handle races; rejected |
| Add Go-only locks or disable native stale takeover | Cannot constrain the frozen TypeScript writer and changes protocol behavior; rejected |
| Authorize a coordinated TypeScript/Go safety patch immediately | Can address shared defects, but changes the frozen baseline and independent deployment requirements; not selected |
| Preserve the baseline and block cutover on counterexamples | Keeps the approved compatibility boundary and prevents unsupported safety claims; selected, even if it delays release |

The [storage contract](../designs/shared-storage.md) names the exact coordination identities, model state, invariants and evidence gates. This decision establishes a release constraint, not proof that the current protocol satisfies it. No passing model replaces real process/filesystem tests. A later decision supersedes this record if a coordinated fix is approved.
