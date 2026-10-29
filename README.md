# PiG plugins

Owned PiG extensions, separate from personal system configuration. This package targets PiG only.

**Available:** compact `pig-status` footer with integrated usage collection and corrected OpenAI server compaction. **Planned:** native Go Hermes. The package does not install or activate a partial memory replacement.

## Install from a checkout

Requires PiG 0.5.3, Go 1.26+, Node 24+ and npm.

```sh
npm ci
npm run prepare:compaction
pig install .
```

Status includes the pinned `@narumitw/pi-usage` backend internally. Do not also load that package as a separate PiG extension: it would duplicate commands and collection. Existing provider adapters, `/usage` and `/fast` remain available. The usage collector still uses Node; the footer is native Go.

For Claude quota, use the tested bridge v0.2.0 commit:

```sh
pig install git:github.com/balcsida/pig-claude-bridge@670c5a379a9843db82ea069aad840f71099afde2
```

Remove a previously selected bridge source so only one bridge is active. Use `/reload` or start a new session after changing extension selection.

## Server compaction

The package selects `extensions/pig-openai-server-compaction.ts` exactly once. Remove the separately selected upstream `pi-openai-server-compaction` package before enabling this entry; do not load both. Regular Pi configuration is outside this package.

PiG retains its native OpenAI and Codex providers and WebSocket/HTTP transport; this extension registers no custom provider. The pinned upstream compactor owns remote compaction requests, local summaries, opaque-history storage and session reconstruction. With no compatible remote state, outgoing native requests are left unchanged. Replay keeps the current request’s ordered instructions, tool declarations and configuration updates ahead of opaque remote history, instead of discarding transcript-anchored tools or replaying stale context. Ordinary original conversation history is replaced, not retained.

Lifecycle scripts remain disabled. After `npm ci`, explicitly run `npm run prepare:compaction` before loading the checkout or running `npm pack`. The upstream tarball is a development-only preparation input. Preparation verifies every upstream source hash and generates only the compaction-hook modules and license; extension loading only reads that copy. `npm test` and the Nix build prepare it automatically. No runtime download, installation or source patching occurs. The upstream global `~/.pi/agent/openai-server-compaction.json` and project `.pi/openai-server-compaction.json` locations and enabled/notify controls remain available. Optional continuation/store policy from the removed custom provider no longer applies.

Offline synthetic checks cover payload preservation, native-provider retention and PiG registration/lifecycle replay for both OpenAI and Codex. They do not establish live OpenAI acceptance, a native provider network round trip or model tool execution.

## Footer

- One row, with project/Git/quota/session on the left and context/model/thinking/tokens/cost/statuses on the right.
- Low-priority fields disappear on narrow terminals; quota is truncated rather than silently dropped.
- Input totals include cache reads and writes. Claude subscription sessions show `plan`, not misleading zero-dollar billing.
- Other providers' normalized percentages are labelled `left`.
- Claude's own native status is displayed unchanged with a `used` prefix. It includes reset times and per-model windows when available. No parsing of its display text is needed.
- Missing quota stays unknown. A valid Claude `/usage` reply can contain `rate_limits: null`; the bridge falls back to rate-limit events during normal turns.
- The renderer performs no network requests, credential reads or history scans.

The collector uses the backend's versioned `usage:query:v1` / `usage:changed:v1` contract. It rejects late responses after session/model changes and clears quota when its cache entry or window expires.

The target is a Go-only PiG package. The [native usage port plan](docs/designs/native-usage.md) preserves the current provider adapters, settings and commands before removing the TypeScript collector and Node dependency. That port is not implemented yet.

## Review design docs

From a checkout:

```sh
npm ci
npm run docs:dev
```

Open **http://127.0.0.1:4173/**. Use `PORT=4180 npm run docs:dev` for another port. Refresh after editing Markdown; stop with `Ctrl+C`.

To connect from another machine, run `HOST=0.0.0.0 npm run docs:dev` and open `http://<server-ip>:4173/`. This exposes the docs without authentication on all network interfaces; use only on a trusted network.

The local server lists all Markdown under `docs/` and uses Aqua Cutter **2.0.0** for diagrams. Its pinned, integrity-checked assets download on first run into `node_modules/.cache/pig-docs/`; later runs work offline. Browser requests stay local. No personal configuration is read or changed.

Aqua Cutter supports flowcharts and sequence diagrams, not C4, state or entity-relationship diagrams. Unsupported or rejected diagrams retain their source and a visible notice. All text and diagram sources remain readable if JavaScript or renderer loading fails.

## Check

```sh
npm test
```

This runs adapter checks, TypeScript checking, Go build/vet/race tests, real package discovery and a terminal smoke test at 120 and 80 columns. The terminal fixture sends no model prompt.

## Reproducible package

```sh
nix build "path:$PWD#pig-status"
pig install ./result --validate-only --json
```

The output contains the prebuilt native footer, status collector, prepared compactor and locked production dependencies. PiG supplies the Node compatibility runtime. Source installation builds the Go footer on first use; the Nix output needs no Go compiler at runtime.

Dotfiles should select this package and own personal configuration, not contain its implementation. During development, select the checkout. Use an immutable source/release pin after publication.

## Native Hermes

- [Delivery contract and PB work issues](https://github.com/danruto/pig-plugins/issues/1)
- [Implementation milestones](https://github.com/danruto/pig-plugins/milestones)
- [Native Hermes contract and checkpoints](docs/designs/pig-hermes.md)
- [Compatibility findings and reproduction](docs/compatibility/hermes.md)
- [Captured upstream public surface](docs/compatibility/hermes-surface.json)

Milestones and GitHub dependencies own delivery order and progress for native usage and Hermes. Repository design documents own behavior and safety contracts. Live Hermes activation is a separate owner-approval gate, not queued implementation work.

Start the first ready objective from this checkout:

```text
/pb:work 2 --prod
```

`.pb/issues-repo` selects the GitHub backlog. The routing roster uses native workers unless explicitly configured. `--prod` preserves the existing installed backend while its replacement is staged. Commits, pushes and live activation require separate authorization.

All compatibility probes use disposable roots. They must never be pointed at live memory or session databases. Passing feasibility probes does not establish full storage or feature parity.

See [third-party notices](THIRD_PARTY_NOTICES.md) for provenance and licensing.
