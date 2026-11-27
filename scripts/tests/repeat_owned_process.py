"""Bounded repeat and concurrency runner for the owned-process tests (P0-T5 supervisor revision).

    python3 -I scripts/tests/repeat_owned_process.py --runs 10 --load-runs 5 --workers 4
    python3 -I scripts/tests/repeat_owned_process.py --parallel 4 --runs 4 --load-runs 0
    python3 -I scripts/tests/repeat_owned_process.py --runs 10 --load-runs 5 -- make smoke-cleanup-test

Each run executes the command in its OWN unique TMPDIR root. The first (runs - load_runs)
runs are idle; the last load_runs run while `--workers` busy-loop workers are alive. A
loaded run is never started until every idle run has finished AND every worker has
printed its readiness line. --parallel P starts up to P runs at the same time.

Every run and every worker is a direct child that this process owns through
scripts/lib/owned_process.py: spawned, polled, signalled and collected by this process
alone. A run that exceeds --timeout enters the bounded TERM -> KILL -> reap sequence
(--term-seconds, --kill-seconds, shared deadlines, never reset), so a command that ignores
TERM still ends and is reported. Nothing is found by name or PID.

Per run it prints the exit code, elapsed time, tests run and what was left in its root. A
root is removed only if EMPTY, with a non-recursive rmdir; the log files this program
created are unlinked one by one by name; anything else is kept and reported. The exit status
is 0 only if every run passed, every root was empty and every owned child was REAPED with
status 0 and no escalation. The summary reports REAPED, LOST and uncollected children
separately and never says "reaped" for a child that was not.

The default command runs the Python ownership-library tests; use `-- CMD ARGS...` for another.
"""
import argparse
import os
import re
import sys
import tempfile
import time

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib"))
import owned_process as op  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))
DEFAULT_COMMAND = [sys.executable, "-I", "-B", "-m", "unittest", "discover", "-s", os.path.join(REPO, "scripts", "tests"),
                   "-p", "test_owned_process.py"]
WORKER_READY = "worker ready"
WORKER_CODE = ("import signal, sys\n"
               "signal.signal(signal.SIGTERM, lambda *a: sys.exit(0))\n"
               "print('%s', flush=True)\n"
               "while True:\n    pass\n" % WORKER_READY)
WORKER_READY_SECONDS = 20.0


class Run:
    def __init__(self, number, loaded, root, child):
        self.number, self.loaded, self.root, self.child = number, loaded, root, child
        self.started = time.monotonic()
        self.elapsed = None
        self.timed_out = False


def finite(minimum, inclusive):
    def parse(text):
        try:
            value = float(text)
        except ValueError:
            raise argparse.ArgumentTypeError("%r is not a number" % text)
        if value != value or value in (float("inf"), float("-inf")) or value < minimum or (value == minimum and not inclusive):
            raise argparse.ArgumentTypeError("%r must be a finite number %s %g" % (text, ">=" if inclusive else ">", minimum))
        return value
    return parse


def parse_args(argv):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--runs", type=int, default=10)
    p.add_argument("--load-runs", type=int, default=0, help="how many of the runs (the last ones) execute under load")
    p.add_argument("--workers", type=int, default=4, help="busy-loop workers alive during loaded runs")
    p.add_argument("--parallel", type=int, default=1, help="how many runs may execute at the same time")
    p.add_argument("--timeout", type=finite(0, False), default=300.0, help="seconds a run may take before it is stopped")
    p.add_argument("--term-seconds", type=finite(0, True), default=op.DEFAULT_TERM_SECONDS)
    p.add_argument("--kill-seconds", type=finite(0, True), default=op.DEFAULT_KILL_SECONDS)
    p.add_argument("command", nargs="*", help="command to run, after --; default: the ownership-library tests")
    args = p.parse_args(argv)
    if args.runs < 1 or args.parallel < 1 or args.workers < 0 or not 0 <= args.load_runs <= args.runs:
        p.error("need runs >= 1, parallel >= 1, workers >= 0 and 0 <= load-runs <= runs")
    if args.load_runs and not args.workers:
        p.error("--load-runs needs --workers >= 1")
    return args


def tests_ran(text):
    """Sum every unittest "Ran N tests" summary (a wrapper such as make may run several suites)."""
    counts = [int(n) for n in re.findall(r"^Ran (\d+) tests?", text, re.M)]
    return sum(counts) if counts else None


def main(argv=None):
    args = parse_args(sys.argv[1:] if argv is None else argv)
    command = args.command or DEFAULT_COMMAND
    owner = op.Owner()
    owner.install_signal_handlers()
    base = tempfile.mkdtemp(prefix="repeat-owned-process.")
    print("repeat: %d runs (%d under load with %d workers), up to %d at a time; command: %s" %
          (args.runs, args.load_runs, args.workers, args.parallel, " ".join(command)))
    print("repeat: working directory %s" % base)
    first_load = args.runs - args.load_runs + 1
    pending = list(range(1, args.runs + 1))
    active, finished, problems, workers, stuck = [], [], [], [], []
    created_files = []
    try:
        while (pending or active) and owner.interrupted is None:
            if stuck:
                break
            idle_left = any(n < first_load for n in pending) or any(not r.loaded for r in active)
            # Loaded runs: only after the whole idle cohort is finished, and only once every worker is ready.
            if args.load_runs and not idle_left and not workers:
                for i in range(args.workers):
                    log = os.path.join(base, "worker%d.log" % i)
                    created_files.append(log)
                    workers.append(owner.spawn([sys.executable, "-I", "-c", WORKER_CODE], log, name="worker%d" % i))
                end = time.monotonic() + WORKER_READY_SECONDS
                while time.monotonic() < end and owner.interrupted is None:
                    for w in workers:
                        owner.poll(w)
                    if all(WORKER_READY in owner.log_tail(w) for w in workers):
                        break
                    time.sleep(0.02)
                else:
                    pass
                if not all(WORKER_READY in owner.log_tail(w) for w in workers):
                    problems.append("busy workers were not ready within %.0f s; loaded runs were not started" % WORKER_READY_SECONDS)
                    break
                print("repeat: %d busy workers started and ready" % len(workers))
            while pending and len(active) < args.parallel and owner.interrupted is None:
                n = pending[0]
                loaded = n >= first_load
                if loaded and (idle_left or not workers):
                    break                                            # never start a nominally loaded run early
                if not loaded and any(r.loaded for r in active):
                    break
                pending.pop(0)
                root = os.path.join(base, "root%02d" % n)
                os.mkdir(root)
                log = os.path.join(base, "run%02d.log" % n)
                created_files.append(log)
                child = owner.spawn(command, log, name="run%d" % n, env=dict(os.environ, TMPDIR=root))
                active.append(Run(n, loaded, root, child))
            time.sleep(0.05)
            for run in list(active):
                owner.poll(run.child)
                if not run.child.done and time.monotonic() - run.started > args.timeout:
                    run.timed_out = True
                    problems.append("run %d exceeded %.0f s; stopping it (TERM, then KILL if ignored)" % (run.number, args.timeout))
                    owner.shutdown(args.term_seconds, args.kill_seconds, children=[run.child])
                    if not run.child.done:
                        # One bounded TERM/KILL/reap attempt was made and its deadlines are spent. Do not
                        # retry, do not reset them, do not signal anything else: stop scheduling and fail,
                        # keeping the evidence (the child may still be running; the summary counts it).
                        active.remove(run)
                        stuck.append(run)
                        problems.append("run %d could not be collected within its reap deadline (state %s); cleanup is incomplete and the process may still be running" % (run.number, run.child.state))
                        pending.clear()
                        continue
                if not run.child.done:
                    continue
                run.elapsed = time.monotonic() - run.started
                active.remove(run)
                finished.append(run)
                text = owner.log_tail(run.child, 200000)
                left = sorted(os.listdir(run.root))
                ok = run.child.state == op.State.REAPED and run.child.exit_code == 0 and not left and not run.timed_out
                print("run %2d %-6s exit=%s elapsed=%.1fs tests=%s left-in-root=%d %s" % (
                    run.number, "LOADED" if run.loaded else "idle", run.child.exit_code, run.elapsed, tests_ran(text), len(left),
                    "PASS" if ok else "FAIL"))
                if run.child.escalated:
                    problems.append("run %d ignored SIGTERM and had to be killed (status %s)" % (run.number, run.child.exit_code))
                if not ok:
                    problems.append("run %d failed (state %s, exit %s, %d entries left in its root)" % (
                        run.number, run.child.state, run.child.exit_code, len(left)))
                    for line in text.strip().splitlines()[-15:]:
                        print("    | " + line)
                if not left:
                    os.rmdir(run.root)                               # EMPTY only, never recursive
                else:
                    print("    root kept for inspection: %s" % run.root)
    finally:
        if owner.interrupted is not None:
            problems.append("interrupted by signal %d" % owner.interrupted)
        try:
            report = owner.shutdown(args.term_seconds, args.kill_seconds)   # workers and any unfinished run, bounded
        finally:
            owner.restore_signal_handlers()
            owner.close()          # descriptors are released on EVERY outcome; the log FILES stay as evidence
    if workers:
        print("repeat: busy workers stopped")
    already = {r.child.name for r in finished}
    for failure in report.failures:
        if failure.split(":")[0] not in already:
            problems.append(failure)
    reaped = [c for c in owner.children if c.state == op.State.REAPED]
    lost = [c for c in owner.children if c.state == op.State.LOST]
    uncollected = [c for c in owner.children if c.state not in (op.State.REAPED, op.State.LOST)]
    passed = sum(1 for r in finished if r.child.state == op.State.REAPED and r.child.exit_code == 0
                 and not r.timed_out and not os.path.isdir(r.root))
    kept = [r for r in finished if os.path.isdir(r.root)]
    clean = not problems and not lost and not uncollected
    if clean:
        for path in created_files:
            try:
                os.unlink(path)                                      # exactly the files this run created, by name
            except FileNotFoundError:
                pass
        owner.close()
        try:
            os.rmdir(base)
        except OSError as exc:
            problems.append("could not remove the empty working directory %s (%s)" % (base, exc))
    elif os.path.isdir(base):
        print("repeat: working directory kept for inspection: %s" % base)
    print("repeat: RESULT %d of %d runs passed; owned children: %d reaped, %d lost, %d uncollected; non-empty roots kept: %d" %
          (passed, args.runs, len(reaped), len(lost), len(uncollected), len(kept)))
    for problem in problems:
        print("repeat: PROBLEM: %s" % problem)
    ok = clean and not problems and passed == args.runs
    print("repeat: %s" % ("OK" if ok else "FAILED"))
    return 0 if ok else (owner.interrupt_exit_code or 1)


if __name__ == "__main__":
    sys.exit(main())
