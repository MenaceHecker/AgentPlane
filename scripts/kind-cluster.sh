#!/usr/bin/env bash
# Create or delete the named local kind cluster safely.
#
#   scripts/kind-cluster.sh up   NAME   create it (labeled topology) or accept a compatible one
#   scripts/kind-cluster.sh down NAME   delete exactly that named cluster
#
# Guarantees (tested by scripts/test-kind-cluster.sh with stub tools):
#   * Every cluster is addressed by an explicit --name and every kubectl call by an
#     explicit --context kind-NAME. The current kubectl context is never used for
#     an operation, and it is put back after anything that changes it (kind itself
#     switches it on create/export).
#   * An existing cluster is never recreated or deleted by `up`. It is accepted
#     only if its nodes match deploy/kind/config.yaml (roles, count, pool labels,
#     tenant taint, Kubernetes version); otherwise `up` fails with recovery
#     instructions, and deleting remains an explicit owner-approved step.
#   * `down` deletes only the cluster named, and only if it exists.
#   * Nothing is written into the repository and no kubeconfig is persisted here.
set -euo pipefail

cd "$(dirname "$0")/.."

# The temp directory is created and removed by the owned-process helper, which anchors
# ownership to the physical path and the directory's identity (review P0-T5-r2, finding 4),
# not to how TMPDIR is spelled.
# shellcheck source=lib/owned-process.sh
. scripts/lib/owned-process.sh

KIND_VERSION="${KIND_VERSION:-0.31.0}"          # pinned; the Makefile passes the same value
KIND_CONFIG="${KIND_CONFIG:-deploy/kind/config.yaml}"

die() { echo "kind-cluster: ERROR: $*" >&2; exit 1; }

mode="${1:-}"; name="${2:-}"
case "$mode" in up|down) ;; *) echo "usage: $0 up|down NAME" >&2; exit 2 ;; esac
case "$name" in
  ""|*[!a-z0-9-]*|-*|*-) die "cluster name must be lowercase alphanumerics and '-', not starting or ending with '-' (got '$name')" ;;
esac
ctx="kind-$name"

for tool in docker kind kubectl; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool not found. Install it (see docs/local-development.md)."
done
docker info >/dev/null 2>&1 || die "the Docker daemon is not reachable. Start Docker and retry."

have="$(kind version 2>/dev/null | sed -n 's/^kind v\([0-9][0-9.]*\).*/\1/p' | head -1)"
[ "$have" = "$KIND_VERSION" ] || die "kind $KIND_VERSION is required (found '${have:-unknown}'): the node image pinned in $KIND_CONFIG is only supported by that release. See docs/local-development.md."

# --- the current kubectl context (review r2, finding 3) -----------------------------------
# A read is only trusted if it SUCCEEDED. `kubectl config view -o jsonpath={.current-context}`
# exits 0 with empty output when there is positively no current context (a valid kubeconfig
# without one, an empty file, no file) and exits nonzero when the kubeconfig cannot be loaded;
# `kubectl config current-context` exits 1 for both, so it cannot tell them apart. An empty
# string is therefore never inferred from an error: no successful read, no conclusion.
op_mktemp kind-cluster || die "cannot create a temp directory."
work="$OP_WORKDIR"
prior=""; want=""; prior_known=""

# ctx_read: sets CTX_NOW and returns 0 only if the read succeeded (CTX_NOW empty means "no
# current context", established); returns 1 with CTX_ERR set if the read failed.
ctx_read() {
  local out
  CTX_NOW=""; CTX_ERR=""
  if ! out="$(kubectl config view -o 'jsonpath={.current-context}' 2>"$work/ctx.err")"; then
    CTX_ERR="$(tr '\n' ' ' <"$work/ctx.err")"
    return 1
  fi
  CTX_NOW="$out"
}

# restore_context: make the current context exactly $want again, and VERIFY it by reading it
# back. Returns 0 only if a successful read shows it is; a failed read is never success.
restore_context() {
  local err=""
  if ! ctx_read; then
    echo "kind-cluster: ERROR: cannot read your kubectl current context to verify it ($CTX_ERR); it may no longer be '${want:-<none>}'. Check it: kubectl config view -o 'jsonpath={.current-context}'" >&2
    return 1
  fi
  [ "$CTX_NOW" != "$want" ] || return 0
  if [ -n "$want" ]; then
    err="$(kubectl config use-context "$want" 2>&1 >/dev/null)" || true
  else
    err="$(kubectl config unset current-context 2>&1 >/dev/null)" || true
  fi
  if ! ctx_read; then   # trust the result, never the exit status of use-context or unset
    echo "kind-cluster: ERROR: restored your kubectl current context but cannot read it back to verify it ($CTX_ERR); it should be '${want:-<none>}'." >&2
    return 1
  fi
  [ "$CTX_NOW" != "$want" ] || return 0
  echo "kind-cluster: ERROR: could not restore your kubectl current context: it was '${want:-<none>}' and it is now '${CTX_NOW:-<none>}'${err:+ ($err)}." >&2
  if [ -n "$want" ]; then echo "  Restore it yourself: kubectl config use-context '$want'" >&2
  else echo "  Unset it yourself: kubectl config unset current-context" >&2; fi
  return 1
}

# One exit path for success, refusal and failure, in both modes: restore and verify the
# context (only if it was established before anything changed), remove only our temp dir,
# keep an earlier failure's status, and fail an otherwise successful run if the context could
# not be restored, read back or reported.
finish() {
  local rc=$? rrc=0
  trap - EXIT
  if [ -n "$prior_known" ]; then restore_context || rrc=1; fi
  if [ "$rrc" -ne 0 ] && [ "$rc" -eq 0 ]; then rc=1; fi
  if [ "$rc" -eq 0 ] && [ -n "$prior_known" ]; then
    if ctx_read; then
      echo "kind-cluster: your kubectl current context is '${CTX_NOW:-<none>}' (verified)."
    else
      echo "kind-cluster: ERROR: cannot read your kubectl current context to report it ($CTX_ERR)." >&2
      rc=1
    fi
  fi
  # remove our temp dir last: the reads above write their error output into it. A refusal (the
  # directory is not the one we created) leaves it alone and fails an otherwise successful run.
  if ! op_remove_workdir; then
    echo "kind-cluster: ERROR: the temp directory was not removed (see above)." >&2
    if [ "$rc" -eq 0 ]; then rc=1; fi
  fi
  exit "$rc"
}
trap finish EXIT

# Establish the prior state BEFORE anything is created, exported or deleted. If it cannot be
# established the run changes nothing.
ctx_read || die "cannot establish your current kubectl context ($CTX_ERR); refusing to change anything. Fix your kubeconfig first: kubectl config view"
prior="$CTX_NOW"; want="$prior"; prior_known=1

clusters="$(kind get clusters 2>&1)" || die "'kind get clusters' failed; refusing to guess whether '$name' exists: $clusters"
exists=""
if printf '%s\n' "$clusters" | grep -qx -- "$name"; then exists=1; fi

if [ "$mode" = down ]; then
  if [ -n "$exists" ]; then
    kind delete cluster --name "$name"
    # Deleting a cluster also removes its own context entries, so if that was the current
    # context there is nothing to go back to; any other current context must be unchanged.
    if [ "$prior" = "$ctx" ]; then want=""; fi
  else
    echo "kind cluster '$name' does not exist; nothing to do."
  fi
  exit 0
fi

# --- up
command -v go >/dev/null 2>&1 || die "go not found (it builds the topology checker)."
if [ -n "${KINDSHAPE_BIN:-}" ]; then
  shape="$KINDSHAPE_BIN"
else
  shape="$work/kindshape"
  go build -o "$shape" ./cmd/kindshape || die "could not build the topology checker."
fi
"$shape" check-config --config "$KIND_CONFIG" || die "$KIND_CONFIG is not the supported topology; not creating anything."

check_shape() { # check_shape -> 0 if the cluster's nodes match the config
  local nodes
  nodes="$(kubectl --context "$ctx" get nodes -o json 2>"$work/get-nodes.err")" || {
    echo "kind-cluster: cannot read the nodes of '$name' through context '$ctx': $(cat "$work/get-nodes.err")" >&2
    return 2
  }
  printf '%s' "$nodes" | "$shape" check-nodes --config "$KIND_CONFIG"
}

recovery() {
  cat >&2 <<MSG
kind-cluster: the existing cluster '$name' was NOT changed.
  To rebuild it with the supported topology, an owner-approved step that DELETES the cluster and its workloads:
    make kind-down KIND_CLUSTER_NAME=$name && make kind-up KIND_CLUSTER_NAME=$name
  Or choose another name, e.g. make kind-up KIND_CLUSTER_NAME=$name-2
MSG
}

if [ -n "$exists" ]; then
  # a failed read is not "the context is missing": it must not lead to an export
  contexts="$(kubectl config get-contexts -o name 2>"$work/contexts.err")" || die "cannot list your kubeconfig contexts ($(tr '\n' ' ' <"$work/contexts.err")); refusing to change anything."
  if ! printf '%s\n' "$contexts" | grep -qx -- "$ctx"; then
    echo "kind-cluster: cluster '$name' exists but kubeconfig has no context '$ctx'; exporting it."
    kind export kubeconfig --name "$name"
  fi
  rc=0; check_shape || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "kind cluster '$name' already exists and matches the supported topology; nothing to do."
    exit 0
  fi
  [ "$rc" -eq 2 ] && die "refusing to recreate '$name' because its state could not be read."
  recovery
  exit 1
fi

kind create cluster --name "$name" --config "$KIND_CONFIG"
rc=0; check_shape || rc=$?
[ "$rc" -eq 0 ] || die "the new cluster '$name' does not match the supported topology (see above); it was left in place for inspection."
echo "kind cluster '$name' created; use it with: kubectl --context $ctx ..."
