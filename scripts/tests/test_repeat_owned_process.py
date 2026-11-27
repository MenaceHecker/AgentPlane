"""Tests for scripts/tests/repeat_owned_process.py (the bounded repeat and concurrency runner).

The runner is itself a direct child owned by the test through scripts/lib/owned_process.py,
so its interruption, collection and exit status are observed through retained ownership.
TMPDIR is pointed at a test-owned directory so everything the runner creates is inside it.

Run:  python3 -I -m unittest discover -s scripts/tests -p 'test_repeat_owned_process.py' -v
"""
import contextlib
import errno
import io
import os
import signal
import sys
import tempfile
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "lib"))
import owned_process as op  # noqa: E402

RUNNER = os.path.join(HERE, "repeat_owned_process.py")


class RepeatRunnerTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory(prefix="repeat-runner-test.")
        self.addCleanup(self._tmp.cleanup)
        self.dir = self._tmp.name
        self._saved_sigchld = signal.getsignal(signal.SIGCHLD)
        self.addCleanup(signal.signal, signal.SIGCHLD, self._saved_sigchld)
        self.owner = op.Owner()
        # Bounded cleanup is registered BEFORE anything is spawned (r3 finding 3). The runner is given a
        # real graceful interval (its own cleanup is bounded) so that, if a test fails midway, it can stop
        # and reap ITS children before it is itself killed; killing it first would orphan them.
        self.addCleanup(self.owner.close)
        self.addCleanup(lambda: self.owner.shutdown(20.0, 5.0))

    def run_runner(self, *args, timeout=60):
        log = os.path.join(self.dir, "runner-%d.log" % len(self.owner.children))
        child = self.owner.spawn([sys.executable, "-I", "-B", RUNNER] + list(args), log, env=dict(os.environ, TMPDIR=self.dir))
        end = time.monotonic() + timeout
        while time.monotonic() < end and not child.done:
            self.owner.poll(child)
            time.sleep(0.02)
        if not child.done:
            self.owner.shutdown(20.0, 5.0, children=[child])
            self.fail("the runner did not finish within %s s" % timeout)
        return child, self.owner.log_tail(child, 200000)

    @staticmethod
    def cmd(code):
        return ["--", sys.executable, "-I", "-c", code]

    def test_all_runs_passing_gives_exit_zero_and_collects_everything(self):
        child, out = self.run_runner("--runs", "3", *self.cmd("pass"))
        self.assertEqual(child.exit_code, 0, out)
        self.assertIn("RESULT 3 of 3 runs passed; owned children: 3 reaped, 0 lost, 0 uncollected", out)
        self.assertEqual([n for n in os.listdir(self.dir) if n.startswith("repeat-owned-process.")], [],
                         "the runner left its working directory behind after a clean result")

    def test_a_failing_run_is_reported_and_fails_the_whole(self):
        child, out = self.run_runner("--runs", "2", *self.cmd("import sys; sys.exit(3)"))
        self.assertNotEqual(child.exit_code, 0)
        self.assertIn("exit=3", out)
        self.assertIn("FAIL", out)
        self.assertIn("0 of 2 runs passed", out)

    def test_something_left_in_a_runs_root_is_a_failure_and_the_root_is_kept(self):
        code = "import os; open(os.path.join(os.environ['TMPDIR'], 'leftover'), 'w').close()"
        child, out = self.run_runner("--runs", "1", *self.cmd(code))
        self.assertNotEqual(child.exit_code, 0)
        self.assertIn("left-in-root=1", out)
        self.assertIn("root kept for inspection", out)
        self.assertIn("non-empty roots kept: 1", out)

    def test_every_run_gets_its_own_unique_tmpdir(self):
        code = "import os; print('ROOT=' + os.environ['TMPDIR'])"
        child, out = self.run_runner("--runs", "3", "--parallel", "3", *self.cmd(code))
        self.assertEqual(child.exit_code, 0, out)
        self.assertIn("3 of 3 runs passed", out)

    def test_a_hanging_run_is_stopped_at_the_timeout_and_reaped(self):
        start = time.monotonic()
        child, out = self.run_runner("--runs", "1", "--timeout", "1", "--term-seconds", "2", *self.cmd("import time; time.sleep(60)"))
        self.assertLess(time.monotonic() - start, 30)
        self.assertNotEqual(child.exit_code, 0)
        self.assertIn("exceeded 1 s; stopping it", out)
        self.assertIn("0 lost, 0 uncollected", out)

    def test_busy_workers_start_only_after_the_idle_runs_and_are_stopped_at_the_end(self):
        child, out = self.run_runner("--runs", "3", "--load-runs", "1", "--workers", "2", *self.cmd("pass"))
        self.assertEqual(child.exit_code, 0, out)
        lines = out.splitlines()
        idx = {k: next(i for i, l in enumerate(lines) if k in l) for k in ("run  2 idle", "busy workers started", "run  3 LOADED", "busy workers stopped")}
        self.assertLess(idx["run  2 idle"], idx["busy workers started"])
        self.assertLess(idx["busy workers started"], idx["run  3 LOADED"])
        self.assertLess(idx["run  3 LOADED"], idx["busy workers stopped"])
        self.assertIn("0 lost, 0 uncollected", out)

    def test_a_term_ignoring_run_is_killed_and_reaped_without_an_external_interrupt(self):
        ignore = ("import signal, time\nsignal.signal(signal.SIGTERM, signal.SIG_IGN)\nprint('armed', flush=True)\ntime.sleep(60)")
        start = time.monotonic()
        child, out = self.run_runner("--runs", "1", "--timeout", "1", "--term-seconds", "0.5", "--kill-seconds", "5", *self.cmd(ignore))
        self.assertLess(time.monotonic() - start, 30, "the runner did not end by itself")
        self.assertNotEqual(child.exit_code, 0)
        self.assertIn("exceeded 1 s; stopping it", out)
        self.assertIn("ignored SIGTERM", out)
        self.assertIn("1 reaped, 0 lost, 0 uncollected", out)

    def test_loaded_runs_wait_for_the_idle_cohort_and_worker_readiness_in_mixed_parallel_mode(self):
        child, out = self.run_runner("--runs", "3", "--parallel", "2", "--load-runs", "1", "--workers", "2", *self.cmd("import time; time.sleep(0.5)"))
        self.assertEqual(child.exit_code, 0, out)
        lines = out.splitlines()
        pos = {k: next(i for i, l in enumerate(lines) if k in l) for k in ("run  1 idle", "run  2 idle", "workers started and ready", "run  3 LOADED")}
        self.assertLess(max(pos["run  1 idle"], pos["run  2 idle"]), pos["workers started and ready"], "workers started before the idle cohort finished")
        self.assertLess(pos["workers started and ready"], pos["run  3 LOADED"], "a loaded run started before the workers were ready")

    def test_lost_children_are_never_reported_as_reaped(self):
        class Lost:                                          # every wait says "not our child"; nothing real is spawned
            n = 0
            def spawn(self, argv, log_fd, env):
                Lost.n += 1
                return 7000 + Lost.n
            def waitpid_nohang(self, pid):
                raise ChildProcessError(errno.ECHILD, "No child processes")
            def kill(self, pid, sig):
                raise AssertionError("a lost child must never be signalled")
        real = op.PosixAdapter
        op.PosixAdapter = Lost
        sys.path.insert(0, HERE)
        import repeat_owned_process as rop
        out = io.StringIO()
        try:
            with contextlib.redirect_stdout(out):
                rc = rop.main(["--runs", "1", "--timeout", "0.2", "--term-seconds", "0.1", "--kill-seconds", "0.1", "--", "x"])
        finally:
            op.PosixAdapter = real
        text = out.getvalue()
        self.assertNotEqual(rc, 0)
        self.assertIn("0 reaped, 1 lost, 0 uncollected", text)
        self.assertNotIn("collected: yes", text)
        self.assertNotIn("OK\n", text.replace("PASS", ""))

    def test_an_interrupt_stops_the_runs_collects_every_child_and_exits_143(self):
        log = os.path.join(self.dir, "interrupted.log")
        child = self.owner.spawn([sys.executable, "-I", "-B", RUNNER, "--runs", "2", "--"] + [sys.executable, "-I", "-c", "import time; time.sleep(60)"],
                                 log, env=dict(os.environ, TMPDIR=self.dir))
        end = time.monotonic() + 15
        while time.monotonic() < end and "working directory" not in self.owner.log_tail(child):
            self.owner.poll(child)
            time.sleep(0.02)
        time.sleep(0.5)                                     # let the first run start
        self.assertTrue(self.owner.request_stop(child))      # SIGTERM to the runner, sent by its sole owner
        end = time.monotonic() + 30
        while time.monotonic() < end and not child.done:
            self.owner.poll(child)
            time.sleep(0.02)
        out = self.owner.log_tail(child, 200000)
        self.assertTrue(child.done, out)
        self.assertEqual(child.exit_code, 143, out)
        self.assertIn("interrupted by signal 15", out)
        self.assertIn("0 lost, 0 uncollected", out)


if __name__ == "__main__":
    unittest.main()
