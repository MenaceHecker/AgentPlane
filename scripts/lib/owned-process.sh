#!/usr/bin/env bash
# Owned-child process helper, sourced by scripts that start disposable servers.
#
# It exists so a harness can start a background child, stop exactly that child
# with a bounded wait, and always leave nothing behind (review P0-T4-r1,
# finding 4). Rules the code enforces:
#   * Only a PID this shell started and registered is ever signalled, and only
#     while that PID has not been reaped (an unreaped PID cannot be reused).
#     Nothing is signalled by name, by pattern, or by process group.
#   * Graceful stop: SIGTERM, then wait at most OWNED_PROCESS_TERM_MS (default
#     10000). If the child is still running, SIGKILL that same child and wait at
#     most OWNED_PROCESS_KILL_MS (default 5000), then reap it. A child that
#     cannot be reaped is reported and left alone; the helper never waits
#     without a bound.
#   * A normal stop requires exit status 0. A nonzero exit, a kill after the
#     deadline, or a child that had already died with a failure is a failure
#     with a diagnostic on stderr.
#   * EXIT, INT and TERM run one cleanup that stops every registered child and
#     removes only the directory made by op_mktemp.
# Deadlines are counted in polls of OWNED_PROCESS_POLL_MS (default 50), so tests
# can inject short ones. Real elapsed time can exceed the nominal deadline by
# the cost of each poll. Compatible with bash 3.2 (macOS /bin/bash).

OP_NAMES=()
OP_PIDS=()
OP_REAPED=()   # "1" once wait has collected the child
OP_RESULT=()   # final status of a reaped child
OP_TMP_ROOT=""
OP_TMP_PREFIX=""
OP_WORKDIR=""
OP_CLEANUP_STARTED=""

op_log() { echo "owned-process: $*" >&2; }

op__ms() { # op__ms VAR DEFAULT -> a positive integer number of milliseconds
  local v="${!1:-$2}"
  case "$v" in ''|*[!0-9]*) v="$2" ;; esac
  [ "$v" -gt 0 ] || v="$2"
  echo "$v"
}

# op__state PID -> prints the ps state letters, or nothing if the PID is gone.
op__state() { ps -o stat= -p "$1" 2>/dev/null | tr -d ' '; }

# op__running PID: true while the process exists and is not a zombie.
op__running() {
  local s
  s="$(op__state "$1")"
  [ -n "$s" ] && case "$s" in Z*) return 1 ;; esac
}

op__ppid() { ps -o ppid= -p "$1" 2>/dev/null | tr -d ' '; }

op__index() { # op__index NAME -> prints the registry index, nonzero if unknown
  local i=0
  while [ "$i" -lt "${#OP_NAMES[@]}" ]; do
    if [ "${OP_NAMES[$i]}" = "$1" ]; then echo "$i"; return 0; fi
    i=$((i + 1))
  done
  return 1
}

# op_register NAME PID: record a child this shell just started (cmd & ; op_register n $!).
op_register() {
  local name="$1" pid="$2"
  case "$pid" in ''|*[!0-9]*) op_log "refusing to register '$name': '$pid' is not a PID"; return 1 ;; esac
  [ "$pid" -gt 1 ] || { op_log "refusing to register '$name': PID $pid"; return 1; }
  if op__index "$name" >/dev/null; then op_log "refusing to register '$name' twice"; return 1; fi
  if [ "$(op__ppid "$pid")" != "$$" ]; then
    op_log "refusing to register '$name' (pid $pid): it is not a child of this shell"
    return 1
  fi
  OP_NAMES+=("$name"); OP_PIDS+=("$pid"); OP_REAPED+=(""); OP_RESULT+=("")
}

# op_alive NAME: true while the registered child is running (a zombie is not running).
op_alive() {
  local i; i="$(op__index "$1")" || return 1
  [ -z "${OP_REAPED[$i]}" ] && op__running "${OP_PIDS[$i]}"
}

# op_pid NAME: print the PID of a registered child.
op_pid() { local i; i="$(op__index "$1")" || return 1; echo "${OP_PIDS[$i]}"; }

# op__wait_gone PID POLLS: poll until PID is no longer running; return 0 if it stopped.
op__wait_gone() {
  local pid="$1" polls="$2" poll_s n=0
  poll_s="$(awk -v ms="$(op__ms OWNED_PROCESS_POLL_MS 50)" 'BEGIN { printf "%.3f", ms / 1000 }')"
  while op__running "$pid"; do
    [ "$n" -lt "$polls" ] || return 1
    sleep "$poll_s"
    n=$((n + 1))
  done
  return 0
}

# op_stop NAME: stop one registered child as described at the top. Returns 0 only
# for a clean exit status 0 with no escalation.
op_stop() {
  local name="$1" i pid term_ms kill_ms poll_ms status=0 escalated="" rc
  i="$(op__index "$name")" || { op_log "cannot stop '$name': not registered"; return 2; }
  if [ -n "${OP_REAPED[$i]}" ]; then
    # Already collected: never signal a reaped PID. Report the recorded result.
    [ "${OP_RESULT[$i]}" = 0 ] && return 0
    op_log "$name had already stopped with status ${OP_RESULT[$i]}"
    return 1
  fi
  pid="${OP_PIDS[$i]}"
  if [ "$(op__ppid "$pid")" != "$$" ] && op__running "$pid"; then
    op_log "refusing to signal $name (pid $pid): it is not a child of this shell"
    return 1
  fi
  poll_ms="$(op__ms OWNED_PROCESS_POLL_MS 50)"
  term_ms="$(op__ms OWNED_PROCESS_TERM_MS 10000)"
  kill_ms="$(op__ms OWNED_PROCESS_KILL_MS 5000)"

  if op__running "$pid"; then
    kill -TERM "$pid" 2>/dev/null || true
    if ! op__wait_gone "$pid" $((term_ms / poll_ms)); then
      escalated=1
      op_log "$name (pid $pid) did not exit within ${term_ms} ms of SIGTERM; sending SIGKILL to that process"
      kill -KILL "$pid" 2>/dev/null || true
      if ! op__wait_gone "$pid" $((kill_ms / poll_ms)); then
        op_log "$name (pid $pid) is still running ${kill_ms} ms after SIGKILL; leaving it unreaped"
        return 1
      fi
    fi
  fi
  # The child has exited (a zombie, or already gone): collecting it cannot block.
  wait "$pid" 2>/dev/null || status=$?
  OP_REAPED[$i]=1; OP_RESULT[$i]="$status"
  rc=0
  if [ -n "$escalated" ]; then
    op_log "$name (pid $pid) was killed after ignoring SIGTERM (status $status)"; rc=1
  elif [ "$status" -ne 0 ]; then
    op_log "$name (pid $pid) exited with status $status, want 0"; rc=1
  fi
  return "$rc"
}

# op_stop_all: stop every registered child that is not yet reaped; nonzero if any failed.
op_stop_all() {
  local i=0 rc=0
  while [ "$i" -lt "${#OP_NAMES[@]}" ]; do
    if [ -z "${OP_REAPED[$i]}" ]; then op_stop "${OP_NAMES[$i]}" || rc=1; fi
    i=$((i + 1))
  done
  return "$rc"
}

# op_mktemp PREFIX: create the harness directory (sets OP_WORKDIR).
op_mktemp() {
  local root="${TMPDIR:-/tmp}"
  OP_TMP_ROOT="${root%/}"
  OP_TMP_PREFIX="$1"
  OP_WORKDIR="$(mktemp -d "$OP_TMP_ROOT/$1.XXXXXX")" || return 1
  OP_WORKDIR="${OP_WORKDIR//\/\///}"
}

# op_remove_workdir: remove only the directory op_mktemp created. Refuses anything
# else (empty, a symlink, not directly under the temp root, wrong prefix).
op_remove_workdir() {
  local d="$OP_WORKDIR" rest
  [ -n "$d" ] || return 0
  rest="${d#"$OP_TMP_ROOT/$OP_TMP_PREFIX."}"
  if [ "$rest" = "$d" ] || [ -z "$rest" ] || [ "${rest#*/}" != "$rest" ] || [ "${d#*..}" != "$d" ] || [ -L "$d" ] || [ ! -d "$d" ]; then
    op_log "refusing to remove '$d': it is not a directory this harness created under $OP_TMP_ROOT"
    return 1
  fi
  rm -rf -- "$d"
  OP_WORKDIR=""
}

# op__cleanup EXIT_STATUS: the single cleanup path for EXIT, INT and TERM.
op__cleanup() {
  local rc="$1" crc=0
  [ -z "$OP_CLEANUP_STARTED" ] || return "$rc"
  OP_CLEANUP_STARTED=1
  trap - EXIT INT TERM
  op_stop_all || crc=1
  op_remove_workdir || crc=1
  if [ "$rc" -eq 0 ] && [ "$crc" -ne 0 ]; then rc=1; fi
  return "$rc"
}

op__on_exit() { local rc=$?; op__cleanup "$rc"; exit $?; }
op__on_signal() { op__cleanup "$1"; exit $?; }

# op_install_traps: route EXIT, INT and TERM through the one cleanup.
op_install_traps() {
  trap op__on_exit EXIT
  trap 'op__on_signal 130' INT
  trap 'op__on_signal 143' TERM
}
