# AgentPlane

AgentPlane is a multi-tenant platform on Azure Kubernetes Service (AKS) where teams declare an AI agent in Git and the platform deploys and runs it with tenant isolation, token budgets, policy enforcement, audited gateway calls, canary delivery and SLO-driven rollback.

## Status

**Phase 0 (foundations).** Nothing described above is running yet. This repository contains the agent workflow documents, ADRs, local tooling, the Go module with the Tenant and Agent API types (`api/v1alpha1`, types only: no CRD manifests, controllers or admission validation), the gateway and audit contracts with fixtures and checks (`docs/gateway`, `docs/audit`), a deterministic mock chat model with a local image, and a labeled local kind topology. No controller, gateway, audit store, signer or cloud infrastructure exists, and no performance or cost numbers have been measured. The local workflow is in [`docs/local-development.md`](docs/local-development.md).

Progress is tracked in [`docs/PROGRESS.md`](docs/PROGRESS.md). Requirements live in `AgentPlane_Requirements_and_Phased_Plan.docx` at the repo root (currently untracked, managed by the owner). Phases 0-3 run on a local kind cluster only; no cloud resources are used before Phase 4.

## Planned stack

Go (Kubebuilder/controller-runtime), Terraform (azurerm), Flux, Argo Rollouts, OPA Gatekeeper, Prometheus/Grafana/OpenTelemetry, PostgreSQL (audit store), kind (local), AKS (cloud). None of these are wired up yet.

## Prerequisites

Git, GNU Make, Docker, kind v0.31.0 and kubectl for the local cluster; plus the pinned quality tooling below. Trivy 0.76.2 is needed only for the image scan. Full prerequisites, the tested host and troubleshooting: [`docs/local-development.md`](docs/local-development.md).

| Tool | Pinned version | Install |
| --- | --- | --- |
| pre-commit | 4.7.0 | `pip install pre-commit==4.7.0` |
| gitleaks | 8.30.1 | `brew install gitleaks` or a release binary from github.com/gitleaks/gitleaks |
| golangci-lint | 2.15.0 | github.com/golangci/golangci-lint |
| Go | declared by `go.mod` (the `go` directive) | golang.org/dl |
| Python 3 | 3.8 or newer, standard library only | needed by `make contracts-check` |

The `Makefile` fails with an actionable message if gitleaks or golangci-lint is missing or not the pinned version. A missing tool is never treated as a pass.

## Directory map

| Path | Purpose |
| --- | --- |
| `agents/` | Agent handoff protocol: `plans/`, `runs/`, `reviews/`, `templates/` |
| `api/` | Go types for the `agentplane.io/v1alpha1` Tenant and Agent resources (types only) |
| `internal/contractcheck/` | Test-only validation of the gateway and audit contracts |
| `controllers/` | Tenant and Agent controllers (Phase 1) |
| `gateway/` | LLM and tool gateway (Phase 2) |
| `mockmodel/`, `cmd/mockmodel/` | Deterministic mock chat model, its command and its `Dockerfile` (see `docs/mockmodel.md`) |
| `internal/kindshape/`, `cmd/kindshape/` | Derives the expected kind topology from `deploy/kind/config.yaml` and checks a cluster's nodes against it |
| `audit/` | Audit hash-chain library and CLI (Phase 2) |
| `policy/` | Gatekeeper policies (Phase 3) |
| `infra/terraform/` | Azure infrastructure (Phase 4) |
| `deploy/` | `kind/` (local topology config and pins), plus `base/`, `platform/`, `tenants/` manifests |
| `docs/` | Progress tracker, `adr/` decision records, `gateway/` OpenAPI contract, `audit/` record and checkpoint contracts |
| `experiments/` | Experiment scripts; raw results go in `experiments/results/` |
| `scripts/` | Check, cluster, image-scan and smoke scripts (`scripts/lib/` holds the owned-process helper and the scan evaluator) and the audit fixture reference calculation (`scripts/contracts/`) |

Directories other than `agents/`, `api/`, `docs/`, `internal/` and `scripts/` are placeholders (`.gitkeep`) at this stage.

## Local cluster commands

`make` (or `make help`) lists targets and creates nothing.

| Command | Effect |
| --- | --- |
| `make kind-up` | Creates the named local kind cluster (default `agentplane`): one control plane, one `system` and one `tenant` worker (tenant tainted `NoSchedule`), from `deploy/kind/config.yaml`. An existing cluster is accepted only if its nodes match; it is **never recreated or deleted**. Every operation uses an explicit name and `--context kind-<name>`; your current kubectl context is restored after kind switches it. |
| `make kind-down` | Deletes exactly the named cluster, **destroying its workloads**. Needs the owner's explicit authorization for that name when an agent runs it. |

Override the name with `KIND_CLUSTER_NAME=<name>`. kind merges the new context into your user kubeconfig (`~/.kube/config`); nothing is written into this repository. Labels and the taint are placement simulation, **not tenant isolation**. Details, pins and troubleshooting: [`docs/local-development.md`](docs/local-development.md).

## Quality checks

| Command | Effect |
| --- | --- |
| `make fmt-check` | Fails if Go files are not `gofmt`-formatted. Never rewrites files. |
| `make lint` | Runs `golangci-lint run ./...` at the pinned version. |
| `make test` | Runs `go test -race ./...`. |
| `make secrets` | Scans every tracked working-tree file with gitleaks, redacted output. `go.sum` is scanned through stdin with only exact module/version/checksum lines removed (see below). |
| `make secrets-history` | Scans every line ever added to `go.sum`. |
| `make secrets-selftest` | Regression tests for the scanner itself: checksum lines pass, keys in `go.sum` and other files are detected, redaction holds, and scanner errors fail. |
| `make mock-run` | Runs the deterministic mock chat model on loopback (see [`docs/mockmodel.md`](docs/mockmodel.md)). |
| `make mock-smoke` | Starts a disposable mock server, checks determinism, streaming, validation and failures, and stops it. Needs go, curl and python3. |
| `make smoke-cleanup-test` | Tests the owned-process helper the smoke script uses: bounded termination, a TERM-ignoring child, partial-start and signal cleanup, unrelated processes surviving. Needs bash and python3. |
| `make kind-config-test` | Tests the kind topology, node-shape checks and cluster targeting with stub tools (no Docker, no cluster). Needs go and python3. |
| `make mock-image` | Builds the nonroot local image `agentplane/mockmodel:p0` (needs Docker; pushes nothing). |
| `make mock-image-scan` | Scans it with the pinned Trivy; fails on fixable HIGH/CRITICAL findings and on any scan failure, prints everything else. Needs network for the database. |
| `make image-scan-test` | Tests the scan gate with fixture reports and stub tools (no Docker, no network). |
| `make contracts-check` | Validates the gateway and audit contract fixtures: OpenAPI and JSON Schema validation of positive and negative fixtures, independent recomputation of hashes and signatures, and the audit verification rules. Needs go and python3. |

The Go module exists (since P0-T3), so the Go targets run for real and propagate failures. Go source without a root `go.mod` is an error, and a repository without Go source and without `go.mod` would report `NOT APPLICABLE` and succeed. The checks live in `scripts/check-go.sh` and `scripts/scan-secrets.sh`. **`go.sum` and gitleaks:** gitleaks' built-in configuration allowlists any path named `go.sum` for every rule, so path-aware scans (`gitleaks dir`, `gitleaks git`) never look inside it. The scripts therefore scan the root `go.sum` through stdin and drop only lines with the exact structure `module v<version>[/go.mod] h1:<43 base64 characters>=` (public checksums that trip the generic-api-key rule on module names containing "api"). Any other line in `go.sum`, a `go.sum` in a subdirectory, and every other rule are scanned in full. A secret that is itself shaped like a checksum line cannot be told apart by content; that residual risk is accepted.

Hooks: `pre-commit install --hook-type pre-commit --hook-type pre-push`, then `pre-commit run --all-files` to run everything on demand. Hooks call the same make targets (`make test` runs at pre-push). Validate the configuration with `pre-commit validate-config`.

## CI

`.github/workflows/ci.yml` runs on pull requests and pushes to `main` with read-only permissions, job timeouts, commit-pinned actions and pinned tool versions (pre-commit 4.7.0, gitleaks 8.30.1, golangci-lint 2.15.0; Go module dependencies pinned in `go.mod` and `go.sum`). Five jobs:

- `lint`: `pre-commit validate-config`, then `pre-commit run --all-files` (YAML and private-key checks, `actionlint` workflow syntax validation at v1.7.12, `make fmt-check`, `make lint`).
- `unit-test`: `make test` (`go test -race ./...`, including the contract, mock model and kind-topology tests), `make smoke-cleanup-test`, `make kind-config-test` and `make mock-smoke`.
- `contracts`: `make contracts-check`.
- `image`: `make image-scan-test`, `make mock-image`, container start and hardened-run checks, a checksum-verified install of Trivy 0.76.2, and `make mock-image-scan`. Nothing is pushed.
- `gitleaks`: `gitleaks git --redact --no-banner --log-opts="--all" .` over full history, `make secrets`, `make secrets-history` (go.sum history) and `make secrets-selftest`.

The workflow installs the Go toolchain declared in `go.mod` and golangci-lint and runs the real checks. CI cannot show controller or gateway correctness yet (none exists), and it creates no kind cluster. Only the mock image is scanned; later images need their own scans.

## Contributing workflow

Work is split between a planner/reviewer agent and an executor agent, coordinated through files. See [`AGENTS.md`](AGENTS.md) and [`agents/README.md`](agents/README.md).

## License

Apache-2.0. See [`LICENSE`](LICENSE).
