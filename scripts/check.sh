#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export HOME="$tmp/home" PIG_HOME="$tmp/pig" PIG_CODING_AGENT_DIR="$tmp/pig-agent"
export PI_CODING_AGENT_DIR="$tmp/pi-agent" XDG_CONFIG_HOME="$tmp/config" TMPDIR="$tmp/tmp"
mkdir -p "$HOME" "$PIG_HOME" "$PIG_CODING_AGENT_DIR" "$PI_CODING_AGENT_DIR" "$XDG_CONFIG_HOME" "$TMPDIR"
go telemetry off
sdk=$(pig reload --sdk-path)
cd "$root"
npm run prepare:compaction
node --import tsx --test test/test-compaction-payload.mjs test/test-compaction-integration.mjs
npm run test:hermes-publication
(
  cd probes/hermes-sqlite
  go build -modcacherw -o "$tmp/publication-peer" ./publication-peer
  go vet ./publication-peer
  go test ./publication-peer
)
python3 probes/hermes-closure.py --check
if [[ -n ${HERMES_PACKAGE:-} ]]; then
  python3 probes/hermes-closure.py --verify-installed
fi
python3 probes/test-hermes-closure.py -v -k test_h1_check -k test_h1_retained
node --import tsx scripts/usage-reference.mjs --check
node --import tsx scripts/usage-billing-reference.mjs --check
node --import tsx scripts/usage-regional-reference.mjs --check
node --import tsx scripts/usage-consumer-reference.mjs --check
node --test test/test-usage.mjs test/test-docs.mjs
npm run typecheck
(
  cd extensions/pig-hermes
  cp go.mod "$tmp/hermes.go.mod"
  cp go.sum "$tmp/hermes.go.sum"
  go mod edit -modfile="$tmp/hermes.go.mod" -replace="github.com/MichaelKinsy/PiG/extensions/sdk=$sdk"
  gofmt -l . | grep . && exit 1
  go build -modcacherw -modfile="$tmp/hermes.go.mod" ./...
  go vet -modcacherw -modfile="$tmp/hermes.go.mod" ./...
  export HERMES_RECOVERY_SCRATCH="${HERMES_RECOVERY_SCRATCH:-$tmp/hermes-recovery}"
  mkdir -p "$HERMES_RECOVERY_SCRATCH"
  HERMES_PACKAGE="${HERMES_PUBLICATION_SOURCE:-${HERMES_PACKAGE:-}}" \
    go test -modcacherw -modfile="$tmp/hermes.go.mod" -count=1 -race -json ./...
  bash "$root/scripts/hermes-settings-reference.sh" "$tmp/hermes.go.mod" > "$tmp/hermes-settings.md"
  diff -u "$root/docs/reference/hermes-settings.md" "$tmp/hermes-settings.md"
  bash "$root/scripts/hermes-recovery-reference.sh" "$tmp/hermes.go.mod" > "$tmp/hermes-recovery.md"
  diff -u "$root/docs/reference/hermes-recovery.md" "$tmp/hermes-recovery.md"
)
cd extensions/pig-status
cp go.mod "$tmp/go.mod"
go mod edit -modfile="$tmp/go.mod" -replace="github.com/MichaelKinsy/PiG/extensions/sdk=$sdk"
gofmt -l . | grep . && exit 1
go build -modfile="$tmp/go.mod" -o "$tmp/pig-status" .
go vet -modfile="$tmp/go.mod" ./...
go test -modfile="$tmp/go.mod" -race ./...
cd "$root"
PIG_STATUS_PACKAGE="$root" node --test test/test-discovery.mjs
python3 test/check-footer.py
