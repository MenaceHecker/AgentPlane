# Plan: Final Phase 0 acceptance and owner handoff
Status: DRAFT — review the proposed approvals in CLAUDE_CODE_PROMPT.md before sending
Branch: phase-0/final-acceptance
Requirements: NFR-8/NFR-9 (Phase 0 foundations), requirements DOCX Phase 0 exit gate

## Goal
Finish the remaining Phase 0 work after PR #11 (merge 2d31fbf894bedcb14a86e2d9ddce7faaa3d137ca). Section A's fixture fix, historical approval reconciliation and tests are complete and must not be repeated as new implementation. Apply the separately planned Go patch, collect one bounded acceptance batch and one exact-name live kind lifecycle, then return evidence for Codex review and the owner's exit decision.

Read AGENTS.md, P0.md, P0-wrap-up.md, P0-wrap-up-r1.md and P0-go-patch.md. The prior plans/reviews remain historical evidence. This plan supersedes their remaining execution sequence only when the owner approves it; earlier constraints/failed results do not vanish. Root requirements source is unchanged; Phase 0 remains foundations, not full runtime isolation/compliance.

## Out of scope
Azure/cloud/provider APIs, Phase 1, mutation drivers or unsafe real-child mutants, long wrapped stress replays, supervisor/workdir/kind product redesign, checker changes, deleting the deferred Bash helper, broad filesystem/container/cluster cleanup, global package-manager upgrades, automatic merge or declaration of phase completion. No phase checkbox changes before explicit owner sign-off.

## Owner choices proposed for one approval
These are not accepted merely because Codex wrote this plan. The root prompt proposes explicit approval when sent:
- Apply P0-go-patch.md (1.27.4), retaining the existing Go topology checker without expanded scope.
- Fetch checksum-verified Go 1.27.4, gitleaks 8.30.1 and Trivy 0.76.2 into one retained user-owned tool root; do not replace global installations. Use matching official release assets and checksums, record URLs/platform/hashes and version output before execution. Unknown/missing/contradictory checksums stop installation. No installer scripts piped to a shell, no sudo, no Homebrew state changes. Existing pinned lint/kind/kubectl must already be available; report missing prerequisites instead of adding other installs.
- Authorize local mock image build/scan and the exact live scope below, including deletion only of the new agentplane-p0-exit cluster (twice, including partial-create teardown), and removal of the one stopped binary-inspection container created by the Go subplan. Image tag agentplane/mockmodel:p0 is local and may be rebuilt; no push or foreign container/image deletion.
- Run local secrets targets after the pinned installation, instead of carrying forward a missing-tool exception.
- Explicitly defer the incomplete long kind/mock batches and optional mutation work; retain historical results as historical/incomplete, with no current pass/score claim. mutation-r4 stays pinned to df8a840; r2/r3, earlier rejected command including W10 and two unsafe real-child mutants remain unapproved.
- Preserve the existing Bash-helper deletion deferral. Git email verification is a fact the owner still must confirm; do not manufacture that confirmation from this prompt or Git configuration.

If the owner approves only part, execute independent approved work and record pending items. Do not silently choose risk acceptance instead of the Go patch, waive a blocked image scan, or infer deletion permission from a general request to plan.

## Commit list
1. Execute P0-go-patch.md commit 1 (go.mod, mockmodel/Dockerfile).
2. Execute P0-go-patch.md commit 2 (docs/local-development.md; Go docs plus the one deferred timeout prose fix).
3. `docs(agents): record final Phase 0 acceptance evidence` — agents/runs/P0-final-acceptance.md and optional redacted logs under agents/runs/P0-final-acceptance/. Include exact owner authorizations/dispositions, tool provenance, individual checks, live evidence and remaining blocks. Save raw failures; append follow-up final-main results in a new commit, never overwrite prior reports.
4. `docs: record Phase 0 acceptance handoff` — docs/PROGRESS.md only. Record evidence readiness, PR #11 merger and new review links when real. Leave all phase checkboxes unchecked and P0-T0..P0-T5 MERGED; no phase completion by the executor.

Codex writes the review independently. Claude may open a draft PR for final-head CI after pushing, but must not merge it. Final owner sign-off is a later explicit action, not an executor commit in advance.

## Acceptance checks (the test runner executes these)

### 1. Pin source and preserve evidence
- Pull the handoff branch --ff-only; record HEAD, `git status --porcelain` and `git config user.email` (do not publish an account verification claim). The owner-managed root requirements DOCX and root prompt must not be changed/staged. Stop for unexpected tracked changes.
- After the two source/doc commits, pin TESTED_SHA and create a fresh retained root with `mktemp -d "${TMPDIR:-/tmp}/agentplane-p0-final.XXXXXX"`. Create a clean local clone with `git clone --no-local /Users/tm2508/Dev/AgentPlane <root>/repo` and `git checkout --detach <TESTED_SHA>` inside it; initial Git status empty. Do not copy untracked files/tools into the clone. Tool root may be separately retained; set PATH/GOTOOLCHAIN explicitly without global configuration.
- Run P0-go-patch acceptance once each on that clean source. Record each command/status separately, tool/OS/architecture, cache use, relevant stdout/stderr, image identity and scan database timestamp. Run `make mock-smoke` twice separately: deterministic complete JSON, SSE/errors, direct children REAPED and owned cleanup. No duplicate full batch or automatic retry loop. A failure returns a bounded finding to Codex, not another improvised implementation round.
- Check `test -s docs/gateway/openapi.yaml`; `test -s docs/audit/record.schema.json`; `test -s docs/audit/hash-chain.md`; `git ls-files docs/adr` alongside contracts-check. No future runtime enforcement claim.

### 2. Exact-name live acceptance (only after explicit approval)
Use the same clean pinned clone and verified local image. Proposed name: **agentplane-p0-exit**. Substitute only recorded owned root paths for placeholders below. Capture status for every read/mutation, stopping dependent steps on failure except explicitly approved teardown of this run's partial creation. Record inventory, context and node identity proofs; no unconditional shell chain.

1. `docker version`; `kind version`; `kubectl version --client`; `trivy --version` — reachable Docker, kind v0.31.0, compatible kubectl per deploy/kind docs, Trivy 0.76.2. Tool/network/scan prerequisites must pass before cluster creation; no full acceptance with a BLOCKED scan.
2. `kind get clusters`; `kubectl config view -o 'jsonpath={.current-context}'`; `kubectl config get-contexts -o name` — reads must succeed. Save initial inventory/context; successful empty context is valid. Stop if cluster agentplane-p0-exit **or a conflicting kind-agentplane-p0-exit kubeconfig context** already exists. Do not adopt, recreate or overwrite unrelated configuration; never print full kubeconfig.
3. Verify the successful new image build/scan from the Go plan applies to TESTED_SHA. If source differs, stop or rerun the affected build/checks with a new evidence directory. Never push an image or relax scan findings to obtain acceptance.
4. `mkdir <root>/live-tmp`; `find <root>/live-tmp -mindepth 1 -maxdepth 1 -print` — new and empty. `make kind-up KIND_CLUSTER_NAME=agentplane-p0-exit TMPDIR=<root>/live-tmp` — raw exit 0. After **every** up/down below, run `kubectl config view -o 'jsonpath={.current-context}'` with status checked and compare to initial value; run the same find command, expecting no entries. Keep any failure/leftover for inspection.
5. `kubectl --context kind-agentplane-p0-exit wait --for=condition=Ready nodes --all --timeout=180s`; `kubectl --context kind-agentplane-p0-exit get nodes -L agentplane.io/pool`; `kubectl --context kind-agentplane-p0-exit get nodes -l agentplane.io/pool=tenant -o json`; `kubectl --context kind-agentplane-p0-exit get nodes -o 'custom-columns=NAME:.metadata.name,UID:.metadata.uid'` — correct configured count/roles/version, system/tenant labels and tenant NoSchedule taint. Save UIDs; no network-isolation claim.
6. Repeat the exact kind-up command once; repeat context/empty-root/UID reads. Require raw exit 0, same node identities, no recreation or duplicates. `kind load docker-image agentplane/mockmodel:p0 --name agentplane-p0-exit` — targeted load exit 0; do not claim in-cluster serving from image load.
7. `make kind-down KIND_CLUSTER_NAME=agentplane-p0-exit TMPDIR=<root>/live-tmp` twice separately, with the per-operation context/empty-root checks above. Final `kind get clusters`: test name absent, unrelated inventory unchanged. Delete only the cluster absent at initial inventory and created by this run, including a failed partial creation. Record incomplete teardown; no foreign/default deletion or current-context-switch experiment.

Retain root, scan/binary/tool archives and redacted evidence; no broad cleanup or removal of historical TMPDIR entries. If a child is LOST/uncollectible, retain workdir and report it; no logged-PID/process-group recovery. If creation fails, use only the explicitly permitted named teardown, report residual resources and stop the acceptance claim.

### 3. Review, final main and owner exit
- `git diff --check` on new code/prose; preserve known historical raw-log whitespace with an explicit exception, not edited raw bytes. Record redactions and keep original private evidence separately if paths expose personal data. No secret/environment/kubeconfig dumps committed.
- Push, open a draft PR (read-only CI workflow permissions unchanged) and record `gh pr checks <PR_NUMBER>` and relevant `gh run view <RUN_ID> --json headSha,status,conclusion,url,jobs`: all final-head lint/unit-test/contracts/gitleaks/image jobs successful. Code/tests/image acceptance is still not a phase declaration.
- Codex reviews the Go pin/digest/provenance, timeout wording, actual scan, live lifecycle and decision records; writes agents/reviews/P0-final-acceptance-r1.md. New blockers/majors must be resolved, not merged away. Owner separately authorizes merge after review/CI.
- After the owner-directed merge, fetch/pin final main. `git diff --exit-code <TESTED_SHA>..<FINAL_MAIN_SHA> -- scripts Makefile go.mod go.sum mockmodel cmd internal api deploy .github .pre-commit-config.yaml` must prove identical relevant source/config, or affected checks must be rerun. Record actual exact-main CI, not branch-only CI. Do not rerun a complete live lifecycle solely for evidence-only merges with verified identical source/config.
- Ask the owner to review the final evidence and confirm the Git email fact. Only the owner may state “Phase 0 exit gate passed at <FINAL_MAIN_SHA> with <evidence links> and the recorded deferrals; Phase 1 planning may begin.” This is proposed wording, not received approval. Any evidence/decision still missing remains listed.
- Only after that explicit sign-off, record the confirmed SHA/evidence/dispositions and tick the three existing exit boxes through a documentation handoff/PR. No Phase 1 implementation approval is inferred. Do not append fabricated live/scan results to README or resume.

## Security-sensitive
Yes: compiler/scanner download provenance, Docker digest, scan integrity, owned process/workdir cleanup, explicit live targeting/deletion/context restoration, and honest exit status. Never weaken tests or suppress findings. No cloud credentials, model provider, dependency update, new checker or generalized cleanup script.

## Risks and open questions
The owner has requested plans and a prompt, not yet approved the proposed operations/deferrals. No install, Go patch, image/live run or new runtime result is performed by Codex planning. The version/architecture/digest must be observed, never guessed. If fixed compiler/linter compatibility or a new scan finding blocks progress, return for a narrow plan revision. User email verification and final phase sign-off cannot be inferred by an agent.
