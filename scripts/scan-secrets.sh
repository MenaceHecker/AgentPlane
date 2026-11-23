#!/usr/bin/env bash
# Scan tracked files with gitleaks, redacting findings.
#
#   scan-secrets.sh                  scan every tracked working-tree file
#   scan-secrets.sh --go-sum-history scan every line ever added to the root go.sum
#
# Fails if any file has a finding, if no file was scanned, or if the scan itself
# errors (missing tool, wrong version, unreadable file list).
#
# go.sum handling. gitleaks' built-in configuration allowlists any path named
# go.sum for every rule (verified with gitleaks 8.30.1), so a path-aware scan
# (gitleaks dir, gitleaks git) never looks inside it. Scanning it through stdin
# avoids that allowlist, but then the generic-api-key rule flags public module
# checksum lines whose module name contains "api" (k8s.io/api, kin-openapi).
# So go.sum is scanned through stdin after removing ONLY lines with the exact
# structure of a go.sum checksum entry:
#     <module path> v<version>[/go.mod] h1:<43 base64 characters>=
# Every other line, and every other rule, still applies. The exception is
# limited to the root go.sum; a go.sum in a subdirectory is scanned in full.
# Residual risk: a secret that is itself shaped like a valid checksum line
# cannot be told apart from one by content.
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

# A structurally valid go.sum entry (extended regular expression).
GOSUM_ENTRY='^[A-Za-z0-9._~/-]+ v[0-9][A-Za-z0-9.+-]*(/go\.mod)? h1:[A-Za-z0-9+/]{43}=$'

# -v prints each finding (rule, entropy, line) with the secret redacted.
scan_stdin() { gitleaks stdin --redact -v --no-banner --log-level warn --exit-code 1 >&2; }

# Scan a go.sum through stdin. $1 = file, $2 = "filter" to drop valid entries.
scan_go_sum() {
  if [ "$2" = filter ]; then
    { grep -Ev "$GOSUM_ENTRY" "$1" || [ $? -eq 1 ]; } | scan_stdin
  else
    scan_stdin < "$1"
  fi
}

if [ "${1:-}" = "--go-sum-history" ]; then
  git rev-parse --git-dir >/dev/null
  # every line ever added to the root go.sum, without the diff markers
  added="$(git log --all --no-color -p --format= -- go.sum | { grep -E '^\+[^+]|^\+$' || [ $? -eq 1 ]; } | sed 's/^+//' | sort -u)"
  if [ -z "$added" ]; then
    echo "go.sum history: no go.sum lines in history; nothing to scan"
    exit 0
  fi
  n="$(printf '%s\n' "$added" | wc -l | tr -d ' ')"
  if printf '%s\n' "$added" | { grep -Ev "$GOSUM_ENTRY" || [ $? -eq 1 ]; } | scan_stdin; then
    echo "go.sum history: scanned $n distinct added lines, exit 0"
    exit 0
  fi
  echo "SECRET SCAN FAILED: go.sum history" >&2
  exit 1
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
    if [ "$(basename "$f")" = "go.sum" ]; then
      mode=full
      [ "$f" = "go.sum" ] && mode=filter
      if ! scan_go_sum "$f" "$mode"; then
        echo "SECRET SCAN FAILED: $f" >&2
        fail=1
      fi
    elif ! gitleaks dir --redact -v --no-banner --log-level warn --exit-code 1 "$f" >&2; then
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
