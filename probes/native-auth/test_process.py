#!/usr/bin/env python3
# /// script
# requires-python = ">=3.13"
# dependencies = []
# ///
"""Assert native authentication behavior at the isolated real-host/local-HTTPS seam."""

from __future__ import annotations

import json
import os
import unittest
from pathlib import Path
from typing import Any


def _load(root: Path, name: str) -> Any:
    return json.loads((root / name).read_text(encoding="utf-8"))


def _rows(root: Path, name: str) -> list[dict[str, Any]]:
    return [json.loads(line) for line in (root / name).read_text(encoding="utf-8").splitlines()]


def _events(rows: list[dict[str, Any]], kind: str) -> list[dict[str, Any]]:
    return [row for row in rows if row.get("event") == kind]


def _segments(rows: list[dict[str, Any]]) -> list[list[dict[str, Any]]]:
    segments, start = [], 0
    for index, row in enumerate(rows):
        if row.get("event") == "result":
            segments.append(rows[start:index + 1])
            start = index + 1
    return segments


def _outcome(segment: list[dict[str, Any]]) -> dict[str, Any]:
    return _events(segment, "result")[-1]["outcomes"][0]


def _projection(report: dict[str, Any]) -> dict[str, Any]:
    return {key: report.get(key) for key in ("providerId", "semantics", "buckets", "metrics", "accountLabel")}


def _assert_consuming_wait_ownership(test: unittest.TestCase, waits: dict[str, Any]) -> None:
    created = {row["pid"]: row for row in waits["createdProcesses"]}
    adopted = {row["pid"]: row for row in waits["adoptedWaits"]}
    original = {pid for pid, row in created.items() if row.get("receipt")}
    process_owners = set(created) | {row["ownerProcess"] for row in created.values()}

    test.assertTrue(created, "missing actual child creation")
    test.assertEqual(len(created), len(waits["createdProcesses"]), "duplicate child creation")
    test.assertEqual(waits["missingConsumingWaits"], [], "missing consuming waits")
    test.assertEqual(waits["wrongWaitOwners"], [], "wrong wait owners")
    test.assertEqual(len(adopted), len(waits["adoptedWaits"]), "duplicate adoption")
    test.assertEqual(set(adopted), set(created) - original, "unsupported or missing adoption")
    test.assertLessEqual(len({row["waitOwner"] for row in adopted.values()}), 1, "multiple adoption owners")
    for pid, row in created.items():
        test.assertGreater(pid, 0)
        test.assertGreater(row["createdByThread"], 0)
        test.assertGreater(row["ownerProcess"], 0)
        if pid in original:
            test.assertEqual(row["waitOwnerProcess"], row["ownerProcess"], "wrong wait owner")
            test.assertGreater(row["waitOwnerThread"], 0)
            test.assertIn(row["syscall"], {"waitid", "wait4"})
            test.assertTrue(row["receipt"])
        else:
            test.assertTrue({"waitOwnerProcess", "waitOwnerThread", "syscall", "receipt"}.isdisjoint(row), "incomplete original wait")
            receipt = adopted[pid]
            test.assertGreater(receipt["waitOwner"], 0)
            test.assertNotIn(receipt["waitOwner"], process_owners, "adoption owner is not coordinator")
            test.assertGreaterEqual(receipt["status"], 0)
            test.assertLessEqual(receipt["status"], 65535)
            test.assertTrue(os.WIFEXITED(receipt["status"]) or os.WIFSIGNALED(receipt["status"]), "non-consuming adoption status")


class AuthenticationProcessTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.root = Path(os.environ["NA_OUTPUT"])
        cls.rows = _rows(cls.root, "native.jsonl")
        cls.segments = _segments(cls.rows)
        cls.cases = {case["name"]: case for case in _load(cls.root, "scenarios.json")}
        cls.oracle = _load(cls.root, "source-oracle.json")

    def test_current_and_other_match_actual_pinned_source(self) -> None:
        for index, provider in ((0, "deepseek"), (1, "openrouter"), (5, "fireworks"), (8, "codex")):
            with self.subTest(provider=provider):
                outcome = _outcome(self.segments[index])
                self.assertEqual(outcome["status"], "ok")
                self.assertEqual(_projection(outcome["report"]), _projection(self.oracle[provider]))

    def test_other_avoids_failing_selected_auth_and_all_preserves_partial_results(self) -> None:
        receipt = _load(self.root, "isolation-checks.json")
        partial = _events(self.rows, "result")[20]

        self.assertEqual(receipt, {"explicitOtherAvoidedFailingSelectedResolver": True, "allPreservedPartialResultsWithFailingSelectedResolver": True})
        self.assertEqual(partial["error"], "")
        self.assertEqual({row["providerId"]: row["status"] for row in partial["outcomes"]}, {"baseten":"failed", "openai":"ok", "openai-codex":"ok", "deepseek":"failed", "openrouter":"ok", "fireworks":"selection-required"})
        self.assertEqual(_outcome(self.segments[19])["status"], "ok")

    def test_fireworks_requires_exact_discovered_slug_and_never_substitutes(self) -> None:
        for index in (4, 6):
            with self.subTest(index=index):
                outcome = _outcome(self.segments[index])
                self.assertEqual(outcome["status"], "selection-required")
                self.assertEqual([choice["id"] for choice in outcome["choices"]], self.oracle["choices"])
                self.assertEqual([request["path"] for request in _events(self.segments[index], "request")], ["/v1/accounts"])
        self.assertEqual(_outcome(self.segments[5])["targetId"], "second")
        self.assertEqual([request["path"].split("/")[3] for request in _events(self.segments[5], "request") if request["path"] != "/v1/accounts"], ["second"])

    def test_native_openai_is_auth_only_and_companion_uses_its_own_credential(self) -> None:
        native = _outcome(self.segments[7])
        companion = _outcome(self.segments[9])

        self.assertEqual(native["status"], "ok")
        self.assertEqual(native["report"]["buckets"], [])
        self.assertEqual(_events(self.segments[7], "request"), [])
        self.assertEqual(companion["status"], "ok")
        self.assertEqual({row["host"] for row in _events(self.segments[9], "request")}, {"chatgpt.com"})
        self.assertEqual({row["authTag"] for row in _events(self.segments[9], "request")}, {"codex"})
        self.assertTrue(all(row["accountHeader"] for row in _events(self.segments[9], "request")))

    def test_nullable_empty_overrides_suppress_auth_without_losing_explicit_override(self) -> None:
        self.assertEqual(_outcome(self.segments[11])["status"], "ok")
        self.assertEqual([row["authTag"] for row in _events(self.segments[11], "request")], ["selected"])
        for index in (12, 13):
            with self.subTest(index=index):
                self.assertEqual(_outcome(self.segments[index])["status"], "no-auth")
                self.assertEqual(_events(self.segments[index], "request"), [])

    def test_fresh_oauth_matching_rejects_mismatch_and_duplicate_candidates(self) -> None:
        self.assertEqual(_outcome(self.segments[7])["status"], "ok")
        for index in (17, 18):
            with self.subTest(index=index):
                self.assertEqual(_outcome(self.segments[index])["status"], "failed")
                self.assertEqual(_events(self.segments[index], "request"), [])
        self.assertGreater(sum(row["event"] == "ready-openai" for row in _rows(self.root, "source.jsonl")), 0)

    def test_discovery_ids_must_match_first_successful_snapshot_before_https(self) -> None:
        rows = self.cases["discovery-id-change"]["events"]

        self.assertEqual(_outcome(self.cases["ordinary-other"]["events"])["status"], "ok")
        self.assertEqual(_events(rows, "result")[-1]["error"], "identity-changed")
        self.assertEqual(_events(rows, "request"), [])

    def test_complete_common_selected_model_changes_reject_before_https(self) -> None:
        self.assertEqual(_outcome(self.cases["ordinary-other"]["events"])["status"], "ok")
        for change in ("config", "origin", "override"):
            with self.subTest(change=change):
                rows = self.cases["pre-io-common-" + change]["events"]
                self.assertEqual(_outcome(rows)["status"], "failed")
                self.assertEqual(_outcome(rows)["reason"], "identity-changed")
                self.assertEqual(_events(rows, "request"), [])

    def test_requested_provider_reacquires_fresh_auth_or_rejects_before_https(self) -> None:
        rotation = self.cases["pre-io-requested-other-rotate"]["events"]
        origin = self.cases["pre-io-requested-other-origin"]["events"]

        self.assertEqual(_outcome(rotation)["status"], "ok")
        self.assertEqual([row["authTag"] for row in _events(rotation, "request")], ["rotated"])
        self.assertEqual(_outcome(origin)["status"], "failed")
        self.assertEqual(_events(origin, "request"), [])

    def test_same_id_complete_common_and_requested_changes_reject_before_return(self) -> None:
        self.assertEqual(_outcome(self.cases["ordinary-other"]["events"])["status"], "ok")
        for case in ("return-common-config", "return-common-origin", "return-common-override", "return-requested-other-config", "return-requested-other-origin", "return-requested-other-rotate", "return-choices-common-config"):
            with self.subTest(case=case):
                rows = self.cases[case]["events"]
                self.assertGreater(len(_events(rows, "request")), 0)
                self.assertEqual(_outcome(rows)["status"], "failed")
                self.assertNotIn("report", _outcome(rows))
                self.assertNotIn("choices", _outcome(rows))

    def test_all_failed_auth_is_honest_without_https_or_invented_complete_fence(self) -> None:
        rows = self.cases["all-auth-failed"]["events"]
        result = _events(rows, "result")[-1]

        self.assertEqual(_outcome(self.cases["ordinary-other"]["events"])["status"], "ok")
        self.assertEqual(result["error"], "")
        self.assertEqual([row["providerId"] for row in result["outcomes"]], ["baseten", "openai", "openai-codex", "deepseek", "openrouter", "fireworks"])
        self.assertEqual({(row["status"], row["reason"]) for row in result["outcomes"]}, {("failed", "host-acquisition-failed")})
        self.assertEqual(_events(rows, "request"), [])

    def test_shared_deadline_cancels_https_and_joins_before_return(self) -> None:
        rows = self.cases["cancel-http"]["events"]
        result = _events(rows, "result")[-1]

        self.assertEqual(_outcome(self.cases["ordinary-other"]["events"])["status"], "ok")
        self.assertEqual(len(_events(rows, "request")), 1)
        self.assertEqual(result["error"], "timed-out")
        self.assertIsNone(result["outcomes"])
        self.assertEqual(_events(rows, "request-active")[-1]["active"], 0)

    def test_all_query_concurrency_is_two_and_partial_results_keep_order(self) -> None:
        rows = self.cases["bounded-all"]["events"]
        result = _events(rows, "result")[-1]

        self.assertEqual(max(row["active"] for row in _events(rows, "request-active")), 2)
        self.assertEqual(result["error"], "")
        self.assertEqual([row["providerId"] for row in result["outcomes"]], ["baseten", "openai", "openai-codex", "deepseek", "openrouter", "fireworks"])
        self.assertEqual([row["status"] for row in result["outcomes"]], ["failed", "ok", "ok", "ok", "ok", "selection-required"])

    def test_real_process_cleanup_namespace_and_diagnostics_safety(self) -> None:
        completion = _load(self.root, "completion.json")
        waits = _load(self.root, "process-receipts.json")
        text = (self.root / "native.jsonl").read_text(encoding="utf-8") + (self.root / "stdout.jsonl").read_text(encoding="utf-8") + (self.root / "stderr.log").read_text(encoding="utf-8")

        self.assertEqual(completion["exit"], 0)
        self.assertTrue(completion["readerJoined"])
        self.assertTrue(completion["noForbiddenEvents"])
        self.assertGreater(completion["IPConnections"], 0)
        _assert_consuming_wait_ownership(self, waits)
        for canary in ("synthetic-private-body", "synthetic-initial", "synthetic-rotated", "synthetic-selected", "synthetic-native", "synthetic-codex-refresh", "header.eyJ"):
            with self.subTest(canary=canary):
                self.assertNotIn(canary, text)


def main() -> int:
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(AuthenticationProcessTests)
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    report = {"executed": result.testsRun, "failures": [{"test":test.id(), "output":output} for test, output in result.failures], "errors": [{"test":test.id(), "output":output} for test, output in result.errors], "successful": result.wasSuccessful()}
    Path(os.environ["NA_OUTPUT"], "acceptance-results.json").write_text(json.dumps(report, indent=2), encoding="utf-8")
    return int(not result.wasSuccessful())


if __name__ == "__main__":
    raise SystemExit(main())
