#!/usr/bin/env bash
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${HERMES_RECOVERY_SCRATCH:?Set an objective19 disposable scratch directory}"
mkdir -p "$HERMES_RECOVERY_SCRATCH"/{tmp,pids,reports}
export HERMES_RECOVERY_SCRATCH="$(cd "$HERMES_RECOVERY_SCRATCH" && pwd)"
export TMPDIR="$HERMES_RECOVERY_SCRATCH/tmp"
export GOMAXPROCS="${GOMAXPROCS:-2}"
export GOFLAGS="${GOFLAGS:--p=2} -modcacherw"
printf 'running-scoped-runner\n' > "$HERMES_RECOVERY_SCRATCH/pids/$$"
trap 'printf "joined-scoped-runner\\n" > "$HERMES_RECOVERY_SCRATCH/pids/$$"' EXIT
cd "$repo/extensions/pig-hermes"
set +e
go test -json -count=1 -timeout=180s ./acceptance/recovery > "$HERMES_RECOVERY_SCRATCH/reports/go-test.json" 2> "$HERMES_RECOVERY_SCRATCH/reports/go-test.stderr" &
test_pid=$!
printf 'running-go-test\n' > "$HERMES_RECOVERY_SCRATCH/pids/$test_pid"
wait "$test_pid"
result=$?
printf 'joined-go-test\n' > "$HERMES_RECOVERY_SCRATCH/pids/$test_pid"
set -e
printf '%s\n' "$result" > "$HERMES_RECOVERY_SCRATCH/reports/exit-code.txt"
printf 'runner report: %s/reports/go-test.json\nexit: %s\n' "$HERMES_RECOVERY_SCRATCH" "$result"
exit "$result"
