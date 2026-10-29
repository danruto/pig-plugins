#!/usr/bin/env python3
import errno
import fcntl
import os
from pathlib import Path
import pty
import select
import signal
import struct
import subprocess
import tempfile
import termios
import time

project = Path(__file__).resolve().parent.parent
package = Path(os.environ.get("PIG_STATUS_PACKAGE", project))
with tempfile.TemporaryDirectory(prefix="pig-status-tui-") as tmp:
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 120, 0, 0))
    env = dict(
        os.environ,
        HOME=tmp,
        PIG_HOME=tmp,
        PIG_CODING_AGENT_DIR=str(Path(tmp) / "agent"),
        PI_CODING_AGENT_DIR=str(Path(tmp) / "pi-agent"),
        PI_HOME=str(Path(tmp) / "pi"),
        TERM="xterm-256color",
    )
    process = subprocess.Popen(
        ["pig", "--approve", "--no-extensions", "--no-session", "-e", str(package), "-e", str(project / "test/fixtures/claude-usage.ts"), "--model", "claude-bridge/fixture"],
        cwd=tmp, env=env, stdin=slave, stdout=slave, stderr=slave, start_new_session=True,
    )
    os.close(slave)
    try:
        captured = bytearray()

        def read_terminal(timeout: float) -> bytes | None:
            if not select.select([master], [], [], timeout)[0]:
                return None
            try:
                return os.read(master, 65536)
            except OSError as error:
                if error.errno != errno.EIO:
                    raise
                return b""

        def await_footer() -> None:
            phase = bytearray()
            deadline = time.monotonic() + 45
            while time.monotonic() < deadline:
                chunk = read_terminal(0.1)
                if chunk == b"":
                    break
                if chunk is not None:
                    captured.extend(chunk)
                    phase.extend(chunk)
                if b"used Claude 5h 6%" in phase:
                    return
                if process.poll() is not None:
                    break
            raise AssertionError("Native footer did not show Claude usage:\n" + captured.decode(errors="replace"))

        def await_shutdown() -> None:
            deadline = time.monotonic() + 15
            while (remaining := deadline - time.monotonic()) > 0:
                chunk = read_terminal(min(0.1, remaining))
                if chunk == b"":
                    process.wait(timeout=max(0, deadline - time.monotonic()))
                    return
                if chunk is not None:
                    captured.extend(chunk)
            raise AssertionError("Native footer did not shut down:\n" + captured.decode(errors="replace"))

        await_footer()
        fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 80, 0, 0))
        os.kill(process.pid, signal.SIGWINCH)
        await_footer()
        os.write(master, b"\x04")
        await_shutdown()
        output = captured.decode(errors="replace")
        assert process.returncode == 0, (process.returncode, output)
        assert b"panic" not in captured.lower(), output
        print("Native footer: Claude usage visible at 120 and 80 columns; clean exit; no model prompt")
    finally:
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=5)
        os.close(master)
