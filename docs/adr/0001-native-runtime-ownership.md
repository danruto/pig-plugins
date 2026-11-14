# ADR 0001: Native runtime ownership and staged replacement

Status: Accepted — [ADR 0005](0005-feature-level-native-scope.md) supersedes complete-baseline parity and pre-port capture requirements.

## Context

The current status package has a Go footer and pinned TypeScript usage backend. PiG SDK request contexts end with their handlers; background work needs independent ownership. Consumer deployment selection and personal configuration belong in dotfiles.

## Decision

Target a Go-only status extension. Domain owners receive explicit authentication snapshots, clocks and cancellation; only extension adapters use the SDK. Keep awaited work in handlers and give asynchronous work an owned cancellation/drain lifecycle.

Use a conventional Go factory for status if fusion is supported. Fusion is optional and must match subprocess behavior. Preserve the existing collector until its native replacement qualifies.

## Alternatives and consequences

| Alternative | Tradeoff and outcome |
| --- | --- |
| Keep mixed Go/Node status indefinitely | Lowest port risk, but does not meet the Go-only status goal; retained during migration |
| Replace only visible quota/footer behavior | Smaller port, but drops provider, command, auth and account semantics; rejected |
| Detach request-scoped SDK contexts into goroutines | Loses context validity when handlers return; rejected |
| Owned extension-bus worker bridge | Requires fresh awaited contexts, cancellation, draining and failure checks |

This record does not authorize a new host API, embedded scripting runtime, deployment or live-data change. See [architecture](../designs/architecture.md) and [native usage](../designs/native-usage.md).
