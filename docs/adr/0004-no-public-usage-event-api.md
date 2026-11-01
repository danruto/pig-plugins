# ADR 0004: No public usage event API in the native port

Status: Accepted — the owner chose to leave the public usage event API out of the native port.

## Context

The pinned usage backend exposes a read-only `usage:*:v1` event API (`usage:query:v1`, `usage:capabilities:v1`, `usage:changed:v1`). Its only consumer is the package's own footer adapter, `status/usage.ts`. The package serves a single owner's PiG setup.

Real PiG captures show that the Go/Node event bus carries values as JSON. The API's session reference, `respond` callback and `AbortSignal` cannot cross it in either direction. [ADR 0001](0001-native-runtime-ownership.md) left this bridge for a separate decision.

## Decision

Do not port the public usage event API. The native Go collector feeds the Go footer directly, in process; the footer is the only usage consumer. Provide no replacement event API, versioned or JSON-only. Third-party extensions that rely on `usage:*:v1` from this package are unsupported.

Behavior the footer depends on stays in scope at the collector-to-footer seam: normalized reports, cache expiry, stale-publication rejection and cancellation on model or session change. That includes the footer's own session, session ID and provider fence on an invalidation, even though the baseline footer receives invalidations through a `usage:changed:v1` listener. Obligations that exist only for the public API seam are out of scope.

## Alternatives and consequences

| Alternative | Tradeoff and outcome |
| --- | --- |
| Upstream host API that carries callbacks, signals and session identity across realms | Keeps the contract, but needs a PiG host change for a package with one consumer; not selected |
| Node shim kept in the package | Keeps the contract, but retains a Node runtime and fails the Go-only goal; rejected |
| Drop the public API and feed the footer in process | Loses third-party `usage:*:v1` access, which no supported consumer uses; selected |

The [Go/Node boundary evidence](../compatibility/usage.md#gonode-boundary) is the recorded basis for this decision, not a checkpoint gate. See [native usage](../designs/native-usage.md#scope-and-current-state). A later decision supersedes this record if a supported external consumer appears.
