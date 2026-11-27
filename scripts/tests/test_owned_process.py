"""Tests for scripts/lib/owned_process.py (ADR-007).

Two groups. FakeAdapterTests replace the POSIX adapter with one that logs every wait and
every signal and can model events no test could safely force on a real kernel (a process
number reused by a new spawn, a child that exits between a poll and a signal, ECHILD): a
signal sent to the wrong target is an assertion failure and nothing real is ever
signalled. RealChildTests start real children and check the real spawn, collection and
shutdown. Standard library only.

Run:  python3 -I -m unittest discover -s scripts/tests -p 'test_owned_process.py' -v
"""
import copy
import errno
import json
import os
import signal
import subprocess
import sys
import tempfile
import threading
import time
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib"))
import owned_process as op  # noqa: E402


class FakeClock:
    def __init__(self):
        self.t = 0.0
        self.on_tick = []

    def now(self):
        return self.t

    def sleep(self, d):
        self.t += d
        for cb in list(self.on_tick):
            cb(self.t)


class FakeAdapter:
    """Logs every call. 'serial' identifies which spawn currently holds a process number, so a
    signal that reaches a replacement child is visible as a kill whose serial is not the target's."""

    def __init__(self):
        self.log = []
        self.procs = {}
        self.forced_pids = []
        self._next_pid = 5000
        self.serial = 0
        self.spawned_ever = set()
        self.term_behaviour = {}      # pid -> ("exit", code) | "ignore"
        self.unkillable = set()
        self.wait_error = {}
        self.kill_error = {}
        self.exit_after_next_wait = {}

    def spawn(self, argv, log_fd, env):
        self.serial += 1
        pid = self.forced_pids.pop(0) if self.forced_pids else self._fresh()
        held = self.procs.get(pid)
        assert not (held and not held["collected"]), "the fake handed out a number that is still held"
        self.procs[pid] = {"serial": self.serial, "status": None, "collected": False}
        self.spawned_ever.add(pid)
        self.log.append(("spawn", self.serial, pid))
        return pid

    def _fresh(self):
        self._next_pid += 1
        return self._next_pid

    def waitpid_nohang(self, pid):
        self.log.append(("wait", pid))
        assert pid > 0 and pid in self.spawned_ever, "wait on a number this adapter never spawned (an unrelated wait)"
        if pid in self.wait_error:
            raise self.wait_error[pid]
        p = self.procs.get(pid)
        if p is None or p["collected"]:
            raise ChildProcessError(errno.ECHILD, "No child processes")
        result = (False, 0)
        if p["status"] is not None:
            p["collected"] = True
            result = (True, p["status"])
        if pid in self.exit_after_next_wait:   # the child exits right after this poll returned "running"
            p["status"] = self.exit_after_next_wait.pop(pid) << 8
        return result

    def kill(self, pid, sig):
        p = self.procs.get(pid)
        self.log.append(("kill", pid, sig, p["serial"] if p else None, p["collected"] if p else None))
        if pid in self.kill_error:
            raise self.kill_error[pid]
        if p is None or p["collected"]:
            return   # a signal to a collected number would hit whoever holds it: the tests assert none is sent
        if sig == signal.SIGKILL and pid not in self.unkillable:
            p["status"] = int(signal.SIGKILL)
        elif sig == signal.SIGTERM:
            b = self.term_behaviour.get(pid, ("exit", 0))
            if b != "ignore" and p["status"] is None:
                p["status"] = b[1] << 8

    def exit_child(self, pid, code=0):
        self.procs[pid]["status"] = code << 8

    def kills(self):
        return [e for e in self.log if e[0] == "kill"]

    def waits(self):
        return [e for e in self.log if e[0] == "wait"]


class OwnerTestCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory(prefix="owned-process-unit.")
        self.dir = self._tmp.name
        self.addCleanup(self._tmp.cleanup)
        self._saved_sigchld = signal.getsignal(signal.SIGCHLD)
        self.addCleanup(signal.signal, signal.SIGCHLD, self._saved_sigchld)
        self._saved = {s: signal.getsignal(s) for s in op.INTERRUPT_SIGNALS}
        self.addCleanup(lambda: [signal.signal(s, h) for s, h in self._saved.items()])

    def log_path(self, name="child"):
        n = getattr(self, "_log_n", 0) + 1
        self._log_n = n
        return os.path.join(self.dir, "%s-%d.log" % (name, n))   # always a fresh path: logs are created exclusively

    def track(self, owner, real=False):
        """Register bounded cleanup for an owner BEFORE it spawns anything, so a failing assertion or an
        ordinary exception cannot leave an owned child running (r3 finding 3). Cleanups run last-in
        first-out; the shutdown is the bounded TERM/KILL sequence, never an unbounded wait."""
        self.addCleanup(owner.close)
        if real:
            self.addCleanup(lambda: owner.shutdown(2.0, 3.0))
        return owner


class FakeAdapterTests(OwnerTestCase):
    def make(self):
        self.adapter = FakeAdapter()
        self.clock = FakeClock()
        self.owner = self.track(op.Owner(self.adapter, self.clock.now, self.clock.sleep))
        return self.owner

    def pid_of(self, child_index=0):
        spawns = [e for e in self.adapter.log if e[0] == "spawn"]
        return spawns[child_index][2]

    # --- identity ---------------------------------------------------------------------
    def test_retained_identity_before_reap_exited_child_is_collected_not_signalled(self):
        owner = self.make()
        a = owner.spawn(["x"], self.log_path("a"))
        self.adapter.exit_child(self.pid_of(0), 0)            # exited, still a zombie nobody has collected
        self.assertFalse(owner.request_stop(a))
        self.assertEqual(a.state, op.State.REAPED)
        self.assertEqual(self.adapter.kills(), [], "an exited child was signalled instead of collected")

    def test_exit_between_poll_and_signal_still_targets_the_retained_child(self):
        owner = self.make()
        a = owner.spawn(["x"], self.log_path("a"))
        pid = self.pid_of(0)
        self.adapter.exit_after_next_wait[pid] = 0           # the poll before the signal says "running", then it exits
        self.assertTrue(owner.request_stop(a))
        kills = self.adapter.kills()
        self.assertEqual(len(kills), 1)
        self.assertEqual(kills[0][3], 1, "the signal must reach the serial-1 child")
        self.assertIs(kills[0][4], False, "the child was still uncollected when signalled (a zombie keeps its number)")
        self.assertEqual(owner.poll(a), 0)                   # the sole waiter collects it afterwards
        self.assertEqual(a.state, op.State.REAPED)
        owner.request_stop(a)
        self.assertEqual(len(self.adapter.kills()), 1, "a signal was sent after REAPED")

    def test_same_parent_same_second_replacement_is_not_signalled(self):
        """The model from review r2 finding 1: the registered child is collected, a NEW child of the same
        owner gets the same number. Stopping the old handle must not touch the new child."""
        owner = self.make()
        a = owner.spawn(["old"], self.log_path("a"))
        pid = self.pid_of(0)
        self.adapter.exit_child(pid, 0)
        self.assertEqual(owner.poll(a), 0)                   # collected: a is terminal
        self.adapter.forced_pids.append(pid)                 # the next spawn receives the SAME number
        b = owner.spawn(["new"], self.log_path("b"))
        self.assertEqual(self.pid_of(1), pid)
        waits_before = len(self.adapter.waits())
        self.assertFalse(owner.request_stop(a))              # the old handle
        self.assertEqual(self.adapter.kills(), [], "the old handle signalled the replacement")
        self.assertEqual(len(self.adapter.waits()), waits_before, "a terminal handle was waited on again")
        self.assertEqual(b.state, op.State.OWNED)
        self.assertTrue(owner.request_stop(b))               # only the new handle reaches the new child
        kills = self.adapter.kills()
        self.assertEqual(len(kills), 1)
        self.assertEqual(kills[0][3], 2)

    def test_old_pid_reused_by_a_new_spawn_after_shutdown(self):
        owner = self.make()
        a = owner.spawn(["old"], self.log_path("a"))
        pid = self.pid_of(0)
        report = owner.shutdown(1, 1)
        self.assertTrue(report.ok)
        self.adapter.forced_pids.append(pid)
        b = owner.spawn(["new"], self.log_path("b"))
        kills_before = len(self.adapter.kills())
        owner.shutdown(1, 1, children=[a])                  # shutting the old child down again
        self.assertEqual(len(self.adapter.kills()), kills_before, "the finished handle caused a signal")
        self.assertEqual(b.state, op.State.OWNED)

    def test_repeated_stop_is_idempotent_and_resets_no_deadline(self):
        owner = self.make()
        a = owner.spawn(["x"], self.log_path("a"))
        self.adapter.term_behaviour[self.pid_of(0)] = "ignore"
        self.assertTrue(owner.request_stop(a))
        self.assertFalse(owner.request_stop(a))
        self.assertEqual(len([k for k in self.adapter.kills() if k[2] == signal.SIGTERM]), 1)
        t0 = self.clock.t
        owner.shutdown(1.0, 1.0)
        first = self.clock.t - t0
        self.assertLessEqual(first, 2.0 + op.POLL_STEP_SECONDS)
        kills = len(self.adapter.kills())
        t1 = self.clock.t
        owner.shutdown(100.0, 100.0)                         # a second, re-entrant cleanup: nothing resets
        self.assertEqual(len(self.adapter.kills()), kills)
        self.assertEqual(self.clock.t, t1, "a repeated shutdown waited again")

    def test_forged_foreign_and_non_handle_objects_send_no_signal(self):
        owner = self.make()
        other = self.track(op.Owner(self.adapter, self.clock.now, self.clock.sleep))
        mine = owner.spawn(["mine"], self.log_path("mine"))
        theirs = other.spawn(["theirs"], self.log_path("theirs"))
        for bad in (theirs, 5001, "child", None, object()):
            with self.assertRaises(op.OwnershipError, msg=repr(bad)):
                owner.request_stop(bad)
        with self.assertRaises(op.OwnershipError):
            op.OwnedChild(object(), owner, "forged", ["x"], "/dev/null", self.pid_of(0))
        clone = copy.copy(mine)                              # a copy is not the registered object
        with self.assertRaises(op.OwnershipError):
            owner.request_stop(clone)

        class Impostor(op.OwnedChild):
            pass
        with self.assertRaises(op.OwnershipError):
            Impostor(object(), owner, "x", ["x"], "/dev/null", 1)
        self.assertEqual(self.adapter.kills(), [], "a refused handle produced a signal")
        with self.assertRaises(op.OwnershipError):
            owner.poll(theirs)

    def test_a_completed_handle_is_never_signalled(self):
        owner = self.make()
        a = owner.spawn(["x"], self.log_path("a"))
        self.adapter.exit_child(self.pid_of(0), 0)
        owner.poll(a)
        self.assertFalse(owner.request_stop(a))
        owner.shutdown(1, 1)
        self.assertEqual(self.adapter.kills(), [])

    def test_echild_makes_the_child_lost_and_it_is_never_signalled(self):
        owner = self.make()
        a = owner.spawn(["x"], self.log_path("a"))
        self.adapter.wait_error[self.pid_of(0)] = ChildProcessError(errno.ECHILD, "No child processes")
        self.assertIsNone(owner.poll(a))
        self.assertEqual(a.state, op.State.LOST)
        self.assertFalse(owner.request_stop(a))
        report = owner.shutdown(1, 1)
        self.assertEqual(self.adapter.kills(), [], "a lost child was signalled")
        self.assertFalse(report.ok)
        self.assertIn("lost ownership", report.failures[0])
        waits = len(self.adapter.waits())
        owner.poll(a)
        self.assertEqual(len(self.adapter.waits()), waits, "a lost child was waited on again")

    def test_wait_never_targets_an_unrelated_or_negative_pid(self):
        owner = self.make()
        kids = [owner.spawn(["k%d" % i], self.log_path("k%d" % i)) for i in range(3)]
        self.adapter.term_behaviour[self.pid_of(1)] = "ignore"
        owner.shutdown(0.5, 0.5)
        ours = {e[2] for e in self.adapter.log if e[0] == "spawn"}
        for e in self.adapter.waits():
            self.assertIn(e[1], ours)
            self.assertGreater(e[1], 0)
        self.assertEqual(len(kids), 3)

    def test_a_failing_signal_call_loses_the_child_instead_of_claiming_success(self):
        for exc in (ProcessLookupError(errno.ESRCH, "No such process"), PermissionError(errno.EPERM, "Operation not permitted")):
            owner = self.make()
            a = owner.spawn(["x"], self.log_path("a"))
            self.adapter.kill_error[self.pid_of(0)] = exc
            self.assertFalse(owner.request_stop(a))
            self.assertEqual(a.state, op.State.LOST)
            report = owner.shutdown(0.2, 0.2)
            self.assertFalse(report.ok)

    # --- bounded shutdown -------------------------------------------------------------------
    def test_term_goes_to_everyone_first_then_kill_only_to_the_survivors_with_shared_deadlines(self):
        owner = self.make()
        a = owner.spawn(["a"], self.log_path("a"))
        b = owner.spawn(["b"], self.log_path("b"))
        self.adapter.term_behaviour[self.pid_of(1)] = "ignore"
        start = self.clock.t
        report = owner.shutdown(2.0, 3.0)
        kills = self.adapter.kills()
        self.assertEqual([k[2] for k in kills[:2]], [signal.SIGTERM, signal.SIGTERM], "TERM must go to every child first")
        self.assertEqual([k[2] for k in kills[2:]], [signal.SIGKILL])
        self.assertEqual(kills[2][3], 2, "KILL must go only to the child that survived TERM")
        self.assertGreaterEqual(self.clock.t - start, 2.0, "KILL was sent before the graceful interval elapsed")
        self.assertLessEqual(self.clock.t - start, 2.0 + 3.0 + op.POLL_STEP_SECONDS)
        self.assertEqual(a.state, op.State.REAPED)
        self.assertFalse(a.escalated)
        self.assertTrue(b.escalated)
        self.assertFalse(report.ok)
        self.assertIn("ignored SIGTERM", report.failures[0])

    def test_a_child_that_cannot_be_collected_after_kill_fails_the_run_without_more_signals(self):
        owner = self.make()
        a = owner.spawn(["a"], self.log_path("a"))
        pid = self.pid_of(0)
        self.adapter.term_behaviour[pid] = "ignore"
        self.adapter.unkillable.add(pid)
        start = self.clock.t
        report = owner.shutdown(1.0, 1.0)
        self.assertLessEqual(self.clock.t - start, 2.0 + op.POLL_STEP_SECONDS, "shutdown exceeded its deadlines")
        self.assertEqual(a.state, op.State.KILL_SENT)
        self.assertFalse(report.ok)
        self.assertIn("cleanup is incomplete", report.failures[0])
        self.assertEqual([k[2] for k in self.adapter.kills()], [signal.SIGTERM, signal.SIGKILL])

    def test_all_children_are_collected_and_the_first_failure_is_preserved(self):
        owner = self.make()
        a = owner.spawn(["a"], self.log_path("a"))
        b = owner.spawn(["b"], self.log_path("b"))
        c = owner.spawn(["c"], self.log_path("c"))
        self.adapter.term_behaviour[self.pid_of(0)] = ("exit", 3)
        self.adapter.term_behaviour[self.pid_of(1)] = ("exit", 4)
        report = owner.shutdown(1, 1)
        self.assertEqual([r.state for r in report.children], [op.State.REAPED] * 3)
        self.assertEqual(len(report.failures), 2)
        self.assertIn("a: exited with status 3", report.failures[0])
        self.assertIn("b: exited with status 4", report.failures[1])
        self.assertEqual(c.exit_code, 0)

    def test_a_clean_shutdown_reports_ok(self):
        owner = self.make()
        owner.spawn(["a"], self.log_path("a"))
        owner.spawn(["b"], self.log_path("b"))
        report = owner.shutdown(1, 1)
        self.assertTrue(report.ok)
        self.assertEqual(report.failures, [])

    # --- r3 findings 4 and 5, and call-order evidence ------------------------------------------
    def test_a_preexisting_fifo_log_is_refused_without_blocking_and_nothing_is_spawned(self):
        owner = self.make()
        fifo = os.path.join(self.dir, "fifo.log")
        os.mkfifo(fifo)
        start = time.monotonic()
        with self.assertRaises(OSError):
            owner.spawn(["x"], fifo)                      # O_EXCL|O_NONBLOCK: it fails instead of waiting for a reader
        self.assertLess(time.monotonic() - start, 2.0, "opening a FIFO blocked")
        self.assertEqual([e for e in self.adapter.log if e[0] == "spawn"], [])

    def test_a_preexisting_regular_file_or_hardlink_log_is_refused(self):
        owner = self.make()
        existing = os.path.join(self.dir, "existing.log")
        open(existing, "w").close()
        link = os.path.join(self.dir, "hard.log")
        os.link(existing, link)
        for path in (existing, link):
            with self.assertRaises(FileExistsError):
                owner.spawn(["x"], path)
        self.assertEqual([e for e in self.adapter.log if e[0] == "spawn"], [])

    def test_a_caller_log_descriptor_must_be_a_regular_append_readwrite_file(self):
        owner = self.make()
        r, w = os.pipe()
        self.addCleanup(os.close, r)
        self.addCleanup(os.close, w)
        with self.assertRaises(op.OwnershipError):          # a pipe is not a regular file
            owner.spawn(["x"], log_fd=w)
        ro = os.open(os.path.join(self.dir, "ro.log"), os.O_CREAT | os.O_RDONLY, 0o600)
        self.addCleanup(os.close, ro)
        with self.assertRaises(op.OwnershipError):
            owner.spawn(["x"], log_fd=ro)
        good = os.open(os.path.join(self.dir, "good.log"), os.O_CREAT | os.O_RDWR | os.O_APPEND, 0o600)
        self.addCleanup(os.close, good)
        child = owner.spawn(["x"], log_fd=good)
        self.assertEqual(child.state, op.State.OWNED)
        with self.assertRaises(op.OwnershipError):          # both or neither
            owner.spawn(["x"], self.log_path("both"), log_fd=good)
        with self.assertRaises(op.OwnershipError):
            owner.spawn(["x"])

    def test_diagnostics_are_read_from_the_retained_descriptor_not_a_reopened_path(self):
        owner = self.make()
        path = self.log_path("tail")
        child = owner.spawn(["x"], path)
        os.write(child._log_fd, b"hello from the child")
        os.unlink(path)                                      # the path is gone; the retained descriptor still reads
        self.assertEqual(owner.log_tail(child), "hello from the child")
        self.assertEqual(owner.log_tail(child, 5), "child")
        owner.close()
        self.assertEqual(owner.log_tail(child), "(log closed)")

    def test_a_registration_failure_after_the_fork_does_not_leak_the_child(self):
        owner = self.make()
        real = op.OwnedChild.__init__

        def broken(*a, **k):
            raise MemoryError("injected: cannot build the handle")
        op.OwnedChild.__init__ = broken
        try:
            with self.assertRaises(MemoryError):
                owner.spawn(["x"], self.log_path("a"))
        finally:
            op.OwnedChild.__init__ = real
        pid = self.pid_of(0)
        self.assertEqual([k[2] for k in self.adapter.kills()], [signal.SIGKILL], "the unregistered child must be killed")
        self.assertEqual(self.adapter.kills()[0][1], pid)
        self.assertTrue(self.adapter.procs[pid]["collected"], "and collected by this owner")
        self.assertEqual(owner.children, ())
        self.assertEqual(set(signal.pthread_sigmask(signal.SIG_BLOCK, [])) & set(op.INTERRUPT_SIGNALS), set(), "the mask was not restored")

    def test_a_failing_spawn_closes_the_log_descriptor_and_registers_nothing(self):
        owner = self.make()
        before = set(os.listdir("/dev/fd")) if os.path.isdir("/dev/fd") else None

        def boom(argv, log_fd, env):
            raise OSError(errno.EAGAIN, "fork failed")
        self.adapter.spawn = boom
        with self.assertRaises(OSError):
            owner.spawn(["x"], self.log_path("a"))
        self.assertEqual(owner.children, ())
        if before is not None:
            self.assertEqual(set(os.listdir("/dev/fd")), before, "a descriptor leaked")

    def test_the_status_is_marked_reaped_before_it_is_decoded(self):
        owner = self.make()
        a = owner.spawn(["x"], self.log_path("a"))
        self.adapter.exit_child(self.pid_of(0), 0)
        real = op.decode_status

        def boom(status):
            raise RuntimeError("injected decoder failure")
        op.decode_status = boom
        try:
            with self.assertRaises(RuntimeError):
                owner.poll(a)
        finally:
            op.decode_status = real
        self.assertEqual(a.state, op.State.REAPED, "REAPED must be set before anything else can fail")
        self.assertFalse(owner.request_stop(a))
        self.assertEqual(self.adapter.kills(), [])

    def test_a_wait_immediately_precedes_every_term_and_every_kill_for_that_child(self):
        owner = self.make()
        owner.spawn(["a"], self.log_path("a"))
        pid = self.pid_of(0)
        self.adapter.term_behaviour[pid] = "ignore"
        owner.shutdown(0.5, 0.5)
        log = [e for e in self.adapter.log if e[0] in ("wait", "kill")]
        kill_positions = [i for i, e in enumerate(log) if e[0] == "kill"]
        self.assertEqual([log[i][2] for i in kill_positions], [signal.SIGTERM, signal.SIGKILL])
        for i in kill_positions:
            self.assertGreater(i, 0)
            self.assertEqual(log[i - 1], ("wait", pid), "the poll of the same child must come right before the signal")

    def test_a_second_stop_request_while_cleanup_is_pending_resets_nothing(self):
        owner = self.make()
        owner.spawn(["a"], self.log_path("a"))
        self.adapter.term_behaviour[self.pid_of(0)] = "ignore"
        self.adapter.unkillable.add(self.pid_of(0))
        owner.shutdown(1.0, 1.0)                              # leaves the child KILL_SENT and uncollectible
        t = self.clock.t
        report = owner.shutdown(50.0, 50.0)                   # re-entered while cleanup is still pending
        self.assertEqual(self.clock.t - t, 0.0, "the second call waited past the original deadlines")
        self.assertFalse(report.ok)
        self.assertEqual(len(self.adapter.kills()), 2, "no extra signal on re-entry")

    def test_an_independent_batch_gets_its_own_deadlines_after_an_earlier_shutdown(self):
        owner = self.make()
        a = owner.spawn(["a"], self.log_path("a"))
        self.adapter.term_behaviour[self.pid_of(0)] = "ignore"
        owner.shutdown(1.0, 1.0, children=[a])                # a is killed; its deadlines are spent
        b = owner.spawn(["b"], self.log_path("b"))
        self.adapter.term_behaviour[self.pid_of(1)] = "ignore"
        start = self.clock.t
        owner.shutdown(2.0, 2.0, children=[b])
        self.assertGreaterEqual(self.clock.t - start, 2.0, "b inherited a's spent graceful deadline and was killed early")
        self.assertEqual([k[2] for k in self.adapter.kills() if k[3] == 2], [signal.SIGTERM, signal.SIGKILL])

    # --- signals and spawn ordering ---------------------------------------------------------------
    def test_interrupt_handlers_only_set_a_flag_and_keep_the_first_signal(self):
        owner = self.make()
        owner.install_signal_handlers()
        self.addCleanup(owner.restore_signal_handlers)
        self.assertIsNone(owner.interrupted)
        os.kill(os.getpid(), signal.SIGTERM)
        os.kill(os.getpid(), signal.SIGHUP)
        self.assertEqual(owner.interrupted, signal.SIGTERM, "the first interrupt must be kept")
        self.assertEqual(owner.interrupt_exit_code, 143)
        self.assertEqual(op.INTERRUPT_EXIT_CODES, {signal.SIGINT: 130, signal.SIGTERM: 143, signal.SIGHUP: 129})
        self.assertEqual(self.adapter.kills(), [], "a handler signalled something")

    def test_interrupts_are_blocked_from_before_the_fork_until_the_child_is_registered(self):
        owner = self.make()
        owner.install_signal_handlers()
        self.addCleanup(owner.restore_signal_handlers)
        seen = {}
        real_spawn = self.adapter.spawn

        def spawn(argv, log_fd, env):
            seen["blocked"] = set(signal.pthread_sigmask(signal.SIG_BLOCK, []))
            os.kill(os.getpid(), signal.SIGTERM)          # an interrupt arrives while the fork is happening
            seen["flag_during"] = owner.interrupted
            return real_spawn(argv, log_fd, env)
        self.adapter.spawn = spawn
        child = owner.spawn(["x"], self.log_path("x"))
        for s in op.INTERRUPT_SIGNALS:
            self.assertIn(s, seen["blocked"])
        self.assertIsNone(seen["flag_during"], "the interrupt was acted on before the child was registered")
        self.assertIn(child, owner.children, "the child was not registered")
        self.assertEqual(owner.interrupted, signal.SIGTERM, "the pending interrupt was not delivered after registration")
        self.assertEqual(set(signal.pthread_sigmask(signal.SIG_BLOCK, [])) & set(op.INTERRUPT_SIGNALS), set(), "the mask was not restored")

    def test_sigchld_is_set_to_its_default_even_if_the_parent_ignored_it(self):
        signal.signal(signal.SIGCHLD, signal.SIG_IGN)
        self.make()
        self.assertEqual(signal.getsignal(signal.SIGCHLD), signal.SIG_DFL)

    def test_the_owner_refuses_to_be_created_off_the_main_thread(self):
        errors = []

        def run():
            try:
                op.Owner(FakeAdapter())
            except op.OwnershipError as exc:
                errors.append(exc)
        t = threading.Thread(target=run)
        t.start()
        t.join()
        self.assertEqual(len(errors), 1)

    def test_bad_argv_and_a_symlinked_log_are_refused_before_anything_is_spawned(self):
        owner = self.make()
        for bad in ([], None, ["x", 5], "echo hi"):
            with self.assertRaises(op.OwnershipError):
                owner.spawn(bad, self.log_path("z"))
        target = os.path.join(self.dir, "target")
        open(target, "w").close()
        link = os.path.join(self.dir, "link.log")
        os.symlink(target, link)
        with self.assertRaises(OSError):
            owner.spawn(["x"], link)
        self.assertEqual([e for e in self.adapter.log if e[0] == "spawn"], [])


PY = [sys.executable, "-I", "-c"]


def child_script(body):
    return PY + ["import os, signal, sys, time, json\n" + body]


class RealChildTests(OwnerTestCase):
    def setUp(self):
        super().setUp()
        self.owner = self.track(op.Owner(), real=True)
        self.bystanders = []
        self.addCleanup(self._reap_bystanders)

    def _reap_bystanders(self):
        for p in self.bystanders:      # the test owns these through Popen: it may kill and wait for them
            if p.poll() is None:
                p.kill()
            p.wait()

    def wait_for_log(self, child, text, timeout=10.0):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            self.owner.poll(child)
            if child.done:
                self.fail("child ended early (%s): %s" % (child.exit_code, self.owner.log_tail(child)))
            if text in self.owner.log_tail(child):
                return
            time.sleep(0.02)
        self.fail("child never printed %r: %s" % (text, self.owner.log_tail(child)))

    def test_a_clean_term_exit_is_reaped_with_status_zero(self):
        child = self.owner.spawn(child_script(
            "signal.signal(signal.SIGTERM, lambda *a: sys.exit(0))\nprint('up', flush=True)\ntime.sleep(60)"), self.log_path())
        self.wait_for_log(child, "up")
        report = self.owner.shutdown(5, 3)
        self.assertTrue(report.ok, report.failures)
        self.assertEqual(child.exit_code, 0)
        self.assertFalse(child.escalated)

    def test_a_nonzero_exit_is_reported_with_its_status(self):
        child = self.owner.spawn(child_script(
            "signal.signal(signal.SIGTERM, lambda *a: sys.exit(3))\nprint('up', flush=True)\ntime.sleep(60)"), self.log_path())
        self.wait_for_log(child, "up")
        report = self.owner.shutdown(5, 3)
        self.assertFalse(report.ok)
        self.assertIn("exited with status 3", report.failures[0])
        self.assertEqual(child.exit_code, 3)

    def test_an_early_exit_status_is_collected_and_nothing_is_signalled(self):
        child = self.owner.spawn(child_script("sys.exit(5)"), self.log_path())
        end = time.monotonic() + 10
        while time.monotonic() < end and not child.done:
            self.owner.poll(child)
            time.sleep(0.02)
        self.assertEqual(child.state, op.State.REAPED)
        self.assertEqual(child.exit_code, 5)
        report = self.owner.shutdown(1, 1)
        self.assertFalse(child.escalated)
        self.assertIn("exited with status 5", report.failures[0])

    def test_a_term_ignoring_child_is_killed_after_the_deadline_and_reaped(self):
        child = self.owner.spawn(child_script(
            "signal.signal(signal.SIGTERM, signal.SIG_IGN)\nprint('up', flush=True)\ntime.sleep(60)"), self.log_path())
        self.wait_for_log(child, "up")
        start = time.monotonic()
        report = self.owner.shutdown(0.3, 5)
        self.assertLess(time.monotonic() - start, 3.0)
        self.assertEqual(child.state, op.State.REAPED)
        self.assertTrue(child.escalated)
        self.assertEqual(child.signal_number, signal.SIGKILL)
        self.assertFalse(report.ok)
        self.assertIn("ignored SIGTERM", report.failures[0])

    def test_an_executable_that_does_not_exist_is_reaped_as_status_127_with_a_diagnostic(self):
        child = self.owner.spawn(["/nonexistent/definitely-not-here"], self.log_path())
        end = time.monotonic() + 10
        while time.monotonic() < end and not child.done:
            self.owner.poll(child)
            time.sleep(0.02)
        self.assertEqual(child.exit_code, op.EXEC_FAILURE_STATUS)
        self.assertIn("cannot start", self.owner.log_tail(child))

    def test_an_unrelated_process_with_the_same_command_line_survives_every_stop(self):
        code = "import time; time.sleep(60)"
        bystander = subprocess.Popen([sys.executable, "-I", "-c", code])
        self.bystanders.append(bystander)
        child = self.owner.spawn([sys.executable, "-I", "-c", code + "  # owned"], self.log_path())
        time.sleep(0.2)
        self.owner.shutdown(0.5, 5)
        self.assertEqual(child.state, op.State.REAPED)
        self.assertIsNone(bystander.poll(), "the unrelated process was signalled")

    def test_the_child_starts_in_a_clean_environment_of_its_own(self):
        before = os.pipe()                                   # a descriptor the child must not inherit
        self.addCleanup(lambda: [os.close(fd) for fd in before])
        for s in op.INTERRUPT_SIGNALS:                       # an IGNORED disposition survives exec, so the child must reset it
            signal.signal(s, signal.SIG_IGN)
        child = self.owner.spawn(child_script(
            "print(json.dumps({'sid': os.getsid(0), 'pid': os.getpid(), 'pgid': os.getpgid(0),"
            " 'int_default': signal.getsignal(signal.SIGINT) is signal.default_int_handler, 'term_default': signal.getsignal(signal.SIGTERM) == signal.SIG_DFL,"
            " 'hup_default': signal.getsignal(signal.SIGHUP) == signal.SIG_DFL, 'chld_default': signal.getsignal(signal.SIGCHLD) == signal.SIG_DFL,"
            " 'mask': sorted(int(s) for s in signal.pthread_sigmask(signal.SIG_BLOCK, [])),"
            " 'stdin': sys.stdin.read(), 'inherited_open': [fd for fd in %r if os.path.exists('/dev/fd/%%d' %% fd)]}), flush=True)"
            % (list(before),)), self.log_path())
        end = time.monotonic() + 10
        while time.monotonic() < end and not child.done:
            self.owner.poll(child)
            time.sleep(0.02)
        self.assertEqual(child.exit_code, 0, self.owner.log_tail(child))
        info = json.loads(self.owner.log_tail(child).strip().splitlines()[-1])
        self.assertEqual(info["sid"], info["pid"], "the child must lead its own session")
        self.assertEqual(info["pgid"], info["pid"])
        # A fresh Python child turns SIG_DFL for SIGINT into its own default_int_handler; had the owner's SIG_IGN been
        # inherited it would still be ignored. The others must read as the default disposition.
        self.assertTrue(info["int_default"], "SIGINT was inherited as ignored")
        for key in ("term_default", "hup_default", "chld_default"):
            self.assertTrue(info[key], key + ": the disposition was not reset")
        self.assertEqual(info["mask"], [], "the owner's signal block leaked into the child")
        self.assertEqual(info["stdin"], "", "stdin must be the null device")
        self.assertEqual(info["inherited_open"], [], "an unrelated descriptor was inherited")

    def test_output_goes_to_an_owned_log_and_the_diagnostic_tail_is_bounded(self):
        child = self.owner.spawn(child_script("sys.stdout.write('x' * 50000); sys.stdout.flush(); sys.stderr.write('E' * 10)"), self.log_path())
        end = time.monotonic() + 10
        while time.monotonic() < end and not child.done:
            self.owner.poll(child)
            time.sleep(0.02)
        self.assertEqual(child.exit_code, 0)
        self.assertLessEqual(len(self.owner.log_tail(child, limit=100)), 100)
        self.assertGreaterEqual(os.fstat(child._log_fd).st_size, 50010)

    def test_a_parent_that_ignored_sigchld_can_still_collect_a_real_child(self):
        signal.signal(signal.SIGCHLD, signal.SIG_IGN)
        owner = self.track(op.Owner(), real=True)
        child = owner.spawn(child_script("sys.exit(7)"), self.log_path("ign"))
        end = time.monotonic() + 10
        while time.monotonic() < end and not child.done:
            owner.poll(child)
            time.sleep(0.02)
        self.assertEqual(child.state, op.State.REAPED)
        self.assertEqual(child.exit_code, 7)

    def test_two_children_are_stopped_together_and_both_statuses_are_collected(self):
        script = "signal.signal(signal.SIGTERM, lambda *a: sys.exit(0))\nprint('up', flush=True)\ntime.sleep(60)"
        a = self.owner.spawn(child_script(script), self.log_path("a"))
        b = self.owner.spawn(child_script(script), self.log_path("b"))
        self.wait_for_log(a, "up")
        self.wait_for_log(b, "up")
        report = self.owner.shutdown(5, 3)
        self.assertTrue(report.ok, report.failures)
        self.assertEqual((a.exit_code, b.exit_code), (0, 0))


if __name__ == "__main__":
    unittest.main()
