"""Foreground smoke runner for the deterministic mock model (P0-T5 supervisor revision, commit 3).

Replaces the Bash scripts/mock-smoke.sh. It builds the binary, starts disposable servers on
OS-assigned loopback ports, waits for readiness with timeouts, checks the same things the Bash
script checked (byte-identical plain responses, well-framed stream that agrees with the plain
response, a 400 for bad input, a deterministic 500 with --error-rate=1), then stops exactly the
processes it started and removes exactly the directory it created.

Ownership (ADR-007): this single-threaded foreground process is the parent, the sole waiter and the
sole signaller of every child (the go build and each server), through scripts/lib/owned_process.py.
There is no shell, no PID, no ps lookup, no process-group signal and no PID file. INT, TERM and HUP
only set a flag; the flag is checked between steps and in every wait loop, so an interrupt always
reaches the common cleanup. Exit codes: 0 success, 1 failure, 130 INT, 143 TERM, 129 HUP; a cleanup
failure never turns an interrupt or an earlier failure into success.

Workdir (ADR-007 follow-up): scripts/lib/owned_workdir.py owns a private directory through held
descriptors; only the manifest files (the build log, the compiled binary, each server's log) and
the empty directory are removed, and any surprise retains the directory and fails the run. It is
removed only after every child has been REAPED; if one is lost or uncollected the directory is kept.

Limits (accepted in ADR-007): if THIS process is SIGKILLed or crashes, children can outlive it and
nothing here finds them again; the Go build tool's own descendants are not given a kill guarantee
(the build is bounded by --build-timeout and the direct `go` child is stopped, its compiler
children are not tracked). Only loopback is contacted. Needs go (unless --server-cmd is given).
"""
import argparse
import http.client
import json
import os
import re
import socket
import sys
import time
import traceback

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
sys.path.insert(0, os.path.join(HERE, "lib"))
import owned_process as op  # noqa: E402
import owned_workdir as ow  # noqa: E402

MAX_BODY = 1 << 20
REQ = {"model": "mock-large", "messages": [{"role": "user", "content": "Summarize this document please."}]}
REQ_STREAM = dict(REQ, stream=True)


class SmokeFailure(Exception):
    pass


class Interrupted(Exception):
    pass


class Smoke:
    def __init__(self, args, out):
        self.args = args
        self.out = out
        self.owner = None
        self.wd = None
        self.servers = {}

    # ---- plumbing -----------------------------------------------------------------------
    def say(self, msg):
        print("mock-smoke: " + msg, file=self.out, flush=True)

    def fail(self, msg):
        raise SmokeFailure(msg)

    def check_interrupt(self):
        if self.owner.interrupted is not None:
            raise Interrupted()

    def wait_until(self, deadline_seconds, predicate, step=0.05):
        """Poll predicate until true or the deadline; returns the final truth. Honors the stop flag."""
        end = time.monotonic() + deadline_seconds
        while True:
            self.check_interrupt()
            if predicate():
                return True
            if time.monotonic() >= end:
                return False
            time.sleep(step)

    def spawn(self, name, argv):
        log_fd = self.wd.create_file(name + ".log")
        try:
            child = self.owner.spawn(argv, log_fd=log_fd, name=name)
        finally:
            os.close(log_fd)
        return child

    # ---- build and start ----------------------------------------------------------------
    def build(self):
        if self.args.server_cmd:
            return list(self.args.server_cmd)
        binary = self.wd.file_path("mockmodel")
        self.say("building")
        child = self.spawn("build", ["go", "build", "-o", binary, "./cmd/mockmodel"])
        if not self.wait_until(self.args.build_timeout, lambda: self.owner.poll(child) is not None or child.done, 0.1):
            self.owner.shutdown(self.args.term_seconds, self.args.kill_seconds, children=[child])
            self.fail("go build did not finish within %.0f s (its compiler children are not tracked): %s" % (
                self.args.build_timeout, self.owner.log_tail(child, 2000).strip()))
        if child.state != op.State.REAPED or child.exit_code != 0:
            self.fail("go build failed (state %s, exit %s): %s" % (child.state, child.exit_code, self.owner.log_tail(child, 2000).strip()))
        self.wd.adopt_file("mockmodel")
        return [binary]

    def start_server(self, name, prefix, extra=()):
        argv = list(prefix) + ["--listen", "127.0.0.1:0"] + list(extra)
        child = self.spawn(name, argv)
        self.servers[name] = child
        addr = {"v": ""}

        def have_address():
            self.owner.poll(child)
            if child.done:
                self.fail("%s exited early (status %s): %s" % (name, child.exit_code, self.owner.log_tail(child, 2000).strip()))
            m = re.search(r"^mockmodel listening on (\S+)$", self.owner.log_tail(child, 4096), re.M)
            addr["v"] = m.group(1) if m else ""
            return bool(addr["v"])

        if not self.wait_until(self.args.address_timeout, have_address):
            self.fail("%s never reported its address" % name)
        host, _, port = addr["v"].rpartition(":")
        if host != "127.0.0.1" or not port.isdigit() or int(port) == 0:
            self.fail("%s listens on %s, want 127.0.0.1 and an assigned port" % (name, addr["v"]))

        def ready():
            self.owner.poll(child)
            if child.done:
                self.fail("%s exited before it was ready (status %s)" % (name, child.exit_code))
            try:
                status, _ = self.http(addr["v"], "GET", "/readyz", None, timeout=2)
                return status == 200
            except (OSError, http.client.HTTPException):
                return False

        if not self.wait_until(self.args.ready_timeout, ready, 0.1):
            self.fail("%s never became ready" % name)
        self.say("%s ready on %s" % (name, addr["v"]))
        return addr["v"]

    # ---- bounded loopback HTTP ----------------------------------------------------------
    def http(self, addr, method, path, body, timeout=10):
        host, _, port = addr.rpartition(":")
        if host != "127.0.0.1":
            self.fail("refusing to contact %s: only 127.0.0.1 is allowed" % addr)
        conn = http.client.HTTPConnection(host, int(port), timeout=timeout)
        try:
            headers = {"Content-Type": "application/json"} if body is not None else {}
            conn.request(method, path, body=body, headers=headers)
            resp = conn.getresponse()
            data = resp.read(MAX_BODY + 1)
            if len(data) > MAX_BODY:
                self.fail("response to %s exceeded %d bytes" % (path, MAX_BODY))
            return resp.status, data
        finally:
            conn.close()

    def post(self, addr, payload):
        body = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
        return self.http(addr, "POST", "/v1/chat/completions", body)

    # ---- the checks (the same assertions the Bash script made) ---------------------------
    def check_identical(self, addr):
        s1, r1 = self.post(addr, REQ)
        s2, r2 = self.post(addr, REQ)
        if s1 != 200:
            self.fail("first request was not 200 (got %s)" % s1)
        if s2 != 200:
            self.fail("second request was not 200 (got %s)" % s2)
        if r1 != r2:
            self.fail("identical requests gave different bytes")
        self.say("two identical requests gave byte-identical responses (%d bytes)" % len(r1))
        return r1

    def check_stream(self, addr, plain_bytes):
        s1, a = self.post(addr, REQ_STREAM)
        s2, b = self.post(addr, REQ_STREAM)
        if s1 != 200:
            self.fail("stream request was not 200 (got %s)" % s1)
        if s2 != 200:
            self.fail("second stream request was not 200 (got %s)" % s2)
        if a != b:
            self.fail("identical stream requests gave different bytes")
        plain = json.loads(plain_bytes)
        raw = a.decode("utf-8")
        try:
            assert raw.endswith("\n\n"), "stream must end with a blank line"
            events = []
            for block in raw[:-2].split("\n\n"):
                assert block.startswith("data: ") and "\n" not in block, "malformed SSE block: %r" % block
                events.append(block[len("data: "):])
            assert events[-1] == "[DONE]", "last event must be [DONE]"
            assert events.count("[DONE]") == 1, "[DONE] must appear exactly once"
            chunks = [json.loads(e) for e in events[:-1]]
            usage = [c for c in chunks if "usage" in c]
            assert len(usage) == 1 and chunks[-1] is usage[0], "exactly one usage chunk, immediately before [DONE]"
            assert usage[0]["choices"] == [], "the usage chunk must have empty choices"
            content = "".join(ch["delta"].get("content", "") for c in chunks for ch in c["choices"])
            finish = [ch["finish_reason"] for c in chunks for ch in c["choices"] if ch["finish_reason"]]
            assert content == plain["choices"][0]["message"]["content"], "stream content differs from the plain response"
            assert usage[0]["usage"] == plain["usage"], "stream usage differs from the plain response"
            assert finish == [plain["choices"][0]["finish_reason"]], "finish reason differs"
            assert all(c["id"] == plain["id"] for c in chunks), "chunk ids differ from the plain response id"
        except (AssertionError, KeyError, ValueError) as exc:
            self.fail("stream check failed: %s" % exc)
        self.say("stream ok (%d events, usage %s, finish %s)" % (len(events), json.dumps(usage[0]["usage"]), finish[0]))

    def check_invalid(self, addr):
        status, body = self.post(addr, {"model": "m", "messages": [], "tenant": "acme"})
        if status != 400:
            self.fail("invalid request gave %s, want 400" % status)
        if b'"code":"invalid_request"' not in body:
            self.fail("invalid request lacks the invalid_request error")
        self.say("invalid request rejected with 400")

    def check_failing(self, addr):
        status, body = self.post(addr, REQ)
        if status != 500:
            self.fail("--error-rate=1 gave %s, want 500" % status)
        if b'"code":"internal_error"' not in body:
            self.fail("simulated failure lacks the internal_error envelope")
        self.say("--error-rate=1 fails deterministically with 500")

    def checks(self):
        prefix = self.build()
        main_addr = self.start_server("main", prefix)
        self.main_addr = main_addr
        plain = self.check_identical(main_addr)
        self.check_stream(main_addr, plain)
        self.check_invalid(main_addr)
        self.check_interrupt()
        fail_addr = self.start_server("failing", prefix, ["--error-rate=1"])
        self.check_failing(fail_addr)

    # ---- the single cleanup path ---------------------------------------------------------
    def stop_and_verify(self, failures):
        """TERM to every still-owned child, shared graceful interval, KILL to survivors, reap. Success
        needs every child REAPED with status 0 and no escalation."""
        report = self.owner.shutdown(self.args.term_seconds, self.args.kill_seconds)
        failures.extend(report.failures)
        return report

    def run(self):
        failures = []
        interrupted = False
        crashed = None
        main_addr = None
        try:
            self.checks()
            main_addr = self.main_addr
        except Interrupted:
            interrupted = True
        except SmokeFailure as exc:
            failures.append(str(exc))
        except BaseException as exc:                       # an ordinary exception still reaches cleanup
            crashed = exc
            failures.append("unexpected %s: %s" % (type(exc).__name__, exc))
            self.say("unexpected error:\n" + "".join(traceback.format_exception(type(exc), exc, exc.__traceback__)))
        report = self.stop_and_verify(failures)
        if not failures and not interrupted and main_addr is not None:
            try:
                self.http(main_addr, "GET", "/readyz", None, timeout=2)
                failures.append("the main server still answers after shutdown")
            except (ConnectionRefusedError, socket.timeout, OSError, http.client.HTTPException):
                pass
        children = self.owner.children
        all_reaped = all(c.state == op.State.REAPED for c in children)
        self.say("children: " + (", ".join("%s=%s(%s)" % (c.name, c.state, c.exit_code) for c in children) or "none"))
        self.owner.close()
        if self.wd is not None:
            if all_reaped:
                result = self.wd.cleanup()
                if not result.ok:
                    failures.extend(result.failures)
            else:
                failures.append("some child is lost or uncollected, so the workdir %s was kept for inspection" % self.wd.path)
                self.wd._close_all()
        if failures:
            for f in failures:
                self.say("FAIL: " + f)
        if interrupted or self.owner.interrupted is not None:
            self.say("interrupted by signal %d; cleanup %s" % (self.owner.interrupted, "failed" if failures else "complete"))
            return self.owner.interrupt_exit_code
        if failures:
            return 1
        self.say("both servers exited 0 on SIGTERM and every owned child was reaped")
        self.say("OK")
        return 0


def parse_args(argv):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--server-cmd", type=json.loads, default=None,
                   help="JSON list: the command prefix to run as the mock server instead of building one (tests)")
    p.add_argument("--build-timeout", type=float, default=600.0)
    p.add_argument("--address-timeout", type=float, default=15.0)
    p.add_argument("--ready-timeout", type=float, default=10.0)
    p.add_argument("--term-seconds", type=float, default=op.DEFAULT_TERM_SECONDS)
    p.add_argument("--kill-seconds", type=float, default=op.DEFAULT_KILL_SECONDS)
    args = p.parse_args(argv)
    if args.server_cmd is not None and (not isinstance(args.server_cmd, list) or not args.server_cmd
                                        or not all(isinstance(a, str) for a in args.server_cmd)):
        p.error("--server-cmd must be a non-empty JSON list of strings")
    return args


def main(argv=None, out=None):
    args = parse_args(sys.argv[1:] if argv is None else argv)
    out = out or sys.stdout
    os.chdir(REPO)
    smoke = Smoke(args, out)
    try:
        smoke.wd = ow.Workdir.create("mock-smoke")
    except (ow.WorkdirError, OSError) as exc:
        print("mock-smoke: ERROR: cannot create the workdir: %s" % exc, file=sys.stderr)
        return 1
    try:
        smoke.owner = op.Owner()
    except BaseException:
        smoke.wd.cleanup()
        raise
    smoke.owner.install_signal_handlers()
    print("mock-smoke: handlers installed", file=out, flush=True)
    try:
        return smoke.run()
    finally:
        smoke.owner.restore_signal_handlers()


if __name__ == "__main__":
    sys.exit(main())
