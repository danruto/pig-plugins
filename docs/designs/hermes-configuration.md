# Staged native Hermes storage

This is disposable feature development for issues 16–19, not activation or a cutover guide. `extensions/pig-hermes` is an isolated Go factory module, deliberately absent from `pi.extensions`. The existing selected backends are unchanged. SQLite remains outside status. Guarded mutations, operation-specific coordination and [bounded recovery, repair and maintenance](hermes-recovery.md) are staged; native tools, indexing and learning are not implemented. Their settings are retained and validated, not claims that those workflows already run.

## Configuration migration

Native Hermes reads its own `hermes-memory-config.json` under the PiG agent directory (`PIG_CODING_AGENT_DIR`, otherwise `$PIG_HOME/agent`, otherwise `~/.pig/agent`). An absolute `PIG_HERMES_CONFIG` selects a different native configuration file. Sharing knowledge never selects Pi's settings or authentication. Only a missing configuration file means defaults; malformed or unreadable files fail initialization.

[Accepted fields, enum inputs and defaults](../reference/hermes-settings.md) are extracted from the Go settings type and default parser. Configuration is strict JSON: booleans are booleans, counts/budgets are integers, arrays contain strings, and unknown fields or null values fail. `projectsMemoryDir` must resolve to one child directory of the shared agent root. `consolidationChunkChars` is at least 500; nonpositive `flushCompactTimeoutMs` retains the source's disable semantics. Paths may use `~` or `~/`.

To prepare a **disposable synthetic** native configuration from an existing shape:

1. Copy only the desired settings into a separate fixture; never migrate personal settings during qualification.
2. Preserve `memoryMode`, `memoryDir` and `projectsMemoryDir` when shared knowledge layout is intentional. Knowledge still defaults to `PI_CODING_AGENT_DIR/pi-hermes-memory` (or `~/.pi/agent/pi-hermes-memory`). Relative `memoryDir` is relative to that shared agent root, not the PiG settings root.
3. Legacy `autoConsolidate: false` becomes `memoryOverflowStrategy: "reject"`; true retains `"auto-consolidate"`. An explicit strategy wins and determines `autoConsolidate`.
4. Split array-valued `llmModelOverride` into a primary string and `llmFallbackModels`. Rename legacy `llmModelFallbacks`/`fallbackModels` to `llmFallbackModels`. Aliases are deliberately not parsed. Remove unsupported session-search `anchors`; omit `sessionSearch` or use the query-based `legacy` setting. Other harness-specific settings are not accepted.
5. Set `sessionRoots.pig`, `sessionRoots.pi` and `sessionRoots.claudeCode` explicitly when importing nondefault layouts. No session/authentication/configuration trees are copied or merged.

PiG sessions default to the PiG agent's `sessions` directory; Pi sessions default to the shared Pi agent's `sessions`; Claude Code data defaults to `~/.claude/projects`. `PIG_CODING_AGENT_SESSION_DIR` and `PI_CODING_AGENT_SESSION_DIR` independently override the first two defaults; explicit native `sessionRoots` wins. Relative session roots resolve against the session working directory. This checkpoint only resolves paths: it does not discover, parse or import any sessions.

## Storage identities and authority

Paths follow pinned `paths.ts`, `project.ts` and `canonical-storage-path.ts`. Canonicalization traverses symlinks (including dangling links), retains missing tails, bounds symlink traversal at 40, and propagates non-missing filesystem failures. Project detection receives the session's cwd on each `session_start`, rather than capturing the extension process's startup cwd. Git subdirectories and linked worktrees use the repository name; the source's existing cwd-basename store migration bridge is retained. Home and filesystem root have no project. Project selection uses the source's lexical cwd naming before canonicalizing the resulting storage path.

Both modes retain document-backed authority: `policy-only` means searchable SQLite knowledge plus Markdown mutation/reconciliation without legacy character caps; `legacy-inject` retains encoded-entry UTF-16 character limits. Guarded memory add/replace/remove plans scan content and secrets, reject ambiguous matching and destructive multi-line replacements, and reconcile exact target/project/category identities while retaining existing recall statistics. The factory constructs this storage API; tool registration and prompt injection are separate checkpoints. No SQLite-only publication protocol or atomic document/database rollback is claimed.

## Guarded mutation checkpoint

Mutations use the baseline canonical Markdown identity, shared `.pi-hermes-locks.sqlite` location, `mutation:` key, token/PID/incarnation rows and 5,000/10/300,000-ms wait/poll/stale timings. Each operation reloads disk truth, builds a complete plan before publication and retries external-edit conflicts at most twice. Replacement preserves creation dates and failure project attribution; failure categories and details remain visible Markdown with the baseline `project64` metadata. Scoped mirror reconciliation runs in its own SQLite transaction, retains exact-identity row IDs and usage signals, removes stale/duplicate scoped rows and updates baseline sync metadata.

Publication stages a same-directory file, preserves document permissions, displaces the prior document into a recovery artifact, uses hard-link no-clobber publication and verifies displaced/current fingerprints. Recovery snapshots are retained with bounded retired/conflict artifacts. This implementation always takes the baseline displaced-snapshot path rather than its hourly snapshot-reuse optimization. These are separate filesystem and SQLite steps, **not an atomic compare-and-publish primitive**. ADR 0002's stale-holder/check-then-act counterexamples and mixed-writer qualification gate remain unresolved; matching the mutation lease is not proof of no lost updates. No native-only protocol repair is introduced.

A saved document plus failed search reconciliation is reported as saved with stale-search warning and repair advice. Cancellation or cleanup errors after publication retain the publication flag; callers must inspect the result even when an error is returned. The selected backend's `/memory-sync-markdown` remains the operational repair path; no new native command is registered. Legacy overflow is rejected with capacity advice at this layer; FIFO/automatic consolidation orchestration and frozen prompt snapshots remain future feature work. Managed memory now exposes document-authoritative reconciliation without republishing a mutation; database recovery and dormant snapshot maintenance remain unselected staged behaviors.

## Schema checkpoint

The factory's awaited session-start handler loads validated settings, resolves paths and opens the managed store. It cancels/joins the previous integrity and maintenance workers on replacement or shutdown before closing SQLite. Those workers have independent bounded contexts; no request-scoped SDK context is retained. Schema work takes explicit paths and standard cancellation. Initialization errors at the SDK boundary use fixed stage messages without private paths or content; direct config/store errors remain available to local validation.

The schema is translated directly from pinned `schema.ts`, with the additive legacy columns, target-constraint upgrade, session-project backfill and versioned trigram migration from `db.ts`. Initialization uses an immediate SQLite transaction; failed migrations roll back rather than replace or discard the database. Existing metadata and memory/session rows are retained. FTS tables rebuild from their owning rows when the tokenizer version or table definition requires it, or after a legacy memory-table replacement. Indexes and insert/update/delete triggers are recreated after replacement. Connection-local foreign keys, WAL, busy timeout, autocheckpoint and journal-size settings follow the source.

`modernc.org/sqlite` 1.52.0 remains a candidate. Config/schema checks on disposable roots do not qualify recovery, peer-open-handle replacement, every platform, publication safety or rollout. Frozen-writer publication counterexamples still block cutover under ADR 0002, not this scoped feature work. No protocol repair or baseline change is made.

## Verification and provenance

`GOMAXPROCS=2 GOFLAGS=-p=2 npm test` stages PiG's version-matched SDK via a temporary modfile, then builds, vets and race-tests the isolated native module and checks deterministic settings-reference drift. `bash scripts/hermes-settings-reference.sh` regenerates the settings reference without touching host state.

Source: `pi-hermes-memory` 0.9.11, commit `71cf5ab7d909318a383bf52bb3ad3cf83a9efc03`, specifically `src/config.ts`, `constants.ts`, `paths.ts`, `project.ts`, `index.ts`, `store/memory-store.ts`, `store/memory-lookup.ts`, `store/content-scanner.ts`, `store/markdown-mutation-lock.ts`, `store/atomic-lock-coordinator.ts`, `store/sqlite-memory-store.ts`, `store/schema.ts` and the schema/connection sections of `store/db.ts`. The retained async-integrity patch informs the owned eager scan; see the recovery guide for its staged lifecycle and qualification limits. Attribution and license are retained in `THIRD_PARTY_NOTICES.md` and `extensions/pig-hermes/LICENSE`.
