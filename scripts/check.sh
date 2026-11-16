#!/usr/bin/env bash
set -euo pipefail
profile=${1:-full}
case "$profile" in
  full | sandbox | usage | go-lint-baseline) ;;
  *) echo "usage: check.sh [full|sandbox|usage|go-lint-baseline]" >&2; exit 2 ;;
esac
golangci_lint_version=2.15.0
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export HOME="$tmp/home" PIG_HOME="$tmp/pig" PIG_CODING_AGENT_DIR="$tmp/pig-agent"
export PI_CODING_AGENT_DIR="$tmp/pi-agent" XDG_CONFIG_HOME="$tmp/config" TMPDIR="$tmp/tmp"
mkdir -p "$HOME" "$PIG_HOME" "$PIG_CODING_AGENT_DIR" "$PI_CODING_AGENT_DIR" "$XDG_CONFIG_HOME" "$TMPDIR"
go telemetry off
if [[ $profile == sandbox ]]; then
  sdk=${PIG_STATUS_SDK:?sandbox profile needs PIG_STATUS_SDK}
else
  sdk=$(pig reload --sdk-path)
fi
status_dir="$root/extensions/pig-status"
native_dir="$status_dir/nativeusage"
modfile="$tmp/go.mod"
cp "$status_dir/go.mod" "$modfile"
go mod edit -replace="github.com/MichaelKinsy/PiG/extensions/sdk=$sdk" -replace="github.com/danruto/pig-plugins/extensions/pig-status/nativeusage=$native_dir" "$modfile"
native_modfile="$tmp/nativeusage.mod"
cp "$native_dir/go.mod" "$native_modfile"
go mod edit -replace="github.com/MichaelKinsy/PiG/extensions/sdk=$sdk" -replace="github.com/danruto/pig-plugins/extensions/pig-status=$status_dir" "$native_modfile"
mkdir "$tmp/lint"
workfile="$tmp/lint/go.work"
GOWORK="$workfile" go work init "$status_dir"
GOWORK="$workfile" go work edit -replace="github.com/MichaelKinsy/PiG/extensions/sdk=$sdk"
cd "$root"

step() {
  local profiles=$1 name=$2
  shift 2
  [[ " $profiles " == *" $profile "* ]] || return 0
  printf '==> %s: %s\n' "$name" "$*"
  "$@"
}

in_status() { (cd "$status_dir" && "$@"); }
in_nativeusage() { (cd "$native_dir" && "$@"); }

gofmt_clean() {
  local unformatted
  unformatted=$(gofmt -l "$status_dir")
  [[ -z $unformatted ]] || { printf 'gofmt needed:\n%s\n' "$unformatted" >&2; return 1; }
}

go_lint() {
  local found
  found=$(golangci-lint version --short)
  [[ $found == "$golangci_lint_version" ]] || { echo "golangci-lint $golangci_lint_version required, found $found; use the flake devShell" >&2; return 1; }
  in_status env GOWORK="$workfile" golangci-lint run --issues-exit-code=0 --output.json.path="$tmp/go-lint.json" ./...
  node scripts/go-lint-baseline.mjs "$1" "$tmp/go-lint.json"
}

step "full sandbox" prepare-compaction npm run prepare:compaction
step "full sandbox" compaction-payload node --import tsx --test test/test-compaction-payload.mjs
step "full" compaction-integration node --import tsx --test test/test-compaction-integration.mjs
step "full sandbox usage" usage-reference node --import tsx scripts/usage-reference.mjs --check
step "full sandbox usage" billing-reference node --import tsx scripts/usage-billing-reference.mjs --check
step "full sandbox usage" regional-reference node --import tsx scripts/usage-regional-reference.mjs --check
step "full sandbox usage" consumer-reference node --import tsx scripts/usage-consumer-reference.mjs --check
step "full sandbox usage" consumer-query-fixtures node --import tsx scripts/usage-consumer-query-fixtures.mjs --check
step "full sandbox usage" regional-query-fixtures node --import tsx scripts/usage-regional-query-fixtures.mjs --check
step "full sandbox" usage-docs-tests node --test test/test-docs.mjs
step "full sandbox" typecheck npm run typecheck
step "full sandbox" oxlint node_modules/.bin/oxlint --deny-warnings
step "full sandbox" gofmt gofmt_clean
step "full sandbox" go-fix-diff in_status go fix -modfile="$modfile" -diff ./...
step "full sandbox" go-build in_status go build -modfile="$modfile" -o "$tmp/pig-status" .
step "full sandbox" go-vet in_status go vet -modfile="$modfile" ./...
step "full" go-lint go_lint --check
step "go-lint-baseline" go-lint-baseline-write go_lint --write
step "full sandbox" go-test in_status env CGO_ENABLED=1 go test -modfile="$modfile" -race ./...
step "full sandbox" native-vet in_nativeusage go vet -modfile="$native_modfile" ./...
step "full sandbox" native-test in_nativeusage env CGO_ENABLED=1 go test -modfile="$native_modfile" -race ./...
step "usage" go-test-usage in_status env CGO_ENABLED=1 go test -modfile="$modfile" -race ./internal/usage
step "full" discovery env PIG_STATUS_PACKAGE="$root" python3 test/check-discovery.py
step "full" footer python3 test/check-footer.py
