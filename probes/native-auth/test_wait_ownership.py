#!/usr/bin/env python3
# /// script
# requires-python = ">=3.13"
# dependencies = []
# ///
"""Check consuming-wait assertions with retained receipts and invalid mutations."""

from __future__ import annotations

import copy
import json
import os
import unittest
from pathlib import Path

from test_process import _assert_consuming_wait_ownership


class ConsumingWaitOwnershipTests(unittest.TestCase):
    def setUp(self) -> None:
        self.waits = json.loads(Path(os.environ["NA_OUTPUT"], "process-receipts.json").read_text(encoding="utf-8"))

    def test_original_owner_without_adoption_passes(self) -> None:
        self.assertEqual(self.waits["adoptedWaits"], [])

        _assert_consuming_wait_ownership(self, self.waits)

    def test_missing_wait_fails_even_with_empty_summary(self) -> None:
        row = self.waits["createdProcesses"][0]
        del row["receipt"], row["syscall"], row["waitOwnerProcess"], row["waitOwnerThread"]

        with self.assertRaisesRegex(AssertionError, "unsupported or missing adoption"):
            _assert_consuming_wait_ownership(self, self.waits)

    def test_wrong_owner_fails_even_with_empty_summary(self) -> None:
        self.waits["createdProcesses"][0]["waitOwnerProcess"] += 1

        with self.assertRaisesRegex(AssertionError, "wrong wait owner"):
            _assert_consuming_wait_ownership(self, self.waits)

    def test_no_real_child_creation_fails(self) -> None:
        self.waits["createdProcesses"] = []

        with self.assertRaisesRegex(AssertionError, "missing actual child creation"):
            _assert_consuming_wait_ownership(self, self.waits)

    def test_adoption_for_already_consumed_child_fails(self) -> None:
        self.waits["adoptedWaits"] = [{"pid": self.waits["createdProcesses"][0]["pid"], "status": 0, "waitOwner": 1}]

        with self.assertRaisesRegex(AssertionError, "unsupported or missing adoption"):
            _assert_consuming_wait_ownership(self, self.waits)

    def test_supported_coordinator_receipt_shape_passes_but_original_owner_is_not_adoption(self) -> None:
        row = self.waits["createdProcesses"][0]
        del row["receipt"], row["syscall"], row["waitOwnerProcess"], row["waitOwnerThread"]
        self.waits["adoptedWaits"] = [{"pid": row["pid"], "status": 0, "waitOwner": 1}]
        invalid = copy.deepcopy(self.waits)
        invalid["adoptedWaits"][0]["waitOwner"] = row["ownerProcess"]

        _assert_consuming_wait_ownership(self, self.waits)
        with self.assertRaisesRegex(AssertionError, "adoption owner is not coordinator"):
            _assert_consuming_wait_ownership(self, invalid)


if __name__ == "__main__":
    unittest.main(verbosity=2)
