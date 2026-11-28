#!/usr/bin/env bash
# Kubernetes API scaffold tooling for P1-T1: pinned generators and the disposable-API-server tests.
# Usage: scripts/api-tools.sh tools | generate | manifests | check-generated | test-envtest
#
#   tools            install the pinned controller-gen and setup-envtest into ./bin (ignored by Git)
#   generate         regenerate the deepcopy code with the pinned controller-gen
#   manifests        regenerate the CRDs under config/crd/bases with the pinned controller-gen
#   check-generated  regenerate both and fail if anything under api/ or config/crd/ differs from the
#                    working tree or the index (it never commits or stages anything)
#   test-envtest     run the schema admission suites against a disposable local kube-apiserver and
#                    etcd of ENVTEST_K8S_VERSION (exactly 1.35.0 or 1.37.0)
#
# Everything is installed with `go install <module>@<version>` (module checksums verified by the Go
# checksum database) into ./bin. Nothing is installed globally, nothing is piped to a shell, no
# cluster or kubeconfig is read. GOTOOLCHAIN=local: Go never downloads another toolchain.
set -euo pipefail
cd "$(dirname "$0")/.."

# --- pins (Makefile exports nothing for these: they are fixed here and in the CI workflow) ---
CONTROLLER_TOOLS_VERSION="v0.23.0"                    # sigs.k8s.io/controller-tools
SETUP_ENVTEST_COMMIT="18149e6fafab1fe3966fd13a65c1d4273558afef"   # sigs.k8s.io/controller-runtime/tools/setup-envtest (resolves to v0.25.3)
SETUP_ENVTEST_MODULE_VERSION="v0.25.3"
ENVTEST_VERSIONS="1.35.0 1.37.0"

export GOTOOLCHAIN=local
bin="$PWD/bin"
cache="$bin/envtest-assets"

die() { echo "ERROR: $*" >&2; exit 1; }

require_go() {
  command -v go >/dev/null 2>&1 || die "go not found."
  local want have
  want="go$(sed -n 's/^go //p' go.mod | head -1)"
  have="$(go env GOVERSION)"
  [ "$want" = "$have" ] || die "go.mod pins $want but the go on PATH is $have (GOTOOLCHAIN=local never downloads another one)."
}

module_version() {  # module_version <binary> <module path> -> the resolved module version
  go version -m "$1" | awk -v m="$2" '$1 == "mod" && $2 == m {print $3}'
}

cmd_tools() {
  require_go
  mkdir -p "$bin"
  GOBIN="$bin" go install "sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_TOOLS_VERSION}"
  GOBIN="$bin" go install "sigs.k8s.io/controller-runtime/tools/setup-envtest@${SETUP_ENVTEST_COMMIT}"
  local v
  v="$(module_version "$bin/controller-gen" sigs.k8s.io/controller-tools)"
  [ "$v" = "$CONTROLLER_TOOLS_VERSION" ] || die "controller-gen resolved to '$v', expected $CONTROLLER_TOOLS_VERSION."
  v="$(module_version "$bin/setup-envtest" sigs.k8s.io/controller-runtime/tools/setup-envtest)"
  [ "$v" = "$SETUP_ENVTEST_MODULE_VERSION" ] || die "setup-envtest resolved to '$v', expected $SETUP_ENVTEST_MODULE_VERSION (commit $SETUP_ENVTEST_COMMIT)."
  echo "tools: controller-gen $CONTROLLER_TOOLS_VERSION and setup-envtest $SETUP_ENVTEST_MODULE_VERSION (commit $SETUP_ENVTEST_COMMIT) installed in ./bin"
}

need_controller_gen() {
  require_go
  [ -x "$bin/controller-gen" ] || die "bin/controller-gen is missing; run 'make tools'."
  local v
  v="$(module_version "$bin/controller-gen" sigs.k8s.io/controller-tools)"
  [ "$v" = "$CONTROLLER_TOOLS_VERSION" ] || die "bin/controller-gen is '$v', expected $CONTROLLER_TOOLS_VERSION; run 'make tools'."
}

cmd_generate() {
  need_controller_gen
  "$bin/controller-gen" object paths="./api/..."
  echo "generate: deepcopy code is current"
}

cmd_manifests() {
  need_controller_gen
  "$bin/controller-gen" crd paths="./api/..." output:crd:artifacts:config=config/crd/bases
  echo "manifests: CRDs are current"
}

cmd_check_generated() {
  cmd_generate
  cmd_manifests
  # Compare the regenerated tree with what is committed or staged. Untracked generated files are
  # drift too: a generated file nobody committed.
  if ! git diff --quiet -- api config/crd || [ -n "$(git ls-files --others --exclude-standard -- api config/crd)" ]; then
    echo "ERROR: generated files under api/ or config/crd/ differ from the committed ones:" >&2
    git status --short -- api config/crd >&2
    git diff --stat -- api config/crd >&2
    exit 1
  fi
  echo "check-generated: OK (nothing changed, nothing committed or staged by this check)"
}

cmd_test_envtest() {
  require_go
  local ver="${ENVTEST_K8S_VERSION:-}"
  case " $ENVTEST_VERSIONS " in
    *" $ver "*) ;;
    *) die "ENVTEST_K8S_VERSION must be exactly one of: $ENVTEST_VERSIONS (got '${ver}')." ;;
  esac
  # The suites talk only to the local API server envtest starts. Refuse the settings that would make
  # controller-runtime's envtest use an existing cluster or a kubeconfig.
  if [ "${USE_EXISTING_CLUSTER:-false}" != "false" ]; then
    die "USE_EXISTING_CLUSTER is set ('${USE_EXISTING_CLUSTER}'); these tests only use a disposable local API server."
  fi
  unset USE_EXISTING_CLUSTER KUBECONFIG
  [ -x "$bin/setup-envtest" ] || die "bin/setup-envtest is missing; run 'make tools'."
  [ "$(module_version "$bin/setup-envtest" sigs.k8s.io/controller-runtime/tools/setup-envtest)" = "$SETUP_ENVTEST_MODULE_VERSION" ] \
    || die "bin/setup-envtest is not $SETUP_ENVTEST_MODULE_VERSION; run 'make tools'."
  mkdir -p "$cache"
  local assets
  assets="$("$bin/setup-envtest" use "$ver" --bin-dir "$cache" -p path)" \
    || die "could not obtain the envtest assets for $ver (BLOCKED, not a pass)."
  [ -x "$assets/kube-apiserver" ] && [ -x "$assets/etcd" ] \
    || die "the envtest assets directory '$assets' lacks kube-apiserver or etcd (BLOCKED, not a pass)."
  echo "test-envtest: Kubernetes $ver assets at $assets"
  local out status=0
  out="$(mktemp "${TMPDIR:-/tmp}/envtest-run.XXXXXX")"
  AGENTPLANE_ENVTEST=1 KUBEBUILDER_ASSETS="$assets" ENVTEST_K8S_VERSION="$ver" \
    go test -count=1 -v ./internal/apitest/... 2>&1 | tee "$out" || status=$?
  # A skipped admission test is not a pass: the target must run every one of them.
  if grep -q -- '--- SKIP' "$out"; then
    echo "ERROR: an envtest test was skipped; that is not a pass." >&2
    status=1
  fi
  if ! grep -q -- '--- PASS' "$out"; then
    echo "ERROR: no envtest test passed." >&2
    status=1
  fi
  rm -f "$out"
  [ "$status" -eq 0 ] || exit "$status"
  echo "test-envtest: Kubernetes $ver OK"
}

case "${1:-}" in
  tools) cmd_tools ;;
  generate) cmd_generate ;;
  manifests) cmd_manifests ;;
  check-generated) cmd_check_generated ;;
  test-envtest) cmd_test_envtest ;;
  *) echo "usage: $0 tools|generate|manifests|check-generated|test-envtest" >&2; exit 2 ;;
esac
