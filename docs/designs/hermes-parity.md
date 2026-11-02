# Hermes feature verification contract

## Authority and method

[ADR 0005](../adr/0005-feature-level-native-scope.md) replaces the former 1:1 consumer-parity requirement. [Native Hermes](pig-hermes.md) defines required workflows; [shared storage](shared-storage.md) defines data and publication invariants. This file keeps its existing path so references remain valid.

Use the pinned upstream source as an implementation reference, not an exhaustive specification. Read the relevant modules, reuse useful tests/fixtures and compare representative outputs. Retained captures establish only what they observed. They do not require reproducing every branch, field, alias, schema variant or message.

Write focused runnable checks alongside each feature. Do not block implementation on a prebuilt black-box corpus, full runtime-closure recapture or a branch-to-fixture ledger. Keep source attribution and synthetic test data; never retain personal memories, sessions or credentials.

## Required coverage families

| Family | Required native behavior and checks | Checkpoint |
| --- | --- | --- |
| Configuration and paths | Validated settings, useful defaults, explicit knowledge/session roots, canonical identities and project binding; document migration for required existing settings | H2 |
| Memory writes | Add/replace/remove, global/project/category attribution, scans, Unicode-sensitive matching, duplicates/ambiguity, cancellation and repairable partial success | H2/H3 |
| Memory search | Bounded query/results, scope/category filters, useful ranking/snippets, timestamps and hit metadata; malformed queries fail safely | H3 |
| Session search | Query-based search over PiG, Pi and Claude Code only; project/role filters, bounded snippets, source attribution and relevant branch/compaction/tool content | H3/H4 |
| Skills | Create/view/patch/update/delete, global/project scope and guarded external discovery, collisions, safe moves, deletion confirmation and partial success | H3/H4 |
| Context and instructions | Both storage modes, fenced context, policies/preview and explicit-user-only standing-instruction writes | H3 |
| Learning and consolidation | Useful configured thresholds, review serialization, correction capture, bounded budgets, fallback/attribution, no-op/failure distinction and restricted child tools | H4 |
| Commands | Insights, skills, consolidation, interview, project switching, indexing, sync, preview, pins and tool guidance; usable terminal/RPC behavior and cancellation | H4 |
| Lifecycle and maintenance | Bounded indexing/backfill, retention, eager asynchronous integrity checks, failed init, replacement, compaction, reload, joined shutdown and recoverable mixed-writer failures | H2/H4 |

Check public tool registration and real calls for supported schemas. Names/counts alone are not functional evidence. Exact UI text, error strings, permissive parser quirks and unused legacy aliases are not requirements. The anchors-only alternate `session_search` schema is not required; query-based search is the supported surface.

## Supported session boundary

1. Implement separate PiG, Pi and Claude Code parsers against small synthetic format fixtures.
2. Discover only configured supported roots. Keep harness/source identity, project, role and source path traceable without adding an incompatible shared schema.
3. Exercise indexing, duplicate/reindex behavior, partial files, malformed records, retention and search across the three roots.
4. Skip unsupported formats explicitly. Do not add Codex, Cursor, Gemini, OpenCode or other session adapters merely because upstream supports them.

This boundary covers session data only. The extension runs in PiG; Pi continues using its existing Hermes. Model/provider support is independent of session harness support.

## Host and safety gates

- Prove attributed completions, configured fallback, cancellation and owner/session fencing through PiG's provider stack. Offline feasibility does not prove production ownership.
- Enumerate the restricted child's actual registry and attempt denied calls. Only `memory_add`, `memory_replace` and `memory_remove` are model-callable there; file/shell/MCP and skill tools remain denied.
- Prove shared-store behavior with TypeScript/Go processes, canonical operation identities, final data assertions and deterministic barriers. Preserve ADR 0002's modeling and publication/recovery safety requirements.
- Check scans, traversal/symlink/collision guards, permission handling, partial success, secret-safe diagnostics and explicit standing-instruction provenance.
- Exercise real PiG lifecycle transitions; do not retain request-scoped SDK contexts in background workers.

## Completion rule

A feature is complete when its supported workflow and owning safety checks pass, not when every upstream branch is captured. Record intentional differences and known limits with the feature. Shared-data incompatibility or a security/lifecycle defect still blocks qualification.

Native Hermes stays outside installed membership until the scoped workflows, shared-store safety, packaging and rollback checks pass. Live activation remains separately authorized. Never label feature-level qualification as full upstream parity.
