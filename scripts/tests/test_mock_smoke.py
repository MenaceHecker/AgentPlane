"""Tests for the foreground smoke runner scripts/mock_smoke.py (P0-T5 supervisor revision, commit 3).

The runner is driven against scripts/tests/fake_mockmodel.py (a deterministic stand-in that can inject
faults), so these tests need no Go build; `make mock-smoke` runs the same runner against the real
binary. Each runner is a direct child owned by the test. Every case also asserts the outcome that
matters for cleanup: no workdir left behind on success, the directory retained and the run failed on any
surprise, and every owned server REAPED.

Run:  python3 -I -B -m unittest discover -s scripts/tests -p 'test_mock_smoke.py' -v
"""
import errno
import os
import re
import signal
import sys
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from smoke_support import SmokeCase, FAKE_CMD, REPO, op  # noqa: E402

import mock_smoke  # noqa: E402


class HappyPathTests(SmokeCase):
    def test_success_checks_identical_json_and_sse_bytes_and_removes_the_workdir(self):
        child, out = self.run_runner()
        self.assertEqual(child.exit_code, 0, out)
        for line in ("two identical requests gave byte-identical responses", "stream ok (5 events",
                     "invalid request rejected with 400", "--error-rate=1 fails deterministically with 500",
                     "both servers exited 0 on SIGTERM and every owned child was reaped", "mock-smoke: OK"):
            self.assertIn(line, out)
        self.assertEqual(self.workdirs_left(), [], "the workdir must be removed after success")

    def test_each_stream_invariant_is_enforced_by_breaking_it_in_turn(self):
        cases = (("bad-usage", "stream usage differs from the plain response"),
                 ("bad-done", "[DONE] must appear exactly once"),
                 ("usage-first", "exactly one usage chunk, immediately before [DONE]"),
                 ("bad-content", "stream content differs from the plain response"),
                 ("bad-finish", "finish reason differs"),
                 ("bad-id", "chunk ids differ from the plain response id"))
        for mode, message in cases:
            with self.subTest(mode=mode):
                child, out = self.run_runner(mode=mode)
                self.assertEqual(child.exit_code, 1, out)
                self.assertIn(message, out)
                self.assertEqual(self.workdirs_left(), [], "a check failure still cleans up its own workdir")

    def test_non_identical_requests_fail(self):
        child, out = self.run_runner(mode="nondeterministic")
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("identical requests gave different bytes", out)

    def test_an_invalid_request_that_is_accepted_fails_the_400_check(self):
        child, out = self.run_runner(mode="accept-invalid")
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("invalid request gave 200, want 400", out)

    def test_error_rate_one_that_does_not_fail_fails_the_500_check(self):
        child, out = self.run_runner(mode="ignore-error-rate")
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("--error-rate=1 gave 200, want 500", out)


class LoopbackTests(SmokeCase):
    def ports(self, out):
        return [int(p) for p in re.findall(r"ready on 127\.0\.0\.1:(\d+)", out)]

    def test_servers_listen_on_distinct_ephemeral_loopback_ports_even_when_runs_overlap(self):
        a = self.start_runner()
        b = self.start_runner()
        out_a, out_b = self.wait(a), self.wait(b)
        self.assertEqual((a.exit_code, b.exit_code), (0, 0), out_a + out_b)
        pa, pb = self.ports(out_a), self.ports(out_b)
        self.assertEqual(len(pa), 2)
        self.assertEqual(len(pb), 2)
        self.assertEqual(len(set(pa + pb)), 4, "ports must not collide between overlapping runs")
        self.assertTrue(all(p > 0 for p in pa + pb))
        self.assertEqual(self.workdirs_left(), [], "no shared workdir or decoy between overlapping runs")

    def test_a_server_that_listens_beyond_loopback_is_rejected(self):
        child, out = self.run_runner(mode="bad-addr")
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("want 127.0.0.1 and an assigned port", out)


class StartupFailureTests(SmokeCase):
    def test_readiness_timeout_fails_and_reaps_the_server(self):
        child, out = self.run_runner("--ready-timeout", "1", mode="never-ready")
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("never became ready", out)
        self.assertEqual(self.workdirs_left(), [])

    def test_a_server_that_never_reports_its_address_times_out(self):
        child, out = self.run_runner("--address-timeout", "1", mode="no-address")
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("never reported its address", out)

    def test_a_missing_executable_is_reaped_as_status_127_with_a_diagnostic(self):
        import json
        child, out = self.run_runner(server_cmd=json.dumps(["/nonexistent/mockmodel"]))
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("exited early (status 127)", out)
        self.assertIn("cannot start", out)

    def test_a_server_that_exits_early_is_reported_with_its_status(self):
        child, out = self.run_runner(mode="exit-early")
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("exited early (status 3)", out)

    def test_a_server_that_ignores_term_is_killed_after_the_deadline_and_the_run_fails(self):
        child, out = self.run_runner("--term-seconds", "0.5", "--kill-seconds", "5", mode="ignore-term")
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("ignored SIGTERM", out)
        self.assertEqual(self.workdirs_left(), [], "all children are reaped after the KILL, so the workdir is removed")

    def test_a_nonzero_server_exit_after_term_fails_the_run(self):
        child, out = self.run_runner(mode="nonzero-exit")
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("exited with status 3, want 0", out)

    def test_a_malformed_server_cmd_is_a_usage_error(self):
        child, out = self.run_runner(server_cmd="not json")
        self.assertNotEqual(child.exit_code, 0)


class InProcessTests(SmokeCase):
    """Cases that need an injected fault the real kernel will not produce on demand. They run main() in
    this process with a recording Smoke subclass; the children are still real, owned and reaped."""

    def run_main(self, patches=None, adapter=None, extra=(), fake_exit_after="60"):
        import contextlib
        import io
        recorded = []

        class Recording(mock_smoke.Smoke):
            def __init__(self, *a, **k):
                super().__init__(*a, **k)
                recorded.append(self)

        saved = (mock_smoke.Smoke, op.PosixAdapter, os.environ.get("TMPDIR"), os.environ.get("FAKE_EXIT_AFTER"))
        mock_smoke.Smoke = Recording
        if adapter:
            op.PosixAdapter = adapter
        import tempfile
        saved_tempdir = tempfile.tempdir
        tempfile.tempdir = self.tmp                  # gettempdir() caches; point this process at the test-owned tree
        os.environ["TMPDIR"] = self.tmp
        os.environ["FAKE_EXIT_AFTER"] = fake_exit_after
        for name, fn in (patches or {}).items():
            setattr(Recording, name, fn)
        out = io.StringIO()
        try:
            with contextlib.redirect_stdout(out):
                rc = mock_smoke.main(["--server-cmd", FAKE_CMD] + list(extra))
        finally:
            mock_smoke.Smoke, op.PosixAdapter = saved[0], saved[1]
            tempfile.tempdir = saved_tempdir
            for k, v in (("TMPDIR", saved[2]), ("FAKE_EXIT_AFTER", saved[3])):
                if v is None:
                    os.environ.pop(k, None)
                else:
                    os.environ[k] = v
        return rc, out.getvalue(), recorded[0]

    def test_a_failure_during_the_second_spawn_still_stops_the_first_server_and_cleans_up(self):
        real = op.PosixAdapter

        class SecondSpawnFails(real):
            n = 0

            def spawn(self, argv, log_fd, env):
                SecondSpawnFails.n += 1
                if SecondSpawnFails.n == 2:
                    raise OSError(errno.ENOMEM, "injected: cannot fork the second server")
                return super().spawn(argv, log_fd, env)

        rc, out, smoke = self.run_main(adapter=SecondSpawnFails)
        self.assertEqual(rc, 1, out)
        self.assertRegex(out, r"unexpected \w*Error: .*cannot fork the second server")
        self.assertEqual([c.name for c in smoke.owner.children], ["main"], "only the first server was ever spawned")
        self.assertEqual(smoke.owner.children[0].state, op.State.REAPED)
        self.assertEqual(smoke.owner.children[0].exit_code, 0)
        self.assertEqual(self.workdirs_left(), [])

    def test_an_ordinary_exception_mid_run_runs_the_common_cleanup(self):
        def boom(self, addr):
            raise RuntimeError("injected failure after the servers are up")
        rc, out, smoke = self.run_main(patches={"check_invalid": boom})
        self.assertEqual(rc, 1, out)
        self.assertIn("unexpected RuntimeError", out)
        self.assertTrue(smoke.owner.children and all(c.state == op.State.REAPED and c.exit_code == 0 for c in smoke.owner.children))
        self.assertEqual(self.workdirs_left(), [])

    def test_a_lost_child_keeps_the_workdir_and_fails_without_signalling_it(self):
        real = op.PosixAdapter
        kills = []

        class LoseAfterStart(real):
            lose = False

            def waitpid_nohang(self, pid):
                if LoseAfterStart.lose:                  # from now on the kernel says the child is not ours
                    raise ChildProcessError(10, "No child processes")
                return super().waitpid_nohang(pid)

            def kill(self, pid, sig):
                kills.append((pid, sig))
                return super().kill(pid, sig)

        orig_check = mock_smoke.Smoke.check_invalid

        def check(self, addr):
            LoseAfterStart.lose = True
            return orig_check(self, addr)
        try:
            rc, out, smoke = self.run_main(adapter=LoseAfterStart, patches={"check_invalid": check}, fake_exit_after="5",
                                           extra=("--term-seconds", "0.2", "--kill-seconds", "0.2"))
        finally:
            LoseAfterStart.lose = False
        self.assertEqual(rc, 1, out)
        self.assertEqual(smoke.owner.children[0].state, op.State.LOST)
        self.assertEqual(kills, [], "a lost child must never be signalled")
        self.assertIn("lost ownership", out)
        self.assertIn("was kept for inspection", out)
        self.assertEqual(len(self.workdirs_left()), 1, "a lost child must keep the workdir")
        # Nothing may signal the still-running fake server (that would be a rediscovered PID); its own watchdog
        # ends it after FAKE_EXIT_AFTER seconds. Wait for that so no server outlives the test.
        import socket
        import time
        port = int(re.search(r"main ready on 127\.0\.0\.1:(\d+)", out).group(1))
        end = time.monotonic() + 15
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
            self.fail("the fake server did not end by its own watchdog")


class HttpDeadlineTests(SmokeCase):
    def test_a_trickling_response_cannot_outlast_the_absolute_deadline(self):
        start = time.monotonic()
        child, out = self.run_runner("--http-timeout", "1", mode="trickle")
        self.assertLess(time.monotonic() - start, 15.0, "a slow trickle outlasted the deadline")
        self.assertEqual(child.exit_code, 1, out)
        self.assertRegex(out, r"POST /v1/chat/completions on 127\.0\.0\.1:\d+ did not complete within 1\.0 s")
        self.assertIn("main=REAPED(0)", out)
        self.assertEqual(self.workdirs_left(), [])

    def test_a_server_that_accepts_but_never_answers_hits_the_same_deadline(self):
        start = time.monotonic()
        child, out = self.run_runner("--http-timeout", "1", mode="stall")
        self.assertLess(time.monotonic() - start, 15.0)
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("did not complete within 1.0 s", out)

    def test_an_interrupt_during_a_slow_read_is_honoured_long_before_the_deadline(self):
        note = os.path.join(self.tmp, "slow.note")
        child = self.start_runner("--http-timeout", "120", mode="trickle", env={"FAKE_NOTE_FILE": note})
        end = time.monotonic() + 30
        while time.monotonic() < end and not (os.path.exists(note) and os.path.getsize(note) > 0):
            self.owner.poll(child)
            time.sleep(0.02)
        time.sleep(1.0)                                         # the runner is now reading the trickle
        start = time.monotonic()
        self.assertTrue(self.owner.interrupt(child, signal.SIGTERM))
        out = self.wait(child)
        self.assertLess(time.monotonic() - start, 10.0, "the interrupt waited for the HTTP deadline")
        self.assertEqual(child.exit_code, 143, out)
        self.assertIn("interrupted by signal 15", out)
        self.assertIn("main=REAPED(0)", out)
        self.assertEqual(self.workdirs_left(), [])

    def test_nonfinite_and_nonpositive_deadlines_are_refused(self):
        for flag in ("--http-timeout", "--ready-timeout", "--address-timeout", "--build-timeout", "--term-seconds", "--kill-seconds"):
            for bad in ("nan", "inf", "-1") + (("0",) if flag not in ("--term-seconds", "--kill-seconds") else ()):
                child, out = self.run_runner(flag, bad)
                self.assertEqual(child.exit_code, 2, "%s %s: %s" % (flag, bad, out))
                self.assertIn("finite number", out)
                self.assertEqual(self.workdirs_left(), [], "an invalid argument must be refused before anything is created")


class BrokenOutput:
    """A stdout whose writes start failing (a closed pipe) once a trigger text is seen."""
    def __init__(self, trigger):
        self.trigger, self.broken = trigger, False

    def write(self, text):
        if self.trigger in text:
            self.broken = True
        if self.broken:
            raise BrokenPipeError(32, "Broken pipe")
        return len(text)

    def flush(self):
        if self.broken:
            raise BrokenPipeError(32, "Broken pipe")


class OutputFailureTests(SmokeCase):
    """Diagnostics must never skip cleanup (r4 finding 4)."""

    def run_with_output(self, trigger, patches=None):
        import tempfile
        recorded = []

        class Recording(mock_smoke.Smoke):
            def __init__(self, *a, **k):
                super().__init__(*a, **k)
                recorded.append(self)
        saved = (mock_smoke.Smoke, tempfile.tempdir, os.environ.get("FAKE_EXIT_AFTER"))
        mock_smoke.Smoke = Recording
        tempfile.tempdir = self.tmp
        os.environ["FAKE_EXIT_AFTER"] = "60"
        for k, v in (patches or {}).items():
            setattr(Recording, k, v)
        try:
            rc = mock_smoke.main(["--server-cmd", FAKE_CMD], out=BrokenOutput(trigger))
        finally:
            mock_smoke.Smoke, tempfile.tempdir = saved[0], saved[1]
            if saved[2] is None:
                os.environ.pop("FAKE_EXIT_AFTER", None)
            else:
                os.environ["FAKE_EXIT_AFTER"] = saved[2]
        return rc, recorded[0]

    def assert_cleaned_up(self, smoke):
        self.assertTrue(smoke.owner.children)
        for c in smoke.owner.children:
            self.assertEqual((c.state, c.exit_code), (op.State.REAPED, 0), c)
        self.assertEqual(self.workdirs_left(), [], "the workdir must still be removed")
        self.assertEqual(smoke.wd._child_fd, -1, "descriptors must still be released")

    def test_output_that_closes_after_the_servers_started_still_cleans_up_and_fails(self):
        rc, smoke = self.run_with_output("main ready on")
        self.assertEqual(rc, 1, "a run whose diagnostics could not be written must not report success")
        self.assertTrue(smoke.output_failed)
        self.assert_cleaned_up(smoke)

    def test_output_that_closes_while_reporting_the_result_still_cleans_up(self):
        rc, smoke = self.run_with_output("children:")
        self.assertEqual(rc, 1)
        self.assert_cleaned_up(smoke)

    def test_output_that_fails_while_printing_an_exceptions_traceback_still_cleans_up(self):
        def boom(self, addr):
            raise RuntimeError("injected failure after the servers are up")
        rc, smoke = self.run_with_output("unexpected error", patches={"check_invalid": boom})
        self.assertEqual(rc, 1)
        self.assert_cleaned_up(smoke)

    def test_a_failing_shutdown_step_does_not_skip_closing_descriptors_or_the_workdir_decision(self):
        def boom(self, failures):
            raise RuntimeError("injected: shutdown raised")
        rc, smoke = self.run_with_output("never-matches", patches={"stop_and_verify": boom})
        self.assertEqual(rc, 1)
        self.assertEqual(smoke.wd._child_fd, -1, "workdir descriptors were not released")
        self.assertEqual([c for c in smoke.owner.children if c._log_fd is not None], [], "log descriptors were not closed")
        # the servers were never stopped by the injected failure; their own watchdog (60 s) ends them. Nothing may
        # be signalled by PID from here: the shutdown that failed is exercised, not replaced.
        smoke.owner.shutdown(10.0, 5.0)


class WrapperTests(SmokeCase):
    def test_the_shell_wrapper_execs_the_runner_and_passes_arguments_through(self):
        script = os.path.join(REPO, "scripts", "mock-smoke.sh")
        with open(script) as fh:
            text = fh.read()
        self.assertIn("exec python3 -I -B scripts/mock_smoke.py", text)
        for forbidden in ("kill ", "ps ", "trap ", "&\n", "op_register", "owned-process.sh", "rm -rf"):
            self.assertNotIn(forbidden, text, "the wrapper must contain no process control or deletion")
        child = self.owner.spawn(["bash", script, "--server-cmd", FAKE_CMD], os.path.join(self.tmp, "wrapper.log"),
                                 env=dict(os.environ, TMPDIR=self.tmp, FAKE_EXIT_AFTER="90"))
        out = self.wait(child)
        self.assertEqual(child.exit_code, 0, out)
        self.assertIn("mock-smoke: OK", out)


if __name__ == "__main__":
    unittest.main()
