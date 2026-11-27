#!/usr/bin/env bash
# Temporary-directory helper for scripts that need a private working directory (sourced).
#
# The PROCESS API that used to live here (op_register, op_stop, op_alive and the traps that
# signalled registered children by PID after a ps/job-table identity check) is RETIRED
# (ADR-007, review P0-T5-r2 finding 1, P0-T5-r3): a PID plus a start time is not a unique
# identity, and the check was not atomic. Processes are now owned only by the foreground
# Python supervisor in scripts/lib/owned_process.py, which is the sole parent, waiter and
# signaller of its children. This file starts and signals nothing.
#
# What remains is op_mktemp / op_remove_workdir for callers that cannot use the descriptor-owned
# Python workdir (scripts/lib/owned_workdir.py). Removal is deliberately limited: see
# op_remove_workdir. It never deletes recursively. Compatible with bash 3.2 (macOS /bin/bash).

OP_TMP_ROOT=""
OP_TMP_PREFIX=""
OP_WORKDIR=""
OP_ROOT_PHYS=""    # the temp root as resolved when op_mktemp ran
OP_ROOT_ID=""      # its device:inode
OP_WORKDIR_ID=""   # device:inode of the directory op_mktemp created

op_log() { echo "owned-process: $*" >&2; }

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

# op_remove_workdir: remove only the EMPTY directory op_mktemp created, with a nonrecursive rmdir.
# It refuses (status 1, nothing removed, a diagnostic) unless ALL of these hold: the directory
# was made by op_mktemp (its identity was recorded); it is directly under the recorded physical
# root with the harness prefix and no slash or parent reference in its name; it is a real
# directory, not a symlink; the root still resolves to itself and is the same directory
# (device and inode) as when the directory was created; the directory itself resolves to itself
# and has the recorded device and inode; AND it is empty.
#
# A non-empty directory is ALWAYS refused and RETAINED, even when every identity check passes
# (review P0-T5-r3, finding 1). The recorded device:inode is not unique over time: on overlayfs
# and ext4 an unrelated replacement directory created after the original was removed can have
# the SAME pair (measured: 199/200 and 200/200 tries; see agents/runs/P0-T5.md), so no number
# recorded before can tell the original from a replacement, and deleting a non-empty directory on
# that evidence deleted the replacement's files. This helper has no persistent directory
# descriptor across its external commands, so it cannot do better; there is no recursive removal
# anywhere in it. The descriptor-owned workdir in scripts/lib/owned_workdir.py is the safe
# mechanism and is used by the smoke runner. Callers of this helper must treat a refusal as a
# failure and leave the directory for the owner to inspect and remove.
# Limit: between the checks and rmdir there is a window no path-based check can close; it needs
# write access to the temp root's own directory. Only an empty directory can be affected.
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
  if [ -n "$(ls -A -- "$d" 2>/dev/null)" ]; then
    op_log "refusing to remove '$d': it is not empty, so it is RETAINED for inspection (automatic removal of a non-empty directory needs a persistent descriptor owner; see ADR-007). Its contents are not printed."
    return 1
  fi
  if ! rmdir -- "$d" 2>/dev/null; then
    op_log "could not remove '$d' with rmdir; it is retained"
    return 1
  fi
  OP_WORKDIR=""
}
