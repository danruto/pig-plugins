#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
node --test probes/usage/ledger-artifact.test.mjs probes/usage/branch-ledger.test.mjs probes/usage/branch-ledger-patterns.test.mjs probes/usage/branch-ledger-identity.test.mjs probes/usage/settings-auth-evidence.test.mjs probes/usage/settings-auth.test.mjs probes/usage/dist-attribution.test.mjs probes/usage/lifecycle-evidence.test.mjs probes/usage/response-barrier.test.mjs probes/usage/provenance.test.mjs probes/usage/named-gaps.test.mjs probes/usage/counter-replay.test.mjs
node probes/usage-baseline.mjs --check
sdk=$(pig reload --sdk-path)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd probes/usage/bridge
cp go.mod "$tmp/go.mod"
go mod edit -modfile="$tmp/go.mod" -replace="github.com/MichaelKinsy/PiG/extensions/sdk=$sdk"
gofmt -l . | grep . && exit 1
go vet -modfile="$tmp/go.mod" ./...
cd ../tls
gofmt -l . | grep . && exit 1
go vet ./...
