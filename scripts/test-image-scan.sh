#!/usr/bin/env bash
# Tests for the mock-image scan gate (make image-scan-test): the report
# evaluator (scripts/lib/evaluate_trivy.py) with fixture reports, and the scan
# script (scripts/scan-mock-image.sh) with stub docker and trivy. No Docker, no
# network, no database download. The rule under test: a scan that did not
# complete, or produced anything that is not valid scan evidence, can never pass.
set -uo pipefail
cd "$(dirname "$0")/.."
SRC="$PWD"

command -v python3 >/dev/null 2>&1 || { echo "ERROR: python3 not found; the image-scan tests cannot run." >&2; exit 1; }

fix="$(mktemp -d "${TMPDIR:-/tmp}/image-scan-test.XXXXXX")"
trap 'case "$fix" in "${TMPDIR:-/tmp}"/image-scan-test.*) rm -rf -- "$fix" ;; esac' EXIT
failed=0
ok()  { echo "PASS  $1"; }
bad() { echo "FAIL  $1"; failed=$((failed + 1)); }
PIN=0.76.2

# --- fixture reports
python3 -I - "$fix" "$PIN" <<'PY'
import copy, json, os, sys
d, pin = sys.argv[1], sys.argv[2]
os.makedirs(d + "/r", exist_ok=True)
def vuln(id, sev, fixed="", pkg="stdlib", ver="v1.27.3"):
    v = {"VulnerabilityID": id, "PkgName": pkg, "InstalledVersion": ver, "Severity": sev, "Status": "fixed" if fixed else "affected"}
    if fixed: v["FixedVersion"] = fixed
    return v
def report(vulns=(), results=None):
    return {"SchemaVersion": 2, "Trivy": {"Version": pin}, "ArtifactName": "image.tar", "ArtifactType": "container_image",
            "Metadata": {"ImageID": "sha256:" + "a" * 64, "OS": {"Family": "debian", "Name": "13.8"}},
            "Results": results if results is not None else [
                {"Target": "image.tar (debian 13.8)", "Class": "os-pkgs", "Type": "debian"},
                {"Target": "mockmodel", "Class": "lang-pkgs", "Type": "gobinary", **({"Vulnerabilities": list(vulns)} if vulns else {})}]}
def w(n, doc): json.dump(doc, open("%s/r/%s.json" % (d, n), "w"))
w("clean", report())
w("fixable-high", report([vuln("CVE-1", "HIGH", "1.2.3")]))
w("fixable-critical", report([vuln("CVE-2", "CRITICAL", "9.9")]))
w("unfixed-high", report([vuln("CVE-3", "HIGH")]))
w("unfixed-critical", report([vuln("CVE-4", "CRITICAL")]))
w("fixable-medium", report([vuln("CVE-5", "MEDIUM", "1.2.4")]))
w("mixed-passes", report([vuln("CVE-6", "HIGH"), vuln("CVE-7", "MEDIUM", "2.0"), vuln("CVE-8", "LOW", "3.0"), vuln("CVE-9", "UNKNOWN")]))
w("mixed-fails", report([vuln("CVE-6", "HIGH"), vuln("CVE-10", "CRITICAL", "4.0")]))
w("os-pkgs-only", report(results=[{"Target": "x (debian 13)", "Class": "os-pkgs", "Type": "debian"}]))
w("no-results", {k: v for k, v in report().items() if k != "Results"})
w("empty-results", report(results=[]))
w("secrets-only", report(results=[{"Target": "f", "Class": "secret"}]))
b = report(); b["SchemaVersion"] = 1; w("schema-1", b)
b = report(); del b["Trivy"]; w("no-trivy", b)
b = report(); b["Trivy"]["Version"] = "0.1.0"; w("wrong-version", b)
b = report(); b["ArtifactType"] = "filesystem"; w("not-image", b)
b = report(); del b["Metadata"]["ImageID"]; w("no-imageid", b)
b = report([vuln("CVE-1", "SEVERE", "1")]); w("bad-severity", b)
b = report([vuln("CVE-1", "HIGH", "1")]); del b["Results"][1]["Vulnerabilities"][0]["VulnerabilityID"]; w("no-id", b)
b = report([vuln("CVE-1", "HIGH", "1")]); b["Results"][1]["Vulnerabilities"][0]["FixedVersion"] = 5; w("fixed-not-string", b)
b = report(); b["Results"][1]["Vulnerabilities"] = "none"; w("vulns-not-list", b)
open(d + "/r/empty.json", "w").close()
open(d + "/r/garbage.json", "w").write("{not json")
open(d + "/r/array.json", "w").write("[]")
open(d + "/r/null.json", "w").write("null")
PY

eval_rc() { python3 -I scripts/lib/evaluate_trivy.py "$1" "${2-$PIN}" >"$fix/out" 2>"$fix/err"; echo $?; }
expect() { # expect NAME FILE WANT_RC [WANT_TEXT]
  local rc; rc="$(eval_rc "$fix/r/$2.json")"
  if [ "$rc" = "$3" ] && { [ -z "${4-}" ] || cat "$fix/out" "$fix/err" | grep -q -- "$4"; }; then ok "$1"; else bad "$1 (exit $rc, want $3; ${4-})"; cat "$fix/out" "$fix/err" | head -4 | sed 's/^/        /'; fi
}

# --- evaluator: valid evidence
expect "clean report passes"                                   clean 0 "GATE PASSED"
expect "a fixable HIGH finding fails the gate"                 fixable-high 1 "GATE FAILED: 1 fixable"
expect "a fixable CRITICAL finding fails the gate"             fixable-critical 1 "GATE FAILED"
expect "an unfixed HIGH finding passes but is listed as a limitation" unfixed-high 0 "LIMITATION: 1 unfixed HIGH/CRITICAL"
expect "an unfixed CRITICAL finding passes but is listed"      unfixed-critical 0 "finding CRITICAL CVE-4"
expect "a fixable MEDIUM finding passes and is listed"         fixable-medium 0 "finding MEDIUM   CVE-5"
expect "unfixed HIGH plus lower fixable findings pass, all listed" mixed-passes 0 "finding UNKNOWN  CVE-9"
expect "an unfixed HIGH does not hide a fixable CRITICAL"      mixed-fails 1 "GATE FAILED"
expect "a report with only an OS target and no findings passes" os-pkgs-only 0 "GATE PASSED"
# --- evaluator: anything that is not valid evidence is status 2, never a pass
expect "an empty file is invalid"                              empty 2 "empty"
expect "non-JSON is invalid"                                   garbage 2 "not valid JSON"
expect "a JSON array is invalid"                               array 2 "not a JSON object"
expect "JSON null is invalid"                                  null 2 "not a JSON object"
expect "a missing file is invalid"                             nosuchfile 2 "cannot read"
expect "a report without Results is invalid (nothing analyzed)" no-results 2 "no Results"
expect "a report with empty Results is invalid"                empty-results 2 "no Results"
expect "a report that analyzed no package target is invalid"   secrets-only 2 "no OS-package or language-package target"
expect "SchemaVersion 1 is invalid"                            schema-1 2 "SchemaVersion"
expect "a report without Trivy.Version is invalid"             no-trivy 2 "Trivy.Version"
expect "a report from another Trivy version is invalid"        wrong-version 2 "want $PIN"
expect "a non-image artifact is invalid"                       not-image 2 "ArtifactType"
expect "a report without an image ID is invalid"               no-imageid 2 "ImageID"
expect "an unknown severity is invalid"                        bad-severity 2 "severity"
expect "a vulnerability without an ID is invalid"              no-id 2 "VulnerabilityID"
expect "a non-string FixedVersion is invalid"                  fixed-not-string 2 "FixedVersion"
expect "a non-list Vulnerabilities is invalid"                 vulns-not-list 2 "not a list"

# --- the scan script, run from a copy of the layout with stub docker and trivy
mkdir -p "$fix/repo/scripts/lib" "$fix/bin"
cp scripts/scan-mock-image.sh "$fix/repo/scripts/"; cp scripts/lib/evaluate_trivy.py "$fix/repo/scripts/lib/"
cat >"$fix/bin/docker" <<'SH'
#!/usr/bin/env bash
echo "docker $*" >>"$STUB_LOG"
case "$1" in
  info) exit 0 ;;
  image) [ -n "${STUB_NO_IMAGE:-}" ] && exit 1; echo "sha256:0793ad23d1dc95569240a8dd4930f480abc969da5d6f8f5358ce8a8622b34e2d" ;;
  save) [ -n "${STUB_SAVE_FAIL:-}" ] && { echo "save failed" >&2; exit 1; }
        out=""; while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done
        if [ -n "${STUB_SAVE_EMPTY:-}" ]; then : >"$out"; else echo "tar-bytes" >"$out"; fi ;;
  *) exit 99 ;;
esac
SH
cat >"$fix/bin/trivy" <<'SH'
#!/usr/bin/env bash
echo "trivy $*" >>"$STUB_LOG"
case "$1" in
  --version) echo "Version: ${STUB_TRIVY_VERSION:-0.76.2}" ;;
  version) cat "$STUB_VERSION_JSON" ;;
  image)
    out=""; a=("$@"); i=0; while [ $i -lt ${#a[@]} ]; do [ "${a[$i]}" = --output ] && out="${a[$((i+1))]}"; i=$((i+1)); done
    [ -n "${STUB_TRIVY_RC:-}" ] && [ "$STUB_TRIVY_RC" != 0 ] && { echo "FATAL failed to download vulnerability DB" >&2; exit "$STUB_TRIVY_RC"; }
    [ -n "${STUB_NO_REPORT:-}" ] || cp "$STUB_REPORT" "$out"
    echo "INFO Detected OS family=debian version=13.8" ;;
  *) exit 99 ;;
esac
SH
chmod +x "$fix/bin/docker" "$fix/bin/trivy"
echo '{"Version":"0.76.2","VulnerabilityDB":{"Version":2,"UpdatedAt":"2026-11-25T13:07:24Z","DownloadedAt":"2026-11-25T15:40:44Z"}}' >"$fix/version-ok.json"
echo '{"Version":"0.76.2"}' >"$fix/version-nodb.json"

scan() { # scan REPORT [ENV=VAL...] -> RC, OUT, ARTIFACT dir
  local rep="$1"; shift
  export STUB_LOG="$fix/stub.log"; : >"$STUB_LOG"
  rm -rf "$fix/ev"
  OUT="$(env -u TRIVY_CACHE_DIR PATH="$fix/bin:$PATH" STUB_REPORT="$fix/r/$rep.json" STUB_VERSION_JSON="${STUB_VERSION_JSON:-$fix/version-ok.json}" \
        SCAN_TRIVY_VERSION="${PIN_OVERRIDE-$PIN}" SCAN_EVIDENCE_DIR="$fix/ev" "$@" "$fix/repo/scripts/scan-mock-image.sh" 2>&1)"; RC=$?
}

scan clean env
if [ $RC -eq 0 ] && printf '%s' "$OUT" | grep -q 'GATE PASSED' && [ -s "$fix/ev/trivy-report.json" ] && [ ! -e "$fix/ev/image.tar" ] && printf '%s' "$OUT" | grep -q 'vulnerability database v2 updated 2026-11-25T13:07:24Z'; then ok "script: a clean scan passes, keeps the JSON evidence, removes the image tar and records the database timestamp"; else bad "script clean (rc=$RC): $OUT"; fi
if grep -q '^trivy image --input ' "$STUB_LOG" && ! grep -Eq 'ignore-unfixed|skip-db-update|skip-version-check|ignorefile|--severity [A-Z,]*$' <(grep '^trivy image' "$STUB_LOG" | sed 's/ --severity [A-Z,]*//') && grep '^trivy image' "$STUB_LOG" | grep -q -- '--severity UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL'; then ok "script: Trivy runs on the saved tar with every severity and without --ignore-unfixed, --skip-db-update or an ignore file"; else bad "trivy flags: $(grep '^trivy image' "$STUB_LOG")"; fi
scan fixable-high env
if [ $RC -eq 1 ] && printf '%s' "$OUT" | grep -q 'GATE FAILED'; then ok "script: a fixable HIGH finding exits 1"; else bad "script fixable high (rc=$RC): $OUT"; fi
scan unfixed-high env
if [ $RC -eq 0 ] && printf '%s' "$OUT" | grep -q 'LIMITATION'; then ok "script: an unfixed HIGH finding exits 0 and prints the limitation"; else bad "script unfixed high (rc=$RC): $OUT"; fi

scan clean env STUB_TRIVY_RC=1
if [ $RC -eq 2 ] && printf '%s' "$OUT" | grep -q 'NOT a pass' && ! printf '%s' "$OUT" | grep -q 'GATE PASSED'; then ok "script: a database or scanner error (Trivy exit 1) is a failed scan, never a pass"; else bad "trivy error (rc=$RC): $OUT"; fi
scan garbage env
if [ $RC -eq 2 ] && ! printf '%s' "$OUT" | grep -q 'GATE PASSED' && printf '%s' "$OUT" | grep -q 'INVALID REPORT'; then ok "script: Trivy exiting 0 with invalid JSON is a failed scan"; else bad "invalid json (rc=$RC): $OUT"; fi
scan empty env
if [ $RC -eq 2 ] && ! printf '%s' "$OUT" | grep -q 'GATE PASSED'; then ok "script: Trivy exiting 0 with an empty report is a failed scan"; else bad "empty report (rc=$RC): $OUT"; fi
scan clean env STUB_NO_REPORT=1
if [ $RC -eq 2 ] && ! printf '%s' "$OUT" | grep -q 'GATE PASSED'; then ok "script: Trivy exiting 0 without writing a report is a failed scan"; else bad "no report (rc=$RC): $OUT"; fi
STUB_VERSION_JSON="$fix/version-nodb.json" scan clean env
if [ $RC -eq 2 ] && printf '%s' "$OUT" | grep -q 'no vulnerability database is loaded'; then ok "script: a scan with no vulnerability database recorded is a failed scan"; else bad "no db (rc=$RC): $OUT"; fi
scan no-results env
if [ $RC -eq 2 ]; then ok "script: a report that analyzed nothing is a failed scan"; else bad "no results (rc=$RC): $OUT"; fi
scan clean env STUB_TRIVY_VERSION=0.75.0
if [ $RC -eq 2 ] && printf '%s' "$OUT" | grep -q 'Trivy 0.76.2 is required'; then ok "script: any Trivy version other than the pin is refused"; else bad "wrong trivy (rc=$RC): $OUT"; fi
PIN_OVERRIDE="" scan clean env
if [ $RC -eq 2 ] && printf '%s' "$OUT" | grep -q 'SCAN_TRIVY_VERSION is not set'; then ok "script: an unpinned scan is refused"; else bad "unpinned (rc=$RC): $OUT"; fi
scan clean env STUB_NO_IMAGE=1
if [ $RC -eq 2 ] && printf '%s' "$OUT" | grep -q 'make mock-image'; then ok "script: a missing image is a failed scan with the build hint"; else bad "no image (rc=$RC): $OUT"; fi
scan clean env STUB_SAVE_FAIL=1
if [ $RC -eq 2 ] && printf '%s' "$OUT" | grep -q 'docker save failed'; then ok "script: a failing docker save is a failed scan"; else bad "save fail (rc=$RC): $OUT"; fi
scan clean env STUB_SAVE_EMPTY=1
if [ $RC -eq 2 ] && printf '%s' "$OUT" | grep -q 'empty archive'; then ok "script: an empty image archive is a failed scan"; else bad "empty tar (rc=$RC): $OUT"; fi

: >"$fix/repo/.trivyignore"; scan fixable-high env
if [ $RC -eq 2 ] && printf '%s' "$OUT" | grep -q '.trivyignore exists'; then ok "script: an ignore file in the repository is refused (no silent suppression)"; else bad "ignore file (rc=$RC): $OUT"; fi
rm -f "$fix/repo/.trivyignore"
for v in TRIVY_IGNOREFILE=/dev/null TRIVY_SEVERITY=LOW TRIVY_IGNORE_UNFIXED=true TRIVY_SKIP_DB_UPDATE=true; do
  scan fixable-high env "$v"
  if [ $RC -eq 2 ] && printf '%s' "$OUT" | grep -q "${v%%=*} is set"; then :; else bad "script accepted ${v%%=*} (rc=$RC): $OUT"; fi
done
ok "script: TRIVY_IGNOREFILE, TRIVY_SEVERITY, TRIVY_IGNORE_UNFIXED and TRIVY_SKIP_DB_UPDATE in the environment are refused"
scan clean env TRIVY_CACHE_DIR="$fix/cache"
if [ $RC -eq 0 ]; then ok "script: TRIVY_CACHE_DIR (which cannot change findings) is allowed"; else bad "cache dir (rc=$RC): $OUT"; fi

if [ "$failed" -eq 0 ]; then echo "image-scan self-test: all passed"; else echo "image-scan self-test: $failed FAILED"; exit 1; fi
