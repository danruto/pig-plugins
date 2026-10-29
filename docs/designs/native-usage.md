# Native Go usage collection

## Scope and current state

This repository targets PiG only. The target is a Go-only status package with no Node runtime requirement. Pi adapters and Pi-only tests are outside its scope.

The current package is still mixed: `extensions/pig-status/` renders the footer in Go; `extensions/pig-status-usage.ts` loads `@narumitw/pi-usage` **0.68.3** and `status/usage.ts`. Keep that collector selected until the complete native replacement passes the gates below. Removing it first would remove provider adapters, settings and commands.

The approved immediate change removes the legacy Pi adapter and its dedicated tests. The native usage port is staged, not implemented. Native Hermes remains a separate project under [its existing contract](pig-hermes.md); sharing a store with an independent Pi installation does not require shipping Pi support here.

The [architecture](architecture.md) defines context/container boundaries, collector ownership, the refresh sequence, worker lifecycle, trust boundaries and operational signals. [ADR 0001](../adr/0001-native-runtime-ownership.md) records alternatives and the staged native-runtime decision. Status does not inherit Hermes's storage dependencies or release schedule.

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

Source evidence is in the pinned backend's `src/query.ts`, `src/providers/`, `src/usage.ts`, `src/settings.ts`, `src/usage-api.ts` and `src/codex-*.ts`. Its README and `docs/` describe provider semantics, operations, settings and the read-only event API. Inspect these alongside executable captures before implementing each checkpoint.

Derive a branch-to-fixture inventory from provider dispatch, host-auth resolution, settings parsing, command registration and public API consumers. Each row names its source symbol/digest, inputs, expected result/side effects, checkpoint and evidence. Include defaults, invalid inputs, optional sections and failure paths. The 18-provider list is an inventory check, not proof of complete adapter behavior.

## Behavior to preserve

### Collection and presentation

- Keep quota, balance, spend, currencies, billing targets and provider-specific model groups distinct. Unknown is not zero. Do not derive subscription quota from token counts.
- Preserve current-provider, another-configured-provider and all-configured-provider menu actions. Cross-provider queries require explicit user selection. Preserve partial results and bounded concurrency.
- Preserve billing-target discovery and selection, including multi-account Fireworks behavior. Never aggregate targets or silently pick a different account.
- Keep automatic refresh, cache expiry, reset countdowns, failure backoff and bounded account caches. xAI remains menu-only, without scheduled collection.
- Preserve `/usage` and `/fast` in terminal and remote procedure call (RPC) modes, their no-argument behavior, settings interaction and failure messages. Do not register duplicate commands during migration.
- Preserve native OpenAI's authentication-only default and optional Codex companion usage. Companion collection stays opt-in; never send the native OpenAI token to ChatGPT backend endpoints or claim the two logins independently prove shared account identity.
- Preserve Codex Fast capability discovery, conservative fallback, request-tier rewriting and matching assistant-message cost correction. A saved toggle affects subsequent payload hooks, not requests already sent.
- Preserve Codex reset discovery, fresh OAuth/account matching, explicit confirmation, stable redemption identity for safe retry, and outcome reporting. Do not make live redemption requests in tests.
- Preserve settings validation, defaults, unknown fields, legacy target selection migration, ordered private atomic saves and refusal to overwrite invalid files. Keep existing user settings readable without copying Pi configuration.
- Preserve the structured usage API's query, capabilities and invalidation behavior. Characterize callback, cancellation and session-identity behavior across the Go bridge before relying on equivalence.

### Safety and lifecycle

- Resolve the host's effective provider and selected-model authentication. Validate official origins, explicit authorization overrides and fresh credential identity before provider requests and publication. Do not bypass host resolution by reading an unrelated credential file.
- Preserve OAuth candidate readiness and uniquely matched runtime-account checks, including provider-specific restrictions and regional credential isolation.
- Reject redirects before credentials can be forwarded. Bound response size, total query time and concurrency. Sanitize terminal output and redact credentials in errors.
- Fence results by session, complete model configuration, account, target and settings generation. Old requests cannot publish into a successor's state or cache.
- Own workers, timers and cancellation through session startup, shutdown, reload and replacement. Join owned work; do not detach a request-scoped SDK context into a goroutine.
- Keep network requests, credentials and history reads outside the footer render callback. Preserve cache-inclusive token totals, cost, subscription labelling, narrow layouts and opaque Claude bridge status labelled `used`.

## Implementation checkpoints

Before checkpoint 2, use real PiG processes to characterize the existing public usage protocol's callback acceptance, cancellation, session object identity, result timing, invalidation and capabilities behavior at the Go/Node boundary. Inspect `src/usage-api-types.ts`, `src/usage-api.ts` and `status/usage.ts`; a plain JSON-shaped substitute cannot establish reference/callback parity. Record the native adaptation and its failure modes. If the bridge cannot preserve the contract, stop and request a decision rather than introducing a new host API or silently removing the public API.

1. [ ] Capture the baseline in disposable PiG roots: registration, commands, settings, normalized reports, request boundaries, failure behavior and lifecycle sequences. Use synthetic credentials and local provider fixtures. Retain portable expected data with source fingerprints; never retain personal credentials or session history.
2. [ ] Port provider normalization and guarded HTTP transport with fixtures for all 18 providers. Exercise malformed data, optional-section failures, origin rejection, redirect rejection and secret-safe errors. Compare against the frozen backend, including units and timestamp conversions.
3. [ ] Port host-auth integration, account/target selection, cache ownership, automatic refresh and the structured usage API. Prove that model/account/settings changes, cancellation and expiry reject stale publications through real PiG processes.
4. [ ] Port `/usage`, settings, `/fast` and reset workflows. Prove terminal/RPC behavior, persistence and rollback on failure, request-tier and cost attribution, confirmation and safe reset retry with local fixtures only.
5. [ ] Integrate native collection with the footer. Use conventional Go factories if the package is to support fusion; the current executable footer is not a fusible factory. Keep collection separate from rendering and SQLite/Hermes dependencies out of status.
6. [ ] Replace package membership atomically after parity passes. Remove the TypeScript collector, its dependency and obsolete adapter tests only when their behavior has native coverage. Replace Node-dependent status checks and the Claude test fixture; retain package metadata needed for PiG discovery.
7. [ ] Switch Nix packaging from the npm build to a native Go build. Verify source installation, prebuilt package discovery, validation, 120/80-column terminal rendering, clean shutdown and supported Linux/macOS targets without Node. Measure matched startup, resident memory and shutdown; distinguish fixture proof from live-provider qualification.

## Cutover gate

All provider and command behavior above must have executable evidence before removing the existing backend. Every consumer-inventory row must have passing evidence; no behavioral branch is covered merely because its provider ID or command name exists. A partial port must not be installed as a full replacement or described as Go-only. If a behavior cannot cross the Go bridge unchanged, record the gap and obtain an explicit decision rather than silently dropping it.

Qualification includes the architecture's threat cases and operational outcomes: credential/origin changes during a request, redirects, stale publication, settings-save failure, unknown quota, bounded background failure diagnostics and interrupted shutdown. Verify the renderer remains cache-only and errors never disclose account identifiers, secrets or raw provider bodies. Keep original command/query failure fields and messages; extra diagnostics do not alter the public result schema.

Do not change consumer deployment selection, live credentials, billing state or the shared Hermes store as part of this port. Consumer wiring remains in dotfiles. Committing, pushing and activating a replacement require their own authorization.
