#!/usr/bin/env python3
"""Assert the startup, memory and shutdown record a built package produces, its comparison identity, and the shutdown wait it measures."""

import importlib.util
import json
import os
import platform
import re
import signal
import subprocess
import sys
import tempfile
import time
import types
from collections.abc import Callable
from pathlib import Path

project = Path(__file__).resolve().parent.parent
MEASUREMENTS = project / "docs/compatibility/usage/measurements.json"
TOOL = project / "scripts/measure-status.py"
DOCUMENT_KEYS = {"package", "host", "samples", "median", "retained", "comparison"}
HOST_KEYS = {"system", "pig"}
SAMPLE_KEYS = {"startup", "rss_kib", "shutdown", "loadavg"}
MEDIAN_KEYS = {"startup", "rss_kib", "shutdown"}
POSITIVE_METRICS = ("startup", "rss_kib")
NON_NEGATIVE_METRICS = ("shutdown", "loadavg")
ARCH_ALIASES = {"aarch64": "arm64", "x86_64": "amd64", "x64": "amd64"}
COMPOSITION_PREFIX = "$PACKAGE/"
TOOLCHAIN_IDENTITY = re.compile(r"probeSha256|identity\s?protocol|toolchain identity", re.IGNORECASE)
RUN_TIMEOUT = 300
DESCENDANT_LIFETIME = "2.0"
DESCENDANT_LAUNCHER = (
    "import subprocess, sys\n"
    f"print(subprocess.Popen([sys.executable, '-c', 'import time; time.sleep({DESCENDANT_LIFETIME})']).pid, flush=True)\n"
)

Check = Callable[[Path, dict[str, object]], list[str]]


def package_path(argv: list[str]) -> Path:
    if len(argv) > 1:
        return Path(argv[1]).resolve()
    configured = os.environ.get("PIG_STATUS_PACKAGE")
    if not configured:
        raise SystemExit("usage: check-measure.py <package-dir> (or set PIG_STATUS_PACKAGE)")
    return Path(configured).resolve()


def measure_document(package: Path) -> dict[str, object]:
    result = subprocess.run(
        [sys.executable, str(TOOL), str(package), "--samples", "1"],
        capture_output=True,
        text=True,
        timeout=RUN_TIMEOUT,
    )
    assert result.returncode == 0, f"measure-status.py exited {result.returncode}: {result.stderr.strip()}"
    document = json.loads(result.stdout)
    assert isinstance(document, dict), document
    return document


def launch_failure_problems(package: Path, document: dict[str, object]) -> list[str]:
    with tempfile.TemporaryDirectory(prefix="pig-status-measure-stub-") as stub:
        executable = Path(stub) / "pig"
        executable.write_text("#!/bin/sh\nexit 3\n", encoding="utf-8")
        executable.chmod(0o755)
        environment = dict(os.environ, PATH=f"{stub}:{os.environ['PATH']}")
        result = subprocess.run(
            [sys.executable, str(TOOL), str(package), "--samples", "1"],
            env=environment,
            capture_output=True,
            text=True,
            timeout=RUN_TIMEOUT,
        )
    problems = [] if result.returncode != 0 else ["a launch that never answers get_commands exited 0"]
    problems.extend(
        ["a failed launch left stderr empty"] if not result.stderr.strip() else []
    )
    problems.extend(
        [f"a failed launch printed a document: {result.stdout.strip()[:200]!r}"] if result.stdout.strip() else []
    )
    return problems


def magnitude(record: dict[str, object], key: str) -> float | None:
    value = record.get(key)
    if isinstance(value, bool) or not isinstance(value, int | float):
        return None
    return float(value)


def positive(record: dict[str, object], key: str) -> bool:
    value = magnitude(record, key)
    return value is not None and value > 0


def non_negative(record: dict[str, object], key: str) -> bool:
    value = magnitude(record, key)
    return value is not None and value >= 0


def package_problem(package: Path, document: dict[str, object]) -> list[str]:
    reported = document.get("package")
    return [] if reported == str(package) else [f"package is {reported!r}, expected {str(package)!r}"]


def host_problems(package: Path, document: dict[str, object]) -> list[str]:
    host = document.get("host")
    if not isinstance(host, dict):
        return [f"host is {host!r}, expected an object"]
    expected_system = f"{platform.system()}/{platform.machine()}"
    version = subprocess.run(["pig", "--version"], capture_output=True, text=True, timeout=120).stdout.strip()
    problems = [] if set(host) == HOST_KEYS else [f"host keys are {sorted(host)}, expected {sorted(HOST_KEYS)}"]
    if host.get("system") != expected_system:
        problems.append(f"host.system is {host.get('system')!r}, expected {expected_system!r}")
    if host.get("pig") != version:
        problems.append(f"host.pig is {host.get('pig')!r}, expected the pig --version output {version!r}")
    return problems


def sample_problems(package: Path, document: dict[str, object]) -> list[str]:
    samples = document.get("samples")
    if not isinstance(samples, list) or len(samples) != 1:
        return [f"samples is {samples!r}, expected exactly one sample for --samples 1"]
    sample = samples[0]
    if not isinstance(sample, dict):
        return [f"sample is {sample!r}, expected an object"]
    problems = [] if set(sample) == SAMPLE_KEYS else [f"sample keys are {sorted(sample)}, expected {sorted(SAMPLE_KEYS)}"]
    problems.extend(
        f"sample {metric} is {sample.get(metric)!r}, expected a positive number" for metric in POSITIVE_METRICS if not positive(sample, metric)
    )
    problems.extend(
        f"sample {metric} is {sample.get(metric)!r}, expected a non-negative number"
        for metric in NON_NEGATIVE_METRICS
        if not non_negative(sample, metric)
    )
    return problems


def median_problems(package: Path, document: dict[str, object]) -> list[str]:
    median = document.get("median")
    samples = document.get("samples")
    if not isinstance(median, dict) or not isinstance(samples, list) or not samples:
        return [f"median is {median!r} for samples {samples!r}, expected an object over the samples"]
    problems = [] if set(median) == MEDIAN_KEYS else [f"median keys are {sorted(median)}, expected {sorted(MEDIAN_KEYS)}"]
    record = samples[0]
    if not isinstance(record, dict):
        return problems + [f"sample is {record!r}, expected an object"]
    problems.extend(
        f"median {metric} is {median.get(metric)!r}, expected the single sample's {record.get(metric)!r}"
        for metric in sorted(MEDIAN_KEYS)
        if median.get(metric) != record.get(metric)
    )
    return problems


def recorded_measurements() -> dict[str, object] | None:
    if not MEASUREMENTS.is_file():
        return None
    document = json.loads(MEASUREMENTS.read_text(encoding="utf-8"))
    return document if isinstance(document, dict) else None


def retained_problems(package: Path, document: dict[str, object]) -> list[str]:
    retained = document.get("retained")
    recorded = recorded_measurements()
    if recorded is None:
        return [] if retained is None else [f"retained is {retained!r}, expected null without {MEASUREMENTS.name}"]
    expected = recorded.get("package")
    recorded_samples = expected.get("samples") if isinstance(expected, dict) else None
    retained_samples = retained.get("samples") if isinstance(retained, dict) else None
    if retained_samples != recorded_samples:
        return [f"retained samples are {retained_samples!r}, expected the samples recorded in {MEASUREMENTS.name}"]
    return []


def normalized_arch(machine: str) -> str:
    lowered = machine.lower()
    return ARCH_ALIASES.get(lowered, lowered)


def describes_this_host(recorded: dict[str, object] | None) -> bool:
    if recorded is None:
        return False
    same_system = str(recorded.get("platform", "")).lower() == platform.system().lower()
    same_arch = normalized_arch(str(recorded.get("arch", ""))) == normalized_arch(platform.machine())
    return same_system and same_arch


def measure_tool() -> types.ModuleType:
    spec = importlib.util.spec_from_file_location("pig_status_measure", TOOL)
    assert spec is not None and spec.loader is not None, TOOL
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def retained_composition(recorded: dict[str, object] | None) -> list[str] | None:
    if recorded is None:
        return None
    retained = recorded.get("package")
    composition = retained.get("composition") if isinstance(retained, dict) else None
    if not isinstance(composition, list) or not all(isinstance(entry, str) for entry in composition):
        return None
    return composition


def candidate_package(composition: list[str], root: Path) -> Path:
    extensions = [entry.removeprefix(COMPOSITION_PREFIX) for entry in composition]
    for extension in extensions:
        declared = root / extension
        declared.parent.mkdir(parents=True, exist_ok=True)
        declared.write_text("", encoding="utf-8")
    manifest = {"name": "pig-status-identity", "pi": {"extensions": extensions}}
    (root / "package.json").write_text(json.dumps(manifest), encoding="utf-8")
    return root


def identity_problems(package: Path, document: dict[str, object]) -> list[str]:
    recorded = recorded_measurements()
    composition = retained_composition(recorded)
    if recorded is None:
        return [f"{MEASUREMENTS} is missing, so the comparison identity cannot be probed"]
    if composition is None:
        return [f"{MEASUREMENTS} records no package composition to probe"]
    with tempfile.TemporaryDirectory(prefix="pig-status-identity-") as stub:
        candidate = candidate_package(composition, Path(stub))
        crafted = dict(recorded, platform=platform.system(), arch=normalized_arch(platform.machine()))
        verdict = measure_tool().comparison(crafted, candidate)
    if not isinstance(verdict, str) or not verdict.startswith("unmatched: "):
        return [
            f"a record differing only in protocol and toolchain identity compared as {verdict!r},"
            " expected 'unmatched: <reason>'"
        ]
    reason = verdict.removeprefix("unmatched: ")
    problems = []
    if "protocol" not in reason.lower():
        problems.append(f"the unmatched reason omits the protocol: {reason!r}")
    if TOOLCHAIN_IDENTITY.search(reason) is None:
        problems.append(f"the unmatched reason omits the toolchain identity: {reason!r}")
    return problems


def shutdown_wait_problems(package: Path, document: dict[str, object]) -> list[str]:
    tool = measure_tool()
    child = subprocess.Popen(
        [sys.executable, "-c", DESCENDANT_LAUNCHER],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
    )
    descendant_output = child.stdout
    assert descendant_output is not None, child.args
    descendant = int(descendant_output.readline())
    descendant_output.close()
    child.wait()
    sleeps: list[float] = []
    original_sleep = time.sleep
    time.sleep = sleeps.append
    try:
        elapsed = tool.await_shutdown(child, child.stdin, [descendant])
    finally:
        time.sleep = original_sleep
        if child.poll() is None:
            child.kill()
        child.wait()
        kill_descendant(descendant)
    problems = []
    if sleeps:
        problems.append(f"the shutdown wait polled time.sleep {len(sleeps)} times over a descendant instead of blocking on its pid")
    if not isinstance(elapsed, float) or elapsed <= 0:
        problems.append(f"the shutdown wait reported {elapsed!r} for a descendant that had to exit, expected a positive duration")
    return problems


def kill_descendant(pid: int) -> None:
    try:
        os.kill(pid, signal.SIGKILL)
    except ProcessLookupError:
        return


def departed_pid_shutdown_problems(package: Path, document: dict[str, object]) -> list[str]:
    tool = measure_tool()
    departed = subprocess.Popen(
        [sys.executable, "-c", "pass"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    departed.wait()
    child = subprocess.Popen(
        [sys.executable, "-c", "import sys; sys.stdin.buffer.read()"],
        stdin=subprocess.PIPE,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    try:
        elapsed = tool.await_shutdown(child, child.stdin, [departed.pid])
    finally:
        if child.poll() is None:
            child.kill()
        child.wait()
    if not isinstance(elapsed, float) or elapsed <= 0:
        return [f"a shutdown wait over the departed pid {departed.pid} reported {elapsed!r}, expected a positive duration"]
    return []


def comparison_problems(package: Path, document: dict[str, object]) -> list[str]:
    verdict = document.get("comparison")
    current = f"{platform.system()}/{platform.machine()}"
    if verdict == "matched":
        if describes_this_host(recorded_measurements()):
            return []
        return [f"comparison claims matched, but the retained measurements do not describe {current}"]
    if not isinstance(verdict, str) or not verdict.startswith("unmatched: ") or not verdict.removeprefix("unmatched: ").strip():
        return [f"comparison is {verdict!r}, expected 'matched' or 'unmatched: <reason>'"]
    return []


CHECKS: list[tuple[str, Check]] = [
    ("package identity", package_problem),
    ("host identity", host_problems),
    ("sample record", sample_problems),
    ("median", median_problems),
    ("retained measurements", retained_problems),
    ("comparison verdict", comparison_problems),
    ("comparison identity", identity_problems),
    ("shutdown wait", shutdown_wait_problems),
    ("departed pid shutdown", departed_pid_shutdown_problems),
    ("failed launch", launch_failure_problems),
]


def main() -> int:
    package = package_path(sys.argv)
    document = measure_document(package)

    failed = 0
    for name, check in CHECKS:
        problems = check(package, document)
        if problems:
            failed += 1
            print(f"FAIL {name}: {'; '.join(problems)}")
        else:
            print(f"PASS {name}")
    if failed:
        print(f"measurement document: {failed} of {len(CHECKS)} checks failed for {package}")
        return 1
    print(f"measurement document: {len(CHECKS)} of {len(CHECKS)} checks passed for {package}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
