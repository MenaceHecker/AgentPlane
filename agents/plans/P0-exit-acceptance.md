# Plan: Phase 0 exit acceptance (P0-T5 follow-up)
Status: DRAFT — owner approval pending; sending the runner prompt approves the non-live scope only
Branch: phase-0/exit-acceptance
Requirements: NFR-8, NFR-9 (Phase 0 foundations); requirements document Phase 0 exit gate

## Goal
Collect acceptance evidence for the merged Phase 0 implementation from a clean checkout of main. Close documentation drift and expose the remaining owner decisions without another speculative implementation round. This is a P0-T5 acceptance follow-up, not a new implementation task or authorization to begin Phase 1. Only the owner declares the phase passed.

Source: root requirements DOCX Phase 0 exit gate and NFR-8/9, AGENTS.md, P0.md, P0-T5 plan and review r6. No conflict found in the exit-gate scope. P0-T5 merged via PR #8; tracker merge recorded via PR #9. Neither merge proves live acceptance.

## Out of scope
Product/test/CI changes, new tools or checker implementation, Go upgrades, changing scan gates, deleting the deferred Bash helper, mutation execution, Azure/cloud/provider commands, Phase 1, PR merge, and automatic scratch deletion. Failures become evidence and a specific planner handoff; do not fix product code or weaken assertions.

## Commit list
1. `docs: align local quickstart with owned Python supervision` — docs/local-development.md only. Replace obsolete ps/Bash process-identity prerequisites, troubleshooting and limitations with current Python ownership/ADR-007 behavior. Preserve direct-child/supervisor-death/uncollectible-child and filesystem limits; preserve scan and kind warnings. This corrects visibly stale documentation, not behavior.
2. `docs(agents): record Phase 0 exit acceptance evidence` — agents/runs/P0-exit-acceptance.md and optional redacted raw logs under agents/runs/P0-exit-acceptance/. Record the exact main SHA, clean clone, prerequisites, individual commands/statuses, current CI, failures, live authorization if any, and owner decisions still missing. Report evidence gathered before documentation edits against its actual SHA. Append later authorized live results in a new commit, never overwrite failure history.
3. `docs: update Phase 0 acceptance progress` — docs/PROGRESS.md only. Link evidence; keep P0-T5 MERGED, distinguish pending/failed/passed checks. Do not check the owner exit gate or start Phase 1 without the owner's explicit confirmation.

## Execution and acceptance checks

### 1. Clean clone and bounded local checks (non-live scope)
- Start on the handoff branch, pull --ff-only, inspect status. The owner-managed untracked requirements DOCX is known; do not add, remove or modify it. Stop for unexpected tracked changes. Record Git email; do not claim verified GitHub email without evidence.
- `git fetch origin main`; `git rev-parse origin/main` — pin the full SHA as SOURCE_SHA in the report. Create a fresh owned evidence root with `mktemp -d "${TMPDIR:-/tmp}/agentplane-p0-exit.XXXXXX"`, clone `git clone --no-local --branch main /Users/tm2508/Dev/AgentPlane <root>/repo`, then `git checkout --detach <SOURCE_SHA>` inside that clone. `git status --porcelain` must be empty before checks. Use actual paths/SHA, not these placeholders. Retain the root; do not copy the untracked DOCX into the clone.
- Record `git --version`, `make --version`, `go version`, `python3 --version`, `golangci-lint --version`, `gitleaks version`. Compare to repository pins. Do not silently install/upgrade or change global configuration. Missing prerequisites are BLOCKED, not PASS.
- Run each target separately, once, capturing its own exit code and relevant raw output: `make fmt-check`, `make lint`, `make test`, `make secrets`, `make secrets-history`, `make secrets-selftest`, `make contracts-check`, `make supervisor-test`, `make smoke-cleanup-test`, `make make-args-test`, `make kind-config-test`, `make image-scan-test`. Expected exit 0. Stub suites are labeled stub-only. Do not chain targets in a way that obscures individual results.
- Run `make mock-smoke` twice separately — each exit 0 with deterministic JSON, SSE/errors, collected children and owned-directory cleanup. Loopback/mock only; no real provider calls. No long wrapped repeat batches in this scope. Report current single-run results separately from historical stress evidence.
- `test -s docs/gateway/openapi.yaml`; `test -s docs/audit/record.schema.json`; `test -s docs/audit/hash-chain.md`; `git ls-files docs/adr` — contracts/ADRs present in the pinned checkout; contract tests provide format checks. Do not call future runtime requirements complete.
- `gh run list --branch main --commit <SOURCE_SHA> --limit 10`; for the relevant run `gh run view <RUN_ID> --json headSha,status,conclusion,url,jobs` — save exact-main CI evidence (lint, unit-test, contracts, gitleaks, image). Missing/pending/red is not green. Branch-only CI is insufficient. Recheck if main changes before owner sign-off; do not chase unrelated main changes automatically.
- For the quickstart edit: verify every changed statement against scripts/mock_smoke.py, scripts/lib/owned_process.py, scripts/lib/owned_workdir.py, ADR-007 and r6. `git diff --check`; review the exact doc diff and links. The clean-clone checks above test existing product; no new product tests for a prose correction.

Capture output with redaction, without shell xtrace, environment dumps, kubeconfig contents or credentials. Preserve failures; no blind retries or hours-long automatic batches. Give progress at least once a minute; if a command stalls or exceeds its documented bound, report the active command and retain evidence. No arbitrary PID/process-group cleanup; use the owned runner's supported interruption path.

### 2. Live kind/image acceptance — separate exact owner authorization required
Do not execute this section merely because the plan or non-live runner prompt was approved. Before beginning, present the exact disposable name `agentplane-p0-exit`, commands, creation/deletion scope and retained evidence root for owner approval. Deletion includes only this newly created cluster's workloads. No reuse/adoption of an existing cluster: inventory first, stop if the name exists. No current-context switching, default/foreign cluster deletion, registry push, or automatic root removal is included.

After approval, execute from the pinned clean clone:

1. `docker version`; `kind version`; `kubectl version --client`; `kind get clusters`; `kubectl config view -o 'jsonpath={.current-context}'`. Record successful initial inventory/context reads, including a successful empty-context read. Never print the full kubeconfig. Ensure the name is absent.
2. `make mock-image`; `make mock-image-scan` — local build, digest and point-in-time scanner evidence, exit 0; retain the report. Do not overwrite an existing scan evidence directory; supply a new SCAN_EVIDENCE_DIR beneath the retained root. Any HIGH/CRITICAL or scanner failure remains visible; do not upgrade or suppress findings without a separate plan.
3. Create a new empty `<root>/live-tmp` and pass it as TMPDIR to both up/down commands. `make kind-up KIND_CLUSTER_NAME=agentplane-p0-exit TMPDIR=<root>/live-tmp` — exit 0, all children collected and private directory removed. Inventory of live-tmp before/after must be empty; keep scan evidence elsewhere.
4. `kubectl --context kind-agentplane-p0-exit wait --for=condition=Ready nodes --all --timeout=180s`; `kubectl --context kind-agentplane-p0-exit get nodes -L agentplane.io/pool`; `kubectl --context kind-agentplane-p0-exit get nodes -l agentplane.io/pool=tenant -o json` — verify configured count/roles/version, system and tenant labels, tenant NoSchedule taint. Placement simulation only; no network isolation claim.
5. Repeat the exact kind-up command once — exit 0 without recreation, changed node identities or duplicate clusters. Compare before/after node UID inventories; successful current-context read must match the initial value after each operation.
6. `kind load docker-image agentplane/mockmodel:p0 --name agentplane-p0-exit` — exit 0, explicitly targeted. Image load is not a claim of an in-cluster serving test.
7. With the exact deletion authorization recorded: `make kind-down KIND_CLUSTER_NAME=agentplane-p0-exit TMPDIR=<root>/live-tmp`; repeat once. Both exit 0; `kind get clusters` proves only the test name removed and unrelated inventory unchanged. Final successful current-context read matches the initial value; live-tmp is empty. Failed create may leave test resources: report them and use only the authorized named teardown, never broaden cleanup.

Any extra interrupt/current-context-switch scenario needs a separate exact owner scope. Do not leave a cluster running without reporting it; if deletion was not authorized, stop and request it rather than inventing teardown permission.

### 3. Explicit owner disposition and final review
Record these separately; green CI or merge permission does not close them:
- Live/clean-clone evidence and any unavailable prerequisite/failure.
- Incomplete long kind/mock repeats: owner accepts historical stress evidence with explicit deferral, or authorizes a bounded list of reruns from the r6 review. No automatic replay of all previous stress forms.
- Mutation: retain inspection-only status. mutation-r4 is pinned to historical df8a840; current-code claims require a separate re-pin/reinspection plan and exact authorization. mutation-r2/r3, the earlier rejected command including W10, and the two unsafe real-child mutants remain unapproved. No mutation execution under this plan.
- Go toolchain / reported CVE-2026-97054 / extra Go checker: unresolved decisions. Do not assert present vulnerability or fix status without fresh official-source verification. Present existing evidence and request a separate decision/plan if needed; no silent acceptance or scope expansion.
- Unused Bash helper deletion remains owner-deferred.
- P0-T0/P0-T1 stale READY_FOR_REVIEW tracker entries: reconcile only with actual review/merge evidence. Do not infer merge/completion merely from files existing on main.

Codex reviews the acceptance report and quickstart diff. Owner then explicitly signs the Phase 0 gate with exact-main SHA/evidence links, or names remaining blockers. Only after that confirmation should Codex expand P1-T1 from the draft P1 roadmap. Neither Claude nor this draft declares Phase 0 done.

## Security-sensitive
Yes: live context/cluster targeting and teardown, owned-process/workdir integrity, image/secret scanning and evidence redaction. No product security change is authorized. Reviewer verifies exact targeting, read-back restoration, collection/cleanup evidence, honest negative results, no secrets and no weakened checks.

## Risks and open questions
All runtime results for this follow-up are unmeasured. Owner live/deletion permission and residual evidence/Go decisions remain pending. Historical quickstart statements about ps identity are stale; correct against current code without expanding guarantees. A successful clean clone can use normal dependency caches; disclose cache use rather than claim a fully cold/offline build. Retain all scratch evidence until separate exact-path cleanup approval.

## Claude runner prompt
Read AGENTS.md, CLAUDE.md, docs/PROGRESS.md, agents/plans/P0-exit-acceptance.md, agents/plans/P0.md and agents/reviews/P0-T5-r6.md. Mode: Phase 0 acceptance follow-up, branch phase-0/exit-acceptance. Owner sending this prompt approves only the plan's non-live scope and the documentation/evidence commits; it does not authorize live cluster/image operations, deletion, mutations, Go changes, cloud/provider commands or Phase 1. Restate the three-commit list, pin main, run the bounded clean-clone checks once each, correct stale quickstart prose against current source, record raw results and blockers, update progress, and push for Codex review. Write only docs/local-development.md, agents/runs/P0-exit-acceptance.md, optional redacted evidence beneath agents/runs/P0-exit-acceptance/, and docs/PROGRESS.md. Leave the untracked root requirements DOCX untouched. No product/test/CI edits, test weakening, automatic long stress loops, merge or phase completion. Retain scratch roots. If live acceptance is next, present section 2's exact commands/name/creation/deletion scope for explicit owner authorization; continue independent evidence work while waiting. Report every missing prerequisite, failure and pending owner decision honestly.
