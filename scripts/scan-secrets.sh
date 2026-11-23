#!/usr/bin/env bash
# Scan every tracked working-tree file with gitleaks, redacting findings.
# Reads each file from the working tree (so uncommitted edits are scanned; the
# file path is known to gitleaks, so findings name the file and line and the
# narrow path allowlist in .gitleaks.toml applies),
# and fails if any file has a finding or if the scan itself errors.
set -euo pipefail

want="${GITLEAKS_VERSION:-}"

command -v gitleaks >/dev/null 2>&1 || { echo "ERROR: gitleaks not found. Install v${want:-<pinned>}, e.g. 'brew install gitleaks'." >&2; exit 1; }
if [ -n "$want" ]; then
  have="$(gitleaks version 2>&1 | head -1)"
  case "$have" in
    "$want"|"v$want"|*" $want") ;;
    *) echo "ERROR: gitleaks $want required, found: $have" >&2; exit 1 ;;
  esac
fi

# pipefail (set above) makes a failed `git ls-files` fail the whole pipeline,
# and set -e then aborts. An empty file list also fails: a scan of nothing
# must never be reported as a pass.
git ls-files -z --cached --deduplicate | {
  fail=0
  count=0
  while IFS= read -r -d '' f; do
    [ -f "$f" ] || continue
    count=$((count + 1))
    if ! gitleaks dir --redact --no-banner --log-level warn --exit-code 1 "$f" >&2; then
      echo "SECRET SCAN FAILED: $f" >&2
      fail=1
    fi
  done
  if [ "$count" -eq 0 ]; then
    echo "ERROR: no tracked files were scanned; refusing to report success." >&2
    exit 1
  fi
  echo "secrets: scanned $count tracked files, exit $fail"
  exit "$fail"
}
