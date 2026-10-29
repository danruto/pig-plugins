#!/usr/bin/env python3
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

PROJECT = Path(__file__).resolve().parent.parent
SCRIPT = PROJECT / "probes/capture-hermes.py"


class BaselineCaptureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source = Path(os.environ["HERMES_PACKAGE"]).resolve(strict=True)
        try:
            cls.baseline = json.loads((PROJECT / "docs/compatibility/hermes-baseline.json").read_text())
        except (OSError, json.JSONDecodeError) as error:
            raise RuntimeError("cannot read frozen baseline manifest") from error

    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="hermes-baseline-test-")
        self.addCleanup(temporary.cleanup)
        self.package = Path(temporary.name)
        for name in self.baseline["sourceRoots"]:
            shutil.copytree(self.source / name, self.package / name)
        for name in self.baseline["sourceFiles"]:
            shutil.copyfile(self.source / name, self.package / name)

    def verify(self):
        return subprocess.run(
            ["python3", str(SCRIPT), str(self.package), "--verify-source-only"],
            capture_output=True, text=True, timeout=10,
        )

    def test_exact_source_without_starting_pig(self):
        result = self.verify()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(self.baseline["sourceTreeSha256"], result.stdout)

    def test_changed_integrity_source_is_rejected(self):
        path = self.package / "src/store/db.ts"
        path.write_bytes(path.read_bytes() + b"\n")
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("source tree does not match", result.stderr)

    def test_added_runtime_source_is_rejected(self):
        (self.package / "src/unreviewed.ts").write_text("export {};\n")
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("source tree does not match", result.stderr)

    def test_removed_runtime_source_is_rejected(self):
        (self.package / "src/store/db.ts").unlink()
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("source tree does not match", result.stderr)

    def test_symlink_source_is_rejected(self):
        path = self.package / "src/store/db.ts"
        path.unlink()
        path.symlink_to(self.package / "src/index.ts")
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must not be symlinks", result.stderr)


if __name__ == "__main__":
    unittest.main()
