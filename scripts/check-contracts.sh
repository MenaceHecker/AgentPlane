#!/usr/bin/env bash
# Validate the published Phase 0 contracts (docs/gateway and docs/audit).
#
# 1. Go tests in internal/contractcheck (test-only, no production code):
#    - the OpenAPI document loads and validates (kin-openapi), and every
#      gateway request, response, header and SSE stream fixture is validated
#      against the declared schemas: valid ones must pass, invalid ones must be
#      rejected for the stated reason;
#    - every audit record and checkpoint fixture is validated against its JSON
#      Schema (santhosh-tekuri/jsonschema), canonical bytes, hashes and the
#      Ed25519 signature are recomputed with different code than the generator,
#      and the verification rules of docs/audit/hash-chain.md are applied to
#      each case in docs/audit/examples/manifest.json.
# 2. The independent Python reference (standard library only) regenerates the
#    audit fixtures in memory and requires the committed files to match.
#
# Pinned dependencies: the Go modules in go.mod/go.sum (verified with
# `go mod verify`); Python needs only the standard library (3.8 or newer).
# This script performs no network access beyond what `go` needs for modules
# not yet in the module cache.
set -euo pipefail

cd "$(dirname "$0")/.."

command -v go >/dev/null 2>&1 || { echo "ERROR: go not found." >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "ERROR: python3 not found." >&2; exit 1; }
[ -f go.mod ] || { echo "ERROR: go.mod not found; contracts need the Go module." >&2; exit 1; }

echo "== 1/3 go mod verify"
go mod verify

echo "== 2/3 Go contract tests (OpenAPI, JSON Schema, vectors, verification rules)"
go test -count=1 ./internal/contractcheck/...

echo "== 3/3 independent Python reference: regenerate audit fixtures and compare"
python3 -I scripts/contracts/audit_reference.py --check

echo "contracts-check: OK"
