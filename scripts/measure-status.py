#!/usr/bin/env python3
"""Measure startup, resident memory and shutdown of a built pig-status package through PiG RPC."""

import argparse
import contextlib
import json
import os
import platform
import select
import signal
import statistics
import subprocess
import sys
import tempfile
import time
from collections.abc import Sequence
from pathlib import Path
from typing import IO

READY_TIMEOUT = 120.0
SHUTDOWN_TIMEOUT = 30.0
METRICS = ("startup", "rss_kib", "shutdown")
MEASUREMENTS = Path(__file__).resolve().parent.parent / "docs/compatibility/usage/measurements.json"
ARCH_ALIASES = {"aarch64": "arm64", "x86_64": "x64", "amd64": "x64"}


class MeasureError(Exception):
    pass


def disposable_env(root: Path) -> dict[str, str]:
    env = dict(
        os.environ,
        HOME=str(root),
        PIG_HOME=str(root),
        PIG_CODING_AGENT_DIR=str(root / "agent"),
        PI_CODING_AGENT_DIR=str(root / "pi-agent"),
        PI_HOME=str(root / "pi"),
    )
    (root / "agent").mkdir(parents=True, exist_ok=True)
    return env


def await_commands(process: subprocess.Popen[bytes], request: IO[bytes], response: IO[bytes], deadline: float) -> None:
    request.write(b'{"id":"commands","type":"get_commands"}\n')
    request.flush()
    buffer = bytearray()
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise MeasureError("pig did not answer get_commands")
        if not select.select([response], [], [], remaining)[0]:
            continue
        chunk = os.read(response.fileno(), 65536)
        if not chunk:
            raise MeasureError(f"pig closed its output before answering get_commands (exit {process.poll()})")
        buffer.extend(chunk)
        while b"\n" in buffer:
            line, _, rest = bytes(buffer).partition(b"\n")
            buffer = bytearray(rest)
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not isinstance(record, dict) or record.get("type") != "response" or record.get("id") != "commands":
                continue
            if record.get("success") is not True:
                raise MeasureError(f"get_commands failed: {record}")
            return


def parent_pid(pid: int) -> int | None:
    try:
        stat = Path(f"/proc/{pid}/stat").read_text(encoding="utf-8")
    except OSError:
        return None
    fields = stat[stat.rindex(")") + 2:].split()
    return int(fields[1]) if len(fields) > 1 else None


def resident_kib(pid: int) -> int:
    try:
        status = Path(f"/proc/{pid}/status").read_text(encoding="utf-8")
    except OSError:
        return 0
    for line in status.splitlines():
        if line.startswith("VmRSS:"):
            return int(line.split()[1])
    return 0


def descendants(root: int) -> list[int]:
    children: dict[int, list[int]] = {}
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        pid = int(entry.name)
        ppid = parent_pid(pid)
        if ppid is not None:
            children.setdefault(ppid, []).append(pid)
    found: list[int] = []
    queue = [root]
    while queue:
        pid = queue.pop()
        found.append(pid)
        queue.extend(children.get(pid, []))
    return found


def await_shutdown(process: subprocess.Popen[bytes], request: IO[bytes], observed: Sequence[int]) -> float:
    started = time.monotonic()
    deadline = started + SHUTDOWN_TIMEOUT
    request.close()
    try:
        process.wait(timeout=max(0.0, deadline - time.monotonic()))
    except subprocess.TimeoutExpired as error:
        raise MeasureError(f"pig did not exit within {SHUTDOWN_TIMEOUT:.0f} s of stdin closing") from error
    while any(Path(f"/proc/{pid}").exists() for pid in observed):
        if time.monotonic() >= deadline:
            raise MeasureError("descendant processes outlived pig")
        time.sleep(0.005)
    return time.monotonic() - started


def kill_group(process: subprocess.Popen[bytes]) -> None:
    with contextlib.suppress(ProcessLookupError):
        os.killpg(process.pid, signal.SIGKILL)
    with contextlib.suppress(subprocess.TimeoutExpired):
        process.wait(timeout=5)


def drain(stream: IO[bytes] | None, seconds: float = 1.0) -> str:
    if stream is None or stream.closed:
        return ""
    deadline = time.monotonic() + seconds
    chunks = bytearray()
    while (remaining := deadline - time.monotonic()) > 0:
        if not select.select([stream], [], [], remaining)[0]:
            break
        chunk = os.read(stream.fileno(), 65536)
        if not chunk:
            break
        chunks.extend(chunk)
    return chunks.decode(errors="replace").strip()


def measure(package: Path) -> dict[str, float | int]:
    with tempfile.TemporaryDirectory(prefix="pig-status-measure-") as scratch:
        root = Path(scratch)
        load_average = os.getloadavg()[0]
        started = time.monotonic()
        process = subprocess.Popen(
            ["pig", "--mode", "rpc", "--no-session", "--approve", "--no-extensions", "-e", str(package)],
            cwd=root,
            env=disposable_env(root),
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        request, response, stderr = process.stdin, process.stdout, process.stderr
        try:
            if request is None or response is None:
                raise MeasureError("pig started without pipes")
            await_commands(process, request, response, started + READY_TIMEOUT)
            startup = time.monotonic() - started
            observed = descendants(process.pid)
            resident = sum(resident_kib(pid) for pid in observed)
            shutdown = await_shutdown(process, request, observed)
        except (MeasureError, OSError) as error:
            error.add_note(f"pig stderr: {drain(stderr)}")
            raise
        finally:
            kill_group(process)
            for stream in (request, response, stderr):
                if stream is not None and not stream.closed:
                    stream.close()
        return {"startup": startup, "rss_kib": resident, "shutdown": shutdown, "loadavg": load_average}


def declared_extensions(package: Path) -> list[str]:
    try:
        manifest = json.loads((package / "package.json").read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise MeasureError(f"cannot read {package}/package.json") from error
    pi = manifest.get("pi") if isinstance(manifest, dict) else None
    extensions = pi.get("extensions") if isinstance(pi, dict) else None
    return [str(entry) for entry in extensions] if isinstance(extensions, list) else []


def retained_measurements() -> dict[str, object] | None:
    try:
        document = json.loads(MEASUREMENTS.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return None
    return document if isinstance(document, dict) else None


def normalized_arch(machine: str) -> str:
    return ARCH_ALIASES.get(machine.lower(), machine.lower())


def comparison(recorded: dict[str, object] | None, package: Path) -> str:
    if recorded is None:
        return f"unmatched: no retained package measurements in {MEASUREMENTS}"
    reasons = []
    recorded_platform = str(recorded.get("platform", "")).lower()
    if recorded_platform != platform.system().lower():
        reasons.append(f"recorded platform {recorded_platform or 'unknown'}, current {platform.system().lower()}")
    recorded_arch = normalized_arch(str(recorded.get("arch", "")))
    current_arch = normalized_arch(platform.machine())
    if recorded_arch != current_arch:
        reasons.append(f"recorded arch {recorded_arch or 'unknown'}, current {current_arch}")
    retained_package = recorded.get("package")
    composition = retained_package.get("composition") if isinstance(retained_package, dict) else None
    retained = sorted(str(entry).removeprefix("$PACKAGE/") for entry in composition) if isinstance(composition, list) else []
    current = sorted(declared_extensions(package))
    if retained != current:
        reasons.append(f"recorded composition {retained}, current {current}")
    return "unmatched: " + "; ".join(reasons) if reasons else "matched"


def median(samples: Sequence[dict[str, float | int]]) -> dict[str, float | int]:
    return {metric: statistics.median(sample[metric] for sample in samples) for metric in METRICS}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Measure a built pig-status package through PiG RPC.")
    parser.add_argument("package", type=Path)
    parser.add_argument("--samples", type=int, default=3)
    args = parser.parse_args(argv)
    if args.samples < 1:
        parser.error("--samples must be at least 1")
    if platform.system() != "Linux":
        print("measure-status: Linux /proc is required", file=sys.stderr)
        return 2
    package = args.package.resolve()
    try:
        samples = [measure(package) for _ in range(args.samples)]
        pig = subprocess.run(["pig", "--version"], check=True, capture_output=True, text=True).stdout.strip()
        recorded = retained_measurements()
        identity = comparison(recorded, package)
    except (MeasureError, OSError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        notes = "\n".join(error.__notes__)
        print(f"measure-status: {error}{f'{chr(10)}{notes}' if notes else ''}", file=sys.stderr)
        return 1
    report = {
        "package": str(package),
        "host": {"system": f"{platform.system()}/{platform.machine()}", "pig": pig},
        "samples": samples,
        "median": median(samples),
        "retained": recorded.get("package") if recorded else None,
        "comparison": identity,
    }
    json.dump(report, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
