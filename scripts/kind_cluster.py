"""Create or delete the named local kind cluster safely, under a persistent owner (ADR-007 follow-up).

    scripts/kind-cluster.sh up   NAME   create it (labeled topology) or accept a compatible one
    scripts/kind-cluster.sh down NAME   delete exactly that named cluster

The wrapper scripts/kind-cluster.sh validates the arguments and execs this file. Everything here
is ONE foreground process that owns, for its whole life, (a) every tool it starts (docker, kind,
kubectl, go and the topology checker), through scripts/lib/owned_process.py, and (b) one private
temporary directory, through the held descriptors of scripts/lib/owned_workdir.py. It returns 0
only when the operation succeeded AND the user's kubectl current context was restored and read
back AND every child was reaped AND the owned workdir was removed. Any failure returns nonzero
and retains what is needed to inspect it. This replaces the Bash script and its temporary
"retain the directory and exit 3" policy.

Guarantees (tested with stub tools by scripts/test-kind-cluster.sh and scripts/tests/test_kind_cluster.py):
  * Every cluster is addressed by an explicit --name and every kubectl call that touches a cluster by an
    explicit --context kind-NAME. The current kubectl context is never used for an operation, and it
    is put back (and verified by reading it back) after anything that changes it.
  * An existing cluster is never recreated or deleted by `up`. It is accepted only if its nodes match
    deploy/kind/config.yaml; otherwise `up` fails with recovery instructions.
  * `down` deletes only the cluster named, and only if it exists.
  * A context read is only trusted if it SUCCEEDED (an empty result is "no current context" only when
    the read exited 0; an error is never inferred to mean empty).
  * Nothing is written into the repository and no kubeconfig content is printed, copied or persisted.
  * No shell, no PID, no ps, no process group: every command is argv; its stdin is the null device or
    a regular file; its stdout and stderr go to regular files in the owned workdir, which are echoed
    (bounded) to this process's terminal as they grow. Each command has an absolute deadline; an
    interrupt (INT, TERM, HUP: flag only) stops the current command through the bounded TERM, KILL,
    reap sequence and still runs the context restoration and cleanup.

Exit codes: 0 success; 1 failure (a tool's own nonzero status is used when it is 1..255); 2 usage;
130/143/129 for INT/TERM/HUP. A cleanup failure never turns an earlier failure or an interrupt into
success. Needs python3 (3.8+); the topology checker is built with go unless KINDSHAPE_BIN is set.
"""
import argparse
import math
import os
import re
import shutil
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
sys.path.insert(0, os.path.join(HERE, "lib"))
import owned_process as op  # noqa: E402
import owned_workdir as ow  # noqa: E402

NAME_RE = re.compile(r"^[a-z0-9]([a-z0-9-]*[a-z0-9])?$")
MAX_CAPTURE = 1 << 20
MAX_ECHO = 4 << 20
# Absolute per-command deadlines (seconds); --command-timeout overrides them all (used by the tests).
TIMEOUTS = {"docker-info": 60.0, "kind-version": 60.0, "kubectl": 60.0, "kind-get-clusters": 120.0, "kind-delete": 600.0,
            "kind-export": 120.0, "kind-create": 1200.0, "go-build": 600.0, "kindshape": 60.0}


class Die(Exception):
    def __init__(self, message, code=1):
        super().__init__(message)
        self.code = code


class Interrupted(Exception):
    pass


class Result:
    def __init__(self, child, stdout, stderr):
        self.state, self.code = child.state, child.exit_code
        self.stdout, self.stderr = stdout, stderr

    @property
    def ok(self):
        return self.state == op.State.REAPED and self.code == 0

    @property
    def status(self):
        """The status to exit with when this command failed and the script has no message of its own."""
        return self.code if self.state == op.State.REAPED and isinstance(self.code, int) and 1 <= self.code <= 255 else 1


def one_line(text):
    return text.replace("\n", " ")


class Kind:
    def __init__(self, mode, name, args, out, err):
        self.mode, self.name, self.args = mode, name, args
        self.ctx = "kind-" + name
        self.out, self.err = out, err
        self.output_failed = False
        self.owner = None
        self.wd = None
        self.counter = 0
        self.echoed = {}
        self.prior = self.want = ""
        self.prior_known = False
        self.ctx_now = ""
        self.ctx_err = ""
        self.shape_path = None

    # ---- output: never allowed to interrupt cleanup --------------------------------------
    def say(self, msg):
        self._write(self.out, msg + "\n")

    def warn(self, msg):
        self._write(self.err, msg + "\n")

    def _write(self, stream, text):
        try:
            stream.write(text)
            stream.flush()
        except (OSError, ValueError):
            self.output_failed = True

    def die(self, msg, code=1):
        raise Die("kind-cluster: ERROR: " + msg, code)

    def check_interrupt(self):
        if self.owner.interrupted is not None:
            raise Interrupted()

    # ---- commands ------------------------------------------------------------------------
    def timeout_for(self, label):
        return self.args.command_timeout if self.args.command_timeout else TIMEOUTS[label.split(":")[0]]

    def run(self, label, argv, *, stdin_from=None, passthrough=False, split_stderr=True, honor_interrupt=True):
        """Run one argv as a direct child with regular-file IO and an absolute deadline. Returns a Result.
        stdout (and stderr, unless split_stderr is False) are captured in the workdir; with passthrough
        they are also echoed, bounded, to the terminal while the command runs."""
        self.counter += 1
        stem = "%02d-%s" % (self.counter, re.sub(r"[^a-z0-9]+", "-", label.lower()).strip("-"))
        out_fd = self.wd.create_file(stem + ".out")
        err_fd = self.wd.create_file(stem + ".err") if split_stderr else None
        in_fd = self.wd.open_read(stdin_from) if stdin_from else None
        try:
            child = self.owner.spawn(argv, log_fd=out_fd, name=label, stdin_fd=in_fd, stderr_fd=err_fd)
        finally:
            for fd in (out_fd, err_fd, in_fd):
                if fd is not None:
                    os.close(fd)
        self.echoed[child] = {"stdout": 0, "stderr": 0}
        import time
        deadline = time.monotonic() + self.timeout_for(label)
        try:
            while True:
                self.owner.poll(child)
                if passthrough:
                    self.pump(child)
                if child.done:
                    break
                if honor_interrupt and self.owner.interrupted is not None:
                    self.owner.shutdown(self.args.term_seconds, self.args.kill_seconds, children=[child])
                    raise Interrupted()
                if time.monotonic() >= deadline:
                    self.owner.shutdown(self.args.term_seconds, self.args.kill_seconds, children=[child])
                    if passthrough:
                        self.pump(child)
                    self.die("%s did not finish within %.0f s and was stopped%s" % (
                        " ".join(argv[:3]), self.timeout_for(label), "" if child.done else "; it could not be collected, so cleanup is incomplete"))
                time.sleep(0.02)
        finally:
            if passthrough:
                self.pump(child)
        stdout = self.owner.log_tail(child, MAX_CAPTURE)
        stderr = self.owner.log_tail(child, MAX_CAPTURE, stream="stderr") if split_stderr else ""
        return Result(child, stdout, stderr)

    def pump(self, child):
        """Echo newly captured output to the terminal, bounded."""
        for stream, dest in (("stdout", self.out), ("stderr", self.err)):
            if stream == "stderr" and child._err_fd is None:
                continue
            offset = self.echoed[child][stream]
            if offset >= MAX_ECHO:
                continue
            data = self.owner.log_read(child, offset, min(65536, MAX_ECHO - offset), stream=stream)
            if data:
                self.echoed[child][stream] = offset + len(data)
                self._write(dest, data.decode("utf-8", "replace"))

    # ---- the current kubectl context (review r2, finding 3) --------------------------------
    def ctx_read(self, honor_interrupt=True):
        """Sets ctx_now and returns True only if the read SUCCEEDED (ctx_now "" then means positively
        'no current context'); returns False with ctx_err set if it failed."""
        self.ctx_now = self.ctx_err = ""
        r = self.run("kubectl:ctx", ["kubectl", "config", "view", "-o", "jsonpath={.current-context}"], honor_interrupt=honor_interrupt)
        if not r.ok:
            self.ctx_err = one_line(r.stderr)
            return False
        self.ctx_now = r.stdout.rstrip("\n")
        return True

    def restore_context(self):
        """Make the current context exactly self.want again and VERIFY it by reading it back."""
        if not self.ctx_read(honor_interrupt=False):
            self.warn("kind-cluster: ERROR: cannot read your kubectl current context to verify it (%s); it may no longer be '%s'. Check it: kubectl config view -o 'jsonpath={.current-context}'" % (self.ctx_err, self.want or "<none>"))
            return False
        if self.ctx_now == self.want:
            return True
        if self.want:
            r = self.run("kubectl:use-context", ["kubectl", "config", "use-context", self.want], honor_interrupt=False)
        else:
            r = self.run("kubectl:unset", ["kubectl", "config", "unset", "current-context"], honor_interrupt=False)
        err = r.stderr.rstrip("\n")
        if not self.ctx_read(honor_interrupt=False):        # trust the read, never the exit status of use-context or unset
            self.warn("kind-cluster: ERROR: restored your kubectl current context but cannot read it back to verify it (%s); it should be '%s'." % (self.ctx_err, self.want or "<none>"))
            return False
        if self.ctx_now == self.want:
            return True
        self.warn("kind-cluster: ERROR: could not restore your kubectl current context: it was '%s' and it is now '%s'%s." % (
            self.want or "<none>", self.ctx_now or "<none>", " (%s)" % err if err else ""))
        if self.want:
            self.warn("  Restore it yourself: kubectl config use-context '%s'" % self.want)
        else:
            self.warn("  Unset it yourself: kubectl config unset current-context")
        return False

    # ---- the operation ---------------------------------------------------------------------
    def preflight(self):
        for tool in ("docker", "kind", "kubectl"):
            if shutil.which(tool) is None:
                self.die("%s not found. Install it (see docs/local-development.md)." % tool)
        if not self.run("docker-info", ["docker", "info"]).ok:
            self.die("the Docker daemon is not reachable. Start Docker and retry.")
        r = self.run("kind-version", ["kind", "version"])
        m = re.match(r"^kind v([0-9][0-9.]*)", r.stdout.split("\n", 1)[0]) if r.ok else None
        have = m.group(1) if m else ""
        if have != self.args.kind_version:
            self.die("kind %s is required (found '%s'): the node image pinned in %s is only supported by that release. See docs/local-development.md." % (
                self.args.kind_version, have or "unknown", self.args.kind_config))

    def operate(self):
        self.preflight()
        # Establish the prior state BEFORE anything is created, exported or deleted.
        if not self.ctx_read():
            self.die("cannot establish your current kubectl context (%s); refusing to change anything. Fix your kubeconfig first: kubectl config view" % self.ctx_err)
        self.prior = self.want = self.ctx_now
        self.prior_known = True
        r = self.run("kind-get-clusters", ["kind", "get", "clusters"], split_stderr=False)
        if not r.ok:
            self.die("'kind get clusters' failed; refusing to guess whether '%s' exists: %s" % (self.name, r.stdout.rstrip("\n")))
        exists = self.name in r.stdout.split("\n")
        if self.mode == "down":
            return self.down(exists)
        return self.up(exists)

    def tool_failed(self, r, what):
        """A command whose failure the Bash script left to `set -e`: exit with its own status."""
        raise Die("kind-cluster: ERROR: %s failed (status %s)" % (what, r.code), r.status)

    def down(self, exists):
        if not exists:
            self.say("kind cluster '%s' does not exist; nothing to do." % self.name)
            return
        r = self.run("kind-delete", ["kind", "delete", "cluster", "--name", self.name], passthrough=True)
        if not r.ok:
            self.tool_failed(r, "kind delete cluster")
        # Deleting a cluster also removes its own context entries, so if that was the current context
        # there is nothing to go back to; any other current context must be unchanged.
        if self.prior == self.ctx:
            self.want = ""

    def build_shape(self):
        if shutil.which("go") is None:
            self.die("go not found (it builds the topology checker).")
        if self.args.kindshape_bin:
            return self.args.kindshape_bin
        path = self.wd.file_path("kindshape")
        r = self.run("go-build", ["go", "build", "-o", path, "./cmd/kindshape"], passthrough=True)
        if not r.ok:
            self.die("could not build the topology checker.")
        self.wd.adopt_file("kindshape")
        return path

    def check_shape(self):
        """0 if the cluster's nodes match the config; 2 if its nodes could not be read; the checker's own
        status otherwise (as the Bash script's pipeline returned it)."""
        r = self.run("kubectl:nodes", ["kubectl", "--context", self.ctx, "get", "nodes", "-o", "json"])
        if not r.ok:
            self.warn("kind-cluster: cannot read the nodes of '%s' through context '%s': %s" % (self.name, self.ctx, r.stderr.rstrip("\n")))
            return 2
        nodes_file = "%02d-kubectl-nodes.out" % self.counter
        c = self.run("kindshape:nodes", [self.shape_path, "check-nodes", "--config", self.args.kind_config], stdin_from=nodes_file, passthrough=True)
        return c.status if not c.ok else 0

    def recovery(self):
        self.warn("kind-cluster: the existing cluster '%s' was NOT changed.\n"
                  "  To rebuild it with the supported topology, an owner-approved step that DELETES the cluster and its workloads:\n"
                  "    make kind-down KIND_CLUSTER_NAME=%s && make kind-up KIND_CLUSTER_NAME=%s\n"
                  "  Or choose another name, e.g. make kind-up KIND_CLUSTER_NAME=%s-2" % (self.name, self.name, self.name, self.name))

    def up(self, exists):
        self.shape_path = self.build_shape()
        r = self.run("kindshape:config", [self.shape_path, "check-config", "--config", self.args.kind_config], passthrough=True)
        if not r.ok:
            self.die("%s is not the supported topology; not creating anything." % self.args.kind_config)
        if exists:
            # a failed read is not "the context is missing": it must not lead to an export
            r = self.run("kubectl:contexts", ["kubectl", "config", "get-contexts", "-o", "name"])
            if not r.ok:
                self.die("cannot list your kubeconfig contexts (%s); refusing to change anything." % one_line(r.stderr))
            if self.ctx not in r.stdout.split("\n"):
                self.say("kind-cluster: cluster '%s' exists but kubeconfig has no context '%s'; exporting it." % (self.name, self.ctx))
                e = self.run("kind-export", ["kind", "export", "kubeconfig", "--name", self.name], passthrough=True)
                if not e.ok:
                    self.tool_failed(e, "kind export kubeconfig")
            rc = self.check_shape()
            if rc == 0:
                self.say("kind cluster '%s' already exists and matches the supported topology; nothing to do." % self.name)
                return
            if rc == 2:
                self.die("refusing to recreate '%s' because its state could not be read." % self.name)
            self.recovery()
            raise Die("", 1)
        c = self.run("kind-create", ["kind", "create", "cluster", "--name", self.name, "--config", self.args.kind_config], passthrough=True)
        if not c.ok:
            self.tool_failed(c, "kind create cluster")
        if self.check_shape() != 0:
            self.die("the new cluster '%s' does not match the supported topology (see above); it was left in place for inspection." % self.name)
        self.say("kind cluster '%s' created; use it with: kubectl --context %s ..." % (self.name, self.ctx))

    # ---- one exit path -----------------------------------------------------------------------
    def main(self):
        failures, state = [], {"code": 0, "interrupted": False}
        try:
            try:
                self.operate()
            except Interrupted:
                state["interrupted"] = True
            except Die as exc:
                if str(exc):
                    self.warn(str(exc))
                state["code"] = exc.code
            except BaseException as exc:
                failures.append("unexpected %s: %s" % (type(exc).__name__, exc))
                state["code"] = 1
        finally:
            self.finish(failures, state)
        return self.exit_code(failures, state)

    def finish(self, failures, state):
        """Stop every child, restore and verify the context, then dispose of the workdir. Each step is
        independent and protected: one failing step never skips the next."""
        try:
            try:
                self.owner.shutdown(self.args.term_seconds, self.args.kill_seconds)
                # A tool's own nonzero status is the script's business (it is checked where the command ran);
                # ownership trouble is not: a lost, uncollected or force-killed child always fails the run.
                for c in self.owner.children:
                    if c.state == op.State.LOST:
                        failures.append("%s: lost ownership (%s); it was not signalled again and may still be running" % (c.name, c.lost_reason))
                    elif c.state != op.State.REAPED:
                        failures.append("%s: not collected within the reap deadline; cleanup is incomplete and it may still be running" % c.name)
                    elif c.escalated:
                        failures.append("%s ignored SIGTERM and had to be killed" % c.name)
            except BaseException as exc:
                failures.append("shutdown raised %s: %s" % (type(exc).__name__, exc))
            try:
                if self.prior_known and not self.restore_context():
                    failures.append("the kubectl current context could not be restored and verified")
                elif self.prior_known and state["code"] == 0 and not state["interrupted"] and not failures:
                    if self.ctx_read(honor_interrupt=False):
                        self.say("kind-cluster: your kubectl current context is '%s' (verified)." % (self.ctx_now or "<none>"))
                    else:
                        self.warn("kind-cluster: ERROR: cannot read your kubectl current context to report it (%s)." % self.ctx_err)
                        failures.append("the kubectl current context could not be read to report it")
            except BaseException as exc:
                failures.append("context restoration raised %s: %s" % (type(exc).__name__, exc))
            finally:
                try:       # restoring ran commands too: stop and collect anything still owned
                    self.owner.shutdown(self.args.term_seconds, self.args.kill_seconds)
                except BaseException as exc:
                    failures.append("final shutdown raised %s: %s" % (type(exc).__name__, exc))
        finally:
            children = self.owner.children
            all_reaped = all(c.state == op.State.REAPED for c in children)
            try:
                self.owner.close()
            except BaseException as exc:
                failures.append("closing capture descriptors raised %s: %s" % (type(exc).__name__, exc))
            finally:
                self.dispose_workdir(failures, all_reaped)

    def dispose_workdir(self, failures, all_reaped):
        if self.wd is None:
            return
        try:
            if all_reaped:
                result = self.wd.cleanup()
                if not result.ok:
                    failures.extend(result.failures)
            else:
                failures.append("some child is lost, uncollected or unregistered, so the private workdir %s was kept for inspection" % self.wd.path)
        except BaseException as exc:
            failures.append("workdir cleanup raised %s: %s" % (type(exc).__name__, exc))
        finally:
            self.wd._close_all()

    def exit_code(self, failures, state):
        for f in failures:
            self.warn("kind-cluster: ERROR: " + f)
        if state["interrupted"] or self.owner.interrupted is not None:
            self.warn("kind-cluster: interrupted by signal %d; cleanup %s" % (self.owner.interrupted, "FAILED" if failures else "complete"))
            return self.owner.interrupt_exit_code
        if state["code"]:
            return state["code"]
        return 1 if failures or self.output_failed else 0


def finite(minimum, inclusive):
    def parse(text):
        try:
            value = float(text)
        except ValueError:
            raise argparse.ArgumentTypeError("%r is not a number" % text)
        if not math.isfinite(value) or value < minimum or (value == minimum and not inclusive):
            raise argparse.ArgumentTypeError("%r must be a finite number %s %g" % (text, ">=" if inclusive else ">", minimum))
        return value
    return parse


def parse_args(argv):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter, usage="kind_cluster.py up|down NAME [options]")
    p.add_argument("mode", choices=("up", "down"))
    p.add_argument("name")
    p.add_argument("--kind-version", default=os.environ.get("KIND_VERSION", "0.31.0"))
    p.add_argument("--kind-config", default=os.environ.get("KIND_CONFIG", "deploy/kind/config.yaml"))
    p.add_argument("--kindshape-bin", default=os.environ.get("KINDSHAPE_BIN", ""))
    p.add_argument("--command-timeout", type=finite(0, False), default=None, help="override every command's absolute deadline (tests)")
    p.add_argument("--term-seconds", type=finite(0, True), default=op.DEFAULT_TERM_SECONDS)
    p.add_argument("--kill-seconds", type=finite(0, True), default=op.DEFAULT_KILL_SECONDS)
    return p.parse_args(argv)


def main(argv=None, out=None, err=None):
    args = parse_args(sys.argv[1:] if argv is None else argv)
    out, err = out or sys.stdout, err or sys.stderr
    if not NAME_RE.match(args.name):
        print("kind-cluster: ERROR: cluster name must be lowercase alphanumerics and '-', not starting or ending with '-' (got '%s')" % args.name, file=err)
        return 1
    os.chdir(REPO)
    kind = Kind(args.mode, args.name, args, out, err)
    try:
        kind.wd = ow.Workdir.create("kind-cluster")
    except (ow.WorkdirError, OSError) as exc:
        kind.warn("kind-cluster: ERROR: cannot create a temp directory: %s" % exc)
        return 1
    try:
        kind.owner = op.Owner()
    except BaseException:
        kind.wd.cleanup()
        raise
    kind.owner.install_signal_handlers()
    try:
        return kind.main()
    finally:
        kind.owner.restore_signal_handlers()


if __name__ == "__main__":
    sys.exit(main())
