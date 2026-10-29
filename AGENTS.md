# Project

Owned PiG plugins live here. This package targets PiG only; do not add Pi adapters or Pi-only tests. Personal configuration and host deployment selection belong in the consuming dotfiles repository.

- `extensions/pig-status/` is the native Go footer.
- `extensions/pig-status-usage.ts` composes the pinned upstream usage backend with `status/usage.ts`.
- Go-only status is the target, not the current runtime. Read `docs/designs/native-usage.md` before replacing the collector; retain its complete provider and command behavior until the native parity gates pass.
- `probes/` contains compatibility experiments, not installed extensions.
- Native Hermes is planned, not implemented. Read `docs/designs/pig-hermes.md` and the generated public-surface capture before starting it. Never register a partial replacement as full parity.

## Checks

Run `npm ci`, then `GOMAXPROCS=2 GOFLAGS=-p=2 npm test`.

Build the complete package with `nix build "path:$PWD#pig-status"`. The path form includes uncommitted files. Go modules use PiG's version-matched staged SDK; the check script supplies a temporary replacement rather than committing a machine path.

Hermes probes require `HERMES_PACKAGE` pointing to source code, not a memory directory. Reproduction commands and evidence limits are in `docs/compatibility/hermes.md`.

## Safety

All memory/database/session fixtures must use disposable roots. Do not copy personal memories, credentials or session history into this repository. Shared TypeScript/Go write compatibility and the complete parity gates must pass before native Hermes can use live data.

The footer renders only cached state. Do not add network, credential or history reads to its render callback. Claude native status is opaque text labelled `used`; do not parse it or claim its percentages mean `left`.
