# AgentPlane

AgentPlane is a multi-tenant platform on Azure Kubernetes Service (AKS) where teams declare an AI agent in Git and the platform deploys and runs it with tenant isolation, token budgets, policy enforcement, audited gateway calls, canary delivery and SLO-driven rollback.

## Status

**Phase 0 (foundations), repository bootstrap only.** Nothing described above is implemented yet. This repository currently contains the agent workflow documents, the directory layout and local tooling scaffolding. No Go code, controllers, gateway, audit store or cloud infrastructure exists, and no performance, coverage or cost numbers have been measured.

Progress is tracked in [`docs/PROGRESS.md`](docs/PROGRESS.md). Requirements live in `AgentPlane_Requirements_and_Phased_Plan.docx` at the repo root (currently untracked, managed by the owner). Phases 0-3 run on a local kind cluster only; no cloud resources are used before Phase 4.

## Planned stack

Go (Kubebuilder/controller-runtime), Terraform (azurerm), Flux, Argo Rollouts, OPA Gatekeeper, Prometheus/Grafana/OpenTelemetry, PostgreSQL (audit store), kind (local), AKS (cloud). None of these are wired up yet.

## Prerequisites

Planned local tooling: Git, GNU Make, Docker, kind. Quality and secret-scanning tooling is documented as it is added.

## Directory map

| Path | Purpose |
| --- | --- |
| `agents/` | Agent handoff protocol: `plans/`, `runs/`, `reviews/`, `templates/` |
| `api/` | CRD types and API contracts (later tasks) |
| `controllers/` | Tenant and Agent controllers (Phase 1) |
| `gateway/` | LLM and tool gateway (Phase 2) |
| `mockmodel/` | Deterministic mock model for tests (P0-T4) |
| `audit/` | Audit hash-chain library and CLI (Phase 2) |
| `policy/` | Gatekeeper policies (Phase 3) |
| `infra/terraform/` | Azure infrastructure (Phase 4) |
| `deploy/` | `base/`, `platform/`, `tenants/` manifests |
| `docs/` | Progress tracker and `adr/` architecture decision records |
| `experiments/` | Experiment scripts; raw results go in `experiments/results/` |
| `scripts/` | Helper scripts |

Directories other than `agents/` and `docs/` are placeholders (`.gitkeep`) at this stage.

## Contributing workflow

Work is split between a planner/reviewer agent and an executor agent, coordinated through files. See [`AGENTS.md`](AGENTS.md) and [`agents/README.md`](agents/README.md).

## License

Apache-2.0. See [`LICENSE`](LICENSE).
