"""Public RunContext contracts against private Linux fixtures and serial owned children."""

from __future__ import annotations

import errno
import importlib
import json
import math
import operator
import os
import resource
import shutil
import stat
import struct
import subprocess
import sys
import tempfile
import time
import unittest
from collections.abc import Callable
from contextlib import ExitStack
from pathlib import Path
from typing import override
from unittest.mock import patch

import run_context

_FSTAT = os.fstat
_FLOOR = 2 * 1024**3


def _fds() -> dict[int, str]:
    result = {}
    for name in os.listdir('/proc/self/fd'):
        try:
            result[int(name)] = os.readlink(f'/proc/self/fd/{name}')
        except FileNotFoundError:
            continue
    return result


def _scratch() -> Path:
    root = Path(os.environ['RUN_CONTEXT_TEST_ROOT'])
    info = root.lstat()
    if root != root.resolve() or not stat.S_ISDIR(info.st_mode):
        raise RuntimeError('test root must be a canonical directory')
    if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise RuntimeError('test root must be owned and private')
    if shutil.disk_usage(root).free < _FLOOR:
        raise RuntimeError('test free-space floor not met')
    mask = os.umask(0o077)
    if mask != 0o077:
        os.umask(mask)
        raise RuntimeError('test requires inherited0077')
    return root


class _Budget:
    def __init__(self) -> None:
        self.calls: list[tuple[Path, int, int]] = []
        self.before: Callable[[Path, int, int], None] | None = None
        self.error: BaseException | None = None

    def __call__(self, path: Path, size: int, ticket: int) -> None:
        self.calls.append((path, size, ticket))
        if self.before is not None:
            self.before(path, size, ticket)
        if self.error is not None:
            raise self.error


class _Fixture(unittest.TestCase):
    @override
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory(dir=_scratch(), prefix='fixture-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.now = 1.0
        self.budget = _Budget()
        self.baseline = _fds()
        self.addCleanup(self.assert_fd_baseline)

    def clock(self) -> float:
        return self.now

    def make(self, **overrides: object) -> run_context.RunContext:
        options: dict[str, object] = {
            'budget': self.budget, 'ticket': 17, 'deadline': 10.0, 'clock': self.clock,
        }
        options.update(overrides)
        context = operator.call(run_context.RunContext, self.root, **options)
        self.addCleanup(context.close)
        return context

    def assert_private_file(self, path: Path, expected: bytes) -> None:
        info = path.lstat()
        self.assertTrue(stat.S_ISREG(info.st_mode))
        self.assertEqual((stat.S_IMODE(info.st_mode), info.st_uid, info.st_nlink),
                         (0o600, os.getuid(), 1))
        self.assertEqual(path.read_bytes(), expected)

    def assert_poisoned(self, context: run_context.RunContext) -> None:
        calls = list(self.budget.calls)
        with self.assertRaises(RuntimeError):
            context.write_artifact('after-error', b'x')
        with self.assertRaises(RuntimeError):
            context.append_log('after-log', b'x')
        self.assertEqual(self.budget.calls, calls)
        self.assertFalse((context.path / 'after-error').exists())
        self.assertFalse((context.path / 'after-log').exists())

    def assert_fd_baseline(self) -> None:
        self.assertEqual(_fds(), self.baseline)


class _RunContextCases(_Fixture):
    def test_fresh_namespaces_reuse_labels_without_reusing_bytes(self) -> None:
        first = self.make()
        old_log = first.append_log('stdout.log', b'older')
        old_artifact = first.write_artifact('report', b'old report')
        first.close()
        second = self.make()

        new_log = second.append_log('stdout.log', b'new')
        second.write_artifact('report', b'new report')
        second.append_log('stdout.log', b' appended')

        self.assertNotEqual(first.path, second.path)
        self.assertEqual(set(self.root.iterdir()), {first.path, second.path})
        self.assertEqual(first.path.parent, self.root)
        self.assertEqual(second.path.parent, self.root)
        self.assertEqual(set(second.path.iterdir()), {new_log, second.path / 'report'})
        self.assert_private_file(old_log, b'older')
        self.assert_private_file(old_artifact, b'old report')
        self.assert_private_file(new_log, b'new appended')
        self.assert_private_file(second.path / 'report', b'new report')
        self.assertEqual(stat.S_IMODE(second.path.lstat().st_mode), 0o700)
        attribute = 'path'
        with self.assertRaises((AttributeError, TypeError)):
            setattr(second, attribute, self.root)

    def test_constructor_creates_only_one_empty_private_namespace(self) -> None:
        context = self.make()

        self.assertEqual(list(self.root.iterdir()), [context.path])
        self.assertEqual(list(context.path.iterdir()), [])
        self.assertEqual(self.budget.calls, [])
        info = context.path.lstat()
        self.assertEqual((info.st_uid, stat.S_IMODE(info.st_mode)), (os.getuid(), 0o700))
        self.assertTrue(stat.S_ISDIR(info.st_mode))
        self.assertIs(context.__enter__(), context)

    def test_native_creation_is_private_before_return_without_mode_repair(self) -> None:
        real_open, real_mkdir = os.open, os.mkdir
        created = []

        def inspect(fd: int) -> None:
            target = os.readlink(f'/proc/self/fd/{fd}')
            if target != str(self.root) and not target.startswith(str(self.root) + '/'):
                return
            info = _FSTAT(fd)
            self.assertEqual(info.st_uid, os.getuid())
            expected = 0o700 if stat.S_ISDIR(info.st_mode) else 0o600
            self.assertEqual(stat.S_IMODE(info.st_mode), expected)
            if stat.S_ISREG(info.st_mode):
                self.assertEqual(info.st_nlink, 1)
                created.append(os.readlink(f'/proc/self/fd/{fd}'))

        def opening(path: str | bytes | os.PathLike[str] | os.PathLike[bytes],
                    flags: int, mode: int = 0o777, *, dir_fd: int | None = None) -> int:
            fd = real_open(path, flags, mode, dir_fd=dir_fd)
            try:
                inspect(fd)
            except AssertionError:
                os.close(fd)
                raise
            return fd

        def making(path: str | bytes | os.PathLike[str] | os.PathLike[bytes],
                   mode: int = 0o777, *, dir_fd: int | None = None) -> None:
            real_mkdir(path, mode, dir_fd=dir_fd)
            info = os.stat(path, dir_fd=dir_fd, follow_symlinks=False)
            self.assertEqual(stat.S_IMODE(info.st_mode), 0o700)
            self.assertEqual(info.st_uid, os.getuid())

        with (
            patch('os.open', side_effect=opening),
            patch('os.mkdir', side_effect=making),
            patch('os.chmod', side_effect=AssertionError('mode repair')),
            patch('os.fchmod', side_effect=AssertionError('mode repair')),
        ):
            context = self.make()
            artifact = context.write_artifact('report', b'private')
            log = context.append_log('stdout.log', b'log')

        self.assertEqual(set(created), {str(artifact), str(log)})
        self.assert_private_file(artifact, b'private')
        self.assert_private_file(log, b'log')

    def test_import_has_no_host_io_or_process_creation(self) -> None:
        with ExitStack() as stack:
            denied = [stack.enter_context(patch(target, side_effect=AssertionError(target)))
                      for target in ('os.open', 'os.mkdir', 'os.statvfs', 'os.fstatvfs',
                                     'builtins.open', 'os.getenv', 'subprocess.Popen',
                                     'socket.socket', 'threading.Thread.start',
                                     'os.stat', 'os.lstat', 'os.listdir', 'os.scandir',
                                     'os.readlink', 'os.getcwd')]
            module = importlib.reload(run_context)

        self.assertIsInstance(module.RunContext, type)
        self.assertTrue(all(not operation.called for operation in denied))
        self.assertEqual(list(self.root.iterdir()), [])

    def test_invalid_constructor_arguments_do_not_create_namespace(self) -> None:
        cases = ({'budget': None}, {'clock': None}, {'ticket': None}, {'ticket': '17'},
                 {'ticket': 1.5}, {'deadline': None}, {'deadline': '10'},
                 {'deadline': math.inf}, {'deadline': -math.inf}, {'deadline': math.nan})
        for changes in cases:
            with self.subTest(changes=changes):
                with self.assertRaises((ValueError, TypeError)):
                    self.make(**changes)
                self.assertEqual(list(self.root.iterdir()), [])
        self.assert_private_file(self.make().write_artifact('valid', b'ok'), b'ok')

    def test_invalid_names_and_data_refuse_before_budget_or_growth(self) -> None:
        context = self.make()
        cases = [('', b'x'), ('.', b'x'), ('..', b'x'), ('../neighbor', b'x'),
                 ('a/b', b'x'), ('/absolute', b'x'), ('a\x00b', b'x'),
                 (None, b'x'), (b'name', b'x'), ('text', 'not bytes'),
                 ('buffer', bytearray(b'x')), ('view', memoryview(b'x'))]
        for method in (context.write_artifact, context.append_log):
            for name, data in cases:
                with self.subTest(method=method.__name__, name=name):
                    with self.assertRaises((ValueError, TypeError)):
                        operator.call(method, name, data)
                    self.assertEqual(list(context.path.iterdir()), [])
                    self.assertEqual(self.budget.calls, [])
        self.assert_private_file(context.append_log('valid', b'ok'), b'ok')

    def test_missing_root_is_not_created(self) -> None:
        missing = self.root / 'missing'

        with self.assertRaises((RuntimeError, FileNotFoundError)):
            run_context.RunContext(missing, budget=self.budget, ticket=17,
                                   deadline=10.0, clock=self.clock)

        self.assertFalse(missing.exists())
        self.assertEqual(list(self.root.iterdir()), [])

    def test_regular_file_root_is_rejected_without_changing_older_bytes(self) -> None:
        target = self.root / 'not-directory'
        target.write_bytes(b'preserved')

        with self.assertRaises((RuntimeError, NotADirectoryError)):
            run_context.RunContext(target, budget=self.budget, ticket=17,
                                   deadline=10.0, clock=self.clock)

        self.assert_private_file(target, b'preserved')
        self.assertEqual(list(self.root.iterdir()), [target])
        self.assertEqual(self.budget.calls, [])

    def test_noncanonical_and_symlink_roots_cannot_take_neighbor(self) -> None:
        neighbor = self.root / 'neighbor'
        neighbor.mkdir(mode=0o700)
        marker = neighbor / 'canary'
        marker.write_bytes(b'preserved')
        alias = self.root / 'alias'
        alias.symlink_to(neighbor, target_is_directory=True)
        roots = (alias, neighbor / '..' / 'neighbor', Path(os.path.relpath(neighbor)))
        for root in roots:
            with self.subTest(root=root):
                with self.assertRaises((RuntimeError, ValueError)):
                    run_context.RunContext(root, budget=self.budget, ticket=17,
                                           deadline=10.0, clock=self.clock)
                self.assertEqual(set(neighbor.iterdir()), {marker})
                self.assert_private_file(marker, b'preserved')

    def test_permissive_inherited_umask_refuses_without_mode_repair(self) -> None:
        previous = os.umask(0o022)
        try:
            with self.assertRaises(RuntimeError):
                self.make()
            current = os.umask(0o022)
            self.assertEqual(current, 0o022)
        finally:
            os.umask(previous)

        self.assertEqual(list(self.root.iterdir()), [])
        self.assert_private_file(self.make().write_artifact('valid', b'ok'), b'ok')

    def test_permissive_default_acl_refuses_without_repair(self) -> None:
        entries = ((1, 7), (4, 7), (16, 7), (32, 0))
        acl = struct.pack('<I', 2) + b''.join(struct.pack('<HHI', tag, perm, 0xffffffff)
                                             for tag, perm in entries)
        canary = self.root / 'older-evidence'
        canary.write_bytes(b'preserved')
        os.setxattr(self.root, 'system.posix_acl_default', acl)
        try:
            with self.assertRaises(RuntimeError):
                self.make()
            self.assertEqual(os.getxattr(self.root, 'system.posix_acl_default'), acl)
            self.assertEqual(list(self.root.iterdir()), [canary])
            self.assert_private_file(canary, b'preserved')
        finally:
            os.removexattr(self.root, 'system.posix_acl_default')

        self.assert_private_file(self.make().write_artifact('valid', b'ok'), b'ok')

    def test_native_root_owner_or_mode_guard_refuses_metadata_drift(self) -> None:
        real_stat, real_lstat = os.stat, os.lstat
        identity = (self.root.stat().st_dev, self.root.stat().st_ino)

        def changed(info: os.stat_result, field: int, value: int) -> os.stat_result:
            values = list(info)
            values[field] = value
            return os.stat_result(values)

        for field, value in ((0, stat.S_IFDIR | 0o755), (4, os.getuid() + 1)):
            def alter(info: os.stat_result, field: int = field,
                      value: int = value) -> os.stat_result:
                return changed(info, field, value) if (info.st_dev, info.st_ino) == identity else info

            with self.subTest(field=field):
                with (
                    patch('os.stat', side_effect=lambda *a, **k: alter(real_stat(*a, **k))),
                    patch('os.lstat', side_effect=lambda *a, **k: alter(real_lstat(*a, **k))),
                    patch('os.fstat', side_effect=lambda fd: alter(_FSTAT(fd))),
                    self.assertRaises(RuntimeError),
                ):
                    self.make()
                self.assertEqual(list(self.root.iterdir()), [])
        self.assert_private_file(self.make().write_artifact('valid', b'ok'), b'ok')

    def test_budget_once_before_each_creation_and_append_including_empty(self) -> None:
        context = self.make()
        states = []

        def inspect(path: Path, size: int, ticket: int) -> None:
            states.append(path.read_bytes() if path.exists() else None)
            self.assertEqual(path.parent, context.path)

        self.budget.before = inspect
        artifact = context.write_artifact('report', b'abc')
        log = context.append_log('stdout.log', b'x')
        context.append_log('stdout.log', b'yz')
        empty = context.write_artifact('empty', b'')
        context.append_log('stdout.log', b'')
        empty_log = context.append_log('empty.log', b'')

        self.assertEqual(states, [None, None, b'x', None, b'xyz', None])
        self.assertEqual(self.budget.calls, [(artifact, 3, 17), (log, 1, 17),
                         (log, 2, 17), (empty, 0, 17), (log, 0, 17), (empty_log, 0, 17)])
        self.assert_private_file(artifact, b'abc')
        self.assert_private_file(log, b'xyz')
        self.assert_private_file(empty, b'')
        self.assert_private_file(empty_log, b'')

    def test_explicit_budget_refusal_preserves_bytes_and_context_usability(self) -> None:
        context = self.make()
        log = context.append_log('stdout.log', b'old')
        refusal = PermissionError('synthetic budget refusal')
        self.budget.error = refusal
        for method, name in ((context.write_artifact, 'report'),
                             (context.append_log, 'new.log'),
                             (context.append_log, 'stdout.log')):
            with self.subTest(name=name):
                with self.assertRaises(PermissionError) as caught:
                    method(name, b'blocked')
                self.assertIs(caught.exception, refusal)
                self.assertEqual(set(context.path.iterdir()), {log})
                self.assert_private_file(log, b'old')
        self.budget.error = None

        self.assert_private_file(context.append_log('stdout.log', b' current'), b'old current')
        self.assertEqual([size for _, size, _ in self.budget.calls], [3, 7, 7, 7, 8])

    def test_budget_revalidates_same_ticket_at_each_current_epoch(self) -> None:
        context = self.make()
        epoch = [1, 1]
        stale = RuntimeError('synthetic stale epoch')

        def current(path: Path, size: int, ticket: int) -> None:
            self.assertEqual(ticket, 17)
            if epoch[0] != epoch[1]:
                raise stale

        self.budget.before = current
        log = context.append_log('stdout.log', b'one')
        epoch[1] = 2
        with self.assertRaises(RuntimeError) as caught:
            context.append_log('stdout.log', b'stale')
        self.assertIs(caught.exception, stale)
        self.assert_private_file(log, b'one')
        epoch[0] = 2

        self.assert_private_file(context.append_log('stdout.log', b'two'), b'onetwo')
        self.assertEqual(self.budget.calls, [(log, 3, 17), (log, 5, 17), (log, 3, 17)])

    def test_deadline_refuses_constructor_and_debit_but_never_owned_close(self) -> None:
        self.now = 10.0
        with self.assertRaises(TimeoutError):
            self.make()
        self.assertEqual(list(self.root.iterdir()), [])
        self.now = 1.0
        context = self.make()
        retained = context.write_artifact('report', b'keep')
        calls = list(self.budget.calls)
        self.now = 10.0
        with self.assertRaises(TimeoutError):
            context.append_log('late.log', b'late')

        self.assertEqual(self.budget.calls, calls)
        self.assertEqual(set(context.path.iterdir()), {retained})
        context.close()
        context.close()
        self.assert_fd_baseline()
        self.assert_private_file(retained, b'keep')

    def test_oversized_real_bytes_refuse_without_lowering_per_file_limit(self) -> None:
        context = self.make()
        log = context.append_log('stdout.log', b'keep')
        oversized = bytes(128 * 1024**2 + 1)
        calls = list(self.budget.calls)
        for method, name in ((context.write_artifact, 'large'),
                             (context.append_log, 'stdout.log')):
            with self.subTest(name=name):
                with self.assertRaises(RuntimeError):
                    method(name, oversized)
                self.assertEqual(self.budget.calls, calls)
                self.assertEqual(set(context.path.iterdir()), {log})
                self.assert_private_file(log, b'keep')

    def test_free_floor_accounts_for_requested_growth_before_debit(self) -> None:
        context = self.make()
        real = os.statvfs(self.root)
        values = list(real)
        values[0:2] = [1, 1]
        values[3:5] = [_FLOOR + 2, _FLOOR + 2]
        low = os.statvfs_result(values)
        with (
            patch('os.statvfs', return_value=low),
            patch('os.fstatvfs', return_value=low),
            self.assertRaises(RuntimeError),
        ):
            context.write_artifact('report', b'abc')

        self.assertEqual(list(context.path.iterdir()), [])
        self.assertEqual(self.budget.calls, [])

    def test_constructor_free_floor_refuses_before_namespace_creation(self) -> None:
        values = list(os.statvfs(self.root))
        values[0:2] = [1, 1]
        values[3:5] = [_FLOOR - 1, _FLOOR - 1]
        low = os.statvfs_result(values)
        with (
            patch('os.statvfs', return_value=low),
            patch('os.fstatvfs', return_value=low),
            self.assertRaises(RuntimeError),
        ):
            self.make()

        self.assertEqual(list(self.root.iterdir()), [])
        self.assertEqual(self.budget.calls, [])
        self.assert_private_file(self.make().write_artifact('valid', b'ok'), b'ok')

    def test_write_refuses_new_permissive_mask_without_repair(self) -> None:
        context = self.make()
        previous = os.umask(0o022)
        try:
            with self.assertRaises(RuntimeError):
                context.write_artifact('wrong', b'x')
            current = os.umask(0o022)
            self.assertEqual(current, 0o022)
        finally:
            os.umask(previous)

        self.assertEqual(list(context.path.iterdir()), [])
        self.assertEqual(self.budget.calls, [])

    def test_file_collision_never_truncates_and_does_not_poison(self) -> None:
        context = self.make()
        target = context.write_artifact('report', b'retained')

        with self.assertRaises(FileExistsError):
            context.write_artifact('report', b'wrong')

        self.assert_private_file(target, b'retained')
        self.assert_private_file(context.write_artifact('next', b'ok'), b'ok')

    def test_artifact_cannot_be_relabelled_as_log(self) -> None:
        context = self.make()
        target = context.write_artifact('report', b'retained')

        with self.assertRaises((RuntimeError, FileExistsError)):
            context.append_log('report', b'wrong')

        self.assert_private_file(target, b'retained')

    def test_unregistered_regular_file_cannot_be_taken_as_log(self) -> None:
        context = self.make()
        target = context.path / 'foreign.log'
        target.write_bytes(b'foreign')

        with self.assertRaises((RuntimeError, FileExistsError)):
            context.append_log('foreign.log', b'wrong')

        self.assert_private_file(target, b'foreign')

    def test_symlink_and_hardlink_collisions_preserve_owned_neighbor_canaries(self) -> None:
        context = self.make()
        canary = self.root / 'canary'
        canary.write_bytes(b'neighbor')
        (context.path / 'symlink').symlink_to(canary)
        os.link(canary, context.path / 'hardlink')
        for name in ('symlink', 'hardlink'):
            for method in (context.write_artifact, context.append_log):
                with self.subTest(name=name, method=method.__name__):
                    with self.assertRaises((RuntimeError, FileExistsError)):
                        method(name, b'wrong')
                    self.assertEqual(canary.read_bytes(), b'neighbor')
        self.assertEqual(canary.stat().st_nlink, 2)

    def test_registered_log_identity_replacement_poisons_without_taking_new_inode(self) -> None:
        context = self.make()
        log = context.append_log('stdout.log', b'old')
        retained = context.path / 'retained'
        log.rename(retained)
        log.write_bytes(b'replacement')

        with self.assertRaises(RuntimeError):
            context.append_log('stdout.log', b'wrong')

        self.assert_private_file(log, b'replacement')
        self.assert_private_file(retained, b'old')
        self.assert_poisoned(context)

    def test_registered_log_external_size_drift_poisons_without_growth(self) -> None:
        context = self.make()
        log = context.append_log('stdout.log', b'old')
        with log.open('ab') as sink:
            sink.write(b'external')

        with self.assertRaises(RuntimeError):
            context.append_log('stdout.log', b'wrong')

        self.assert_private_file(log, b'oldexternal')
        self.assert_poisoned(context)

    def test_registered_log_gaining_hardlink_poisons_without_growth(self) -> None:
        context = self.make()
        log = context.append_log('stdout.log', b'old')
        alias = self.root / 'alias'
        os.link(log, alias)

        with self.assertRaises(RuntimeError):
            context.append_log('stdout.log', b'wrong')

        self.assertEqual(log.read_bytes(), b'old')
        self.assertEqual(alias.read_bytes(), b'old')
        self.assert_poisoned(context)

    def test_root_replacement_cannot_redirect_context(self) -> None:
        context = self.make()
        moved = self.root.with_name(self.root.name + '-moved')
        self.root.rename(moved)
        self.addCleanup(shutil.rmtree, moved)
        self.root.mkdir(mode=0o700)

        with self.assertRaises(RuntimeError):
            context.write_artifact('wrong', b'x')

        self.assertEqual(list(self.root.iterdir()), [])
        self.assertFalse((moved / context.path.name / 'wrong').exists())
        self.assert_poisoned(context)

    def test_namespace_alias_replacement_cannot_take_owned_neighbor(self) -> None:
        context = self.make()
        neighbor = self.root / 'neighbor'
        neighbor.mkdir(mode=0o700)
        canary = neighbor / 'canary'
        canary.write_bytes(b'preserved')
        saved = self.root / 'saved-namespace'
        context.path.rename(saved)
        context.path.symlink_to(neighbor, target_is_directory=True)

        with self.assertRaises(RuntimeError):
            context.append_log('wrong.log', b'x')

        self.assertEqual(list(neighbor.iterdir()), [canary])
        self.assert_private_file(canary, b'preserved')
        self.assertEqual(list(saved.iterdir()), [])
        self.assert_poisoned(context)

    def test_budget_time_root_drift_is_rechecked_before_creation(self) -> None:
        context = self.make()
        moved = self.root.with_name(self.root.name + '-moved')
        self.addCleanup(shutil.rmtree, moved)

        def drift(path: Path, size: int, ticket: int) -> None:
            self.root.rename(moved)
            self.root.mkdir(mode=0o700)

        self.budget.before = drift
        with self.assertRaises(RuntimeError):
            context.write_artifact('wrong', b'x')
        self.budget.before = None

        self.assertEqual(self.budget.calls, [(context.path / 'wrong', 1, 17)])
        self.assertEqual(list(self.root.iterdir()), [])
        self.assertFalse((moved / context.path.name / 'wrong').exists())
        self.assert_poisoned(context)

    def test_short_native_writes_retain_partial_and_full_debit_without_retry(self) -> None:
        real_write = os.write
        for count in (0, 1):
            with self.subTest(count=count):
                context = self.make()
                attempts = []

                def short(fd: int, data: bytes, count: int = count,
                          attempts: list[bytes] = attempts) -> int:
                    attempts.append(bytes(data))
                    return real_write(fd, data[:count])

                with patch('os.write', side_effect=short), self.assertRaises(OSError) as caught:
                    context.write_artifact('partial', b'abc')

                self.assertEqual(caught.exception.errno, errno.EIO)
                self.assertEqual(attempts, [b'abc'])
                self.assert_private_file(context.path / 'partial', b'abc'[:count])
                self.assertEqual(self.budget.calls[-1], (context.path / 'partial', 3, 17))
                self.assert_poisoned(context)
                context.close()
                self.assert_fd_baseline()
        self.assertEqual([size for _, size, _ in self.budget.calls], [3, 3])

    def test_short_append_retains_prefix_and_poisons_without_refund(self) -> None:
        context = self.make()
        log = context.append_log('stdout.log', b'old')
        real_write = os.write
        attempts = []

        def short(fd: int, data: bytes) -> int:
            attempts.append(bytes(data))
            return real_write(fd, data[:1])

        with patch('os.write', side_effect=short), self.assertRaises(OSError) as caught:
            context.append_log('stdout.log', b'new')

        self.assertEqual(caught.exception.errno, errno.EIO)
        self.assertEqual(attempts, [b'new'])
        self.assert_private_file(log, b'oldn')
        self.assertEqual(self.budget.calls, [(log, 3, 17), (log, 3, 17)])
        self.assert_poisoned(context)
        context.close()
        self.assert_fd_baseline()

    def test_native_write_exception_propagates_once_with_full_debit(self) -> None:
        context = self.make()
        error = OSError(errno.ENOSPC, 'synthetic native write failure')
        with (
            patch('os.write', side_effect=error) as write,
            self.assertRaises(OSError) as caught,
        ):
            context.write_artifact('failed', b'abc')

        self.assertIs(caught.exception, error)
        self.assertEqual(write.call_count, 1)
        self.assert_private_file(context.path / 'failed', b'')
        self.assertEqual(self.budget.calls, [(context.path / 'failed', 3, 17)])
        self.assert_poisoned(context)
        context.close()
        self.assert_fd_baseline()

    def test_real_native_open_error_poisons_without_leaking_operation_handles(self) -> None:
        context = self.make()
        name = 'x' * 4096

        with self.assertRaises(OSError) as caught:
            context.write_artifact(name, b'abc')

        self.assertEqual(caught.exception.errno, errno.ENAMETOOLONG)
        self.assertEqual(list(context.path.iterdir()), [])
        self.assertEqual(self.budget.calls, [(context.path / name, 3, 17)])
        self.assert_poisoned(context)
        context.close()
        self.assert_fd_baseline()

    def test_acquisition_and_completion_fstat_failures_release_operation_handles(self) -> None:
        for stage in ('acquisition', 'completion'):
            with self.subTest(stage=stage):
                context = self.make()
                error = OSError(errno.EIO, stage)
                triggered = []

                def fail(fd: int, stage: str = stage, error: OSError = error,
                         triggered: list[int] = triggered) -> os.stat_result:
                    info = _FSTAT(fd)
                    if stat.S_ISREG(info.st_mode) and not triggered:
                        ready = stage == 'acquisition' or info.st_size > 0
                        if ready:
                            triggered.append(fd)
                            raise error
                    return info

                with patch('os.fstat', side_effect=fail), self.assertRaises(OSError) as caught:
                    context.write_artifact('failed', b'abc')

                self.assertIs(caught.exception, error)
                self.assertEqual(len(triggered), 1)
                expected = b'' if stage == 'acquisition' else b'abc'
                self.assert_private_file(context.path / 'failed', expected)
                self.assertEqual(self.budget.calls[-1], (context.path / 'failed', 3, 17))
                self.assertEqual([target for target in _fds().values()
                                  if target == str(context.path / 'failed')], [])
                self.assert_poisoned(context)
                context.close()
                self.assert_fd_baseline()
        self.assertEqual([size for _, size, _ in self.budget.calls], [3, 3])

    def test_overlapping_contexts_keep_tickets_handles_and_bytes_independent(self) -> None:
        left = self.make(ticket=17)
        right_budget = _Budget()
        right = self.make(ticket=23, budget=right_budget)
        left_log = left.append_log('stdout.log', b'left')
        right_log = right.append_log('stdout.log', b'right')

        left.close()
        right.append_log('stdout.log', b' alive')

        self.assert_private_file(left_log, b'left')
        self.assert_private_file(right_log, b'right alive')
        self.assertEqual(self.budget.calls, [(left_log, 4, 17)])
        self.assertEqual(right_budget.calls, [(right_log, 5, 23), (right_log, 6, 23)])
        self.assert_poisoned(left)
        right.close()
        self.assert_fd_baseline()

    def test_successful_close_is_idempotent_and_never_deletes_evidence(self) -> None:
        context = self.make()
        target = context.write_artifact('report', b'retained')
        context.close()
        real_close = os.close
        with patch('os.close', wraps=real_close) as close:
            context.close()

        self.assertEqual(close.call_count, 0)
        self.assert_private_file(target, b'retained')
        self.assert_poisoned(context)
        self.assert_fd_baseline()

    def test_body_exception_is_same_object_after_successful_cleanup(self) -> None:
        context = self.make()
        body = LookupError('body')
        with self.assertRaises(LookupError) as caught, context:
            context.append_log('stderr.log', b'retained')
            raise body

        self.assertIs(caught.exception, body)
        self.assertIsNone(body.__cause__)
        self.assert_fd_baseline()
        self.assert_private_file(context.path / 'stderr.log', b'retained')

    def test_soft64_one_ticket_256_actual_files_and_fixed_logs_has_bounded_fds(self) -> None:
        baseline = _fds()
        soft, hard = resource.getrlimit(resource.RLIMIT_NOFILE)
        self.assertEqual(soft, 64)
        highwater = [len(baseline)]
        context = self.make(ticket=71)
        real_open, real_close, real_write = os.open, os.close, os.write

        def quiescent() -> None:
            snapshot = _fds()
            self.assertEqual({fd: snapshot[fd] for fd in baseline}, baseline)
            retained = {fd: target for fd, target in snapshot.items() if fd not in baseline}
            self.assertLessEqual(len(retained), 1)
            self.assertTrue(all(target == str(context.path) for target in retained.values()))

        def observe() -> None:
            snapshot = _fds()
            highwater[0] = max(highwater[0], len(snapshot))
            directory = [fd for fd, target in snapshot.items() if target == str(context.path)]
            operation = [fd for fd, target in snapshot.items()
                         if target.startswith(str(context.path) + '/')]
            self.assertLessEqual(len(directory), 1)
            self.assertLessEqual(len(operation), 1)
            simultaneous = len(directory) + len(operation)
            self.assertLessEqual(len(snapshot), len(baseline) + simultaneous + 8)
            self.assertLessEqual(len(snapshot), 56)

        def opening(path: str | bytes | os.PathLike[str] | os.PathLike[bytes],
                    flags: int, mode: int = 0o777, *, dir_fd: int | None = None) -> int:
            fd = real_open(path, flags, mode, dir_fd=dir_fd)
            try:
                observe()
            except AssertionError:
                real_close(fd)
                raise
            return fd

        def closing(fd: int) -> None:
            observe()
            real_close(fd)
            observe()

        def writing(fd: int, data: bytes) -> int:
            observe()
            return real_write(fd, data)

        def stating(fd: int) -> os.stat_result:
            observe()
            return _FSTAT(fd)

        quiescent()
        with (
            patch('os.open', side_effect=opening),
            patch('os.close', side_effect=closing),
            patch('os.write', side_effect=writing),
            patch('os.fstat', side_effect=stating),
        ):
            for index in range(256):
                payload = f'payload-{index}'.encode()
                path = context.write_artifact(f'artifact-{index:03d}', payload)
                self.assert_private_file(path, payload)
                observe()
                quiescent()
                self.assertEqual([target for target in _fds().values()
                                  if target.startswith(str(context.path) + '/')], [])
            for label in ('stdout.log', 'stderr.log', 'report.log'):
                context.append_log(label, b'first')
                context.append_log(label, b' second')
                self.assert_private_file(context.path / label, b'first second')
                quiescent()
                self.assertEqual([target for target in _fds().values()
                                  if target.startswith(str(context.path) + '/')], [])
            context.close()

        self.assertEqual({path.name for path in context.path.iterdir()},
                         {f'artifact-{index:03d}' for index in range(256)} |
                         {'stdout.log', 'stderr.log', 'report.log'})
        self.assertEqual(len(self.budget.calls), 262)
        self.assertEqual({ticket for _, _, ticket in self.budget.calls}, {71})
        self.assertEqual(resource.getrlimit(resource.RLIMIT_NOFILE), (64, hard))
        self.assertEqual(_fds(), baseline)
        sys.stdout.write(json.dumps({'native_soft64': {'baseline': len(baseline),
                         'highwater': highwater[0], 'final': len(_fds()),
                         'soft': soft, 'hard': hard, 'files': 256, 'log_operations': 6}}) + '\n')


class _UncertainCloseCases(unittest.TestCase):
    def exercise_close(self, body_kind: str) -> None:
        with tempfile.TemporaryDirectory(dir=_scratch(), prefix='unresolved-') as directory:
            root = Path(directory)
            baseline = _fds()
            budget = _Budget()
            context = run_context.RunContext(root, budget=budget, ticket=17,
                                             deadline=10.0, clock=lambda: 1.0)
            error = OSError(errno.EIO, 'synthetic unresolved operation close')
            previous = KeyError('previous body cause')
            close_previous = ValueError('previous close cause')
            error.__cause__ = close_previous
            body = error if body_kind == 'same' else LookupError('body')
            if body_kind == 'chain':
                body.__cause__ = previous
            attempts: list[int] = []
            guards: list[tuple[int, int]] = []
            real_close, real_open = os.close, os.open
            target = context.path / 'report'
            guard_path = root / 'reuse-guard'

            def uncertain(fd: int) -> None:
                if fd in attempts:
                    attempts.append(fd)
                    raise AssertionError('forbidden uncertain descriptor retry')
                if os.readlink(f'/proc/self/fd/{fd}') == str(target):
                    attempts.append(fd)
                    if body_kind != 'none':
                        real_close(fd)
                        guard = real_open(guard_path, os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o600)
                        guards.append((guard, _FSTAT(guard).st_ino))
                        if guard != fd:
                            self.assertNotIn(fd, _fds(), 'reuse target must still be known closed')
                            rebound = os.dup2(guard, fd, inheritable=False)
                            real_close(guard)
                            guard = rebound
                            guards[-1] = (guard, _FSTAT(guard).st_ino)
                        self.assertEqual(guard, fd)
                        os.write(guard, b'guard')
                    raise error
                real_close(fd)

            try:
                with patch('os.close', side_effect=uncertain):
                    if body_kind == 'none':
                        with self.assertRaises(OSError) as operation:
                            context.write_artifact('report', b'retained')
                        self.assertIs(operation.exception, error)
                        with self.assertRaises(OSError) as caught:
                            context.close()
                        self.assertIs(caught.exception, error)
                    else:
                        with self.assertRaises(type(body)) as caught, context:
                            with self.assertRaises(OSError) as operation:
                                context.write_artifact('report', b'retained')
                            self.assertIs(operation.exception, error)
                            raise body
                        self.assertIs(caught.exception, body)
                        if body_kind == 'same':
                            self.assertIsNot(body.__cause__, body)
                        else:
                            self.assertIs(body.__cause__, error)
                        self.assertIn(close_previous, _cause_chain(body))
                        if body_kind == 'chain':
                            self.assertIn(previous, _cause_chain(body))
                    with self.assertRaises(OSError) as repeated:
                        context.close()
                    self.assertIs(repeated.exception, error)
                    with self.assertRaises(OSError) as repeated_exit:
                        context.__exit__(None, None, None)
                    self.assertIs(repeated_exit.exception, error)
                    self.assertEqual(len(attempts), 1)
                    with self.assertRaises(RuntimeError):
                        context.write_artifact('wrong', b'x')
                    with self.assertRaises(RuntimeError):
                        context.append_log('wrong.log', b'x')
                    self.assertEqual(budget.calls, [(target, 8, 17)])
                    self.assertEqual(target.read_bytes(), b'retained')
                    snapshot = _fds()
                    extras = {fd: path for fd, path in snapshot.items() if fd not in baseline}
                    expected = target if body_kind == 'none' else guard_path
                    self.assertEqual(extras, {attempts[0]: str(expected)})
                    for guard, inode in guards:
                        self.assertEqual(_FSTAT(guard).st_ino, inode)
                        self.assertEqual(guard_path.read_bytes(), b'guard')
                    sys.stdout.write(json.dumps({'unresolved_close': body_kind,
                                     'baseline': len(baseline), 'actual_after': len(snapshot),
                                     'actual_extra_fds': extras,
                                     'native_close_released': body_kind != 'none',
                                     'library_restoration_claim': False,
                                     'cleanup': 'known reuse-guard close; unresolved kernel exit; parent wait'}) + '\n')
            finally:
                for guard, inode in guards:
                    self.assertEqual(_FSTAT(guard).st_ino, inode)
                    real_close(guard)

    def test_close_error_remains_unresolved_without_numeric_retry(self) -> None:
        self.exercise_close('none')

    def test_distinct_body_is_primary_with_close_as_explicit_cause(self) -> None:
        self.exercise_close('distinct')

    def test_existing_body_cause_chain_survives_cleanup_error(self) -> None:
        self.exercise_close('chain')

    def test_same_body_and_close_error_never_creates_self_cause(self) -> None:
        self.exercise_close('same')


def _cause_chain(error: BaseException) -> list[BaseException]:
    chain = []
    current = error
    while current.__cause__ is not None:
        current = current.__cause__
        if current in chain:
            raise AssertionError('cyclic explicit cause chain')
        chain.append(current)
    return chain


class SerialChildTests(unittest.TestCase):
    def child(self, group: str) -> None:
        root = _scratch()
        with tempfile.TemporaryDirectory(dir=root, prefix='child-control-') as control:
            output = Path(control) / 'stdout.log'
            errors = Path(control) / 'stderr.log'
            command = [sys.executable, '-B', str(Path(__file__).resolve()), '--child', group]
            started = time.monotonic()
            timed_out = False
            with output.open('xb') as stdout, errors.open('xb') as stderr:
                child = subprocess.Popen(command, stdout=stdout, stderr=stderr)
                try:
                    status = child.wait(timeout=45)
                except subprocess.TimeoutExpired:
                    timed_out = True
                    child.kill()
                    status = child.wait(timeout=5)
            raw_out = output.read_bytes()
            raw_err = errors.read_bytes()
            receipt = {'group': group, 'command': command, 'pid': child.pid,
                       'exit': status, 'timeout': timed_out, 'consumed_wait': True,
                       'seconds': time.monotonic() - started,
                       'stdout_bytes': len(raw_out), 'stderr_bytes': len(raw_err)}
            retained = Path(tempfile.mkdtemp(dir=root, prefix='receipt-'))
            (retained / 'stdout.log').write_bytes(raw_out)
            (retained / 'stderr.log').write_bytes(raw_err)
            (retained / 'wait.json').write_text(json.dumps(receipt) + '\n', encoding='utf-8')
            sys.stdout.write(raw_out.decode('utf-8', errors='replace'))
            sys.stderr.write(raw_err.decode('utf-8', errors='replace'))
            sys.stdout.write(json.dumps(receipt) + '\n')
            self.assertLessEqual(len(raw_out) + len(raw_err), 4 * 1024**2)
            self.assertFalse(timed_out, 'child timeout is not completion')
            self.assertEqual(status, 0, f'child failure; retained raw output: {retained}')

    def test_native_filesystem_budget_and_soft64_in_serial_child(self) -> None:
        self.child('normal')

    def test_uncertain_close_in_separate_serial_child(self) -> None:
        self.child('unresolved')


def load_tests(loader: unittest.TestLoader, tests: unittest.TestSuite,
               pattern: str | None) -> unittest.TestSuite:
    return loader.loadTestsFromTestCase(SerialChildTests)


def main() -> int:
    if len(sys.argv) == 3 and sys.argv[1] == '--child':
        _scratch()
        soft, hard = resource.getrlimit(resource.RLIMIT_NOFILE)
        if soft < 64:
            raise RuntimeError('inherited softNOFILE below64; raising forbidden')
        resource.setrlimit(resource.RLIMIT_NOFILE, (64, hard))
        file_soft, file_hard = resource.getrlimit(resource.RLIMIT_FSIZE)
        resource.setrlimit(resource.RLIMIT_FSIZE, (min(2 * 1024**2, file_soft)
                           if file_soft != resource.RLIM_INFINITY else 2 * 1024**2, file_hard))
        case = {'normal': _RunContextCases, 'unresolved': _UncertainCloseCases}[sys.argv[2]]
        sys.stdout.write(json.dumps({'child': os.getpid(), 'nofile_before': [soft, hard],
                         'nofile_actual': list(resource.getrlimit(resource.RLIMIT_NOFILE)),
                         'free_bytes': shutil.disk_usage(_scratch()).free}) + '\n')
        suite = unittest.defaultTestLoader.loadTestsFromTestCase(case)
        result = unittest.TextTestRunner(verbosity=2).run(suite)
        return 0 if result.wasSuccessful() else 1
    unittest.main()
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
