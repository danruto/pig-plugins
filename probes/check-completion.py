#!/usr/bin/env python3
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time

extension = Path(__file__).resolve().parent / "completion-probe"
with tempfile.TemporaryDirectory(prefix="pig-hermes-completion-") as tmp:
    root = Path(tmp)
    report = root / "report.json"
    env = dict(os.environ, PIG_HOME=tmp, PIG_CODING_AGENT_DIR=str(root / "agent"), PROBE_REPORT=str(report), PROBE_RELEASE=str(root / "release"))
    with (root / "stdout").open("w+") as stdout, (root / "stderr").open("w+") as stderr:
        process = subprocess.Popen(
            ["pig", "--mode", "rpc", "--no-session", "--no-extensions", "-e", str(extension), "--model", "hermes-probe/fixture"],
            cwd=tmp, env=env, stdin=subprocess.PIPE, stdout=stdout, stderr=stderr, start_new_session=True,
        )
        try:
            deadline = time.monotonic() + 45
            while not report.exists() and process.poll() is None and time.monotonic() < deadline:
                time.sleep(0.05)
            if not report.exists():
                stderr.seek(0)
                raise RuntimeError("Completion probe did not produce a report: " + stderr.read())
            process.stdin.write(b'{"id":"foreground","type":"get_state"}\n')
            process.stdin.flush()
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                responses = [json.loads(line) for line in (root / "stdout").read_text().splitlines() if line.strip()]
                if any(value.get("id") == "foreground" and value.get("success") for value in responses):
                    break
                time.sleep(0.02)
            else:
                raise AssertionError("Background completion blocked foreground RPC")
            (root / "release").touch()
            background = Path(str(report) + ".background")
            deadline = time.monotonic() + 15
            while not background.exists() and time.monotonic() < deadline:
                time.sleep(0.02)
            assert background.exists(), "Owned background completion failed after session_start returned"
            assert json.loads(background.read_text())["content"][0]["text"] == "offline completion"
            process.communicate(timeout=15)
            assert process.returncode == 0, process.returncode
            value = json.loads(report.read_text())
            for key, result in value.items():
                print(key + ": " + json.dumps(result, ensure_ascii=False))
            for key in ("contextComplete", "registryComplete", "registryStreamSimple"):
                result = value[key]
                assert result.get("stopReason") == "stop", result
                assert result["content"][0]["text"] == "offline completion", result
            print("Owned background completion: foreground RPC responsive; completed after startup handler returned; clean shutdown")
        finally:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.communicate(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.communicate()
