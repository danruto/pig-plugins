#!/usr/bin/env python3
"""Assert the layout of a built pig-status package: manifest, native footer, no Node runtime, no pinned SDK source path, payload."""

import json
import os
import re
import stat
import sys
from collections.abc import Callable
from pathlib import Path

PACKAGED_EXTENSIONS = ["bin/pig-status", "extensions/pig-openai-server-compaction.ts"]
REQUIRED_FILES = [
    "compaction/generated/index.ts",
    "compaction/generated/LICENSE.md",
    "compaction/payload.ts",
    "LICENSE",
    "THIRD_PARTY_NOTICES.md",
]
NATIVE_MAGIC = (
    b"\x7fELF",
    b"\xca\xfe\xba\xbe",
    b"\xcf\xfa\xed\xfe",
    b"\xce\xfa\xed\xfe",
    b"\xfe\xed\xfa\xcf",
    b"\xfe\xed\xfa\xce",
)
NODE_RUNTIME_REFERENCE = re.compile(rb"/nix/store/[0-9a-z]{32}-nodejs")
REMOVED_REFERENCE_HASH = b"e" * 32
SDK_SOURCE_REFERENCE = re.compile(rb"/nix/store/(?!" + REMOVED_REFERENCE_HASH + rb"-)[0-9a-z]{32}-source")
SCAN_CHUNK = 1 << 20
SCAN_OVERLAP = len(b"/nix/store/") + 32 + max(len("-nodejs"), len("-source"))

Check = Callable[[Path, dict[str, object]], list[str]]


def keywords_problems(package: Path, manifest: dict[str, object]) -> list[str]:
    keywords = manifest.get("keywords", [])
    if isinstance(keywords, list) and "pig-package" in keywords:
        return []
    return [f"keywords is {keywords}, expected it to list pig-package"]


def extension_entries(manifest: dict[str, object]) -> object:
    pi = manifest.get("pi")
    return pi.get("extensions") if isinstance(pi, dict) else None


def membership_problems(package: Path, manifest: dict[str, object]) -> list[str]:
    declared = extension_entries(manifest)
    if declared != PACKAGED_EXTENSIONS:
        return [f"pi.extensions is {declared}, expected {PACKAGED_EXTENSIONS}"]
    missing = [entry for entry in PACKAGED_EXTENSIONS if not (package / entry).exists()]
    return [f"pi.extensions names paths missing from the package: {missing}"] if missing else []


def binary_problems(package: Path, manifest: dict[str, object]) -> list[str]:
    footer = package / "bin/pig-status"
    if footer.is_symlink() or not footer.is_file():
        return [f"bin/pig-status is not a regular file in {package}"]
    mode = footer.stat().st_mode
    if not stat.S_ISREG(mode) or not mode & stat.S_IXUSR:
        return [f"bin/pig-status is not executable (mode {stat.filemode(mode)})"]
    with footer.open("rb") as stream:
        magic = stream.read(4)
    if magic not in NATIVE_MAGIC:
        return [f"bin/pig-status does not start with ELF or Mach-O magic: {magic!r}"]
    return []


def node_modules_problems(package: Path, manifest: dict[str, object]) -> list[str]:
    shipped = sorted(str(path.relative_to(package)) for path in package.rglob("node_modules") if path.is_dir())
    return [f"package ships node_modules: {shipped}"] if shipped else []


def references(path: Path, pattern: re.Pattern[bytes]) -> bool:
    tail = b""
    with path.open("rb") as stream:
        while chunk := stream.read(SCAN_CHUNK):
            if pattern.search(tail + chunk):
                return True
            tail = chunk[-SCAN_OVERLAP:]
    return False


def closure_problems(package: Path, manifest: dict[str, object]) -> list[str]:
    offenders = []
    for path in sorted(package.rglob("*")):
        if path.is_symlink():
            if NODE_RUNTIME_REFERENCE.search(os.readlink(path).encode()):
                offenders.append(str(path.relative_to(package)))
        elif path.is_file() and references(path, NODE_RUNTIME_REFERENCE):
            offenders.append(str(path.relative_to(package)))
    return [f"package files reference a nodejs runtime: {offenders}"] if offenders else []


def sdk_source_problems(package: Path, manifest: dict[str, object]) -> list[str]:
    footer = package / "bin/pig-status"
    if not footer.is_file():
        return [f"bin/pig-status is missing from {package}, so the pinned SDK source scan proves nothing"]
    return ["bin/pig-status references a pinned -source store path"] if references(footer, SDK_SOURCE_REFERENCE) else []


def payload_problems(package: Path, manifest: dict[str, object]) -> list[str]:
    missing = [name for name in REQUIRED_FILES if not (package / name).is_file()]
    return [f"package is missing the compaction payload: {missing}"] if missing else []


CHECKS: list[tuple[str, Check]] = [
    ("manifest keyword", keywords_problems),
    ("extension membership", membership_problems),
    ("footer binary", binary_problems),
    ("no node_modules", node_modules_problems),
    ("no nodejs runtime reference", closure_problems),
    ("no sdk source reference", sdk_source_problems),
    ("compaction payload", payload_problems),
]


def package_path(argv: list[str]) -> Path:
    if len(argv) > 1:
        return Path(argv[1]).resolve()
    configured = os.environ.get("PIG_STATUS_PACKAGE")
    if not configured:
        raise SystemExit("usage: check-package.py <package-dir> (or set PIG_STATUS_PACKAGE)")
    return Path(configured).resolve()


def read_manifest(package: Path) -> dict[str, object] | None:
    manifest = package / "package.json"
    if not manifest.is_file():
        return None
    parsed = json.loads(manifest.read_text(encoding="utf-8"))
    return parsed if isinstance(parsed, dict) else None


def main() -> int:
    package = package_path(sys.argv)
    manifest = read_manifest(package)
    if manifest is None:
        print(f"FAIL package.json: {package}/package.json is missing or not an object")
        return 1

    failed = 0
    for name, check in CHECKS:
        problems = check(package, manifest)
        if problems:
            failed += 1
            print(f"FAIL {name}: {'; '.join(problems)}")
        else:
            print(f"PASS {name}")
    if failed:
        print(f"package layout: {failed} of {len(CHECKS)} checks failed for {package}")
        return 1
    print(f"package layout: {len(CHECKS)} of {len(CHECKS)} checks passed for {package}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
