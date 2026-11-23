# Architecture decision records

ADRs record decisions and the specification details the contracts depend on. Use [`template.md`](template.md) for new ones.

## Status meanings
- **Accepted**: the owner made this decision explicitly. Each ADR's "Owner acceptance" section points at the evidence. Accepted does not mean implemented.
- **Proposed**: specification detail written by the executor, not yet reviewed or accepted by the owner. Must not be presented as owner-accepted.
- **Open question**: listed inside an ADR where no decision exists yet.

Phase 0 only records decisions. Nothing in these ADRs is implemented, enforced or measured yet.

## Index
| ADR | Title | Status | Owner-accepted content | Still Proposed or open |
| --- | --- | --- | --- | --- |
| [ADR-001](ADR-001-gitops.md) | GitOps: Flux over ArgoCD | Accepted | Flux over ArgoCD | Rationale text is not owner-attested |
| [ADR-002](ADR-002-audit-store.md) | Audit store: PostgreSQL | Accepted (choice); details Proposed | PostgreSQL, in-cluster/local, restricted writer role | Three-role split, trigger, credential handling |
| [ADR-003](ADR-003-sandbox.md) | Isolation baseline: hardened pods; Kata timeboxed | Accepted (baseline, timebox); details Proposed | Hardened pods baseline; Kata 2-day Phase 3 experiment | Exact control list |
| [ADR-004](ADR-004-budget-accounting.md) | Budget accounting: reserve then reconcile | Proposed | None directly (relies on the accepted UTC window in ADR-005) | Whole mechanism; open question on the counter store |
| [ADR-005](ADR-005-contract-semantics.md) | Gateway and budget contract semantics | Accepted (section 1: UTC window); rest Proposed | Fixed daily UTC window, reset-boundary edge case, cross-midnight accounting; one logical audit trail per request (admission, completion, correction events) with its streaming failure boundary | Burst, zero values, identity mapping, errors, usage finalization; open questions on unattributable requests and tenant RPM |
| [ADR-006](ADR-006-audit-chain.md) | Audit hash chain and signed checkpoints | Accepted (chain per tenant; checkpoint outline); details Proposed | One chain per tenant; signed checkpoint with head hash, count, timestamp, tenant-bound, separate store; format-only Phase 0; uncheckpointed-tail limitation; one logical audit trail per request as multiple chain entries (see ADR-005 6.4) | JCS encoding, SHA-256, Ed25519, genesis, locking, verification procedure, `_platform` chain |

Related guide decisions in `AGENTS.md` (hard rules, cut order) are not re-decided here.

## Go module and toolchain choice (not yet made)
No Go module exists. P0-T3 creates it and records the choice; this section states only what is known.

- **Not decided:** the Go language version, the module path, and the Kubernetes/controller-runtime dependency versions. They are specification work for P0-T3 and are not owner-accepted by anything in P0-T2. This ADR set invents no version numbers.
- **Proposed module path:** the GitHub repository path, `github.com/MenaceHecker/AgentPlane`, so imports match where the code lives. To be confirmed in P0-T3.
- **Already recorded facts (not choices for the module):**
  - CI and local checks pin tool versions in `Makefile`, `.pre-commit-config.yaml` and `.github/workflows/ci.yml` (pre-commit, gitleaks, golangci-lint, actionlint, action commit SHAs); those are the source of truth for tool pins.
  - The Go version that happened to be installed on the executor's machine when P0-T1 ran is recorded in `agents/runs/P0-T1.md`. It is an observation about one machine, not a pin or a project decision.
- **Provenance rules for P0-T3 (Proposed):** take Go and dependency versions from their official release channels, pin them in `go.mod` (with the `go` directive and a `toolchain` line if used), commit `go.sum`, run `go mod verify`, install CI toolchains from the declared version (`go-version-file: go.mod`), and record in the run report where each version was obtained. Compatibility with a future Kubebuilder scaffold (Phase 1) must be preserved without pulling that scaffold forward.
- Once the module exists, the `NOT APPLICABLE` Go-check exemption in `scripts/check-go.sh` stops applying and the checks must really run (P0-T3 acceptance).
