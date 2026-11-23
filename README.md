# AgentPlane

AgentPlane is a multi-tenant platform on Azure Kubernetes Service (AKS) where teams declare an AI agent in Git and the platform deploys and runs it with tenant isolation, token budgets, policy enforcement, audited gateway calls, canary delivery and SLO-driven rollback.

## Status

**Phase 0 (foundations), repository bootstrap only.** Nothing described above is implemented yet. This repository currently contains the agent workflow documents, the directory layout and local tooling scaffolding. No Go code, controllers, gateway, audit store or cloud infrastructure exists, and no performance, coverage or cost numbers have been measured.

Progress is tracked in [`docs/PROGRESS.md`](docs/PROGRESS.md). Requirements live in `AgentPlane_Requirements_and_Phased_Plan.docx` at the repo root (currently untracked, managed by the owner). Phases 0-3 run on a local kind cluster only; no cloud resources are used before Phase 4.

## Planned stack

Go (Kubebuilder/controller-runtime), Terraform (azurerm), Flux, Argo Rollouts, OPA Gatekeeper, Prometheus/Grafana/OpenTelemetry, PostgreSQL (audit store), kind (local), AKS (cloud). None of these are wired up yet.

## Prerequisites

Git, GNU Make, Docker and kind for the local cluster; plus the pinned quality tooling below.

| Tool | Pinned version | Install |
| --- | --- | --- |
| pre-commit | 4.7.0 | `pip install pre-commit==4.7.0` |
| gitleaks | 8.30.1 | `brew install gitleaks` or a release binary from github.com/gitleaks/gitleaks |
| golangci-lint | 2.15.0 | needed only once a Go module exists; github.com/golangci/golangci-lint |
| Go | declared by `go.mod` once it exists (P0-T3) | golang.org/dl |

The `Makefile` fails with an actionable message if gitleaks (or golangci-lint, when a Go module exists) is missing or not the pinned version. A missing tool is never treated as a pass.

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

## Local cluster commands

`make` (or `make help`) lists targets and creates nothing.

| Command | Effect |
| --- | --- |
| `make kind-up` | Creates a local kind cluster named `agentplane` using kind's default topology. Does nothing if it already exists. |
| `make kind-down` | Deletes only the named cluster, **destroying its workloads**. Does nothing if it does not exist. |

Override the name with `KIND_CLUSTER_NAME=<name>`. `kind create cluster` merges into your user kubeconfig (`~/.kube/config`) and switches the current context to `kind-<name>`; nothing is written into this repository. Check `kubectl config current-context` before running other kubectl commands. The labeled multi-node configuration arrives in P0-T5. Per the agent rules, the executor agent must have explicit owner authorization before running `make kind-down`.

## Quality checks

| Command | Effect |
| --- | --- |
| `make fmt-check` | Fails if Go files are not `gofmt`-formatted. Never rewrites files. |
| `make lint` | Runs `golangci-lint run ./...` at the pinned version. |
| `make test` | Runs `go test -race ./...`. |
| `make secrets` | Scans every tracked working-tree file with gitleaks, redacted output. |

Until a Go module exists (P0-T3) the three Go targets print `NOT APPLICABLE: Go module not introduced (P0-T3)` and succeed. Once `go.mod` exists they run for real and propagate failures. Go source without a root `go.mod` is an error. The checks live in `scripts/check-go.sh` and `scripts/scan-secrets.sh`.

Hooks: `pre-commit install --hook-type pre-commit --hook-type pre-push`, then `pre-commit run --all-files` to run everything on demand. Hooks call the same make targets (`make test` runs at pre-push). Validate the configuration with `pre-commit validate-config`.

## Contributing workflow

Work is split between a planner/reviewer agent and an executor agent, coordinated through files. See [`AGENTS.md`](AGENTS.md) and [`agents/README.md`](agents/README.md).

## License

Apache-2.0. See [`LICENSE`](LICENSE).
