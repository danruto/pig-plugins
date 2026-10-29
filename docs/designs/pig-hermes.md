# Native Hermes: implementation contract

## Goal and boundary

Replace `pi-hermes-memory` in PiG with a Go extension, without reducing features or splitting the existing knowledge base. Pi keeps the TypeScript implementation. Both hosts must safely share memories and procedural skills.

Baseline: `pi-hermes-memory` **0.9.11**, upstream commit `71cf5ab7d909318a383bf52bb3ad3cf83a9efc03`, plus the retained [asynchronous integrity-scan patch](../../patches/hermes-0.9.11-async-integrity.patch). The [baseline manifest](../compatibility/hermes-baseline.json) pins the upstream archive, patch and complete packaged source tree, including startup/shutdown behavior in `src/index.ts` and `src/store/db.ts`. The port freezes that baseline; tracking subsequent upstream changes is separate work. Source fingerprints do not pin dependencies. The separate [executable closure](../compatibility/hermes-closure.json) retains the observed dependency graph, installed file hashes, PiG/Node/npm/Go and Nix closure, staged SDK, bundled Node bridge and both SQLite engine identities. [Reproduction](../compatibility/hermes.md#frozen-executable-closure) distinguishes offline artifact checks from installed-byte verification; executable observations must identify that verified closure.

The current package contains **no native Hermes replacement**. The probes establish feasibility only. No live data migration or activation is authorized by a passing probe alone.

## Public parity surface

The generated [surface capture](../compatibility/hermes-surface.json) records real default registration in an isolated PiG configuration: six tools, ten commands, lifecycle handlers, 44 canonical interface fields and packaged-source fingerprints. It does not capture parameter schemas, all accepted configuration inputs or configuration-dependent registration. The [consumer compatibility contract](hermes-parity.md) defines derivation, aliases, union/nested forms, both session-search schemas and the required branch-to-fixture inventory. Counts alone never establish parity.

Preserve these tools, including parameters, defaults, error behavior, scope attribution and response fields:

- `memory_add`, `memory_replace`, `memory_remove`
- `memory_search`, `session_search`
- `skill_manage`, including its legacy action aliases

Preserve these commands:

- `/memory-insights`, `/memory-skills`, `/memory-consolidate`
- `/memory-interview`, `/memory-switch-project`
- `/memory-index-sessions`, `/memory-sync-markdown`
- `/memory-preview-context`, `/memory-pin`, `/learn-memory-tool`

A changed native implementation may use idiomatic Go internals, but cannot silently change the public contract. Interactive skill management retains search, selection, conflict-safe moves, deletion confirmation and partial-success reporting.

## Architecture

Use a conventional `Extension() *sdk.Extension` factory under `extensions/pig-hermes`, with its own Go module. Keep SQLite dependencies out of `pig-status`. The [architecture views](architecture.md) define container boundaries, component ownership, runtime flows, lifecycle states, logical data, threats and operational signals. The [runtime decision](../adr/0001-native-runtime-ownership.md) records the approved separation and bridge qualification limits.

Keep the SDK at the extension boundary. Domain storage and parsing take explicit paths, clocks and cancellation; they do not depend on a cached request-scoped SDK context.

Suggested internal ownership:

- `store`: SQLite, Markdown authority/export, mutation locks, recovery and maintenance.
- `skills`: skill documents, scope, path guards, collision detection and discovery.
- `sessions`: JSONL parsing, indexing/backfill, retention and both search variants.
- `learning`: review/correction/consolidation plans and their lifecycle.
- Extension registration: schemas, commands, prompt sections and PiG host calls.

Create those packages only as each checkpoint needs them; do not scaffold empty abstractions.

## Shared storage invariants

1. Resolve the same canonical storage identities as TypeScript, including existing symlinked paths and missing final path components.
2. Preserve the existing global data root and project directories. Installation under PiG does not imply storage under PiG.
3. Keep Pi and PiG session roots explicit and separate from the shared knowledge root. Native indexing must deliberately include both hosts, not accidentally share settings or authentication.
4. Preserve schema, FTS5 trigram indexes, triggers, timestamps, categories, project attribution, hit counts and session-file metadata.
5. Preserve storage-mode authority and actual write paths: `policy-only` uses SQLite for search but still mutates/reconciles Markdown without legacy capacity enforcement; `legacy-inject` retains its Markdown limits and frozen prompt snapshot behavior. Document/search partial success retains the existing repair warning, not a fictional cross-store transaction.
6. Use the exact operation-specific coordination databases, paths and keys in the [shared-storage contract](shared-storage.md#coordination-identities). Mutation and recovery use their respective `.pi-hermes-locks.sqlite` locations; consolidation uses its separate `locks.sqlite` database. Go-only mutexes or file locks do not coordinate with TypeScript.
7. Match lease keys, PID/incarnation handling, timestamps, renewal, stale takeover and token-fenced release. A resumed stale holder must not publish an outdated mutation plan. Matching lease rows does not prove this invariant: the [publication-safety gate](shared-storage.md#publication-safety-gate) requires bounded modeling and concrete mixed-writer tests. A frozen-writer counterexample blocks cutover until a separately approved coordinated fix exists.
8. Preserve atomic plans, duplicate handling, ambiguous substring matching, cancellation and external Markdown edit reconciliation.
9. Do not introduce a schema that requires destructive rollback. Preserve readable rows during corruption recovery and cap recovery retries/backups.
10. Do not replace JavaScript string-length semantics with UTF-8 byte or rune counts. Capacity, matching and truncation need a Unicode compatibility corpus.

`modernc.org/sqlite` **1.52.0** is the initial native-driver candidate because the existing cached dependency passed the real TypeScript schema/trigram/read-write probe. That result does not settle corruption recovery, search ranking, contention or every target platform; those remain checkpoint gates. The [candidate decision record](../adr/0003-sqlite-driver-candidate.md) compares alternatives without presenting qualification as complete.

## Security invariants

- Memory, skills and standing-instruction writes pass the existing content/secret scans.
- Port JavaScript regular-expression semantics deliberately. Go's ASCII whitespace classes are not a drop-in replacement for JavaScript Unicode whitespace matching.
- Preserve path traversal, symlink and skill-name/collision guards.
- Memory is context, not new instructions. Preserve prompt fencing and policy styles.
- Only explicit user commands/editor actions write standing instructions. Reviews, correction capture and consolidation cannot promote content into them.
- Preserve permissions and avoid logging source memory, credentials, provider headers or prompts.
- Test with synthetic data. Never copy personal memory into fixtures or source control.

## Learning and lifecycle

Preserve turn/tool-call thresholds, the initial-turn floor, review serialization, correction patterns, configured model/fallback chains, thinking settings, no-op handling and saved-memory notifications.

Direct completions use PiG's provider stack. Keep side-channel requests separate from the foreground conversation and retain provider attribution. Support extension-provided providers, not just built-ins.

A Go SDK request context is cancelled when its handler returns. Do not retain it in a background goroutine. The feasibility probe demonstrates an owned worker emitting an unbound extension-bus event; its listener receives a fresh awaited context and can complete after startup has returned while foreground RPC stays responsive. Production use needs operation/session ownership checks, bounded work, cancellation and shutdown draining. The probe is not a complete worker implementation.

Preserve the subprocess fallback and its capability restrictions: memory mutation tools only, no general file/shell/MCP access. Native fallback uses the current PiG executable with explicit extension/provider selection, not an accidental invocation of a different harness. Honor trusted provider-extension configuration.

Preserve separate budgets and behavior for automatic review, correction save, compaction flush, shutdown flush and consolidation. Reload skips the normal shutdown flush. Drain owned indexers, learners and integrity scans before closing SQLite. Follow the [owned-work lifecycle](architecture.md#owned-work-lifecycle): close admission, run eligible normal flush, cancel/terminate and join owned work, then close resources. Capture the exact baseline budget and failure semantics per operation; interrupted cleanup is not a clean shutdown.

Preserve public response fields and configured notifications. Implement the [operational failure matrix](architecture.md#operational-signals) with fixed reason codes, bounded process-local diagnostic state and secret-safe outcomes. Do not add a telemetry service or persist a new operation journal.

## Ordered implementation checkpoints

### H1 — Executable compatibility contract

- [x] Capture real default upstream registration and canonical interface field names; this is not complete schema/configuration parity.
- [x] Verify the pinned upstream archive plus retained patch reconstructs all 92 baseline packaged files with the recorded source-tree digest.
- [x] Retain exact dependency/runtime versions and digests for the executable corpus.
- [x] Prove cross-engine schema/trigram/read-write compatibility on a disposable database.
- [x] Prove TypeScript lease visibility, release and refusal to steal a live Go-created lease.
- [x] Prove direct and registry completions through an extension provider.
- [x] Prove owned background completion leaves foreground RPC responsive.
- [x] Parse a real offline-provider PiG JSONL session with the existing TypeScript session parser.
- [ ] Derive the consumer branch-to-fixture inventory and capture operation/search/configuration fixtures, including aliases, precedence, both search schemas and disabled/variant registration.
- [ ] Establish matched synthetic startup/search/shutdown measurements.

### H2 — Storage and safety

- [ ] Port config/path/project resolution, schema and both memory modes.
- [ ] Port scanning, matching, budgets and atomic memory plans.
- [ ] Model and validate the actual publication/recovery protocol under the frozen-writer safety gate before accepting native publication code; retain assumptions and counterexamples.
- [ ] Port canonical cross-process leases, synchronization, recovery and maintenance using each operation's exact identity.
- [ ] Run real concurrent TypeScript/Go add/replace/remove, consolidation, reconciliation, recovery and crash/restart tests. Assert final content, provenance and no lost updates, including pause/takeover/resume interleavings.
- [ ] Keep this checkpoint off the live store.

### H3 — Explicit tools and context

- [ ] Port all six tool schemas and behavior, including both session-search variants.
- [ ] Port skills, collisions, scope moves and resource discovery.
- [ ] Port memory policies, legacy injection, standing instructions and context preview.
- [ ] Compare results against the TypeScript fixture corpus; preserve failure behavior rather than only happy paths.

### H4 — Automatic work and user commands

- [ ] Port review/correction/consolidation and direct/subprocess transport behavior.
- [ ] Port live indexing, bounded startup backfill, manual indexing, retention and Markdown sync.
- [ ] Port all ten commands and interactive workflows.
- [ ] Verify cancellation, compaction, reload, session replacement, failed initialization and shutdown ordering with real PiG processes.
- [ ] Verify the integrity scan remains eager/asynchronous rather than disabling or deferring the check to claim a startup win.

### H5 — Qualification and cutover

- [ ] Match every captured consumer contract: canonical fields, legacy aliases, nested/union inputs, defaults, schemas, registration variants, side effects and failure behavior. Require evidence for every parity-inventory row, not only names/counts.
- [ ] Resolve every publication-safety counterexample without weakening invariants. A coordinated baseline/protocol change requires separate approval under [ADR 0002](../adr/0002-frozen-shared-storage.md).
- [ ] Build/test Linux amd64/arm64 and macOS arm64, including SQLite FTS5/trigram support.
- [ ] Measure matched final builds for startup, first lookup, resident memory and shutdown. Report first-use and background costs separately.
- [ ] Back up shared data and record a rollback rehearsal using disposable stores.
- [ ] Activate native Hermes in PiG only, after the complete parity and mixed-writer gates pass.
- [ ] Keep Pi's TypeScript extension and shared data compatible.

## Release and ownership

Do not include `pig-hermes` in package membership until it is a usable extension. Experimental probes stay outside the installed inventory. A passing subset does not justify a reduced-feature replacement.

Dotfiles selects the package version and personal configuration. Plugin code, fixtures and checks live here. Publish an immutable source/release pin only after an explicit commit/push request.
