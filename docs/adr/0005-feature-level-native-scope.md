# ADR 0005: Feature-level native replacements and supported sessions

Status: Accepted — owner approved a PiG-only runtime with PiG, Pi and Claude session support.

## Context

The delivery contracts required 1:1 upstream behavior and an exhaustive Hermes consumer corpus before implementation. This package serves one owner's workflow, not every upstream consumer. Those requirements add work without establishing that the needed features work.

## Decision

Build source-led, feature-level native replacements, not exact upstream ports. This supersedes the complete-baseline-parity requirement in ADR 0001 and the capture-first Hermes coverage rules. Pinned source, useful upstream tests and retained fixtures remain implementation references.

- Install and run this package only in PiG. Pi keeps its existing TypeScript Hermes; do not add Pi or Claude runtime adapters here.
- Import, index and search sessions from PiG, Pi and Claude Code only. Keep their roots explicit and separate from knowledge, authentication and settings roots. Other session harnesses are out of scope.
- Retain memory mutations/search, session search, skills/discovery, context/standing instructions, learning/consolidation, indexing/sync and user command workflows. Preserve native usage provider/account features and commands; session harness scope is not a provider allowlist.
- Use clear native schemas, validated configuration and honest outcomes. Exact legacy aliases, permissive parser quirks, alternate upstream schemas, UI wording and error strings are not requirements unless a retained consumer or shared-data contract needs them.
- Write focused runnable checks with each feature. Compare representative outputs where useful. Exhaustive branch inventories, full runtime-closure recapture and pre-implementation black-box corpora are not implementation or cutover gates.
- Keep existing backends selected until the scoped replacement is usable and passes functional, security, lifecycle, packaging and shared-store checks. Never claim full upstream parity.

## Unchanged safety boundaries

ADR 0002 and the shared-storage publication/recovery invariants remain in force. A simpler product contract does not authorize lost updates, schema changes, weaker scans, unsafe child tools or broken rollback. A defect requiring a coordinated TypeScript writer change still needs separate approval.

Live data, personal configuration, credentials, billing, activation, commits and publication remain separately authorized. Retained historical captures are evidence of their original observations, not additional product requirements.

## Consequences

Capture-only backlog items are superseded; their useful checks belong with feature implementation. Release review checks the supported workflows and safety properties rather than counting upstream branches, names or fields. Unsupported session formats are skipped explicitly, not misparsed as another harness.
