#!/usr/bin/env bash
# Smoke test for the deterministic mock model.
#
# Builds the binary, starts a disposable server on an OS-assigned loopback port
# (never a fixed one, so parallel runs cannot collide), waits for readiness with
# a timeout, and checks: identical requests give byte-identical responses, the
# streaming response is well framed (data events, one terminal usage chunk with
# empty choices, [DONE]) and agrees with the non-streaming response, bad input is
# a 400, and a server started with --error-rate=1 fails deterministically. It
# stops ONLY the processes it started (registered owned children, see
# scripts/lib/owned-process.sh): graceful SIGTERM with a bounded wait, a bounded
# escalation for that same child, no signal to anything else, and a cleanup of
# only its own temp directory on every exit path (normal, failure, INT, TERM).
# It needs go, curl and python3 and contacts nothing but 127.0.0.1.
set -euo pipefail

cd "$(dirname "$0")/.."

for tool in go curl python3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "ERROR: $tool not found." >&2; exit 1; }
done

# shellcheck source=lib/owned-process.sh
. scripts/lib/owned-process.sh
op_mktemp mock-smoke || { echo "ERROR: cannot create the temp directory." >&2; exit 1; }
work="$OP_WORKDIR"
op_install_traps

fail() { echo "mock-smoke: FAIL: $*" >&2; exit 1; }

echo "mock-smoke: building"
go build -o "$work/mockmodel" ./cmd/mockmodel

# start_server NAME [flags...] -> sets ADDR and PID once the server is ready.
start_server() {
  local name="$1"; shift
  "$work/mockmodel" --listen 127.0.0.1:0 "$@" >"$work/$name.out" 2>"$work/$name.err" &
  PID=$!
  op_register "$name" "$PID" || fail "cannot register $name"
  local i
  ADDR=""
  for i in $(seq 1 150); do # up to ~15 s for the address line
    op_alive "$name" || fail "$name exited early: $(cat "$work/$name.err")"
    ADDR="$(sed -n 's/^mockmodel listening on //p' "$work/$name.out" | head -1)"
    [ -n "$ADDR" ] && break
    sleep 0.1
  done
  [ -n "$ADDR" ] || fail "$name never reported its address"
  case "$ADDR" in 127.0.0.1:*) ;; *) fail "$name listens on $ADDR, want loopback" ;; esac
  for i in $(seq 1 100); do # up to ~10 s for readiness
    if curl -fsS --max-time 2 "http://$ADDR/readyz" >/dev/null 2>&1; then
      echo "mock-smoke: $name ready on $ADDR"
      return 0
    fi
    sleep 0.1
  done
  fail "$name never became ready"
}

post() { # post ADDR BODY OUTFILE -> prints the HTTP status
  curl -sS --max-time 10 -o "$3" -w '%{http_code}' -H 'Content-Type: application/json' --data-binary "$2" "http://$1/v1/chat/completions"
}

REQ='{"model":"mock-large","messages":[{"role":"user","content":"Summarize this document please."}]}'
REQ_STREAM='{"model":"mock-large","stream":true,"messages":[{"role":"user","content":"Summarize this document please."}]}'

start_server main
MAIN_ADDR="$ADDR"; MAIN_PID="$PID"

# 1. byte-identical non-streaming responses
[ "$(post "$MAIN_ADDR" "$REQ" "$work/r1.json")" = 200 ] || fail "first request was not 200"
[ "$(post "$MAIN_ADDR" "$REQ" "$work/r2.json")" = 200 ] || fail "second request was not 200"
cmp -s "$work/r1.json" "$work/r2.json" || fail "identical requests gave different bytes"
echo "mock-smoke: two identical requests gave byte-identical responses ($(wc -c <"$work/r1.json" | tr -d ' ') bytes)"

# 2. streaming framing, terminal events and agreement with the plain response
[ "$(post "$MAIN_ADDR" "$REQ_STREAM" "$work/s1.sse")" = 200 ] || fail "stream request was not 200"
[ "$(post "$MAIN_ADDR" "$REQ_STREAM" "$work/s2.sse")" = 200 ] || fail "second stream request was not 200"
cmp -s "$work/s1.sse" "$work/s2.sse" || fail "identical stream requests gave different bytes"
python3 -I - "$work/r1.json" "$work/s1.sse" <<'PY' || fail "stream check failed"
import json, sys
plain = json.load(open(sys.argv[1]))
raw = open(sys.argv[2]).read()
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
print("mock-smoke: stream ok (%d events, usage %s, finish %s)" % (len(events), json.dumps(usage[0]["usage"]), finish[0]))
PY

# 3. invalid input is a 400 with the error envelope
code="$(post "$MAIN_ADDR" '{"model":"m","messages":[],"tenant":"acme"}' "$work/bad.json")"
[ "$code" = 400 ] || fail "invalid request gave $code, want 400"
grep -q '"code":"invalid_request"' "$work/bad.json" || fail "invalid request lacks the invalid_request error"
echo "mock-smoke: invalid request rejected with 400"

# 4. a second disposable server with deterministic failures
start_server failing --error-rate=1
FAIL_ADDR="$ADDR"; FAIL_PID="$PID"
code="$(post "$FAIL_ADDR" "$REQ" "$work/fail.json")"
[ "$code" = 500 ] || fail "--error-rate=1 gave $code, want 500"
grep -q '"code":"internal_error"' "$work/fail.json" || fail "simulated failure lacks the internal_error envelope"
echo "mock-smoke: --error-rate=1 fails deterministically with 500"

# 5. clean, bounded shutdown of exactly the processes we started: SIGTERM, at most
# OWNED_PROCESS_TERM_MS (10 s) of waiting, exit status 0 required; a child that
# ignores the signal is killed (that child only) and the run FAILS.
for name in main failing; do
  pid="$(op_pid "$name")"
  op_stop "$name" || fail "$name (pid $pid) did not shut down cleanly on SIGTERM (see the diagnostics above)"
  if ps -p "$pid" >/dev/null 2>&1; then fail "$name (pid $pid) is still in the process table"; fi
done
if curl -fsS --max-time 2 "http://$MAIN_ADDR/readyz" >/dev/null 2>&1; then fail "the main server still answers after shutdown"; fi
echo "mock-smoke: both servers exited 0 on SIGTERM and no owned process survives"
echo "mock-smoke: OK"
