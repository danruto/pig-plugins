#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
sdk=$(pig reload --sdk-path)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$root"
npm run prepare:compaction
node --import tsx --test test/test-compaction-payload.mjs test/test-compaction-integration.mjs
python3 probes/hermes-closure.py --check
if [[ -n ${HERMES_PACKAGE:-} ]]; then
  python3 probes/hermes-closure.py --verify-installed
fi
TMPDIR="$tmp" python3 probes/test-hermes-closure.py -v -k test_h1_check -k test_h1_retained
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
