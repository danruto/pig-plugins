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
printf '%s\n' '<!-- GENERATED FROM CODE — DO NOT EDIT. Run bash scripts/hermes-settings-reference.sh > docs/reference/hermes-settings.md to regenerate. -->' '' '# Native Hermes settings' '' 'JSON keys and allowed strings are extracted as struct tags. Fields not assigned in the defaults retain their Go zero value. See the configuration migration guide for validation and path rules.' '' '## Accepted input shape' '' '```go'
go doc -src ./internal/config Settings
go doc -src ./internal/config SessionRoots
printf '%s\n' '```' '' '## Defaults' '' '```go'
go doc -u -src ./internal/config defaults
printf '%s\n' '```'
