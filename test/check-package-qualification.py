#!/usr/bin/env python3
"""Assert the package qualification profile runs every packaged check and that the installed layout was checked."""

import os
import re
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path

project = Path(__file__).resolve().parent.parent
CHECKS = ["package-layout", "package-validate", "package-discovery", "package-footer", "package-measure"]
STEP = re.compile(r"^==> ([a-z-]+):", re.MULTILINE)
RUN_TIMEOUT = 900

Check = Callable[[Path], list[str]]


def package_path(argv: list[str]) -> Path:
    if len(argv) > 1:
        return Path(argv[1]).resolve()
    configured = os.environ.get("PIG_STATUS_PACKAGE")
    if not configured:
        raise SystemExit("usage: check-package-qualification.py <package-dir> (or set PIG_STATUS_PACKAGE)")
    return Path(configured).resolve()


def run_profile(environment: dict[str, str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", str(project / "scripts/check.sh"), "package"],
        cwd=project,
        env=environment,
        capture_output=True,
        text=True,
        timeout=RUN_TIMEOUT,
    )


def without_package() -> dict[str, str]:
    environment = dict(os.environ)
    environment.pop("PIG_STATUS_PACKAGE", None)
    return environment


def refusal_problems(package: Path) -> list[str]:
    result = run_profile(without_package())
    if result.returncode == 0:
        return ["check.sh package exited 0 with no package configured"]
    if "PIG_STATUS_PACKAGE" not in result.stderr:
        return [f"check.sh package refused with {result.stderr.strip()!r}, expected a usage message naming PIG_STATUS_PACKAGE"]
    return []


def profile_problems(package: Path) -> list[str]:
    result = run_profile(dict(os.environ, PIG_STATUS_PACKAGE=str(package)))
    if result.returncode != 0:
        return [f"check.sh package exited {result.returncode}: {(result.stderr or result.stdout).strip()[-2000:]}"]
    steps = STEP.findall(result.stdout)
    if steps != CHECKS:
        return [f"the profile ran {steps}, expected {CHECKS}"]
    return []


def install_check_problems(package: Path) -> list[str]:
    result = subprocess.run(["nix", "log", str(package)], capture_output=True, text=True, timeout=300)
    if result.returncode != 0:
        return [f"nix log {package} failed, so the installed layout check cannot be proven: {result.stderr.strip()[-500:]}"]
    if "installCheckPhase" not in result.stdout:
        return [f"the build log of {package} records no installCheckPhase"]
    return []


STEP_CHECKS: list[tuple[str, Check]] = [
    ("package required", refusal_problems),
    ("packaged checks", profile_problems),
    ("install check", install_check_problems),
]


def main() -> int:
    package = package_path(sys.argv)

    failed = 0
    for name, check in STEP_CHECKS:
        problems = check(package)
        if problems:
            failed += 1
            print(f"FAIL {name}: {'; '.join(problems)}")
        else:
            print(f"PASS {name}")
    if failed:
        print(f"package qualification: {failed} of {len(STEP_CHECKS)} checks failed for {package}")
        return 1
    print(f"package qualification: {len(STEP_CHECKS)} of {len(STEP_CHECKS)} checks passed for {package}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
