#!/usr/bin/env python3
# /// script
# requires-python = ">=3.13"
# dependencies = []
# ///
"""Run disposable native-auth fixtures against the frozen reviewed host; no activation."""

from __future__ import annotations

import ctypes
import hashlib
import importlib.util
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import threading
import time
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]
SCRATCH = ROOT / ".pb/probes/21/auth"
REPORTS = ROOT / ".pb/evidence/native-auth-20261104/resumed"
FROZEN = ROOT / ".pb/probes/host-auth/hardened"
HOST = FROZEN / "bin/fix-round1-1782627e-pig"
SDK = FROZEN / "runs/fix-round1-1782627e-stage-run/roots/stage/PIG_HOME/state/pigsdk/sdk-6faeac842431b723"
GO = "/nix/store/0940szmkaskjshd3z0gp2m2kfxcnc424-go-1.27.3/bin/go"
FORBIDDEN = {"agent_start", "agent_end", "agent_settled", "turn_start", "turn_end", "provider_stream_event", "message_start", "message_update", "message_end", "extension_error"}


def _frozen_module(name: str, path: Path) -> Any:
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError("frozen helper unavailable")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _environment(run: Path) -> dict[str, str]:
    env = {"PATH": f"{run / 'bin'}:{Path(GO).parent}:/usr/bin", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "GOMAXPROCS": "2", "GOFLAGS": "-p=2 -trimpath", "GOWORK": "off", "GOTOOLCHAIN": "local", "GOENV": "off", "GOPROXY": "off", "GOSUMDB": "off", "GOTELEMETRY": "off", "PIG_REQUIRE_EXTENSION_TOOLCHAINS": "1", "NA_OUTPUT": str(run), "PIG_STATUS_PACKAGE": str(ROOT)}
    for key in ("HOME", "PIG_HOME", "PI_HOME", "PIG_CODING_AGENT_DIR", "PI_CODING_AGENT_DIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "GOCACHE", "GOMODCACHE", "TMPDIR", "PYTHONPYCACHEPREFIX"):
        path = run / "roots" / key
        path.mkdir(parents=True, exist_ok=True)
        env[key] = str(path)
    env["PIG_STATUS_PACKAGE"] = str(run / "package") if (run / "package").exists() else str(ROOT)
    env["TMP"] = env["TEMP"] = env["TMPDIR"]
    env["CGO_ENABLED"] = "0"
    env["PYTHONDONTWRITEBYTECODE"] = "1"
    return env


def _build(tools: Any) -> None:
    build = SCRATCH / "build"
    build.mkdir(parents=True, exist_ok=True)
    env = _environment(SCRATCH / "preparation")
    env["CGO_ENABLED"] = "1"
    env["PATH"] = f"{Path(GO).parent}:/nix/store/99zij8qfs8wzbb2njrj18ys8hpnrcj3p-gcc-wrapper-16.2.0/bin:/usr/bin:/bin"
    modfile = build / "host.mod"
    modfile.write_text((ROOT / "extensions/pig-status/internal/usage/testdata/host/go.mod").read_text(encoding="utf-8"), encoding="utf-8")
    fixture = ROOT / "extensions/pig-status/internal/usage/testdata/host"
    commands = [
        ([GO, "mod", "edit", f"-modfile={modfile}", f"-replace=github.com/MichaelKinsy/PiG/extensions/sdk={SDK}", f"-replace=github.com/danruto/pig-plugins/extensions/pig-status={ROOT / 'extensions/pig-status'}"], fixture),
        ([GO, "build", "-race", f"-modfile={modfile}", "-o", str(build / "consumer"), "."], fixture),
        ([GO, "vet", f"-modfile={modfile}", "./..."], fixture),
    ]
    statusmod = build / "status.mod"
    statusmod.write_text((ROOT / "extensions/pig-status/go.mod").read_text(encoding="utf-8"), encoding="utf-8")
    status = ROOT / "extensions/pig-status"
    commands.extend([
        ([GO, "mod", "edit", f"-modfile={statusmod}", f"-replace=github.com/MichaelKinsy/PiG/extensions/sdk={SDK}"], status),
        ([GO, "build", f"-modfile={statusmod}", "./..."], status),
        ([GO, "vet", f"-modfile={statusmod}", "./..."], status),
        ([GO, "test", "-c", "-race", f"-modfile={statusmod}", "-o", str(build / "usage.test"), "./internal/usage"], status),
        ([GO, "test", "-c", "-race", f"-modfile={statusmod}", "-o", str(build / "footer.test"), "."], status),
    ])
    with (REPORTS / "build.log").open("w", encoding="utf-8") as log:
        for argv, cwd in commands:
            log.write(json.dumps(argv) + "\n")
            log.flush()
            subprocess.run(argv, cwd=cwd, env=env, check=True, stdout=log, stderr=subprocess.STDOUT)


def _launch(name: str, mode: str, tools: Any) -> None:
    if not re.fullmatch(r"[a-z0-9-]+", name) or mode not in {"process", "checks", "footer"}:
        raise ValueError("invalid fixture invocation")
    if hashlib.sha256(HOST.read_bytes()).hexdigest() != "caa1427bf3fd721f813b446748408111e4f1eb0138506b66845bf572e1b4ac0a":
        raise RuntimeError("reviewed binary changed")
    run = SCRATCH / "runs" / name
    run.mkdir(parents=True, exist_ok=False)
    (run / "bin").mkdir()
    (run / "tmp").mkdir()
    (run / "bin/pig").symlink_to(HOST)
    stores = {str(Path(value).parents[1]) for value in (tools.PYTHON, tools.NODE, tools.BASH, tools.ENV, tools.STRACE, GO)} | {tools.GLIBC}
    closure = subprocess.run([tools.NIXSTORE, "-qR", *sorted(stores)], env={}, capture_output=True, text=True, check=True).stdout.splitlines()
    manifest = {"run": str(run), "mode": mode, "outsideNetworkNamespace": os.readlink("/proc/self/ns/net"), "outsidePIDNamespace": os.readlink("/proc/self/ns/pid"), "outsideMountNamespace": os.readlink("/proc/self/ns/mnt")}
    (run / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    argv = [tools.BWRAP, "--die-with-parent", "--new-session", "--unshare-all", "--clearenv", "--proc", "/proc", "--dev", "/dev", "--dir", "/nix/store", "--dir", "/etc", "--dir", "/usr/bin", "--dir", "/lib"]
    for store in closure:
        argv.extend(["--ro-bind", store, store])
    for source, target in ((tools.BASH, "/usr/bin/bash"), (tools.BASH, "/usr/bin/sh"), (tools.ENV, "/usr/bin/env"), (tools.PYTHON, "/usr/bin/python3"), (tools.NODE, "/usr/bin/node"), ("usr/bin", "/bin"), (tools.GLIBC + "/lib/ld-linux-aarch64.so.1", "/lib/ld-linux-aarch64.so.1"), (tools.GLIBC + "/lib/libc.so.6", "/lib/libc.so.6"), ("/nix/store/na9inwswsrgxxqpqrz3ms78qn7pyxg44-gcc-16.2.0-lib/lib/libgcc_s.so.1", "/lib/libgcc_s.so.1")):
        argv.extend(["--symlink", source, target])
    argv.extend(["--ro-bind", str(ROOT), str(ROOT), "--bind", str(run), str(run), "--bind", str(run / "tmp"), str(Path("/") / "tmp"), "--chdir", str(run), "--remount-ro", "/", tools.PYTHON, str(Path(__file__).resolve()), "inner", str(run / "manifest.json")])
    with (REPORTS / f"{name}.log").open("x", encoding="utf-8") as log:
        process = subprocess.Popen(argv, env={}, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            status = process.wait(timeout=300)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            status = process.wait()
            raise
        finally:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
    (REPORTS / f"{name}.exit").write_text(str(status) + "\n", encoding="utf-8")
    print((REPORTS / f"{name}.log").read_text(encoding="utf-8")[-14000:])
    if status:
        raise SystemExit(status)


def _checked(run: Path, argv: list[str], env: dict[str, str], *, cwd: Path = ROOT) -> None:
    clean = _environment(run)
    if clean != env:
        raise RuntimeError("inner environment allowlist differs")
    tools = _frozen_module("checked_tools", FROZEN / "fix-round1-1782627e/launcher.py")
    resources_type = _frozen_module("checked_resources", FROZEN / "recovery/resource_sampler.py").Resources
    receipt_parser = _frozen_module("checked_receipts", FROZEN / "process_receipts.py").process_receipts
    gates = run / "gates"
    gates.mkdir(exist_ok=True)
    directory = gates / str(len(list(gates.iterdir())))
    directory.mkdir()
    trace = directory / "process.trace"
    process = subprocess.Popen([tools.STRACE, "--seccomp-bpf", "-f", "-ttt", "-qq", "-e", "trace=process,connect", "-o", str(trace), *argv], env=clean, cwd=cwd, start_new_session=True)
    resources = resources_type(process.pid,directory)
    try:
        status = process.wait(timeout=180)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid,signal.SIGKILL)
        process.wait()
        raise
    finally:
        resources.finish()
    receipts = receipt_parser(trace.read_text(encoding="utf-8"))
    adopted = []
    while True:
        try:
            pid,child_status = os.waitpid(-1,os.WNOHANG)
        except ChildProcessError:
            break
        if pid == 0:
            raise RuntimeError("gate adopted process still running")
        adopted.append({"pid":pid,"status":child_status,"waitOwner":os.getpid()})
    receipts["coordinatorAdoptedWaits"] = adopted
    consumed = {row["pid"] for row in adopted}
    receipts["missingConsumingWaits"] = [pid for pid in receipts["missingConsumingWaits"] if pid not in consumed]
    (directory / "process-receipts.json").write_text(json.dumps(receipts,indent=2),encoding="utf-8")
    if receipts["missingConsumingWaits"] or receipts["wrongWaitOwners"]:
        raise RuntimeError("gate consuming wait failure")
    if status:
        raise subprocess.CalledProcessError(status,argv)


def _inner(manifest: dict[str, Any], tools: Any) -> None:
    if set(os.environ) - {"LC_CTYPE", "PWD"}:
        raise RuntimeError("ambient inner environment")
    if ctypes.CDLL(None, use_errno=True).prctl(36, 1, 0, 0, 0) != 0:
        raise RuntimeError("subreaper unavailable")
    run = Path(manifest["run"])
    proof = _frozen_module("frozen_inner", FROZEN / "inner.py").namespace_proof(manifest)
    for key, namespace in (("PID", "pid"), ("Mount", "mnt")):
        value = os.readlink(f"/proc/self/ns/{namespace}")
        if value == manifest[f"outside{key}Namespace"]:
            raise RuntimeError("namespace boundary unavailable")
        proof[f"inside{key}Namespace"] = value
    (run / "namespace-proof.json").write_text(json.dumps(proof, indent=2), encoding="utf-8")
    env = _environment(run)
    (run / "environment-keys.json").write_text(json.dumps(sorted(env)), encoding="utf-8")
    mode = manifest["mode"]
    if mode == "process":
        _process(run, env, tools)
        _checked(run, [tools.NODE, "--import", "tsx", "probes/native-auth/source-oracle.mjs"], env)
        _checked(run, [tools.PYTHON, "probes/native-auth/test_process.py"], env)
        return
    if mode == "footer":
        package = run / "package"
        (package / "extensions").mkdir(parents=True)
        shutil.copyfile(ROOT / "package.json", package / "package.json")
        shutil.copytree(ROOT / "extensions/pig-status", package / "extensions/pig-status")
        for source in ("pig-status-usage.ts", "pig-openai-server-compaction.ts"):
            (package / "extensions" / source).symlink_to(ROOT / "extensions" / source)
        for source in ("node_modules", "status", "compaction"):
            (package / source).symlink_to(ROOT / source)
        env = _environment(run)
        _checked(run, [tools.NODE, "--test", "--test-name-pattern=^PiG automatically discovers", "test/test-discovery.mjs"], env)
        _checked(run, [tools.PYTHON, "test/check-footer.py"], env)
        return
    for binary in ("usage.test", "footer.test"):
        cwd = ROOT / "extensions/pig-status"
        if binary == "usage.test":
            cwd = cwd / "internal/usage"
        _checked(run, [str(SCRATCH / "build" / binary), "-test.v", "-test.timeout=90s"], env, cwd=cwd)
    for script in ("usage-reference", "usage-billing-reference", "usage-regional-reference", "usage-consumer-reference"):
        _checked(run, [tools.NODE, "--import", "tsx", f"scripts/{script}.mjs", "--check"], env)
    _checked(run, [str(ROOT / "node_modules/.bin/tsc"), "--noEmit"], env)
    _checked(run, [tools.NODE, "--test", "test/test-usage.mjs", "test/test-docs.mjs"], env)


def _process(run: Path, env: dict[str, str], tools: Any) -> None:
    process_receipts = _frozen_module("frozen_receipts", FROZEN / "process_receipts.py").process_receipts
    Resources = _frozen_module("frozen_resources", FROZEN / "recovery/resource_sampler.py").Resources
    agent = Path(env["PIG_CODING_AGENT_DIR"])
    (agent / "auth.json").write_text("{}", encoding="utf-8")
    (agent / "settings.json").write_text("{}", encoding="utf-8")
    providers = {}
    for provider, base in (("baseten", "https://inference.baseten.co/v1"), ("deepseek", "https://api.deepseek.com/v1"), ("openrouter", "https://openrouter.ai/api/v1"), ("fireworks", "https://api.fireworks.ai/inference/v1"), ("kimi-coding", "https://api.kimi.com/coding/v1"), ("unsupported-fixture", "https://fixture.invalid")):
        providers[provider] = {"baseUrl": base, "api": "openai-completions", "models": [{"id": "fixture", "name": "Fixture", "reasoning": False, "input": ["text"], "cost": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}, "contextWindow": 4096, "maxTokens": 128}]}
        if provider not in {"kimi-coding"}:
            providers[provider]["apiKey"] = "synthetic-initial"
    (agent / "models.json").write_text(json.dumps({"providers": providers}), encoding="utf-8")
    fixture = ROOT / "extensions/pig-status/internal/usage/testdata/host"
    argv = [str(HOST), "--offline", "--mode", "rpc", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files", "--no-mcp", "--no-tools", "-e", str(fixture / "source.mjs"), "-e", str(SCRATCH / "build/consumer"), "--model", "deepseek/fixture"]
    trace = run / "process.trace"
    rows: list[dict[str, Any]] = []
    failures: list[str] = []
    with (run / "stdout.jsonl").open("w", encoding="utf-8") as out, (run / "stderr.log").open("w", encoding="utf-8") as err:
        process = subprocess.Popen([tools.STRACE, "--seccomp-bpf", "-f", "-ttt", "-qq", "-e", "trace=process,connect", "-o", str(trace), *argv], env=_environment(run), cwd=run, text=True, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=err)
        resources = Resources(process.pid, run)
        def consume() -> None:
            if process.stdout is None:
                raise RuntimeError("missing process output")
            for line in process.stdout:
                out.write(line)
                out.flush()
                row = json.loads(line)
                rows.append(row)
                if row.get("type") in FORBIDDEN:
                    failures.append("forbidden runtime event")
            process.stdout.close()
        reader = threading.Thread(target=consume)
        reader.start()
        def wait(predicate: Any) -> Any:
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                if failures:
                    raise RuntimeError(failures[0])
                value = predicate()
                if value:
                    return value
                if process.poll() is not None:
                    raise RuntimeError("host exited before fixture result")
                time.sleep(.01)
            raise TimeoutError("fixture result deadline")
        def events() -> list[dict[str, Any]]:
            path = run / "native.jsonl"
            return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()] if path.exists() else []
        def send(message: str) -> str:
            if not re.fullmatch(r"/na-(?:query (?:current|all|provider:[a-z-]+)(?: target:[a-z-]+)?(?: companion)?(?: short)?|control (?:reset|rotate|origin|config|override|empty|null|mismatch|duplicate|fail-current|fail-all|other-rotate|other-origin|other-config)|model [a-z-]+/fixture)", message):
                raise ValueError("unregistered fixture input")
            invocation = str(len(rows)) + "-" + str(time.monotonic_ns())
            if process.stdin is None:
                raise RuntimeError("missing process input")
            process.stdin.write(json.dumps({"id": invocation, "type": "prompt", "message": message}) + "\n")
            process.stdin.flush()
            return invocation
        def response(invocation: str) -> None:
            row = wait(lambda: next((row for row in rows if row.get("id") == invocation), None))
            if not row.get("success") or row.get("data", {}).get("disposition") != "handled":
                raise RuntimeError("fixture command was not handled")
        def command(message: str) -> None:
            response(send(message))
        try:
            wait(lambda: any(row.get("event") == "ready" for row in events()))
            commands = ["/na-query current", "/na-query provider:openrouter", "/na-query provider:kimi-coding", "/na-query provider:unsupported-fixture", "/na-query provider:fireworks", "/na-query provider:fireworks target:second", "/na-query provider:fireworks target:gone", "/na-query provider:openai", "/na-query provider:openai-codex", "/na-query provider:openai companion", "/na-query all", "/na-control override", "/na-model deepseek/fixture", "/na-query current", "/na-control empty", "/na-model deepseek/fixture", "/na-query current", "/na-control null", "/na-model deepseek/fixture", "/na-query current", "/na-control reset", "/na-model deepseek/fixture"]
            for message in commands:
                command(message)
            for change in ("rotate", "config", "origin"):
                before = sum(row.get("event") == "request" for row in events())
                (run / "hold").touch()
                invocation = send("/na-query current")
                wait(lambda before=before: sum(row.get("event") == "request" for row in events()) > before)
                command("/na-control " + change)
                (run / "release").touch()
                response(invocation)
                (run / "hold").unlink()
                (run / "release").unlink()
                command("/na-control reset")
                command("/na-model deepseek/fixture")
            for message in ("/na-control mismatch", "/na-query provider:openai", "/na-control reset", "/na-control duplicate", "/na-query provider:openai", "/na-control reset"):
                command(message)
            command("/na-model deepseek/fixture")
            command("/na-control fail-current")
            def source_rows() -> list[dict[str, Any]]:
                return [json.loads(line) for line in (run / "source.jsonl").read_text(encoding="utf-8").splitlines()]
            before = sum(row.get("event") == "resolve-deepseek" for row in source_rows())
            command("/na-query provider:openrouter")
            other = [row for row in events() if row.get("event") == "result"][-1]
            after = sum(row.get("event") == "resolve-deepseek" for row in source_rows())
            if before != after or other["error"] or other["outcomes"][0]["status"] != "ok":
                raise RuntimeError("explicit other acquired failing selected authentication")
            command("/na-query all")
            partial = [row for row in events() if row.get("event") == "result"][-1]
            outcomes = {row["providerId"]:row["status"] for row in partial["outcomes"] or []}
            if partial["error"] or outcomes.get("deepseek") != "failed" or outcomes.get("openrouter") != "ok":
                raise RuntimeError("selected authentication failure destroyed configured-provider partial results")
            (run / "isolation-checks.json").write_text(json.dumps({"explicitOtherAvoidedFailingSelectedResolver":True,"allPreservedPartialResultsWithFailingSelectedResolver":True}),encoding="utf-8")
            command("/na-control reset")
            scenarios = []
            def scenario(label: str, operation: Any) -> None:
                start, source_start = len(events()), len(source_rows())
                operation()
                scenarios.append({"name": label, "events": events()[start:], "source": source_rows()[source_start:]})
                (run / "scenarios.json").write_text(json.dumps(scenarios, indent=2), encoding="utf-8")
            def paused(change: str, model: str | None, kind: str = "auth") -> None:
                (run / "pause.json").write_text(json.dumps({"Kind":kind,"Provider":"openrouter" if kind == "auth" else ""}), encoding="utf-8")
                count = sum(row.get("event") == "boundary-paused" for row in events())
                invocation = send("/na-query provider:openrouter")
                wait(lambda: sum(row.get("event") == "boundary-paused" for row in events()) > count)
                command("/na-control " + change)
                if model:
                    command("/na-model " + model)
                (run / "resume").touch()
                response(invocation)
                (run / "resume").unlink()
                (run / "paused.json").unlink()
            def held(scope: str, change: str | None, model: str | None, minimum: int = 1) -> None:
                start = sum(row.get("event") == "request" for row in events())
                (run / "hold").touch()
                invocation = send("/na-query " + scope)
                wait(lambda: sum(row.get("event") == "request" for row in events()) >= start + minimum)
                if change:
                    command("/na-control " + change)
                if model:
                    command("/na-model " + model)
                if "short" not in scope:
                    (run / "release").touch()
                response(invocation)
                (run / "release").touch()
                wait(lambda: [row for row in events() if row.get("event") == "request-active"][-1]["active"] == 0)
                (run / "hold").unlink()
                (run / "release").unlink()
            def reset() -> None:
                command("/na-control reset")
                command("/na-model deepseek/fixture")
            scenario("ordinary-other", lambda: command("/na-query provider:openrouter"))
            scenario("discovery-id-change", lambda: paused("reset", "openrouter/fixture", "metadata"))
            reset()
            for change in ("config", "origin", "override"):
                scenario("pre-io-common-" + change, lambda change=change: paused(change, "deepseek/fixture"))
                reset()
            for change in ("other-config", "other-origin", "other-rotate"):
                scenario("pre-io-requested-" + change, lambda change=change: paused(change, None))
                reset()
            for change in ("config", "origin", "override"):
                scenario("return-common-" + change, lambda change=change: held("provider:openrouter", change, "deepseek/fixture"))
                reset()
            for change in ("other-config", "other-origin", "other-rotate"):
                scenario("return-requested-" + change, lambda change=change: held("provider:openrouter", change, None))
                reset()
            scenario("return-choices-common-config", lambda: held("provider:fireworks", "config", "deepseek/fixture"))
            reset()
            command("/na-control fail-all")
            scenario("all-auth-failed", lambda: command("/na-query all"))
            reset()
            scenario("cancel-http", lambda: held("current short", None, None))
            scenario("bounded-all", lambda: held("all", None, None, 2))
            print(json.dumps({"fixtureCommandsHandled": sum(row.get("command") == "prompt" and row.get("data", {}).get("disposition") == "handled" for row in rows), "results": [row for row in events() if row.get("event") == "result"]}), flush=True)
        finally:
            if process.stdin is not None:
                process.stdin.close()
            try:
                status = process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                raise
            finally:
                reader.join(timeout=2)
                resources.finish()
        if status or reader.is_alive() or failures:
            raise RuntimeError("host cleanup or runtime guard failed")
    if (run / "stderr.log").read_text(encoding="utf-8"):
        raise RuntimeError("host stderr was not empty")
    text = trace.read_text(encoding="utf-8")
    allowed = next(row["address"] for row in events() if row.get("event") == "https")
    address, port = allowed.rsplit(":", 1)
    connections = [line for line in text.splitlines() if "connect(" in line and "AF_INET" in line]
    if any(f"sin_port=htons({port})" not in line or f'inet_addr("{address}")' not in line for line in connections):
        raise RuntimeError("undeclared IP connection")
    receipts = process_receipts(text)
    adopted = []
    while True:
        try:
            pid, child_status = os.waitpid(-1, os.WNOHANG)
        except ChildProcessError:
            break
        if pid == 0:
            raise RuntimeError("unreaped adopted process")
        adopted.append({"pid": pid, "status": child_status, "waitOwner": os.getpid()})
    receipts["adoptedWaits"] = adopted
    receipts["missingConsumingWaits"] = [pid for pid in receipts["missingConsumingWaits"] if pid not in {row["pid"] for row in adopted}]
    (run / "process-receipts.json").write_text(json.dumps(receipts, indent=2), encoding="utf-8")
    if receipts["missingConsumingWaits"] or receipts["wrongWaitOwners"]:
        raise RuntimeError("missing consuming wait")
    (run / "completion.json").write_text(json.dumps({"exit": status, "noForbiddenEvents": True, "readerJoined": True, "allowedHTTPS": allowed, "IPConnections": len(connections)}), encoding="utf-8")


def main() -> int:
    sys.dont_write_bytecode = True
    tools = _frozen_module("reviewed_launcher", FROZEN / "fix-round1-1782627e/launcher.py")
    REPORTS.mkdir(parents=True, exist_ok=True)
    if len(sys.argv) == 3 and sys.argv[1] == "inner":
        _inner(json.loads(Path(sys.argv[2]).read_text(encoding="utf-8")), tools)
    elif len(sys.argv) == 2 and sys.argv[1] == "build":
        _build(tools)
    elif len(sys.argv) == 3:
        _launch(sys.argv[1], sys.argv[2], tools)
    else:
        raise SystemExit("usage: run.py build | UNIQUE_NAME process|checks|footer")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
