#!/usr/bin/env python3
import argparse
import hashlib
import json
import os
import re
import subprocess
import tempfile
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("package", type=Path)
parser.add_argument("--output", type=Path)
parser.add_argument("--verify-source-only", action="store_true")
args = parser.parse_args()
if not args.verify_source_only and args.output is None:
    parser.error("--output is required unless --verify-source-only is selected")
try:
    package = args.package.resolve(strict=True)
    metadata = json.loads((package / "package.json").read_text())
except (OSError, json.JSONDecodeError) as error:
    parser.error(f"cannot read source package: {error}")
project = Path(__file__).resolve().parent.parent
try:
    baseline = json.loads((project / "docs/compatibility/hermes-baseline.json").read_text())
except (OSError, json.JSONDecodeError) as error:
    parser.error(f"cannot read frozen baseline manifest: {error}")
if metadata["name"] != baseline["package"] or metadata["version"] != baseline["version"]:
    parser.error("package name/version does not match the frozen baseline")
patch = project / baseline["patch"]["path"]
if hashlib.sha256(patch.read_bytes()).hexdigest() != baseline["patch"]["sha256"]:
    parser.error("retained patch does not match the frozen baseline")
paths = [package / name for name in baseline["sourceFiles"]]
for name in baseline["sourceRoots"]:
    root = package / name
    if root.is_symlink():
        parser.error("baseline source roots must not be symlinks")
    for path in root.rglob("*"):
        if path.is_symlink():
            parser.error("baseline source entries must not be symlinks")
        if path.is_file():
            paths.append(path)
if any(path.is_symlink() for path in paths):
    parser.error("baseline source files must not be symlinks")
source_hashes = {path.relative_to(package).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest() for path in sorted(paths)}
pairs = [[name, digest] for name, digest in source_hashes.items()]
tree_hash = hashlib.sha256(json.dumps(pairs, separators=(",", ":"), ensure_ascii=True).encode("utf-8")).hexdigest()
if len(pairs) != baseline["sourceFileCount"] or tree_hash != baseline["sourceTreeSha256"]:
    parser.error("source tree does not match the frozen baseline; review provenance before capture")
if args.verify_source_only:
    print(f"Verified {len(pairs)} baseline files: {tree_hash}")
    raise SystemExit(0)
with tempfile.TemporaryDirectory(prefix="pig-hermes-surface-") as tmp:
    root = Path(tmp)
    env = dict(os.environ, PIG_HOME=tmp, PIG_CODING_AGENT_DIR=str(root / "agent"), PI_CODING_AGENT_DIR=str(root / "pi-agent"))
    result = subprocess.run(["pig", "install", str(package), "--validate-only", "--json"], cwd=tmp, env=env, check=True, capture_output=True, text=True, timeout=60)
    try:
        registration = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        parser.error(f"invalid PiG registration JSON: {error}")
    assert registration["valid"], registration
    extension = registration.get("extensions", [registration])[0]
    config = (package / "src/types.ts").read_text().split("export interface MemoryConfig {", 1)[1].split("\n}", 1)[0]
    output = {
        "generatedBy": "probes/capture-hermes.py",
        "package": "pi-hermes-memory", "version": "0.9.11",
        "upstreamCommit": baseline["upstreamCommit"],
        "sourceTreeSha256": tree_hash,
        "patchSha256": baseline["patch"]["sha256"],
        "tools": extension["tools"], "commands": extension["commands"], "handlers": extension["handlers"],
        "toolDetails": extension["toolDetails"], "commandDetails": extension["commandDetails"],
        "configKeys": re.findall(r"^  (\w+)\??:", config, re.MULTILINE),
        "sourceHashes": source_hashes,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(output, indent=2) + "\n")
    print(f"Captured {len(output['tools'])} tools, {len(output['commands'])} commands and {len(output['configKeys'])} config keys")
