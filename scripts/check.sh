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
