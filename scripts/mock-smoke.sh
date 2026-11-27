#!/usr/bin/env bash
# Smoke test for the deterministic mock model. Thin wrapper: all logic lives in the foreground
# Python runner scripts/mock_smoke.py, which owns every child it starts (ADR-007) and a private
# descriptor-owned workdir. The wrapper checks the tools, then REPLACES itself with the runner
# (exec) so there is no shell parent, no PID hand-off and no second process to signal.
# Arguments are passed through unchanged (the runner validates them). Needs go and python3 (3.8+).
set -euo pipefail

cd "$(dirname "$0")/.."

for tool in go python3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "ERROR: $tool not found." >&2; exit 1; }
done

exec python3 -I -B scripts/mock_smoke.py "$@"
