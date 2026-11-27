#!/usr/bin/env bash
# Create or delete the named local kind cluster safely.
#
#   scripts/kind-cluster.sh up   NAME   create it (labeled topology) or accept a compatible one
#   scripts/kind-cluster.sh down NAME   delete exactly that named cluster
#
# Thin wrapper (ADR-007 follow-up): it validates the arguments, supplies the pinned defaults and
# REPLACES itself (exec) with the foreground Python owner scripts/kind_cluster.py, which holds the
# tools it starts and its private temporary directory for the whole run and returns 0 only after a
# verified context restoration, collected children and owned cleanup. Nothing here starts a process
# in the background, signals anything or deletes anything; see scripts/kind_cluster.py for the
# guarantees and exit codes. Needs python3 (3.8+).
set -euo pipefail

cd "$(dirname "$0")/.."

KIND_VERSION="${KIND_VERSION:-0.31.0}"          # pinned; the Makefile passes the same value
KIND_CONFIG="${KIND_CONFIG:-deploy/kind/config.yaml}"
export KIND_VERSION KIND_CONFIG

die() { echo "kind-cluster: ERROR: $*" >&2; exit 1; }

mode="${1:-}"; name="${2:-}"
case "$mode" in up|down) ;; *) echo "usage: $0 up|down NAME" >&2; exit 2 ;; esac
case "$name" in
  ""|*[!a-z0-9-]*|-*|*-) die "cluster name must be lowercase alphanumerics and '-', not starting or ending with '-' (got '$name')" ;;
esac

command -v python3 >/dev/null 2>&1 || die "python3 not found (it runs the kind owner)."
exec python3 -I -B scripts/kind_cluster.py "$mode" "$name"
