# Native Hermes: implementation contract

## Goal and boundary

Replace `pi-hermes-memory` in PiG with a usable Go extension that preserves the owner's knowledge workflows and shared data. This is a feature-level replacement, not a 1:1 upstream port. [ADR 0005](../adr/0005-feature-level-native-scope.md) defines the approved scope.

The package runs only in PiG. Pi keeps its TypeScript Hermes and shares memories and procedural skills safely with PiG. Session import, indexing and search support **PiG, Pi and Claude Code only**. This does not add runtime integrations for Pi or Claude, or remove unrelated model providers.

Reference source: `pi-hermes-memory` **0.9.11**, upstream commit `71cf5ab7d909318a383bf52bb3ad3cf83a9efc03`, plus the retained [asynchronous integrity-scan patch](../../patches/hermes-0.9.11-async-integrity.patch). The [baseline manifest](../compatibility/hermes-baseline.json), [executable closure](../compatibility/hermes-closure.json) and [public-surface capture](../compatibility/hermes-surface.json) retain useful provenance. They do not impose exact API, configuration or branch parity.

The current package contains **no native Hermes replacement**. An unselected [configuration/path/schema checkpoint](hermes-configuration.md) is staged under `extensions/pig-hermes`; it is not a usable memory backend. Probes and scoped checks do not authorize live data.

## Required workflows

- Memory: `memory_add`, `memory_replace`, `memory_remove` and `memory_search`, with global/project attribution, categories, bounded results and honest partial-success outcomes.
- Session search: query-based `session_search` over PiG, Pi and Claude Code sessions, with project/role filters, bounded snippets and source attribution. Preserve relevant conversation, branch, compaction and tool content. The upstream anchors-only alternate schema is not a release requirement.
- Skills: `skill_manage` creation, inspection, patch/update, deletion, scope moves and resource discovery, with collision/path guards and deletion confirmation.
- Context: fenced memory policies, existing storage modes, context preview and explicit user-authored standing instructions.
- Automatic work: bounded review, correction capture, consolidation/overflow, live indexing, startup backfill, retention and Markdown sync.
- User commands: insights, skill management, consolidation, interview, project switching, indexing, sync, context preview, pinning and memory-tool guidance. Keep these workflows usable in terminal and remote procedure call (RPC) modes; exact UI wording is not required.

Use native schemas and validated settings. Document accepted inputs and any needed migration. Preserve settings/data needed by existing consumers, but do not reproduce unused aliases, permissive parser quirks, every optional registration variant or exact error strings. [Feature verification](hermes-parity.md) defines focused checks rather than exhaustive capture gates.

## Architecture

Use a conventional `Extension() *sdk.Extension` factory under `extensions/pig-hermes`, with its own Go module. Keep SQLite dependencies out of `pig-status`. The [architecture views](architecture.md) define ownership, lifecycle, threats and operational signals.

Keep the SDK at the extension boundary. Domain storage and parsing take explicit paths, clocks and cancellation, not cached request-scoped SDK contexts. Create packages only when their feature needs them:

- `store`: SQLite, Markdown publication/reconciliation, coordination, recovery and maintenance.
- `skills`: documents, scope, guards, collisions and discovery through guarded storage.
- `sessions`: three supported parsers, indexing/backfill, retention and query-based search.
- `learning`: review/correction/consolidation plans and scheduling.
- Registration: tool schemas, commands, prompt sections and PiG host calls.

## Shared storage invariants

1. Resolve canonical storage identities, symlinks and missing path components compatibly with TypeScript. Keep the existing global/project knowledge roots.
2. Keep PiG, Pi and Claude Code session roots explicit and separate. Do not infer shared settings or authentication from shared knowledge. Skip unsupported formats explicitly.
3. Preserve the shared SQLite schema, FTS5 trigram indexes, triggers, timestamps, categories, attribution, hit counts and session-file metadata. Do not introduce a destructive rollback requirement.
4. Preserve actual storage authority: `policy-only` still mutates/reconciles Markdown without legacy capacity enforcement; `legacy-inject` retains its limits and prompt snapshot behavior. Report durable document/search partial success honestly.
5. Use the operation-specific coordination identities in the [shared-storage contract](shared-storage.md#coordination-identities). Go-only mutexes or file locks do not coordinate with TypeScript.
6. Preserve lease tokens, PID/incarnation handling, timing, renewal, takeover and fenced release. Pass the [publication-safety gate](shared-storage.md#publication-safety-gate); matching lease rows alone is not proof against lost updates.
7. Preserve guarded mutation plans, duplicate/ambiguous matching, external-edit reconciliation and cancellation outcomes. Shared capacity/matching semantics need focused Unicode checks.
8. Preserve readable rows during recovery and bound retries/backups. A frozen-writer counterexample blocks cutover until an approved coordinated fix exists.

`modernc.org/sqlite` **1.52.0** remains the initial [driver candidate](../adr/0003-sqlite-driver-candidate.md). Existing schema/trigram probes do not qualify recovery, contention or every platform.

## Security and owned work

- Scan memory, skills and standing-instruction writes for unsafe content/secrets. Keep traversal, symlink, name and collision guards.
- Treat memory as fenced context, not new instructions. Only explicit user commands/editor actions write standing instructions; automatic learning cannot promote content into them.
- Preserve permissions. Never log memory content, credentials, provider headers or prompts. All fixtures use disposable synthetic roots.
- Use PiG's provider stack for attributed side-channel completions, including extension-provided providers. Do not mutate the foreground conversation.
- Use the current PiG executable for restricted fallback. Allow only the three memory mutation tools; deny general file/shell/MCP and skill access. Honor trusted provider-extension selection.
- Give review, correction, compaction/shutdown flush and consolidation explicit bounded budgets. Keep configured model/fallback and thinking behavior useful without reproducing parser aliases.
- Request-scoped SDK contexts end with their handlers. Own asynchronous work independently and obtain fresh awaited host-call contexts. Close admission, run eligible normal flush, cancel/terminate and join workers, then close SQLite. Reload skips normal shutdown flush.
- Keep integrity scanning eager/asynchronous. Interrupted cleanup is not a clean shutdown. Use bounded, process-local, secret-safe diagnostics rather than a new telemetry service or journal.

## Implementation checkpoints

### H1 — Source reference and focused verification

Existing provenance, SQLite, lease, completion and session-format probes remain useful evidence. Read the relevant pinned source while implementing each feature; reuse upstream tests and representative fixtures. No exhaustive consumer inventory, full schema capture or pre-port operation corpus is required. Performance measurements accompany qualification rather than blocking storage implementation.

### H2 — Shared storage and safety

- [ ] Implement validated configuration, canonical paths, shared schema and both storage modes.
- [ ] Implement scans, guarded mutation plans, operation-specific coordination, recovery and maintenance.
- [ ] Model publication/recovery and prove deterministic TypeScript/Go mixed-writer interleavings under ADR 0002. Retain assumptions and blocking counterexamples.
- [ ] Verify final content, metadata and no lost updates across contention, takeover/resume and crash/restart. Keep all work off the live store.

### H3 — Tools and context

- [ ] Implement memory tools/search and query-based session search for PiG, Pi and Claude Code.
- [ ] Implement skills, safe scope moves, discovery, memory policies, standing instructions and context preview.
- [ ] Leave focused runnable checks for supported inputs, safety boundaries and honest failure/partial-success behavior.

### H4 — Automatic work and commands

- [ ] Implement owned completions/fallback, review, corrections and consolidation.
- [ ] Implement supported-session indexing, bounded backfill, retention, sync and eager asynchronous integrity scans.
- [ ] Implement the required command workflows with terminal/RPC checks.
- [ ] Verify compaction, cancellation, reload, replacement, failed initialization and shutdown with real PiG processes.

### H5 — Qualification and cutover

- [ ] Verify the required workflows and security/lifecycle properties; no exhaustive upstream parity ledger is required.
- [ ] Resolve publication-safety counterexamples without weakening invariants. Coordinated writer/protocol changes still require separate approval under ADR 0002.
- [ ] Qualify Linux amd64/arm64 and macOS arm64 packaging and SQLite FTS5/trigram support, distinguishing build checks from runtime evidence.
- [ ] Measure synthetic startup, first lookup, resident memory and shutdown. Compare matched existing observations where available; do not disable safety work to claim improvement.
- [ ] Rehearse disposable backup/restore and rollback with Pi's TypeScript writer remaining compatible.
- [ ] Add the usable, qualified extension to package membership. Live backup and activation remain separate owner decisions.

## Release and ownership

Experimental probes are not installed extensions. Keep the existing backend selected until the scoped replacement qualifies; describe its supported scope honestly, never as full upstream parity.

Dotfiles owns package selection and personal configuration. Committing, pushing, publishing and live activation each require their own authorization.
