# ADR 0005: Feature-level native usage replacement

Status: Accepted — PiG-only runtime, source-led implementation.

## Context

This package serves one owner's workflow, not every upstream consumer. Exact upstream parity and exhaustive pre-port captures add work without establishing that the required features work.

## Decision

Translate the pinned TypeScript usage modules directly into Go. Preserve useful workflows rather than every upstream parser quirk or error string.

- Install and run this package only in PiG; do not add adapters for other hosts.
- Preserve provider/account features, settings, commands, authentication, refresh and cache ownership.
- Use validated native inputs and honest failure outcomes. Retain settings needed by existing consumers and document intentional differences.
- Reuse useful upstream tests and representative fixtures. Leave focused runnable checks with each feature; exhaustive branch inventories and runtime recapture are not prerequisites.
- Keep the TypeScript collector selected until the native replacement passes scoped functional, security, lifecycle and packaging checks. Never claim full upstream parity.

Live data, personal configuration, credentials, billing, activation, commits and publication remain separately authorized. Historical captures document their original observations, not additional product requirements.

## Consequences

Release checks cover supported workflows and safety properties rather than counting upstream branches, aliases or fields. Capture-only backlog work is not a gate on implementation or cutover.
