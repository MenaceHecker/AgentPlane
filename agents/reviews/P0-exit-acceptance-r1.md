# Review P0-exit-acceptance-r1
Verdict: APPROVE

**Documentation and non-live evidence approval only**, diff `28a779e..ef30c6e`, branch `phase-0/exit-acceptance`. No blocker or major remains in this scope. This does not approve Phase 0 completion, a merge, live operations, mutations, Go decisions, or the outstanding acceptance gates. P0-T5 stays MERGED; owner exit checkboxes stay unchecked.

Read AGENTS.md, progress, the exit-acceptance plan, P0 plan, r6, the run report and its 20 raw files. Reviewed c55d35a (quickstart), 7f3b530 (report/logs), ef30c6e (tracker). Pull --ff-only was current; the untracked requirements DOCX was untouched. Review plan: check documentation against implementation/ADR, reconcile raw results and exact-main CI, diagnose the fixture observation, then commit only this review and progress.

## Requirements check

| Requirement / obligation | Result | Evidence / limit |
| --- | --- | --- |
| NFR-8, Phase 0 reproducibility | Partially covered | Retained clean clone at SOURCE_SHA, local quality/stub checks and two real local mock runs; live lifecycle still unexecuted. |
| NFR-9, secret/image checks | Exact-main CI covered; local secret checks blocked | Independently verified five successful CI jobs including actual image scan and secret scans. Local image-scan-test is only a scanner self-test; no local real image scan. |
| Accurate supervision documentation | Substantially corrected | Python owner, direct-child limits, no external process lookup, descriptor workdirs match source and ADR-007; small prose clarifications deferred below. |
| No expansion beyond non-live scope | Covered by diff and recorded evidence | Only documentation/report/tracker files changed. No checked exit boxes or claimed phase completion. Commands not executed cannot be proven absent solely from a Git diff; the retained transcripts and explicit report disclose the boundary consistently. |

## Findings

No blocker or major. These minors are explicitly deferred to a small subsequent test/docs follow-up, rather than requiring product changes or invalidating the truthful evidence in this report.

| # | Severity (blocker/major/minor/nit) | File:line | Issue | Suggested fix |
| --- | --- | --- | --- | --- |
| 1 | minor | scripts/tests/test_repeat_owned_process.py:120 | Two in-process fake-failure tests retain repeat roots in the process-wide tempfile directory rather than self.dir. Retention inside the runner is intentional; leakage outside the test fixture is not. | Scoped test-only fix handoff below; preserve the runner's retention and failure assertions. Deferred because product/test edits are forbidden in this review and acceptance follow-up. |
| 2 | minor | docs/local-development.md:148 | The combined `the ... workdir` quote is an abbreviation, not either exact source message. “Signalled nothing further” should refer to the affected LOST/terminal child, not imply that other owned children never receive shutdown signals. The timeout suffix follows an attempted escalation, not absence of signals. | In the next prose edit show the two exact message forms, distinguish timeout escalation from LOST, and require positive manual identity/ownership verification before manual stop/removal. No automated logged-PID recovery is proposed here. |
| 3 | minor | docs/local-development.md:160 | The linked ADR summary omits explicit unsupported external waiters, reparenting tricks and inherited SIGCHLD overrides, and the Go-build preflight's separate deadline. `live kind lifecycle is recorded separately` can be mistaken for acceptance of the current Python owner. | State those exclusions or explicitly call this a non-exhaustive summary; say current Python kind acceptance is pending and older live evidence does not cover it. Python 3.8 is a source-stated compatibility floor, not a tested version. Deferred prose precision; the ADR link, tested-host table and report prevent treating it as new measured evidence. |

### Fixture decision and fix-plan handoff

The repeat runner's `main()` deliberately retains logs/root on LOST or uncollectible outcomes (scripts/tests/repeat_owned_process.py:101, 206 onward). Tests at lines 120 and 158 invoke it in-process without setting `tempfile.tempdir`; setting a child's TMPDIR in run_runner does not affect those calls. The fd-leak test already scopes tempfile.tempdir to self.dir and restores it. `make supervisor-test`, **not the smoke-cleanup wrapper**, includes the repeat suite: scripts/test-owned-process.sh runs only ownership/workdir/smoke/integration suites. Thus the report's attribution to smoke-cleanup-test is not established; matching timestamps alone do not identify the command.

Reviewer reproduction: ran only `test_lost_children_are_never_reported_as_reaped` and `test_an_uncollectible_run_fails_in_bounded_time_without_an_outside_guard`, with an isolated process-wide tempfile root. Both passed (2 tests, 0.492 s), creating exactly two retained `repeat-owned-process.*` directories, one containing run01.log, the other root01 and run01.log. Fake adapters only: no real child or signal. Retained reviewer evidence: `/var/folders/rs/w080hsd90pj8wp4ysx2_rpxw0000gn/T/p0-exit-r1-inspection.4b39f9eg`. No deletion of this or historical directories. The executor's reported 22 historical roots/older mock directory are not independently attributed by this probe.

For a separate owner-approved fix plan: one `test:` commit touching scripts/tests/test_repeat_owned_process.py only. Scope every in-process invocation under its own self.dir using tempfile.tempdir with guaranteed restoration, consistent with the existing fd test. Assert intentional retained logs/roots exist inside the fixture before teardown, and that none escapes into an isolated outer test root. Keep LOST/no-signal, exactly-one-escalation, nonzero/reporting and real-run retention behavior unchanged. Run the 15-test repeat suite in an isolated outer TMPDIR, then supervisor-test; require zero escaped roots and unchanged behavioral assertions. Cleanup only the fixture's owned contents after assertions and child collection; no sweep/delete of historical TMPDIR entries. If investigation requires runner/product changes, return for a new plan. This review is a handoff specification, not permission to implement outside the current plan.

## Test review

The underlying tests did not change. Logs exercise meaningful requirements: ownership/state and fd behavior, manifest/decoy protections, real local smoke HTTP/SSE/error checks, kind targeting/context restoration via stubs, Make argument data handling and scanner negative fixtures. No test deletion, skip or weakened assertion appears in this diff. Stub kind results prove no live cluster lifecycle. Scanner fixtures prove the gate's behavior, not the real local image's security. Clean Git status does not prove an empty TMPDIR: the diagnosed fixture issue illustrates the distinction.

Reviewer independently ran the two focused fake-failure tests and static/evidence checks; did not rerun the full executor batch, stress loops, secret scanner, real image/cluster or mutations. No new product-test pass is inferred from prose edits.

## Security review

Removing the ps prerequisite is correct: mock_smoke.py and kind_cluster.py use the ownership library's direct fork/exec/waitpid path; no shell job table/PID-file lookup or process-group signal. PosixAdapter uses setsid to isolate terminal signals, which is not group signalling. Owner's private PID kernel calls do not contradict the absence of an external PID API. The source uses no ps invocation. The remaining ps mentions in the quickstart are the negated prerequisite and optional manual inspection; no stale `best effort`, `job table` identity mechanism or `Blocked` design text remains.

Exact diagnostics are:
- mock_smoke.py:361: `some child is lost, uncollected or unregistered, so the workdir %s was kept for inspection`.
- kind_cluster.py:396: `some child is lost, uncollected or unregistered, so the private workdir %s was kept for inspection`.
- kind_cluster.py:162 timeout suffix: `; it could not be collected, so cleanup is incomplete`.

Manual inspection advice is directionally safe and does not promise cleanup by the runner after ownership loss; it must not be read as permission to signal a numeric PID copied from a log or remove an ambiguous/replaced directory. The ADR limits are authoritative. Workdir.cleanup verifies names/identities before unlinking manifest files relative to the held child fd, then nonrecursive rmdir relative to the held parent fd; later failures can leave partly cleaned state. The quickstart's “any surprise” is a safety summary, not a promise of atomic rollback. No path fallback or recursive deletion was added.

### Live proposal decision (not authorization)

The proposed `agentplane-p0-exit` scope is suitable for owner authorization **with these explicit command/procedure clarifications included in the authorization**; the abbreviated list alone is not a fully specified evidence script:

- Substitute and record the full source SHA and one newly owned retained root; use a fresh clone pinned to it. Stop if initial inventory/context reads fail or the proposed name exists. Never adopt/delete an existing cluster. Check Docker daemon, exact kind v0.31.0, compatible kubectl, exact Trivy 0.76.2 and database network access before creating the cluster; missing prerequisites remain BLOCKED. No version installs/upgrades are implied.
- Keep `make mock-image` and `make mock-image-scan SCAN_EVIDENCE_DIR=<root>/scan-evidence`, no registry push. Use a new scan directory, separate from live-tmp. Image build/scan findings must stay visible, including non-gating ones.
- In addition to readiness/labels/tenant taint commands, capture `kubectl --context kind-agentplane-p0-exit get nodes -o 'custom-columns=NAME:.metadata.name,UID:.metadata.uid'` after first up and after idempotent up; compare to prove no recreation. Read `kubectl config view -o 'jsonpath={.current-context}'` with its own successful exit status before and after **each** up/down; compare with the initial successful read, including empty context.
- Create `<root>/live-tmp` new/empty. After every up/down, inspect it with `find <root>/live-tmp -mindepth 1 -maxdepth 1 -print`; expected no entries. Do not silently delete leftovers. Record individual raw statuses and before/after `kind get clusters` inventory. Failures preserve evidence and do not authorize broader cleanup.
- Exact deletion authorization must include `make kind-down KIND_CLUSTER_NAME=agentplane-p0-exit TMPDIR=<root>/live-tmp` twice, limited to the cluster created by this acceptance run, including a partially failed creation. Do not switch to the disposable current context, delete a foreign/default cluster, remove the evidence root, or invoke cloud/provider commands. Stop/report if the context cannot be positively read/restored.

This review executed none of those live/image commands. Only explicit owner permission can start that scope.

## Run report verification

SOURCE_SHA file and retained clone HEAD both equal `9cc8fd2767fd8fede461540d4e9c8e73cd52c40d`; reviewer independently observed the clone currently clean. Initial clean status/clone construction are executor attestations; summary.txt records 14 empty post-target statuses. All 20 raw files were inspected, including parsed ci-view JSON. Raw logs contain none of the reported user name/email/home-path identifiers. The narrative itself includes the configured email and clone path; its redaction claim is correctly limited to raw logs.

| Evidence | Verified result |
| --- | --- |
| summary.txt | 14 commands: 11 exit 0, three exit 2; all 14 post-status arrays empty. |
| fmt/lint/test/contracts | fmt-check OK; zero lint issues; six Go packages ok; modules verified; Python reference 88 files/0 problems; contracts OK. |
| supervisor-test | 58, 23, 15 tests, each OK. |
| smoke-cleanup-test | 58, 23, 26, 6 plus static guards; all passed. Does not invoke repeat suite. |
| make-args/kind/scanner | 679 checks; 48 kind PASS lines including 27 Python tests, stub-only; scanner self-test all passed. |
| mock-smoke twice | Both exit 0; 361-byte identical responses, 19 SSE events/usage 4/16/20, 400/500 checks, build/main/failing REAPED(0), OK. |
| local secrets targets | Make exit 2 from missing gitleaks, not scan findings. BLOCKED is correct; scanner did not run. No silent install, bypass or false PASS. |
| documentation timing | Tests predate c55d35a and are explicitly attributed to main SOURCE_SHA. Adequate disclosure for prose-only changes; product/CI/Go trees match SOURCE_SHA. |

GitHub independently confirms [run 46045828720](https://github.com/MenaceHecker/AgentPlane/actions/runs/46045828720): exact headSha, completed/success, unit-test/contracts/lint/gitleaks/image all success. Gitleaks steps include full history, tracked tree, go.sum history and scanner regression tests; .github/workflows/ci.yml calls all three local equivalents. This **does suffice as secret-scan evidence for that exact main commit**. It does not reproduce the local installation/reproducibility checks or scan these later documentation commits. Recommend the owner explicitly accept the exact-main CI evidence while preserving local targets as BLOCKED, or install pinned gitleaks and rerun the three; this review does not silently choose that disposition. Later submitted head still needs its own CI.

`git diff --check 28a779e..ef30c6e` exits nonzero solely for supervisor-test.log:71 (trailing whitespace in a unittest docstring line). Accept unchanged raw output as an explicit evidence-only whitespace exception; do not edit raw logs to manufacture a clean check. The report's clean diff-check statement appears under the earlier documentation-only commit, not as a claim the final evidence diff is clean. No other whitespace exception found.

No product/test/CI/Go file changes or merge commits in the reviewed three-commit range. P0-T0/P0-T1 remain unreconciled and disclosed; no owner box checked. The reported historical directory count/cause was an observation, not a fabricated cleanup PASS; this review supplies the narrower cause diagnosis.

## Remaining owner dispositions

None is closed by this APPROVE:
1. Live acceptance with the explicit clarifications above; clean-clone non-live evidence is reviewed, live kind remains stub-verified only for current code.
2. Gitleaks install-and-rerun versus explicit acceptance of exact-main CI-only secret evidence.
3. Incomplete long kind/mock repeats: explicit historical-evidence deferral or bounded rerun authorization.
4. Mutation: mutation-r4 historical pin df8a840 remains inspection-only; current evidence requires separate re-pin/reinspection/authorization. r2/r3, earlier rejected command including W10 and the two unsafe real-child mutants stay unapproved.
5. Go toolchain / reported CVE-2026-97054 / Go checker decisions. No current advisory or fix-status assertion made or verified in this review.
6. Bash helper remains owner-deferred; P0-T0/P0-T1 stale rows require actual review/merge evidence.
7. Minor fixture/prose handoff above, followed by owner exit-gate decision. No Phase 1 work or Phase 0 completion is authorized here.
