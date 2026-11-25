#!/usr/bin/env bash
# Make-level negative controls (make make-args-test). A value given to make on the command line or
# in the environment must reach the scripts as DATA: quotes, command and Make substitutions and shell
# metacharacters must never execute anything, and an invalid value must be refused before any tool
# runs. The real Makefile and scripts are copied into a fixture; docker, kind, kubectl, trivy and go
# are logging stubs, so nothing real runs. A "marker" file is created if any payload ever executes.
set -uo pipefail
cd "$(dirname "$0")/.."

fix="$(mktemp -d "${TMPDIR:-/tmp}/make-args-test.XXXXXX")"
trap 'case "$fix" in "${TMPDIR:-/tmp}"/make-args-test.*) rm -rf -- "$fix" ;; esac' EXIT
failed=0; checks=0
ok()  { checks=$((checks + 1)); }
bad() { checks=$((checks + 1)); failed=$((failed + 1)); echo "FAIL  $1"; }

repo="$fix/repo"; mkdir -p "$repo/scripts/lib" "$fix/bin" "$fix/log"
cp Makefile "$repo/"; cp scripts/kind-cluster.sh scripts/scan-mock-image.sh "$repo/scripts/"; cp scripts/lib/evaluate_trivy.py "$repo/scripts/lib/"
mkdir -p "$repo/deploy/kind" "$repo/mockmodel"; cp deploy/kind/config.yaml "$repo/deploy/kind/"; : >"$repo/mockmodel/Dockerfile"
for tool in docker kind kubectl trivy go; do
  cat >"$fix/bin/$tool" <<'SH'
#!/usr/bin/env bash
# logging stub: one argument per line, so an argument can be compared exactly
{ echo "== $(basename "$0")"; for a in "$@"; do printf '%s\n' "$a"; done; } >>"$STUB_LOG"
case "$(basename "$0"):$1" in
  kind:version) echo "kind v0.31.0 go1.25.5 darwin/arm64" ;;
  trivy:--version) echo "Version: 0.76.2" ;;
  docker:image) exit 1 ;;   # no such image
  kind:get) exit 0 ;;
  kubectl:config) exit 1 ;;
esac
exit 0
SH
  chmod +x "$fix/bin/$tool"
done
for tool in docker kind kubectl trivy go; do [ -x "$fix/bin/$tool" ] || { echo "ERROR: stub $tool was not created; refusing to run against real tools." >&2; exit 1; }; done
marker="$fix/MARKER"

# payloads: name<TAB>value (MARKER is replaced by the marker path)
payloads=(
  "single quote and comment|x'; touch MARKER; #"
  "double quote|x\" ; touch MARKER ; \""
  "make shell function|\$(shell touch MARKER)"
  "make shell function (braces)|\${shell touch MARKER}"
  "shell command substitution|\$(touch MARKER)"
  "backticks|\`touch MARKER\`"
  "semicolon|x; touch MARKER"
  "and-list|x && touch MARKER"
  "pipe|x | touch MARKER"
  "make dollar escape|x\$\$(touch MARKER)"
  "variable reference|\$HOME\${HOME}\$(HOME)"
  "space|a b"
  "leading dash|-x"
  "newline|x
touch MARKER"
)

run_make() { # run_make MODE TARGET VAR VALUE -> RC, OUT; MODE cmdline|env
  : >"$STUB_LOG"; rm -f "$marker"
  if [ "$1" = cmdline ]; then
    OUT="$(cd "$repo" && PATH="$fix/bin:$PATH" make "$2" "$3=$4" 2>&1)"; RC=$?
  else
    OUT="$(cd "$repo" && env "$3=$4" PATH="$fix/bin:$PATH" make "$2" 2>&1)"; RC=$?
  fi
}
export STUB_LOG="$fix/log/stub.log"

for entry in "${payloads[@]}"; do
  name="${entry%%|*}"; value="${entry#*|}"; value="${value//MARKER/$marker}"
  for mode in cmdline env; do
    # --- kind-up and kind-down: KIND_CLUSTER_NAME must be refused before any tool runs
    for target in kind-up kind-down; do
      run_make "$mode" "$target" KIND_CLUSTER_NAME "$value"
      if [ ! -e "$marker" ] && [ "$RC" -ne 0 ] && ! grep -q '^== kind$' "$STUB_LOG" && ! grep -q '^== kubectl$' "$STUB_LOG"; then ok; else bad "$target $mode KIND_CLUSTER_NAME [$name] rc=$RC marker=$([ -e "$marker" ] && echo CREATED || echo no) tools-called=$(grep -c '^== ' "$STUB_LOG"): $OUT"; fi
    done
    # --- KIND_VERSION is data compared as a string: a payload is a version mismatch, nothing executes
    run_make "$mode" kind-up KIND_VERSION "$value"
    if [ ! -e "$marker" ] && [ "$RC" -ne 0 ] && ! grep -q '^get$' "$STUB_LOG" && ! grep -q '^create$' "$STUB_LOG"; then ok; else bad "kind-up $mode KIND_VERSION [$name] rc=$RC marker=$([ -e "$marker" ] && echo CREATED || echo no): $OUT"; fi
    # --- mock-image: the value is ONE argument to docker build, exactly as given
    run_make "$mode" mock-image MOCK_IMAGE "$value"
    if [ ! -e "$marker" ] && python3 -I - "$STUB_LOG" "$value" <<'PY'
import sys
log = open(sys.argv[1]).read().split("\n")
val = sys.argv[2]
# the log has one argument per line, so a value with a newline spans lines; compare the joined text
text = "\n".join(log)
i = text.find("\n-t\n")
sys.exit(0 if i >= 0 and text[i + 4 : i + 4 + len(val)] == val and text[i + 4 + len(val) : i + 5 + len(val)] in ("\n", "") else 1)
PY
    then ok; else bad "mock-image $mode MOCK_IMAGE [$name] marker=$([ -e "$marker" ] && echo CREATED || echo no): the value was not passed as exactly one argument after -t: $(tr '\n' '|' <"$STUB_LOG")"; fi
    # --- mock-image-scan: bad image reference and bad scanner version refused before docker is asked anything
    run_make "$mode" mock-image-scan MOCK_IMAGE "$value"
    if [ ! -e "$marker" ] && [ "$RC" -ne 0 ] && ! grep -q '^save$' "$STUB_LOG" && ! grep -q '^inspect$' "$STUB_LOG" && printf '%s' "$OUT" | grep -q 'must be a plain reference'; then ok; else bad "mock-image-scan $mode MOCK_IMAGE [$name] rc=$RC marker=$([ -e "$marker" ] && echo CREATED || echo no): $OUT"; fi
    run_make "$mode" mock-image-scan IMAGE_SCANNER_VERSION "$value"
    if [ ! -e "$marker" ] && [ "$RC" -ne 0 ] && ! grep -q '^image$' "$STUB_LOG" && printf '%s' "$OUT" | grep -q 'must be digits and dots'; then ok; else bad "mock-image-scan $mode IMAGE_SCANNER_VERSION [$name] rc=$RC marker=$([ -e "$marker" ] && echo CREATED || echo no): $OUT"; fi
    # --- mock-run: MOCK_ARGS is word-split into flags, never parsed as shell
    run_make "$mode" mock-run MOCK_ARGS "$value"
    if [ ! -e "$marker" ]; then ok; else bad "mock-run $mode MOCK_ARGS [$name] executed a payload"; fi
  done
done

# mock-run still passes ordinary flags through as separate arguments
run_make cmdline mock-run MOCK_ARGS "--latency=5ms --seed=7"
if [ "$RC" -eq 0 ] && grep -qx -- '--latency=5ms' "$STUB_LOG" && grep -qx -- '--seed=7' "$STUB_LOG"; then ok; else bad "mock-run no longer passes ordinary flags: $(tr '\n' '|' <"$STUB_LOG")"; fi
# ordinary valid values still work end to end
run_make cmdline mock-image MOCK_IMAGE "example.invalid/team/mock:p1"
if [ "$RC" -eq 0 ] && grep -qx -- 'example.invalid/team/mock:p1' "$STUB_LOG"; then ok; else bad "a valid MOCK_IMAGE was not passed through: $OUT"; fi
run_make cmdline kind-up KIND_CLUSTER_NAME "agentplane-t"
if grep -q '^== kind$' "$STUB_LOG" && grep -qx 'version' "$STUB_LOG"; then ok; else bad "a valid KIND_CLUSTER_NAME did not reach the script: $OUT"; fi

if [ "$failed" -eq 0 ]; then echo "make-args self-test: all $checks checks passed"; else echo "make-args self-test: $failed of $checks FAILED"; exit 1; fi
