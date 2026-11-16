#!/usr/bin/env python3
"""Assert the sandbox profile builds and labels the three supported cross targets."""

import os
import subprocess
from pathlib import Path

project = Path(__file__).resolve().parent.parent
TARGETS = ("linux/amd64", "linux/arm64", "darwin/arm64")
PROFILE_TIMEOUT = 1800


def sdk_path() -> str:
    configured = os.environ.get("PIG_STATUS_SDK")
    if configured:
        return configured
    result = subprocess.run(["pig", "reload", "--sdk-path"], capture_output=True, text=True, timeout=180)
    assert result.returncode == 0, f"pig reload --sdk-path exited {result.returncode}: {result.stderr.strip()}"
    return result.stdout.strip()


def sandbox_output() -> str:
    environment = dict(os.environ, PIG_STATUS_SDK=sdk_path(), GOMAXPROCS="2", GOFLAGS="-p=2")
    result = subprocess.run(
        ["bash", str(project / "scripts/check.sh"), "sandbox"],
        cwd=project,
        env=environment,
        capture_output=True,
        text=True,
        timeout=PROFILE_TIMEOUT,
    )
    assert result.returncode == 0, (
        f"check.sh sandbox exited {result.returncode}:\n{result.stdout[-4000:]}\n{result.stderr[-2000:]}"
    )
    return result.stdout


def main() -> int:
    lines = set(sandbox_output().splitlines())
    missing = [target for target in TARGETS if f"cross-build: {target} ok (cross-build, not runtime evidence)" not in lines]
    if missing:
        print(f"FAIL cross-build labels: the sandbox profile did not label {missing}")
        return 1
    print(f"cross-build: {len(TARGETS)} of {len(TARGETS)} targets built and labelled as cross-build, not runtime evidence")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
