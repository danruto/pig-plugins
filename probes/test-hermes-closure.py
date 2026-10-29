#!/usr/bin/env python3
"""Exercise frozen Hermes provenance through real CLIs using disposable copies.

Run with HERMES_PACKAGE selecting source, TMPDIR selecting objective scratch,
and python3 probes/test-hermes-closure.py -v. No live memory roots are used.
"""

import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from collections.abc import Callable
from pathlib import Path

PROJECT = Path(__file__).resolve().parent.parent
MANIFEST = Path("docs/compatibility/hermes-closure.json")
RECEIPT = Path("docs/compatibility/hermes-closure.sha256")


def _append(path: Path) -> None:
    with path.open("ab") as stream:
        stream.write(b"\n")


def _set_json(path: Path, field: str, value: object) -> None:
    data = json.loads(path.read_text(encoding="utf-8"))
    target = data
    keys = field.split("::")
    for key in keys[:-1]:
        target = target[key]
    target[keys[-1]] = value
    path.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")


def _rewrite_manifest(project: Path, field: str) -> None:
    path = project / MANIFEST
    _set_json(path, field, "0" * 64)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    (project / RECEIPT).write_text(
        digest + "\n", encoding="utf-8"
    )
    _set_json(
        project / "docs/compatibility/hermes-surface.json",
        "executableClosureSha256",
        digest,
    )


class HermesClosureTests(unittest.TestCase):
    def setUp(self) -> None:
        scratch = Path(os.environ["TMPDIR"]).resolve(strict=True)
        temporary = tempfile.TemporaryDirectory(dir=scratch, prefix="hermes-closure-test-")
        self.addCleanup(temporary.cleanup)
        self.scratch = Path(temporary.name)
        self.env = {
            "PATH": os.environ["PATH"],
            "HOME": str(self.scratch / "home"),
            "TMPDIR": str(self.scratch),
            "XDG_CACHE_HOME": str(self.scratch / "cache"),
            "PI_CODING_AGENT_DIR": str(self.scratch / "pi-agent"),
            "PIG_CODING_AGENT_DIR": str(self.scratch / "pig-agent"),
            "PI_HOME": str(self.scratch / "pi-home"),
            "PIG_HOME": str(self.scratch / "pig-home"),
            "GOMAXPROCS": "2",
            "GOFLAGS": "-p=2",
            "GOCACHE": str(self.scratch / "go-cache"),
            "GOPROXY": "off",
            "GOSUMDB": "off",
            "npm_config_update_notifier": "false",
            "npm_config_offline": "true",
        }
        module_cache = subprocess.check_output(
            ["go", "env", "GOMODCACHE"], text=True, timeout=10
        ).strip()
        cache_link = self.scratch / "home/go/pkg/mod"
        cache_link.parent.mkdir(parents=True)
        cache_link.symlink_to(module_cache, target_is_directory=True)

    def _run(
        self,
        command: list[str],
        *,
        project: Path = PROJECT,
        package: Path | None = None,
        extra_env: dict[str, str] | None = None,
    ) -> subprocess.CompletedProcess[str]:
        env = dict(self.env)
        if package is not None:
            env["HERMES_PACKAGE"] = str(package)
        env.update(extra_env or {})
        return subprocess.run(
            command,
            cwd=project,
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            timeout=240,
        )

    def _closure(
        self, *args: str, project: Path = PROJECT, package: Path | None = None
    ) -> subprocess.CompletedProcess[str]:
        return self._run(
            [sys.executable, "probes/hermes-closure.py", *args],
            project=project,
            package=package,
        )

    def _assert_check(self, project: Path = PROJECT) -> subprocess.CompletedProcess[str]:
        result = self._closure("--check", project=project)
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn("Retained provenance verified", result.stdout)
        return result

    def _copy_project(self) -> Path:
        destination = self.scratch / "project"
        shutil.copytree(
            PROJECT,
            destination,
            symlinks=True,
            ignore=shutil.ignore_patterns(
                ".git", ".pb", "node_modules", "result*", "outputs", "__pycache__"
            ),
        )
        shutil.copytree(PROJECT / "node_modules", destination / "node_modules", symlinks=True)
        return destination

    def _copy_installation(self) -> Path:
        source = Path(os.environ["HERMES_PACKAGE"]).resolve(strict=True)
        installation = source.parent.parent
        destination = self.scratch / "installation"
        manifest = json.loads((PROJECT / MANIFEST).read_text(encoding="utf-8"))
        lock = json.loads(
            (installation / "node_modules/.package-lock.json").read_text(encoding="utf-8")
        )
        packages = {}
        for key in manifest["dependencies"]:
            if key.startswith("node_modules/"):
                shutil.copytree(
                    installation / key, destination / key, symlinks=True
                )
                packages[key] = lock["packages"][key]
        (destination / "node_modules/.package-lock.json").write_text(
            json.dumps({"lockfileVersion": 3, "packages": packages}), encoding="utf-8"
        )
        return destination / "node_modules/pi-hermes-memory"

    def _assert_installed(self, package: Path) -> None:
        result = self._closure("--verify-installed", package=package)
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn("Installed executable closure verified", result.stdout)

    def test_h1_check_reconstructs_source_without_claiming_installed_verification(self) -> None:
        result = self._assert_check()
        baseline = json.loads(
            (PROJECT / "docs/compatibility/hermes-baseline.json").read_text(encoding="utf-8")
        )
        receipt = (PROJECT / RECEIPT).read_text(encoding="utf-8").split()[0]

        self.assertIn(baseline["sourceTreeSha256"], result.stdout)
        self.assertIn(receipt, result.stdout)
        self.assertIn("installed bytes not checked", result.stdout)

    def test_h1_check_rejects_source_manifest_drift(self) -> None:
        self._assert_check()
        project = self._copy_project()
        _set_json(project / "docs/compatibility/hermes-baseline.json", "sourceTreeSha256", "0" * 64)

        result = self._closure("--check", project=project)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertRegex(result.stdout, "baseline|source")

    def test_h1_check_rejects_registration_closure_link_drift(self) -> None:
        self._assert_check()
        project = self._copy_project()
        _set_json(
            project / "docs/compatibility/hermes-surface.json",
            "executableClosureSha256",
            "0" * 64,
        )

        result = self._closure("--check", project=project)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertRegex(result.stdout, "closure|surface")

    def test_h1_check_rejects_receipt_drift(self) -> None:
        self._assert_check()
        project = self._copy_project()
        (project / RECEIPT).write_text("0" * 64 + "  hermes-closure.json\n", encoding="utf-8")

        result = self._closure("--check", project=project)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertRegex(result.stdout, "receipt|digest|SHA|closure")

    def test_h1_installed_rejects_dependency_version_drift(self) -> None:
        self._assert_check()
        package = self._copy_installation()
        self._assert_installed(package)
        _set_json(package.parent / "ansi-regex/package.json", "version", "0.0.0-unreviewed")

        result = self._closure("--verify-installed", package=package)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertRegex(
            result.stdout,
            "installed package does not match lock identity|executable closure drift: dependencies",
        )

    def test_h1_installed_rejects_nix_nar_identity_misrepresentation(self) -> None:
        self._assert_check()
        project = self._copy_project()
        package = self._copy_installation()
        self._assert_installed(package)
        manifest = json.loads((project / MANIFEST).read_text(encoding="utf-8"))
        store_path = next(iter(manifest["nixClosure"]))
        _rewrite_manifest(project, f"nixClosure::{store_path}::narHash")

        result = self._closure("--verify-installed", project=project, package=package)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("executable closure drift: nixClosure", result.stdout)

    def test_h1_installed_rejects_probe_loader_byte_drift(self) -> None:
        self._assert_check()
        project = self._copy_project()
        package = self._copy_installation()
        self._assert_installed(package)
        _append(project / "node_modules/tsx/LICENSE")

        result = self._closure("--verify-installed", project=project, package=package)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("executable closure drift: dependencies", result.stdout)

    def test_h1_retained_artifacts_exclude_personal_roots(self) -> None:
        self._assert_check()
        retained = (PROJECT / MANIFEST).read_text(encoding="utf-8")
        surface = (PROJECT / "docs/compatibility/hermes-surface.json").read_text(encoding="utf-8")

        self.assertNotRegex(retained + surface, r"/home/|/Users/|synthetic-not-a-credential")
        self.assertIn('"personalData": false', retained)
        self.assertIn('"provider": "none; no model requests"', retained)

    def test_h1_installed_verifies_real_copied_dependencies(self) -> None:
        self._assert_check()
        package = self._copy_installation()

        self._assert_installed(package)

    def test_h1_observe_isolates_roots_and_credentials_after_verification(self) -> None:
        self._assert_check()
        package = self._copy_installation()
        code = (
            "import json,os; from pathlib import Path; "
            "print('CHILD='+json.dumps({'roots':{k:os.environ[k] for k in "
            "['HOME','PI_CODING_AGENT_DIR','PIG_CODING_AGENT_DIR']},"
            "'offline':{k:os.environ.get(k) for k in ['GOPROXY','GOSUMDB']},"
            "'credentials':{k:v for k,v in os.environ.items() if k in "
            "['OPENAI_API_KEY','ANTHROPIC_API_KEY','AWS_SECRET_ACCESS_KEY']}}),flush=True)"
        )

        result = self._run(
            [sys.executable, "probes/hermes-closure.py", "--observe", sys.executable, "-c", code],
            package=package,
            extra_env={
                "OPENAI_API_KEY": "synthetic-not-a-credential",
                "ANTHROPIC_API_KEY": "synthetic-not-a-credential",
                "AWS_SECRET_ACCESS_KEY": "synthetic-not-a-credential",
            },
        )

        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertLess(
            result.stdout.index("Hermes executable closure"), result.stdout.index("CHILD=")
        )
        child = json.loads(result.stdout.split("CHILD=", 1)[1].splitlines()[0])
        self.assertEqual(child["credentials"], {})
        self.assertEqual(child["offline"], {"GOPROXY": "off", "GOSUMDB": "off"})
        self.assertEqual(
            {
                key: Path(value).is_relative_to(self.scratch)
                for key, value in child["roots"].items()
            },
            {"HOME": True, "PI_CODING_AGENT_DIR": True, "PIG_CODING_AGENT_DIR": True},
        )

    def test_h1_observe_reports_child_failure(self) -> None:
        self._assert_check()
        package = self._copy_installation()

        result = self._closure(
            "--observe", sys.executable, "-c",
            "print('CHILD-RAN'); raise SystemExit(7)", package=package
        )

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("CHILD-RAN", result.stdout)
        self.assertIn("Hermes executable closure", result.stdout)

    def test_h1_observe_refuses_drift_before_child_execution(self) -> None:
        self._assert_check()
        package = self._copy_installation()
        self._assert_installed(package)
        marker = self.scratch / "must-not-run"
        _append(package.parent / "ansi-regex/index.js")

        result = self._closure(
            "--observe", sys.executable, "-c",
            f"from pathlib import Path; Path({str(marker)!r}).touch()", package=package
        )

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertRegex(result.stdout, "closure|dependencies|dependency")
        self.assertFalse(marker.exists())

    def test_h1_registration_capture_is_bound_to_verified_closure(self) -> None:
        self._assert_check()
        package = self._copy_installation()
        output = self.scratch / "surface.json"

        result = self._run(
            [sys.executable, "probes/capture-hermes.py", str(package), "--output", str(output)]
        )

        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertEqual(
            output.read_bytes(),
            (PROJECT / "docs/compatibility/hermes-surface.json").read_bytes(),
        )

    def test_h1_registration_refuses_dependency_drift(self) -> None:
        self._assert_check()
        package = self._copy_installation()
        self._assert_installed(package)
        output = self.scratch / "rejected-surface.json"
        _append(package.parent / "ansi-regex/index.js")

        result = self._run(
            [sys.executable, "probes/capture-hermes.py", str(package), "--output", str(output)]
        )

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertRegex(result.stdout, "closure|dependencies|dependency")
        self.assertFalse(output.exists())

    def test_h1_npm_rejects_retained_patch_drift(self) -> None:
        self._assert_check()
        project = self._copy_project()
        _append(project / "patches/hermes-0.9.11-async-integrity.patch")

        result = self._run(["npm", "test"], project=project)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertRegex(result.stdout, "[Pp]atch.*(match|hash|drift)|provenance")
        self.assertNotIn("Trust project folder?", result.stdout)

    def test_h1_npm_rejects_selected_installed_dependency_drift(self) -> None:
        self._assert_check()
        project = self._copy_project()
        package = self._copy_installation()
        self._assert_installed(package)
        _append(package.parent / "ansi-regex/index.js")

        result = self._run(["npm", "test"], project=project, package=package)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertRegex(result.stdout, "[Ii]nstalled.*(closure|differ)|dependencies")
        self.assertNotIn("Trust project folder?", result.stdout)

    def test_h1_nix_package_rejects_retained_patch_drift(self) -> None:
        self._assert_check()
        project = self._copy_project()
        command = [
            "nix", "build", "--offline", "--no-link", "-L", f"path:{project}#pig-status"
        ]
        clean = self._run(command, project=project)
        self.assertEqual(clean.returncode, 0, clean.stdout)
        _append(project / "patches/hermes-0.9.11-async-integrity.patch")

        result = self._run(command, project=project)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertRegex(result.stdout, "[Pp]atch.*(match|hash|drift)|provenance")


def _retained_drift(relative: str, reason: str) -> Callable[[HermesClosureTests], None]:
    def test(self: HermesClosureTests) -> None:
        self._assert_check()
        project = self._copy_project()
        _append(project / relative)

        result = self._closure("--check", project=project)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertRegex(result.stdout, reason)

    return test


def _fingerprint_drift(field: str) -> Callable[[HermesClosureTests], None]:
    def test(self: HermesClosureTests) -> None:
        self._assert_check()
        project = self._copy_project()
        _rewrite_manifest(project, field)

        result = self._closure("--check", project=project)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertRegex(result.stdout, "fingerprint|digest|tree|provenance")
        self.assertNotIn("executable closure manifest drift", result.stdout)

    return test


def _installed_drift(relative: str) -> Callable[[HermesClosureTests], None]:
    def test(self: HermesClosureTests) -> None:
        self._assert_check()
        package = self._copy_installation()
        self._assert_installed(package)
        _append(package.parent / relative)

        result = self._closure("--verify-installed", package=package)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertRegex(result.stdout, "closure|dependencies|dependency|package")

    return test


def _installed_identity_drift(field: str) -> Callable[[HermesClosureTests], None]:
    def test(self: HermesClosureTests) -> None:
        self._assert_check()
        project = self._copy_project()
        package = self._copy_installation()
        self._assert_installed(package)
        _rewrite_manifest(project, field)

        result = self._closure("--verify-installed", project=project, package=package)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("executable closure drift:", result.stdout)

    return test


for _name, _field in (
    ("runtime_binary", "executables::node::sha256"),
    ("npm_version", "executables::npm::version"),
    ("go_version", "executables::go::version"),
    ("sdk_identity", "sdk::identity"),
    ("bridge_identity", "nodeBridge::identity"),
    ("typescript_sqlite", "engine::sqlite::sourceId"),
    ("go_sqlite", "goProbe::sqlite::sourceId"),
    (
        "peer_graph",
        "dependencies::node_modules/pi-hermes-memory::edges::@earendil-works/pi-ai::requested",
    ),
):
    setattr(
        HermesClosureTests,
        f"test_h1_installed_rejects_{_name}_misrepresentation",
        _installed_identity_drift(_field),
    )


for _name, _relative, _reason in (
    ("archive", "docs/compatibility/hermes-upstream-0.9.11.tar.gz", "[Aa]rchive"),
    ("patch", "patches/hermes-0.9.11-async-integrity.patch", "[Pp]atch"),
    ("fixture", "probes/hermes-sqlite/fixture.mjs", "fixture|[Pp]rovenance"),
    ("go_module", "probes/hermes-sqlite/go.mod", "go.mod|[Pp]rovenance"),
):
    setattr(
        HermesClosureTests,
        f"test_h1_check_rejects_{_name}_drift",
        _retained_drift(_relative, _reason),
    )

for _name, _field in (
    ("sdk", "sdk::treeSha256"),
    ("bridge", "nodeBridge::treeSha256"),
    ("dependency", "dependencies::node_modules/ansi-regex::installed::treeSha256"),
):
    setattr(
        HermesClosureTests,
        f"test_h1_check_rejects_{_name}_fingerprint_with_reissued_receipt",
        _fingerprint_drift(_field),
    )

for _name, _relative in (
    ("dependency", "ansi-regex/index.js"),
    ("native_binary", "better-sqlite3/prebuilds/linux-arm64.node"),
):
    setattr(
        HermesClosureTests,
        f"test_h1_installed_rejects_{_name}_drift",
        _installed_drift(_relative),
    )


def _remove_execute_permissions(
    case: HermesClosureTests, root: Path, relatives: tuple[str, ...]
) -> None:
    for relative in relatives:
        executable = (root / relative).resolve(strict=True)
        executable.relative_to(case.scratch)
        original_bytes = executable.read_bytes()
        case.assertNotEqual(executable.stat().st_mode & 0o111, 0)
        executable.chmod(executable.stat().st_mode & ~0o111)
        case.assertEqual(executable.read_bytes(), original_bytes)


def _execute_permission_drift(
    root: str, relatives: tuple[str, ...]
) -> Callable[[HermesClosureTests], None]:
    def test(self: HermesClosureTests) -> None:
        self._assert_check()
        project = self._copy_project()
        package = self._copy_installation()
        target_root = {
            "dependency": package.parent,
            "probe_loader": project / "node_modules",
        }[root]
        marker = self.scratch / "permission-observation-marker"
        command = (
            "--observe", sys.executable, "-c",
            f"from pathlib import Path; Path({str(marker)!r}).touch()",
        )
        clean = self._closure("--verify-installed", project=project, package=package)
        self.assertEqual(clean.returncode, 0, clean.stdout)
        self.assertIn("Installed executable closure verified", clean.stdout)
        observed = self._closure(*command, project=project, package=package)
        self.assertEqual(observed.returncode, 0, observed.stdout)
        self.assertTrue(marker.exists(), observed.stdout)
        marker.unlink()
        _remove_execute_permissions(self, target_root, relatives)

        verified = self._closure("--verify-installed", project=project, package=package)
        rejected = self._closure(*command, project=project, package=package)

        self.assertNotEqual(verified.returncode, 0, verified.stdout)
        self.assertIn("executable closure drift: dependencies", verified.stdout)
        self.assertNotEqual(rejected.returncode, 0, rejected.stdout)
        self.assertIn("executable closure drift: dependencies", rejected.stdout)
        self.assertFalse(marker.exists(), rejected.stdout)

    return test


for _root, _relatives in (
    ("dependency", ("node-addon-api/tools/conversion.js",)),
    (
        "probe_loader",
        ("esbuild/bin/esbuild", "@esbuild/linux-arm64/bin/esbuild"),
    ),
):
    setattr(
        HermesClosureTests,
        f"test_h1_observe_rejects_{_root}_execute_permission_drift_before_child",
        _execute_permission_drift(_root, _relatives),
    )


if __name__ == "__main__":
    unittest.main()
