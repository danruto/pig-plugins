# ADR 0001: Native runtime ownership and staged replacement

Status: Accepted — records the existing approved package scope.

## Context

The current status package has a Go footer and pinned TypeScript usage backend. Native Hermes is not implemented. PiG SDK request contexts end with their handlers; background work needs independent ownership. Consumer deployment selection and personal configuration belong in dotfiles.

## Decision

Target Go-only PiG extensions with complete baseline parity before replacing either backend. Keep status independent of Hermes/SQLite. Domain owners receive explicit paths, clocks and cancellation; only extension adapters use the SDK. Keep awaited work in handlers and give deliberately asynchronous work an owned cancellation/drain lifecycle.

Use conventional Go factories for native Hermes and for status if fusion is supported. Fusion is optional and must match subprocess behavior. Preserve the existing collector until its native replacement qualifies.

## Alternatives and consequences

| Alternative | Tradeoff and outcome |
| --- | --- |
| Keep the mixed Go/Node status indefinitely | Lowest port risk, but does not meet the approved Go-only goal; retained during migration only |
| Replace only visible quota/footer behavior | Smaller port, but drops provider, command, auth and account semantics; rejected |
| Couple status and Hermes into one storage/runtime component | Shares implementation detail but puts SQLite and knowledge lifecycle on the status dependency path; rejected |
| Detach request-scoped SDK contexts into goroutines | Simple-looking scheduling but loses cancellation validity when handlers return; rejected |
| Owned extension-bus worker bridge | Existing offline feasibility evidence supports investigation; production ownership, cancellation, draining and failure behavior remain qualification requirements |

The native usage callback/session-identity bridge is not settled by the completion probe. Characterize it before provider implementation; an unbridgeable contract requires a separate decision. This record does not authorize a new host API, embedded scripting runtime, deployment or live-data change.

See [architecture](../designs/architecture.md), [native usage](../designs/native-usage.md) and [native Hermes](../designs/pig-hermes.md). A later change supersedes this record rather than rewriting its decision history.
