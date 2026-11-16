#!/usr/bin/env python3
"""Package-membership checks for the native usage cutover."""

import json
import os
import select
import subprocess
import tempfile
import time
from pathlib import Path

project = Path(__file__).resolve().parent.parent
package = Path(os.environ.get("PIG_STATUS_PACKAGE", project)).resolve()

PACKAGED_EXTENSIONS = ["extensions/pig-status", "extensions/pig-openai-server-compaction.ts"]
DISCOVERED_EXTENSIONS = ["pig-openai-server-compaction", "pig-status"]
OWNED_COMMANDS = {"fast", "usage"}


def disposable_env(root: str) -> dict[str, str]:
    env = dict(
        os.environ,
        HOME=root,
        PIG_HOME=root,
        PIG_CODING_AGENT_DIR=str(Path(root) / "agent"),
        PI_CODING_AGENT_DIR=str(Path(root) / "pi-agent"),
        PI_HOME=str(Path(root) / "pi"),
    )
    (Path(root) / "agent").mkdir(parents=True, exist_ok=True)
    return env


def extension_name(path: str) -> str:
    name = Path(path).name
    return name.split(".", 1)[0] if "." in name else name


def rpc_responses(process: subprocess.Popen, ids: set[str]) -> dict[str, dict]:
    output = process.stdout
    assert output is not None
    responses: dict[str, dict] = {}
    buffer = bytearray()
    deadline = time.monotonic() + 120
    while len(responses) < len(ids):
        remaining = deadline - time.monotonic()
        assert remaining > 0, f"PiG RPC answered only {sorted(responses)}"
        if not select.select([output], [], [], remaining)[0]:
            continue
        chunk = os.read(output.fileno(), 65536)
        assert chunk, f"PiG RPC closed its output after {sorted(responses)}"
        buffer.extend(chunk)
        while b"\n" in buffer:
            line, _, rest = bytes(buffer).partition(b"\n")
            buffer = bytearray(rest)
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                continue
            if record.get("type") == "response" and record.get("id") in ids:
                responses[record["id"]] = record
    for response in responses.values():
        assert response.get("success") is True, response
    return responses


def check_metadata() -> None:
    manifest = json.loads((package / "package.json").read_text())

    assert "@narumitw/pi-usage" not in manifest.get("dependencies", {}), manifest.get("dependencies", {})
    assert manifest["devDependencies"]["@narumitw/pi-usage"] == "0.68.3", manifest["devDependencies"]
    assert manifest["overrides"]["@narumitw/pi-tui-kit"] == "0.67.0", manifest["overrides"]
    assert manifest["pi"]["extensions"] == PACKAGED_EXTENSIONS, manifest["pi"]["extensions"]
    missing = [entry for entry in PACKAGED_EXTENSIONS + manifest.get("files", []) if not (package / entry).exists()]

    assert not missing, f"package.json names missing paths: {missing}"
    print("Package metadata: no runtime usage dependency, two packaged extensions, every listed path present")


def check_discovery() -> None:
    with tempfile.TemporaryDirectory(prefix="pig-status-discovery-") as root:
        env = disposable_env(root)
        discovered = Path(root) / "agent" / "extensions"
        discovered.mkdir(parents=True, exist_ok=True)
        (discovered / "pig-status").symlink_to(package)

        result = subprocess.run(
            ["pig", "status", "--json"], cwd=root, env=env, capture_output=True, text=True, timeout=120,
        )

        assert result.returncode == 0, result.stderr or result.stdout
        resources = json.loads(result.stdout)["resources"]["items"]
        extensions = [item for item in resources if item["kind"] == "extensions"]
        names = sorted(item["name"] for item in extensions)
        footers = [item for item in extensions if item["name"] == "pig-status"]

        assert names == DISCOVERED_EXTENSIONS, names
        assert len(footers) == 1, footers
        assert footers[0]["enabled"] is True, footers[0]
        assert Path(footers[0]["path"]).resolve() == (package / "extensions/pig-status").resolve(), footers[0]["path"]
        print("Discovery: one enabled pig-status extension, no pig-status-usage collector")


def check_registration() -> None:
    with tempfile.TemporaryDirectory(prefix="pig-status-registration-") as root:
        process = subprocess.Popen(
            ["pig", "--mode", "rpc", "--no-session", "--approve", "--no-extensions", "-e", str(package)],
            cwd=root,
            env=disposable_env(root),
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        input_stream = process.stdin
        errors = process.stderr
        assert input_stream is not None
        assert errors is not None
        try:
            input_stream.write(b'{"id":"commands","type":"get_commands"}\n{"id":"extensions","type":"get_extensions"}\n')
            input_stream.flush()
            responses = rpc_responses(process, {"commands", "extensions"})
        finally:
            input_stream.close()
            try:
                process.wait(timeout=60)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=60)

        assert process.returncode == 0, errors.read().decode(errors="replace")
        commands = responses["commands"]["data"]["commands"]
        names = [command["name"] for command in commands]
        owned = [command for command in commands if command["name"] in OWNED_COMMANDS]

        assert names.count("usage") == 1, names
        assert names.count("fast") == 1, names
        assert {extension_name(command["sourceInfo"]["path"]) for command in owned} == {"pig-status"}, owned
        assert not [command for command in owned if Path(command["sourceInfo"]["path"]).suffix], owned
        assert sorted(Path(path).resolve() for path in responses["extensions"]["data"]["paths"]) == sorted(
            (package / entry).resolve() for entry in PACKAGED_EXTENSIONS
        ), responses["extensions"]["data"]["paths"]
        print("Registration: usage and fast registered once each, both from the pig-status extension")


def main() -> None:
    check_metadata()
    check_discovery()
    check_registration()


if __name__ == "__main__":
    main()
