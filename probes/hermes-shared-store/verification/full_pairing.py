"""FULL syscall-stop pairing and conditional AArch64 register mutation.

Acquisition, transport ownership, output admission and process cleanup belong to
callers. This owner never acquires kernel stops or resumes during dispatch.
"""

from __future__ import annotations

__all__ = [
    "EntryInfo",
    "ExitInfo",
    "SyscallInfo",
    "StopSnapshot",
    "EntryProvenance",
    "decode_syscall_info",
    "FullPairing",
]

import copy
import ctypes
import errno
import struct
from collections.abc import Callable
from dataclasses import dataclass
from typing import NamedTuple, NoReturn


class EntryInfo(NamedTuple):
    nr: int
    args: tuple[int, ...]


class ExitInfo(NamedTuple):
    rval: int
    is_error: bool


class SyscallInfo(NamedTuple):
    op: int
    arch: int
    instructionPointer: int
    stackPointer: int
    populated: EntryInfo | ExitInfo

    @property
    def entry(self) -> EntryInfo:
        if self.op != 1 or not isinstance(self.populated, EntryInfo):
            raise ValueError("inactive ENTRY arm")
        return self.populated

    @property
    def exit(self) -> ExitInfo:
        if self.op != 2 or not isinstance(self.populated, ExitInfo):
            raise ValueError("inactive EXIT arm")
        return self.populated


class StopSnapshot(NamedTuple):
    acquisitionId: int
    pid: int
    startTicks: str
    tgid: int
    syscallInfo: bytes
    infoLength: int
    info: SyscallInfo


class EntryProvenance(NamedTuple):
    acquisitionId: int
    pid: int
    startTicks: str
    tgid: int
    number: int
    args: tuple[int, ...]


def _is_error(value: int) -> bool:
    return -4095 <= value <= -1


def decode_syscall_info(raw: bytes, available: int, capacity: int) -> SyscallInfo:
    if capacity != 88 or not 24 <= available <= capacity or len(raw) != available:
        raise ValueError("missing/short syscall-info or unsupported capacity")
    op, arch, ip, sp = struct.unpack_from("<B3xIQQ", raw)
    if raw[1:4] != bytes(3) or arch != 0xC00000B7:
        raise ValueError("unsupported syscall-info reserved fields or architecture")
    if op == 1:
        if available < 80:
            raise ValueError("short syscall-info ENTRY")
        number, *args = struct.unpack_from("<7Q", raw, 24)
        populated = EntryInfo(number, tuple(args))
    elif op == 2:
        if available < 33:
            raise ValueError("short syscall-info EXIT")
        value, flag = struct.unpack_from("<qB", raw, 24)
        if flag not in (0, 1) or bool(flag) != _is_error(value):
            raise ValueError("inconsistent syscall-info EXIT error flag")
        populated = ExitInfo(value, bool(flag))
    else:
        raise ValueError("unsupported syscall-info arm")
    return SyscallInfo(op, arch, ip, sp, populated)


class _IOVec(ctypes.Structure):
    _fields_ = [("base", ctypes.c_void_p), ("length", ctypes.c_size_t)]


class _Pending(NamedTuple):
    provenance: EntryProvenance
    fault_result: int | None
    denied: bool


@dataclass(slots=True, kw_only=True)
class _Task:
    pid: int
    start_ticks: str
    tgid: int
    snapshot: StopSnapshot | None = None
    pending: _Pending | None = None
    stopped: bool = False
    eligible: bool = False
    dispatched: bool = False
    ready: bool = False
    processing: bool = False


class FullPairing:
    def __init__(
        self,
        *,
        deadline: float,
        policy: Callable[[StopSnapshot], None],
        ptrace: Callable[[int, int, int, int], int],
        emit: Callable[..., None],
        clock: Callable[[], float],
    ) -> None:
        self._deadline = deadline
        self._policy = policy
        self._ptrace = ptrace
        self._sink = emit
        self._clock = clock
        self._tasks: dict[int, _Task] = {}
        self._unsafe: dict[int | None, int | None] = {}
        self._last_acquisition = 0

    def _emit(self, event: str, **fields: object) -> None:
        try:
            self._sink(event, **copy.deepcopy(fields))
        except Exception:
            pid = fields.get("pid")
            if isinstance(pid, int) and pid in self._tasks:
                task = self._tasks[pid]
                self._unsafe.setdefault(pid, task.snapshot.acquisitionId if task.snapshot else None)
                task.eligible = False
            raise

    def _common(self, task: _Task | None, *, pid: int | None = None, acquisition: int | None = None) -> dict:
        return {
            "pid": task.pid if task else pid,
            "startTicks": task.start_ticks if task else None,
            "tgid": task.tgid if task else None,
            "acquisitionId": acquisition if acquisition is not None else task.snapshot.acquisitionId if task and task.snapshot else None,
        }

    def _invalidate(self, task: _Task, reason: str) -> None:
        if task.eligible:
            task.eligible = False
            self._emit("kernel-snapshot-invalidated", **self._common(task), reason=reason)

    def _record_error(
        self, code: str, task: _Task | None = None, *, pid: int | None = None,
        acquisition: int | None = None, unsafe: bool = True, details: dict | None = None,
    ) -> None:
        common = self._common(task, pid=pid, acquisition=acquisition)
        key = common["pid"]
        if unsafe:
            self._unsafe.setdefault(key, common["acquisitionId"])
            if task is not None:
                self._invalidate(task, "failure")
        self._emit("observer-error", **common, errorCode=code, unsafe=unsafe,
                   unsafeAcquisitionId=self._unsafe.get(key) if unsafe else None, **(details or {}))

    def _fail(
        self, code: str, task: _Task | None = None, *, error: Exception | None = None,
        pid: int | None = None, acquisition: int | None = None, details: dict | None = None,
        **fields: object,
    ) -> NoReturn:
        self._record_error(code, task, pid=pid, acquisition=acquisition, details={**(details or {}), **fields})
        if error is not None:
            raise error
        raise RuntimeError(code)

    def _safe(self, task: _Task, *, acquisition: int | None = None, **fields: object) -> None:
        if task.pid in self._unsafe:
            self._fail("ownership", task, acquisition=acquisition, details=fields)
        if self._clock() >= self._deadline:
            self._fail("deadline", task, error=TimeoutError("component deadline"),
                       acquisition=acquisition, details=fields)

    def register(self, pid: int, *, start_ticks: str, tgid: int) -> None:
        existing = self._tasks.get(pid)
        if existing is not None or pid <= 0 or tgid <= 0 or not start_ticks:
            self._fail("ownership", existing, pid=pid)
        task = _Task(pid=pid, start_ticks=start_ticks, tgid=tgid)
        self._safe(task)
        self._tasks[pid] = task

    def capture(
        self, pid: int, *, start_ticks: str, tgid: int, acquisition_id: int,
        raw: bytes | bytearray, available: int, capacity: int = 88,
    ) -> StopSnapshot:
        task = self._tasks.get(pid)
        if task is None or (task.start_ticks, task.tgid) != (start_ticks, tgid):
            self._fail("ownership", task, pid=pid, acquisition=acquisition_id)
        if acquisition_id <= self._last_acquisition:
            self._fail("replay", task, acquisition=acquisition_id)
        self._safe(task, acquisition=acquisition_id)
        if task.stopped or task.processing:
            self._fail("ownership", task, acquisition=acquisition_id)
        frozen = bytes(raw)
        try:
            info = decode_syscall_info(frozen, available, capacity)
        except ValueError as error:
            self._fail("invalid-info", task, acquisition=acquisition_id, error=error,
                       syscallInfoHex=frozen.hex(), available=available, capacity=capacity)
        held = StopSnapshot(acquisition_id, pid, start_ticks, tgid, frozen, available, info)
        self._last_acquisition = acquisition_id
        task.snapshot = held
        task.stopped = task.eligible = True
        task.dispatched = task.ready = False
        task.processing = True
        try:
            decoded = {**info._asdict(), "populated": info.populated._asdict()}
            self._emit("kernel-snapshot-captured", **self._common(task), syscallInfoHex=frozen.hex(),
                       infoLength=available, capacity=capacity, decoded=decoded)
            self._safe(task)
        finally:
            task.processing = False
        return held

    def dispatch(
        self, snapshot: StopSnapshot, *, fault_result: int | None = None,
    ) -> EntryProvenance | tuple[EntryProvenance, ExitInfo]:
        task = self._tasks.get(snapshot.pid)
        if task is None:
            self._fail("ownership", pid=snapshot.pid, acquisition=snapshot.acquisitionId)
        self._safe(task)
        if snapshot is not task.snapshot or not task.stopped or not task.eligible or task.dispatched or task.processing:
            self._fail("replay", task)
        task.dispatched = True
        task.processing = True
        try:
            if fault_result is not None and (fault_result not in (-errno.EIO, -errno.ENOSPC) or snapshot.info.op != 1):
                self._fail("ownership", task, error=ValueError("unsupported fault selection"))
            if snapshot.info.op == 1:
                if task.pending is not None:
                    self._fail("missing-return", task)
                entry = snapshot.info.entry
                proof = EntryProvenance(snapshot.acquisitionId, task.pid, task.start_ticks, task.tgid, entry.nr, entry.args)
                denial = None
                try:
                    self._policy(snapshot)
                except PermissionError as error:
                    denial = error
                self._safe(task)
                if denial is not None or fault_result is not None:
                    self._mutate(task, snapshot, reason="admission-refusal" if denial else "fault-entry")
                task.pending = _Pending(proof, None if denial else fault_result, denial is not None)
                self._emit("syscall-entry", **self._common(task), entryProvenance=proof._asdict(),
                           faultResult=task.pending.fault_result, admissionDenied=task.pending.denied)
                self._safe(task)
                task.ready = True
                if denial is not None:
                    self._record_error("admission-refusal", task, unsafe=False)
                    self._safe(task)
                    raise denial
                return proof
            pending = task.pending
            if pending is None:
                self._fail("unmatched-return", task)
            observed = snapshot.info.exit
            delivered = observed.rval
            if pending.fault_result is not None:
                self._mutate(task, snapshot, reason="fault-return", result=pending.fault_result)
                delivered = pending.fault_result
            self._emit("syscall-return", **self._common(task), entryProvenance=pending.provenance._asdict(),
                       result=observed.rval, isError=observed.is_error, deliveredResult=delivered,
                       deliveredIsError=_is_error(delivered), injected=pending.denied or pending.fault_result is not None,
                       admissionDenied=pending.denied)
            self._safe(task)
            task.pending = None
            task.ready = True
            return pending.provenance, observed
        finally:
            task.processing = False

    def resume(self, pid: int) -> None:
        task = self._tasks.get(pid)
        if task is None:
            self._fail("ownership", pid=pid)
        if pid in self._unsafe:
            self._fail("unsafe-resume", task)
        self._safe(task)
        if not task.stopped or not task.dispatched or not task.ready or task.processing:
            self._fail("ownership", task)
        task.processing = True
        try:
            self._invalidate(task, "resume")
            self._safe(task)
            task.stopped = task.ready = False
            try:
                result = self._ptrace(24, pid, 0, 0)
            except OSError as error:
                self._fail("PTRACE_SYSCALL", task, error=error, errno=error.errno)
            self._safe(task)
            if result != 0:
                self._fail("PTRACE_SYSCALL", task, ptraceResult=result, errno=None)
        finally:
            task.processing = False

    def require_complete(self) -> None:
        if self._unsafe:
            pid = next(iter(self._unsafe))
            self._fail("ownership", self._tasks.get(pid) if pid is not None else None, pid=pid)
        for task in self._tasks.values():
            self._safe(task)
            if task.pending is not None:
                self._fail("missing-return", task, acquisition=task.pending.provenance.acquisitionId)
            if task.processing or task.stopped and not task.ready:
                self._fail("ownership", task)

    def _read_registers(
        self, task: _Task, held: StopSnapshot, *, reason: str, note: int, capacity: int,
        general: bytes | None = None,
    ) -> bytes:
        fields = {"reason": reason, "noteType": note, "subphase": "GETREGSET",
                  "capacity": capacity, "originalRegisterBytesHex": general.hex() if general else None,
                  "originalScalarBytesHex": None, "syscallInfoHex": held.syscallInfo.hex()}
        self._safe(task, **fields)
        self._emit("conditional-register-read", **self._common(task), reason=reason, noteType=note, capacity=capacity)
        self._safe(task, **fields)
        values = (ctypes.c_ubyte * capacity)()
        iov = _IOVec(ctypes.addressof(values), capacity)
        try:
            result = self._ptrace(0x4204, task.pid, note, ctypes.addressof(iov))
        except OSError as error:
            self._fail("GETREGSET", task, error=error, available=iov.length, errno=error.errno, **fields)
        self._safe(task, **fields)
        if result != 0 or iov.length != capacity:
            self._fail("GETREGSET", task, available=iov.length, ptraceResult=result, errno=None, **fields)
        return bytes(values)

    def _mutate(self, task: _Task, held: StopSnapshot, *, reason: str, result: int | None = None) -> None:
        general = self._read_registers(task, held, reason=reason, note=1, capacity=272)
        registers = struct.unpack("<34Q", general)
        entry = held.info.op == 1
        agrees = (registers[8], registers[:6]) == (held.info.entry.nr, held.info.entry.args) if entry else struct.unpack_from("<q", general)[0] == held.info.exit.rval
        if not agrees:
            self._fail("GETREGSET-agreement", task, reason=reason, noteType=1, subphase="GETREGSET",
                       originalRegisterBytesHex=general.hex(), originalScalarBytesHex=None,
                       syscallInfoHex=held.syscallInfo.hex())
        scalar = self._read_registers(task, held, reason=reason, note=0x404, capacity=4, general=general) if entry else None
        if scalar is not None and struct.unpack("<i", scalar)[0] != held.info.entry.nr:
            self._fail("GETREGSET-agreement", task, reason=reason, noteType=0x404, subphase="GETREGSET",
                       originalRegisterBytesHex=general.hex(), originalScalarBytesHex=scalar.hex(),
                       syscallInfoHex=held.syscallInfo.hex())
        changed = bytearray(general)
        struct.pack_into("<q", changed, 0, -errno.ENOSYS if entry else result)
        writes = [(0x404, struct.pack("<i", -1)), (1, bytes(changed))] if entry else [(1, bytes(changed))]
        self._invalidate(task, "register-mutation")
        completed = []
        receipts = []
        for note, payload in writes:
            mutation = {**self._common(task), "reason": reason, "noteType": note,
                        "syscallInfoHex": held.syscallInfo.hex(), "infoLength": held.infoLength,
                        "originalRegisterBytesHex": general.hex(), "originalScalarBytesHex": scalar.hex() if scalar else None,
                        "writtenRegisterBytesHex": payload.hex(), "subphase": "SETREGSET"}
            self._safe(task, reason=reason, noteType=note, subphase="SETREGSET", completedTransfers=completed)
            self._emit("kernel-register-mutation", **mutation, writeResult="pending")
            self._safe(task, reason=reason, noteType=note, subphase="SETREGSET", completedTransfers=completed)
            values = (ctypes.c_ubyte * len(payload)).from_buffer_copy(payload)
            iov = _IOVec(ctypes.addressof(values), len(payload))
            try:
                returned = self._ptrace(0x4205, task.pid, note, ctypes.addressof(iov))
            except OSError as error:
                transfer = {"requested": len(payload), "returned": iov.length, "ptraceResult": None, "outcome": "error"}
                self._fail("SETREGSET", task, error=error, reason=reason, noteType=note, subphase="SETREGSET",
                           mutation=mutation, transfer=transfer, completedTransfers=completed, errno=error.errno)
            transfer = {"requested": len(payload), "returned": iov.length, "ptraceResult": returned,
                        "outcome": "error" if returned != 0 else "short" if iov.length != len(payload) else "full"}
            if transfer["outcome"] != "full":
                self._fail("SETREGSET" if returned != 0 else "SETREGSET-short", task, reason=reason, noteType=note,
                           subphase="SETREGSET", mutation=mutation, transfer=transfer, completedTransfers=completed, errno=None)
            completed.append({"noteType": note, "transfer": transfer})
            receipts.append((mutation, transfer))
            self._safe(task, reason=reason, noteType=note, subphase="SETREGSET", mutation=mutation,
                       transfer=transfer, completedTransfers=completed)
        for mutation, transfer in receipts:
            self._emit("kernel-register-mutation-result", **mutation, writeResult="success", transfer=transfer)
            self._safe(task, reason=reason, noteType=mutation["noteType"], subphase="SETREGSET", completedTransfers=completed)
