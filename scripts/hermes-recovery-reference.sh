#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root/extensions/pig-hermes"
modfile=${1:-}
if [[ -z $modfile ]]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  sdk=$(HOME="$tmp/home" PIG_HOME="$tmp/pig" pig reload --sdk-path)
  cp go.mod "$tmp/hermes.go.mod"
  cp go.sum "$tmp/hermes.go.sum"
  modfile="$tmp/hermes.go.mod"
  go mod edit -modfile="$modfile" -replace="github.com/MichaelKinsy/PiG/extensions/sdk=$sdk"
fi
export GOFLAGS="${GOFLAGS:-} -modfile=$modfile -modcacherw"
printf '%s\n' '<!-- GENERATED FROM CODE — DO NOT EDIT. Run bash scripts/hermes-recovery-reference.sh > docs/reference/hermes-recovery.md to regenerate. -->' '' '# Staged Hermes recovery API' '' 'Signatures are extracted from the store package. See the staged recovery guide for ownership, accepted bounds, outcomes and qualification limits.' '' '```go'
for symbol in OpenDatabase Database RecoveryOptions RecoveryResult RecoveryError DatabaseStatus ErrRecoveryCircuit NewManagedMemory Memory MaintenanceResult; do
  go doc ./internal/store "$symbol"
done
printf '%s\n' '```' '' '## Recovery defaults and validation' '' '```go'
go doc -u -src ./internal/store resolveRecoveryOptions
printf '%s\n' '```'
