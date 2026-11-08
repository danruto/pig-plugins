#!/usr/bin/env python3
# /// script
# requires-python = ">=3.13"
# dependencies = []
# ///
"""Fail-closed Linux gate for the staged native usage module and real PiG fixture.

Requires explicit reviewed PIG_NATIVE_USAGE_HOST and PIG_NATIVE_USAGE_SDK inputs.
No installed-host fallback, live roots, external network, or third-party Python code.
The lifecycle run is fixture orchestration, not an independent acceptance oracle.
"""

from collections.abc import Callable
import ctypes
import hashlib
import json
import os
from pathlib import Path
import resource
import selectors
import shutil
import signal
import subprocess
import sys
import tempfile
import time

HOST_SHA256 = "caa1427bf3fd721f813b446748408111e4f1eb0138506b66845bf572e1b4ac0a"
SDK_SHA256 = "f32d435671402b2948d8e14ac010eb8078ce2ceaeb589adab43b8a93f4bb4750"
SDK_MODULE = "github.com/MichaelKinsy/PiG/extensions/sdk"
STATUS_MODULE = "github.com/danruto/pig-plugins/extensions/pig-status"


class PrerequisiteError(Exception):
    pass


def _digest(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def _sdk_digest(root: Path) -> str:
    digest = hashlib.sha256()
    for path in sorted(root.rglob("*")):
        if path.is_symlink():
            raise PrerequisiteError("SDK must not contain symlinks")
        if path.is_file():
            digest.update(path.relative_to(root).as_posix().encode() + b"\0")
            digest.update(path.read_bytes() + b"\0")
    return digest.hexdigest()


def _json(path: Path, value: object) -> None:
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def _run(argv: list[str], cwd: Path, *, env: dict[str, str], timeout: int = 180) -> str:
    print("+", *argv, flush=True)
    with subprocess.Popen(
        argv, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        text=True, encoding="utf-8", start_new_session=True,
    ) as process:
        try:
            output, _ = process.communicate(timeout=timeout)
        finally:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=10)
    print(output, end="", flush=True)
    if process.returncode:
        raise subprocess.CalledProcessError(process.returncode, argv, output)
    return output


def _environment(work: Path, tools: dict[str, str]) -> dict[str, str]:
    env = {
        "PATH": ":".join(sorted({str(Path(value).parent) for value in tools.values()}))
        + ":/usr/bin:/bin",
        "LANG": "C.UTF-8", "GOMAXPROCS": "2", "GOFLAGS": "-p=2 -modcacherw",
        "GOWORK": "off", "GOTOOLCHAIN": "local", "GOENV": "off", "GOPROXY": "off",
        "GOSUMDB": "off", "GOTELEMETRY": "off", "CGO_ENABLED": "1", "CC": tools["cc"],
        "NA_OUTPUT": str(work),
    }
    for key in (
        "HOME", "PIG_HOME", "PIG_CODING_AGENT_DIR", "PI_CODING_AGENT_DIR",
        "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
        "GOCACHE", "GOMODCACHE", "TMPDIR",
    ):
        path = work / "roots" / key
        path.mkdir(parents=True)
        env[key] = str(path)
    return env


def _audit_connections(trace: Path, allowed: list[str]) -> None:
    connections = [line for line in trace.read_text(encoding="utf-8").splitlines()
                   if "connect(" in line and "AF_INET" in line]
    for line in connections:
        if not any(
            f"sin_port=htons({address.rsplit(':', 1)[1]})" in line
            and f'inet_addr("{address.rsplit(":", 1)[0]}")' in line
            for address in allowed
        ):
            raise RuntimeError("host attempted an IP connection outside fixture TLS")
    _json(trace.with_suffix(".connections.json"), {"connections": len(connections), "allowed": allowed})


def _lifecycle(work: Path, tools: dict[str, str], *, env: dict[str, str], fixture: Path) -> None:
    libc = ctypes.CDLL(None, use_errno=True)
    fd = libc.inotify_init1(os.O_NONBLOCK | os.O_CLOEXEC)
    if fd < 0:
        raise OSError(ctypes.get_errno(), "inotify_init1")
    try:
        if libc.inotify_add_watch(fd, os.fsencode(work), 0x100 | 0x2) < 0:
            raise OSError(ctypes.get_errno(), "inotify_add_watch")
        _drive_lifecycle(work, tools, env=env, fixture=fixture, notify_fd=fd)
    finally:
        os.close(fd)


def _drive_lifecycle(work: Path, tools: dict[str, str], *, env: dict[str, str], fixture: Path,
                     notify_fd: int) -> None:
    trace = work / "host.trace"
    argv = [tools["strace"], "-f", "-qq", "-e", "trace=process,connect", "-o", str(trace),
            "/host/pig", "--offline", "--mode", "rpc", "--no-session", "--no-extensions",
            "--no-skills", "--no-prompt-templates", "--no-context-files", "--no-mcp",
            "--no-tools", "-e", str(fixture), "--model", "openrouter/fixture"]
    with (work / "host.stderr").open("w", encoding="utf-8") as errors, \
            (work / "host.stdout.jsonl").open("wb") as output, \
            selectors.DefaultSelector() as selector, \
            subprocess.Popen(argv, cwd=work, env=env, stdin=subprocess.PIPE,
                             stdout=subprocess.PIPE, stderr=errors) as process:
        if process.stdin is None or process.stdout is None:
            raise RuntimeError("missing RPC pipes")
        _json(work / "host-process.json", {"pid": process.pid, "waitOwner": os.getpid(), "argv": argv})
        selector.register(process.stdout, selectors.EVENT_READ, "rpc")
        selector.register(notify_fd, selectors.EVENT_READ, "receipt")
        pending = b""
        replies: dict[str, dict] = {}

        def events() -> list[dict]:
            try:
                raw = (work / "lifecycle.jsonl").read_text(encoding="utf-8")
            except FileNotFoundError:
                return []
            return [json.loads(line) for line in raw.splitlines(keepends=True) if line.endswith("\n")]

        def wait_for(predicate: Callable[[], bool], timeout: int = 90) -> None:
            nonlocal pending
            deadline = time.monotonic() + timeout
            while not predicate():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError("lifecycle fixture message deadline")
                ready = selector.select(remaining)
                if not ready:
                    raise TimeoutError("lifecycle fixture message deadline")
                for key, _ in ready:
                    if key.data == "receipt":
                        os.read(notify_fd, 65536)
                    else:
                        chunk = os.read(process.stdout.fileno(), 65536)
                        if not chunk:
                            raise RuntimeError("host exited before fixture completion")
                        output.write(chunk)
                        output.flush()
                        pending += chunk
                        while b"\n" in pending:
                            line, pending = pending.split(b"\n", 1)
                            row = json.loads(line)
                            if "id" in row:
                                replies[row["id"]] = row

        def begin(text: str) -> str:
            identity = str(time.monotonic_ns())
            process.stdin.write((json.dumps({"id": identity, "type": "prompt", "message": text}) + "\n").encode())
            process.stdin.flush()
            return identity

        def finish(identity: str) -> None:
            wait_for(lambda: identity in replies)
            reply = replies.pop(identity)
            if not reply.get("success") or reply.get("data", {}).get("disposition") != "handled":
                raise RuntimeError("fixture control was not handled")

        def command(text: str) -> None:
            finish(begin(text))

        try:
            wait_for(lambda: any(row["event"] == "ready" for row in events()))
            wait_for(lambda: any(row["event"] == "publish" and row.get("detail", {}).get("windows") for row in events()))
            if (work / "qualification").exists():
                _qualification(work, events, wait_for, command, begin, finish)
            else:
                (work / "hold").touch()
                command("/lifecycle-model deepseek/fixture")
                wait_for(lambda: any(row["event"] == "held" for row in events()))
                command("/lifecycle-config")
                wait_for(lambda: any(row["event"] == "cancelled" for row in events()))
                (work / "hold").unlink()
                command("/lifecycle-close")
        finally:
            process.stdin.close()
            try:
                process.wait(timeout=25)
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=10)
            output.write(process.stdout.read())
        if process.returncode:
            raise RuntimeError("fixture host failed; inspect host.stderr")
        _json(work / "host-wait.json", {"pid": process.pid, "waitOwner": os.getpid(), "exit": process.returncode})
        _audit_connections(trace, [row["detail"] for row in events() if row["event"] == "https"])


def _qualification(work, events, wait_for, command, begin, finish) -> None:
    def phase(name):
        with (work / "lifecycle.jsonl").open("a", encoding="utf-8") as stream:
            stream.write(json.dumps({"event": "phase", "detail": name, "time": time.time_ns() // 1_000_000}) + "\n")

    def count(kind):
        return sum(row["event"] == kind for row in events())

    def response(used=70, fail=False, hold=False, targets=True):
        command("/lifecycle-response " + json.dumps({"used": used, "fail": fail, "hold": hold, "targets": targets}))

    def owner(key="synthetic-lifecycle", max_tokens=128, provider="openrouter", select=False):
        command("/lifecycle-owner " + json.dumps({"provider": provider, "key": key, "maxTokens": max_tokens, "select": select}))

    command("/lifecycle-stop")
    phase("cache-auth")
    command("/lifecycle-collect cached-initial")
    response()
    owner(key="synthetic-rotated")
    command("/lifecycle-collect cached-rotated")
    command("/lifecycle-collect cached-reused")

    for name, change in (
        ("same-id", lambda: owner(key="synthetic-rotated", max_tokens=256)),
        ("auth", lambda: owner(key="synthetic-lifecycle", max_tokens=256)),
        ("settings", lambda: command("/lifecycle-cache-config changed")),
    ):
        phase("late-" + name)
        command("/lifecycle-cache-config")
        response(used=20, hold=True)
        held = count("late-held")
        identity = begin("/lifecycle-collect stale-" + name)
        wait_for(lambda: count("late-held") > held)
        change()
        command("/lifecycle-release")
        finish(identity)
        response()
        command("/lifecycle-collect successor-" + name)

    phase("target-cache")
    owner(provider="fireworks", select=True)
    command("/lifecycle-cache-config first")
    command("/lifecycle-collect target-initial")
    phase("target-invalid")
    response(targets=False)
    command("/lifecycle-collect target-invalid")

    phase("timer-start")
    ready = count("ready")
    command("/lifecycle-reload")
    wait_for(lambda: count("ready") > ready)
    response(used=20)
    start = len(events())
    command("/lifecycle-model openrouter/fixture")
    wait_for(lambda: any(row["event"] == "publish" and row.get("detail", {}).get("windows") for row in events()[start:]))
    phase("timer-cached")
    response(fail=True)
    failed = count("failure-response")
    wait_for(lambda: count("failure-response") >= failed + 1, 330)
    try:
        wait_for(lambda: count("failure-response") >= failed + 2, 45)
        response()
        wait_for(lambda: any(row["event"] == "publish" and any(window["UsedPercent"] == 70 for window in row.get("detail", {}).get("windows") or []) for row in events()[start:]), 45)
    except TimeoutError:
        _json(work / "timer-observation.json", {"result": "timeout", "scope": "automatic retry/recovery; not clean completion"})
    phase("timer-recovered")

    response(fail=True)
    failures = count("failure-response")
    command("/lifecycle-model deepseek/fixture")
    wait_for(lambda: count("failure-response") > failures)
    try:
        wait_for(lambda: sum(row.get("type") == "extension_ui_request" and row.get("method") == "notify" and "query-failed" in row.get("message", "") for row in [json.loads(line) for line in (work / "host.stdout.jsonl").read_text().splitlines()]) >= 2, 3)
    except TimeoutError:
        _json(work / "notification-observation.json", {"result": "timeout", "scope": "owner-change diagnostic; not clean completion"})
    response()
    (work / "hold").touch()
    held = count("held")
    command("/lifecycle-model openrouter/fixture")
    wait_for(lambda: count("held") > held)
    phase("session-active")
    ready = count("ready")
    command("/lifecycle-session")
    wait_for(lambda: count("ready") > ready)
    response()
    start = len(events())
    (work / "hold").unlink()
    wait_for(lambda: any(row["event"] == "publish" and any(window["UsedPercent"] == 70 for window in row.get("detail", {}).get("windows") or []) for row in events()[start:]))
    phase("session-recovered")
    (work / "hold").touch()
    held = count("held")
    command("/lifecycle-model deepseek/fixture")
    wait_for(lambda: count("held") > held)
    phase("reload-active")
    ready = count("ready")
    command("/lifecycle-reload")
    wait_for(lambda: count("ready") > ready)
    response()
    start = len(events())
    command("/lifecycle-model openrouter/fixture")
    (work / "hold").unlink()
    wait_for(lambda: any(row["event"] == "publish" and any(window["UsedPercent"] == 70 for window in row.get("detail", {}).get("windows") or []) for row in events()[start:]))
    phase("reload-recovered")
    (work / "hold").touch()
    held = count("held")
    command("/lifecycle-model deepseek/fixture")
    wait_for(lambda: count("held") > held)
    phase("eof-active")


def _inner(work: Path, tools: dict[str, str], *, original: dict[str, str]) -> None:
    proof = {key: os.readlink("/proc/self/ns/" + key) for key in original}
    if any(proof[key] == original[key] for key in original):
        raise RuntimeError("required namespace isolation unavailable")
    if set(os.environ) - {"LC_CTYPE", "PWD"}:
        raise RuntimeError("ambient environment reached isolated runner")
    _json(work / "namespaces.json", {"outside": original, "inside": proof})
    env = _environment(work, tools)
    _json(work / "environment-keys.json", sorted(env))
    stage_trace = work / "sdk-host.trace"
    sdk = _run([tools["strace"], "-f", "-qq", "-e", "trace=process,connect", "-o", str(stage_trace),
                "/host/pig", "reload", "--sdk-path"], work, env=env).strip()
    _audit_connections(stage_trace, [])
    if not Path(sdk).resolve().is_relative_to(Path(env["PIG_HOME"])) or _sdk_digest(Path(sdk)) != SDK_SHA256:
        raise RuntimeError("host-staged SDK differs from reviewed prerequisite")
    version_trace = work / "version-host.trace"
    version = _run([tools["strace"], "-f", "-qq", "-e", "trace=process,connect", "-o", str(version_trace),
                    "/host/pig", "--version"], work, env=env).strip()
    _audit_connections(version_trace, [])
    _json(work / "identity.json", {"hostSHA256": _digest(Path("/host/pig")), "sdkSHA256": _sdk_digest(Path("/sdk")), "version": version})
    status = work / "source/extensions/pig-status"
    native = status / "nativeusage"
    fixture = native / "testdata/lifecycle"
    for directory, name in ((native, "nativeusage"), (fixture, "fixture")):
        modfile = work / (name + ".mod")
        shutil.copyfile(directory / "go.mod", modfile)
        _run([tools["go"], "mod", "edit", f"-modfile={modfile}", f"-replace={SDK_MODULE}=/sdk",
              f"-replace={STATUS_MODULE}={status}", f"-replace={STATUS_MODULE}/nativeusage={native}"], directory, env=env)
        for action in ("build", "vet", "test"):
            args = [tools["go"], action, f"-modfile={modfile}"]
            if action == "test":
                args += ["-count=1", "-race", "-json"]
            _run([*args, "./..."], directory, env=env, timeout=240)
    _run([tools["go"], "mod", "edit", f"-replace={STATUS_MODULE}={status}",
          f"-replace={STATUS_MODULE}/nativeusage={native}"], fixture, env=env)
    agent = Path(env["PIG_CODING_AGENT_DIR"])
    for name in ("auth", "settings"):
        _json(agent / (name + ".json"), {})
    providers = {}
    for provider, base in (("deepseek", "https://api.deepseek.com/v1"), ("openrouter", "https://openrouter.ai/api/v1")):
        providers[provider] = {"baseUrl": base, "api": "openai-completions", "apiKey": "synthetic-lifecycle",
                               "models": [{"id": "fixture", "name": "Fixture", "reasoning": False,
                                           "input": ["text"], "contextWindow": 4096, "maxTokens": 128,
                                           "cost": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}]}
    _json(agent / "models.json", {"providers": providers})
    validation = work / "prevalidate"
    validation.mkdir()
    validation_trace = validation / "host.trace"
    _run([tools["strace"], "-f", "-qq", "-e", "trace=process,connect", "-o", str(validation_trace),
          "/host/pig", "install", "--validate-only", "--json", str(fixture)], work,
         env={**env, "NA_OUTPUT": str(validation)}, timeout=240)
    _audit_connections(validation_trace, [])
    _lifecycle(work, tools, env=env, fixture=fixture)
    _json(work / "result.json", {"result": "passed", "scope": "nested module gates and real-host fixture orchestration; not independent acceptance"})


def _outer(env: dict[str, str], scenario: str = "frozen") -> None:
    for key in ("PIG_NATIVE_USAGE_HOST", "PIG_NATIVE_USAGE_SDK"):
        if not env.get(key):
            raise PrerequisiteError(f"native usage gate unavailable: set {key} to the reviewed prerequisite (no installed-host fallback)")
    if sys.platform != "linux":
        raise PrerequisiteError("native usage gate requires Linux user/network/PID/mount namespaces")
    host = Path(env["PIG_NATIVE_USAGE_HOST"]).resolve(strict=True)
    sdk = Path(env["PIG_NATIVE_USAGE_SDK"]).resolve(strict=True)
    if _digest(host) != HOST_SHA256 or _sdk_digest(sdk) != SDK_SHA256:
        raise PrerequisiteError("native usage gate prerequisite identity mismatch; only reviewed host/SDK accepted")
    tools = {}
    for name in ("go", "cc", "node", "bash", "env", "strace", "bwrap", "git", "nix-store"):
        executable = env.get("PIG_NATIVE_USAGE_" + name.upper().replace("-", "_")) or shutil.which(name)
        if not executable:
            raise PrerequisiteError(f"native usage gate unavailable: {name} required (or PIG_NATIVE_USAGE_{name.upper()})")
        tools[name] = str(Path(executable).resolve(strict=True))
    tools["python"] = str(Path(sys.executable).resolve(strict=True))
    repo = Path(__file__).resolve().parent.parent
    scratch = env.get("PIG_NATIVE_USAGE_SCRATCH")
    if scratch:
        Path(scratch).mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="nativeusage-", dir=scratch)).resolve()
    print("native usage gate artifacts:", work, flush=True)
    if scenario == "qualification":
        (work / "qualification").touch()
    files = subprocess.run([tools["git"], "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "extensions/pig-status"],
                           cwd=repo, env={}, check=True, capture_output=True, timeout=10).stdout.decode().split("\0")
    for name in filter(None, files):
        source = repo / name
        if source.is_symlink() or not source.is_file():
            raise PrerequisiteError("source inputs must be regular repository files")
        target = work / "source" / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
    shutil.copyfile(__file__, work / "gate.py")
    original = {key: os.readlink("/proc/self/ns/" + key) for key in ("net", "pid", "mnt")}
    stores = {str(Path(*Path(value).parts[:4])) for value in tools.values()}
    if any(not value.startswith("/nix/store/") for value in tools.values()):
        raise PrerequisiteError("native usage gate requires Nix-store toolchain inputs")
    closure = subprocess.run(["nix-store", "-qR", *sorted(stores)], executable=tools["nix-store"], env={}, check=True,
                             capture_output=True, text=True, timeout=30).stdout.splitlines()
    if not closure or any(not path.startswith("/nix/store/") or not Path(path).is_dir() for path in closure):
        raise PrerequisiteError("invalid Nix toolchain closure")
    argv = [tools["bwrap"], "--die-with-parent", "--new-session", "--unshare-all", "--clearenv",
            "--proc", "/proc", "--dev", "/dev", "--dir", "/etc", "--dir", "/host",
            "--dir", "/usr/bin", "--dir", "/lib"]
    for path in closure:
        argv += ["--ro-bind", path, path]
    for tool, name in (("bash", "bash"), ("bash", "sh"), ("env", "env"), ("node", "node")):
        argv += ["--symlink", tools[tool], "/usr/bin/" + name]
    argv += ["--symlink", "usr/bin", "/bin"]
    for library in ("libc.so.6", "libgcc_s.so.1"):
        path = subprocess.run([tools["cc"], "-print-file-name=" + library], env={"PATH": str(Path(tools["cc"]).parent) + ":/usr/bin:/bin"},
                              check=True, capture_output=True, text=True, timeout=10).stdout.strip()
        if not path.startswith("/nix/store/") or not Path(path).is_file():
            raise PrerequisiteError("Nix compiler runtime library unavailable")
        argv += ["--symlink", path, "/lib/" + library]
    argv += ["--ro-bind", str(host), "/host/pig", "--ro-bind", str(sdk), "/sdk",
             "--bind", str(work), "/work", "--tmpfs", "/tmp", "--chdir", "/work", "--remount-ro", "/",
             tools["python"], "/work/gate.py", "--inner", json.dumps(tools), json.dumps(original)]
    _json(work / "launch.json", {"argv": argv, "hostSHA256": HOST_SHA256, "sdkSHA256": SDK_SHA256})
    started = time.monotonic()
    try:
        _run(argv, work, env={}, timeout=900)
    finally:
        usage = resource.getrusage(resource.RUSAGE_CHILDREN)
        _json(work / "runner-resources.json", {"elapsedSeconds": time.monotonic() - started,
                                               "maxRSSKiB": usage.ru_maxrss,
                                               "userSeconds": usage.ru_utime, "systemSeconds": usage.ru_stime})
        print("native usage gate artifacts retained:", work, flush=True)


def main() -> int:
    try:
        if len(sys.argv) == 4 and sys.argv[1] == "--inner":
            _inner(Path("/work"), json.loads(sys.argv[2]), original=json.loads(sys.argv[3]))
        elif len(sys.argv) == 3 and sys.argv[1:] == ["--scenario", "qualification"]:
            _outer(dict(os.environ), "qualification")
        elif len(sys.argv) == 1:
            _outer(dict(os.environ))
        else:
            raise PrerequisiteError("usage: python3 scripts/check-native-usage.py")
    except PrerequisiteError as error:
        sys.stderr.write(f"{error}\n")
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
