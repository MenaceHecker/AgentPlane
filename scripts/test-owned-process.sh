#!/usr/bin/env bash
# Tests for scripts/lib/owned-process.sh (make smoke-cleanup-test).
#
# Controlled children are small Python processes so each test is one process
# with a known signal behaviour. Deadlines are injected (a few hundred
# milliseconds) so nothing waits long. An unrelated "canary" process, started
# with the SAME command line as a stopped child, must survive every case: the
# helper may only signal PIDs it registered, never by name or process group.
set -uo pipefail
cd "$(dirname "$0")/.."

command -v python3 >/dev/null 2>&1 || { echo "ERROR: python3 not found; the owned-process tests cannot run." >&2; exit 1; }

fix="$(mktemp -d "${TMPDIR:-/tmp}/owned-process-test.XXXXXX")"
failed=0
ok()  { echo "PASS  $1"; }
bad() { echo "FAIL  $1"; failed=$((failed + 1)); }
canary_pids=()
cleanup() {
  local p
  for p in ${canary_pids[@]+"${canary_pids[@]}"}; do kill -KILL "$p" 2>/dev/null || true; done
  case "$fix" in "${TMPDIR:-/tmp}"/owned-process-test.*) rm -rf -- "$fix" ;; esac
}
trap cleanup EXIT

# child.py MODE: term0 (exit 0 on TERM), term3 (exit 3 on TERM), ignore (ignore TERM),
# exit0 / exit5 (exit at once with that status).
cat >"$fix/child.py" <<'PY'
import signal, sys, time
mode = sys.argv[1]
if mode in ("exit0", "exit5"):
    sys.exit(int(mode[4:]))
if mode == "ignore":
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
else:
    code = 0 if mode == "term0" else 3
    signal.signal(signal.SIGTERM, lambda *a: sys.exit(code))
sys.stdout.write("up\n"); sys.stdout.flush()
time.sleep(600)
PY

# shared test settings: short, injected deadlines
export OWNED_PROCESS_TERM_MS=400 OWNED_PROCESS_KILL_MS=3000 OWNED_PROCESS_POLL_MS=20
# shellcheck source=lib/owned-process.sh
. scripts/lib/owned-process.sh

# kill wrapper: record every signal the helper sends (functions shadow the builtin)
KILL_LOG="$fix/kill.log"; : >"$KILL_LOG"
kill() { echo "$*" >>"$KILL_LOG"; builtin kill "$@"; }

gone() { ! op__running "$1" && [ -z "$(op__state "$1")" ]; } # fully reaped: no process table entry

start_child() { # start_child NAME MODE -> registers NAME; waits until the child is up
  python3 -I "$fix/child.py" "$2" >"$fix/$1.out" 2>&1 &
  op_register "$1" "$!" || return 1
  case "$2" in exit0|exit5) return 0 ;; esac
  local n=0
  until grep -q '^up$' "$fix/$1.out" 2>/dev/null; do
    n=$((n + 1)); [ "$n" -le 200 ] || { echo "child $1 never started" >&2; return 1; }
    sleep 0.05
  done
}
diag() { "$@" 2>"$fix/diag.txt"; local rc=$?; return $rc; }

# --- the unrelated canary: same command line as the term0 children below
python3 -I "$fix/child.py" term0 >"$fix/canary.out" 2>&1 &
canary=$!; canary_pids+=("$canary"); disown "$canary" 2>/dev/null || true
n=0; until grep -q '^up$' "$fix/canary.out" 2>/dev/null; do n=$((n + 1)); [ "$n" -le 200 ] || break; sleep 0.05; done

# 1. normal exit
start_child normal term0
if diag op_stop normal && gone "$(op_pid normal)"; then ok "normal: a child that exits 0 on SIGTERM is stopped, reaped and reported success"; else bad "normal stop failed: $(cat "$fix/diag.txt")"; fi

# 2. nonzero exit
start_child nonzero term3
if ! diag op_stop nonzero && grep -q 'exited with status 3' "$fix/diag.txt" && gone "$(op_pid nonzero)"; then ok "nonzero: exit status 3 on SIGTERM is a failure with a diagnostic naming the status"; else bad "nonzero exit not reported: $(cat "$fix/diag.txt")"; fi

# 3. a TERM-ignoring child: bounded wait, escalation to that child only, reaped, failure
start_child stubborn ignore
t0=$SECONDS
if ! diag op_stop stubborn && grep -q 'did not exit within 400 ms of SIGTERM' "$fix/diag.txt" && grep -q 'was killed after ignoring SIGTERM' "$fix/diag.txt" \
   && gone "$(op_pid stubborn)" && [ $((SECONDS - t0)) -lt 6 ]; then
  ok "TERM-ignoring child: waited the injected 400 ms, killed that child, reaped it, failed with diagnostics, in under 6 s"
else bad "TERM-ignoring child mishandled (elapsed $((SECONDS - t0)) s): $(cat "$fix/diag.txt")"; fi

# 4. children that already exited
start_child early0 exit0; sleep 0.2
if diag op_stop early0; then ok "a child that exited 0 on its own is collected and reported success"; else bad "early exit 0 failed: $(cat "$fix/diag.txt")"; fi
start_child early5 exit5; sleep 0.2
if ! diag op_stop early5 && grep -q 'exited with status 5' "$fix/diag.txt"; then ok "a child that died with status 5 before the stop is a failure naming the status"; else bad "early exit 5 not reported: $(cat "$fix/diag.txt")"; fi

# 5. a reaped PID is never signalled again
mark="$(wc -l <"$KILL_LOG" | tr -d ' ')"
diag op_stop normal; diag op_stop stubborn; diag op_stop early5
if [ "$(wc -l <"$KILL_LOG" | tr -d ' ')" = "$mark" ]; then ok "stopping already-reaped children again sends no signal"; else bad "a reaped PID was signalled: $(tail -n +$((mark + 1)) "$KILL_LOG")"; fi

# 6. only TERM/KILL to positive single PIDs: no group, no name, no probe of a reaped PID
if ! grep -Evq '^-(TERM|KILL) [0-9]+$' "$KILL_LOG" || [ ! -s "$KILL_LOG" ]; then
  [ -s "$KILL_LOG" ] && ok "every signal sent was -TERM or -KILL to one positive PID (never a group)" || bad "no signals were recorded"
else bad "an unexpected kill invocation: $(grep -Ev '^-(TERM|KILL) [0-9]+$' "$KILL_LOG")"; fi

# 7. unknown and non-child registrations are refused
if diag op_stop nosuch; [ $? -eq 2 ]; then ok "stopping an unregistered name is refused (status 2)"; else bad "unregistered stop was not refused"; fi
foreign="$( ( sleep 600 >/dev/null 2>&1 & echo $! ) )"; canary_pids+=("$foreign") # stdio redirected so $(...) returns at once
if ! diag op_register foreign "$foreign" && grep -q 'not a child of this shell' "$fix/diag.txt"; then ok "a process that is not our child cannot be registered"; else bad "a non-child was registered"; fi
if ! diag op_register bogus abc && ! diag op_register init 1; then ok "non-numeric PIDs and PID 1 cannot be registered"; else bad "bogus PID registered"; fi

# 8. the canary (same command line as the stopped children) and the foreign process survive
if op__running "$canary" && op__running "$foreign"; then ok "an unrelated process with an identical command line survived every stop"; else bad "an unrelated process was killed (canary running: $(op__running "$canary" && echo yes || echo no); foreign running: $(op__running "$foreign" && echo yes || echo no))"; fi

# --- harness scenarios: a real script using the helper, run as a child
cat >"$fix/harness.sh" <<'SH'
#!/usr/bin/env bash
# usage: harness.sh MODE OUTDIR   (modes: fail, partial, stubborn, signal)
set -uo pipefail
. "$HARNESS_LIB"
op_mktemp optest-harness || exit 9
op_install_traps
echo "$OP_WORKDIR" >"$2/workdir"
case "$1" in
  fail)
    python3 -I "$HARNESS_CHILD" term0 >"$OP_WORKDIR/c.out" 2>&1 & op_register c "$!"
    echo "$!" >"$2/childpid"; sleep 0.3; echo "harness failing" >&2; exit 1 ;;
  partial)
    python3 -I "$HARNESS_CHILD" exit5 >"$OP_WORKDIR/c.out" 2>&1 & op_register c "$!"
    echo "$!" >"$2/childpid"; sleep 0.2; echo "never ready" >&2; exit 1 ;;
  stubborn)
    python3 -I "$HARNESS_CHILD" ignore >"$OP_WORKDIR/c.out" 2>&1 & op_register c "$!"
    echo "$!" >"$2/childpid"; sleep 0.3; exit 0 ;;
  signal)
    python3 -I "$HARNESS_CHILD" term0 >"$OP_WORKDIR/c.out" 2>&1 & op_register c "$!"
    echo "$!" >"$2/childpid"; : >"$2/ready"
    while :; do sleep 0.05; done ;;
esac
SH
chmod +x "$fix/harness.sh"
export HARNESS_LIB="$PWD/scripts/lib/owned-process.sh" HARNESS_CHILD="$fix/child.py"
root="${TMPDIR:-/tmp}"; root="${root%/}"

run_harness() { # run_harness MODE -> sets H_RC, H_DIR; the harness runs with its own copy of the settings
  H_DIR="$fix/h-$1"; mkdir -p "$H_DIR"
  mkdir -p "$root/optest-harness.decoy-$1" # same prefix, not created by the harness: must survive
  "$fix/harness.sh" "$1" "$H_DIR" >"$H_DIR/out" 2>"$H_DIR/err"; H_RC=$?
}
check_clean() { # check_clean MODE -> workdir removed, child reaped, decoy kept
  local wd cp; wd="$(cat "$1/workdir" 2>/dev/null)"; cp="$(cat "$1/childpid" 2>/dev/null)"
  [ -n "$wd" ] && [ ! -e "$wd" ] && { [ -z "$cp" ] || gone "$cp"; }
}

run_harness fail
if [ "$H_RC" -eq 1 ] && check_clean "$H_DIR" && [ -d "$root/optest-harness.decoy-fail" ]; then ok "failure path: exit status kept (1), child stopped, harness directory removed, same-prefix decoy directory kept"; else bad "failure path (rc=$H_RC): $(cat "$H_DIR/err")"; fi
rmdir "$root/optest-harness.decoy-fail" 2>/dev/null

run_harness partial
if [ "$H_RC" -eq 1 ] && check_clean "$H_DIR" && grep -q 'exited with status 5' "$H_DIR/err"; then ok "partial startup: a child that died before readiness is reported, cleanup completes, directory removed"; else bad "partial startup (rc=$H_RC): $(cat "$H_DIR/err")"; fi
rmdir "$root/optest-harness.decoy-partial" 2>/dev/null

OWNED_PROCESS_TERM_MS=300 run_harness stubborn
if [ "$H_RC" -eq 1 ] && check_clean "$H_DIR" && grep -q 'killed after ignoring SIGTERM' "$H_DIR/err"; then ok "a harness that would exit 0 still fails (1) when cleanup had to kill a TERM-ignoring child"; else bad "stubborn harness (rc=$H_RC): $(cat "$H_DIR/err")"; fi
rmdir "$root/optest-harness.decoy-stubborn" 2>/dev/null

# signals: launch through python so the harness gets default (not ignored) INT/TERM dispositions
sig_case() { # sig_case NAME SIGNUM EXPECTED_RC
  local d="$fix/h-sig-$1"; mkdir -p "$d"
  local rc
  rc="$(python3 -I - "$fix/harness.sh" "$d" "$2" <<'PY'
import os, signal, subprocess, sys, time
h, d, sig = sys.argv[1], sys.argv[2], int(sys.argv[3])
p = subprocess.Popen([h, "signal", d], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
for _ in range(200):
    if os.path.exists(os.path.join(d, "ready")): break
    time.sleep(0.05)
p.send_signal(sig)
try:
    print(p.wait(timeout=10))
except subprocess.TimeoutExpired:
    p.kill(); print("timeout")
PY
)"
  if [ "$rc" = "$3" ] && check_clean "$d"; then ok "signal $1: harness exited $3, stopped its child and removed its directory"; else bad "signal $1: rc=$rc (want $3)"; fi
}
mkdir -p "$root/optest-harness.decoy-signal"
sig_case TERM 15 143
sig_case INT 2 130
rmdir "$root/optest-harness.decoy-signal" 2>/dev/null

# --- directory removal refuses anything it did not create
OP_TMP_ROOT="$root"; OP_TMP_PREFIX="optest"
mkdir -p "$fix/elsewhere.123"; : >"$fix/elsewhere.123/keep"
OP_WORKDIR="$fix/elsewhere.123"
if ! diag op_remove_workdir && [ -e "$fix/elsewhere.123/keep" ]; then ok "a directory outside the temp root is refused and left intact"; else bad "an outside directory was removed"; fi
mkdir -p "$fix/target"; : >"$fix/target/keep"
ln -s "$fix/target" "$root/optest.link$$"
OP_WORKDIR="$root/optest.link$$"
if ! diag op_remove_workdir && [ -e "$fix/target/keep" ]; then ok "a symlink with the right name is refused and its target is intact"; else bad "a symlink was followed"; fi
rm -f "$root/optest.link$$"
for weird in "" "$root/optest." "$root/optest.a/b" "$root/optest.x/../y"; do
  OP_WORKDIR="$weird"
  if [ -z "$weird" ]; then diag op_remove_workdir || { bad "an empty OP_WORKDIR is a no-op, not an error"; continue; }; ok "an empty OP_WORKDIR removes nothing"; continue; fi
  if ! diag op_remove_workdir; then ok "refused malformed path '$weird'"; else bad "accepted malformed path '$weird'"; fi
done
OP_WORKDIR=""

# --- the canary is still alive after the harness scenarios too
if op__running "$canary" && op__running "$foreign"; then ok "the unrelated canary survived the harness scenarios as well"; else bad "the canary did not survive"; fi

# --- static guard: the smoke harness must use the helper and have no bare wait or kill
grep -nE '^[^#]*[[:space:]](wait|kill)[[:space:]]' scripts/mock-smoke.sh >"$fix/bare.txt" 2>/dev/null || true
if ! grep -q 'lib/owned-process.sh' scripts/mock-smoke.sh; then bad "scripts/mock-smoke.sh does not use the owned-process helper"
elif [ -s "$fix/bare.txt" ]; then bad "scripts/mock-smoke.sh has a bare wait or kill: $(cat "$fix/bare.txt")"
else ok "scripts/mock-smoke.sh uses the helper and has no bare wait or kill"; fi

if [ "$failed" -eq 0 ]; then echo "owned-process self-test: all passed"; else echo "owned-process self-test: $failed FAILED"; exit 1; fi
