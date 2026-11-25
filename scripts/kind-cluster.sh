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

clusters="$(kind get clusters 2>&1)" || die "'kind get clusters' failed; refusing to guess whether '$name' exists: $clusters"
exists=""
if printf '%s\n' "$clusters" | grep -qx -- "$name"; then exists=1; fi

# The current kubectl context before we touch anything (local file read only).
prior="$(kubectl config current-context 2>/dev/null || true)"
restore_context() {
  local now
  now="$(kubectl config current-context 2>/dev/null || true)"
  [ "$now" != "$prior" ] || return 0
  if [ -n "$prior" ]; then
    kubectl config use-context "$prior" >/dev/null 2>&1 || echo "kind-cluster: WARNING: could not restore the kubectl context '$prior'; it is now '$now'." >&2
  else
    kubectl config unset current-context >/dev/null 2>&1 || true
  fi
}

if [ "$mode" = down ]; then
  if [ -n "$exists" ]; then
    kind delete cluster --name "$name"
    restore_context
  else
    echo "kind cluster '$name' does not exist; nothing to do."
  fi
  exit 0
fi

# --- up
work="$(mktemp -d "${TMPDIR:-/tmp}/kind-cluster.XXXXXX")"
trap 'restore_context; case "$work" in "${TMPDIR:-/tmp}"/kind-cluster.*) rm -rf -- "$work" ;; esac' EXIT
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
  if ! kubectl config get-contexts -o name 2>/dev/null | grep -qx -- "$ctx"; then
    echo "kind-cluster: cluster '$name' exists but kubeconfig has no context '$ctx'; exporting it."
    kind export kubeconfig --name "$name"
    restore_context
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
restore_context
rc=0; check_shape || rc=$?
[ "$rc" -eq 0 ] || die "the new cluster '$name' does not match the supported topology (see above); it was left in place for inspection."
echo "kind cluster '$name' created; kubectl context '$ctx' (current context left as '${prior:-<none>}')."
