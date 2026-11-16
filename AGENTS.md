# Project

Owned PiG plugins live here. This package targets PiG only; do not add Pi adapters or Pi-only tests. Personal configuration and host deployment selection belong in the consuming dotfiles repository.

- `extensions/pig-status/` is the native Go footer and the selected usage collector; `extensions/pig-status/nativeusage/` is its conventional factory.
- Go-only status is the target, not the current runtime. Read `docs/designs/native-usage.md` before changing the collector; retain its provider/account features and commands, and keep the scoped functional and safety checks passing. Exact upstream API, parser and message parity is not required.
- `probes/` contains compatibility experiments, not installed extensions.
- Port usage directly from the pinned open-source backend, module by module. Reuse useful fixtures and compare representative outputs; exhaustive branch capture and the historical pending ledger are not implementation or cutover gates. Keep `npm run test:usage-baseline` optional.

## Checks

Run checks inside the flake devShell (`nix develop "path:$PWD"`, or direnv); it supplies the pinned golangci-lint and Python. Run `npm ci`, then `GOMAXPROCS=2 GOFLAGS=-p=2 npm test`.

`npm run test:native-usage-host` is a separate target that needs a reviewed PiG host; see `scripts/check-native-usage.md`.

Build the complete package with `nix build "path:$PWD#pig-status"`. The path form includes uncommitted files. Go modules use PiG's version-matched staged SDK; the check script supplies a temporary replacement rather than committing a machine path.

## Safety

All fixtures must use disposable roots. Do not copy personal memories, credentials or session history into this repository. Live provider checks and activation require separate owner approval.

The footer renders only cached state. Do not add network, credential or history reads to its render callback. Claude native status is opaque text labelled `used`; do not parse it or claim its percentages mean `left`.
