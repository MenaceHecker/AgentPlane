"""Integration tests: the smoke runner as a supervisor that the test owns (P0-T5 revised commit 4).

The runner is a DIRECT CHILD of the test through scripts/lib/owned_process.py, so INT, TERM and HUP are
delivered by its sole owner (Owner.interrupt: exact handle, poll right before, never after reaping) and
its exit status is collected by the same owner. No test finds a process by name or PID, and none signals
an orphan. Bounded cleanup for the runner is registered before it starts (smoke_support.SmokeCase).

Phases: before startup (handlers installed, nothing spawned yet), during readiness (a server is up but
never becomes ready), during cleanup (a server ignores TERM, so shutdown is waiting out its deadline).
Documented exit codes: INT 130, TERM 143, HUP 129. A controlled supervisor SIGKILL is exercised too, and
the test asserts the documented LIMITATION, that nothing cleans up after it, rather than claiming a
self-cleanup the design does not provide.

Run:  python3 -I -B -m unittest discover -s scripts/tests -p 'test_owned_process_integration.py' -v
"""
import os
import re
import signal
import socket
import sys
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from smoke_support import SmokeCase, op  # noqa: E402

CODES = {signal.SIGINT: 130, signal.SIGTERM: 143, signal.SIGHUP: 129}


class InterruptTests(SmokeCase):
    def note_file(self, name):
        return os.path.join(self.tmp, name)

    def wait_for_file(self, path, child, timeout=30.0):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            self.owner.poll(child)
            if os.path.exists(path) and os.path.getsize(path) > 0:
                return
            if child.done:
                break
            time.sleep(0.02)
        self.fail("%s never appeared: %s" % (path, self.owner.log_tail(child, 3000)))

    def finish_and_check(self, child, signum, expect_children_reaped=True):
        out = self.wait(child)
        self.assertEqual(child.state, op.State.REAPED, out)
        self.assertEqual(child.exit_code, CODES[signum], out)
        self.assertIn("interrupted by signal %d" % signum, out)
        if expect_children_reaped:
            states = re.search(r"mock-smoke: children: (.*)", out).group(1)
            self.assertNotRegex(states, r"OWNED|TERM_SENT|KILL_SENT|LOST", "an owned model was not reaped: " + states)
        self.assertEqual(self.workdirs_left(), [], "the interrupted run must still remove its own workdir")
        return out

    def test_each_interrupt_before_startup_exits_with_its_code_and_leaves_nothing(self):
        for signum in CODES:
            with self.subTest(signal=signal.Signals(signum).name):
                child = self.start_runner()
                self.wait_for(child, "handlers installed")
                self.assertTrue(self.owner.interrupt(child, signum))
                self.finish_and_check(child, signum)

    def test_each_interrupt_during_readiness_stops_the_unready_server_and_exits_with_its_code(self):
        for signum in CODES:
            with self.subTest(signal=signal.Signals(signum).name):
                note = self.note_file("ready-%d.note" % signum)
                child = self.start_runner("--ready-timeout", "60", mode="never-ready", env={"FAKE_NOTE_FILE": note})
                self.wait_for_file(note, child)                   # the server is up and answering 503
                self.assertTrue(self.owner.interrupt(child, signum))
                out = self.finish_and_check(child, signum)
                self.assertIn("main=REAPED(0)", out, "the unready server must be stopped gracefully and reaped")

    def test_each_interrupt_during_cleanup_keeps_its_code_and_still_escalates_within_bounds(self):
        for signum in CODES:
            with self.subTest(signal=signal.Signals(signum).name):
                child = self.start_runner("--term-seconds", "1", "--kill-seconds", "5", mode="ignore-term")
                self.wait_for(child, "--error-rate=1 fails deterministically")   # checks done; cleanup is waiting on TERM
                start = time.monotonic()
                self.assertTrue(self.owner.interrupt(child, signum))
                out = self.finish_and_check(child, signum)
                self.assertLess(time.monotonic() - start, 15, "cleanup exceeded its documented bounds")
                self.assertIn("ignored SIGTERM", out)             # the escalation failure is still reported
                self.assertIn("main=REAPED(-9)", out)

    def test_repeated_interruption_keeps_the_first_code_and_resets_no_deadline(self):
        child = self.start_runner("--term-seconds", "2", "--kill-seconds", "5", mode="ignore-term")
        self.wait_for(child, "--error-rate=1 fails deterministically")
        start = time.monotonic()
        self.assertTrue(self.owner.interrupt(child, signal.SIGINT))
        for later in (signal.SIGTERM, signal.SIGHUP, signal.SIGINT):
            time.sleep(0.3)
            self.owner.interrupt(child, later)                      # may be False once it has exited; never an error
        out = self.finish_and_check(child, signal.SIGINT)
        self.assertLess(time.monotonic() - start, 12, "repeated interrupts must not extend the deadlines")
        self.assertEqual(len(re.findall(r"interrupted by signal", out)), 1)

    def test_no_signal_is_delivered_to_a_runner_that_has_already_been_reaped(self):
        child, out = self.run_runner()
        self.assertEqual(child.exit_code, 0, out)
        self.assertFalse(self.owner.interrupt(child, signal.SIGTERM))
        self.assertFalse(self.owner.request_stop(child))


class SupervisorKilledTests(SmokeCase):
    def test_a_killed_supervisor_is_a_documented_limit_nothing_cleans_up_after_it(self):
        child = self.start_runner(env={"FAKE_EXIT_AFTER": "6"}, mode="")
        self.wait_for(child, "main ready on")
        port = int(re.search(r"main ready on 127\.0\.0\.1:(\d+)", self.owner.log_tail(child, 5000)).group(1))
        # The test owns the runner: TERM is sent, then KILL with no graceful interval, so the runner dies before
        # it can run its cleanup. This is the only way to deliver KILL through the library, and it is the
        # runner's own owner doing it.
        report = self.owner.shutdown(0.0, 5.0, children=[child])
        self.assertTrue(child.escalated, "the runner finished its cleanup before the KILL; the scenario was not exercised")
        self.assertEqual(child.exit_code, -9)
        self.assertFalse(report.ok)
        out = self.owner.log_tail(child, 20000)
        self.assertNotIn("mock-smoke: OK", out)
        self.assertNotIn("children:", out, "a killed supervisor cannot report; no self-cleanup is claimed")
        left = self.workdirs_left()
        self.assertEqual(len(left), 1, "the workdir is retained: nothing removes it after a supervisor SIGKILL")
        # Positively establish that a model SURVIVED the supervisor: it still answers right after the runner was
        # collected. (A failed connect here would mean nothing survived and the test proves nothing.)
        with socket.create_connection(("127.0.0.1", port), timeout=2) as probe:
            probe.sendall(b"GET /readyz HTTP/1.0\r\n\r\n")
            self.assertIn(b"200", probe.recv(64), "the orphaned server did not answer after the supervisor was killed")
        # The orphaned fake server is NOT signalled by rediscovered PID; its watchdog (FAKE_EXIT_AFTER=6) ends it.
        end = time.monotonic() + 20
        while time.monotonic() < end:
            s = socket.socket()
            s.settimeout(0.5)
            try:
                s.connect(("127.0.0.1", port))
            except OSError:
                break
            finally:
                s.close()
            time.sleep(0.25)
        else:
            self.fail("the orphaned fake server did not end by its own watchdog")


if __name__ == "__main__":
    unittest.main()
