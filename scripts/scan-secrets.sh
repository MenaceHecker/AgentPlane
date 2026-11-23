#!/usr/bin/env bash
# Scan every tracked working-tree file with gitleaks, redacting findings.
# Reads each file from the working tree (so uncommitted edits are scanned),
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

fail=0
count=0
while IFS= read -r -d '' f; do
  [ -f "$f" ] || continue
  count=$((count + 1))
  if ! gitleaks stdin --redact --no-banner --log-level warn --exit-code 1 < "$f" >&2; then
    echo "SECRET SCAN FAILED: $f" >&2
    fail=1
  fi
done < <(git ls-files -z --cached --deduplicate)
echo "secrets: scanned $count tracked files, exit $fail"
exit "$fail"
