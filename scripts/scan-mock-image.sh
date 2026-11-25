#!/usr/bin/env bash
# Scan the local mock model image with the pinned Trivy and apply the gate.
#
#   scripts/scan-mock-image.sh [IMAGE]       default agentplane/mockmodel:p0
#
# Environment: SCAN_TRIVY_VERSION (required pin; the Makefile passes it),
# SCAN_EVIDENCE_DIR (default $TMPDIR/agentplane-p0-t5-evidence).
#
# The image is exported with `docker save` and scanned from the tar, so Trivy
# never needs the Docker socket or any registry credential. The vulnerability
# database is downloaded (network is required); if it cannot be downloaded or
# loaded the scan fails and nothing is reported as scanned. Findings are never
# suppressed: no ignore files, no --ignore-unfixed, and no TRIVY_* environment
# that could change the result is accepted. Gate (scripts/lib/evaluate_trivy.py):
# fail on FIXABLE HIGH/CRITICAL findings; unfixed ones and all lower severities
# are printed. The JSON report is kept in the evidence directory.
#
# Exit status: 0 gate passed, 1 gate failed (fixable HIGH/CRITICAL), 2 the scan
# could not be completed or the report is not valid evidence (never a pass).
set -euo pipefail

cd "$(dirname "$0")/.."

image="${1:-agentplane/mockmodel:p0}"
pin="${SCAN_TRIVY_VERSION:-}"
evidence="${SCAN_EVIDENCE_DIR:-${TMPDIR:-/tmp}/agentplane-p0-t5-evidence}"
evidence="${evidence%/}"

err() { echo "image-scan: ERROR: $*" >&2; exit 2; }

[ -n "$pin" ] || err "SCAN_TRIVY_VERSION is not set; refusing to scan with an unpinned scanner."
for tool in docker trivy python3; do
  command -v "$tool" >/dev/null 2>&1 || err "$tool not found. See docs/local-development.md for the pinned install."
done
have="$(trivy --version 2>/dev/null | sed -n 's/^Version: *//p' | head -1)"
[ "$have" = "$pin" ] || err "Trivy $pin is required (found '${have:-unknown}')."

# No silent suppressions: refuse anything that could hide or filter findings.
for f in .trivyignore .trivyignore.yaml .trivyignore.yml trivy.yaml trivy.yml; do
  [ ! -e "$f" ] || err "$f exists in the repository root; findings must not be suppressed by configuration."
done
for v in $(env | sed -n 's/^\(TRIVY_[A-Z0-9_]*\)=.*/\1/p'); do
  case "$v" in TRIVY_CACHE_DIR|TRIVY_NO_PROGRESS) ;; *) err "$v is set; it could change what Trivy reports. Unset it." ;; esac
done

docker info >/dev/null 2>&1 || err "the Docker daemon is not reachable."
image_id="$(docker image inspect --format '{{.Id}}' "$image" 2>/dev/null)" || err "image '$image' does not exist locally. Run: make mock-image"

mkdir -p "$evidence"
tar_file="$evidence/image.tar"
report="$evidence/trivy-report.json"
rm -f "$tar_file" "$report" "$evidence/trivy-version.json" "$evidence/scan.log"
trap 'rm -f "$tar_file"' EXIT

docker save "$image" -o "$tar_file" || err "docker save failed for '$image'."
[ -s "$tar_file" ] || err "docker save produced an empty archive."

echo "image-scan: scanning $image (docker image ID $image_id) with Trivy $have"
status=0
trivy image --input "$tar_file" --scanners vuln \
  --severity UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL --format json --output "$report" \
  --exit-code 0 --no-progress >"$evidence/scan.log" 2>&1 || status=$?
if [ "$status" -ne 0 ]; then
  tail -n 15 "$evidence/scan.log" >&2
  err "Trivy exited with status $status (vulnerability database could not be updated or loaded, or the scan failed); this is NOT a pass."
fi
grep -E 'vulndb|Detected OS|Number of language' "$evidence/scan.log" | sed 's/^/image-scan: trivy: /' || true
trivy version --format json >"$evidence/trivy-version.json" 2>/dev/null || err "could not record the scanner and database versions."
python3 -I - "$evidence/trivy-version.json" <<'PY' || err "the scanner version record is unreadable."
import json, sys
v = json.load(open(sys.argv[1]))
db = v.get("VulnerabilityDB")
if not isinstance(db, dict) or not db.get("UpdatedAt"):
    sys.exit("no vulnerability database is loaded")
print("image-scan: Trivy %s, vulnerability database v%s updated %s, downloaded %s" % (v.get("Version"), db.get("Version"), db.get("UpdatedAt"), db.get("DownloadedAt")))
PY
echo "image-scan: evidence directory: $evidence (trivy-report.json, trivy-version.json, scan.log)"

rc=0
python3 -I scripts/lib/evaluate_trivy.py "$report" "$pin" || rc=$?
exit "$rc"
