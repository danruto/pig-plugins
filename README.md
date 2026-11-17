# PiG plugins

Owned PiG extensions, separate from personal system configuration. This package targets PiG only.

**Available:** compact `pig-status` footer with integrated usage collection and corrected OpenAI server compaction.

## Install from a checkout

Requires PiG 0.7.0, Go 1.26+, Node 24+ and npm.

```sh
npm ci
npm run prepare:compaction
pig install .
```

Status collects usage natively in Go and registers its own `/usage` and `/fast` commands, with the provider adapters, billing-target selection and Codex reset flow. Do not also install `@narumitw/pi-usage` as a separate PiG extension: it would register duplicate commands and collection. The package keeps it as a pinned development dependency only, for the source-reference scripts. Claude quota stays opaque text labelled `used`.

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

This is a Go-only PiG package. The [native usage port plan](docs/designs/native-usage.md) preserves the provider adapters, settings and commands in the native Go collector, which the package now selects; the TypeScript collector is removed. Nix packaging is a native Go build with no Node runtime requirement. The port translates the pinned open-source modules directly, with focused native tests and representative output comparisons rather than exhaustive branch capture. The native transport and balance adapters are part of the selected native collector.

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

This runs adapter checks, pinned-source native usage comparisons, TypeScript checking and oxlint, Go formatting, `go fix -diff`, build/vet/race tests and golangci-lint, real package discovery and a terminal smoke test at 120 and 80 columns. The terminal fixture sends no model prompt. Use the flake devShell (`nix develop`, or direnv) for the pinned golangci-lint and Python.

`scripts/check.sh` holds the one check list. `npm test` runs every step; the Nix build runs the Go steps and the cross-builds; `npm run test:usage-native` checks only the native usage slice; `npm run test:package` qualifies a built output (see [Reproducible package](#reproducible-package)).

golangci-lint reports issues that predate the gate in `test/go-lint-baseline.json`. The check fails on any issue outside it, and on a baseline entry that no longer occurs. After fixing issues, run `npm run lint:go-baseline` and review the diff. Do not add new issues to the baseline or raise a limit.

`npm run test:native-usage-host` runs the staged native usage gate against a reviewed PiG host; see [`scripts/check-native-usage.md`](scripts/check-native-usage.md). Without that host it exits 2.

`npm run test:usage-baseline` retains the optional historical capture suite; its branch ledger is not a prerequisite for the source-led port.

## Reproducible package

```sh
nix build "path:$PWD#pig-status"
pig install ./result --validate-only --json
```

The build compiles the Go footer natively and prepares the pinned compactor hooks from their fetched tarball; Node is a build-time input only. The output holds `bin/pig-status`, the compactor extension, `compaction/payload.ts`, `compaction/generated/`, the licence, the notices, and a `package.json` naming `bin/pig-status`. It ships no `node_modules` and no Node runtime in its closure; PiG's own closure supplies the Node runtime the compactor runs on.

Qualify the built output inside the flake devShell:

```sh
nix build "path:$PWD#pig-status"
PIG_STATUS_PACKAGE=$PWD/result bash scripts/check.sh package
```

That profile checks the package layout, `pig install --validate-only`, discovery, command registration, the 120/80-column footer and its shutdown, then measures startup, resident memory and shutdown. Every step runs with no `node` on `PATH`.

Runtime evidence exists for `x86_64-linux` only. `aarch64-linux` and `aarch64-darwin` are cross-builds (`GOOS`/`GOARCH`, `CGO_ENABLED=0`) and are reported as such, never as runtime evidence. All of these are fixture checks: live-provider qualification is separate (`npm run test:native-usage-host`, with owner approval).

Source installation builds the Go footer on first use; the Nix output needs no Go compiler at runtime.

Dotfiles should select this package and own personal configuration, not contain its implementation. During development, select the checkout. Use an immutable source/release pin after publication.

See [third-party notices](THIRD_PARTY_NOTICES.md) for provenance and licensing.
