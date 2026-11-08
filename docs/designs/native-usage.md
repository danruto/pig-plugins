# Native Go usage collection

## Scope and current state

This repository targets PiG only. The target is a Go-only status package with no Node runtime requirement. Pi adapters and Pi-only tests are outside its scope.

The current package is still mixed: `extensions/pig-status/` renders the footer in Go; `extensions/pig-status-usage.ts` loads `@narumitw/pi-usage` **0.68.3** and `status/usage.ts`. Keep that collector selected until the scoped native replacement passes the functional and safety gates below. Removing it first would remove provider adapters, settings and commands.

The approved immediate change removes the legacy Pi adapter and its dedicated tests. The native usage port is staged and source-led. Native Hermes remains a separate project under [its existing contract](pig-hermes.md); sharing a store with an independent Pi installation does not require shipping Pi support here.

The [architecture](architecture.md) defines context/container boundaries, collector ownership, the refresh sequence, worker lifecycle, trust boundaries and operational signals. [ADR 0001](../adr/0001-native-runtime-ownership.md) records alternatives and the staged native-runtime decision. Status does not inherit Hermes's storage dependencies or release schedule.

The backend's public `usage:*:v1` event API (`usage:query:v1`, `usage:capabilities:v1` and `usage:changed:v1`, defined in `src/usage-api.ts` and `src/usage-api-types.ts`) is not part of the native port. The native Go collector feeds the Go footer directly, in process. The footer is the only usage consumer. The package provides no replacement event API, versioned or JSON-only. Reasons:

- The package serves a single owner's PiG setup.
- The API's only consumer is the package's own footer (`status/usage.ts`).
- Real PiG captures show that the API's session reference, `respond` callback and `AbortSignal` cannot cross the Go/Node bridge. See the [Go/Node boundary evidence](../compatibility/usage.md#gonode-boundary).

Third-party extensions that rely on `usage:*:v1` from this package are unsupported. [ADR 0004](../adr/0004-no-public-usage-event-api.md) records the alternatives.

## Compatibility baseline

Freeze the locked **0.68.3** backend, including its transitive production dependency pins. Capture behavior from the installed source and real PiG registration, not from a reduced reimplementation. Retain third-party attribution when translating upstream code.

The baseline includes these 18 provider IDs:

- `baseten`
- `openai`
- `openai-codex`
- `deepseek`
- `github-copilot`
- `openrouter`
- `vercel-ai-gateway`
- `command-code`
- `fireworks`
- `opencode-go`
- `kimi-coding`
- `minimax`
- `minimax-cn`
- `moonshotai`
- `moonshotai-cn`
- `zai`
- `zai-coding-cn`
- `xai`

Source evidence is in the pinned backend's `src/query.ts`, `src/providers/`, `src/usage.ts`, `src/settings.ts` and `src/codex-*.ts`. Its README and `docs/` describe provider semantics, operations and settings. The read-only event API in `src/usage-api.ts` is out of scope. Inspect these alongside executable captures before implementing each checkpoint.

Read and translate the pinned source module by module: provider dispatch, host-auth resolution, settings parsing, commands and footer publication. Reuse upstream tests and useful retained synthetic fixtures. Compare normalized outputs for representative success, optional-data and failure cases; add focused Go tests for parsers, money, credentials, redirects, cancellation and stale publication. Record the source modules and any intentional differences alongside the port.

Exhaustive branch capture, V8 attribution, exact-counter replay and clearing the historical pending ledger are not prerequisites for implementation or cutover. The owner replaced that capture-first method with this source-led method. The 18-provider list still defines required functionality, not a coverage percentage. This is a feature-level replacement under [ADR 0005](../adr/0005-feature-level-native-scope.md), not a 1:1 API port. Exact upstream UI wording, error strings, unused aliases and permissive parser quirks are not requirements. Keep required existing settings readable and document intentional differences. PiG/Pi/Claude is the Hermes session allowlist, not a usage-provider restriction.

### Initial native slice

`extensions/pig-status/internal/usage/` contains the staged transport and balance adapters for DeepSeek, Vercel AI Gateway, Moonshot AI and Moonshot AI CN. They are not registered or selected yet. Host-auth integration, refresh ownership, commands and the remaining adapters still belong to the checkpoints below.

- `balance.go` translates `src/providers/deepseek.ts`, `vercel-ai-gateway.ts` and `moonshot.ts`. Monetary decimal strings stay strings; Moonshot numeric amounts preserve JavaScript number formatting and regional currency.
- `usage.go` translates the corresponding `query.ts` dispatch/endpoints and the report shape in `types.ts`. Authentication arrives as an explicit host-resolved snapshot with a context-aware revalidation callback.
- `transport.go` uses standard-library HTTP, refuses redirects before forwarding, bounds successful bodies to 64 KiB and shares one deadline with authentication and response reading. Revalidation runs before requests and before returning reports. Remote error bodies and arbitrary transport/guard error text are omitted, as required by the diagnostics safety contract. Cancellation uses native context errors; the later command layer owns user-facing failure messages.
- `node --import tsx scripts/usage-reference.mjs --check` compares the small reference file with actual pinned-source normalization. `npm run test:usage-native` also runs Go parser and local HTTPS safety tests. No V8, branch-ledger or full-host capture is involved.

## Regional adapter slice

The staged `Client.Query` seam now also translates pinned `src/providers/minimax.ts`, `moonshot.ts`, `zai.ts`, `zai-errors.ts` and their `query.ts` consumers. Attribution and the upstream MIT license remain in `THIRD_PARTY_NOTICES.md`. `scripts/usage-regional-reference.mjs` imports the actual installed exports and derives representative quota, balance, subscription, fixed-error and Moonshot auth-provenance fixtures; `--check` detects source/fixture drift. The package gate runs those comparisons and the Go normalization tests. These checks use synthetic data, not live provider qualification.

MiniMax chooses pay-as-you-go balance only for a runtime key starting with `sk-api-`; a selected Bearer override takes priority over the supplied API-key snapshot. It preserves regional currencies, exact decimal balances, model groups, dual quota windows, unlimited status, remaining-versus-used count disambiguation and millisecond reset conversion. Moonshot retains the existing numeric balance adapter unchanged. Each provider ID accepts only its own official origin; MiniMax and Zai have no invented regional key-format heuristics.

The seam consumes pre-resolved host authentication, not credential files. An omitted selected-provider identity means the snapshot describes the queried/current provider, preserving existing callers. Explicit sibling Moonshot queries require known host auth provenance; environment credentials need an explicitly supplied label that does not list the exact comma-separated `MOONSHOT_API_KEY` name. An explicitly empty label is distinct from an absent label, as upstream requires. The host must include selected-provider identity and source/label in synchronous revalidation. The later host resolver must populate those fields for cross-provider menu queries; this slice does not activate host collection or alter TypeScript selection.

Zai uses the official regional origin for both quota and optional subscription requests, strips a Bearer prefix only for monitor authentication, preserves count/percent windows, MCP detail metrics and active/current plan selection, and maps only validated numeric error codes to fixed upstream messages. Optional plan transport, HTTP, JSON and payload failures fall back to quota-level information. Invalid renewal dates discard the optional plan, matching the upstream query's catch around subscription normalization. No arbitrary provider text enters an error.

Intentional safety differences follow the existing architecture contract: all redirects are refused before forwarding credentials (including Zai, whose upstream fetch may follow before detecting them); all requests, optional work and synchronous revalidation share the bounded total deadline; cancellation or deadline expiry cannot return a report; authentication is revalidated after optional plan work as well as before that request. Non-Zai transport errors retain the existing body-free sanitized diagnostics. Zai error bodies are read only up to the upstream 4 KiB error bound and decoded only for safe fixed codes; success bodies retain the 64 KiB bound. A Zai window whose finite input overflows to infinity is rejected with a fixed error rather than publishing an unserializable report (upstream JavaScript retains infinity, which JSON turns into null). No live credentials, billing or endpoint probes are authorized here.

## Remaining consumer adapters

The staged native seam also translates `src/providers/command-code.ts`, `opencode-zen.ts`, `kimi-coding.ts`, `xai.ts` and their `query.ts`, `usage.ts` and `usage-helpers.ts` consumers from the pinned backend. `scripts/usage-consumer-reference.mjs` imports the installed source exports for representative normalized reports, malformed and optional fields, date/rounding boundaries, xAI OAuth matching and the menu-only scheduling distinction. These synthetic comparisons run in normal, native and Nix package checks; they are not live-provider qualification.

Command Code discovers the account with `alpha/whoami`, derives only a validated organization ID, then reads optional credits, subscription and billing-period summary. The summary uses the subscription's trimmed period start. Each optional transport, HTTP, JSON or shape failure degrades to the source's unavailable notes; account failure remains fatal. USD rolling windows, remaining credit pool, plan labels and split-token fallbacks remain separate. Native optional sections run sequentially so synchronous host revalidation never overlaps; all share the same total deadline. Unlike upstream's optional-budget fallback, expiration of that deadline rejects publication under the architecture contract.

OpenCode Go uses the Zen Go endpoint and preserves percent windows, rate-limited status and sanitized unavailable notes. Kimi preserves count windows, absent-counter inference, duplicate-window rejection, explicit-window suppression of ratio fallbacks, stable window ordering, fixed-point booster rounding, currency validation and optional wallet rejection. Its strict timestamps remain separate from the permissive JavaScript date semantics used by the other adapters, including day overflow, midnight 24:00, expanded years and negative-epoch flooring.

xAI uses fresh host-supplied OAuth candidates through the existing snapshot callback, not API-key billing or credential-file reads. Exactly one complete candidate must match the effective runtime access token; duplicate offers are not collapsed. Consumer identity discovery precedes billing, and only the proxy's validated canonical user ID becomes `x-userid` on billing. Fixed Grok CLI headers accompany both requests; no `pi-usage` user agent or arbitrary selected-model headers are forwarded. Revalidation also rematches the OAuth identity before each request and before publication. Signed integer cents, preferred percentage, legacy/current periods, empty configuration notes and optional tier validation follow the pinned source. Model authentication remains restricted to `https://api.x.ai`, independently of the consumer proxy transport origin.

The staged collector consults the explicit schedulability capability before acquiring automatic authentication: xAI and unsupported IDs have no scheduled collection and cannot enter its footer projection. Explicit `Client.Query` remains available for xAI menu actions. The selected TypeScript collector and package membership are unchanged and continue to enforce the existing menu-only behavior.

All four adapters retain the existing two-query admission bound, 15-second maximum total budget, 64 KiB success-body bound, redirect refusal before credential forwarding and body-free sanitized errors. Nonfinite Command Code credit totals are rejected rather than publishing JSON-null numeric fields. No live provider or billing operations are authorized by these checks.

## Staged host authentication and selection

The SDK-free domain now resolves effective host authentication and dispatches explicit current, other-provider and all-configured selections. It uses fresh same-session readiness and credential offers, preserves nullable authorization suppression and provenance labels, and fences complete selected-model/auth revisions plus the queried registry model and provider origin. Only the selected provider uses selected-model authorization; another provider uses its own provider authentication. Inventory is owned by one awaited dispatch, not retained as a cache. Requests and publication reuse the existing guarded client, OAuth identity matching, two-query admission bound and total deadline.

Fireworks discovery returns stable account slugs. Billing requires an explicitly named discovered slug, even for a singleton; an absent or missing selection returns choices, never a substitute or aggregation. Native OpenAI remains authentication-only unless companion collection is explicitly requested. Companion authentication is independently acquired and pinned; no native credential is forwarded to ChatGPT.

The reviewed host capability is staged only at disposable process adapters, not installed registration or package membership. The current package still builds against its unpatched pinned SDK and selects the Node collector. See [the authentication fixture instructions](../../extensions/pig-status/internal/usage/testdata/host/README.md) for that isolated local-HTTPS fixture; fixture proof is not live qualification.

## Staged refresh and cache ownership

The SDK-free collector translates the five-minute expiry/refresh cadence, thirty-second failure backoff and bounded account state from pinned `src/core.ts` and `src/usage.ts`. Quota projection follows `status/usage.ts`, retaining unknown versus zero, count-versus-percent units and reset expiry; countdowns are recomputed from cached reports. Cache identity includes the full selected and registered model, host authentication revision, session, provider, explicit billing target and settings generation. Fresh authentication and Fireworks target discovery precede cache reuse. Returned data is revalidated before cache publication; superseded generations cannot write. Credentials and SDK callbacks are never cached.

`extensions/pig-status/nativeusage/` stages the conventional-factory adapter against the reviewed SDK without changing the selected executable footer. A per-owner unguessable private wakeup listener obtains a fresh awaited host context; its payload carries no reports or credentials. This is internal scheduling, not a usage API or footer feed. The staged sink receives cached projections directly in process; installed footer wiring is unchanged. Replacement closes admission, signals cancellation, joins the emitter and all admitted listeners, unregisters the private listener, and clears the sink before starting its successor. Each timer waits for the prior emission to settle, so there is no overlapping tick queue. Host notifications use fixed sanitized failure reasons and coalesce until recovery or owner change.

The [conventional lifecycle fixture](../../extensions/pig-status/nativeusage/testdata/lifecycle/README.md) uses only disposable roots and its own loopback TLS server. It exercises the production registration and collector, not a fake resolver or a retained request context. Source installation, selected-footer factory conversion, native settings/commands and backend cutover remain separate checkpoints. Scoped fixture evidence does not claim those later features or a green unrelated Hermes baseline.

## Behavior to preserve

### Collection and presentation

- Keep quota, balance, spend, currencies, billing targets and provider-specific model groups distinct. Unknown is not zero. Do not derive subscription quota from token counts.
- Preserve current-provider, another-configured-provider and all-configured-provider menu actions. Cross-provider queries require explicit user selection. Preserve partial results and bounded concurrency.
- Preserve billing-target discovery and selection, including multi-account Fireworks behavior. Never aggregate targets or silently pick a different account.
- Keep automatic refresh, cache expiry, reset countdowns, failure backoff and bounded account caches. xAI remains menu-only, without scheduled collection.
- Keep `/usage` and `/fast` useful in terminal and remote procedure call (RPC) modes, including no-argument behavior, settings interaction and clear sanitized failures; exact upstream messages are not required. Do not register duplicate commands during migration.
- Preserve native OpenAI's authentication-only default and optional Codex companion usage. Companion collection stays opt-in; never send the native OpenAI token to ChatGPT backend endpoints or claim the two logins independently prove shared account identity.
- Preserve Codex Fast capability discovery, conservative fallback, request-tier rewriting and matching assistant-message cost correction. A saved toggle affects subsequent payload hooks, not requests already sent.
- Preserve Codex reset discovery, fresh OAuth/account matching, explicit confirmation, stable redemption identity for safe retry, and outcome reporting. Do not make live redemption requests in tests.
- Validate settings, retain useful defaults and unknown fields, migrate existing target selections when needed, and use ordered private atomic saves. Refuse to overwrite invalid files. Keep required existing settings readable without copying Pi configuration; unused parser aliases are not a compatibility gate.
- Preserve what the footer depends on, at the collector-to-footer seam: normalized reports, cache expiry, stale-publication rejection and cancellation on model or session change.

### Safety and lifecycle

- Resolve the host's effective provider and selected-model authentication. Validate official origins, explicit authorization overrides and fresh credential identity before provider requests and publication. Do not bypass host resolution by reading an unrelated credential file.
- Preserve OAuth candidate readiness and uniquely matched runtime-account checks, including provider-specific restrictions and regional credential isolation.
- Reject redirects before credentials can be forwarded. Bound response size, total query time and concurrency. Sanitize terminal output and redact credentials in errors.
- Fence results by session, complete model configuration, account, target and settings generation. Old requests cannot publish into a successor's state or cache.
- Own workers, timers and cancellation through session startup, shutdown, reload and replacement. Join owned work; do not detach a request-scoped SDK context into a goroutine.
- Keep network requests, credentials and history reads outside the footer render callback. Preserve cache-inclusive token totals, cost, subscription labelling, narrow layouts and opaque Claude bridge status labelled `used`.

## Implementation checkpoints

The Go/Node bridge characterization in the [compatibility record](../compatibility/usage.md#gonode-boundary) is the evidence for leaving the public usage event API out of scope. It is not a gate on checkpoint 2.

1. [ ] Identify the pinned source and reuse useful baseline fixtures. Derive behavior from source and upstream tests while porting; do not finish exhaustive capture first. Use disposable PiG roots and local synthetic endpoints for host integration checks. Never retain personal credentials or session history.
2. [ ] Port provider normalization and guarded HTTP transport with fixtures for all 18 providers. Exercise malformed data, optional-section failures, origin rejection, redirect rejection and secret-safe errors. Compare against the frozen backend, including units and timestamp conversions.
3. [ ] Port host-auth integration, account/target selection, cache ownership, automatic refresh and in-process publication to the footer. Prove that model/account/settings changes, cancellation and expiry reject stale publications through real PiG processes.
4. [ ] Port `/usage`, settings, `/fast` and reset workflows. Prove terminal/RPC behavior, persistence and rollback on failure, request-tier and cost attribution, confirmation and safe reset retry with local fixtures only.
5. [ ] Integrate native collection with the footer. Use conventional Go factories if the package is to support fusion; the current executable footer is not a fusible factory. Keep collection separate from rendering and SQLite/Hermes dependencies out of status.
6. [ ] Replace package membership atomically after scoped functional and safety qualification. Remove the TypeScript collector, its dependency and obsolete adapter tests only when required workflows have native coverage. Replace Node-dependent status checks and the Claude test fixture; retain package metadata needed for PiG discovery.
7. [ ] Switch Nix packaging from the npm build to a native Go build. Verify source installation, prebuilt package discovery, validation, 120/80-column terminal rendering, clean shutdown and supported Linux/macOS targets without Node. Measure matched startup, resident memory and shutdown; distinguish fixture proof from live-provider qualification.

## Cutover gate

All required provider/account features and command workflows above must be implemented before removing the existing backend; exact upstream API/parser/message parity is not required. Review the translation against the pinned source, compare representative normalized outputs, and pass focused tests for the safety and lifecycle requirements below. Historical branch-ledger completion is not a release gate. Test supported native branches and safety boundaries rather than reproducing unreachable upstream paths. The public usage event API remains out of [scope](#scope-and-current-state). An unqualified scoped replacement must not be selected or described as Go-only; never claim full upstream parity. Record intentional behavior changes; an unresolved host capability or product decision must not silently remove a feature.

Qualification includes the architecture's threat cases and operational outcomes: credential/origin changes during a request, redirects, stale publication, settings-save failure, unknown quota, bounded background failure diagnostics and interrupted shutdown. Verify the renderer remains cache-only and errors never disclose account identifiers, secrets or raw provider bodies. Return clear structured failures and preserve durable partial-success information. Document supported native result schemas rather than requiring exact upstream fields or messages.

Do not change consumer deployment selection, live credentials, billing state or the shared Hermes store as part of this port. Consumer wiring remains in dotfiles. Committing, pushing and activating a replacement require their own authorization.
