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
# Fixture processes are tracked with their start time and killed at exit only if that
# start time still matches, so cleanup can never signal a recycled PID. This runs even
# when assertions fail, so a failing run leaves nothing behind.
tracked=()
track_pid() { tracked+=("$1|$(command ps -o lstart= -p "$1" 2>/dev/null | tr -s ' ')"); }
cleanup() {
  local e p want now
  for e in ${tracked[@]+"${tracked[@]}"}; do
    p="${e%%|*}"; want="${e#*|}"
    now="$(command ps -o lstart= -p "$p" 2>/dev/null | tr -s ' ')"
    if [ -n "$want" ] && [ "$now" = "$want" ]; then builtin kill -KILL "$p" 2>/dev/null || true; fi
  done
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

# ps-based helpers for the TEST (independent of the library under test)
running() { local s; s="$(command ps -o stat= -p "$1" 2>/dev/null | tr -d ' ')"; [ -n "$s" ] && case "$s" in Z*) return 1 ;; esac; }
gone() { [ -z "$(command ps -o stat= -p "$1" 2>/dev/null | tr -d ' ')" ]; }   # fully reaped: no process table entry

start_child() { # start_child NAME MODE -> registers NAME; waits until the child is up
  python3 -I "$fix/child.py" "$2" >"$fix/$1.out" 2>&1 &
  local started=$!
  track_pid "$started"
  op_register "$1" "$started" || return 1
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
canary=$!; track_pid "$canary"; disown "$canary" 2>/dev/null || true
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

# 5b. repeated stops report the recorded result, and an entry that is marked reaped is never signalled even when
#     its PID now belongs to a live child of ours (the PID-reuse case)
diag op_stop normal; r1=$?; diag op_stop stubborn; r2=$?
if [ "$r1" -eq 0 ] && [ "$r2" -eq 1 ] && grep -q 'had already stopped' "$fix/diag.txt"; then ok "a repeated stop returns the recorded result (0 after a clean exit, 1 after a kill) and says so"; else bad "repeated stop results: normal=$r1 stubborn=$r2: $(cat "$fix/diag.txt")"; fi
start_child reused term0
reused_pid="$(op_pid reused)"; idx="$(op__index reused)"
OP_REAPED[$idx]=1; OP_RESULT[$idx]=0   # pretend it was reaped while its PID is in fact a live child
mark="$(wc -l <"$KILL_LOG" | tr -d ' ')"
diag op_stop reused; r3=$?
if [ "$r3" -eq 0 ] && [ "$(wc -l <"$KILL_LOG" | tr -d ' ')" = "$mark" ] && running "$reused_pid"; then ok "an entry marked reaped is never signalled, even if its PID is now a live process of ours"; else bad "a reaped entry's PID was signalled (rc=$r3)"; fi
OP_REAPED[$idx]=""; op_stop reused 2>/dev/null   # really stop it now

# 5c. identity changes. The library must revalidate who a PID belongs to at every poll and immediately
#     before each signal. A wrapper for `ps` (used only by the library; this test's own helpers use
#     `command ps`) can report a different parent or start time, from the beginning or after a given
#     signal and a given number of ps calls. The registered children really stay alive and an
#     unrelated canary with the same command line must never be signalled.
PS_MODE=normal; PS_FLIP_SIGNAL=""; PS_FLIP_CALLS=0; PS_FLIP_MODE=normal; PS_MARK=0; PS_SEEN_FILE="$fix/ps-seen"; : >"$PS_SEEN_FILE"
ps() {
  local mode="$PS_MODE" out rc
  if [ -n "$PS_FLIP_SIGNAL" ] && tail -n +$((PS_MARK + 1)) "$KILL_LOG" | grep -q -- "-$PS_FLIP_SIGNAL "; then
    echo x >>"$PS_SEEN_FILE"   # a file, not a variable: the library calls ps inside a command substitution
    [ "$(wc -l <"$PS_SEEN_FILE" | tr -d ' ')" -lt "$PS_FLIP_CALLS" ] || mode="$PS_FLIP_MODE"
  fi
  case "$mode" in
    normal) command ps "$@" ;;
    fail) return 2 ;;
    empty) return 0 ;;
    absent) return 1 ;;
    foreign-any) echo " 1 Ss Mon Jan  1 00:00:00 1999" ;;   # some other process now holds the PID
    foreign-start|foreign-ppid)
      out="$(command ps "$@")"; rc=$?
      [ "$rc" -eq 0 ] || return "$rc"
      if [ "$mode" = foreign-start ]; then printf '%s\n' "$out" | sed -E 's/^( *[0-9]+ +[^ ]+ +)[A-Za-z]{3}/\1Zzz/'
      else printf '%s\n' "$out" | sed -E 's/^ *[0-9]+/ 1/'; fi ;;
  esac
}
ps_reset() { PS_MODE=normal; PS_FLIP_SIGNAL=""; PS_FLIP_CALLS=0; PS_FLIP_MODE=normal; : >"$PS_SEEN_FILE"; PS_MARK="$(wc -l <"$KILL_LOG" | tr -d ' ')"; }
signals_since() { tail -n +$((PS_MARK + 1)) "$KILL_LOG" | grep -c -- "$1"; }
teardown_child() { # end a test child ourselves (never through the library under test)
  local p; p="$(op_pid "$1")"; builtin kill -KILL "$p" 2>/dev/null; wait "$p" 2>/dev/null; OP_REAPED[$(op__index "$1")]=1; OP_RESULT[$(op__index "$1")]=0
}
only_registered_signalled() { # only_registered_signalled PID: every signal since the mark went to PID
  ! tail -n +$((PS_MARK + 1)) "$KILL_LOG" | grep -v -- " $1\$"
}

ps_reset
start_child idtest1 ignore; id1pid="$(op_pid idtest1)"
PS_MODE=foreign-ppid   # from now on ps reports that our child belongs to someone else
diag op_stop idtest1; r=$?
if [ "$r" -eq 1 ] && grep -q 'cannot establish its state' "$fix/diag.txt" && grep -q 'job table lists our child as running' "$fix/diag.txt" && [ "$(signals_since '^-')" -eq 0 ] && running "$id1pid" && running "$canary"; then ok "identity: ps claiming another owner for a child the job table says is running is refused (no TERM, no KILL, no wait)"; else bad "identity at first probe (rc=$r, signals=$(signals_since '^-')): $(cat "$fix/diag.txt")"; fi
ps_reset; teardown_child idtest1

# PID reuse model: our child really exited and was collected by bash; ps now shows an unrelated process at that PID
ps_reset
python3 -I "$fix/child.py" exit0 >/dev/null 2>&1 &
reuse_pid=$!; op_register idtest1b "$reuse_pid" || bad "could not register the short-lived child"
sleep 0.4   # the child has exited by now and bash has collected it
PS_MODE=foreign-any
diag op_stop idtest1b; r=$?
if [ "$r" -eq 1 ] && grep -q 'identity changed' "$fix/diag.txt" && [ "$(signals_since '^-')" -eq 0 ]; then ok "identity: once our child has exited, an unrelated process holding its PID is detected as a changed identity (no signal, no wait)"; else bad "PID reuse model (rc=$r, signals=$(signals_since '^-')): $(cat "$fix/diag.txt")"; fi
ps_reset; OP_REAPED[$(op__index idtest1b)]=1; OP_RESULT[$(op__index idtest1b)]=0

ps_reset; PS_FLIP_SIGNAL=TERM; PS_FLIP_CALLS=2; PS_FLIP_MODE=foreign-start
start_child idtest2 ignore; id2pid="$(op_pid idtest2)"
diag op_stop idtest2; r=$?
if [ "$r" -eq 1 ] && [ "$(signals_since '-TERM')" -eq 1 ] && [ "$(signals_since '-KILL')" -eq 0 ] && only_registered_signalled "$id2pid" && grep -q 'not escalating' "$fix/diag.txt" && running "$id2pid" && running "$canary"; then ok "identity: if the owner changes after SIGTERM, while polling, the library does not escalate (one TERM, no KILL)"; else bad "identity change while polling (rc=$r, TERM=$(signals_since '-TERM') KILL=$(signals_since '-KILL')): $(cat "$fix/diag.txt")"; fi
ps_reset; teardown_child idtest2

OWNED_PROCESS_TERM_MS=100 OWNED_PROCESS_POLL_MS=20
ps_reset; PS_FLIP_SIGNAL=TERM; PS_FLIP_CALLS=7; PS_FLIP_MODE=foreign-start   # the 7th ps call after TERM is the one made immediately before SIGKILL
start_child idtest3 ignore; id3pid="$(op_pid idtest3)"
diag op_stop idtest3; r=$?
if [ "$r" -eq 1 ] && [ "$(signals_since '-TERM')" -eq 1 ] && [ "$(signals_since '-KILL')" -eq 0 ] && grep -q 'refused to send SIGKILL' "$fix/diag.txt" && only_registered_signalled "$id3pid" && running "$id3pid" && running "$canary"; then ok "identity: if the owner changes between the last poll and SIGKILL, SIGKILL is not sent (the foreign canary is untouched)"; else bad "identity change before KILL (rc=$r, TERM=$(signals_since '-TERM') KILL=$(signals_since '-KILL')): $(cat "$fix/diag.txt")"; fi
ps_reset; teardown_child idtest3
export OWNED_PROCESS_TERM_MS=400 OWNED_PROCESS_POLL_MS=20

ps_reset; start_child idtest4 ignore; id4pid="$(op_pid idtest4)"
diag op_stop idtest4; r=$?
if [ "$r" -eq 1 ] && [ "$(signals_since '-KILL')" -eq 1 ] && only_registered_signalled "$id4pid" && gone "$id4pid"; then ok "identity (control): with an unchanged identity the same TERM-ignoring child is escalated and killed"; else bad "identity control (rc=$r)"; fi
ps_reset

# 5d. observation failures (review r1, finding 2). When the library cannot establish a child's state it
#     must signal nothing, WAIT FOR NOTHING and fail with a diagnostic, never claim success. The child is
#     long-lived (it would run 600 s). An outer watchdog, which belongs to the test, kills only that child
#     after 6 s so a regression to an unbounded wait shows up as a failed assertion instead of a hang.
WATCHDOG_FILE="$fix/watchdog-fired"
watchdog_start() { rm -f "$WATCHDOG_FILE"; ( sleep "$2"; if command ps -p "$1" >/dev/null 2>&1; then echo fired >"$WATCHDOG_FILE"; builtin kill -KILL "$1" 2>/dev/null; fi ) >/dev/null 2>&1 & WD=$!; disown "$WD" 2>/dev/null || true; }
watchdog_stop() { builtin kill "$WD" 2>/dev/null || true; }
observe_case() { # observe_case LABEL WANT_TEXT -- runs op_stop on a fresh long-lived child under the current ps/jobs setup
  local label="$1" want="$2" pid t0 r elapsed
  start_child obs term0; pid="$(op_pid obs)"
  eval "$3"   # arm the failure (PS_MODE=..., or a jobs function)
  watchdog_start "$pid" 6
  t0=$SECONDS; diag op_stop obs; r=$?; elapsed=$((SECONDS - t0))
  watchdog_stop
  local fired=""; [ -e "$WATCHDOG_FILE" ] && fired=1
  if [ "$r" -eq 1 ] && [ -z "$fired" ] && [ "$elapsed" -le 3 ] && [ "$(signals_since '^-')" -eq 0 ] && grep -q -- "$want" "$fix/diag.txt" && running "$pid" && running "$canary"; then
    ok "observation failure ($label): returns failure in $elapsed s, sends no signal, does not wait, leaves the child alone"
  else
    bad "observation failure ($label): rc=$r elapsed=${elapsed}s watchdog_fired=${fired:-no} signals=$(signals_since '^-'): $(cat "$fix/diag.txt")"
  fi
  PS_MODE=normal; unset -f jobs 2>/dev/null; ps_reset; teardown_child obs; OP_NAMES[$(op__index obs)]="obs-done-$RANDOM"
}
ps_reset; observe_case "ps fails with status 2" "cannot establish its state" 'ps_reset; PS_MODE=fail'
ps_reset; observe_case "ps succeeds with no output" "cannot establish its state" 'ps_reset; PS_MODE=empty'
ps_reset; observe_case "ps reports no such process for a live child" "job table lists it as running" 'ps_reset; PS_MODE=absent'
ps_reset; observe_case "the job table cannot be read" "job table could not be read" 'ps_reset; jobs() { return 1; }'
ps_reset; observe_case "the job table claims the live child finished" "job table says it has finished" 'ps_reset; jobs() { return 0; }'

ps_reset; PS_MODE=fail
python3 -I "$fix/child.py" term0 >/dev/null 2>&1 & blind_pid=$!
if ! diag op_register blind "$blind_pid" && grep -q 'cannot establish its identity' "$fix/diag.txt"; then ok "a child whose identity cannot be established is not registered"; else bad "registered a child without an identity: $(cat "$fix/diag.txt")"; fi
builtin kill -KILL "$blind_pid" 2>/dev/null; wait "$blind_pid" 2>/dev/null; ps_reset

# 6. only TERM/KILL to positive single PIDs: no group, no name, no probe of a reaped PID
if ! grep -Evq '^-(TERM|KILL) [0-9]+$' "$KILL_LOG" || [ ! -s "$KILL_LOG" ]; then
  [ -s "$KILL_LOG" ] && ok "every signal sent was -TERM or -KILL to one positive PID (never a group)" || bad "no signals were recorded"
else bad "an unexpected kill invocation: $(grep -Ev '^-(TERM|KILL) [0-9]+$' "$KILL_LOG")"; fi

# 7. unknown and non-child registrations are refused
if diag op_stop nosuch; [ $? -eq 2 ]; then ok "stopping an unregistered name is refused (status 2)"; else bad "unregistered stop was not refused"; fi
foreign="$( ( sleep 600 >/dev/null 2>&1 & echo $! ) )"; track_pid "$foreign" # stdio redirected so $(...) returns at once
if ! diag op_register foreign "$foreign" && grep -q 'not a child of this shell' "$fix/diag.txt"; then ok "a process that is not our child cannot be registered"; else bad "a non-child was registered"; fi
if ! diag op_register bogus abc && ! diag op_register init 1; then ok "non-numeric PIDs and PID 1 cannot be registered"; else bad "bogus PID registered"; fi

# 8. the canary (same command line as the stopped children) and the foreign process survive
if running "$canary" && running "$foreign"; then ok "an unrelated process with an identical command line survived every stop"; else bad "an unrelated process was killed (canary running: $(running "$canary" && echo yes || echo no); foreign running: $(running "$foreign" && echo yes || echo no))"; fi

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
  blind)
    python3 -I "$HARNESS_CHILD" term0 >"$OP_WORKDIR/c.out" 2>&1 & op_register c "$!"
    echo "$!" >"$2/childpid"; sleep 0.3
    ps() { return 2; }   # from here on the process table cannot be read: cleanup must still be bounded
    exit 0 ;;
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

# observation failure at cleanup time: the EXIT cleanup must not hang, must keep a failing status, must still
# remove the harness directory, and must not touch the child it can no longer identify
mkdir -p "$root/optest-harness.decoy-blind"
t0=$SECONDS; run_harness blind; blind_elapsed=$((SECONDS - t0))
blind_child="$(cat "$H_DIR/childpid" 2>/dev/null)"
if [ "$H_RC" -eq 1 ] && [ "$blind_elapsed" -le 6 ] && [ -n "$blind_child" ] && running "$blind_child" && [ ! -e "$(cat "$H_DIR/workdir")" ] && grep -q 'not signalling and not waiting' "$H_DIR/err"; then ok "cleanup when the process table cannot be read: bounded ($blind_elapsed s), exit 1, directory removed, the unidentifiable child left alone"; else bad "blind cleanup (rc=$H_RC, ${blind_elapsed}s): $(cat "$H_DIR/err")"; fi
if [ -n "$blind_child" ] && case "$(command ps -o command= -p "$blind_child" 2>/dev/null)" in *"$fix/child.py"*) true ;; *) false ;; esac; then builtin kill -KILL "$blind_child" 2>/dev/null || true; fi
rmdir "$root/optest-harness.decoy-blind" 2>/dev/null

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
mkdir -p "$fix/cwd/optest.relative"; : >"$fix/cwd/optest.relative/keep"
OP_WORKDIR="optest.relative"
if ! (cd "$fix/cwd" && diag op_remove_workdir) && [ -e "$fix/cwd/optest.relative/keep" ]; then ok "a relative directory name with no slash is refused and left intact"; else bad "a relative directory was removed"; fi
for weird in "" "$root/optest." "$root/optest.a/b" "$root/optest.x/../y"; do
  OP_WORKDIR="$weird"
  if [ -z "$weird" ]; then diag op_remove_workdir || { bad "an empty OP_WORKDIR is a no-op, not an error"; continue; }; ok "an empty OP_WORKDIR removes nothing"; continue; fi
  if ! diag op_remove_workdir; then ok "refused malformed path '$weird'"; else bad "accepted malformed path '$weird'"; fi
done
OP_WORKDIR=""

# --- the canary is still alive after the harness scenarios too
if running "$canary" && running "$foreign"; then ok "the unrelated canary survived the harness scenarios as well"; else bad "the canary did not survive"; fi

# --- static guard: the smoke harness must use the helper and have no bare wait or kill
grep -nE '^[^#]*[[:space:]](wait|kill)[[:space:]]' scripts/mock-smoke.sh >"$fix/bare.txt" 2>/dev/null || true
if ! grep -q 'lib/owned-process.sh' scripts/mock-smoke.sh; then bad "scripts/mock-smoke.sh does not use the owned-process helper"
elif [ -s "$fix/bare.txt" ]; then bad "scripts/mock-smoke.sh has a bare wait or kill: $(cat "$fix/bare.txt")"
else ok "scripts/mock-smoke.sh uses the helper and has no bare wait or kill"; fi

if [ "$failed" -eq 0 ]; then echo "owned-process self-test: all passed"; else echo "owned-process self-test: $failed FAILED"; exit 1; fi
