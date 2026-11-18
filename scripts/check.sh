#!/usr/bin/env bash
set -euo pipefail
profile=${1:-full}
case "$profile" in
  full | sandbox | usage | go-lint-baseline | ts-lint-baseline | package) ;;
  *) echo "usage: check.sh [full|sandbox|usage|go-lint-baseline|ts-lint-baseline|package]" >&2; exit 2 ;;
esac
golangci_lint_version=2.15.0
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export HOME="$tmp/home" PIG_HOME="$tmp/pig" PIG_CODING_AGENT_DIR="$tmp/pig-agent"
export PI_CODING_AGENT_DIR="$tmp/pi-agent" XDG_CONFIG_HOME="$tmp/config" TMPDIR="$tmp/tmp"
mkdir -p "$HOME" "$PIG_HOME" "$PIG_CODING_AGENT_DIR" "$PI_CODING_AGENT_DIR" "$XDG_CONFIG_HOME" "$TMPDIR"
package_dir='' python_bin='' pig_bin=''
status_dir="$root/extensions/pig-status"
native_dir="$status_dir/nativeusage"
modfile="$tmp/go.mod"
native_modfile="$tmp/nativeusage.mod"
workfile="$tmp/lint/go.work"
if [[ $profile == package ]]; then
  package_dir=${PIG_STATUS_PACKAGE:-}
  [[ -n $package_dir ]] || { echo "usage: PIG_STATUS_PACKAGE=<built package path> check.sh package" >&2; exit 2; }
  package_dir=$(cd "$package_dir" && pwd)
  python_bin=$(type -P python3) || { echo "check.sh package: python3 not found on PATH" >&2; exit 1; }
  pig_bin=$(type -P pig) || { echo "check.sh package: pig not found on PATH" >&2; exit 1; }
  pig_bin=$(readlink -f "$pig_bin")
elif [[ $profile != ts-lint-baseline ]]; then
  go telemetry off
  if [[ $profile == sandbox ]]; then
    sdk=${PIG_STATUS_SDK:?sandbox profile needs PIG_STATUS_SDK}
  else
    sdk=$(pig reload --sdk-path)
  fi
  cp "$status_dir/go.mod" "$modfile"
  go mod edit -replace="github.com/MichaelKinsy/PiG/extensions/sdk=$sdk" -replace="github.com/danruto/pig-plugins/extensions/pig-status/nativeusage=$native_dir" "$modfile"
  cp "$native_dir/go.mod" "$native_modfile"
  go mod edit -replace="github.com/MichaelKinsy/PiG/extensions/sdk=$sdk" -replace="github.com/danruto/pig-plugins/extensions/pig-status=$status_dir" "$native_modfile"
  mkdir -p "$tmp/lint"
  GOWORK="$workfile" go work init "$status_dir"
  GOWORK="$workfile" go work edit -replace="github.com/MichaelKinsy/PiG/extensions/sdk=$sdk"
fi
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

without_node() {
  local dir filtered
  local -a dirs=() kept=()
  IFS=: read -ra dirs <<<"$PATH"
  for dir in "${dirs[@]}"; do
    [[ -x $dir/node ]] || kept+=("$dir")
  done
  filtered="$tmp/no-node-bin"
  for dir in "${kept[@]}"; do
    filtered="$filtered:$dir"
  done
  mkdir -p "$tmp/no-node-bin"
  ln -sfn "$pig_bin" "$tmp/no-node-bin/pig"
  if env PATH="$filtered" bash -c 'command -v node' >/dev/null; then
    echo "without_node: node is still reachable on the filtered PATH" >&2
    return 1
  fi
  env PATH="$filtered" "$@"
}

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
  node scripts/lint-baseline.mjs "$1" go "$tmp/go-lint.json"
}

ts_lint() {
  local status
  set +e
  node_modules/.bin/oxlint --format json >"$tmp/ts-lint.json"
  status=$?
  set -e
  case $status in
    0 | 1) ;;
    *) echo "oxlint exited $status; expected 0 (clean) or 1 (diagnostics reported)" >&2; return 1 ;;
  esac
  node -e 'const fs = require("node:fs"); const config = JSON.parse(fs.readFileSync(".oxlintrc.json", "utf8")); const rules = ["max-lines-per-function", "max-statements", "complexity", "max-depth", "max-params", "max-nested-callbacks"]; const limits = rules.map((rule) => `${rule}=${config.rules[rule][1]?.max ?? config.rules[rule][1]}`); console.log(`oxlint plugins: ${config.plugins.join(",")}; limits: ${limits.join(" ")}`);'
  node scripts/lint-baseline.mjs "$1" ts "$tmp/ts-lint.json"
}

cross_build() {
  local target os arch artifact
  for target in linux/amd64 linux/arm64 darwin/arm64; do
    os=${target%%/*}
    arch=${target##*/}
    artifact=$tmp/cross-$os-$arch
    in_status env CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -modfile="$modfile" -o "$artifact" .
    if [[ ! -s $artifact ]]; then
      echo "cross-build: $os/$arch produced no binary at $artifact" >&2
      return 1
    fi
    printf 'cross-build: %s/%s ok (cross-build, not runtime evidence)\n' "$os" "$arch"
  done
}

step "full" prepare-compaction node scripts/prepare-compaction.mjs
step "full" compaction-payload node --import tsx --test test/test-compaction-payload.mjs
step "full" compaction-integration node --import tsx --test test/test-compaction-integration.mjs
step "full usage" usage-reference node --import tsx scripts/usage-reference.mjs --check
step "full usage" billing-reference node --import tsx scripts/usage-billing-reference.mjs --check
step "full usage" regional-reference node --import tsx scripts/usage-regional-reference.mjs --check
step "full usage" consumer-reference node --import tsx scripts/usage-consumer-reference.mjs --check
step "full usage" consumer-query-fixtures node --import tsx scripts/usage-consumer-query-fixtures.mjs --check
step "full usage" regional-query-fixtures node --import tsx scripts/usage-regional-query-fixtures.mjs --check
step "full" usage-docs-tests node --test test/test-docs.mjs
step "full" typecheck npm run typecheck
step "full" lint-baseline-tests node --test test/test-lint-baseline.mjs
step "full" ts-lint ts_lint --check
step "ts-lint-baseline" ts-lint-baseline-write ts_lint --write
step "full sandbox" gofmt gofmt_clean
step "full sandbox" go-fix-diff in_status go fix -modfile="$modfile" -diff ./...
step "full sandbox" go-build in_status go build -modfile="$modfile" -o "$tmp/pig-status" .
step "full sandbox" go-vet in_status go vet -modfile="$modfile" ./...
step "full sandbox" cross-build cross_build
step "full" go-lint go_lint --check
step "go-lint-baseline" go-lint-baseline-write go_lint --write
step "full sandbox" go-test in_status env CGO_ENABLED=1 go test -modfile="$modfile" -race ./...
step "full sandbox" native-vet in_nativeusage go vet -modfile="$native_modfile" ./...
step "full sandbox" native-test in_nativeusage env CGO_ENABLED=1 go test -modfile="$native_modfile" -race ./...
step "usage" go-test-usage in_status env CGO_ENABLED=1 go test -modfile="$modfile" -race ./internal/usage
step "full" discovery env PIG_STATUS_PACKAGE="$root" python3 test/check-discovery.py
step "full" footer python3 test/check-footer.py
step "package" package-layout without_node "$python_bin" test/check-package.py "$package_dir"
step "package" package-validate without_node pig install "$package_dir" --validate-only --json
step "package" package-discovery without_node env PIG_STATUS_PACKAGE="$package_dir" "$python_bin" test/check-discovery.py
step "package" package-footer without_node env PIG_STATUS_PACKAGE="$package_dir" "$python_bin" test/check-footer.py
step "package" package-measure without_node "$python_bin" test/check-measure.py "$package_dir"
