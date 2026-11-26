"""Bounded repeat and concurrency runner for the owned-process tests (P0-T5 supervisor revision).

    python3 -I scripts/tests/repeat_owned_process.py --runs 10 --load-runs 5 --workers 4
    python3 -I scripts/tests/repeat_owned_process.py --parallel 4 --runs 4 --load-runs 0

Each run executes the test command in its OWN unique TMPDIR root. The first
(runs - load_runs) runs are idle; the last load_runs run while `--workers` busy-loop
workers are alive. Every test run and every worker is a direct child that this process
owns through scripts/lib/owned_process.py: spawned, polled, signalled and collected by
this process alone, and reported with its status. Nothing is found by name or PID.
--parallel P starts up to P runs at the same time (load_runs is then normally 0).

For every run it prints the exit code, elapsed time, the number of tests that ran, and
what was left in its root. A root is removed only if it is EMPTY, with a non-recursive
rmdir; a non-empty root is kept and reported. The exit status is 0 only if every run
passed, every root was empty, and every owned child (workers included) was collected
with status 0 and no escalation.

The default command runs the Python ownership-library tests; use `-- CMD ARGS...` to
run another command (for example `make smoke-cleanup-test`).
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
WORKER_CODE = "import signal, sys\nsignal.signal(signal.SIGTERM, lambda *a: sys.exit(0))\nwhile True:\n    pass\n"


class Run:
    def __init__(self, number, loaded, root, child):
        self.number = number
        self.loaded = loaded
        self.root = root
        self.child = child
        self.started = time.monotonic()
        self.elapsed = None


def parse_args(argv):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--runs", type=int, default=10)
    p.add_argument("--load-runs", type=int, default=0, help="how many of the runs (the last ones) execute under load")
    p.add_argument("--workers", type=int, default=4, help="busy-loop workers alive during loaded runs")
    p.add_argument("--parallel", type=int, default=1, help="how many runs may execute at the same time")
    p.add_argument("--timeout", type=float, default=300.0, help="seconds allowed per run before it is stopped and failed")
    p.add_argument("command", nargs="*", help="command to run, after --; default: the Python ownership-library tests")
    args = p.parse_args(argv)
    if args.runs < 1 or args.parallel < 1 or args.workers < 0 or not 0 <= args.load_runs <= args.runs:
        p.error("need runs >= 1, parallel >= 1, workers >= 0 and 0 <= load-runs <= runs")
    return args


def tests_ran(text):
    m = re.search(r"^Ran (\d+) tests?", text, re.M)
    return int(m.group(1)) if m else None


def main(argv=None):
    args = parse_args(sys.argv[1:] if argv is None else argv)
    command = args.command or DEFAULT_COMMAND
    owner = op.Owner()
    owner.install_signal_handlers()
    base = tempfile.mkdtemp(prefix="repeat-owned-process.")          # one private directory; every root and log is inside it
    print("repeat: %d runs (%d under load with %d workers), up to %d at a time; command: %s" %
          (args.runs, args.load_runs, args.workers, args.parallel, " ".join(command)))
    print("repeat: working directory %s" % base)
    first_load = args.runs - args.load_runs + 1
    pending = list(range(1, args.runs + 1))
    active, finished, problems, workers = [], [], [], []
    created_files = []                                                # the exact files this run created, to remove one by one
    try:
        while (pending or active) and owner.interrupted is None:
            if args.workers and not workers and pending and pending[0] >= first_load and args.load_runs and not active:   # idle runs first, all of them finished
                for i in range(args.workers):
                    log = os.path.join(base, "worker%d.log" % i)
                    created_files.append(log)
                    workers.append(owner.spawn([sys.executable, "-I", "-c", WORKER_CODE], log, name="worker%d" % i))
                print("repeat: %d busy workers started" % len(workers))
            while pending and len(active) < args.parallel and owner.interrupted is None:
                n = pending.pop(0)
                root = os.path.join(base, "root%02d" % n)
                os.mkdir(root)
                log = os.path.join(base, "run%02d.log" % n)
                created_files.append(log)
                child = owner.spawn(command, log, name="run%d" % n, env=dict(os.environ, TMPDIR=root))
                active.append(Run(n, n >= first_load, root, child))
            time.sleep(0.05)
            for run in list(active):
                owner.poll(run.child)
                if not run.child.done and time.monotonic() - run.started > args.timeout:
                    owner.request_stop(run.child)                    # the first request only; repeats are idempotent
                    if "run %d exceeded" % run.number not in " ".join(problems):
                        problems.append("run %d exceeded %.0f s and was stopped" % (run.number, args.timeout))
                if not run.child.done:
                    continue
                run.elapsed = time.monotonic() - run.started
                active.remove(run)
                finished.append(run)
                text = owner.log_tail(run.child, 200000)
                left = sorted(os.listdir(run.root))
                ok = run.child.exit_code == 0 and not left
                print("run %2d %-6s exit=%s elapsed=%.1fs tests=%s left-in-root=%d %s" % (
                    run.number, "LOADED" if run.loaded else "idle", run.child.exit_code, run.elapsed, tests_ran(text), len(left),
                    "PASS" if ok else "FAIL"))
                if not ok:
                    problems.append("run %d failed (exit %s, %d entries left in its root)" % (run.number, run.child.exit_code, len(left)))
                    for line in text.strip().splitlines()[-15:]:
                        print("    | " + line)
                if not left:
                    os.rmdir(run.root)                               # an EMPTY root only, never recursive
                else:
                    print("    root kept for inspection: %s" % run.root)
    finally:
        if owner.interrupted is not None:
            problems.append("interrupted by signal %d" % owner.interrupted)
        for run in active:
            owner.request_stop(run.child)
        report = owner.shutdown(10, 5)                               # workers and any unfinished run, bounded
        owner.restore_signal_handlers()
    if workers:
        print("repeat: busy workers stopped")
    unfinished = {r.child.name for r in finished}
    for failure in report.failures:
        if failure.split(":")[0] not in unfinished:                 # a finished run's failure was reported above
            problems.append(failure)
    passed = sum(1 for r in finished if r.child.exit_code == 0 and not os.path.isdir(r.root))
    kept = [r for r in finished if os.path.isdir(r.root)]
    all_done = all(c.done for c in owner.children)
    if not problems and all_done:                                    # nothing worth inspecting: remove only what this run created
        for path in created_files:
            try:
                os.unlink(path)
            except FileNotFoundError:
                pass
        try:
            os.rmdir(base)
        except OSError as exc:
            problems.append("could not remove the empty working directory %s (%s)" % (base, exc))
    elif os.path.isdir(base):
        print("repeat: working directory kept for inspection: %s" % base)
    print("repeat: RESULT %d of %d runs passed; every owned child collected: %s; non-empty roots kept: %d" %
          (passed, args.runs, "yes" if all_done else "NO", len(kept)))
    for problem in problems:
        print("repeat: PROBLEM: %s" % problem)
    ok = not problems and passed == args.runs and all_done
    print("repeat: %s" % ("OK" if ok else "FAILED"))
    return 0 if ok else (owner.interrupt_exit_code or 1)


if __name__ == "__main__":
    sys.exit(main())
