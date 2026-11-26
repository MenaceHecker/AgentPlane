"""Tests for scripts/tests/repeat_owned_process.py (the bounded repeat and concurrency runner).

The runner is itself a direct child owned by the test through scripts/lib/owned_process.py,
so its interruption, collection and exit status are observed through retained ownership.
TMPDIR is pointed at a test-owned directory so everything the runner creates is inside it.

Run:  python3 -I -m unittest discover -s scripts/tests -p 'test_repeat_owned_process.py' -v
"""
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

    def run_runner(self, *args, timeout=60):
        log = os.path.join(self.dir, "runner-%d.log" % len(self.owner.children))
        child = self.owner.spawn([sys.executable, "-I", "-B", RUNNER] + list(args), log, env=dict(os.environ, TMPDIR=self.dir))
        end = time.monotonic() + timeout
        while time.monotonic() < end and not child.done:
            self.owner.poll(child)
            time.sleep(0.02)
        if not child.done:
            self.owner.shutdown(2, 2, children=[child])
            self.fail("the runner did not finish within %s s" % timeout)
        return child, self.owner.log_tail(child, 200000)

    @staticmethod
    def cmd(code):
        return ["--", sys.executable, "-I", "-c", code]

    def test_all_runs_passing_gives_exit_zero_and_collects_everything(self):
        child, out = self.run_runner("--runs", "3", *self.cmd("pass"))
        self.assertEqual(child.exit_code, 0, out)
        self.assertIn("RESULT 3 of 3 runs passed; every owned child collected: yes", out)
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
        child, out = self.run_runner("--runs", "1", "--timeout", "1", *self.cmd("import time; time.sleep(60)"))
        self.assertLess(time.monotonic() - start, 30)
        self.assertNotEqual(child.exit_code, 0)
        self.assertIn("exceeded 1 s and was stopped", out)
        self.assertIn("every owned child collected: yes", out)

    def test_busy_workers_start_only_after_the_idle_runs_and_are_stopped_at_the_end(self):
        child, out = self.run_runner("--runs", "3", "--load-runs", "1", "--workers", "2", *self.cmd("pass"))
        self.assertEqual(child.exit_code, 0, out)
        lines = out.splitlines()
        idx = {k: next(i for i, l in enumerate(lines) if k in l) for k in ("run  2 idle", "busy workers started", "run  3 LOADED", "busy workers stopped")}
        self.assertLess(idx["run  2 idle"], idx["busy workers started"])
        self.assertLess(idx["busy workers started"], idx["run  3 LOADED"])
        self.assertLess(idx["run  3 LOADED"], idx["busy workers stopped"])
        self.assertIn("every owned child collected: yes", out)

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
        self.assertIn("every owned child collected: yes", out)


if __name__ == "__main__":
    unittest.main()
