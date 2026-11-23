#!/usr/bin/env bash
# Shared Go quality checks used by the Makefile, pre-commit and CI.
# Usage: scripts/check-go.sh fmt-check|lint|test
#
# Before a Go module exists the checks report NOT APPLICABLE and succeed.
# Once go.mod exists the checks run for real and every failure, including a
# missing tool, is propagated. Go source without a root go.mod is an error.
set -euo pipefail

mode="${1:-}"
case "$mode" in
  fmt-check|lint|test) ;;
  *) echo "usage: $0 fmt-check|lint|test" >&2; exit 2 ;;
esac

# Optional version pin for golangci-lint (e.g. 2.15.0), set by the Makefile.
want_lint="${GOLANGCI_LINT_VERSION:-}"

prune=( -path ./.git -o -path ./vendor -o -path ./node_modules -o -path '*/.terraform' )

go_files="$(find . \( "${prune[@]}" \) -prune -o -type f -name '*.go' -print)"
nested_mods="$(find . \( "${prune[@]}" \) -prune -o -type f -name go.mod -print | grep -vx './go.mod' || true)"

if [ ! -f go.mod ]; then
  if [ -n "$go_files" ] || [ -n "$nested_mods" ]; then
    echo "ERROR: Go source or a nested go.mod exists but there is no root go.mod:" >&2
    printf '%s\n' "$go_files" $nested_mods >&2
    exit 1
  fi
  echo "NOT APPLICABLE: Go module not introduced (P0-T3)"
  exit 0
fi

command -v go >/dev/null 2>&1 || { echo "ERROR: go not found but go.mod exists." >&2; exit 1; }

case "$mode" in
  fmt-check)
    if [ -z "$go_files" ]; then
      echo "fmt-check: no Go files."
      exit 0
    fi
    # shellcheck disable=SC2086
    unformatted="$(echo "$go_files" | xargs gofmt -l)"
    if [ -n "$unformatted" ]; then
      echo "ERROR: gofmt reports unformatted files (run 'gofmt -w' on them):" >&2
      echo "$unformatted" >&2
      exit 1
    fi
    echo "fmt-check: OK"
    ;;
  lint)
    command -v golangci-lint >/dev/null 2>&1 || { echo "ERROR: golangci-lint not found. Install v${want_lint:-<pinned>}." >&2; exit 1; }
    if [ -n "$want_lint" ]; then
      have="$(golangci-lint version 2>&1 | head -1)"
      case "$have" in
        *"version $want_lint "*|*"version v$want_lint "*) ;;
        *) echo "ERROR: golangci-lint $want_lint required, found: $have" >&2; exit 1 ;;
      esac
    fi
    golangci-lint run ./...
    ;;
  test)
    go test -race ./...
    ;;
esac
