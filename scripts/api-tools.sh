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

# SHA-512 of the envtest kube-apiserver and etcd binaries, taken from the official
# kubernetes-sigs/controller-tools releases envtest-v<version> after verifying each archive against
# its published .sha512 file. setup-envtest also checks the archive hash it downloads, but that hash
# comes from a moving index, so this pinned table is the check that does not move. A platform that is
# not listed is reported as BLOCKED rather than run unverified.
ENVTEST_BINARY_SHA512='
1.35.0 darwin-arm64 kube-apiserver b06dca735a91499270abb73526cfad83424db0d5452cb4df546a1de32d705f5d129547e5284a85d1f46bb6acbc94bbb358d04761dae8177254f8bb8ef494cdbd
1.35.0 linux-amd64 kube-apiserver f3bd0ead7ed969d45a7aada4cc8b26d2f74240ec2f05fa49812e1268ea6bdfe7c336bbbf285d5ad9315e260d8c38c7d06ea8ad1ec4ac863f98464fa79dc4e43b
1.35.0 darwin-arm64 etcd 9a934c1c7f13b73eacf44be45651abf9ef950c354dcfa0e7702d74b9e619b06c831bf0375341a03192852ea7aecae4b6b609348d84c08b949064943e789e25cb
1.35.0 linux-amd64 etcd 9bac0b51898f24f8bbe58531a87571f235cf9993259ee659ace3f73573d58a0b8390f6adbcdceb8fcffd3729e2f10a5a27056beac40dd976ae3db335afcdbe85
1.37.0 darwin-arm64 kube-apiserver 65be86efa7ce4a487d2b82d2235c2d56db46724e7fdb17e67d99f4da82d8d388d2919fc04d1e9d0e4069bc243ecb527a2134ef4036ad10f4035f6e36f7e0bfc3
1.37.0 linux-amd64 kube-apiserver 902e74a15cbf7e1d1927a5b45879b93657db25f6d36b90ed719905648d511cb9c101cefbdc8db07a1b5f0371f42305ef38f28430dbc0b111b52793198edb8d76
1.37.0 darwin-arm64 etcd b0434b454d98316888ce27283718e97873c3746d40662d0c4932fa641f6a457a3cdc15b768bbcd47d55225db55e6901616707c569814271ce54907f40f9fd4bb
1.37.0 linux-amd64 etcd 314790095882124ea9687c1a2811e12892d52847ef0310a07a0e244d6c7fa701ea4233eedc2ce90457e39d9538b67f2c24783cbd47c79c739876e6e7d87b4142
'

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

sha512_of() {
  if command -v sha512sum >/dev/null 2>&1; then sha512sum "$1" | awk '{print $1}'; else shasum -a 512 "$1" | awk '{print $1}'; fi
}

verify_envtest_assets() {  # verify_envtest_assets <version> <assets dir>
  local ver="$1" dir="$2" plat bin want got
  plat="$(go env GOOS)-$(go env GOARCH)"
  for bin in kube-apiserver etcd; do
    want="$(printf '%s\n' "$ENVTEST_BINARY_SHA512" | awk -v v="$ver" -v p="$plat" -v b="$bin" '$1 == v && $2 == p && $3 == b {print $4}')"
    [ -n "$want" ] || die "no pinned SHA-512 for envtest $bin $ver on $plat (BLOCKED, not a pass); add it from the official release."
    got="$(sha512_of "$dir/$bin")"
    [ "$got" = "$want" ] || die "envtest $bin $ver on $plat has SHA-512 $got, expected $want; refusing to run it."
  done
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
  verify_envtest_assets "$ver" "$assets"
  echo "test-envtest: Kubernetes $ver assets at $assets (kube-apiserver and etcd match the pinned official SHA-512)"
  local out status=0 run_tmp
  out="$(mktemp "${TMPDIR:-/tmp}/envtest-run.XXXXXX")"
  # The control plane's data and certificate directories are created under TMPDIR. A TMPDIR that
  # belongs to this run alone lets the check below recognize this run's processes by path.
  run_tmp="$(mktemp -d "${TMPDIR:-/tmp}/envtest-tmp.XXXXXX")"
  AGENTPLANE_ENVTEST=1 KUBEBUILDER_ASSETS="$assets" ENVTEST_K8S_VERSION="$ver" TMPDIR="$run_tmp" \
    go test -count=1 -v ./internal/apitest/... 2>&1 | tee "$out" || status=$?
  # Teardown check (read-only): any process still running from this run's private TMPDIR is a
  # leaked API server or etcd. They are reported with their command lines and NOT signalled: the
  # script never kills a process by a number it looked up. Stop them yourself after checking.
  local leftovers
  leftovers="$(ps -axo pid=,command= | grep -F -- "$run_tmp" | grep -v -F -- 'grep -F' || true)"
  if [ -n "$leftovers" ]; then
    echo "ERROR: processes from this run are still running (leaked envtest control plane); NOT stopped by this script:" >&2
    printf '%s\n' "$leftovers" | cut -c1-200 >&2
    echo "ERROR: kept $run_tmp for inspection." >&2
    status=1
  elif ! rmdir "$run_tmp" 2>/dev/null; then
    echo "ERROR: the run's private TMPDIR $run_tmp is not empty after the tests; kept for inspection (not deleted)." >&2
    status=1
  fi
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
