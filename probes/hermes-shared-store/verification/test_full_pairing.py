"""Exercise FULL packet, pairing and conditional-mutation contracts with method doubles."""

from __future__ import annotations

import builtins
import copy
import ctypes
import errno
import importlib
import io
import operator
import os
import signal
import struct
import unittest
from collections.abc import Callable
from contextlib import ExitStack
from functools import partial
from unittest.mock import patch

import full_pairing as pairing

_ARCH = 0xC00000B7
_ARGS = (11, 22, 33, 44, 55, 66)
_MASK = (1 << 64) - 1


def _entry(*, number: int = 63, args: tuple[int, ...] = _ARGS) -> bytes:
    return struct.pack("<B3xIQQ7Q", 1, _ARCH, 0x12345678, 0x87654321, number, *args)


def _exit(value: int = 0, *, error: int = 0) -> bytes:
    return struct.pack("<B3xIQQqB", 2, _ARCH, 0x12345678, 0x87654321, value, error)


def _registers(*, number: int = 63, args: tuple[int, ...] = _ARGS, result: int | None = None) -> bytes:
    values = list(range(34))
    values[8] = number
    values[:6] = args
    if result is not None:
        values[0] = result & _MASK
    return struct.pack("<34Q", *values)


def _changed(raw: bytes, index: int, value: int) -> bytes:
    result = bytearray(raw)
    struct.pack_into("<Q", result, index * 8, value & _MASK)
    return bytes(result)


def _allow(snapshot: object) -> None:
    return None


def _mutation_cases() -> list[tuple[str, str, str, list[tuple[int, int, int]]]]:
    general_read = [(0x4204, 11, 1)]
    scalar_read = general_read + [(0x4204, 11, 0x404)]
    scalar_write = scalar_read + [(0x4205, 11, 0x404)]
    entry_write = scalar_write + [(0x4205, 11, 1)]
    exit_write = general_read + [(0x4205, 11, 1)]
    common = (("read-error", "GETREGSET"), ("read-error-ESRCH", "GETREGSET"), ("short-read", "GETREGSET"), ("set-error", "SETREGSET"), ("short-set", "SETREGSET-short"))
    cases = []
    for selector in ("fault-entry", "fault-return", "admission-refusal"):
        write = exit_write if selector == "fault-return" else entry_write
        cases.extend((selector, failure, code, write if failure in ("set-error", "short-set") else general_read) for failure, code in common)
    cases.extend((selector, "number-drift", "GETREGSET-agreement", general_read) for selector in ("fault-entry", "admission-refusal"))
    cases.extend((selector, f"argument-drift-{index}", "GETREGSET-agreement", general_read) for selector in ("fault-entry", "admission-refusal") for index in range(6))
    cases.append(("fault-return", "result-drift", "GETREGSET-agreement", general_read))
    scalar = (("scalar-read-error", "GETREGSET", scalar_read), ("scalar-short-read", "GETREGSET", scalar_read), ("scalar-number-drift", "GETREGSET-agreement", scalar_read), ("scalar-set-error", "SETREGSET", scalar_write), ("scalar-short-set", "SETREGSET-short", scalar_write))
    cases.extend((selector, failure, code, calls) for selector in ("fault-entry", "admission-refusal") for failure, code, calls in scalar)
    return cases


class _IOVec(ctypes.Structure):
    _fields_ = [("base", ctypes.c_void_p), ("length", ctypes.c_size_t)]


class _Transport:
    def __init__(self) -> None:
        self.calls: list[tuple[int, int, int]] = []
        self.registers = _registers()
        self.scalar_registers = struct.pack("<i", 63)
        self.writes: list[tuple[int, bytes]] = []
        self.failure: str | None = None
        self.error = OSError(errno.EIO, "synthetic transport failure")
        self.after_read: Callable[[], None] | None = None
        self.before_set: Callable[[], None] | None = None
        self.lengths: list[int] = []

    def __call__(self, op: int, pid: int, address: int = 0, data: int = 0) -> int:
        self.calls.append((op, pid, address))
        if op == 24:
            if self.failure == "resume-error":
                raise self.error
            if data != 0:
                raise AssertionError("ordinary resume supplied a signal")
            return 0
        if op not in (0x4204, 0x4205) or address not in (1, 0x404):
            raise AssertionError((op, pid, address))
        capacity = 4 if address == 0x404 else 272
        failure = self.failure
        if address == 0x404:
            failure = failure.removeprefix("scalar-") if failure is not None and failure.startswith("scalar-") else None
        elif failure is not None and failure.startswith("scalar-"):
            failure = None
        iov = ctypes.cast(data, ctypes.POINTER(_IOVec)).contents
        self.lengths.append(iov.length)
        if iov.length != capacity:
            raise AssertionError((address, iov.length))
        if op == 0x4204:
            if failure in ("read-error", "read-error-ESRCH"):
                raise self.error
            raw = self.scalar_registers if address == 0x404 else self.registers
            ctypes.memmove(iov.base, raw, capacity)
            if failure == "short-read":
                iov.length = 3 if address == 0x404 else 264
            if self.after_read is not None:
                self.after_read()
            return 0
        if self.before_set is not None:
            self.before_set()
        self.writes.append((address, ctypes.string_at(iov.base, capacity)))
        if failure == "set-error":
            raise self.error
        if failure == "short-set":
            iov.length = 3 if address == 0x404 else 264
        return 0


class FullPairingTests(unittest.TestCase):
    def setUp(self) -> None:
        self.now = 1.0
        self.events: list[dict] = []
        self.transport = _Transport()
        self.owner = self.make_owner()
        self.owner.register(11, start_ticks="2", tgid=11)

    def make_owner(self, *, policy: Callable = _allow, emit: Callable | None = None) -> pairing.FullPairing:
        return pairing.FullPairing(
            deadline=10.0,
            policy=policy,
            ptrace=self.transport,
            emit=self.emit if emit is None else emit,
            clock=lambda: self.now,
        )

    def emit(self, event: str, **fields) -> None:
        self.events.append({"event": event, **fields})

    def rows(self, event: str) -> list[dict]:
        return [row for row in self.events if row["event"] == event]

    def capture(self, raw: bytes | bytearray, acquisition: int, *, pid: int = 11, start: str = "2", tgid: int = 11) -> pairing.StopSnapshot:
        return self.owner.capture(
            pid, start_ticks=start, tgid=tgid, acquisition_id=acquisition,
            raw=raw, available=len(raw),
        )

    def assert_provenance(self, value: object, acquisition: int = 1, *, number: int = 63, args: tuple[int, ...] = _ARGS) -> None:
        self.assertEqual(value, pairing.EntryProvenance(acquisition, 11, "2", 11, number, args))

    def assert_event_provenance(self, event: dict, acquisition: int = 1, *, args: tuple[int, ...] = _ARGS) -> None:
        proof = event["entryProvenance"]
        self.assertEqual(
            (proof["acquisitionId"], proof["pid"], proof["startTicks"], proof["tgid"], proof["number"], tuple(proof["args"])),
            (acquisition, 11, "2", 11, 63, args),
        )

    def assert_error(self, code: str, *, unsafe: bool = True, acquisition: int | None = 1) -> dict:
        row = self.rows("observer-error")[-1]
        self.assertEqual(row["errorCode"], code)
        self.assertIs(row["unsafe"], unsafe)
        self.assertEqual(row["unsafeAcquisitionId"], acquisition)
        return row

    def assert_sticky(self, acquisition: int | None = 1) -> None:
        before = list(self.transport.calls)
        with self.assertRaises(RuntimeError):
            self.owner.resume(11)
        self.assert_error("unsafe-resume", acquisition=acquisition)
        with self.assertRaises(RuntimeError):
            self.owner.require_complete()
        self.assertEqual(self.transport.calls, before)

    def complete_pair(self, *, first: int = 1, result: int = 7) -> tuple[object, object]:
        entry = self.capture(_entry(), first)
        proof = self.owner.dispatch(entry)
        self.owner.resume(11)
        returned = self.capture(_exit(result), first + 1)
        pair = self.owner.dispatch(returned)
        self.owner.resume(11)
        self.owner.require_complete()
        self.assert_provenance(proof, first)
        self.assertEqual(pair, (proof, pairing.ExitInfo(result, False)))
        return proof, pair

    def assert_original_cause(self, error: BaseException) -> None:
        self.assertTrue(error is self.transport.error or error.__cause__ is self.transport.error)

    def prepare_missing(self, operation: str) -> tuple[Callable, int]:
        self.setUp()
        self.assert_provenance(self.owner.dispatch(self.capture(_entry(), 1)))
        self.owner.resume(11)
        if operation == "new-entry":
            later = self.capture(_entry(number=64), 2)
            return partial(self.owner.dispatch, later), 2
        return self.owner.require_complete, 1

    def prepare_unmatched(self, operation: str) -> tuple[pairing.StopSnapshot, int, int]:
        self.setUp()
        acquisition, pid, start, previous_returns = 1, 11, "2", 0
        if operation == "duplicate":
            self.complete_pair()
            acquisition, previous_returns = 3, 1
        if operation == "other-task":
            self.assert_provenance(self.owner.dispatch(self.capture(_entry(), 1)))
            self.owner.resume(11)
            self.owner.register(12, start_ticks="3", tgid=12)
            acquisition, pid, start = 2, 12, "3"
        return self.capture(_exit(7), acquisition, pid=pid, start=start, tgid=pid), acquisition, previous_returns

    def prepare_misassociated(self, operation: str) -> tuple[int, dict]:
        self.setUp()
        self.complete_pair()
        changes = {
            "start": (3, {"start": "wrong"}), "tgid": (3, {"tgid": 12}),
            "pid": (3, {"pid": 12}), "same-acquisition": (2, {}),
            "older-acquisition": (1, {}), "zero-acquisition": (0, {}),
            "other-task-replay": (2, {"pid": 12, "start": "3", "tgid": 12}),
        }
        if operation == "other-task-replay":
            self.owner.register(12, start_ticks="3", tgid=12)
        return changes[operation]

    def prepare_lifecycle(self, operation: str) -> Callable:
        self.setUp()
        if operation == "resume-without-capture":
            return partial(self.owner.resume, 11)
        held = self.capture(_entry(), 1)
        if operation == "forged":
            forged = pairing.StopSnapshot(held.acquisitionId, held.pid, held.startTicks, held.tgid, held.syscallInfo, held.infoLength, held.info)
            self.assertEqual(forged, held)
            self.assertIsNot(forged, held)
            return partial(self.owner.dispatch, forged)
        if operation == "resume-before-dispatch":
            return partial(self.owner.resume, 11)
        if operation == "recapture":
            return partial(self.capture, _entry(), 1)
        self.assert_provenance(self.owner.dispatch(held))
        if operation == "redispatch":
            return partial(self.owner.dispatch, held)
        if operation == "capture-before-resume":
            return partial(self.capture, _exit(7), 2)
        self.owner.resume(11)
        if operation == "redispatch-return":
            returned = self.capture(_exit(7), 2)
            self.assertEqual(self.owner.dispatch(returned), (pairing.EntryProvenance(1, 11, "2", 11, 63, _ARGS), pairing.ExitInfo(7, False)))
            return partial(self.owner.dispatch, returned)
        return partial(self.owner.resume, 11)

    def prepare_mutation_failure(self, selector: str, failure: str) -> tuple:
        self.setUp()
        acquisition, raw, fault = 1, _entry(), -errno.EIO
        if selector == "fault-return":
            self.assert_provenance(self.owner.dispatch(self.capture(raw, 1), fault_result=fault))
            self.owner.resume(11)
            acquisition, raw = 2, _exit(-errno.ENOSYS, error=1)
            self.transport.registers = _registers(result=-errno.ENOSYS)
        if selector == "admission-refusal":
            def refuse(snapshot: object) -> None:
                raise PermissionError(errno.ENOSPC, "synthetic prewrite refusal")
            self.owner = self.make_owner(policy=refuse)
            self.owner.register(11, start_ticks="2", tgid=11)
        if failure == "number-drift":
            self.transport.registers = _changed(self.transport.registers, 8, 999)
        if failure == "result-drift":
            self.transport.registers = _changed(self.transport.registers, 0, 999)
        if failure == "scalar-number-drift":
            self.transport.scalar_registers = struct.pack("<i", 999)
        if failure.startswith("argument-drift-"):
            self.transport.registers = _changed(self.transport.registers, int(failure[-1]), 999)
        self.transport.failure = failure
        if failure == "read-error-ESRCH":
            self.transport.error = OSError(errno.ESRCH, "synthetic transport failure")
        original = self.transport.registers
        held = self.capture(raw, acquisition)
        before = len(self.transport.calls)
        successes = len(self.rows("kernel-register-mutation-result"))
        entries = len(self.rows("syscall-entry"))
        faults = {} if selector in ("fault-return", "admission-refusal") else {"fault_result": fault}
        return held, acquisition, raw, original, before, successes, entries, faults

    def assert_mutation_failure_fields(self, selector: str, failure: str, error: dict, original: bytes, raw: bytes, raised: BaseException) -> None:
        scalar = failure.startswith("scalar-")
        kind = failure.removeprefix("scalar-")
        note, capacity, short = (0x404, 4, 3) if scalar else (1, 272, 264)
        setting = kind in ("set-error", "short-set")
        self.assertEqual(error["noteType"], note)
        self.assertEqual(error["subphase"], "SETREGSET" if setting else "GETREGSET")
        if kind in ("read-error", "read-error-ESRCH", "set-error"):
            self.assert_original_cause(raised)
            self.assertEqual(error["errno"], self.transport.error.errno)
        if kind == "short-read":
            self.assertEqual((error["available"], error["capacity"]), (short, capacity))
        if kind in ("number-drift", "result-drift") or kind.startswith("argument-drift-"):
            self.assertEqual((error["originalRegisterBytesHex"], error["syscallInfoHex"]), (original.hex(), raw.hex()))
            if scalar:
                self.assertEqual(error["originalScalarBytesHex"], self.transport.scalar_registers.hex())
        if setting:
            mutation = error["mutation"]
            self.assertEqual(mutation["noteType"], note)
            self.assertEqual(mutation["originalRegisterBytesHex"], original.hex())
            self.assertEqual(mutation["originalScalarBytesHex"], None if selector == "fault-return" else self.transport.scalar_registers.hex())
            self.assertEqual(mutation["syscallInfoHex"], raw.hex())
            expected = struct.pack("<i", -1) if scalar else _changed(original, 0, -errno.EIO if selector == "fault-return" else -errno.ENOSYS)
            self.assertEqual(mutation["writtenRegisterBytesHex"], expected.hex())
            self.assertEqual(error["transfer"], {"requested": capacity, "returned": short if kind == "short-set" else capacity, "ptraceResult": 0 if kind == "short-set" else None, "outcome": "short" if kind == "short-set" else "error"})
            completed = [] if scalar or selector == "fault-return" else [{"noteType": 0x404, "transfer": {"requested": 4, "returned": 4, "ptraceResult": 0, "outcome": "full"}}]
            self.assertEqual(error["completedTransfers"], completed)

    def test_entry_decodes_every_argument_common_field_and_guards_exit_arm(self) -> None:
        info = pairing.decode_syscall_info(_entry(), 80, 88)

        self.assertEqual(info.entry, pairing.EntryInfo(63, _ARGS))
        self.assertEqual((info.op, info.arch, info.instructionPointer, info.stackPointer), (1, _ARCH, 0x12345678, 0x87654321))
        self.assertEqual(info.populated, info.entry)
        self.assertEqual(pairing.decode_syscall_info(_entry() + b"\xff" * 8, 88, 88), info)
        with self.assertRaises((ValueError, AttributeError, RuntimeError)):
            _ = info.exit

    def test_exit_decodes_signed_errno_boundaries_without_interpreting_inactive_tail(self) -> None:
        for value, is_error in ((-(1 << 63), False), (-4096, False), (-4095, True), (-1, True), (0, False), (7, False), ((1 << 63) - 1, False)):
            with self.subTest(value=value):
                raw = _exit(value, error=int(is_error))

                info = pairing.decode_syscall_info(raw, 33, 88)

                self.assertEqual(info.exit, pairing.ExitInfo(value, is_error))
                self.assertEqual(info.populated, info.exit)
                self.assertEqual(pairing.decode_syscall_info(raw + b"\xaa" * 55, 88, 88), info)
                with self.assertRaises((ValueError, AttributeError, RuntimeError)):
                    _ = info.entry

    def test_decoder_rejects_malformed_lengths_architecture_arms_and_error_flags(self) -> None:
        bad_packets = [
            ("empty", b"", 0, 88), ("negative-available", _entry(), -1, 88), ("header-short", bytes(23), 23, 88),
            ("entry-short", _entry()[:79], 79, 88), ("exit-short", _exit()[:32], 32, 88),
            ("over-capacity", _entry(), 89, 88), ("wrong-capacity", _entry(), 80, 80),
            ("missing-bytes", _entry()[:-1], 80, 88),
        ]
        bad_packets.extend((f"op-{op}", bytes([op]) + _entry()[1:], 80, 88) for op in (0, 3, 9, 255))
        bad_packets.extend((f"reserved-{offset}", _entry()[:offset] + b"\x01" + _entry()[offset + 1:], 80, 88) for offset in (1, 2, 3))
        bad_packets.append(("wrong-arch", _entry()[:4] + struct.pack("<I", 0xC000003E) + _entry()[8:], 80, 88))
        bad_packets.extend((f"flag-{value}-{flag}", _exit(value, error=flag), 33, 88) for value, flag in ((0, 1), (1, 1), (-4096, 1), (-1, 0), (-4095, 0), (0, 2)))
        for name, raw, available, capacity in bad_packets:
            with self.subTest(name=name), self.assertRaises(ValueError):
                pairing.decode_syscall_info(raw, available, capacity)
        self.assertEqual(pairing.decode_syscall_info(_entry(), 80, 88).entry, pairing.EntryInfo(63, _ARGS))

    def test_ordinary_pair_and_independent_next_entry_use_explicit_resume_without_register_io(self) -> None:
        admitted = []
        self.owner = self.make_owner(policy=admitted.append)
        self.owner.register(11, start_ticks="2", tgid=11)
        entry = self.capture(_entry(), 1)

        proof = self.owner.dispatch(entry)

        self.assertEqual(admitted, [entry])
        self.assert_provenance(proof)
        self.assertEqual(self.transport.calls, [])
        self.owner.resume(11)
        returned = self.capture(_exit(-4096), 2)
        self.assertEqual(self.owner.dispatch(returned), (proof, pairing.ExitInfo(-4096, False)))
        self.assertEqual(len(admitted), 1)
        row = self.rows("syscall-return")[0]
        self.assert_event_provenance(row)
        self.assertEqual((row["result"], row["isError"], row["deliveredResult"], row["deliveredIsError"], row["injected"], row["admissionDenied"]), (-4096, False, -4096, False, False, False))
        self.owner.resume(11)
        self.complete_pair(first=3)
        self.assertEqual(self.transport.calls, [(24, 11, 0)] * 4)
        self.assertEqual(self.rows("conditional-register-read"), [])
        self.assertEqual([row["reason"] for row in self.rows("kernel-snapshot-invalidated")], ["resume"] * 4)

    def test_fault_pair_changes_only_selected_registers_and_retains_originals(self) -> None:
        for fault, observed in ((-errno.ENOSPC, -errno.ENOSYS), (-errno.EIO, -4096)):
            with self.subTest(fault=fault):
                self.setUp()
                raw = _entry()
                original_entry = self.transport.registers
                captured = self.capture(raw, 1)
                at_set = []
                self.transport.before_set = lambda at_set=at_set: at_set.append((copy.deepcopy(self.rows("kernel-snapshot-invalidated")), len(self.rows("kernel-register-mutation-result"))))

                proof = self.owner.dispatch(captured, fault_result=fault)

                self.assert_provenance(proof)
                self.assertEqual(self.transport.calls, [(0x4204, 11, 1), (0x4204, 11, 0x404), (0x4205, 11, 0x404), (0x4205, 11, 1)])
                self.assertEqual(self.rows("syscall-entry")[0]["faultResult"], fault)
                self.assertIs(self.rows("syscall-entry")[0]["admissionDenied"], False)
                self.owner.resume(11)
                raw_exit = _exit(observed, error=int(observed == -errno.ENOSYS))
                original_exit = _registers(result=observed)
                self.transport.registers = original_exit
                returned = self.capture(raw_exit, 2)
                pair = self.owner.dispatch(returned)
                self.assertEqual(pair, (proof, pairing.ExitInfo(observed, observed == -errno.ENOSYS)))
                self.assertEqual(self.transport.writes, [(0x404, struct.pack("<i", -1)), (1, _changed(original_entry, 0, -errno.ENOSYS)), (1, _changed(original_exit, 0, fault))])
                self.assertEqual(self.transport.registers, original_exit)
                self.assertEqual(captured.syscallInfo, raw)
                self.assertEqual(returned.syscallInfo, raw_exit)
                self.assertEqual(self.transport.lengths, [272, 4, 4, 272, 272, 272])
                self.assertEqual([rows[-1]["reason"] for rows, successes in at_set], ["register-mutation"] * 3)
                self.assertEqual([successes for rows, successes in at_set], [0, 0, 2])
                self.assertEqual([(row["noteType"], row["capacity"]) for row in self.rows("conditional-register-read")], [(1, 272), (0x404, 4), (1, 272)])
                mutations = self.rows("kernel-register-mutation")
                self.assertEqual([row["noteType"] for row in mutations], [0x404, 1, 1])
                self.assertEqual([row["originalRegisterBytesHex"] for row in mutations], [original_entry.hex(), original_entry.hex(), original_exit.hex()])
                self.assertEqual([row["originalScalarBytesHex"] for row in mutations], [struct.pack("<i", 63).hex(), struct.pack("<i", 63).hex(), None])
                self.assertEqual([row["writtenRegisterBytesHex"] for row in mutations], [value.hex() for note, value in self.transport.writes])
                self.assertEqual([row["syscallInfoHex"] for row in mutations], [raw.hex(), raw.hex(), raw_exit.hex()])
                self.assertEqual([row["infoLength"] for row in mutations], [80, 80, 33])
                self.assertEqual([row["reason"] for row in mutations], ["fault-entry", "fault-entry", "fault-return"])
                self.assertEqual([row["subphase"] for row in mutations], ["SETREGSET"] * 3)
                self.assertEqual([row["writeResult"] for row in mutations], ["pending"] * 3)
                results = self.rows("kernel-register-mutation-result")
                self.assertEqual([row["writeResult"] for row in results], ["success"] * 3)
                self.assertEqual([row["noteType"] for row in results], [0x404, 1, 1])
                self.assertEqual([row["transfer"] for row in results], [{"requested": 4, "returned": 4, "ptraceResult": 0, "outcome": "full"}, {"requested": 272, "returned": 272, "ptraceResult": 0, "outcome": "full"}, {"requested": 272, "returned": 272, "ptraceResult": 0, "outcome": "full"}])
                row = self.rows("syscall-return")[0]
                self.assert_event_provenance(row)
                self.assertEqual((row["result"], row["isError"], row["deliveredResult"], row["deliveredIsError"], row["injected"], row["admissionDenied"]), (observed, observed == -errno.ENOSYS, fault, True, True, False))
                self.owner.resume(11)
                self.complete_pair(first=3)

    def test_admission_denial_suppresses_entry_and_keeps_its_real_return_provenance(self) -> None:
        denial = PermissionError(errno.ENOSPC, "synthetic prewrite refusal")
        def refuse(snapshot: object) -> None:
            raise denial
        self.owner = self.make_owner(policy=refuse)
        self.owner.register(11, start_ticks="2", tgid=11)
        captured = self.capture(_entry(), 1)
        original = self.transport.registers

        with self.assertRaises(PermissionError) as raised:
            self.owner.dispatch(captured)

        self.assertIs(raised.exception, denial)
        self.assert_error("admission-refusal", unsafe=False, acquisition=None)
        entry = self.rows("syscall-entry")[0]
        self.assert_event_provenance(entry)
        self.assertIs(entry["admissionDenied"], True)
        self.assertEqual(self.transport.writes, [(0x404, struct.pack("<i", -1)), (1, _changed(original, 0, -errno.ENOSYS))])
        self.assertEqual([row["reason"] for row in self.rows("conditional-register-read")], ["admission-refusal"] * 2)
        self.assertEqual([(row["noteType"], row["capacity"]) for row in self.rows("conditional-register-read")], [(1, 272), (0x404, 4)])
        self.assertEqual(self.transport.calls, [(0x4204, 11, 1), (0x4204, 11, 0x404), (0x4205, 11, 0x404), (0x4205, 11, 1)])
        self.assertEqual([row["transfer"] for row in self.rows("kernel-register-mutation-result")], [{"requested": 4, "returned": 4, "ptraceResult": 0, "outcome": "full"}, {"requested": 272, "returned": 272, "ptraceResult": 0, "outcome": "full"}])
        self.owner.resume(11)
        returned = self.capture(_exit(-errno.ENOSYS, error=1), 2)
        pair = self.owner.dispatch(returned)
        self.assert_provenance(pair[0])
        self.assertEqual(pair[1], pairing.ExitInfo(-errno.ENOSYS, True))
        row = self.rows("syscall-return")[0]
        self.assert_event_provenance(row)
        self.assertEqual((row["result"], row["isError"], row["deliveredResult"], row["deliveredIsError"], row["injected"], row["admissionDenied"]), (-errno.ENOSYS, True, -errno.ENOSYS, True, True, True))
        self.assertEqual(self.transport.writes, [(0x404, struct.pack("<i", -1)), (1, _changed(original, 0, -errno.ENOSYS))])
        self.assertEqual(self.transport.calls, [(0x4204, 11, 1), (0x4204, 11, 0x404), (0x4205, 11, 0x404), (0x4205, 11, 1), (24, 11, 0)])
        self.owner.resume(11)
        self.owner.require_complete()

    def test_required_return_cannot_be_replaced_by_another_entry_or_completion_claim(self) -> None:
        for operation in ("completion", "new-entry"):
            with self.subTest(operation=operation):
                action, acquisition = self.prepare_missing(operation)

                with self.assertRaises(RuntimeError):
                    action()

                self.assert_error("missing-return", acquisition=acquisition)
                self.assert_sticky(acquisition)
                self.assertEqual(self.rows("syscall-return"), [])
                self.assertEqual(len(self.rows("syscall-entry")), 1)

    def test_orphan_duplicate_and_other_task_returns_do_not_manufacture_pairs(self) -> None:
        for operation in ("orphan", "duplicate", "other-task"):
            with self.subTest(operation=operation):
                returned, acquisition, previous_returns = self.prepare_unmatched(operation)

                with self.assertRaises(RuntimeError):
                    self.owner.dispatch(returned)

                self.assert_error("unmatched-return", acquisition=acquisition)
                self.assertEqual(len(self.rows("syscall-return")), previous_returns)

    def test_capture_refuses_wrong_incarnation_group_pid_and_replayed_acquisitions(self) -> None:
        cases = (("start", "ownership"), ("tgid", "ownership"), ("pid", "ownership"), ("same-acquisition", "replay"), ("older-acquisition", "replay"), ("zero-acquisition", "replay"), ("other-task-replay", "replay"))
        for operation, code in cases:
            with self.subTest(operation=operation):
                acquisition, kwargs = self.prepare_misassociated(operation)

                with self.assertRaises(RuntimeError):
                    self.capture(_exit(7), acquisition, **kwargs)

                self.assertEqual(self.rows("observer-error")[-1]["errorCode"], code)
                self.assertEqual(len(self.rows("syscall-return")), 1)

    def test_live_registration_cannot_duplicate_or_rebind_identity(self) -> None:
        for start, tgid in (("2", 11), ("changed", 11), ("2", 12)):
            with self.subTest(start=start, tgid=tgid):
                self.setUp()
                self.assert_provenance(self.owner.dispatch(self.capture(_entry(), 1)))

                with self.assertRaises(RuntimeError):
                    self.owner.register(11, start_ticks=start, tgid=tgid)

                self.assert_error("ownership")
                self.assert_sticky()

    def test_stop_is_captured_dispatched_and_explicitly_resumed_only_once(self) -> None:
        for operation in ("recapture", "forged", "redispatch", "resume-without-capture", "resume-before-dispatch", "capture-before-resume", "second-resume", "redispatch-return"):
            with self.subTest(operation=operation):
                action = self.prepare_lifecycle(operation)
                before = list(self.transport.calls)

                with self.assertRaises(RuntimeError):
                    action()

                self.assertEqual(self.transport.calls, before)
                self.assertIs(self.rows("observer-error")[-1]["unsafe"], True)
                self.assert_sticky(self.rows("observer-error")[-1]["unsafeAcquisitionId"])

    def test_snapshot_freezes_caller_bytes_nested_records_and_event_time_values(self) -> None:
        raw = bytearray(_entry())
        original = bytes(raw)

        held = self.capture(raw, 1)
        raw[24:32] = (99).to_bytes(8, "little")

        self.assertEqual(held.syscallInfo, original)
        self.assertEqual(held.info.entry, pairing.EntryInfo(63, _ARGS))
        field = "acquisitionId"
        with self.assertRaises((AttributeError, TypeError)):
            setattr(held, field, 99)
        field = "nr"
        with self.assertRaises((AttributeError, TypeError)):
            setattr(held.info.entry, field, 99)
        operation = "setitem"
        with self.assertRaises((AttributeError, TypeError)):
            getattr(operator, operation)(held.info.entry.args, 0, 99)
        captured = copy.deepcopy(self.rows("kernel-snapshot-captured")[0])
        self.assertEqual((captured["pid"], captured["startTicks"], captured["tgid"], captured["acquisitionId"], captured["syscallInfoHex"], captured["infoLength"], captured["capacity"]), (11, "2", 11, 1, original.hex(), 80, 88))
        self.assertEqual(captured["decoded"]["populated"]["nr"], 63)
        self.assertEqual(tuple(captured["decoded"]["populated"]["args"]), _ARGS)
        self.assert_provenance(self.owner.dispatch(held))
        self.assertEqual(self.rows("kernel-snapshot-captured")[0], captured)

    def test_mutating_nested_sink_fields_cannot_change_owner_or_future_return(self) -> None:
        def hostile_sink(event: str, **fields) -> None:
            self.emit(event, **fields)
            if event == "kernel-snapshot-captured" and fields["decoded"]["op"] == 1:
                fields["decoded"]["populated"]["nr"] = 999
                fields["decoded"]["populated"]["args"] = (999,) * 6
            if event == "syscall-entry":
                fields["entryProvenance"]["args"] = (999,) * 6
                fields["entryProvenance"]["number"] = 999
        self.owner = self.make_owner(emit=hostile_sink)
        self.owner.register(11, start_ticks="2", tgid=11)
        held = self.capture(_entry(), 1)

        proof = self.owner.dispatch(held)
        self.owner.resume(11)
        pair = self.owner.dispatch(self.capture(_exit(7), 2))

        self.assert_provenance(proof)
        self.assertEqual(held.info.entry, pairing.EntryInfo(63, _ARGS))
        self.assertEqual(pair, (proof, pairing.ExitInfo(7, False)))
        self.assert_event_provenance(self.rows("syscall-return")[0])
        self.owner.resume(11)
        self.owner.require_complete()

    def test_mutation_failures_keep_evidence_unsafe_and_never_publish_success(self) -> None:
        for selector, failure, code, expected_calls in _mutation_cases():
            with self.subTest(selector=selector, failure=failure):
                held, acquisition, raw, original, before, successes, entries, faults = self.prepare_mutation_failure(selector, failure)

                with self.assertRaises((RuntimeError, OSError)) as raised:
                    self.owner.dispatch(held, **faults)

                error = self.assert_error(code, acquisition=acquisition)
                self.assertEqual(error["reason"], selector)
                self.assertEqual(self.transport.registers, original)
                self.assertEqual(held.syscallInfo, raw)
                self.assertEqual(len(self.rows("kernel-register-mutation-result")), successes)
                self.assertEqual(len(self.rows("syscall-entry")), entries)
                self.assertEqual(self.rows("syscall-return"), [])
                self.assertEqual(self.transport.calls[before:], expected_calls)
                self.assert_mutation_failure_fields(selector, failure, error, original, raw, raised.exception)
                self.assert_sticky(acquisition)

    def test_rebind_attempt_during_register_read_cannot_reenable_write(self) -> None:
        held = self.capture(_entry(), 1)
        failures = []
        def rebind() -> None:
            try:
                self.owner.register(11, start_ticks="changed", tgid=11)
            except RuntimeError as error:
                failures.append(error)
        self.transport.after_read = rebind

        with self.assertRaises(RuntimeError):
            self.owner.dispatch(held, fault_result=-errno.EIO)

        self.assertEqual(len(failures), 1)
        self.assertEqual([row[0] for row in self.transport.calls], [0x4204])
        self.assertEqual(self.rows("kernel-register-mutation-result"), [])
        self.assert_sticky()

    def test_programming_error_in_policy_propagates_without_admission_success_or_mutation(self) -> None:
        bug = LookupError("synthetic programming error")
        def broken(snapshot: object) -> None:
            raise bug
        self.owner = self.make_owner(policy=broken)
        self.owner.register(11, start_ticks="2", tgid=11)
        held = self.capture(_entry(), 1)

        with self.assertRaises(LookupError) as raised:
            self.owner.dispatch(held)

        self.assertIs(raised.exception, bug)
        self.assertEqual(self.transport.calls, [])
        self.assertEqual(self.rows("syscall-entry"), [])

    def test_fault_selection_rejects_unsupported_values_and_selection_on_exit(self) -> None:
        for fault in (0, errno.EIO, -errno.EPERM, -errno.ENOSYS):
            with self.subTest(fault=fault):
                self.setUp()
                held = self.capture(_entry(), 1)

                with self.assertRaises(ValueError):
                    self.owner.dispatch(held, fault_result=fault)

                self.assertEqual(self.transport.calls, [])
                self.assertEqual(self.rows("syscall-entry"), [])
        self.setUp()
        self.assert_provenance(self.owner.dispatch(self.capture(_entry(), 1)))
        self.owner.resume(11)
        held = self.capture(_exit(7), 2)
        before = list(self.transport.calls)
        with self.assertRaises(ValueError):
            self.owner.dispatch(held, fault_result=-errno.EIO)
        self.assertEqual(self.transport.calls, before)
        self.assertEqual(self.rows("syscall-return"), [])

    def test_resume_transport_failure_invalidates_before_transport_and_cannot_retry(self) -> None:
        self.assert_provenance(self.owner.dispatch(self.capture(_entry(), 1)))
        self.transport.failure = "resume-error"

        with self.assertRaises((RuntimeError, OSError)) as raised:
            self.owner.resume(11)

        self.assert_original_cause(raised.exception)
        error = self.assert_error("PTRACE_SYSCALL")
        self.assertEqual(error["errno"], errno.EIO)
        self.assertEqual(self.rows("kernel-snapshot-invalidated")[0]["reason"], "resume")
        self.assertEqual(self.transport.calls, [(24, 11, 0)])
        self.assert_sticky()

    def test_absolute_deadline_prevents_progress_and_cannot_be_cleared(self) -> None:
        self.assert_provenance(self.owner.dispatch(self.capture(_entry(), 1)))
        self.now = 10.0

        with self.assertRaises(TimeoutError):
            self.owner.resume(11)

        self.assert_error("deadline")
        self.assertEqual(self.transport.calls, [])
        self.now = 1.0
        self.assert_sticky()

    def test_invalid_capture_is_sticky_without_a_register_or_resume_fallback(self) -> None:
        cases = ((_entry()[:79], 79, 88), (bytes([3]) + _entry()[1:], 80, 88), (_entry(), 79, 80))
        contexts = ((0, 1, []), (1, 3, [(24, 11, 0), (24, 11, 0)]))
        for prior_pairs, acquisition, expected_calls in contexts:
            for packet, available, capacity in cases:
                with self.subTest(prior_pairs=prior_pairs, packet=packet.hex(), available=available, capacity=capacity):
                    self.setUp()
                    for _ in range(prior_pairs):
                        self.complete_pair()
                    raw = bytearray(packet)
                    original = bytes(raw)

                    with self.assertRaises(ValueError):
                        self.owner.capture(11, start_ticks="2", tgid=11, acquisition_id=acquisition, raw=raw, available=available, capacity=capacity)

                    error = self.assert_error("invalid-info", acquisition=acquisition)
                    self.assert_sticky(acquisition)
                    self.assertEqual(self.transport.calls, expected_calls)
                    for field in ("syscallInfoHex", "available", "capacity", "acquisitionId"):
                        self.assertIn(field, error)
                    self.assertEqual((error["syscallInfoHex"], error["available"], error["capacity"], error["acquisitionId"]), (original.hex(), available, capacity, acquisition))
                    retained = copy.deepcopy(error)
                    raw[:] = b"\xff" * len(raw)
                    self.assertEqual(error, retained)
                    self.assertEqual(error["syscallInfoHex"], original.hex())

    def test_import_and_construction_leave_host_io_configuration_and_wrappers_untouched(self) -> None:
        originals = (builtins.open, io.open, os.open, os.getenv, os.kill, signal.signal)
        targets = ("builtins.open", "io.open", "os.open", "os.getenv", "os.kill", "signal.signal", "ctypes.CDLL")

        with ExitStack() as stack:
            for target in targets:
                stack.enter_context(patch(target, side_effect=AssertionError("unexpected import or constructor host operation")))
            guarded = (builtins.open, io.open, os.open, os.getenv, os.kill, signal.signal)
            stack.enter_context(patch.object(type(os.environ), "__getitem__", side_effect=AssertionError("unexpected environment read")))
            importlib.reload(pairing)
            self.owner = self.make_owner()
            self.owner.register(11, start_ticks="2", tgid=11)
            self.assertEqual((builtins.open, io.open, os.open, os.getenv, os.kill, signal.signal), guarded)

        self.assertEqual((builtins.open, io.open, os.open, os.getenv, os.kill, signal.signal), originals)
        self.complete_pair()


if __name__ == "__main__":
    unittest.main(verbosity=2)
