#!/usr/bin/env bash
# Deterministic tests for scripts/kind-cluster.sh and the topology checker
# (make kind-config-test). No Docker, no cluster, no network: docker, kind and
# kubectl are stubs that record every call, and kubectl FAILS if it is ever
# invoked on a cluster without an explicit --context (so a fallback to the
# current context cannot pass). The stub `kind create` switches the stub
# current context exactly as real kind does, so context restoration is tested.
set -uo pipefail
cd "$(dirname "$0")/.."

command -v go >/dev/null 2>&1 || { echo "ERROR: go not found; the kind config tests cannot run." >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "ERROR: python3 not found; the kind config tests cannot run." >&2; exit 1; }

fix="$(mktemp -d "${TMPDIR:-/tmp}/kind-config-test.XXXXXX")"
trap 'case "$fix" in "${TMPDIR:-/tmp}"/kind-config-test.*) rm -rf -- "$fix" ;; esac' EXIT
failed=0
ok()  { echo "PASS  $1"; }
bad() { echo "FAIL  $1"; failed=$((failed + 1)); }

# --- the Go checks first (topology derived from the committed config, node fixtures)
if go test -count=1 ./internal/kindshape/ ./cmd/kindshape/ >"$fix/go.out" 2>&1; then ok "go test ./internal/kindshape: committed topology, node-shape cases and config-shape cases"; else bad "go tests failed: $(tail -5 "$fix/go.out")"; fi
go build -o "$fix/kindshape" ./cmd/kindshape || { echo "cannot build kindshape" >&2; exit 1; }
if "$fix/kindshape" check-config --config deploy/kind/config.yaml >/dev/null 2>&1; then ok "kindshape accepts the committed config"; else bad "kindshape rejected the committed config"; fi
"$fix/kindshape" check-nodes --config deploy/kind/config.yaml </dev/null >/dev/null 2>"$fix/e"; rc=$?
if [ "$rc" -eq 2 ]; then ok "kindshape refuses empty input (status 2), never treating it as compatible"; else bad "kindshape on empty input exited $rc, want 2"; fi
echo '{"kind":"List"}' | "$fix/kindshape" check-nodes --config deploy/kind/config.yaml >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 2 ]; then ok "kindshape refuses a non-NodeList document (status 2)"; else bad "kindshape on a non-NodeList exited $rc, want 2"; fi

# --- stub tools
mkdir -p "$fix/bin" "$fix/st"
cat >"$fix/bin/docker" <<'SH'
#!/usr/bin/env bash
echo "docker $*" >>"$ST/log"
[ "$1" = info ] && [ -n "${STUB_DOCKER_DOWN:-}" ] && { echo "Cannot connect to the Docker daemon" >&2; exit 1; }
exit 0
SH
cat >"$fix/bin/kind" <<'SH'
#!/usr/bin/env bash
echo "kind $*" >>"$ST/log"
name() { while [ $# -gt 0 ]; do [ "$1" = --name ] && { echo "$2"; return; }; shift; done; }
case "$1 $2" in
  "version "*|"version ") echo "kind v${STUB_KIND_VERSION:-0.31.0} go1.25.5 darwin/arm64" ;;
  "get clusters")
    [ -n "${STUB_GET_CLUSTERS_FAIL:-}" ] && { echo "failed to list clusters" >&2; exit 1; }
    cat "$ST/clusters" ;;
  "create cluster")
    n="$(name "$@")"; echo "$n" >>"$ST/clusters"; echo "kind-$n" >>"$ST/contexts"; echo "kind-$n" >"$ST/current" ;;
  "delete cluster")
    n="$(name "$@")"; grep -vx -- "$n" "$ST/clusters" >"$ST/c.tmp"; mv "$ST/c.tmp" "$ST/clusters"
    grep -vx -- "kind-$n" "$ST/contexts" >"$ST/x.tmp"; mv "$ST/x.tmp" "$ST/contexts"
    if [ "$(cat "$ST/current")" = "kind-$n" ]; then : >"$ST/current"; fi ;;
  "export kubeconfig")
    n="$(name "$@")"; echo "kind-$n" >>"$ST/contexts"; echo "kind-$n" >"$ST/current" ;;
  *) echo "stub kind: unexpected: $*" >&2; exit 99 ;;
esac
SH
cat >"$fix/bin/kubectl" <<'SH'
#!/usr/bin/env bash
echo "kubectl $*" >>"$ST/log"
case "$1" in
  config)
    case "$2" in
      current-context) [ -s "$ST/current" ] && cat "$ST/current" || { echo "error: current-context is not set" >&2; exit 1; } ;;
      use-context) echo "$3" >"$ST/current" ;;
      unset) if [ "$3" = current-context ]; then : >"$ST/current"; fi ;;
      get-contexts) cat "$ST/contexts" ;;
      *) echo "stub kubectl: unexpected config $*" >&2; exit 99 ;;
    esac ;;
  --context)
    shift 2
    if [ "$1 $2 $3 $4" = "get nodes -o json" ]; then
      [ -n "${STUB_GETNODES_FAIL:-}" ] && { echo "Unable to connect to the server" >&2; exit 1; }
      cat "$STUB_NODES"
    else echo "stub kubectl: unexpected targeted call: $*" >&2; exit 99; fi ;;
  *) echo "UNTARGETED kubectl $*" >>"$ST/violations"; echo "kubectl used the current context" >&2; exit 99 ;;
esac
SH
chmod +x "$fix/bin/docker" "$fix/bin/kind" "$fix/bin/kubectl"

# --- node fixtures in the shape of `kubectl get nodes -o json`
python3 -I - "$fix" <<'PY'
import json, sys
d = sys.argv[1]
def node(name, ver, labels, taints=()):
    return {"apiVersion": "v1", "kind": "Node", "metadata": {"name": name, "labels": labels},
            "spec": {"taints": list(taints)}, "status": {"nodeInfo": {"kubeletVersion": ver}}}
cp = node("c-control-plane", "v1.35.0", {"node-role.kubernetes.io/control-plane": ""},
          [{"key": "node-role.kubernetes.io/control-plane", "effect": "NoSchedule"}])
sysw = node("c-worker", "v1.35.0", {"agentplane.io/pool": "system"})
ten = node("c-worker2", "v1.35.0", {"agentplane.io/pool": "tenant"},
           [{"key": "agentplane.io/pool", "value": "tenant", "effect": "NoSchedule"}])
def w(n, items): json.dump({"apiVersion": "v1", "kind": "NodeList", "items": items}, open("%s/nodes-%s.json" % (d, n), "w"))
w("good", [cp, sysw, ten])
w("single", [cp])                                     # a default one-node kind cluster
tn = json.loads(json.dumps(ten)); tn["spec"]["taints"] = []
w("notaint", [cp, sysw, tn])
old = json.loads(json.dumps(sysw)); old["status"]["nodeInfo"]["kubeletVersion"] = "v1.34.3"
w("oldver", [cp, old, ten])
PY
# a config that is not the supported shape (tag without a digest)
sed 's#@sha256:[0-9a-f]\{64\}##' deploy/kind/config.yaml >"$fix/bad-config.yaml"

reset() { # reset PRIOR_CONTEXT CLUSTERS...
  local prior="$1"; shift
  export ST="$fix/st"
  find "${ST:?}" -mindepth 1 -maxdepth 1 -type f -delete
  : >"$ST/log"; : >"$ST/violations"; : >"$ST/clusters"; : >"$ST/contexts"; : >"$ST/current"
  [ -z "$prior" ] || { echo "$prior" >"$ST/current"; echo "$prior" >"$ST/contexts"; }
  local c; for c in "$@"; do echo "$c" >>"$ST/clusters"; echo "kind-$c" >>"$ST/contexts"; done
  unset STUB_DOCKER_DOWN STUB_KIND_VERSION STUB_GET_CLUSTERS_FAIL STUB_GETNODES_FAIL KIND_CONFIG
  export STUB_NODES="$fix/nodes-good.json"
}
run() { # run MODE NAME -> OUT, RC
  OUT="$(PATH="$fix/bin:$PATH" KINDSHAPE_BIN="$fix/kindshape" scripts/kind-cluster.sh "$@" 2>&1)"; RC=$?
}
count() { grep -c -- "$1" "$ST/log" || true; }   # count log lines matching a fixed string
cur() { cat "$ST/current"; }
untargeted() { [ ! -s "$ST/violations" ]; }
all_targeted() { # every kubectl call other than `config ...` carries --context kind-NAME first
  ! grep '^kubectl ' "$ST/log" | grep -v '^kubectl config ' | grep -qv "^kubectl --context kind-$1 "
}

PRIOR="gke_prod_example"   # stands for an unrelated, possibly production context that must never be touched

reset "$PRIOR"; run up agentplane-t
if [ $RC -eq 0 ] && [ "$(count 'kind create cluster --name agentplane-t --config deploy/kind/config.yaml')" -eq 1 ] && [ "$(cur)" = "$PRIOR" ]; then ok "up creates the named cluster from the labeled config and restores the prior kubectl context (kind switched it)"; else bad "up/create (rc=$RC, current=$(cur)): $OUT"; fi
if untargeted && all_targeted agentplane-t && ! grep '^kubectl --context' "$ST/log" | grep -q "$PRIOR"; then ok "every kubectl operation named --context kind-agentplane-t; none used the current or the unrelated context"; else bad "a kubectl call was not explicitly targeted: $(cat "$ST/log")"; fi

reset "$PRIOR" agentplane-t; run up agentplane-t
if [ $RC -eq 0 ] && [ "$(count 'kind create')" -eq 0 ] && [ "$(count 'kind delete')" -eq 0 ] && [ "$(cur)" = "$PRIOR" ] && printf '%s' "$OUT" | grep -q 'already exists and matches'; then ok "an existing compatible cluster is accepted with no create, no delete and the context untouched"; else bad "idempotent up (rc=$RC): $OUT"; fi
if untargeted && all_targeted agentplane-t; then ok "the idempotent run also used only explicit --context targeting"; else bad "idempotent run kubectl targeting: $(cat "$ST/log")"; fi

for fx in single notaint oldver; do
  reset "$PRIOR" agentplane-t; export STUB_NODES="$fix/nodes-$fx.json"; run up agentplane-t
  if [ $RC -eq 1 ] && [ "$(count 'kind create')" -eq 0 ] && [ "$(count 'kind delete')" -eq 0 ] && [ "$(cur)" = "$PRIOR" ] \
     && printf '%s' "$OUT" | grep -q 'kindshape: incompatible' && printf '%s' "$OUT" | grep -q 'make kind-down KIND_CLUSTER_NAME=agentplane-t' && printf '%s' "$OUT" | grep -q 'owner-approved'; then
    ok "incompatible existing cluster ($fx): refused, nothing created or deleted, recovery needs owner-approved deletion"
  else bad "incompatible cluster $fx (rc=$RC): $OUT"; fi
done

reset "$PRIOR" agentplane-t; export STUB_GETNODES_FAIL=1; run up agentplane-t
if [ $RC -eq 1 ] && [ "$(count 'kind create')" -eq 0 ] && [ "$(count 'kind delete')" -eq 0 ] && printf '%s' "$OUT" | grep -q 'refusing to recreate'; then ok "an unreadable existing cluster is never recreated"; else bad "unreachable cluster (rc=$RC): $OUT"; fi

reset "$PRIOR"; export STUB_GET_CLUSTERS_FAIL=1; run up agentplane-t
if [ $RC -eq 1 ] && [ "$(count 'kind create')" -eq 0 ] && printf '%s' "$OUT" | grep -q 'refusing to guess'; then ok "'kind get clusters' failing creates nothing"; else bad "get clusters failure (rc=$RC): $OUT"; fi

reset "$PRIOR"; export STUB_KIND_VERSION=0.30.0; run up agentplane-t
if [ $RC -eq 1 ] && [ "$(count 'kind create')" -eq 0 ] && [ "$(count 'kind get clusters')" -eq 0 ] && printf '%s' "$OUT" | grep -q 'kind 0.31.0 is required'; then ok "an unsupported kind version is refused before any cluster is listed or created"; else bad "kind version gate (rc=$RC): $OUT"; fi

reset "$PRIOR"; export STUB_DOCKER_DOWN=1; run up agentplane-t
if [ $RC -eq 1 ] && [ "$(count 'kind ')" -eq 0 ] && printf '%s' "$OUT" | grep -q 'Docker daemon is not reachable'; then ok "an unreachable Docker daemon is refused with no kind call"; else bad "docker down (rc=$RC): $OUT"; fi

reset "$PRIOR"; export KIND_CONFIG="$fix/bad-config.yaml"; run up agentplane-t
if [ $RC -eq 1 ] && [ "$(count 'kind create')" -eq 0 ] && printf '%s' "$OUT" | grep -q 'pinned by an @sha256 digest'; then ok "a config whose node image is not digest-pinned is refused before creating anything"; else bad "bad config (rc=$RC): $OUT"; fi

reset ""; run up agentplane-t
if [ $RC -eq 0 ] && [ -z "$(cur)" ]; then ok "with no prior kubectl context, none is left set after creation"; else bad "empty prior context (rc=$RC, current='$(cur)'): $OUT"; fi

reset "$PRIOR" agentplane-t; echo "$PRIOR" >"$ST/contexts"; run up agentplane-t
if [ $RC -eq 0 ] && [ "$(count 'kind export kubeconfig --name agentplane-t')" -eq 1 ] && [ "$(count 'kind create')" -eq 0 ] && [ "$(cur)" = "$PRIOR" ]; then ok "an existing cluster missing from kubeconfig is exported, the prior context restored, nothing recreated"; else bad "export path (rc=$RC, current=$(cur)): $OUT"; fi

names_ok=1
for badname in "" "Agent" "a;b" "-a" "a-" "a b" 'a$(id)' "a/b"; do
  reset "$PRIOR" agentplane-t; run up "$badname"
  if [ $RC -ne 0 ] && [ "$(count 'kind ')" -eq 0 ] && [ "$(count 'kubectl --context')" -eq 0 ]; then :; else names_ok=0; bad "invalid name '$badname' was not refused (rc=$RC): $OUT"; fi
done
[ "$names_ok" -eq 1 ] && ok "invalid cluster names (empty, uppercase, ';', leading/trailing '-', space, command substitution, '/') are refused before any tool runs"

# down: exactly the named cluster, nothing else
reset "$PRIOR" other agentplane-x third; run down agentplane-x
if [ $RC -eq 0 ] && [ "$(count 'kind delete')" -eq 1 ] && [ "$(count 'kind delete cluster --name agentplane-x')" -eq 1 ] && [ "$(grep -c . "$ST/clusters")" -eq 2 ] && grep -qx other "$ST/clusters" && grep -qx third "$ST/clusters" && [ "$(cur)" = "$PRIOR" ]; then ok "down deletes exactly the named cluster; unrelated clusters and the prior context are unchanged"; else bad "down (rc=$RC): $OUT; clusters: $(cat "$ST/clusters")"; fi
reset "$PRIOR" other; run down agentplane-x
if [ $RC -eq 0 ] && [ "$(count 'kind delete')" -eq 0 ] && grep -qx other "$ST/clusters"; then ok "down on a missing cluster deletes nothing"; else bad "down missing (rc=$RC): $OUT"; fi
reset "$PRIOR" agentplane-x; export STUB_GET_CLUSTERS_FAIL=1; run down agentplane-x
if [ $RC -eq 1 ] && [ "$(count 'kind delete')" -eq 0 ]; then ok "down deletes nothing when the cluster list cannot be read"; else bad "down with list failure (rc=$RC): $OUT"; fi

if [ "$failed" -eq 0 ]; then echo "kind-config self-test: all passed"; else echo "kind-config self-test: $failed FAILED"; exit 1; fi
