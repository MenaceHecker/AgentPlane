"""Shared helpers for the smoke-runner tests: run scripts/mock_smoke.py as a DIRECT CHILD that the
test owns through scripts/lib/owned_process.py, with bounded cleanup registered before it spawns.

Every runner gets its own private TMPDIR (a test-owned directory), so the runner's workdir, its
fake servers' logs and anything left behind are inside a tree the test created and removes.
"""
import json
import os
import shutil
import signal
import sys
import tempfile
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(REPO, "scripts", "lib"))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import owned_process as op  # noqa: E402

RUNNER = os.path.join(REPO, "scripts", "mock_smoke.py")
FAKE = os.path.join(HERE, "fake_mockmodel.py")
FAKE_CMD = json.dumps([sys.executable, "-I", "-B", FAKE])


class SmokeCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.mkdtemp(prefix="smoke-test.")
        self.tmp = os.path.realpath(self._tmp)
        self._saved_sigchld = signal.getsignal(signal.SIGCHLD)
        self.addCleanup(signal.signal, signal.SIGCHLD, self._saved_sigchld)
        self.addCleanup(lambda: shutil.rmtree(self.tmp, ignore_errors=True))   # the test's own scratch tree
        self.owner = op.Owner()
        # Registered BEFORE anything is spawned, run last-in-first-out: a bounded, real graceful interval so a
        # runner can stop and reap its own children before it is itself killed, then the descriptors.
        self.addCleanup(self.owner.close)
        self.addCleanup(lambda: self.owner.shutdown(20.0, 5.0))
        self.n = 0

    def start_runner(self, *args, mode="", env=None, server_cmd=FAKE_CMD, tmpdir=None):
        self.n += 1
        e = dict(os.environ, TMPDIR=tmpdir or self.tmp, FAKE_MODE=mode, FAKE_EXIT_AFTER="90")
        e.update(env or {})
        argv = [sys.executable, "-I", "-B", RUNNER]
        if server_cmd:
            argv += ["--server-cmd", server_cmd]
        argv += list(args)
        return self.owner.spawn(argv, os.path.join(self.tmp, "runner-%d.log" % self.n), name="runner%d" % self.n, env=e)

    def wait(self, child, timeout=90.0):
        end = time.monotonic() + timeout
        while time.monotonic() < end and not child.done:
            self.owner.poll(child)
            time.sleep(0.02)
        if not child.done:
            self.owner.shutdown(20.0, 5.0, children=[child])
            self.fail("the runner did not finish in %.0f s: %s" % (timeout, self.owner.log_tail(child, 3000)))
        return self.owner.log_tail(child, 200000)

    def wait_for(self, child, text, timeout=30.0):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            self.owner.poll(child)
            if text in self.owner.log_tail(child, 200000):
                return
            if child.done:
                break
            time.sleep(0.02)
        self.fail("never saw %r in the runner output: %s" % (text, self.owner.log_tail(child, 3000)))

    def run_runner(self, *args, **kw):
        child = self.start_runner(*args, **kw)
        out = self.wait(child)
        return child, out

    def workdirs_left(self):
        return sorted(n for n in os.listdir(self.tmp) if n.startswith("mock-smoke."))
