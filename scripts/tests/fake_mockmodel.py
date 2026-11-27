"""A tiny deterministic stand-in for cmd/mockmodel, used ONLY by the smoke-runner tests so they need
neither Go nor a build. It speaks just enough of the same protocol: --listen ADDR, the
"mockmodel listening on ADDR" line, /readyz, POST /v1/chat/completions (plain and stream), a 400
invalid_request envelope for bad input and a 500 internal_error envelope with --error-rate=1.

FAKE_MODE injects faults: ignore-term, exit-early, never-ready, no-address, bad-addr, nonzero-exit,
nondeterministic, bad-usage, accept-invalid, ignore-error-rate, trickle (a POST response that drips one
byte per 0.2 s), stall (a POST that never answers), bad-done, usage-first, bad-content, bad-finish, bad-id. A watchdog ends the process after
FAKE_EXIT_AFTER seconds (default 120) so a test that loses its runner can never leave it running. FAKE_NOTE_FILE: when set, the server appends its pid+mode there once ready.
"""
import hashlib
import http.server
import json
import os
import signal
import sys
import threading
import time

MODE = os.environ.get("FAKE_MODE", "")
ERROR_RATE = 0.0
for a in sys.argv[1:]:
    if a.startswith("--error-rate="):
        ERROR_RATE = float(a.split("=", 1)[1])
listen = "127.0.0.1:0"
if "--listen" in sys.argv:
    listen = sys.argv[sys.argv.index("--listen") + 1]
host, _, port = listen.rpartition(":")

COUNTER = {"n": 0}


def envelope(code, msg):
    return json.dumps({"error": {"code": code, "message": msg}}, separators=(",", ":")).encode()


def reply_for(body):
    key = hashlib.sha256(body).hexdigest()[:16]
    content = "Mock reply " + key
    if MODE == "nondeterministic":
        COUNTER["n"] += 1
        content += str(COUNTER["n"])
    return key, content


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def send(self, status, body, ctype="application/json"):
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/readyz":
            if MODE == "never-ready":
                self.send(503, b"not ready")
            else:
                self.send(200, b"ok")
        else:
            self.send(404, b"")

    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(n)
        try:
            req = json.loads(body)
        except ValueError:
            return self.send(400, envelope("invalid_request", "bad json"))
        if MODE != "accept-invalid" and (not req.get("messages") or set(req) - {"model", "messages", "stream"}):
            return self.send(400, envelope("invalid_request", "bad request"))
        if ERROR_RATE >= 1 and MODE != "ignore-error-rate":
            return self.send(500, envelope("internal_error", "simulated failure"))
        if MODE == "trickle" and req.get("messages"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            try:
                for _ in range(300):
                    self.wfile.write(b" ")
                    self.wfile.flush()
                    time.sleep(0.2)
            except OSError:
                pass
            return
        if MODE == "stall" and req.get("messages"):
            time.sleep(60)
            return
        canon = json.dumps({k: req.get(k) for k in ("model", "messages")}, sort_keys=True).encode()
        key, content = reply_for(canon)
        usage = {"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12}
        if MODE == "bad-usage" and req.get("stream"):
            usage = {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
        rid = "mock-" + key
        if not req.get("stream"):
            doc = {"id": rid, "object": "chat.completion", "model": req["model"],
                   "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
                   "usage": {"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12}}
            return self.send(200, json.dumps(doc, separators=(",", ":")).encode())
        chunks = [{"id": rid, "choices": [{"index": 0, "delta": {"role": "assistant"}, "finish_reason": None}]},
                  {"id": rid, "choices": [{"index": 0, "delta": {"content": content}, "finish_reason": None}]},
                  {"id": rid, "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]},
                  {"id": rid, "choices": [], "usage": usage}]
        if MODE == "usage-first":
            chunks = [chunks[3]] + chunks[:3]
        if MODE == "bad-content":
            chunks[1]["choices"][0]["delta"]["content"] = content + " tampered"
        if MODE == "bad-finish":
            chunks[2]["choices"][0]["finish_reason"] = "length"
        if MODE == "bad-id":
            chunks[1]["id"] = "mock-other"
        raw = "".join("data: " + json.dumps(c, separators=(",", ":")) + "\n\n" for c in chunks) + "data: [DONE]\n\n"
        if MODE == "bad-done":
            raw += "data: [DONE]\n\n"
        self.send(200, raw.encode(), "text/event-stream")


def main():
    if MODE == "exit-early":
        print("fake: exiting early", file=sys.stderr, flush=True)
        sys.exit(3)
    server = http.server.ThreadingHTTPServer((host, int(port)), Handler)
    if MODE == "no-address":
        pass
    elif MODE == "bad-addr":
        print("mockmodel listening on 0.0.0.0:%d" % server.server_address[1], flush=True)
    else:
        print("mockmodel listening on 127.0.0.1:%d" % server.server_address[1], flush=True)
    if MODE == "ignore-term":
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
    else:
        def stop(*_):
            threading.Thread(target=server.shutdown, daemon=True).start()
        signal.signal(signal.SIGTERM, stop)
        signal.signal(signal.SIGINT, stop)
    watchdog = threading.Timer(float(os.environ.get("FAKE_EXIT_AFTER", "120")), lambda: os._exit(0))
    watchdog.daemon = True
    watchdog.start()
    note = os.environ.get("FAKE_NOTE_FILE")
    if note:
        with open(note, "a") as fh:
            fh.write("%d %s\n" % (os.getpid(), MODE))
    server.serve_forever()
    server.server_close()
    sys.exit(3 if MODE == "nonzero-exit" else 0)


if __name__ == "__main__":
    main()
