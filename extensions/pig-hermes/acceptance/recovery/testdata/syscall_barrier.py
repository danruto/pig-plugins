"""Disposable syscall barriers for recovery; no production hooks or shared data."""
import ctypes
import errno
import json
import os
import pathlib
import signal
import sys

libc = ctypes.CDLL(None, use_errno=True)
libc.ptrace.restype = ctypes.c_long
machine = os.uname().machine


def ptrace(op, pid, addr=0, data=0):
    result = libc.ptrace(op, pid, ctypes.c_void_p(addr), ctypes.c_void_p(data))
    if result == -1 and ctypes.get_errno():
        raise OSError(ctypes.get_errno(), os.strerror(ctypes.get_errno()))
    return result


class IOVec(ctypes.Structure):
    _fields_ = [("base", ctypes.c_void_p), ("length", ctypes.c_size_t)]


def registers(pid):
    values = (ctypes.c_ulonglong * (34 if machine == "aarch64" else 27))()
    vector = IOVec(ctypes.addressof(values), ctypes.sizeof(values))
    ptrace(0x4204, pid, 1, ctypes.addressof(vector))
    return values


def set_registers(pid, values):
    vector = IOVec(ctypes.addressof(values), ctypes.sizeof(values))
    ptrace(0x4205, pid, 1, ctypes.addressof(vector))


def string_at(pid, address):
    result = bytearray()
    while len(result) < 4096:
        ctypes.set_errno(0)
        word = ptrace(2, pid, address + len(result))
        data = (word & ((1 << 64) - 1)).to_bytes(8, "little")
        if 0 in data:
            result.extend(data[:data.index(0)])
            return os.fsdecode(bytes(result))
        result.extend(data)
    raise RuntimeError("unterminated syscall path")


def syscall_paths(pid, values):
    number = values[8 if machine == "aarch64" else 15]
    args = list(values[:6]) if machine == "aarch64" else [values[x] for x in [14, 13, 12, 7, 9, 8]]
    renames = {38, 276} if machine == "aarch64" else {264, 316}
    links = {37} if machine == "aarch64" else {265}
    if number in renames | links:
        return ("rename" if number in renames else "link", string_at(pid, args[1]), string_at(pid, args[3]))
    if machine == "x86_64" and number in {82, 86}:
        return ("rename" if number == 82 else "link", string_at(pid, args[0]), string_at(pid, args[1]))
    return None


def main():
    binary, mode, database, report, pidroot = sys.argv[1:]
    if machine not in {"aarch64", "x86_64"}:
        raise RuntimeError("unsupported ptrace architecture")
    child = os.fork()
    if child == 0:
        ptrace(0, 0)
        os.execv(binary, [binary])
    receipt = pathlib.Path(pidroot) / str(child)
    receipt.write_text("running-traced-child")
    os.waitpid(child, 0)
    options = 1 | 8 | (1 << 20)
    ptrace(0x4200, child, 0, options)
    states = {}
    events = []
    moved = False
    killed = False
    ptrace(24, child)
    try:
        while True:
            try:
                pid, status = os.waitpid(-1, 0x40000000)
            except ChildProcessError:
                break
            if os.WIFEXITED(status) or os.WIFSIGNALED(status):
                states.pop(pid, None)
                continue
            sig = os.WSTOPSIG(status)
            if sig == (signal.SIGTRAP | 128):
                entering, call, injected = states.get(pid, (True, None, False))
                values = registers(pid)
                if entering:
                    call = syscall_paths(pid, values)
                    injected = False
                    if call:
                        kind, source, target = call
                        if mode == "restore-fail" and moved and target == database:
                            values[8 if machine == "aarch64" else 15] = (1 << 64) - 1
                            set_registers(pid, values)
                            injected = True
                    states[pid] = (False, call, injected)
                else:
                    result_index = 0 if machine == "aarch64" else 10
                    if injected:
                        values[result_index] = (1 << 64) - errno.EACCES
                        set_registers(pid, values)
                    result = ctypes.c_longlong(values[result_index]).value
                    if call:
                        kind, source, target = call
                        event = dict(kind=kind, source=source, target=target, result=result, injected=injected)
                        events.append(event)
                        if kind == "rename" and source == database and target.startswith(database + ".corrupt-") and result == 0:
                            moved = True
                            suffix = {"crash-main": "", "crash-wal": "-wal", "crash-shm": "-shm"}.get(mode)
                            if suffix == "":
                                killed = True
                                os.kill(child, signal.SIGKILL)
                        if mode in {"crash-wal", "crash-shm"} and kind == "rename" and source == database + {"crash-wal": "-wal", "crash-shm": "-shm"}[mode] and result == 0:
                            killed = True
                            os.kill(child, signal.SIGKILL)
                    states[pid] = (True, None, False)
                if not killed:
                    ptrace(24, pid)
            else:
                deliver = 0 if sig in {signal.SIGSTOP, signal.SIGTRAP} else sig
                try:
                    ptrace(24, pid, 0, deliver)
                except ProcessLookupError:
                    pass
    finally:
        try:
            os.kill(child, signal.SIGKILL)
        except ProcessLookupError:
            pass
        while True:
            try:
                pid, status = os.waitpid(-1, 0x40000000)
                if os.WIFSTOPPED(status):
                    ptrace(24, pid, 0, signal.SIGKILL)
            except ChildProcessError:
                break
        pathlib.Path(report).write_text(json.dumps(dict(BackupMoved=moved, Killed=killed, Events=events), indent=2))
        receipt.write_text("joined-traced-child")


main()
