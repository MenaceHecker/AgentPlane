#!/usr/bin/env bash
# Tests for scripts/lib/owned-process.sh, the mock-smoke wrapper and the Python supervisor suites
# (make smoke-cleanup-test).
#
# The Bash PROCESS API (op_register / op_stop / traps that signalled children by PID) is RETIRED
# (ADR-007). Its process-lifecycle cases moved to the Python suites, which exercise the owning
# supervisor itself; this file keeps the directory-removal cases (their replacement/decoy
# assertions are unchanged) under the new refusal contract: the Bash helper removes an EMPTY
# directory only and RETAINS any non-empty one (review P0-T5-r3, finding 1). It also guards that no
# process control or recursive deletion remains reachable, and runs the Python suites.
set -uo pipefail
cd "$(dirname "$0")/.."

command -v python3 >/dev/null 2>&1 || { echo "ERROR: python3 not found; the owned-process tests cannot run." >&2; exit 1; }

fix="$(mktemp -d "${TMPDIR:-/tmp}/owned-process-test.XXXXXX")"
failed=0
ok()  { echo "PASS  $1"; }
bad() { echo "FAIL  $1"; failed=$((failed + 1)); }
# This test creates no processes. It removes only its own fixture directory (created above).
cleanup() { case "$fix" in "${TMPDIR:-/tmp}"/owned-process-test.*) rm -rf -- "$fix" ;; esac; }
trap cleanup EXIT

# shellcheck source=lib/owned-process.sh
. scripts/lib/owned-process.sh
diag() { "$@" 2>"$fix/diag.txt"; local rc=$?; return $rc; }
root="$fix/htmp"; mkdir -p "$root"

# the path normaliser, directly
norm_ok=1
for pair in "/a/b|/a/b" "/a/b/|/a/b" "/a/b///|/a/b" "//a//b//|/a/b" "/a b//c d///|/a b/c d" "/|/" "//|/" "///|/" "rel//x/|rel/x"; do
  in="${pair%%|*}"; want="${pair#*|}"; got="$(op__norm "$in")"
  [ "$got" = "$want" ] || { norm_ok=0; bad "op__norm '$in' gave '$got', want '$want'"; }
done
[ "$norm_ok" -eq 1 ] && ok "the path normaliser collapses repeated slashes, drops trailing ones, keeps spaces and a lone root"


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

# --- TMPDIR spelling (review r1, minor 5): creation and removal must agree whatever the spelling of the root
saved_tmpdir="${TMPDIR-__unset__}"
spell_case() { # spell_case LABEL ROOT_DIR SPELLING
  local label="$1" real="$2" spelling="$3" made
  mkdir -p "$real"; real="$(cd "$real" && pwd -P)"   # (independent of the library) the PHYSICAL path: macOS resolves /var to /private/var, and $fix itself contains a "//" because TMPDIR ends in a slash
  TMPDIR="$spelling"; OP_WORKDIR=""
  if op_mktemp spelltest && made="$OP_WORKDIR" && [ -d "$made" ] && case "$made" in "$real"/spelltest.*) true ;; *) false ;; esac; then
    if op_remove_workdir && [ ! -e "$made" ] && [ -z "$OP_WORKDIR" ]; then ok "TMPDIR spelling ($label): created, then removed, its own directory"; else bad "TMPDIR spelling ($label): could not remove its own directory '$made'"; fi
  else bad "TMPDIR spelling ($label): op_mktemp gave '$OP_WORKDIR' (wanted a directory under '$real')"; fi
  [ "$saved_tmpdir" = "__unset__" ] && unset TMPDIR || TMPDIR="$saved_tmpdir"
}
spell_case "spaces, no slash" "$fix/tmp with space" "$fix/tmp with space"
spell_case "spaces, one trailing slash" "$fix/tmp with space" "$fix/tmp with space/"
spell_case "spaces, three trailing slashes" "$fix/tmp with space" "$fix/tmp with space///"
spell_case "internal double slash and trailing slashes" "$fix/tmp with space" "$fix//tmp with space//"
spell_case "internal triple slash" "$fix/plain" "$fix///plain"
# the refusals still hold inside such a root
TMPDIR="$fix/tmp with space///"; op_mktemp spelltest; weird_root="$OP_WORKDIR"; [ "$saved_tmpdir" = "__unset__" ] && unset TMPDIR || TMPDIR="$saved_tmpdir"
mkdir -p "$fix/target2"; : >"$fix/target2/keep"; ln -s "$fix/target2" "$fix/tmp with space/spelltest.link"
OP_WORKDIR="$fix/tmp with space///spelltest.link"
if ! diag op_remove_workdir && [ -e "$fix/target2/keep" ]; then ok "TMPDIR spelling: a symlink under a spaced, slash-heavy root is still refused and its target is intact"; else bad "symlink followed under a weird root"; fi
# these directories REALLY EXIST, so only the library's own checks (not "no such directory") can refuse them
mkdir -p "$fix/tmp with space/spelltest.a/b" "$fix/tmp with space/spelltest..x"; : >"$fix/tmp with space/spelltest.a/b/keep"; : >"$fix/tmp with space/spelltest..x/keep"
OP_WORKDIR="$fix/tmp with space//spelltest.a/b"
if ! diag op_remove_workdir && [ -e "$fix/tmp with space/spelltest.a/b/keep" ]; then ok "TMPDIR spelling: an existing directory whose suffix contains a slash is still refused and intact"; else bad "slash suffix accepted"; fi
OP_WORKDIR="$fix/tmp with space//spelltest..x"
if ! diag op_remove_workdir && [ -e "$fix/tmp with space/spelltest..x/keep" ]; then ok "TMPDIR spelling: an existing directory whose name holds a parent reference is still refused and intact"; else bad "parent reference accepted"; fi
mkdir -p "$fix/tmp with space/spelltest.real"; : >"$fix/tmp with space/spelltest.real/keep"
OP_WORKDIR="$fix/tmp with space//spelltest.x/../spelltest.real"
if ! diag op_remove_workdir && [ -e "$fix/tmp with space/spelltest.real/keep" ]; then ok "TMPDIR spelling: a path that reaches an existing workdir through a parent reference is refused and intact"; else bad "parent-reference path accepted"; fi
OP_WORKDIR="$fix/tmp with space//elsewhere.zzz"; mkdir -p "$fix/tmp with space/elsewhere.zzz"
if ! diag op_remove_workdir && [ -d "$fix/tmp with space/elsewhere.zzz" ]; then ok "TMPDIR spelling: a directory with another prefix is still refused"; else bad "wrong-prefix directory removed"; fi
OP_WORKDIR="$weird_root"; op_remove_workdir >/dev/null 2>&1; OP_WORKDIR=""

# --- ancestor retargeting (review r2, finding 4). Removal must act only on the directory op_mktemp
# created, found by its recorded PHYSICAL path and checked by device and inode, never through how TMPDIR is
# spelled. Every case plants an unrelated decoy directory with the SAME basename that must survive.
restore_tmpdir() { [ "$saved_tmpdir" = "__unset__" ] && unset TMPDIR || TMPDIR="$saved_tmpdir"; }
anc_setup() { # anc_setup N -> a fresh root pair under $fix/ancN: A (real), B (real), link -> A; creates the workdir through the link
  ANC="$fix/anc$1"; mkdir -p "$ANC/A" "$ANC/B"; ln -s "$ANC/A" "$ANC/link"
  TMPDIR="$ANC/link"; op_mktemp anctest || bad "op_mktemp failed through a symlinked TMPDIR"; restore_tmpdir
  ANC_MADE="$OP_WORKDIR"; ANC_LEAF="${OP_WORKDIR##*/}"
  mkdir -p "$ANC/B/$ANC_LEAF"; echo decoy >"$ANC/B/$ANC_LEAF/decoy"   # same basename, a different directory, in the other target
}
# 1. the reviewer's case: the workdir is created through link -> A, link is retargeted to B, then it is removed
anc_setup 1
if [ -d "$ANC/A/$ANC_LEAF" ] && case "$ANC_MADE" in *"/anc1/A/"*) true ;; *) false ;; esac; then ok "ancestor retarget: the workdir is created at the resolved physical path (under A, not under the link)"; else bad "workdir not at the physical path: $ANC_MADE"; fi
ln -sfn "$ANC/B" "$ANC/link"   # retarget the TMPDIR symlink to B
if diag op_remove_workdir && [ ! -e "$ANC/A/$ANC_LEAF" ] && [ -f "$ANC/B/$ANC_LEAF/decoy" ]; then ok "ancestor retarget: the harness's own directory (in A) is the one removed; the same-named decoy in B survives"; else bad "retargeted TMPDIR link: A/leaf exists=$([ -e "$ANC/A/$ANC_LEAF" ] && echo yes || echo no) decoy=$([ -f "$ANC/B/$ANC_LEAF/decoy" ] && echo kept || echo GONE): $(cat "$fix/diag.txt")"; fi
# 2. a caller that spells the path through the retargeted link (which now leads to the decoy) is refused
anc_setup 2; ln -sfn "$ANC/B" "$ANC/link"
OP_WORKDIR="$ANC/link/$ANC_LEAF"
if ! diag op_remove_workdir && [ -f "$ANC/B/$ANC_LEAF/decoy" ] && [ -d "$ANC/A/$ANC_LEAF" ]; then ok "ancestor retarget: a path spelled through the retargeted link, which now reaches the decoy, is refused and nothing is removed"; else bad "spelled path through a retargeted link was accepted: $(cat "$fix/diag.txt")"; fi
OP_WORKDIR="$ANC_MADE"; op_remove_workdir >/dev/null 2>&1
# 3. the recorded root is renamed away and a symlink put at its old name
anc_setup 3; mv "$ANC/A" "$ANC/A.moved"; ln -s "$ANC/A.moved" "$ANC/A"
if ! diag op_remove_workdir && grep -q 'no longer resolves to itself' "$fix/diag.txt" && [ -d "$ANC/A.moved/$ANC_LEAF" ] && [ -f "$ANC/B/$ANC_LEAF/decoy" ]; then ok "ancestor retarget: a root renamed away and replaced by a symlink is refused (no longer resolves to itself)"; else bad "renamed root with a symlink in its place: $(cat "$fix/diag.txt")"; fi
# 4. the recorded root is replaced by a different real directory that holds a same-named decoy
anc_setup 4; mv "$ANC/A" "$ANC/A.old"; mkdir -p "$ANC/A/$ANC_LEAF"; echo decoy >"$ANC/A/$ANC_LEAF/decoy"
if ! diag op_remove_workdir && grep -q 'not the same directory as when' "$fix/diag.txt" && [ -f "$ANC/A/$ANC_LEAF/decoy" ] && [ -d "$ANC/A.old/$ANC_LEAF" ]; then ok "ancestor retarget: a root replaced by a different directory (new inode) is refused; its same-named decoy survives"; else bad "replaced root: $(cat "$fix/diag.txt")"; fi
# 5. the workdir itself is removed and replaced by a different directory of the same name
anc_setup 5; rmdir "$ANC/A/$ANC_LEAF"; mkdir "$ANC/A/$ANC_LEAF"; echo decoy >"$ANC/A/$ANC_LEAF/decoy"
if ! diag op_remove_workdir && grep -Eq 'not the directory that was created|is not empty' "$fix/diag.txt" && [ -f "$ANC/A/$ANC_LEAF/decoy" ]; then ok "ancestor retarget: a workdir replaced by another directory of the same name is refused; the replacement survives"; else bad "replaced workdir: $(cat "$fix/diag.txt")"; fi
# 6. a directory the library did not create (no recorded identity) is never removed, whatever it is called
OP_ROOT_PHYS=""; OP_ROOT_ID=""; OP_WORKDIR_ID=""; OP_TMP_ROOT="$fix"; OP_TMP_PREFIX="optest"
mkdir -p "$fix/optest.manual"; echo keep >"$fix/optest.manual/f"; OP_WORKDIR="$fix/optest.manual"
if ! diag op_remove_workdir && grep -q 'no recorded identity' "$fix/diag.txt" && [ -f "$fix/optest.manual/f" ]; then ok "a directory with a matching name that op_mktemp did not create is refused (no recorded identity)"; else bad "an unrecorded directory was removed: $(cat "$fix/diag.txt")"; fi
OP_WORKDIR=""


# --- the NEW refusal contract (review P0-T5-r3, finding 1): a non-empty workdir is retained, always
nonempty_case() { # nonempty_case LABEL -> make a workdir holding a file and try to remove it
  TMPDIR="$fix/ne" ; mkdir -p "$fix/ne"; op_mktemp netest || { bad "$1: op_mktemp failed"; return; }
  restore_tmpdir; NE_MADE="$OP_WORKDIR"; echo keep >"$NE_MADE/work.file"
}
nonempty_case "a normal, unmodified workdir"
if ! diag op_remove_workdir && grep -q 'is not empty' "$fix/diag.txt" && grep -q 'RETAINED' "$fix/diag.txt" && [ -f "$NE_MADE/work.file" ] && [ -n "$OP_WORKDIR" ]; then ok "a non-empty workdir that passes every identity check is still refused, retained and reported"; else bad "non-empty workdir was removed or not reported: $(cat "$fix/diag.txt")"; fi
if ! grep -q 'work.file' "$fix/diag.txt"; then ok "the refusal does not print the directory's contents"; else bad "contents printed"; fi
# the CI failure, replayed under identity reuse: make the recorded identity MATCH an unrelated replacement
nonempty_case "inode reuse"; rm -f "$NE_MADE/work.file"; rmdir "$NE_MADE"; mkdir "$NE_MADE"; echo decoy >"$NE_MADE/decoy"
OP_WORKDIR_ID="$(op__ident "$NE_MADE")"      # the worst case: the identity check is fooled (what overlayfs/ext4 do)
if ! diag op_remove_workdir && [ -f "$NE_MADE/decoy" ]; then ok "even with an identity that matches the replacement, the replacement's decoy survives (non-empty is refused)"; else bad "decoy lost under identity reuse: $(cat "$fix/diag.txt")"; fi
# an empty workdir is the only thing removed, with a nonrecursive rmdir
nonempty_case "empty"; rm -f "$NE_MADE/work.file"
if diag op_remove_workdir && [ ! -e "$NE_MADE" ] && [ -z "$OP_WORKDIR" ]; then ok "an empty workdir is removed (rmdir)"; else bad "empty workdir not removed: $(cat "$fix/diag.txt")"; fi

# --- static guards: no process control and no recursive deletion is reachable from the helper or the wrappers
helper=scripts/lib/owned-process.sh
if ! grep -nE '^[^#]*(op_register|op_stop|op_alive|op_pid|op_install_traps|builtin kill|[[:space:]]kill[[:space:]]|[[:space:]]wait[[:space:]]|jobs|[[:space:]]ps[[:space:]])' "$helper" >"$fix/g1.txt"; then ok "the Bash helper has no process API, no kill/wait, no jobs and no ps"; else bad "process control remains in the helper: $(cat "$fix/g1.txt")"; fi
if ! grep -nE '^[^#]*rm[[:space:]]+-[a-zA-Z]*r' "$helper" scripts/kind-cluster.sh scripts/mock-smoke.sh >"$fix/g2.txt"; then ok "no recursive deletion in the helper, kind-cluster.sh or the smoke wrapper"; else bad "recursive deletion found: $(cat "$fix/g2.txt")"; fi
if grep -q '^exec python3 -I -B scripts/mock_smoke.py' scripts/mock-smoke.sh && ! grep -nE '^[^#]*(kill|trap|owned-process.sh|op_|&$)' scripts/mock-smoke.sh >"$fix/g3.txt"; then ok "scripts/mock-smoke.sh only checks tools and execs the foreground Python runner"; else bad "the smoke wrapper does more than exec the runner: $(cat "$fix/g3.txt" 2>/dev/null)"; fi
if ! grep -nE '(^|[^_a-z])(os\.killpg|os\.kill\(|waitpid\(-1|subprocess|Popen)' scripts/mock_smoke.py scripts/lib/owned_workdir.py >"$fix/g4.txt"; then ok "the smoke runner and workdir module have no killpg, bare kill, waitpid(-1) or subprocess"; else bad "forbidden process call: $(cat "$fix/g4.txt")"; fi
if ! grep -nE 'shutil\.rmtree|os\.removedirs|rm -rf' scripts/lib/owned_workdir.py scripts/mock_smoke.py scripts/lib/owned_process.py >"$fix/g5.txt"; then ok "no recursive deletion in the Python runner, workdir or supervisor modules"; else bad "recursive deletion found: $(cat "$fix/g5.txt")"; fi

# --- the Python suites (ADR-007): ownership library, descriptor-owned workdir, smoke runner, integration
py_suite() { # py_suite FILE
  local out="$fix/py-$1.out"
  if python3 -I -B -m unittest discover -s scripts/tests -p "$1" >"$out" 2>&1; then ok "$1 passes ($(grep -E '^Ran ' "$out"))"
  else bad "$1 failed"; tail -40 "$out" | sed 's/^/        /'; fi
}
py_suite test_owned_process.py
py_suite test_owned_workdir.py
py_suite test_mock_smoke.py

if [ "$failed" -eq 0 ]; then echo "owned-process self-test: all passed"; else echo "owned-process self-test: $failed FAILED"; exit 1; fi
