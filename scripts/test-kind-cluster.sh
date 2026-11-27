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
echo '{"kind":"Pod"}' | "$fix/kindshape" check-nodes --config deploy/kind/config.yaml >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 2 ]; then ok "kindshape refuses a document that is not a node list (status 2)"; else bad "kindshape on a Pod exited $rc, want 2"; fi
echo '{"apiVersion":"v1","kind":"List","items":[]}' | "$fix/kindshape" check-nodes --config deploy/kind/config.yaml >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 1 ]; then ok "kindshape reports an empty node list as incompatible (status 1), never compatible"; else bad "kindshape on an empty List exited $rc, want 1"; fi
if "$fix/kindshape" check-nodes --config deploy/kind/config.yaml <internal/kindshape/testdata/kubectl-get-nodes.json >/dev/null 2>&1; then ok "kindshape accepts the committed real kubectl get nodes -o json fixture (kind List)"; else bad "kindshape rejected the real-output fixture"; fi

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
    # test hook: act like an attacker who retargets the TMPDIR symlink while the script runs and plants a
    # decoy with the same basename as the script's temp directory in the new target
    if [ -n "${STUB_RETARGET_LINK:-}" ]; then
      leaf="$(ls "$STUB_RETARGET_FROM" | grep '^kind-cluster\.' | head -1)"
      mkdir -p "$STUB_RETARGET_TO/$leaf"; echo decoy >"$STUB_RETARGET_TO/$leaf/decoy"
      ln -sfn "$STUB_RETARGET_TO" "$STUB_RETARGET_LINK"
    fi
    if [ -n "${STUB_REPLACE_WORKDIR_IN:-}" ]; then   # test hook: swap the owner's directory for an unrelated one with a decoy
      leaf="$(ls "$STUB_REPLACE_WORKDIR_IN" | grep '^kind-cluster\.' | head -1)"
      mv "$STUB_REPLACE_WORKDIR_IN/$leaf" "$STUB_REPLACE_WORKDIR_IN/$leaf.moved"
      mkdir "$STUB_REPLACE_WORKDIR_IN/$leaf"; echo decoy >"$STUB_REPLACE_WORKDIR_IN/$leaf/decoy"
    fi
    [ -n "${STUB_GET_CLUSTERS_FAIL:-}" ] && { echo "failed to list clusters" >&2; exit 1; }
    cat "$ST/clusters" ;;
  "create cluster")
    n="$(name "$@")"; echo "$n" >>"$ST/clusters"; echo "kind-$n" >>"$ST/contexts"; echo "kind-$n" >"$ST/current" ;;
  "delete cluster")
    n="$(name "$@")"; grep -vx -- "$n" "$ST/clusters" >"$ST/c.tmp"; mv "$ST/c.tmp" "$ST/clusters"
    grep -vx -- "kind-$n" "$ST/contexts" >"$ST/x.tmp"; mv "$ST/x.tmp" "$ST/contexts"
    if [ "$(cat "$ST/current")" = "kind-$n" ]; then : >"$ST/current"; fi
    if [ -n "${STUB_DELETE_SWITCH:-}" ]; then echo "$STUB_DELETE_SWITCH" >"$ST/current"; fi ;;   # a misbehaving kind that switches context
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
      current-context)
        # real kubectl exits 1 for BOTH "no current context" and an unloadable kubeconfig; only the message differs
        n=$(( $(cat "$ST/ctxreads" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$ST/ctxreads"
        for f in ${STUB_CTXREAD_FAIL_ON:-}; do
          if [ "$f" = "$n" ]; then echo "error: error loading config file \"/stub/kubeconfig\": invalid" >&2; exit 1; fi
        done
        if [ -s "$ST/current" ]; then cat "$ST/current"; else echo "error: current-context is not set" >&2; exit 1; fi ;;
      view)
        # `kubectl config view -o jsonpath={.current-context}`: exit 0 with empty output when there is no current context,
        # nonzero when the kubeconfig cannot be loaded. STUB_CTXREAD_FAIL_ON is a list of 1-based call numbers that fail.
        n=$(( $(cat "$ST/ctxreads" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$ST/ctxreads"
        for f in ${STUB_CTXREAD_FAIL_ON:-}; do
          if [ "$f" = "$n" ]; then echo "error: error loading config file \"/stub/kubeconfig\": invalid" >&2; exit 1; fi
        done
        cat "$ST/current" ;;
      use-context)
        if [ -n "${STUB_USECONTEXT_FAIL:-}" ]; then echo "error: no context exists with the name: \"$3\"" >&2; exit 1; fi
        if [ -z "${STUB_USECONTEXT_NOOP:-}" ]; then echo "$3" >"$ST/current"; fi ;;   # NOOP: claims success but changes nothing
      unset)
        if [ -n "${STUB_UNSET_FAIL:-}" ]; then echo "error: could not unset" >&2; exit 1; fi
        if [ "$3" = current-context ]; then : >"$ST/current"; fi ;;
      get-contexts)
        if [ -n "${STUB_GETCONTEXTS_FAIL:-}" ]; then echo "error: error loading config file" >&2; exit 1; fi
        cat "$ST/contexts" ;;
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
def w(n, items): json.dump({"apiVersion": "v1", "kind": "List", "items": items}, open("%s/nodes-%s.json" % (d, n), "w"))
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
  unset STUB_DOCKER_DOWN STUB_KIND_VERSION STUB_GET_CLUSTERS_FAIL STUB_GETNODES_FAIL KIND_CONFIG STUB_USECONTEXT_FAIL STUB_USECONTEXT_NOOP STUB_UNSET_FAIL STUB_DELETE_SWITCH STUB_CTXREAD_FAIL_ON STUB_GETCONTEXTS_FAIL
  export STUB_NODES="$fix/nodes-good.json"
}
run() { # run MODE NAME -> OUT, RC
  # Each run gets its own TMPDIR inside the fixture. The persistent Python owner must remove its private
  # directory itself, so a clean root after EVERY run is asserted here, not assumed; no exit-status mapping.
  mkdir -p "$fix/runtmp"
  OUT="$(TMPDIR="$fix/runtmp" PATH="$fix/bin:$PATH" KINDSHAPE_BIN="$fix/kindshape" scripts/kind-cluster.sh "$@" 2>&1)"; RC=$?
  RETAINED_DIRS="$(ls -A "$fix/runtmp" 2>/dev/null | grep -c '^kind-cluster\.')"
  if [ "$RETAINED_DIRS" -ne 0 ]; then bad "a run left its private temp directory behind (rc=$RC): $OUT"; for d in "$fix"/runtmp/kind-cluster.*; do [ -d "$d" ] && rm -rf -- "$d"; done; fi   # the test's own fixture area
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

# --- restoring the current context must be verified, and a failure must fail the operation (review r1, finding 4)
reset "$PRIOR"; export STUB_USECONTEXT_FAIL=1; run up agentplane-t
if [ $RC -ne 0 ] && [ "$(count 'kind create')" -eq 1 ] && printf '%s' "$OUT" | grep -q "could not restore your kubectl current context: it was '$PRIOR' and it is now 'kind-agentplane-t'" && ! printf '%s' "$OUT" | grep -q '(verified)' && ! printf '%s' "$OUT" | grep -q 'left as'; then ok "a failed use-context after create fails the run (rc=$RC) and reports the actual current context; no preservation is claimed"; else bad "failed restore after create (rc=$RC): $OUT"; fi
reset "$PRIOR" agentplane-t; echo "$PRIOR" >"$ST/contexts"; export STUB_USECONTEXT_FAIL=1; run up agentplane-t
if [ $RC -ne 0 ] && [ "$(count 'kind export kubeconfig')" -eq 1 ] && [ "$(count 'kind create')" -eq 0 ] && printf '%s' "$OUT" | grep -q "it is now 'kind-agentplane-t'"; then ok "a failed use-context after export fails the run and reports the actual current context"; else bad "failed restore after export (rc=$RC): $OUT"; fi
reset "$PRIOR"; export STUB_USECONTEXT_NOOP=1; run up agentplane-t
if [ $RC -ne 0 ] && printf '%s' "$OUT" | grep -q 'could not restore'; then ok "a use-context that claims success but changes nothing is caught by verifying the result"; else bad "lying use-context (rc=$RC): $OUT"; fi
reset ""; export STUB_UNSET_FAIL=1; run up agentplane-t
if [ $RC -ne 0 ] && printf '%s' "$OUT" | grep -q "it was '<none>' and it is now 'kind-agentplane-t'"; then ok "a failed unset of the current context (none before) fails the run and reports what remains"; else bad "failed unset (rc=$RC): $OUT"; fi
reset "$PRIOR"; run up agentplane-t
if [ $RC -eq 0 ] && printf '%s' "$OUT" | grep -q "your kubectl current context is '$PRIOR' (verified)" && [ "$(cur)" = "$PRIOR" ]; then ok "on success the verified current context is reported"; else bad "success message (rc=$RC): $OUT"; fi
reset "$PRIOR" agentplane-t; echo "$PRIOR" >"$ST/contexts"; export STUB_USECONTEXT_FAIL=1 STUB_NODES="$fix/nodes-single.json"; run up agentplane-t
if [ $RC -ne 0 ] && printf '%s' "$OUT" | grep -q 'was NOT changed' && printf '%s' "$OUT" | grep -q 'could not restore your kubectl current context'; then ok "an earlier failure (incompatible cluster) is preserved and the restore failure is reported as well"; else bad "preserve earlier failure (rc=$RC): $OUT"; fi
reset "$PRIOR" agentplane-t; export STUB_USECONTEXT_FAIL=1; run up agentplane-t
if [ $RC -eq 0 ] && [ "$(count 'use-context')" -eq 0 ]; then ok "when nothing changed the context, a failing use-context is never needed or called"; else bad "idempotent run with failing use-context (rc=$RC): $OUT"; fi
reset "kind-agentplane-x" agentplane-x; run down agentplane-x
if [ $RC -eq 0 ] && [ -z "$(cur)" ] && printf '%s' "$OUT" | grep -q "current context is '<none>' (verified)"; then ok "down of the cluster that was the current context leaves none, verified, with nothing to restore to"; else bad "down of the current cluster (rc=$RC, current='$(cur)'): $OUT"; fi
reset "$PRIOR" agentplane-x; export STUB_DELETE_SWITCH=some-other-context; run down agentplane-x
if [ $RC -eq 0 ] && [ "$(cur)" = "$PRIOR" ]; then ok "down restores the prior context if kind switched it"; else bad "down restore (rc=$RC, current='$(cur)'): $OUT"; fi
reset "$PRIOR" agentplane-x; export STUB_DELETE_SWITCH=some-other-context STUB_USECONTEXT_FAIL=1; run down agentplane-x
if [ $RC -ne 0 ] && printf '%s' "$OUT" | grep -q "it is now 'some-other-context'"; then ok "down fails and reports the actual context if the switched context cannot be restored"; else bad "down failed restore (rc=$RC): $OUT"; fi

# --- a FAILED read of the current context is never success (review r2, finding 3). For each scenario, count
# the reads a normal run makes, then fail EACH read in turn: the run must exit nonzero, must never claim
# "(verified)", and if the very first read (the one that establishes the prior state) fails nothing may be
# created, exported or deleted and the context must be untouched.
mutations() { grep -cE '^kind (create|delete|export)' "$ST/log" || true; }
setup_scenario() { # setup_scenario NAME -> prepares the stub state, sets SC_MODE and SC_NAME
  case "$1" in
    "create, prior context set")      reset "$PRIOR"; SC_MODE=up; SC_NAME=agentplane-t ;;
    "create, no prior context")       reset "";       SC_MODE=up; SC_NAME=agentplane-t ;;
    "existing compatible cluster")    reset "$PRIOR" agentplane-t; SC_MODE=up; SC_NAME=agentplane-t ;;
    "existing, context not in kubeconfig") reset "$PRIOR" agentplane-t; echo "$PRIOR" >"$ST/contexts"; SC_MODE=up; SC_NAME=agentplane-t ;;
    "existing incompatible cluster")  reset "$PRIOR" agentplane-t; export STUB_NODES="$fix/nodes-single.json"; SC_MODE=up; SC_NAME=agentplane-t ;;
    "down of an unrelated cluster")   reset "$PRIOR" agentplane-x; SC_MODE=down; SC_NAME=agentplane-x ;;
  esac
}
read_fail_ok=1
for sc in "create, prior context set" "create, no prior context" "existing compatible cluster" "existing, context not in kubeconfig" "existing incompatible cluster" "down of an unrelated cluster"; do
  setup_scenario "$sc"; run "$SC_MODE" "$SC_NAME"; baseline_rc=$RC; reads="$(cat "$ST/ctxreads" 2>/dev/null || echo 0)"
  if [ "$reads" -lt 2 ]; then bad "read-failure scenario '$sc': only $reads reads in a normal run"; read_fail_ok=0; continue; fi
  i=1
  while [ "$i" -le "$reads" ]; do
    setup_scenario "$sc"; export STUB_CTXREAD_FAIL_ON="$i"; run "$SC_MODE" "$SC_NAME"
    if [ $RC -eq 0 ] || printf '%s' "$OUT" | grep -q '(verified)'; then bad "[$sc] failing context read #$i of $reads: rc=$RC and/or a (verified) claim: $OUT"; read_fail_ok=0
    elif [ "$i" -eq 1 ] && { [ "$(mutations)" -ne 0 ] || [ "$(cur)" != "$(if [ "$sc" = "create, no prior context" ]; then echo ""; else echo "$PRIOR"; fi)" ] || ! printf '%s' "$OUT" | grep -q 'cannot establish your current kubectl context'; }; then bad "[$sc] the first read failed but the run changed something or gave the wrong message (mutations=$(mutations), current='$(cur)'): $OUT"; read_fail_ok=0
    elif [ "$i" -eq "$reads" ] && ! printf '%s' "$OUT" | grep -qE 'cannot read your kubectl current context to report it|cannot read your kubectl current context to verify'; then bad "[$sc] the last read (#$i, the report) failed but no read error was reported: $OUT"; read_fail_ok=0; fi
    i=$((i + 1))
  done
done
[ "$read_fail_ok" -eq 1 ] && ok "every failing context read (at capture, at restore, at read-back and at the final report) in six scenarios is a nonzero exit, never a (verified) claim, and a failed first read changes nothing"
# the fixed script never uses `kubectl config current-context` (it cannot tell "not set" from "unreadable")
setup_scenario "create, prior context set"; run up agentplane-t
if ! grep -q '^kubectl config current-context' "$ST/log" && grep -q '^kubectl config view -o jsonpath' "$ST/log"; then ok "the script reads the current context only with config view (exit status separates absence from failure), never with config current-context"; else bad "the script used config current-context or never read the context: $(grep '^kubectl config' "$ST/log" | head -3)"; fi
# the reviewer's two probes, named: A) initial read fails although a prior context exists, B) no prior context, create switches it, later reads fail
setup_scenario "create, prior context set"; export STUB_CTXREAD_FAIL_ON="1"; run up agentplane-t
if [ $RC -ne 0 ] && [ "$(count 'kind create')" -eq 0 ] && [ "$(cur)" = "$PRIOR" ] && ! grep -q 'unset' "$ST/log"; then ok "probe A: an unreadable initial context aborts before any mutation and the existing context is not unset"; else bad "probe A (rc=$RC, current='$(cur)'): $OUT"; fi
setup_scenario "create, no prior context"; export STUB_CTXREAD_FAIL_ON="2 3 4"; run up agentplane-t
if [ $RC -ne 0 ] && [ "$(cur)" = "kind-agentplane-t" ] && ! printf '%s' "$OUT" | grep -q '(verified)'; then ok "probe B: after create switched an empty context, unreadable follow-up reads fail the run and the real remaining context is not claimed restored"; else bad "probe B (rc=$RC, current='$(cur)'): $OUT"; fi
setup_scenario "existing, context not in kubeconfig"; export STUB_GETCONTEXTS_FAIL=1; run up agentplane-t
if [ $RC -ne 0 ] && [ "$(count 'kind export')" -eq 0 ] && printf '%s' "$OUT" | grep -q 'cannot list your kubeconfig contexts'; then ok "a failing contexts listing is not treated as a missing context: no export, the run fails"; else bad "failed get-contexts (rc=$RC): $OUT"; fi
# confirmed absence still works: no current context, readable kubeconfig, create switches it, restore unsets it
setup_scenario "create, no prior context"; run up agentplane-t
if [ $RC -eq 0 ] && [ -z "$(cur)" ] && printf '%s' "$OUT" | grep -q "current context is '<none>' (verified)"; then ok "a positively established absence of a current context is still handled and verified"; else bad "confirmed absence (rc=$RC, current='$(cur)'): $OUT"; fi
# the real kubectl contract the stub models, on throwaway kubeconfig files (a local read; no cluster, no network)
if command -v kubectl >/dev/null 2>&1; then
  kc="$fix/kc"; mkdir -p "$kc"
  printf 'apiVersion: v1\nkind: Config\nclusters: [{name: c1, cluster: {server: "https://127.0.0.1:1"}}]\nusers: [{name: u1, user: {}}]\ncontexts: [{name: ctx1, context: {cluster: c1, user: u1}}]\ncurrent-context: ctx1\n' >"$kc/set.yaml"
  grep -v '^current-context' "$kc/set.yaml" >"$kc/unset.yaml"; : >"$kc/empty.yaml"; printf 'apiVersion: v1\nkind: Config\ncurrent-context: [unclosed\n' >"$kc/invalid.yaml"
  real_ok=1
  read_real() { KUBECONFIG="$1" kubectl config view -o 'jsonpath={.current-context}' 2>/dev/null; }
  [ "$(read_real "$kc/set.yaml")" = "ctx1" ] || real_ok=0
  for f in unset.yaml empty.yaml nonexistent.yaml; do KUBECONFIG="$kc/$f" kubectl config view -o 'jsonpath={.current-context}' >"$kc/out" 2>/dev/null; rc=$?; { [ $rc -eq 0 ] && [ ! -s "$kc/out" ]; } || real_ok=0; done
  KUBECONFIG="$kc/invalid.yaml" kubectl config view -o 'jsonpath={.current-context}' >/dev/null 2>&1; [ $? -ne 0 ] || real_ok=0
  [ "$real_ok" -eq 1 ] && ok "real kubectl: an unset, empty or missing kubeconfig reads as exit 0 with no output, a corrupt one as a nonzero exit (the contract the stub models)" || bad "real kubectl does not behave as the stub models"
else ok "real-kubectl contract check skipped (kubectl not installed)"; fi

# --- the script's own temp directory: retargeting the TMPDIR symlink mid-run must not redirect anything.
# The owner holds the parent and child descriptors, so removal goes through them: the script's own directory
# (in A) is removed, the same-named decoy in B is untouched, and the run succeeds.
rt="$fix/retarget"; mkdir -p "$rt/A" "$rt/B"; ln -s "$rt/A" "$rt/link"
reset "$PRIOR" agentplane-t
OUT="$(TMPDIR="$rt/link" STUB_RETARGET_LINK="$rt/link" STUB_RETARGET_FROM="$rt/A" STUB_RETARGET_TO="$rt/B" PATH="$fix/bin:$PATH" KINDSHAPE_BIN="$fix/kindshape" scripts/kind-cluster.sh up agentplane-t 2>&1)"; RC=$?
decoys="$(ls "$rt/B" | grep -c '^kind-cluster\.')"; leftovers="$(ls "$rt/A" | grep -c '^kind-cluster\.')"
if [ $RC -eq 0 ] && [ "$decoys" -eq 1 ] && [ -f "$rt/B/$(ls "$rt/B" | grep '^kind-cluster\.' | head -1)/decoy" ] && [ "$leftovers" -eq 0 ]; then ok "kind-cluster.sh: a TMPDIR symlink retargeted mid-run removes the script's own directory (in A) through its held descriptors and leaves the same-named decoy in B untouched (exit 0)"; else bad "retargeted TMPDIR (rc=$RC, decoys=$decoys, left-in-A=$leftovers): $OUT"; fi

# --- successful kind operations return 0 only after owned cleanup (review P0-T5-r4, finding 1)
reset "$PRIOR" agentplane-t; mkdir -p "$fix/rtn"
OUT="$(TMPDIR="$fix/rtn" PATH="$fix/bin:$PATH" KINDSHAPE_BIN="$fix/kindshape" scripts/kind-cluster.sh up agentplane-t 2>&1)"; RC=$?
if [ $RC -eq 0 ] && [ -z "$(ls -A "$fix/rtn")" ] && printf '%s' "$OUT" | grep -q "your kubectl current context is '$PRIOR' (verified)"; then ok "a successful up exits 0 with the context verified and the private temp directory gone"; else bad "successful up (rc=$RC, root: $(ls -A "$fix/rtn" | tr '\n' ' ')): $OUT"; fi
reset "$PRIOR" agentplane-t agentplane-x; OUT="$(TMPDIR="$fix/rtn" PATH="$fix/bin:$PATH" KINDSHAPE_BIN="$fix/kindshape" scripts/kind-cluster.sh down agentplane-x 2>&1)"; RC=$?
if [ $RC -eq 0 ] && [ -z "$(ls -A "$fix/rtn")" ]; then ok "a successful down exits 0 and leaves nothing"; else bad "successful down (rc=$RC): $OUT"; fi
reset "$PRIOR" agentplane-t; OUT="$(TMPDIR="$fix/rtn" PATH="$fix/bin:$PATH" KINDSHAPE_BIN="$fix/kindshape" scripts/kind-cluster.sh up agentplane-t 2>&1)"; RC=$?
if [ $RC -eq 0 ] && [ -z "$(ls -A "$fix/rtn")" ] && printf '%s' "$OUT" | grep -q 'already exists and matches'; then ok "an idempotent up on a compatible cluster exits 0 and leaves nothing"; else bad "idempotent up (rc=$RC): $OUT"; fi
# a FAILED operation keeps its own status and message and leaves nothing behind
reset "$PRIOR" agentplane-t; export STUB_GET_CLUSTERS_FAIL=1
OUT="$(TMPDIR="$fix/rtn" PATH="$fix/bin:$PATH" KINDSHAPE_BIN="$fix/kindshape" scripts/kind-cluster.sh down agentplane-t 2>&1)"; RC=$?
unset STUB_GET_CLUSTERS_FAIL
if [ $RC -eq 1 ] && printf '%s' "$OUT" | grep -q "'kind get clusters' failed" && [ -z "$(ls -A "$fix/rtn")" ]; then ok "a failed operation keeps its status 1 and message and the owned directory is still removed"; else bad "failed operation (rc=$RC): $OUT"; fi
# the owned directory is REPLACED mid-run by an unrelated directory with a decoy: cleanup must refuse, retain the original, never touch the decoy
reset "$PRIOR" agentplane-t; rm -rf -- "${fix:?}/rtn"; mkdir -p "$fix/rtn"
OUT="$(TMPDIR="$fix/rtn" STUB_REPLACE_WORKDIR_IN="$fix/rtn" PATH="$fix/bin:$PATH" KINDSHAPE_BIN="$fix/kindshape" scripts/kind-cluster.sh up agentplane-t 2>&1)"; RC=$?
leaf="$(ls "$fix/rtn" | grep '^kind-cluster\.' | grep -v moved | head -1)"
if [ $RC -ne 0 ] && [ -f "$fix/rtn/$leaf/decoy" ] && [ -n "$(ls -d "$fix"/rtn/kind-cluster.*.moved 2>/dev/null)" ] && printf '%s' "$OUT" | grep -q 'replaced'; then ok "a workdir replaced mid-run fails the run, keeps the original (renamed) and never touches the replacement's decoy"; else bad "replaced workdir (rc=$RC leaf=$leaf): $OUT"; fi
rm -rf -- "${fix:?}/rtn"

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
