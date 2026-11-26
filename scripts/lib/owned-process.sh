#!/usr/bin/env bash
# Owned-child process helper, sourced by scripts that start disposable servers.
#
# It exists so a harness can start a background child, stop exactly that child
# with a bounded wait, and always leave nothing behind (review P0-T4-r1,
# finding 4). Rules the code enforces:
#   * Only a PID this shell started and registered is signalled, never by name,
#     by pattern or by process group, and only after the child's identity (parent,
#     start time as ps prints it, and this shell's job table) has been checked
#     immediately before the signal. THIS IS NOT A UNIQUE IDENTITY (review
#     P0-T5-r2, finding 1, reproduced): if bash has collected a registered child
#     and its PID is given to a NEW child of the same shell started within the same
#     one-second start time, ps and the job table cannot tell them apart and a signal
#     can reach the replacement. There is also an unquantified gap between the last
#     check and the kill system call. Neither is closed here; the exposure is a
#     sibling child of the same shell, not an arbitrary process. See
#     agents/runs/P0-T5.md (Blocked) for the design decision this needs.
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
OP_IDS=()      # identity token per child: its start time as ps reports it ("" if it had already exited)
OP_REAPED=()   # "1" once wait has collected the child
OP_RESULT=()   # final status of a reaped child
OP_TMP_ROOT=""
OP_TMP_PREFIX=""
OP_WORKDIR=""
OP_ROOT_PHYS=""    # the temp root as resolved when op_mktemp ran
OP_ROOT_ID=""      # its device:inode
OP_WORKDIR_ID=""   # device:inode of the directory op_mktemp created
OP_CLEANUP_STARTED=""

op_log() { echo "owned-process: $*" >&2; }

op__ms() { # op__ms VAR DEFAULT -> a positive integer number of milliseconds
  local v="${!1:-$2}"
  case "$v" in ''|*[!0-9]*) v="$2" ;; esac
  [ "$v" -gt 0 ] || v="$2"
  echo "$v"
}

# --- observing a child: identity, not just a PID --------------------------------
# A PID alone is not an identity: once a process has been collected the kernel can
# hand the number to an unrelated process, and bash can collect a child before we
# call wait, so a registry flag cannot prove the PID is still ours. Every
# registered child therefore has an identity token (its start time as ps reports it,
# captured at registration) and must still be a child of THIS shell. The token is
# re-checked at every poll and immediately before each signal. Anything that does
# not match, or cannot be established, means "do not signal" (fail closed).

# op__ps_fields PID: sets F_RC (ps exit status), F_PPID, F_STAT, F_START ("" if none).
op__ps_fields() {
  local out a b c d e f g
  F_PPID=""; F_STAT=""; F_START=""
  out="$(ps -o ppid= -o stat= -o lstart= -p "$1" 2>/dev/null)"; F_RC=$?
  if [ "$F_RC" -eq 0 ]; then
    if [ -z "$out" ]; then F_RC=98; return 0; fi    # success with no output is not an answer
    read -r a b c d e f g <<<"$out"
    if [ -z "$g" ]; then F_RC=97; return 0; fi      # malformed: fewer fields than ppid, stat and a 5-field start time
    F_PPID="$a"; F_STAT="$b"; F_START="$c $d $e $f $g"
  fi
  return 0
}

# op__in_jobs MODE PID: is PID in this shell's job table? MODE "-r" = running jobs only,
# "-p" = all jobs. The table is the shell's own record of the children IT started and
# has not yet collected, so a PID recycled for an unrelated process does not appear in it
# and a failing or lying ps cannot change it. It does NOT tell a registered child from a
# new child of this same shell that was given the same PID. Returns 0 listed, 1 not
# listed, 2 the table could not be read.
# Only meaningful in the shell that started the children (not in a subshell).
op__in_jobs() {
  local list
  list="$(jobs "$1" -p 2>/dev/null)" || return 2
  case " $(echo $list) " in *" $2 "*) return 0 ;; *) return 1 ;; esac
}

# op__probe INDEX: sets PROBE to one of
#   RUN      the registered child is alive and still ours (ps and the job table agree)
#   EXITED   the registered child has exited (a zombie, or gone from both ps and the job table)
#   CHANGED  the PID now belongs to something that is not our child (identity differs)
#   UNKNOWN  the state could not be established, or the two sources disagree
# and PROBE_WHY to a sentence for diagnostics. Only EXITED allows a wait, and only
# RUN allows a signal.
op__probe() {
  local pid="${OP_PIDS[$1]}" want="${OP_IDS[$1]}" jr
  PROBE=UNKNOWN; PROBE_WHY=""
  op__ps_fields "$pid"
  op__in_jobs -r "$pid"; jr=$?
  if [ "$jr" -eq 2 ]; then PROBE_WHY="this shell's job table could not be read"; return 0; fi
  if [ "$F_RC" -eq 0 ]; then
    if [ "$F_PPID" != "$$" ] || [ "$F_START" != "$want" ]; then
      if [ "$jr" -eq 0 ]; then
        PROBE_WHY="ps says pid $pid belongs to another process (parent $F_PPID, start '$F_START') but the job table lists our child as running"
        return 0
      fi
      PROBE=CHANGED
      PROBE_WHY="pid $pid now reports parent $F_PPID and start '$F_START'; the registered child had parent $$ and start '$want'"
      return 0
    fi
    case "$F_STAT" in
      Z*) PROBE=EXITED; PROBE_WHY="zombie" ;;
      *)
        if [ "$jr" -eq 0 ]; then PROBE=RUN; else PROBE_WHY="ps shows pid $pid running but the job table says it has finished"; fi ;;
    esac
    return 0
  fi
  if [ "$F_RC" -eq 1 ]; then
    if [ "$jr" -eq 0 ]; then PROBE_WHY="ps reports no process $pid but the job table lists it as running"; return 0; fi
    PROBE=EXITED; PROBE_WHY="no process with pid $pid"; return 0
  fi
  PROBE_WHY="ps could not report on pid $pid (status $F_RC)"
}

op__index() { # op__index NAME -> prints the registry index, nonzero if unknown
  local i=0
  while [ "$i" -lt "${#OP_NAMES[@]}" ]; do
    if [ "${OP_NAMES[$i]}" = "$1" ]; then echo "$i"; return 0; fi
    i=$((i + 1))
  done
  return 1
}

# op_register NAME PID: record a child this shell just started (cmd & ; op_register n $!).
# Must be called in the shell that started the child (not a subshell). Refused unless
# the child's identity can be established.
op_register() {
  local name="$1" pid="$2"
  case "$pid" in ''|*[!0-9]*) op_log "refusing to register '$name': '$pid' is not a PID"; return 1 ;; esac
  [ "$pid" -gt 1 ] || { op_log "refusing to register '$name': PID $pid"; return 1; }
  if op__index "$name" >/dev/null; then op_log "refusing to register '$name' twice"; return 1; fi
  op__ps_fields "$pid"
  local ident=""
  if [ "$F_RC" -eq 0 ]; then
    if [ "$F_PPID" != "$$" ]; then
      op_log "refusing to register '$name' (pid $pid): it is not a child of this shell"
      return 1
    fi
    ident="$F_START"
  elif [ "$F_RC" -eq 1 ]; then
    ident=""    # already gone: it exited before it could be registered; stopping it will report its status
  else
    op_log "refusing to register '$name' (pid $pid): cannot establish its identity (ps status $F_RC)"
    return 1
  fi
  op__in_jobs -p "$pid"; local jr=$?
  if [ "$jr" -ne 0 ]; then
    op_log "refusing to register '$name' (pid $pid): it is not in this shell's job table (not started with & by this shell, or the table is unreadable)"
    return 1
  fi
  OP_NAMES+=("$name"); OP_PIDS+=("$pid"); OP_IDS+=("$ident"); OP_REAPED+=(""); OP_RESULT+=("")
}

# op_alive NAME: true while the registered child is running and still ours.
op_alive() {
  local i; i="$(op__index "$1")" || return 1
  [ -z "${OP_REAPED[$i]}" ] || return 1
  op__probe "$i"
  [ "$PROBE" = RUN ]
}

# op_pid NAME: print the PID of a registered child.
op_pid() { local i; i="$(op__index "$1")" || return 1; echo "${OP_PIDS[$i]}"; }

# op__signal INDEX SIGNAL: send SIGNAL to the child only if, immediately before, it
# is established to be the registered child and alive. Returns 0 sent, 1 refused
# (not running or not ours), 2 refused (state unknown). Nothing is sent in 1 or 2.
op__signal() {
  op__probe "$1"
  case "$PROBE" in
    RUN) ;;
    UNKNOWN) return 2 ;;
    *) return 1 ;;
  esac
  kill -"$2" "${OP_PIDS[$1]}" 2>/dev/null || true
  return 0
}

# op__poll_exit INDEX POLLS: poll until the child has exited. Returns 0 exited,
# 1 still running after POLLS polls, 2 identity changed or state unknown (PROBE_WHY set).
op__poll_exit() {
  local i="$1" polls="$2" poll_s n=0
  poll_s="$(awk -v ms="$(op__ms OWNED_PROCESS_POLL_MS 50)" 'BEGIN { printf "%.3f", ms / 1000 }')"
  while :; do
    op__probe "$i"
    case "$PROBE" in
      EXITED) return 0 ;;
      RUN) ;;
      *) return 2 ;;
    esac
    [ "$n" -lt "$polls" ] || return 1
    sleep "$poll_s"
    n=$((n + 1))
  done
}

# op_stop NAME: stop one registered child as described at the top. Returns 0 only
# for a clean exit status 0 with no escalation. If the child's identity or state
# cannot be established it signals nothing, waits for nothing, and fails.
op_stop() {
  local name="$1" i pid term_ms kill_ms poll_ms status=0 escalated="" rc r
  i="$(op__index "$name")" || { op_log "cannot stop '$name': not registered"; return 2; }
  if [ -n "${OP_REAPED[$i]}" ]; then
    # Already collected by us: nothing to signal. Report the recorded result.
    [ "${OP_RESULT[$i]}" = 0 ] && return 0
    op_log "$name had already stopped with status ${OP_RESULT[$i]}"
    return 1
  fi
  pid="${OP_PIDS[$i]}"
  poll_ms="$(op__ms OWNED_PROCESS_POLL_MS 50)"
  term_ms="$(op__ms OWNED_PROCESS_TERM_MS 10000)"
  kill_ms="$(op__ms OWNED_PROCESS_KILL_MS 5000)"

  op__probe "$i"
  case "$PROBE" in
    UNKNOWN) op_log "$name (pid $pid): cannot establish its state ($PROBE_WHY); not signalling and not waiting"; return 1 ;;
    CHANGED) op_log "$name (pid $pid): identity changed ($PROBE_WHY); not signalling"; return 1 ;;
  esac
  if [ "$PROBE" = RUN ]; then
    r=0; op__signal "$i" TERM || r=$?
    if [ "$r" -ne 0 ] && [ "$PROBE" != EXITED ]; then
      op_log "$name (pid $pid): refused to send SIGTERM ($PROBE_WHY); nothing was signalled"; return 1
    fi
    r=0; op__poll_exit "$i" $((term_ms / poll_ms)) || r=$?
    if [ "$r" -eq 2 ]; then op_log "$name (pid $pid): lost track while waiting after SIGTERM ($PROBE_WHY); not escalating"; return 1; fi
    if [ "$r" -eq 1 ]; then
      escalated=1
      op_log "$name (pid $pid) did not exit within ${term_ms} ms of SIGTERM; sending SIGKILL to that process"
      r=0; op__signal "$i" KILL || r=$?
      if [ "$r" -ne 0 ] && [ "$PROBE" != EXITED ]; then
        op_log "$name (pid $pid): refused to send SIGKILL ($PROBE_WHY); nothing was signalled"; return 1
      fi
      r=0; op__poll_exit "$i" $((kill_ms / poll_ms)) || r=$?
      if [ "$r" -eq 1 ]; then op_log "$name (pid $pid) is still running ${kill_ms} ms after SIGKILL; leaving it unreaped"; return 1; fi
      if [ "$r" -eq 2 ]; then op_log "$name (pid $pid): lost track after SIGKILL ($PROBE_WHY)"; return 1; fi
    fi
  fi
  # The child has exited (a zombie, or already collected): collecting it cannot block.
  wait "$pid" 2>/dev/null || status=$?
  OP_REAPED[$i]=1; OP_RESULT[$i]="$status"
  rc=0
  if [ -n "$escalated" ]; then
    op_log "$name (pid $pid) was killed after ignoring SIGTERM (status $status)"; rc=1
  elif [ "$status" -eq 127 ]; then
    op_log "$name (pid $pid) exited but its exit status could not be collected"; rc=1
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

# op__norm PATH: print PATH with repeated slashes collapsed and trailing slashes dropped
# (a lone "/" stays). Spaces and every other character are kept as they are.
op__norm() {
  local p="$1"
  while :; do case "$p" in *//*) p="${p//\/\///}" ;; *) break ;; esac; done
  while [ "${#p}" -gt 1 ] && [ "${p%/}" != "$p" ]; do p="${p%/}"; done
  printf '%s' "$p"
}

# op__phys DIR: print DIR's physical path, every symlink in every ancestor resolved.
op__phys() { ( cd "$1" 2>/dev/null && pwd -P ); }

# op__ident PATH: print "device:inode" of PATH itself (a symlink is not followed, so a symlink
# and its target have different identities), or fail if it cannot be determined. GNU stat
# first, then BSD/macOS stat.
op__ident() {
  local out
  out="$(stat -c '%d:%i' -- "$1" 2>/dev/null)" || out="$(stat -f '%d:%i' "$1" 2>/dev/null)" || return 1
  case "$out" in ''|*[!0-9:]*|:*|*:) return 1 ;; esac
  printf '%s' "$out"
}

# op_mktemp PREFIX: create the harness directory (sets OP_WORKDIR). Ownership is anchored to
# the PHYSICAL location, not to how TMPDIR is spelled (review P0-T5-r2, finding 4): the temp
# root is resolved with every symlink in every ancestor followed, the directory is created
# inside that resolved path, and the device and inode of the root and of the new directory are
# recorded. op_remove_workdir uses only these recorded values, so retargeting a symlink in
# TMPDIR afterwards cannot redirect the removal to another directory.
op_mktemp() {
  OP_TMP_ROOT="$(op__norm "${TMPDIR:-/tmp}")"
  OP_TMP_PREFIX="$1"
  OP_ROOT_PHYS=""; OP_ROOT_ID=""; OP_WORKDIR=""; OP_WORKDIR_ID=""
  local phys made rid wid
  phys="$(op__phys "$OP_TMP_ROOT")" || { op_log "cannot resolve the temp root '$OP_TMP_ROOT'"; return 1; }
  rid="$(op__ident "$phys")" || { op_log "cannot determine the identity of the temp root '$phys'"; return 1; }
  made="$(mktemp -d "${phys%/}/$1.XXXXXX")" || return 1
  made="$(op__norm "$made")"
  if ! wid="$(op__ident "$made")"; then
    rmdir -- "$made" 2>/dev/null   # just created by us and still empty
    op_log "cannot determine the identity of the new directory '$made'; removed it"
    return 1
  fi
  OP_ROOT_PHYS="$phys"; OP_ROOT_ID="$rid"; OP_WORKDIR="$made"; OP_WORKDIR_ID="$wid"
}

# op_remove_workdir: remove only the directory op_mktemp created. It refuses (status 1, nothing
# removed, a diagnostic) unless ALL of these hold: the directory was made by op_mktemp (its
# identity was recorded); it is directly under the recorded physical root with the harness
# prefix and no slash or parent reference in its name; it is a real directory, not a symlink;
# the root still resolves to itself and is the same directory (device and inode) as when the
# directory was created; and the directory itself resolves to itself and is the same
# directory (device and inode) as the one created. A symlinked, renamed, replaced or
# retargeted ancestor, or a replaced directory, therefore ends in a refusal.
# Limit: between these checks and rm there is a window no path-based check can close; it needs
# write access to the temp root's own directory.
op_remove_workdir() {
  [ -n "$OP_WORKDIR" ] || return 0
  local d base rest why=""
  d="$(op__norm "$OP_WORKDIR")"; base="${OP_ROOT_PHYS%/}"
  if [ -z "$OP_ROOT_PHYS" ] || [ -z "$OP_ROOT_ID" ] || [ -z "$OP_WORKDIR_ID" ]; then
    why="it was not created by op_mktemp (no recorded identity)"
  else
    rest="${d#"$base/$OP_TMP_PREFIX."}"
    if [ "$rest" = "$d" ] || [ -z "$rest" ] || [ "${rest#*/}" != "$rest" ] || [ "${d#*..}" != "$d" ]; then
      why="it is not directly under the recorded temp root with the harness prefix"
    elif [ -L "$d" ] || [ ! -d "$d" ]; then why="it is not a real directory"
    elif [ "$(op__phys "$OP_ROOT_PHYS")" != "$OP_ROOT_PHYS" ]; then why="the temp root no longer resolves to itself (a symlink or rename changed it)"
    elif [ "$(op__ident "$OP_ROOT_PHYS")" != "$OP_ROOT_ID" ]; then why="the temp root is not the same directory as when the workdir was created"
    elif [ "$(op__phys "$d")" != "$d" ]; then why="the path does not resolve to itself"
    elif [ "$(op__ident "$d")" != "$OP_WORKDIR_ID" ]; then why="it is not the directory that was created (different device or inode)"
    fi
  fi
  if [ -n "$why" ]; then
    op_log "refusing to remove '$d': $why"
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
