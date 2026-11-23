#!/usr/bin/env bash
# Regression tests for the secret scanner (scripts/scan-secrets.sh). Each case
# builds a throwaway git repository, copies the real scanner into it, and checks that the scan passes or fails as
# required. Secrets are generated at run time and are never printed.
#
# What this proves:
#   - go.sum is really scanned (gitleaks' built-in config allowlists the path
#     for every rule, so a path-aware scan alone would skip it entirely);
#   - legitimate go.sum checksum lines are ignored (the known false positives
#     of the generic-api-key rule on module names that contain "api");
#   - the exception is narrow in CONTENT: a generic key inside go.sum, on its
#     own line or appended to a checksum line, is still detected;
#   - the exception is narrow in PATH: the same checksum-shaped line in another
#     file or in a nested go.sum is still detected;
#   - other detector rules still fire inside go.sum;
#   - findings are redacted, and a missing scanner is an error, not a pass.
set -uo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/secrets-selftest.XXXXXX")"
fails=0

cleanup() {
  case "$work" in
    "${TMPDIR:-/tmp}"/secrets-selftest.*) rm -rf -- "$work" ;;
  esac
}
trap cleanup EXIT

command -v gitleaks >/dev/null 2>&1 || { echo "ERROR: gitleaks not found; the scanner self-test cannot run." >&2; exit 1; }

rand() { ( set +o pipefail; LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c "$1" ); }

# newrepo NAME -> creates $work/NAME with the real scanner and config
newrepo() {
  local d="$work/$1"
  mkdir -p "$d/scripts"
  cp "$repo/scripts/scan-secrets.sh" "$d/scripts/scan-secrets.sh"
  ( cd "$d" && git init -q && git config user.email t@example.invalid && git config user.name selftest )
  printf '%s' "$d"
}

# scan DIR -> runs the real scanner; output in $work/out, status in $rc
scan() {
  ( cd "$1" && git add -A >/dev/null 2>&1 && bash scripts/scan-secrets.sh ) >"$work/out" 2>&1
  rc=$?
}

ok()   { echo "PASS  $1"; }
bad()  { echo "FAIL  $1"; fails=$((fails + 1)); }

expect_pass() {   # name dir
  scan "$2"
  if [ "$rc" -eq 0 ]; then ok "$1"; else bad "$1 (expected exit 0, got $rc)"; sed 's/^/        /' "$work/out" | head -5; fi
}
expect_detect() { # name dir file-substring
  scan "$2"
  if [ "$rc" -ne 0 ] && grep -q "SECRET SCAN FAILED: .*$3" "$work/out"; then ok "$1"; else bad "$1 (expected a finding in $3, got exit $rc)"; sed 's/^/        /' "$work/out" | head -5; fi
}

# A structurally valid go.sum line: module path, version, optional /go.mod, h1:<43 base64>=
valid_line()    { printf 'github.com/example/apikey-client v1.2.3 h1:%s=\n' "$(rand 43)"; }
valid_modline() { printf 'github.com/example/apikey-client v1.2.3/go.mod h1:%s=\n' "$(rand 43)"; }

# --- legitimate checksums are ignored
d="$(newrepo real-go-sum)";      cp "$repo/go.sum" "$d/go.sum";                         expect_pass "the repository's real go.sum passes"                "$d"
d="$(newrepo synthetic-go-sum)"; { valid_line; valid_modline; } >"$d/go.sum";            expect_pass "synthetic checksum-shaped lines in go.sum pass"      "$d"

# --- a generic key inside go.sum is still detected
d="$(newrepo key-own-line)"; { valid_line; printf 'api_key = "%s"\n' "$(rand 40)"; } >"$d/go.sum"
expect_detect "a generic api_key on its own line in go.sum is detected" "$d" "go.sum"
secret="$(grep -o 'api_key = "[A-Za-z0-9]*"' "$d/go.sum" | grep -o '"[A-Za-z0-9]*"' | tr -d '"')"
if grep -q "Finding:" "$work/out" && ! grep -q "$secret" "$work/out"; then ok "findings are printed and the secret in go.sum is redacted"; else bad "finding output missing or the go.sum secret leaked"; fi

d="$(newrepo key-appended)"; printf 'github.com/example/apikey-client v1.2.3 h1:%s= api_key=%s\n' "$(rand 43)" "$(rand 40)" >"$d/go.sum"
expect_detect "a key appended to a checksum line in go.sum is detected" "$d" "go.sum"

d="$(newrepo key-in-module-field)"; printf 'api_key=%s v1.2.3 h1:%s=\n' "$(rand 40)" "$(rand 43)" >"$d/go.sum"
expect_detect "a key used as the module field in go.sum is detected" "$d" "go.sum"

# --- other rules still fire inside go.sum
d="$(newrepo other-rule-in-go-sum)"; { valid_line; printf 'token = "ghp_%s"\n' "$(rand 36)"; } >"$d/go.sum"
expect_detect "a GitHub-token-shaped value in go.sum is detected (other rules intact)" "$d" "go.sum"

# --- the exception is path-limited
d="$(newrepo key-other-file)"; printf 'api_key = "%s"\n' "$(rand 40)" >"$d/notes.txt"
expect_detect "a generic api_key in another file is detected" "$d" "notes.txt"
secret="$(grep -o '"[A-Za-z0-9]*"' "$d/notes.txt" | tr -d '"')"
if grep -q "Finding:" "$work/out" && ! grep -q "$secret" "$work/out"; then ok "findings are printed and the secret in an ordinary file is redacted"; else bad "finding output missing or the secret leaked (ordinary file)"; fi

d="$(newrepo checksum-line-other-file)"; grep 'k8s.io/api ' "$repo/go.sum" | head -1 >"$d/notes.txt"
expect_detect "a checksum-shaped line in another file is not excepted" "$d" "notes.txt"

d="$(newrepo nested-go-sum)"; mkdir -p "$d/sub"; grep 'k8s.io/api ' "$repo/go.sum" | head -1 >"$d/sub/go.sum"
expect_detect "a checksum-shaped line in a nested go.sum is not excepted (exact root path only)" "$d" "sub/go.sum"

# --- scanner errors are errors
d="$(newrepo no-scanner)"; valid_line >"$d/go.sum"
( cd "$d" && git add -A >/dev/null 2>&1 && PATH="/usr/bin:/bin" bash scripts/scan-secrets.sh ) >"$work/out" 2>&1; rc=$?
if [ "$rc" -ne 0 ] && grep -q "gitleaks not found" "$work/out"; then ok "a missing gitleaks fails the scan instead of passing"; else bad "missing gitleaks did not fail (exit $rc)"; fi

d="$(newrepo empty-repo)"
( cd "$d" && bash scripts/scan-secrets.sh ) >"$work/out" 2>&1; rc=$?
if [ "$rc" -eq 0 ]; then bad "a scan that read zero files passed"; elif grep -q "no tracked files were scanned" "$work/out"; then ok "a scan of zero tracked files fails instead of passing"; else bad "zero-file scan failed for the wrong reason (exit $rc)"; fi

d="$(newrepo not-a-repo)"; mkdir -p "$d/scripts"; cp "$repo/scripts/scan-secrets.sh" "$d/scripts/"; rm -rf "$d/.git"
( cd "$d" && bash scripts/scan-secrets.sh ) >"$work/out" 2>&1; rc=$?
if [ "$rc" -ne 0 ]; then ok "an unreadable file list fails the scan"; else bad "scan outside a git repository passed (exit $rc)"; fi

# --- go.sum history (gitleaks' default history scan skips go.sum entirely)
hist() { ( cd "$1" && bash scripts/scan-secrets.sh --go-sum-history ) >"$work/out" 2>&1; rc=$?; }

d="$(newrepo hist-clean)"; cp "$repo/go.sum" "$d/go.sum"; ( cd "$d" && git add -A && git commit -q -m legit )
hist "$d"; if [ "$rc" -eq 0 ]; then ok "go.sum history with only checksum lines passes"; else bad "clean go.sum history failed (exit $rc)"; sed 's/^/        /' "$work/out" | head -5; fi

d="$(newrepo hist-removed-key)"; { valid_line; printf 'api_key = "%s"\n' "$(rand 40)"; } >"$d/go.sum"
( cd "$d" && git add -A && git commit -q -m add-key && valid_line >go.sum && git add -A && git commit -q -m remove-key )
hist "$d"; if [ "$rc" -ne 0 ] && grep -q "SECRET SCAN FAILED: go.sum history" "$work/out"; then ok "a key added to go.sum and later removed is still found in history"; else bad "history scan missed a removed key (exit $rc)"; fi
( cd "$d" && bash scripts/scan-secrets.sh ) >"$work/out" 2>&1; rc=$?
if [ "$rc" -eq 0 ]; then ok "(control) the same repository's current tree is clean, so only the history scan sees the removed key"; else bad "current tree should be clean (exit $rc)"; fi

# A removed secret whose own text starts with "+" must still be found. In a
# unified diff an added line is "+" followed by the content, so content that
# starts with "+" appears as "++..." or "+++...", which a diff parser that
# skips header-looking lines would drop (review r2 finding 1).
for prefix in '+' '++' '+++'; do
  name="hist-plus-$(printf '%s' "$prefix" | wc -c | tr -d ' ')"
  d="$(newrepo "$name")"
  printf '%s github_token = "ghp_%s"\n' "$prefix" "$(rand 36)" >"$d/go.sum"
  ( cd "$d" && git add -A && git commit -q -m add-secret )
  expect_detect "(control) a line starting with '$prefix' holding a token is detected while it is in go.sum" "$d" "go.sum"
  ( cd "$d" && valid_line_content=$(printf 'github.com/example/apikey-client v1.2.3 h1:%s=\n' "$(rand 43)") && printf '%s' "$valid_line_content" >go.sum && git add -A && git commit -q -m remove-secret )
  scan "$d"; if [ "$rc" -eq 0 ]; then ok "(control) after removal the current tree is clean for prefix '$prefix'"; else bad "tree should be clean after removal (prefix '$prefix', exit $rc)"; fi
  hist "$d"
  if [ "$rc" -ne 0 ] && grep -q "SECRET SCAN FAILED: go.sum history" "$work/out"; then ok "a removed secret on a line starting with '$prefix' is found in go.sum history"; else bad "history scan missed a removed secret on a line starting with '$prefix' (exit $rc)"; sed 's/^/        /' "$work/out" | head -3; fi
done

d="$(newrepo hist-no-gosum)"; echo hi >"$d/a.txt"; ( cd "$d" && git add -A && git commit -q -m a )
hist "$d"; if [ "$rc" -eq 0 ]; then ok "a repository with no go.sum history passes the history scan"; else bad "no-go.sum history failed (exit $rc)"; fi

d="$(newrepo hist-not-a-repo)"; mkdir -p "$d/scripts"; cp "$repo/scripts/scan-secrets.sh" "$d/scripts/"; rm -rf "$d/.git"
hist "$d"; if [ "$rc" -ne 0 ]; then ok "the history scan outside a git repository fails"; else bad "history scan outside a repository passed"; fi

echo
if [ "$fails" -ne 0 ]; then echo "secrets self-test: $fails FAILED"; exit 1; fi
echo "secrets self-test: all passed"
