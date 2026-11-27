"""Tests for the persistent kind owner scripts/kind_cluster.py (P0-T5 r4, kind follow-up commit 3).

NO live cluster operation exists here: docker, kind, kubectl, go and the topology checker are stub
scripts in a test-owned directory placed first on PATH. Each run of the owner is a direct child that
the test owns through scripts/lib/owned_process.py (so INT, TERM and HUP are delivered, and its exit
status collected, by its sole owner), with bounded cleanup registered before it starts and a private
TMPDIR so everything it creates is inside a tree the test created.

The requirement under test: a successful operation returns 0 ONLY after the context was restored and
read back, every child was reaped and the owned workdir was removed; any failure returns nonzero, keeps
its own message and status, and retains what is needed for inspection without touching a replacement's
decoy or any unrelated cluster.

Run:  python3 -I -B -m unittest discover -s scripts/tests -p 'test_kind_cluster.py' -v
"""
import contextlib
import errno
import io
import os
import shutil
import signal
import stat
import sys
import tempfile
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(REPO, "scripts", "lib"))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import owned_process as op  # noqa: E402
import kind_cluster  # noqa: E402

RUNNER = os.path.join(REPO, "scripts", "kind_cluster.py")
PRIOR = "my-prior-context"
DECOY = b"decoy: must survive\n"

DOCKER = """#!/usr/bin/env bash
echo "docker $*" >>"$ST/log"
[ "$1" = info ] && [ -n "${STUB_DOCKER_DOWN:-}" ] && { echo "Cannot connect to the Docker daemon" >&2; exit 1; }
exit 0
"""
KIND = """#!/usr/bin/env bash
echo "kind $*" >>"$ST/log"
name() { while [ $# -gt 0 ]; do [ "$1" = --name ] && { echo "$2"; return; }; shift; done; }
case "$1 $2" in
  "version "*) echo "kind v0.31.0 go1.25.5 test/stub" ;;
  "get clusters") cat "$ST/clusters" ;;
  "create cluster")
    n="$(name "$@")"
    if [ -n "${STUB_CREATE_SLEEP:-}" ]; then
      [ -n "${STUB_CREATE_IGNORE_TERM:-}" ] && trap '' TERM
      echo "creating..." ; echo "kind-$n" >"$ST/current"   # kind switches the context as soon as it starts
      i=0; while [ "$i" -lt "$STUB_CREATE_SLEEP" ]; do sleep 1; i=$((i + 1)); done
    fi
    echo "$n" >>"$ST/clusters"; echo "kind-$n" >>"$ST/contexts"; echo "kind-$n" >"$ST/current"; echo "Created $n" ;;
  "delete cluster")
    n="$(name "$@")"; grep -vx -- "$n" "$ST/clusters" >"$ST/c.tmp"; mv "$ST/c.tmp" "$ST/clusters"
    grep -vx -- "kind-$n" "$ST/contexts" >"$ST/x.tmp"; mv "$ST/x.tmp" "$ST/contexts"
    if [ "$(cat "$ST/current")" = "kind-$n" ]; then : >"$ST/current"; fi ;;
  "export kubeconfig") n="$(name "$@")"; echo "kind-$n" >>"$ST/contexts"; echo "kind-$n" >"$ST/current" ;;
  *) echo "stub kind: unexpected: $*" >&2; exit 99 ;;
esac
"""
KUBECTL = """#!/usr/bin/env bash
echo "kubectl $*" >>"$ST/log"
case "$1" in
  config)
    case "$2" in
      view)
        n=$(( $(cat "$ST/ctxreads" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$ST/ctxreads"
        for f in ${STUB_CTXREAD_FAIL_ON:-}; do [ "$f" = "$n" ] && { echo 'error: error loading config file "/stub/kubeconfig": invalid' >&2; exit 1; }; done
        cat "$ST/current" ;;
      use-context) [ -z "${STUB_USECONTEXT_NOOP:-}" ] && echo "$3" >"$ST/current"; exit 0 ;;
      unset) : >"$ST/current" ;;
      get-contexts) cat "$ST/contexts" ;;
      *) exit 99 ;;
    esac ;;
  --context) shift 2
    if [ "$1 $2 $3 $4" = "get nodes -o json" ]; then cat "$STUB_NODES"; else echo "stub kubectl: unexpected: $*" >&2; exit 99; fi ;;
  *) echo "UNTARGETED kubectl $*" >>"$ST/violations"; exit 99 ;;
esac
"""
SHAPE = """#!/usr/bin/env bash
echo "kindshape $*" >>"$ST/log"
if [ "$1" = check-nodes ]; then n="$(wc -c | tr -d ' ')"; echo "kindshape stdin bytes: $n" >>"$ST/log"; fi
[ -n "${STUB_SHAPE_FAIL:-}" ] && { echo "kindshape: incompatible" >&2; exit 1; }
echo "kindshape: $1 OK"
"""
GO = """#!/usr/bin/env bash
echo "go $*" >>"$ST/log"
[ -n "${STUB_GO_FAIL:-}" ] && { echo "go: build failed" >&2; exit 1; }
if [ "$1" = build ] && [ "$2" = -o ]; then cp "$STUB_SHAPE_SRC" "$3" && chmod +x "$3"; fi
"""


class KindCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.mkdtemp(prefix="kind-test.")
        self.tmp = os.path.realpath(self._tmp)
        self.addCleanup(lambda: shutil.rmtree(self.tmp, ignore_errors=True))        # the test's own scratch tree
        self._saved_sigchld = signal.getsignal(signal.SIGCHLD)
        self.addCleanup(signal.signal, signal.SIGCHLD, self._saved_sigchld)
        self.owner = op.Owner()
        self.addCleanup(self.owner.close)                                            # registered BEFORE anything spawns
        self.addCleanup(lambda: self.owner.shutdown(20.0, 5.0))
        self.bin = os.path.join(self.tmp, "bin")
        self.st = os.path.join(self.tmp, "st")
        self.roottmp = os.path.join(self.tmp, "tmp")
        for d in (self.bin, self.st, self.roottmp):
            os.mkdir(d)
        for name, body in (("docker", DOCKER), ("kind", KIND), ("kubectl", KUBECTL), ("go", GO)):
            self.write_exec(os.path.join(self.bin, name), body)
        self.shape = os.path.join(self.tmp, "kindshape-stub")
        self.write_exec(self.shape, SHAPE)
        self.nodes = os.path.join(self.tmp, "nodes.json")
        with open(self.nodes, "w") as fh:
            fh.write('{"items": []}')
        self.n = 0
        self.reset(PRIOR)

    @staticmethod
    def write_exec(path, body):
        with open(path, "w") as fh:
            fh.write(body)
        os.chmod(path, 0o755)

    def reset(self, prior, *clusters):
        for f in os.listdir(self.st):
            os.unlink(os.path.join(self.st, f))
        for f in ("log", "violations", "clusters", "contexts", "current"):
            open(os.path.join(self.st, f), "w").close()
        if prior:
            self.put("current", prior + "\n")
            self.put("contexts", prior + "\n")
        for c in clusters:
            with open(os.path.join(self.st, "clusters"), "a") as fh:
                fh.write(c + "\n")
            with open(os.path.join(self.st, "contexts"), "a") as fh:
                fh.write("kind-" + c + "\n")

    def put(self, name, text):
        with open(os.path.join(self.st, name), "w") as fh:
            fh.write(text)

    def get(self, name):
        with open(os.path.join(self.st, name)) as fh:
            return fh.read()

    def calls(self, prefix=""):
        return [l for l in self.get("log").split("\n") if l.startswith(prefix) and l]

    def start(self, mode, name, *args, env=None, shape=True):
        self.n += 1
        e = dict(os.environ, TMPDIR=self.roottmp, PATH=self.bin + os.pathsep + os.environ["PATH"], ST=self.st,
                 STUB_NODES=self.nodes, STUB_SHAPE_SRC=self.shape)
        e.pop("KINDSHAPE_BIN", None)
        if shape:
            e["KINDSHAPE_BIN"] = self.shape
        e.update(env or {})
        argv = [sys.executable, "-I", "-B", RUNNER, mode, name] + list(args)
        return self.owner.spawn(argv, os.path.join(self.tmp, "run-%d.log" % self.n), name="kind%d" % self.n, env=e)

    def wait(self, child, timeout=90.0):
        end = time.monotonic() + timeout
        while time.monotonic() < end and not child.done:
            self.owner.poll(child)
            time.sleep(0.02)
        if not child.done:
            self.owner.shutdown(20.0, 5.0, children=[child])
            self.fail("the owner did not finish in %.0f s: %s" % (timeout, self.owner.log_tail(child, 3000)))
        return self.owner.log_tail(child, 400000)

    def wait_for_log(self, name, text, child, timeout=30.0):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            self.owner.poll(child)
            if text in self.get(name):
                return
            if child.done:
                break
            time.sleep(0.02)
        self.fail("never saw %r in %s: %s" % (text, name, self.owner.log_tail(child, 3000)))

    def run_kind(self, mode, name, *args, **kw):
        child = self.start(mode, name, *args, **kw)
        out = self.wait(child)
        return child, out

    def root_entries(self):
        return sorted(os.listdir(self.roottmp))

    def assert_clean_success(self, child, out):
        self.assertEqual(child.exit_code, 0, out)
        self.assertEqual(self.root_entries(), [], "the owned directory must be gone after a successful run")
        self.assertEqual(self.get("current").strip(), PRIOR if self.restored_expected else self.get("current").strip())

    restored_expected = True


class SuccessTests(KindCase):
    def test_a_successful_up_exits_zero_only_after_verified_restoration_and_owned_cleanup(self):
        child, out = self.run_kind("up", "agentplane-t")
        self.assertEqual(child.exit_code, 0, out)
        self.assertEqual(self.root_entries(), [], "the owned directory must be gone")
        self.assertEqual(self.get("current").strip(), PRIOR, "kind switched the context; it must be put back")
        self.assertIn("your kubectl current context is '%s' (verified)" % PRIOR, out)
        self.assertIn("created; use it with: kubectl --context kind-agentplane-t", out)
        self.assertEqual(self.calls("kind create"), ["kind create cluster --name agentplane-t --config deploy/kind/config.yaml"])
        self.assertEqual(self.get("violations"), "", "kubectl used the current context for something")
        self.assertIn("kindshape stdin bytes: %d" % os.path.getsize(self.nodes), self.get("log"), "the captured node JSON must reach the checker's stdin")
        self.assertEqual(child.state, op.State.REAPED)

    def test_down_deletes_exactly_the_named_cluster_and_exits_zero_with_nothing_left(self):
        self.reset(PRIOR, "other", "agentplane-x", "third")
        child, out = self.run_kind("down", "agentplane-x")
        self.assertEqual(child.exit_code, 0, out)
        self.assertEqual(self.root_entries(), [])
        self.assertEqual(self.calls("kind delete"), ["kind delete cluster --name agentplane-x"])
        self.assertEqual(sorted(self.get("clusters").split()), ["other", "third"])
        self.assertEqual(self.get("current").strip(), PRIOR)

    def test_down_of_a_missing_cluster_and_an_idempotent_up_exit_zero_and_change_nothing(self):
        self.reset(PRIOR, "other")
        child, out = self.run_kind("down", "agentplane-x")
        self.assertEqual(child.exit_code, 0, out)
        self.assertIn("does not exist; nothing to do", out)
        self.assertEqual(self.calls("kind delete"), [])
        self.assertEqual(self.root_entries(), [])
        self.reset(PRIOR, "agentplane-t")
        child, out = self.run_kind("up", "agentplane-t")
        self.assertEqual(child.exit_code, 0, out)
        self.assertIn("already exists and matches the supported topology; nothing to do", out)
        self.assertEqual(self.calls("kind create"), [])
        self.assertEqual(self.root_entries(), [])

    def test_down_of_the_current_context_clears_it_and_verifies(self):
        self.reset("kind-agentplane-x", "agentplane-x")
        child, out = self.run_kind("down", "agentplane-x")
        self.assertEqual(child.exit_code, 0, out)
        self.assertIn("current context is '<none>' (verified)", out)
        self.assertEqual(self.get("current").strip(), "")

    def test_no_unrelated_cluster_is_ever_touched(self):
        self.reset(PRIOR, "keep-me", "agentplane-t")
        child, out = self.run_kind("up", "agentplane-t")
        self.assertEqual(child.exit_code, 0, out)
        for line in self.calls("kind ") + self.calls("kubectl "):
            self.assertNotIn("keep-me", line)
        self.assertEqual(self.calls("kind delete"), [])
        targeted = [l for l in self.calls("kubectl ") if not l.startswith("kubectl config ")]
        self.assertTrue(all(l.startswith("kubectl --context kind-agentplane-t ") for l in targeted), targeted)


class FailureTests(KindCase):
    def test_an_unreadable_context_before_any_change_refuses_to_do_anything(self):
        child, out = self.run_kind("up", "agentplane-t", env={"STUB_CTXREAD_FAIL_ON": "1"})
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("cannot establish your current kubectl context", out)
        self.assertEqual(self.calls("kind create"), [])
        self.assertEqual(self.root_entries(), [], "even a refused run removes its own directory")

    def test_a_failed_context_restore_fails_the_run_but_still_removes_the_directory(self):
        child, out = self.run_kind("up", "agentplane-t", env={"STUB_USECONTEXT_NOOP": "1"})
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("could not restore your kubectl current context: it was '%s' and it is now 'kind-agentplane-t'" % PRIOR, out)
        self.assertEqual(self.root_entries(), [])

    def test_an_unreadable_context_after_the_change_fails_the_run(self):
        child, out = self.run_kind("up", "agentplane-t", env={"STUB_CTXREAD_FAIL_ON": "2 3 4 5 6 7 8"})
        self.assertEqual(child.exit_code, 1, out)
        self.assertRegex(out, r"cannot read your kubectl current context to verify it|cannot read it back")
        self.assertEqual(self.root_entries(), [])

    def test_a_failed_operation_keeps_its_own_status_and_message_and_leaves_nothing(self):
        child, out = self.run_kind("up", "agentplane-t", env={"STUB_DOCKER_DOWN": "1"})
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("Docker daemon is not reachable", out)
        self.assertEqual(self.calls("kind create"), [])
        self.assertEqual(self.root_entries(), [])

    def test_a_failed_topology_check_keeps_the_cluster_and_the_message(self):
        child, out = self.run_kind("up", "agentplane-t", env={"STUB_SHAPE_FAIL": "1"})
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("is not the supported topology; not creating anything", out)
        self.assertEqual(self.calls("kind create"), [])
        self.assertEqual(self.root_entries(), [])

    def test_a_failed_checker_build_is_reported_and_leaves_nothing(self):
        child, out = self.run_kind("up", "agentplane-t", env={"STUB_GO_FAIL": "1"}, shape=False)
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("could not build the topology checker", out)
        self.assertIn("go: build failed", out, "the tool's own diagnostics must reach the terminal")
        self.assertEqual(self.calls("kind create"), [])
        self.assertEqual(self.root_entries(), [])

    def test_the_built_checker_is_adopted_and_removed_with_the_workdir(self):
        child, out = self.run_kind("up", "agentplane-t", shape=False)
        self.assertEqual(child.exit_code, 0, out)
        self.assertEqual(self.root_entries(), [])
        self.assertTrue(any(l.startswith("go build -o ") and l.endswith("/kindshape ./cmd/kindshape") for l in self.calls("go ")))

    def test_invalid_names_are_refused_before_any_tool_runs_or_anything_is_created(self):
        for bad in ("Agent", "a;b", "a-", "a b", "a/b", "a$(id)", ""):
            child, out = self.run_kind("up", bad)
            self.assertEqual(child.exit_code, 1, "%r: %s" % (bad, out))
            self.assertIn("cluster name must be lowercase alphanumerics", out)
        self.assertEqual(self.get("log"), "")
        self.assertEqual(self.root_entries(), [])

    def test_the_wrapper_refuses_every_invalid_name_including_a_leading_dash_before_python_starts(self):
        wrapper = os.path.join(REPO, "scripts", "kind-cluster.sh")
        for bad in ("Agent", "a;b", "-a", "a-", "a b", "a/b", "a$(id)", ""):
            child = self.owner.spawn(["bash", wrapper, "up", bad], os.path.join(self.tmp, "w-%d.log" % self.n),
                                     env=dict(os.environ, TMPDIR=self.roottmp, ST=self.st, PATH=self.bin + os.pathsep + os.environ["PATH"]))
            self.n += 1
            out = self.wait(child)
            self.assertEqual(child.exit_code, 1, "%r: %s" % (bad, out))
            self.assertIn("cluster name must be lowercase alphanumerics", out)
        self.assertEqual(self.get("log"), "")
        self.assertEqual(self.root_entries(), [])

    def test_invalid_deadline_arguments_are_refused(self):
        for flag, bad in (("--command-timeout", "nan"), ("--command-timeout", "0"), ("--command-timeout", "inf"),
                          ("--term-seconds", "-1"), ("--kill-seconds", "nan")):
            child, out = self.run_kind("up", "agentplane-t", flag, bad)
            self.assertEqual(child.exit_code, 2, "%s %s: %s" % (flag, bad, out))
            self.assertIn("finite number", out)
        self.assertEqual(self.get("log"), "")


class DeadlineAndInterruptTests(KindCase):
    def test_a_command_that_exceeds_its_deadline_is_stopped_and_reaped_and_the_context_is_restored(self):
        child, out = self.run_kind("up", "agentplane-t", "--command-timeout", "2", "--term-seconds", "1",
                                   env={"STUB_CREATE_SLEEP": "60"})
        self.assertEqual(child.exit_code, 1, out)
        self.assertIn("did not finish within 2 s and was stopped", out)
        self.assertEqual(self.get("current").strip(), PRIOR, "kind switched the context before it hung; it must still be restored")
        self.assertEqual(self.root_entries(), [])

    def test_a_command_that_ignores_term_is_killed_after_the_grace_and_the_run_fails(self):
        child, out = self.run_kind("up", "agentplane-t", "--command-timeout", "2", "--term-seconds", "0.5", "--kill-seconds", "5",
                                   env={"STUB_CREATE_SLEEP": "60", "STUB_CREATE_IGNORE_TERM": "1"})
        self.assertEqual(child.exit_code, 1, out)
        self.assertEqual(self.root_entries(), [], "after the KILL every child is reaped, so the directory is removed")
        self.assertEqual(self.get("current").strip(), PRIOR)

    def test_each_interrupt_during_a_long_command_stops_it_restores_the_context_and_exits_with_its_code(self):
        codes = {signal.SIGINT: 130, signal.SIGTERM: 143, signal.SIGHUP: 129}
        for signum, code in codes.items():
            with self.subTest(signal=signal.Signals(signum).name):
                self.reset(PRIOR)
                child = self.start("up", "agentplane-t", env={"STUB_CREATE_SLEEP": "60"})
                self.wait_for_log("current", "kind-agentplane-t", child)       # kind create switched the context and is now busy
                self.assertTrue(self.owner.interrupt(child, signum))
                out = self.wait(child)
                self.assertEqual(child.state, op.State.REAPED, out)
                self.assertEqual(child.exit_code, code, out)
                self.assertIn("interrupted by signal %d" % signum, out)
                self.assertEqual(self.get("current").strip(), PRIOR, "the context must be restored even when interrupted")
                self.assertEqual(self.root_entries(), [], "the owned directory must be removed even when interrupted")
                self.assertEqual(self.calls("kind delete"), [], "an interrupt must never delete a cluster")


class WorkdirTests(KindCase):
    def test_a_workdir_replaced_mid_run_is_retained_and_the_replacements_decoy_survives(self):
        hook = os.path.join(self.tmp, "replace-hook")
        self.write_exec(os.path.join(self.bin, "kind"), KIND.replace('"get clusters") cat "$ST/clusters" ;;', '''"get clusters")
    leaf="$(ls "$STUB_ROOT" | grep '^kind-cluster\\.' | head -1)"
    mv "$STUB_ROOT/$leaf" "$STUB_ROOT/$leaf.moved"; mkdir "$STUB_ROOT/$leaf"; echo decoy >"$STUB_ROOT/$leaf/decoy"
    cat "$ST/clusters" ;;'''))
        child, out = self.run_kind("up", "agentplane-t", env={"STUB_ROOT": self.roottmp})
        self.assertNotEqual(child.exit_code, 0, out)
        names = self.root_entries()
        moved = [n for n in names if n.endswith(".moved")]
        replacement = [n for n in names if n.startswith("kind-cluster.") and not n.endswith(".moved")]
        self.assertEqual((len(moved), len(replacement)), (1, 1), names)
        with open(os.path.join(self.roottmp, replacement[0], "decoy"), "rb") as fh:
            self.assertEqual(fh.read(), b"decoy\n", "the replacement's decoy was touched")
        self.assertTrue(os.listdir(os.path.join(self.roottmp, moved[0])), "the original's captures are retained for inspection")
        self.assertIn("replaced", out)
        self.assertEqual(self.get("current").strip(), PRIOR, "the context is still restored when cleanup is refused")
        self.assertFalse(os.path.exists(hook))

    def test_an_unknown_file_planted_in_the_workdir_blocks_cleanup_and_is_kept(self):
        self.write_exec(os.path.join(self.bin, "kind"), KIND.replace('"get clusters") cat "$ST/clusters" ;;', '''"get clusters")
    leaf="$(ls "$STUB_ROOT" | grep '^kind-cluster\\.' | head -1)"; echo planted >"$STUB_ROOT/$leaf/surprise.txt"
    cat "$ST/clusters" ;;'''))
        child, out = self.run_kind("up", "agentplane-t", env={"STUB_ROOT": self.roottmp})
        self.assertNotEqual(child.exit_code, 0, out)
        self.assertIn("unknown entries", out)
        dirs = self.root_entries()
        self.assertEqual(len(dirs), 1)
        self.assertTrue(os.path.exists(os.path.join(self.roottmp, dirs[0], "surprise.txt")))
        self.assertEqual(self.get("current").strip(), PRIOR)

    def test_a_manifest_file_replaced_by_a_symlink_is_refused_and_its_target_survives(self):
        target = os.path.join(self.tmp, "target.txt")
        with open(target, "wb") as fh:
            fh.write(DECOY)
        self.write_exec(os.path.join(self.bin, "kind"), KIND.replace('"get clusters") cat "$ST/clusters" ;;', '''"get clusters")
    leaf="$(ls "$STUB_ROOT" | grep '^kind-cluster\\.' | head -1)"
    rm -f "$STUB_ROOT/$leaf/"*-docker-info.out; ln -s "$STUB_TARGET" "$STUB_ROOT/$leaf/01-docker-info.out"
    cat "$ST/clusters" ;;'''))
        child, out = self.run_kind("up", "agentplane-t", env={"STUB_ROOT": self.roottmp, "STUB_TARGET": target})
        self.assertNotEqual(child.exit_code, 0, out)
        with open(target, "rb") as fh:
            self.assertEqual(fh.read(), DECOY)
        self.assertEqual(len(self.root_entries()), 1, "the directory must be retained")

    def test_retargeting_the_tmpdir_alias_mid_run_does_not_redirect_cleanup(self):
        real, other = os.path.join(self.tmp, "real"), os.path.join(self.tmp, "other")
        os.mkdir(real)
        os.mkdir(other)
        alias = os.path.join(self.tmp, "alias")
        os.symlink(real, alias)
        self.write_exec(os.path.join(self.bin, "kind"), KIND.replace('"get clusters") cat "$ST/clusters" ;;', '''"get clusters")
    leaf="$(ls "$STUB_REAL" | grep '^kind-cluster\\.' | head -1)"
    mkdir "$STUB_OTHER/$leaf"; echo decoy >"$STUB_OTHER/$leaf/decoy"
    ln -sfn "$STUB_OTHER" "$STUB_ALIAS"
    cat "$ST/clusters" ;;'''))
        env = {"TMPDIR": alias, "STUB_REAL": real, "STUB_OTHER": other, "STUB_ALIAS": alias}
        child = self.start("up", "agentplane-t", env=env)
        out = self.wait(child)
        self.assertEqual(child.exit_code, 0, out)
        self.assertEqual(os.listdir(real), [], "the original directory is removed through its held descriptors")
        leaf = os.listdir(other)[0]
        with open(os.path.join(other, leaf, "decoy"), "rb") as fh:
            self.assertEqual(fh.read(), b"decoy\n")


class OwnershipTests(KindCase):
    def run_in_process(self, adapter, *argv):
        saved = (op.PosixAdapter, tempfile.tempdir, dict(os.environ))
        op.PosixAdapter = adapter
        tempfile.tempdir = self.roottmp
        os.environ.update(PATH=self.bin + os.pathsep + os.environ["PATH"], ST=self.st, STUB_NODES=self.nodes,
                          STUB_SHAPE_SRC=self.shape, KINDSHAPE_BIN=self.shape)
        out, err = io.StringIO(), io.StringIO()
        try:
            rc = kind_cluster.main(list(argv), out=out, err=err)
        finally:
            op.PosixAdapter, tempfile.tempdir = saved[0], saved[1]
            os.environ.clear()
            os.environ.update(saved[2])
        return rc, out.getvalue() + err.getvalue()

    def test_a_lost_child_fails_the_run_and_keeps_the_workdir_without_signalling_it(self):
        kills = []

        class Lost(op.PosixAdapter):
            def waitpid_nohang(self, pid):
                raise ChildProcessError(errno.ECHILD, "No child processes")     # every wait says "not our child"

            def kill(self, pid, sig):
                kills.append((pid, sig))
                raise AssertionError("a lost child must never be signalled")
        rc, out = self.run_in_process(Lost, "down", "agentplane-t", "--command-timeout", "5")
        self.assertNotEqual(rc, 0, out)
        self.assertEqual(kills, [])
        self.assertIn("lost ownership", out)
        self.assertIn("was kept for inspection", out)
        self.assertEqual(len(self.root_entries()), 1, "a lost child keeps the workdir")

    def fd_count(self):
        return len(os.listdir("/dev/fd"))

    def run_failing_setup(self, patch_name, patch):
        """Run the owner in process with one workdir step made to fail; return fd counts around 3 repeats."""
        if not os.path.isdir("/dev/fd"):
            self.skipTest("no /dev/fd")
        import owned_workdir as ow
        real = getattr(ow.Workdir, patch_name)
        counts = []
        try:
            setattr(ow.Workdir, patch_name, patch(real))
            rc, out = self.run_in_process(op.PosixAdapter, "up", "agentplane-t")           # warm-up
            counts.append(self.fd_count())
            for _ in range(3):
                self.reset(PRIOR)
                rc, out = self.run_in_process(op.PosixAdapter, "up", "agentplane-t")
                counts.append(self.fd_count())
        finally:
            setattr(ow.Workdir, patch_name, real)
        return rc, out, counts

    def test_a_failing_stderr_capture_create_closes_the_stdout_descriptor_already_acquired(self):
        def patch(real):
            def create_file(self_, name):
                if name.endswith(".err"):
                    raise OSError(errno.ENOSPC, "injected: cannot create the stderr capture")
                return real(self_, name)
            return create_file
        rc, out, counts = self.run_failing_setup("create_file", patch)
        self.assertNotEqual(rc, 0, out)
        self.assertEqual(len(set(counts)), 1, "descriptors leaked across failing runs: %r" % counts)

    def test_a_failing_stdin_open_closes_the_stdout_and_stderr_descriptors_already_acquired(self):
        def patch(real):
            def open_read(self_, name):
                raise OSError(errno.EMFILE, "injected: cannot open the stdin file")
            return open_read
        rc, out, counts = self.run_failing_setup("open_read", patch)
        self.assertNotEqual(rc, 0, out)
        self.assertEqual(len(set(counts)), 1, "descriptors leaked across failing runs: %r" % counts)
        self.assertIn("unexpected OSError", out)

    def test_closed_output_never_skips_cleanup_and_fails_the_run(self):
        class Broken:
            def write(self, text):
                raise BrokenPipeError(errno.EPIPE, "Broken pipe")

            def flush(self):
                raise BrokenPipeError(errno.EPIPE, "Broken pipe")
        saved = (tempfile.tempdir, dict(os.environ))
        tempfile.tempdir = self.roottmp
        os.environ.update(PATH=self.bin + os.pathsep + os.environ["PATH"], ST=self.st, STUB_NODES=self.nodes,
                          STUB_SHAPE_SRC=self.shape, KINDSHAPE_BIN=self.shape)
        try:
            rc = kind_cluster.main(["up", "agentplane-t"], out=Broken(), err=Broken())
        finally:
            tempfile.tempdir = saved[0]
            os.environ.clear()
            os.environ.update(saved[1])
        self.assertEqual(rc, 1, "a run whose diagnostics could not be written must not report success")
        self.assertEqual(self.root_entries(), [], "cleanup still ran")
        self.assertEqual(self.get("current").strip(), PRIOR)

    def test_the_wrapper_validates_and_execs_the_python_owner(self):
        with open(os.path.join(REPO, "scripts", "kind-cluster.sh")) as fh:
            text = fh.read()
        self.assertIn("exec python3 -I -B scripts/kind_cluster.py", text)
        for forbidden in ("kill ", "trap ", "owned-process.sh", "op_", "rm -rf", " &\n"):
            self.assertNotIn(forbidden, text)
        child = self.owner.spawn(["bash", os.path.join(REPO, "scripts", "kind-cluster.sh"), "sideways", "x"],
                                 os.path.join(self.tmp, "wrapper.log"))
        out = self.wait(child)
        self.assertEqual(child.exit_code, 2, out)
        self.assertIn("usage:", out)


if __name__ == "__main__":
    unittest.main()
