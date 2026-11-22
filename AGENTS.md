# AgentPlane: Shared Agent Guide

Read this file fully at the start of every session, whichever agent you are. It is the source of truth for how work is done in this repo. Requirements live in `docs/AgentPlane_Requirements_and_Phased_Plan.docx`. If this file and the requirements doc disagree, stop and ask the owner.

Codex reads this file as `AGENTS.md`. Claude Code reads `CLAUDE.md`, which imports this file and adds executor instructions.

## 1. Project in one paragraph

AgentPlane is a multi-tenant platform on Azure Kubernetes Service where teams declare an AI agent in Git and the platform deploys and runs it with tenant isolation, token budgets, policy enforcement, audited gateway calls, canary delivery, and SLO-driven rollback. Stack: Go (Kubebuilder/controller-runtime), Terraform (azurerm), Flux, Argo Rollouts, OPA Gatekeeper, Prometheus/Grafana/OTel, PostgreSQL (audit), kind (local), AKS (cloud).

Owner: Tushar (GitHub: MenaceHecker). Goal: Platform/Infra resume, LinkedIn, interviews. Every claim in the README and on the resume must be backed by a measured result.

## 2. The agent team

The project is built by two AI agents with separate roles, coordinated through files in the repo. The agents do not talk to each other directly. The repo is the message bus and the owner triggers each hand-off.

| Role | Agent | Does | Does not |
| --- | --- | --- | --- |
| Planner | Codex | Turns a phase task into a plan: scope, commit list, acceptance checks, security flags | Write product code |
| Executor | Claude | Implements the plan in small commits | Change scope or the plan without asking |
| Test runner | Claude | Runs lint, unit, integration, and acceptance checks; records raw results | Summarize away failures or edit tests to make them pass |
| Reviewer | Codex | Reviews the PR diff and the run report against the plan and requirements | Edit product code (it writes review files only) |
| Owner | Tushar | Triggers each agent, runs exit-gate checks, merges PRs, approves anything touching Azure | |

Rules that follow from the split:
- Claude does not self-plan. If there is no approved plan file for a task, Claude asks the owner to get one from Codex.
- Codex writes only under `agents/plans/` and `agents/reviews/` (plus ADRs if the plan calls for them). Findings that need code changes go to Claude as review items.
- The test runner role is independent of what Claude wrote: tests must exercise requirements (FR/NFR IDs), not just the implementation. Codex checks this during review.
- Neither agent declares a phase done. Only the owner does, after running the exit gate.

## 3. Hard rules

1. **No Azure until Phase 4.** Phases 0-3 run on kind only. Never run `az`, `terraform apply`, or anything that creates cloud resources without explicit owner approval in the current session.
2. **No secrets in the repo.** No keys, tokens, kubeconfigs, `.tfstate`, or `.env` files. Gitleaks must pass.
3. **No fabricated numbers.** Never write a benchmark, latency, coverage, or cost figure into any doc unless it came from a recorded run committed under `experiments/results/` or `agents/runs/`. Use `TBD (not yet measured)` otherwise.
4. **Mock model only for tests.** Never call real LLM provider APIs from tests or CI.
5. **Never cut:** tenant isolation, token budgets, audit integrity. If behind schedule, cut in this order: canary analysis polish, Kata, extra experiments.
6. **Security-sensitive code gets full review.** Controllers, gateway auth (TokenReview), budget accounting, audit chain, policies, Terraform IAM. The plan must flag these and the review must cover them explicitly.
7. **Ask instead of guessing** when a requirement is ambiguous or a design decision is not covered by an ADR. Record the decision as an ADR once made.
8. **Test integrity.** Never delete, skip, or weaken a test to get a green run. If a test is wrong, say so in the run report and let Codex review the change.

## 4. Hand-off protocol

### 4.1 Files
```
agents/
  README.md
  templates/        plan.md, run.md, review.md
  plans/            P<phase>-T<n>.md          (Codex, planner)
  runs/             P<phase>-T<n>.md          (Claude, executor + test runner)
  reviews/          P<phase>-T<n>-r<k>.md     (Codex, reviewer)
```
`docs/PROGRESS.md` tracks status of each task ID.

### 4.2 Task lifecycle
```
PLANNED -> IN_PROGRESS -> READY_FOR_REVIEW -> CHANGES_REQUESTED -> READY_FOR_REVIEW -> APPROVED -> MERGED
```
1. **Plan (Codex).** Owner asks Codex to plan task `P0-T1`. Codex creates the branch `phase-0/<slug>` and commits `agents/plans/P0-T1.md` as `docs(agents): plan P0-T1`. Owner reviews the plan and sets status PLANNED.
2. **Execute (Claude).** Owner starts Claude on that branch. Claude implements the plan commit by commit.
3. **Test (Claude).** After the last commit, Claude runs every check in the plan and commits `agents/runs/P0-T1.md` with raw command output (trimmed only for length, never edited).
4. **Review (Codex).** Owner starts Codex on the same branch. Codex reads the plan, the run report, and the diff, then commits `agents/reviews/P0-T1-r1.md` with a verdict.
5. **Fix loop.** On CHANGES_REQUESTED, Claude fixes each finding as a new commit, reruns the checks, and appends to the run report. Codex writes `-r2`, `-r3`, and so on until APPROVE.
6. **Merge.** Owner opens or finalizes the PR, confirms CI is green, and merges.

If the plan turns out to be wrong or incomplete during execution, Claude stops, writes the problem in the run report under "Blocked", and the owner takes it back to Codex for a revised plan. Claude does not improvise scope.

### 4.3 Plan template (`agents/templates/plan.md`)
```markdown
# Plan P<phase>-T<n>: <title>
Status: PLANNED
Branch: phase-<n>/<slug>
Requirements: <FR-/NFR- IDs>

## Goal
<2-4 sentences>

## Out of scope
<explicit list>

## Commit list
1. `<type>(<scope>): <summary>` - files: <paths>
2. ...

## Acceptance checks (the test runner executes these)
- [ ] <exact command> -> expected result
- [ ] ...

## Security-sensitive
<yes/no; which files; what the reviewer must look at>

## Risks and open questions
<list>
```

### 4.4 Run report template (`agents/templates/run.md`)
```markdown
# Run report P<phase>-T<n>
Branch: <name>   Final commit: <sha>

## Commits made
<sha - message>, in order. Note any deviation from the plan and why.

## Acceptance checks
For each plan check: command, exit code, relevant raw output, PASS/FAIL.

## Other checks
Lint, unit tests, coverage (raw numbers), race detector, gitleaks.

## Failures and fixes
<what failed, what was changed, which commit>

## Blocked
<anything requiring a plan change or an owner decision; empty if none>
```

### 4.5 Review template (`agents/templates/review.md`)
```markdown
# Review P<phase>-T<n>-r<k>
Verdict: APPROVE | CHANGES_REQUESTED

## Requirements check
<each FR/NFR in the plan: covered / partially / missing, with evidence>

## Findings
| # | Severity (blocker/major/minor/nit) | File:line | Issue | Suggested fix |

## Test review
<do the tests exercise the requirements, or only the implementation? gaps?>

## Security review
<required if the plan flagged security-sensitive files>

## Run report verification
<do the claimed results match the diff and the plan? anything not reproducible?>
```
Blockers and majors must be fixed before APPROVE. Minors and nits are fixed or explicitly deferred with a reason.

## 5. Git workflow

### 5.1 Before starting work (any agent)
```bash
git checkout <task-branch> && git pull --ff-only
git status            # must be clean
git config user.email # must match a verified email on the owner's GitHub account
```

### 5.2 Branching
- One branch per task: `phase-<N>/<short-slug>`. Created by Codex when it commits the plan.
- Never commit directly to `main`.
- Only one agent works on a branch at a time. The owner hands off explicitly.

### 5.3 Commit rules
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
Scopes: `api`, `tenant-ctrl`, `agent-ctrl`, `gateway`, `mockmodel`, `audit`, `policy`, `deploy`, `infra`, `experiments`, `docs`, `ci`, `agents`.

- Stage explicitly: `git add <specific files>`. Never `git add -A` or `git add .` without reviewing `git status` and `git diff --cached` first.
- Never use `--no-verify`, `--amend` on pushed commits, or `git push --force`.
- Review-fix commits are new commits (`fix(...)` / `refactor(...)`), never amends.

### 5.4 Pull request
Opened after the first APPROVE (or earlier as a draft). Template:
```
## What
<one paragraph>

## Task / plan / review
P<phase>-T<n>; links to agents/plans, agents/runs, agents/reviews

## Requirements covered
<FR-/NFR- IDs>

## How to verify
<exact commands>

## Security-sensitive?
<yes/no, and which files>

## Not done / follow-ups
<honest list>
```
Default merge is a normal merge so commit history is preserved. Squash only if the owner asks.

## 6. Definition of done for any task
- Builds, lints (`golangci-lint`, `gofmt`), and passes unit tests.
- Every acceptance check in the plan has a recorded result in the run report.
- New behavior has tests. Controllers and gateway target at least 70 percent coverage (measured, not assumed).
- Codex verdict is APPROVE with no open blockers or majors.
- Docs updated if behavior or commands changed.
- `docs/PROGRESS.md` updated.

## 7. Phase playbook

Work one phase at a time. Do not start a phase until the owner confirms the previous exit gate passed. Each task below is a branch with a task ID (`P<phase>-T<n>`). The bullets are the **seed commit list**: Codex expands each task into a plan file (section 4) and may adjust the split, but commits stay small.

### Phase 0: Foundations and design (local, 2-3 days)
Covers: NFR-8, NFR-9 (CI scaffolding).

**Task 0.0 `phase-0/agent-protocol` (ID P0-T0)**
- `docs: add AGENTS.md shared agent guide`
- `docs: add CLAUDE.md executor instructions`
- `docs(agents): add agents/README and handoff conventions`
- `docs(agents): add plan, run report, and review templates`

**Task 0.1 `phase-0/repo-bootstrap` (ID P0-T1)**
- `chore: add README stub, LICENSE (Apache-2.0), .gitignore`
- `chore: scaffold directory layout with .gitkeep placeholders`
- `chore: add Makefile with help, kind-up, kind-down targets`
- `chore: add pre-commit config (gofmt, golangci-lint, gitleaks)`
- `ci: add GitHub Actions skeleton (lint, unit test, gitleaks)`

**Task 0.2 `phase-0/adrs` (ID P0-T2)**
- `docs(adr): add ADR template`
- `docs(adr): ADR-001 Flux vs ArgoCD`
- `docs(adr): ADR-002 Postgres vs Cosmos for audit store`
- `docs(adr): ADR-003 hardened pods vs Kata sandboxing`
- `docs(adr): ADR-004 token budgeting with reserve-then-reconcile`

**Task 0.3 `phase-0/api-contracts` (ID P0-T3)**
- `feat(api): init Go module and Tenant CRD types`
- `feat(api): add Agent CRD types and status conditions`
- `docs(gateway): add OpenAPI spec for /v1/chat/completions and /v1/tools/{name}`
- `docs(audit): add audit record schema and hash-chain definition`

**Task 0.4 `phase-0/mockmodel` (ID P0-T4)**
- `feat(mockmodel): deterministic chat completion handler`
- `feat(mockmodel): configurable latency, token counts, error rate`
- `feat(mockmodel): streaming responses`
- `test(mockmodel): unit tests for determinism and failure modes`

**Task 0.5 `phase-0/kind` (ID P0-T5)**
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

## 8. Progress tracking
Maintain `docs/PROGRESS.md` with: current phase, a table of task IDs and their lifecycle status, PR links, open questions, and exit-gate checkboxes. Whoever finishes a hand-off updates the status in its own `docs: update progress` commit.

## 9. Session protocols

### Codex (planner and reviewer)
- **Start:** read this file and `docs/PROGRESS.md`; confirm which task ID and mode (plan or review).
- **Plan mode:** read the relevant phase and requirements, write the plan from the template, flag security-sensitive files, define exact acceptance commands. Do not write product code.
- **Review mode:** read the plan, run report, and full diff. Verify claims against the code. Write the review from the template. Be specific (file and line) and honest; approve only when blockers and majors are resolved.
- **End:** commit the plan or review file, update `docs/PROGRESS.md`.

### Claude (executor and test runner)
- **Start:** read this file and `docs/PROGRESS.md`; read the plan for the task; confirm the branch and clean tree; restate the commit list.
- **During:** one commit at a time; run relevant checks before each commit; stage explicit files.
- **Test run:** execute every acceptance check; paste raw output into the run report; never hide a failure.
- **End:** push the branch, commit the run report, update `docs/PROGRESS.md`, list what is left. If on Azure, confirm `make down` was run.

## 10. Style
- Go: standard layout, `gofmt`, `golangci-lint`, errors wrapped with context, table-driven tests, no global state in controllers.
- Docs: concise, honest about limitations. State what was measured and what was not.
- Terraform: one module per concern, pinned provider versions, variables documented, no hardcoded IDs.

## 11. Optional automation (after Phase 0)
Once the manual loop works, add `scripts/agent-loop.sh` that runs plan, execute, test, and review as non-interactive CLI calls to each agent, stopping for owner approval at the plan and at the merge. Treat it as its own task with its own plan. Do not build it before the manual protocol has been used on at least two tasks.
