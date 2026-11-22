# AgentPlane: Agent Development Guide

Read this file fully at the start of every session. It is the source of truth for how work is done in this repo. Requirements live in `docs/AgentPlane_Requirements_and_Phased_Plan.docx` (copy it into `docs/` if missing). If this file and the requirements doc disagree, stop and ask.

## 1. Project in one paragraph

AgentPlane is a multi-tenant platform on Azure Kubernetes Service where teams declare an AI agent in Git and the platform deploys and runs it with tenant isolation, token budgets, policy enforcement, audited gateway calls, canary delivery, and SLO-driven rollback. Stack: Go (Kubebuilder/controller-runtime), Terraform (azurerm), Flux, Argo Rollouts, OPA Gatekeeper, Prometheus/Grafana/OTel, PostgreSQL (audit), kind (local), AKS (cloud).

Owner: Tushar (GitHub: MenaceHecker). Goal: Platform/Infra resume, LinkedIn, interviews. Every claim in the README and on the resume must be backed by a measured result.

## 2. Roles

| Role | Who | Responsibility |
| --- | --- | --- |
| Planner / Executor | Claude (this agent) | Plan each task, write code, tests, docs, and commits |
| Reviewer | Codex | Reviews each PR diff, especially security-sensitive code |
| Owner | Tushar | Runs acceptance tests at each exit gate, merges PRs, approves anything touching Azure |

Claude never declares a phase done. Only the owner does, after running the exit-gate checks.

## 3. Hard rules

1. **No Azure until Phase 4.** Phases 0-3 run on kind only. Never run `az`, `terraform apply`, or anything that creates cloud resources without explicit owner approval in the current session.
2. **No secrets in the repo.** No keys, tokens, kubeconfigs, `.tfstate`, or `.env` files. Gitleaks must pass.
3. **No fabricated numbers.** Never write a benchmark, latency, coverage, or cost figure into any doc unless it came from a recorded run committed under `experiments/results/`. Use `TBD (not yet measured)` otherwise.
4. **Mock model only for tests.** Never call real LLM provider APIs from tests or CI.
5. **Never cut:** tenant isolation, token budgets, audit integrity. If behind schedule, cut in this order: canary analysis polish, Kata, extra experiments.
6. **Security-sensitive code gets a second look.** Controllers, gateway auth (TokenReview), budget accounting, audit chain, policies, Terraform IAM. Flag these in the PR description for Codex review.
7. **Ask instead of guessing** when a requirement is ambiguous or a design decision is not covered by an ADR. Write the decision as an ADR once made.

## 4. Git workflow (every session)

### 4.1 Before starting work
```bash
git checkout main && git pull --ff-only
git status            # must be clean
git config user.email # must match a verified email on the owner's GitHub account
```

### 4.2 Branching
- One branch per task: `phase-<N>/<short-slug>` (example: `phase-0/mockmodel-streaming`).
- Never commit directly to `main`.
- Keep branches short-lived (a task is roughly 1-6 commits). Open a PR into `main` when the task's tests pass.

### 4.3 Commit rules
- **One logical change per commit.** A new file, a function plus its test, a config tweak, a doc section: each is its own commit.
- Do not batch unrelated files. Do not split one logical change into meaningless fragments (no one-line padding commits, no empty commits, no whitespace-only commits).
- Tests for a change go in the same commit as the change, or in the immediately following `test:` commit.
- Every commit must leave the repo building and the existing tests passing.
- Format: Conventional Commits.

```
<type>(<scope>): <imperative summary, under 72 chars>

<optional body: why, not what>
```
Types: `feat`, `fix`, `docs`, `test`, `ci`, `chore`, `refactor`, `perf`, `build`.
Scopes: `api`, `tenant-ctrl`, `agent-ctrl`, `gateway`, `mockmodel`, `audit`, `policy`, `deploy`, `infra`, `experiments`, `docs`, `ci`.

Examples:
```
feat(mockmodel): add configurable latency flag
test(gateway): cover over-budget 429 path
docs(adr): record Flux over ArgoCD decision
```

- Stage explicitly: `git add <specific files>`. Never `git add -A` or `git add .` without reviewing `git status` and `git diff --cached` first.
- Never use `--no-verify`, `--amend` on pushed commits, or `git push --force`.

### 4.4 Per-task loop
1. State the task and the exact commit list you plan to make (short).
2. Make a commit, run the relevant checks, repeat.
3. `git push -u origin <branch>` after the last commit.
4. Open the PR (`gh pr create`) with the template in 4.5.
5. Wait for CI and for Codex review. Address review comments as new commits (`fix(...)` or `refactor(...)`), not amends.
6. Owner merges (squash-merge only if the owner asks; the default is a normal merge so commit history is preserved).

### 4.5 PR description template
```
## What
<one paragraph>

## Requirements covered
<FR-/NFR- IDs>

## How to verify
<exact commands>

## Security-sensitive?
<yes/no, and which files>

## Not done / follow-ups
<honest list>
```

## 5. Definition of done for any task
- Builds, lints (`golangci-lint`, `gofmt`), and passes unit tests.
- New behavior has tests. Controllers and gateway target at least 70 percent coverage.
- `make ci` (or the equivalent) passes locally.
- Docs updated if behavior or commands changed.
- `docs/PROGRESS.md` updated (see section 8).

## 6. Phase playbook

Work one phase at a time. Do not start a phase until the owner confirms the previous exit gate passed. Each task below is a branch; the bullets are the suggested commit sequence. Adjust if the code naturally splits differently, but keep commits small.

### Phase 0: Foundations and design (local, 2-3 days)
Covers: NFR-8, NFR-9 (CI scaffolding).

**Task 0.1 `phase-0/repo-bootstrap`**
- `chore: add README stub, LICENSE (Apache-2.0), .gitignore`
- `chore: scaffold directory layout with .gitkeep placeholders`
- `chore: add Makefile with help, kind-up, kind-down targets`
- `chore: add pre-commit config (gofmt, golangci-lint, gitleaks)`
- `ci: add GitHub Actions skeleton (lint, unit test, gitleaks)`

**Task 0.2 `phase-0/adrs`**
- `docs(adr): add ADR template`
- `docs(adr): ADR-001 Flux vs ArgoCD`
- `docs(adr): ADR-002 Postgres vs Cosmos for audit store`
- `docs(adr): ADR-003 hardened pods vs Kata sandboxing`
- `docs(adr): ADR-004 token budgeting with reserve-then-reconcile`

**Task 0.3 `phase-0/api-contracts`**
- `feat(api): init Go module and Tenant CRD types`
- `feat(api): add Agent CRD types and status conditions`
- `docs(gateway): add OpenAPI spec for /v1/chat/completions and /v1/tools/{name}`
- `docs(audit): add audit record schema and hash-chain definition`

**Task 0.4 `phase-0/mockmodel`**
- `feat(mockmodel): deterministic chat completion handler`
- `feat(mockmodel): configurable latency, token counts, error rate`
- `feat(mockmodel): streaming responses`
- `test(mockmodel): unit tests for determinism and failure modes`

**Task 0.5 `phase-0/kind`**
- `feat(deploy): kind cluster config with system and tenant node labels`
- `docs: local development quickstart`

**Exit gate (owner runs):**
- `make kind-up` creates the cluster; mock model returns identical output for identical input.
- CI is green on `main`.
- ADRs and OpenAPI are committed.

### Phase 1: CRDs and controllers (kind, 5-6 days)
Covers: FR-CTL-1 to FR-CTL-7, NFR-4, NFR-7.

Tasks (one branch each, small commits within):
1. Kubebuilder scaffold; CRD validation markers and printer columns.
2. Tenant controller: namespace, then ResourceQuota, LimitRange, default-deny NetworkPolicy, RBAC, finalizer cleanup (one commit per owned object type, each with a test).
3. Agent controller: ServiceAccount, Service, Deployment, owner references, status conditions and events.
4. Validating webhook: agent budget must not exceed tenant budget; clear error messages.
5. envtest integration tests; table-driven unit tests.
6. Drift tests: delete or edit owned objects and verify correction under 30 s.
7. Leader election and restart-safety test.

Rules: reconcile must be idempotent and level-triggered (no writes when nothing changed). Status must reflect real state.

**Exit gate (owner runs):** apply a Tenant and an Agent on kind, get a running pod in an isolated namespace; drift corrected under 30 s; deleting the Tenant removes everything; coverage at least 70 percent.

### Phase 2: Gateway, budgets, audit (kind, 6-7 days)
Covers: FR-GW-1 to FR-GW-10, FR-AUD-1 to FR-AUD-5, FR-OBS-1 (gateway), NFR-1 to NFR-3.

Tasks:
1. Gateway skeleton with both endpoints and health checks.
2. TokenReview authentication; identity-to-(tenant, agent) mapping. Identity comes from the token, never from the request body.
3. Budget accounting: reserve-then-reconcile, daily window, 429 with budget-exhausted code.
4. Per-agent token-bucket rate limiting (document the per-replica approximation).
5. Tool allowlist enforcement from Agent and Tenant specs.
6. Streaming with mid-stream budget enforcement.
7. Audit library: hash chain, Postgres schema, INSERT/SELECT-only role, fail-closed behavior.
8. `agentplane audit verify` CLI that reports the first broken index.
9. Prometheus metrics and OTel spans (W3C traceparent).
10. Controller change: inject gateway URL and projected SA token; NetworkPolicy allowing egress only to gateway and DNS.
11. k6 load baseline against the mock model; commit raw results.

**Exit gate (owner runs):** a test pod cannot reach anything except the gateway; a looping agent is cut off at its cap within NFR-3; tampering with a row makes `audit verify` fail and name the first broken record; NFR-1 and NFR-2 measured and recorded.

### Phase 3: Policy and isolation hardening (kind, 3-4 days)
Covers: FR-POL-1 to FR-POL-5, isolation model.

Tasks:
1. Gatekeeper ConstraintTemplates and Constraints, one policy per commit (privileged, root, no limits, latest tag, hostPath, seccomp, foreign registry, over-budget agent).
2. `gator test` suites and a corpus of at least 15 bad manifests plus good manifests (commit the corpus in small groups).
3. Pod Security Standards (restricted) on tenant namespaces.
4. Noisy-neighbor harness: runaway tenant plus steady tenant; capture the victim baseline p95.
5. Kata/pod sandboxing feasibility: timeboxed to 2 days; record the result either way in an ADR.
6. STRIDE threat model mapping mitigations to controls.

**Exit gate (owner runs):** 15 of 15 bad manifests rejected, good manifests admitted; noisy-neighbor baseline captured; threat model committed.

### Phase 4: Azure infrastructure (credits start, 3-4 days)
Covers: FR-INF-1 to FR-INF-6, NFR-10.

**Stop and get explicit owner approval before any cloud command.** The owner must first confirm: Azure account activated today, credit expiry date noted, budget alerts created (FR-INF-5), VM quota checked.

Tasks: Terraform modules one per commit (resource group, VNet, AKS with OIDC and workload identity, node pools, ACR, Key Vault, identities and federated credentials); Key Vault CSI test secret; image push and kubelet pull test; `make up` / `make down`; two full up/down cycles; cost-per-hour documentation.

Always run `terraform plan` and show the output to the owner before `apply`. Teardown (`make down`) at the end of every working session unless the owner says otherwise.

**Exit gate (owner runs):** clean `make up`; `make down` leaves no billable resources (verified in portal); a pod reads a Key Vault secret with no static credentials; cost projection is under the cap.

### Phase 5: GitOps, canary, observability on AKS (4-5 days)
Covers: FR-DEL-1 to FR-DEL-4, FR-OBS-1 to FR-OBS-4, NFR-5, NFR-6.

Tasks: Flux bootstrap and repo structure; deploy platform components via Git; switch Agent controller to emit Argo Rollouts; AnalysisTemplates (error rate, p95, budget burn); Grafana dashboards and multi-window burn-rate alerts; sample agents across two tenants; git-push-to-serving timing.

**Exit gate (owner runs):** pushing an Agent manifest yields a serving agent with no manual kubectl; a deliberately bad release auto-rolls back and time-to-rollback is recorded; dashboards show per-tenant usage and rollout health.

### Phase 6: Experiments, demo, docs, teardown (4-5 days)
Experiments E1-E7 (noisy neighbor, budget enforcement, policy rejection, canary rollback, audit tamper, deploy latency, gateway load). Scripts live in `experiments/`; each run at least 3 times; raw results and graphs committed under `experiments/results/`; report median and range.

Then: README (problem, architecture diagram, quickstart, results table, limitations), runbook, ADR index, Grafana exports, demo video, final teardown, spend report.

**Exit gate (owner runs):** README numbers match raw data; demo recorded; repo public with CI green; Azure resources destroyed.

## 7. Phase 0 starting point (next action)
Begin with Task 0.1. Before writing files, print the commit list for the task, then proceed one commit at a time, running `git status` and `git diff --cached` before each commit.

## 8. Progress tracking
Maintain `docs/PROGRESS.md` with: current phase, current task, tasks done (with PR links), open questions, and the status of each exit-gate checkbox. Update it in its own `docs: update progress` commit at the end of each task.

## 9. Session protocol
- **Start:** read this file and `docs/PROGRESS.md`, run the 4.1 checks, state the current task.
- **During:** small commits, run checks before each, flag security-sensitive changes.
- **End:** push the branch, open or update the PR, update `docs/PROGRESS.md`, list what is left. If on Azure, confirm `make down` was run.

## 10. Style
- Go: standard layout, `gofmt`, `golangci-lint`, errors wrapped with context, table-driven tests, no global state in controllers.
- Docs: concise, honest about limitations. State what was measured and what was not.
- Terraform: one module per concern, pinned provider versions, variables documented, no hardcoded IDs.
