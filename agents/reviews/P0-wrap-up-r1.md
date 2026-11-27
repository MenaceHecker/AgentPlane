# Review P0-wrap-up-r1
Verdict: APPROVE

**Section A approval only: test-fixture containment, documentation and recorded evidence**, at `ca9a704`, diff `2a45dfd..ca9a704`, base main `556fab4`. No blocker or major remains in this scope. This is not live acceptance, merge permission, completion of Phase 0, an advisory risk waiver or approval of section B recommendations. Final-head CI remains required before merge.

Read AGENTS.md, progress, wrap-up plan, exit-acceptance r1, P0-T5 r6, wrap-up report and all seven raw files. Reviewed f142d67, 0d104a1, 9f9786d and ca9a704. Pull --ff-only was current. Plan: inspect assertions and prose against source, reproduce current/old containment, reconcile historical approvals and CI/advisory evidence, then write only this review and progress. No live/image/mutation-driver/cloud/provider command or merge. The requested old-containment comparison used only a disposable copy, not the checkout. The untracked requirements DOCX was untouched; the root prompt file was absent at review intake and was neither created nor removed.

## Requirements check

| Requirement / obligation | Result | Evidence / limit |
| --- | --- | --- |
| NFR-8: scoped local reproducibility | Section A covered, final gate partial | Current suites pass; real failure evidence stays in the fixture; old behavior fails the regression. Final-main clean clone/live lifecycle remain pending. |
| NFR-9: scoped security evidence | Partial | Independently verified green pre-fix main CI, including gitleaks/image. Local gitleaks and Trivy remain reported missing. No local image scan or current-head CI claimed. |
| Exit-acceptance r1 minor 1 | Closed | All in-process calls use scoped tempfile.tempdir; retained evidence assertions and outer-root check reproduce the defect. |
| Exit-acceptance r1 minor 2 | Substantially closed; one minor precision issue deferred below | Exact diagnostics, LOST/escalation distinction and safe manual-inspection rule added. Timeout-suffix absence is not by itself proof of collection. |
| Exit-acceptance r1 minor 3 | Closed | Unsupported cases, source-stated Python floor, Go preflight and pending Python-kind acceptance now explicit; scan/context/placement/race warnings preserved. |
| History/status/evidence integrity | Covered for this handoff | Actual MERGED statuses correctly separated from approval-file provenance and owner gate decisions. No review/report history rewritten. |

## Findings

No blocker or major. One minor is explicitly deferred to the next quickstart edit; it does not justify product changes or invalidate the section A test/evidence approval.

| # | Severity (blocker/major/minor/nit) | File:line | Issue | Suggested fix |
| --- | --- | --- | --- | --- |
| 1 | minor | docs/local-development.md:151 | “Without the suffix the child was collected” is too strong. kind_cluster.py:162 uses child.done, which is true for LOST as well as REAPED. A timeout whose shutdown loses ownership can omit the suffix and later emit the explicit LOST diagnostic. | Say to check the final ownership diagnostic/state: the suffix confirms incomplete collection, but its absence does not prove REAPED; LOST overrides any stopped wording. Also describe TERM/KILL as bounded escalation as needed, not an unconditional promise that both signals were delivered. Deferred prose clarification because the adjacent LOST/manual-inspection rules and unchanged fail-closed implementation prevent this being a new cleanup/security defect. |

### Historical approval closure decision

**Use immutable historical review links and record reconciliation here; do not restore or rewrite old review files.** Independent Git checks establish:

- PR #2 merged c458370df92229ebbb800bcaf1bc3e3f8f7330b6, second parent f4fc4c3; both are main ancestors. [P0-T0 historical APPROVE](https://github.com/MenaceHecker/AgentPlane/blob/c3474d46d6c3c5255a08a30bea76a264018faffc/agents/reviews/P0-T0-r1.md) reviews that corrected merged snapshot; the difference f4fc4c3..c3474d4 is only the review file. Its single evidence minor is closed. Main's older CHANGES_REQUESTED file is historical, not evidence of a still-open implementation finding.
- PR #3 merged dcb644c4b3d00e719e8f19998dcb43342022c14a, second parent 7d5d133; both are main ancestors. [P0-T1 historical r2 APPROVE](https://github.com/MenaceHecker/AgentPlane/blob/a967194f0543cd39c12d4408896231794f2a7e36/agents/reviews/P0-T1-r2.md) explicitly reviews 7d5d133. Through 058f2d9 the only differences from that merged head are the r2 review and progress. Its synthetic-fixture prose minor remains explicitly deferred in that review.
- c3474d4, a967194 and 058f2d9 are not main ancestors. The main-tree absence is a provenance gap, now reconciled by these links, not a reason to claim an approval was merged. Both tracker statuses should remain MERGED; BLOCKED/CHANGES_REQUESTED would misstate actual merge history.

Later changes to bootstrap scripts/Makefile/CI **require their own task review coverage**, not retroactive coverage by the bootstrap r2. Existing records to use are P0-T3 r3 (scanner/go.sum history), P0-T4 reviews including r4 (later scanner controls/mock checks), and P0-T5 reviews culminating in r6 (Make argument boundary, CI/image gates, kind and supervision). Git history confirms check-go.sh and scan-secrets.sh have no changes from P0-T3 reviewed head 539bde8 to baseline main; test-secrets.sh later changed in the mock task. P0-T5 r6 explicitly checks product/CI equivalence to its reviewed implementation. This section A diff adds no further bootstrap/product/CI change. Do not reopen all old code solely because a historical approval file was omitted, or claim this narrow review newly approves every later change. Final phase review still checks the resulting evidence and any relevant new delta.

## Test review

AST comparison found no removed test methods and every old assertion call preserved verbatim, including timing bounds, exact TERM then KILL, LOST/no-signal, descriptor counts and real failure retention. The moved lost-child test retains the same fake adapter and argv. New assertions prove one retained root inside self.dir and, for the uncollectible case, run01.log exists. Production repeat_owned_process.py and ownership/workdir/kind/smoke sources are unchanged.

The global tempfile mutation is **per test**, not permanent for the class: setUp creates its own outer stand-in, registers restoration, then changes tempfile.tempdir. in_process restores its saved value in finally. Unittest cleanups run LIFO even after assertions/tearDown fail: owner shutdown, owner close, SIGCHLD restore, tempfile restore, then owned outer/inner temporary-directory cleanup. Those callbacks are registered before spawning. Existing nested tempdir handling in the fd test remains compatible. This is sound for the current serial suite; parallel tests in one interpreter would require a different fixture strategy because tempfile.tempdir and adapter/signal state are process-global. No such parallel execution is introduced.

tearDown checks before deleting the stand-in. It can add a second failure to the original body assertion (as the old-code demonstration intentionally does), but unittest records both and still runs cleanups: it does not mask a passing result or leak the previous tempdir into the next normal test. Subprocess runners use self.dir as TMPDIR; teardown is not offered as a proof against arbitrary late asynchronous filesystem writes or every possible temp filename.

Independent reviewer evidence retained at `/private/tmp/p0-wrap-r1-6azteqax`:

| Check | Actual reviewer result |
| --- | --- |
| Isolated TMPDIR, exact requested repeat-suite command | Exit 0, 15 tests in 22.432 s, OK; external outer root empty. |
| Separate isolated TMPDIR, make supervisor-test | Exit 0; 58/23/15 tests, all OK; no escaped repeat roots. Outer root contained one 649-byte xcrun_db artifact, so I cannot reproduce a literally empty supervisor outer root on this run. No attribution beyond the observed filename is claimed. Artifact retained. |
| Disposable scripts copy, only three in_process containment lines removed | Exit 1, 15 tests in 22.421 s, FAILED (failures=4), exactly the two known tests' retention/body and outer-root assertions; external outer root empty after fixture teardown. No production checkout rewrite or mutation driver. |

The first reviewer orchestration stopped at its overly broad empty-outer assertion after the successful supervisor suite found xcrun_db. I retained that result, then ran the pending old-copy comparison separately. This is a reviewer evidence distinction, not an executor test failure or an escaped repeat fixture. No historical scratch directories were removed. No full live/stress/image/secret batch was rerun.

## Security review

Exact smoke/kind retained-workdir, LOST and reap-deadline messages match source at mock_smoke.py:361 and kind_cluster.py:354–358,396. Kind's escalation message is emitted only after excluding LOST/uncollected children. Smoke extends Owner.shutdown's failures, which emits `name: ignored SIGTERM and had to be killed (status ...)`; test_mock_smoke.py:114 also asserts this condition. The docs correctly call it the same condition, not the identical full string. The timeout prefix/suffix matches kind_cluster.py:160–162, with finding 1's semantic caveat.

Manual inspection now requires positive identification and tells the user to leave ambiguous processes/directories alone. This is human recovery guidance, not a new unique-identity mechanism: command/start-time/path/content observations alone cannot provide the old, disproven automatic identity guarantee against reuse or hostile same-user action. No automated logged-PID signalling or broad deletion command was added. Direct-child/no-descendant, supervisor-death, uncollectible-child, external-waiter/reparenting/SIGCHLD and userspace-deadline limits match ADR-007; the filesystem final-rmdir race exclusion remains. Scan/context/placement/Make-variable warnings are unchanged. Python 3.8 is explicitly source-stated and untested; no new version compatibility test is claimed.

### Go advisory assessment and owner gate

The reviewer fetched the [official GO-2026-6859 record](https://vuln.go.dev/ID/GO-2026-6859.json); its parsed contents equal the committed record. The [official release index](https://go.dev/dl/?mode=json) and committed index identify stable fixed releases 1.27.4 and 1.26.11. Repo/host pins remain 1.27.3, within the advisory's version range. The described condition involves HTTP/1 MaxBytesReader/MaxBytesHandler limit handling racing with concurrent response-header writes. These facts make the advisory relevant to the dependency/toolchain inventory, **not proof that the mock is exploitable**.

Repository Go sources contain no MaxBytesReader/MaxBytesHandler use; mockmodel's body limit uses io.LimitReader in handler.go:258, and it imports net/http. That argues against the described application-level trigger in this handler. It is not a complete dependency/call-graph audit, a substitute for reachability analysis, or a blanket declaration that Go 1.27.3 is safe. Green HIGH/CRITICAL image gating does not dispose of the recorded MEDIUM finding.

Recommend a separately planned update to the fixed 1.27 patch line with matching Docker digest/toolchain pins and rerun checks/image scan. Alternatively the owner must explicitly accept/defer the scoped risk with this evidence and its limits; no risk acceptance is inferred here. Either upgrade or topology-checker scope change requires its own plan. The existing Go checker is a separate owner decision and does not remediate this advisory. No upgrade, checker change, govulncheck run or live image operation performed in this review.

## Run report verification

All seven raw files were inspected, JSON parsed and summary counts cross-checked. repeat-suite-verbose.log records 15 OK; supervisor-test.log records 58/23/15 OK; old-containment-demo.log records four assertion failures in the two expected cases. Its disclosed `<scratch>` path-prefix substitution is acceptable privacy redaction, not byte-for-byte raw evidence; assertions/results remain intact and reviewer independently reproduced the behavior. Keep that qualification.

predecessor-and-ci.txt agrees with independent ancestry, merge-parent, review-diff/verdict and GitHub PR #2/#3 queries. Immutable approvals apply to the actual merged snapshots as explained above. No approval was restored/forged. Exact-main [CI run 46082844858](https://github.com/MenaceHecker/AgentPlane/actions/runs/46082844858) independently confirms head 556fab4ef91d1d31dfaf50a6463d396172205eca, completed/success, all five lint/unit-test/contracts/image/gitleaks jobs successful. This is **pre-fix** main, not final-head CI. No wrap-up PR exists at review time; workflow triggers explain the absence of branch CI. Open a PR and require final-head checks before merge; do not describe a push alone as CI evidence.

`git diff --check 2a45dfd..ca9a704` is **not clean**: preserved raw whitespace at predecessor-and-ci.txt:24–26 and supervisor-test.log:71. Accept these four raw-evidence-only exceptions, as with prior raw transcripts. New prose/test edits are clean. The report's discussion of prior whitespace is not a complete enumeration of the new raw exceptions; do not normalize the logs to hide them or claim the full diff check passed.

Tool availability/version inventory is reported, not an independent installation/provenance check by this reviewer. Both local gitleaks and Trivy are missing; no local scans passed. One read-only Docker daemon version query was run by the executor and expressly disclosed. Thus “no section C command” is not literally true at the command-name level; no live lifecycle/image operation ran. The inventory is consistent with section B prerequisite gathering, harmless and adequately disclosed. Likewise section B item 6 already has an existing owner deferral: not all seven items lack any historical decision. No new recommendation is treated as accepted. Git email remains unverified.

The diff changes only the planned test file, local quickstart, new report/raw files and progress. No product/CI/Go source or historical report/review edit, merge commit or checked exit box. P0-T5 remains MERGED. Diff and logs support the disclosed scope; they cannot independently prove every unrecorded command was absent.

## Remaining owner decisions and live authorization

This APPROVE leaves the following explicit:
1. **Live acceptance:** not authorized or run. agentplane-p0-exit's exact-name creation/idempotence/UID/context/empty-root/load/two-down scope in section C remains suitable for authorization, with name-exists/read-failure stops and no foreign/default deletion or push. Install/authorize installation of checksum-verified Trivy **0.76.2** before the full scope, and verify database network access. Alternatively explicitly authorize a reduced lifecycle-only run and mark the image-scan step BLOCKED; that is not successful complete section C or a clean image result. No install or reduced-scope exception is authorized by this review.
2. **Gitleaks:** owner installs pinned 8.30.1 and reruns three local targets, or explicitly accepts exact-main CI-only evidence while preserving local BLOCKED results.
3. **Long repeats:** explicit historical-evidence deferral or exact bounded rerun authorization; old incomplete entries stay incomplete.
4. **Mutation:** separately decide deferral/current-pin inspection and authorization. mutation-r4 stays historical df8a840/inspection-only; r2/r3, rejected command including W10 and two unsafe real-child mutants remain unapproved. No driver execution here.
5. **Go/CVE/checker:** decide the separately planned patch or explicit scoped risk disposition, and checker scope, based on the evidence/limits above; no security waiver from this APPROVE.
6. **Bash helper:** preserve the existing explicit owner deletion deferral; no new removal authority. Whether to reaffirm it for closure is still an owner choice, not a newly accepted recommendation.
7. **Git email:** owner confirms verified-account status; local config alone proves nothing about verification.

Historical task approval provenance is reconciled by immutable links above, not a new phase sign-off. Final-main acceptance, owner dispositions, final-head CI and owner Phase 0 confirmation remain. No Phase 1 authorization.
