#!/usr/bin/env python3
"""Freeze and verify Hermes source and executable provenance without installing packages.

The retained manifest is an observation, not a dependency solver. Installed checks
require the original Nix runtime and explicitly selected Hermes source. Ordinary
checks reconstruct source offline and verify the retained observation's integrity.
"""

import argparse
import hashlib
import json
import os
import platform
import shutil
import subprocess
import tarfile
import tempfile
from pathlib import Path
from urllib.parse import urlparse

PROJECT = Path(__file__).resolve().parent.parent
MANIFEST = PROJECT / "docs/compatibility/hermes-closure.json"
ARCHIVE = PROJECT / "docs/compatibility/hermes-upstream-0.9.11.tar.gz"
BASELINE = PROJECT / "docs/compatibility/hermes-baseline.json"
FIXTURES = (
    "probes/completion-probe/extension.go",
    "probes/completion-probe/go.mod",
    "probes/hermes-sqlite/fixture.mjs",
    "probes/check-session-format.mjs",
)


def _sha(raw: bytes) -> str:
    return hashlib.sha256(raw).hexdigest()


def _canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()


def _json(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def _run(command: list[str], *, env: dict[str, str], cwd: Path) -> str:
    return subprocess.run(
        command, env=env, cwd=cwd, check=True, capture_output=True, text=True, timeout=120
    ).stdout.strip()


def _raise_os_error(error: OSError) -> None:
    raise error


def _file_fingerprint(path: Path) -> dict:
    return {"sha256": _sha(path.read_bytes()), "executableBits": path.stat().st_mode & 0o111}


def _tree(root: Path, *, skip_modules: bool = False) -> dict:
    files = {}
    for directory, directories, names in os.walk(root, followlinks=False, onerror=_raise_os_error):
        if skip_modules:
            directories[:] = [name for name in directories if name != "node_modules"]
        for name in directories + names:
            path = Path(directory) / name
            relative = path.relative_to(root).as_posix()
            if path.is_symlink():
                raise ValueError(f"symlink in executable tree: {relative}")
            if path.is_file():
                files[relative] = _file_fingerprint(path)
    if not files:
        raise ValueError("empty executable tree")
    return {"fileCount": len(files), "treeSha256": _sha(_canonical(files)), "files": dict(sorted(files.items()))}


def _reconstruct(root: Path) -> None:
    baseline = _json(BASELINE)
    if _sha(ARCHIVE.read_bytes()) != baseline["upstreamArchive"]["sha256"]:
        raise ValueError("upstream archive drift")
    patch = PROJECT / baseline["patch"]["path"]
    if _sha(patch.read_bytes()) != baseline["patch"]["sha256"]:
        raise ValueError("retained patch drift")
    root.mkdir()
    with tarfile.open(ARCHIVE, "r:gz") as archive:
        for member in archive.getmembers():
            relative = member.name.partition("/")[2]
            selected = relative in baseline["sourceFiles"] or any(
                relative.startswith(name + "/") for name in baseline["sourceRoots"]
            )
            if not selected or member.isdir():
                continue
            target = (root / relative).resolve()
            if not member.isfile() or not target.is_relative_to(root.resolve()):
                raise ValueError("unsafe upstream archive member")
            source = archive.extractfile(member)
            if source is None:
                raise ValueError("unreadable upstream archive member")
            with source:
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(source.read())
    with patch.open("rb") as patch_input:
        subprocess.run(
            ["patch", "--batch", "--forward", "--fuzz=0", "-d", str(root), "-p1"],
            stdin=patch_input, check=True, capture_output=True, timeout=30,
        )
    result = subprocess.run(
        ["python3", str(PROJECT / "probes/capture-hermes.py"), str(root), "--verify-source-only"],
        check=True, capture_output=True, text=True, timeout=30,
    )
    print(result.stdout.strip())


def _resolve_package(origin: Path, name: str) -> Path | None:
    if name.startswith(".") or ".." in name.split("/") or name.startswith("/"):
        raise ValueError("invalid dependency name")
    for parent in (origin, *origin.parents):
        candidate = parent / "node_modules" / name
        if (candidate / "package.json").is_file():
            if candidate.is_symlink():
                raise ValueError("linked dependencies are not the frozen installed closure")
            return candidate.resolve(strict=True)
    return None


def _dependencies(package: Path, loader: Path) -> dict:
    install = package.parent.parent
    lock = _json(install / "node_modules/.package-lock.json")["packages"]
    pending = [package, loader]
    found = {}
    while pending:
        current = pending.pop()
        key = current.relative_to(install).as_posix() if current.is_relative_to(install) else "probe-loader/" + current.name
        if key in found:
            continue
        metadata = _json(current / "package.json")
        edges = {}
        requested = dict(metadata.get("dependencies", {}))
        optional = metadata.get("optionalDependencies", {})
        requested.update(optional)
        peers = metadata.get("peerDependencies", {})
        requested.update(peers)
        for name in sorted(requested):
            target = _resolve_package(current, name)
            if target is None:
                if current == package and name in peers:
                    if name not in {"@earendil-works/pi-ai", "@earendil-works/pi-coding-agent", "@earendil-works/pi-tui"}:
                        raise ValueError("unknown host-supplied peer")
                    edges[name] = {"requested": requested[name], "providedBy": "pig-node-bridge"}
                    continue
                if name in optional or metadata.get("peerDependenciesMeta", {}).get(name, {}).get("optional"):
                    edges[name] = {"requested": requested[name], "absentOptional": True}
                    continue
                raise ValueError(f"missing executable dependency: {name}")
            if not target.is_relative_to(install):
                target_key = "probe-loader/" + target.name
            else:
                target_key = target.relative_to(install).as_posix()
            edges[name] = {"requested": requested[name], "target": target_key}
            pending.append(target)
        record = {"name": metadata["name"], "version": metadata["version"], "edges": edges}
        if current != package:
            record["installed"] = _tree(current, skip_modules=True)
        if current.is_relative_to(install):
            locked = lock[key]
        else:
            probe_lock = _json(PROJECT / "package-lock.json")["packages"]
            locked = probe_lock[current.relative_to(PROJECT).as_posix()]
        url = locked["resolved"]
        parsed = urlparse(url)
        if parsed.scheme != "https" or parsed.netloc != "registry.npmjs.org" or parsed.query or parsed.fragment:
            raise ValueError("dependency provenance must be a public credential-free npm URL")
        if locked["version"] != metadata["version"] or not locked["integrity"].startswith("sha512-"):
            raise ValueError("installed package does not match lock identity")
        record["registry"] = {"url": url, "integrity": locked["integrity"]}
        found[key] = record
    return dict(sorted(found.items()))


def _executable(name: str, *, env: dict[str, str], cwd: Path) -> dict:
    located = shutil.which(name)
    if located is None:
        raise ValueError(f"missing executable: {name}")
    path = Path(located).resolve(strict=True)
    if not str(path).startswith("/nix/store/"):
        raise ValueError("this frozen executable corpus requires its original Nix runtime")
    version_args = [name, "version"] if name == "go" else [name, "--version"]
    return {"path": str(path), **_file_fingerprint(path), "version": _run(version_args, env=env, cwd=cwd)}


def _capture(package: Path, root: Path) -> dict:
    env = {"PATH": os.environ["PATH"], "HOME": str(root), "TMPDIR": str(root),
           "PIG_HOME": str(root / "pig"), "PIG_CODING_AGENT_DIR": str(root / "agent"),
           "PI_CODING_AGENT_DIR": str(root / "pi-agent"), "XDG_CONFIG_HOME": str(root / "config")}
    _run(["python3", str(PROJECT / "probes/capture-hermes.py"), str(package), "--verify-source-only"], env=env, cwd=root)
    executables = {name: _executable(name, env=env, cwd=root) for name in ("pig", "node", "npm", "go")}
    stores = sorted({"/".join(item["path"].split("/")[:4]) for item in executables.values()})
    _run(["nix", "store", "verify", "--no-trust", "--recursive", *stores], env=env, cwd=root)
    nix = json.loads(_run(["nix", "path-info", "--json", "--json-format", "1", "--recursive", *stores], env=env, cwd=root))
    nix_closure = {path: {"narHash": value["narHash"], "narSize": value["narSize"], "references": sorted(value["references"])} for path, value in sorted(nix.items())}
    sdk = Path(_run(["pig", "reload", "--sdk-path"], env=env, cwd=root))
    registration = json.loads(_run(["pig", "install", str(package), "--validate-only", "--json"], env=env, cwd=root))
    if not registration["valid"]:
        raise ValueError("Hermes registration failed")
    runtimes = list((root / "pig/cache/ext").glob("node-runtime-*/runtime"))
    if len(runtimes) != 1:
        raise ValueError("expected one observed PiG Node bridge")
    runtime = runtimes[0]
    sqlite = json.loads(_run(["node", "--input-type=module", "-e", '''
import { createRequire } from 'node:module';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { relative } from 'node:path';
const require = createRequire(process.argv[1] + '/package.json');
const db = require('better-sqlite3')(':memory:');
try {
  const native = Object.keys(require.cache).filter(path => path.endsWith('.node'));
  if (native.length !== 1) throw new Error('expected one loaded SQLite native module');
  console.log(JSON.stringify({node: process.version, versions: process.versions,
    nativeModule: {path: relative(process.argv[1] + '/..', native[0]),
      sha256: createHash('sha256').update(readFileSync(native[0])).digest('hex')},
    platform: process.platform, arch: process.arch,
    sqlite: db.prepare('SELECT sqlite_version() AS version, sqlite_source_id() AS sourceId').get(),
    compileOptions: db.pragma('compile_options').map(row => row.compile_options).sort()}));
} finally { db.close(); }
''', str(package)], env=env, cwd=root))
    bundled = {}
    for path in sorted(runtime.rglob("package.json")):
        metadata = _json(path)
        if "name" in metadata and "version" in metadata:
            bundled[path.relative_to(runtime).as_posix()] = {"name": metadata["name"], "version": metadata["version"]}
    go_env = dict(env, GOTOOLCHAIN="local", GOPROXY="off", GOSUMDB="off", GOFLAGS="-mod=readonly -p=2", GOMAXPROCS="2",
                  GOCACHE=str(root.parent / "go-cache"),
                  GOPATH=_run(["go", "env", "GOPATH"], env=dict(os.environ), cwd=root))
    module = PROJECT / "probes/hermes-sqlite"
    _run(["go", "mod", "verify"], env=go_env, cwd=module)
    decoder = json.JSONDecoder()
    remaining = _run(["go", "list", "-m", "-json", "all"], env=go_env, cwd=module)
    modules = []
    while remaining:
        item, end = decoder.raw_decode(remaining)
        modules.append({key: item[key] for key in ("Path", "Version", "Sum", "GoModSum") if key in item})
        remaining = remaining[end:].lstrip()
    go_query = root / "sqlite.go"
    go_query.write_text('''package main
import ("database/sql"; "encoding/json"; "os"; _ "modernc.org/sqlite")
func main() {
 db, err := sql.Open("sqlite", ":memory:"); if err != nil { panic(err) }; defer db.Close()
 var version, source string
 if err := db.QueryRow("SELECT sqlite_version(), sqlite_source_id()").Scan(&version, &source); err != nil { panic(err) }
 if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"version":version,"sourceId":source}); err != nil { panic(err) }
}
''', encoding="utf-8")
    native_sqlite = json.loads(_run(["go", "run", str(go_query)], env=go_env, cwd=module))
    baseline = _json(BASELINE)
    return {
        "generatedBy": "probes/hermes-closure.py --capture (GENERATED FROM CODE; DO NOT EDIT)",
        "formatVersion": 2,
        "source": {"baselineSha256": _sha(BASELINE.read_bytes()), "sourceTreeSha256": baseline["sourceTreeSha256"],
                   "archiveSha256": _sha(ARCHIVE.read_bytes()), "patchSha256": baseline["patch"]["sha256"]},
        "configuration": {"observation": "isolated default registration; SQLite in-memory identity query",
                          "provider": "none; no model requests", "personalData": False,
                          "fixtures": {name: _sha((PROJECT / name).read_bytes()) for name in FIXTURES}},
        "dependencies": _dependencies(package, PROJECT / "node_modules/tsx"),
        "executables": executables, "nixClosure": nix_closure,
        "sdk": {"identity": sdk.name, "pigVersion": executables["pig"]["version"], **_tree(sdk)},
        "nodeBridge": {"identity": runtime.parent.name, "bundledPackages": bundled, **_tree(runtime)},
        "engine": sqlite,
        "goProbe": {"modules": modules, "sqlite": native_sqlite,
                    "files": {name: _sha((module / name).read_bytes()) for name in ("go.mod", "go.sum")}},
        "host": {"system": platform.system(), "machine": platform.machine()},
    }


def _verify_artifacts() -> tuple[dict, str]:
    raw = MANIFEST.read_bytes()
    digest = _sha(raw)
    if MANIFEST.with_suffix(".sha256").read_text(encoding="utf-8").strip() != digest:
        raise ValueError("executable closure manifest drift")
    manifest = json.loads(raw)
    baseline = _json(BASELINE)
    expected = {"baselineSha256": _sha(BASELINE.read_bytes()), "sourceTreeSha256": baseline["sourceTreeSha256"],
                "archiveSha256": _sha(ARCHIVE.read_bytes()), "patchSha256": _sha((PROJECT / baseline["patch"]["path"]).read_bytes())}
    if manifest["source"] != expected:
        raise ValueError("closure source provenance drift")
    fixtures = {name: _sha((PROJECT / name).read_bytes()) for name in FIXTURES}
    if manifest["configuration"]["fixtures"] != fixtures:
        raise ValueError("closure fixture provenance drift")
    if manifest["goProbe"]["files"] != {name: _sha((PROJECT / "probes/hermes-sqlite" / name).read_bytes()) for name in ("go.mod", "go.sum")}:
        raise ValueError("Go probe dependency provenance drift")
    if manifest["formatVersion"] != 2 or not manifest["dependencies"] or not manifest["nixClosure"]:
        raise ValueError("incomplete executable closure")
    for record in [manifest["sdk"], manifest["nodeBridge"], *(item["installed"] for item in manifest["dependencies"].values() if "installed" in item)]:
        if record["fileCount"] != len(record["files"]) or record["treeSha256"] != _sha(_canonical(record["files"])):
            raise ValueError("executable tree fingerprint drift")
    return manifest, digest


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group(required=True)
    modes.add_argument("--check", action="store_true")
    modes.add_argument("--capture", action="store_true")
    modes.add_argument("--verify-installed", action="store_true")
    modes.add_argument("--observe", nargs=argparse.REMAINDER)
    parser.add_argument("--package", type=Path, default=os.environ.get("HERMES_PACKAGE"))
    args = parser.parse_args()
    if not args.check and args.package is None:
        parser.error("--package or HERMES_PACKAGE is required; no default personal installation is selected")
    with tempfile.TemporaryDirectory(prefix="hermes-closure-") as temporary:
        root = Path(temporary)
        _reconstruct(root / "source")
        if args.check:
            source_env = dict(os.environ, HERMES_PACKAGE=str(root / "source"))
            subprocess.run(["python3", str(PROJECT / "probes/test-capture-hermes.py")], env=source_env, check=True, timeout=30)
        if args.capture:
            manifest = _capture(args.package.resolve(strict=True), root)
            raw = (json.dumps(manifest, indent=2, ensure_ascii=True) + "\n").encode()
            MANIFEST.write_bytes(raw)
            MANIFEST.with_suffix(".sha256").write_text(_sha(raw) + "\n", encoding="utf-8")
        manifest, digest = _verify_artifacts()
        if args.check:
            surface = _json(PROJECT / "docs/compatibility/hermes-surface.json")
            if surface.get("executableClosureSha256") != digest:
                raise ValueError("registration observation closure provenance drift")
            if surface["sourceHashes"] != {name: value["sha256"] for name, value in _tree(root / "source")["files"].items()}:
                raise ValueError("registration observation source fingerprints drift")
        if args.verify_installed or args.observe is not None:
            observed = _capture(args.package.resolve(strict=True), root)
            differences = [key for key in manifest if manifest[key] != observed.get(key)]
            if differences:
                raise ValueError("executable closure drift: " + ", ".join(differences))
        print(f"Hermes executable closure {digest}: {len(manifest['dependencies'])} packages; "
              f"PiG {manifest['executables']['pig']['version']}; Node {manifest['engine']['node']}; "
              f"SQLite {manifest['engine']['sqlite']['version']}; SDK {manifest['sdk']['identity']}")
        if args.capture:
            print("Installed executable closure captured", flush=True)
        elif args.verify_installed or args.observe is not None:
            print("Installed executable closure verified", flush=True)
        else:
            print("Retained provenance verified (installed bytes not checked)", flush=True)
        if args.observe is not None:
            command = args.observe[1:] if args.observe[:1] == ["--"] else args.observe
            if not command:
                parser.error("--observe requires a command")
            observation_env = {"PATH": os.environ["PATH"], "HOME": str(root), "TMPDIR": str(root),
                               "PIG_HOME": str(root / "observation-pig"), "PIG_CODING_AGENT_DIR": str(root / "observation-agent"),
                               "PI_CODING_AGENT_DIR": str(root / "observation-pi-agent"),
                               "HERMES_PACKAGE": str(args.package.resolve(strict=True)),
                               "HERMES_EXECUTABLE_CLOSURE_SHA256": digest,
                               "GOMAXPROCS": "2", "GOFLAGS": "-mod=readonly -p=2", "GOPROXY": "off", "GOSUMDB": "off",
                               "GOTOOLCHAIN": "local", "GOCACHE": str(root.parent / "go-cache"),
                               "GOPATH": _run(["go", "env", "GOPATH"], env=dict(os.environ), cwd=root)}
            subprocess.run(command, env=observation_env, cwd=PROJECT, check=True, timeout=300)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
