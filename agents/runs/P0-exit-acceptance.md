# Run report P0-exit-acceptance
Branch: `phase-0/exit-acceptance`   Evidence source: `main` at `9cc8fd2767fd8fede461540d4e9c8e73cd52c40d`   Documentation commit: `c55d35a`

Scope: the plan's non-live scope only ([agents/plans/P0-exit-acceptance.md](../plans/P0-exit-acceptance.md)), approved by the owner's runner prompt. **Section 2 of the plan (live kind/image acceptance) was NOT executed and is not authorized yet.** No product, test or CI file was changed, no mutation was run, no cluster, image, Go, cloud or provider operation was performed, nothing was merged, and **Phase 0 is not declared complete** (only the owner can do that).

## Commits made
1. `c55d35a docs: align local quickstart with owned Python supervision` — `docs/local-development.md` only. This commit was made AFTER the checks below; the checks ran against `main` at 9cc8fd2 and do not include it.
2. `docs(agents): record Phase 0 exit acceptance evidence` — this report and the raw logs under `agents/runs/P0-exit-acceptance/`.
3. `docs: update Phase 0 acceptance progress` — `docs/PROGRESS.md` only.

## Intake
- Branch `phase-0/exit-acceptance`, `git pull --ff-only`: already up to date. `git status --porcelain`: only the two untracked owner files (`AgentPlane_Requirements_and_Phased_Plan.docx`, `CLAUDE_CODE_PROMPT.md`). Neither was touched, added or copied into the clone.
- Git email configured locally: `thedynamic2508@gmail.com`. That it is verified on the owner's GitHub account is **not verified** here (still an open item in `docs/PROGRESS.md`).
- `git fetch origin main`; `git rev-parse origin/main` = `9cc8fd2767fd8fede461540d4e9c8e73cd52c40d` (this is SOURCE_SHA; it is the merge of PR #9).
- Evidence root (retained, not deleted): `/var/folders/rs/w080hsd90pj8wp4ysx2_rpxw0000gn/T//agentplane-p0-exit.4ie6iQ`; clone made with `git clone --no-local --branch main /Users/tm2508/Dev/AgentPlane <root>/repo`, then `git checkout --detach 9cc8fd2767fd8fede461540d4e9c8e73cd52c40d`; `git status --porcelain` was empty before the checks and was recorded empty after every target.
- Normal dependency caches (Go module and build caches) were used; this is not a cold or offline build.
- Capture was by plain redirect of each target's stdout and stderr, no xtrace, no environment dump, no kubeconfig. The logs were grepped for the user name, email and home path before copying: none found. Paths are the evidence root only.

## Prerequisites (raw: `agents/runs/P0-exit-acceptance/versions.txt`)
| Tool | Found | Repository pin | Result |
| --- | --- | --- | --- |
| git | 2.50.1 (Apple Git-155) | none | recorded |
| make | GNU Make 3.81 | none | recorded |
| go | go1.27.3 darwin/arm64 | `go 1.27.3` in go.mod | matches the directive (not a statement about CVE status) |
| python3 | 3.14.7 | "3.8 or newer" in source docstrings | recorded; 3.8 itself was not tested |
| golangci-lint | 2.15.0 (built with go1.27.2) | 2.15.0 | matches |
| gitleaks | **not installed** | 8.30.1 | **MISSING: BLOCKED** (nothing was installed or upgraded) |

## Acceptance checks (non-live), each run once on the clean clone at 9cc8fd2
Each target was run separately with its own log; `agents/runs/P0-exit-acceptance/summary.txt` has the exit code, seconds and post-run `git status`. All post-run status lines were empty (no file in the clone was changed by any target).

| # | Command | Exit | Result | Raw log |
| --- | --- | --- | --- | --- |
| 1 | `make fmt-check` | 0 | PASS. `fmt-check: OK` | fmt-check.log |
| 2 | `make lint` | 0 | PASS. `0 issues.` | lint.log |
| 3 | `make test` | 0 | PASS. 6 Go packages `ok` (api/v1alpha1, cmd/kindshape, cmd/mockmodel, internal/contractcheck, internal/kindshape, mockmodel) | test.log |
| 4 | `make secrets` | 2 | **BLOCKED**: `ERROR: gitleaks not found. Install v8.30.1` | secrets.log |
| 5 | `make secrets-history` | 2 | **BLOCKED**: same error | secrets-history.log |
| 6 | `make secrets-selftest` | 2 | **BLOCKED**: `gitleaks not found; the scanner self-test cannot run` | secrets-selftest.log |
| 7 | `make contracts-check` | 0 | PASS. `go mod verify` all modules verified; Go contract tests ok; Python reference `88 files, 0 problems`; `contracts-check: OK` | contracts-check.log |
| 8 | `make supervisor-test` | 0 | PASS. Ran 58, 23 and 15 tests, each `OK` (stub/fake adapters plus local real children; no cluster) | supervisor-test.log |
| 9 | `make smoke-cleanup-test` | 0 | PASS. wrapped summaries: test_owned_process.py 58, test_owned_workdir.py 23, test_mock_smoke.py 26, test_owned_process_integration.py 6; static guards PASS | smoke-cleanup-test.log |
| 10 | `make make-args-test` | 0 | PASS. `all 679 checks passed` | make-args-test.log |
| 11 | `make kind-config-test` | 0 | PASS, **stub-only** (stub kind/kubectl tools, no cluster). 48 PASS lines including test_kind_cluster.py 27 tests; `kind-config self-test: all passed` | kind-config-test.log |
| 12 | `make image-scan-test` | 0 | PASS (scanner self-test; it is not a scan of the real image). `image-scan self-test: all passed` | image-scan-test.log |
| 13 | `make mock-smoke` (run 1) | 0 | PASS. 361-byte byte-identical responses; stream ok (19 events, usage 4/16/20, finish stop); invalid request 400; `--error-rate=1` gives 500; `children: build=REAPED(0), main=REAPED(0), failing=REAPED(0)`; `mock-smoke: OK` | mock-smoke-1.log |
| 14 | `make mock-smoke` (run 2) | 0 | PASS. same result, different ports | mock-smoke-2.log |

Existence checks (contracts-present.log): `test -s docs/gateway/openapi.yaml` 0; `test -s docs/audit/record.schema.json` 0; `test -s docs/audit/hash-chain.md` 0; `git ls-files docs/adr` lists ADR-001 to ADR-007, README.md, template.md and .gitkeep. These show presence in the pinned checkout; `contracts-check` provides the format checks. They do not show future runtime requirements are met.

Only a single-run result for each target was taken. No wrapped repeat batches or stress loops were run (as the plan directs). Historical stress evidence in `agents/runs/P0-T5.md` is separate and was not re-measured here.

## CI on exact main (ci-list.log, ci-view.log)
`gh run list --branch main --commit 9cc8fd2767fd8fede461540d4e9c8e73cd52c40d` found one run, 46045828720 (push, `ci`, completed, success, 3m46s). `gh run view 46045828720 --json headSha,status,conclusion,url,jobs`: `headSha` is `9cc8fd2767fd8fede461540d4e9c8e73cd52c40d`, status completed, conclusion success; jobs unit-test, contracts, lint, gitleaks and image are all `success`. https://github.com/MenaceHecker/AgentPlane/actions/runs/46045828720

Note: the CI `gitleaks` job passing is the only evidence for the secret scans on main. The local `make secrets*` targets could NOT run on this host (gitleaks missing), so they are not independently reproduced here.

## Documentation correction (commit c55d35a)
Corrected against `scripts/mock_smoke.py` (docstring lines 11 and 18-19; messages at 341 and 361), `scripts/kind_cluster.py` (docstring lines 12 and 25; messages at 162 and 396), `scripts/lib/owned_process.py` (docstring line 39) and ADR-007:
1. Prerequisite row for `ps -o ppid/stat/lstart` replaced with a statement that the runner and kind script use no `ps`, shell job table, PID file or process-group signal. The sources contain no `ps` invocation.
2. Troubleshooting row `cannot establish its state ... (smoke harness)` (a message that no longer exists in the source) replaced with the real `some child is lost, uncollected or unregistered ... was kept for inspection` / `it could not be collected, so cleanup is incomplete` messages and what to do (inspect by hand, nothing signals a logged PID).
3. The "Process identity is best effort" limitation replaced with the current ADR-007 limits (direct children only; runner death leaves nothing to clean up; uncollectible-after-KILL fails the run; grandchildren, daemonizing, hostile same-user code and Go build descendants not covered; deadlines apply to responsive userspace operations), the descriptor-based nonrecursive rmdir with the same-user rename limit, and a note that kind and smoke behavior was verified with stubs and a local mock only.
Preserved: scan warnings, the kind warnings, the explicit-context rules, the Make-variable warning, and every other limitation. `git diff --check` clean. The ADR link resolves (`docs/adr/ADR-007-owned-process-supervision.md` exists). The "Python 3.8 or newer" claim is taken from source docstrings and was not tested on 3.8.

## Observations that need an owner or Codex decision (not fixed here)
1. **`make smoke-cleanup-test` leaves directories in TMPDIR.** After this round's single run, `$TMPDIR` contained 22 `repeat-owned-process.*` directories; exactly two have modification times at 18:14 today, matching that run (one holds a `run01.log`); the rest and one `mock-smoke.eee861419523` (Nov 26) are older leftovers from earlier sessions. I did not investigate which test creates them, and I did not delete them (the plan forbids automatic scratch deletion). Whether this is an intentional retained-on-failure fixture or a cleanup defect is unresolved; the plan forbids fixing product/test code here, so this is a handoff to Codex.
2. `gitleaks` is not installed on this host, so the three secret targets are BLOCKED here rather than PASS.

## Not executed (pending owner authorization or decision)
### Live kind/image acceptance (plan section 2): NOT run, not authorized
Nothing in section 2 was run. Proposed exact scope for the owner to authorize or change. Run from a fresh clean clone at the pinned SHA (a new `mktemp -d` root, not the root above unless you say so); disposable name `agentplane-p0-exit`; evidence root retained afterwards. **Creation scope:** exactly one new kind cluster `agentplane-p0-exit` (three node containers) plus the local image `agentplane/mockmodel:p0`. **Deletion scope:** only `agentplane-p0-exit`, and only by `make kind-down KIND_CLUSTER_NAME=agentplane-p0-exit TMPDIR=<root>/live-tmp`. No existing, default or foreign cluster is touched, no current-context switching beyond the script's own restore-and-read-back, no registry push, no automatic root removal, no cloud or provider call.
1. `docker version`; `kind version`; `kubectl version --client`; `kind get clusters`; `kubectl config view -o 'jsonpath={.current-context}'` (stop if the name exists; never print the full kubeconfig)
2. `make mock-image`; `make mock-image-scan SCAN_EVIDENCE_DIR=<root>/scan-evidence` (needs network for Trivy's database; findings stay visible)
3. `mkdir <root>/live-tmp` (new, empty); `make kind-up KIND_CLUSTER_NAME=agentplane-p0-exit TMPDIR=<root>/live-tmp`
4. `kubectl --context kind-agentplane-p0-exit wait --for=condition=Ready nodes --all --timeout=180s`; `kubectl --context kind-agentplane-p0-exit get nodes -L agentplane.io/pool`; `kubectl --context kind-agentplane-p0-exit get nodes -l agentplane.io/pool=tenant -o json`
5. repeat the same `make kind-up` once (expect exit 0, no recreation, same node UIDs, same current context)
6. `kind load docker-image agentplane/mockmodel:p0 --name agentplane-p0-exit`
7. `make kind-down KIND_CLUSTER_NAME=agentplane-p0-exit TMPDIR=<root>/live-tmp`, run twice; `kind get clusters`; final current-context read; `<root>/live-tmp` empty
Prerequisites not yet checked for this section: Docker daemon, kind v0.31.0, kubectl, Trivy 0.76.2 (the doc says exactly this version), network access to the Trivy database mirror. Missing items will be reported BLOCKED.

### Owner dispositions still open (plan section 3; none is closed by CI, the merge or this report)
- Live/clean-clone evidence: section 2 above, not run.
- Incomplete long kind/mock repeats: not rerun; owner accepts the historical stress evidence with explicit deferral, or authorizes a bounded list of reruns.
- Mutation: inspection-only; mutation-r4 is pinned to historical df8a840; mutation-r2/r3, the earlier rejected command (including W10) and the two unsafe real-child mutants remain unapproved. Nothing was executed.
- Go toolchain / CVE-2026-97054 / extra Go checker: unresolved; this report asserts nothing about vulnerability or fix status (the local `go1.27.3` only matches the go.mod directive).
- Unused Bash helper deletion: remains owner-deferred.
- P0-T0 and P0-T1 tracker rows still say READY_FOR_REVIEW; they were not changed, because no review or merge evidence for them was gathered in this round.

## Failures and fixes
No check failed. Three targets are BLOCKED on the missing gitleaks prerequisite. No product, test or CI fix was attempted.

## Blocked
- `make secrets`, `make secrets-history`, `make secrets-selftest`: gitleaks 8.30.1 is not installed. Owner decision: install it (then rerun these three) or accept the CI `gitleaks` job as the only secrets evidence.
- Live acceptance awaits explicit owner authorization (above).
