# ADR 0003: Pure-Go SQLite as the first qualification candidate

Status: Proposed — candidate selection is established; final driver qualification is not.

## Context

Native Hermes needs the TypeScript schema, FTS5 trigram behavior, triggers and shared file compatibility without adding SQLite dependencies to status. The disposable probe used the cached `modernc.org/sqlite` **1.52.0** dependency and demonstrated basic cross-engine schema/search/read-write compatibility. It did not settle ranking, corruption recovery, contention or every target platform.

## Decision under qualification

Qualify `modernc.org/sqlite` **1.52.0** first. Keep the driver behind Hermes's store boundary. Do not change schema, search semantics or recovery guarantees to accommodate a driver. No production driver selection is complete until Linux amd64/arm64 and macOS arm64 checks, full query corpus and mixed-writer recovery gates pass.

## Alternatives and consequences

| Alternative | Tradeoff and outcome |
| --- | --- |
| Pure-Go driver candidate | Avoids a C toolchain dependency for the extension, but its SQLite version and behavior still need qualification against the TypeScript engine |
| C-backed SQLite driver | A valid alternative if qualification fails, but adds native build/runtime closure and target-platform obligations; not selected without a new decision |
| Retain a TypeScript database helper | Reuses the old engine but preserves a Node runtime boundary and does not meet the native target |
| Replace FTS5 or change the schema | Simplifies driver choice by changing behavior and stored data; incompatible with this port's approved scope |

The first successful trigram query is not a performance or production-safety argument. Measure matched final builds and retain first-use/background costs separately. Record a superseding decision if evidence changes the candidate; do not turn this proposed record into a retroactive approval.

See [Hermes gates](../designs/pig-hermes.md) and [compatibility evidence](../compatibility/hermes.md).
