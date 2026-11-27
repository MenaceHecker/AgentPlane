# P0-T5 mutation-run authorization assessment

Status: NOT READY TO AUTHORIZE AS WRITTEN
Inspected at repository head `98983f4`; inspection only, no mutation run or deletion executed.

## What was inspected

Read the latest blocked/run-report sections and the complete existing scratch driver:

`/private/tmp/claude-501/-Users-tm2508-Dev-AgentPlane/291c0664-5222-484a-8105-92977b8aa3fe/scratchpad/mut-iso.py`

SHA-256 of inspected bytes: `fd1feaddfc5e1004fd0535821fe66c5fba83244e891a08f14deae32eabeff0c3`.
Python AST parsing passed without importing or executing the driver. This proves syntax only.

The proposed command in the run report expands to the following (recorded here as text, **not authorization**):

```sh
cd /Users/tm2508/Dev/AgentPlane && \
PATH=/private/tmp/claude-501/-Users-tm2508-Dev-AgentPlane/291c0664-5222-484a-8105-92977b8aa3fe/scratchpad/bin:/private/tmp/claude-501/-Users-tm2508-Dev-AgentPlane/291c0664-5222-484a-8105-92977b8aa3fe/scratchpad/venv/bin:$PATH NO_COLOR=1 \
python3 -B -u /private/tmp/claude-501/-Users-tm2508-Dev-AgentPlane/291c0664-5222-484a-8105-92977b8aa3fe/scratchpad/mut-iso.py /private/tmp/claude-501/-Users-tm2508-Dev-AgentPlane/291c0664-5222-484a-8105-92977b8aa3fe/scratchpad/mut-r2-list.py > /private/tmp/claude-501/-Users-tm2508-Dev-AgentPlane/291c0664-5222-484a-8105-92977b8aa3fe/scratchpad/mut-r2-new.out 2>&1
```

`mut-r2-list.py` does not exist. Thus the actual 29-entry list, mutation replacements and test commands cannot be inspected or approved. The report's names are not a substitute. W10 remains excluded/unexecuted. The command also overwrites its output file; approval must distinguish that from scratch-copy deletion.

## Findings in the driver

| Driver lines | Observation | Required correction |
| --- | --- | --- |
| 12 | Executes the entire supplied Python list file with exec before applying any copy guards. The missing list could execute arbitrary actions in the real checkout or redefine driver globals. | Supply the actual list as declarative data; parse without execution, validate exact field types, relative paths confined to the copy, allowed validity modes and explicit argv test commands. Human-review/hash the complete list. |
| 17–24 | Uses shell=True and kills a numeric process group on timeout; then communicate has no timeout and can hang if a detached descendant retains its output. This is not merely deleting scratch copies, and does not satisfy the proposed no-process-group-fallback design. | Provide a separately reviewed owned-process timeout design; do not assume killing the command group safely owns every descendant. Bound output collection and failure handling. Stop/report when ownership is lost. No ps/PID-file/group kill fallback. |
| 27–31 | Resolves mkdtemp paths and archives HEAD separately for each copy; a sibling `.tar` is created and unlinked outside the copy. Failures during archive/extract have no outer cleanup path. | Pin one explicit commit SHA at startup; keep archives inside the owned run root or in memory. Define cleanup/retention on every startup failure. Validate archive paths/symlinks against the trusted source policy before extraction; never write outside the copy. |
| 33–44 | Direct-parent/prefix/not-symlink/CREATED checks improve the prior boolean guard. But CREATED stores only a pathname, not directory identity, so replacement of that path can still pass; the deletion checks are asserts, which Python optimization can disable. Path checks also do not eliminate concurrent rename races. | Use explicit refusal branches, record parent and child device/inode/physical identities at creation, and revalidate them. Document the same-user concurrent-filesystem threat limit; retain scratch copies when safe deletion cannot be established. Prefer an initial no-recursive-cleanup mode, with any later deletion separately inspected and approved. Do not substitute a different deletion mechanism to evade the rejected action. |
| 45–49 | No validity mode exists for `none`. The previously proposed documentation mutant V10 uses that mode; unpacking its None return would abort the run rather than classify it. Make -n may expand Make functions; it is not a side-effect-free parser. | Define all accepted modes explicitly, reject unknown modes before starting, and distinguish syntax validation from bounded scratch execution. A documentation-only mode must be deliberate. |
| 50–51 | Prints a failed/hung baseline but still proceeds to mutation scoring. | Abort scoring on any baseline failure, hang or harness error. Preserve the failing output and classify the run as blocked. |
| 64–68 | Any nonzero test exit is KILLED, even an infrastructure error, missing tool or unrelated failure; only two failure lines are printed. | Require evidence of the intended behavioral assertion. Separate harness errors and hangs; retain full per-mutant output and exact applied diff. Never count parse failure, missing dependency or safety rejection as a kill. |
| 71 | A shell pipeline filters requirements out of status output rather than comparing a complete before/after snapshot. | Use read-only argv git calls and compare the complete tracked/index state plus the owner-managed DOCX identity/hash without printing contents. Preserve all initial untracked files. |

The actual driver does archive/extract with argv arrays and uses a fresh copy per mutant. These are useful protections. They do not compensate for the missing executable input, timeout behavior, deletion-identity gap or invalid scoring conditions.

The report's phrase "only recursive deletion" describes shutil.rmtree, but the same run can signal a whole process group and execute arbitrary Python from its input. Those side effects must be included in any authorization request. No assertion here claims that the historical driver actually deleted an unrelated directory; this is a static assessment of its safeguards.

## Concrete next authorization request

1. Prepare corrected driver/list artifacts for inspection only, with no execution or cleanup of the previously rejected action. Supply exact bytes/hashes, pinned source SHA and resolved paths.
2. Include the baseline command, each syntax/compile command, each mutant diff and each behavioral test command; explicitly list all process signals, file overwrites, archive writes and deletions. Scope any subprocess-tree management separately from the application supervisor.
3. Prefer retaining every scratch copy during the first run. No deletion is needed to establish mutation results. If removal is later requested, present exact created paths and ownership checks for approval; do not turn this recommendation into permission to rerun the rejected command by splitting it.
4. Approval must come from the owner after inspecting that concrete command and artifacts. Submit it through the normal approval check. Do not concatenate, encode, change tools or split operations to bypass a rejection.

This assessment does not authorize implementation of the supervisor or execution of any mutation. Findings 2–5 remain awaiting code review; finding 1 awaits approval of the revised task plan. No CI result is inferred for unpushed fixes.


## R3 inspection: mutation-r2 (2026-11-26)

Verdict: **DO NOT AUTHORIZE A RUN OF THESE BYTES.** Inspection and AST parsing only; no mutation was executed. Earlier rejected command including W10 remains unapproved. The owner must approve a corrected, concrete action through normal approval review; this document grants no execution permission.

Artifact root:
`/private/tmp/claude-501/-Users-tm2508-Dev-AgentPlane/291c0664-5222-484a-8105-92977b8aa3fe/scratchpad/mutation-r2/`

| Artifact | SHA-256 recomputed by reviewer |
| --- | --- |
| mutate.py | 1b57d97e566d3e915ac801174680c7722febcda44b5fa55564ee50351a4f66e5 |
| mutations.json | 7e735391349c6cde6b3495f952326bcc207903a970638c80f288bc4ee60c6020 |
| README.txt | e6894c4d819680beffa572d35d7194f3f62ed8e65e3a80b6de78c28eea4a314d |

All six old patterns occur exactly once in source `737a777e0618c9431df4ce9ec464a87f51a1af40`. Driver and all six substituted Python files AST-parse. That establishes syntax, not safe execution or a behavioral kill.

Improvements: declarative JSON, in-memory pinned archive with member validation, argv arrays, fresh retained copies, full command logs, baseline failure stops scoring, no driver recursive deletion or process-group kill.

Remaining reasons to refuse:

1. **M6 can orphan actual busy workers.** It removes the repeat runner's final shutdown, then runs all repeat tests. The busy-worker test starts infinite-loop Python processes under that mutated runner; the outer test owns only the runner, and has no retained authority to reap/stop its children after the runner exits. The test's TemporaryDirectory cleanup removes files, not processes. M1–M5 also run real-child cases using the mutated library; RealChildTests lacks unconditional owned-child teardown on assertion failure. Use deterministic intercepted adapters for dangerous mutants; do not run real lifecycle fixtures against broken supervision without an independently reviewed safe owner. No rediscovered PID/group cleanup.
2. **Kill oracles are too broad.** M1, M2 and M6 accept any `(FAIL|ERROR): test_`. A missing import or unrelated test exception can count as a kill. M3–M5 name more specific tests but still need the intended assertion evidence, not merely the test name. Give every mutant an exact expected assertion/test and distinguish missing-tool/infrastructure/setup errors.
3. **Baseline coverage is incomplete.** README baselines only test_owned_process.py, but M6 runs test_repeat_owned_process.py. Baseline each distinct suite with identical environment before scoring any mutant. Smoke second-spawn cleanup mutation remains blocked until that runner exists; M6 is not a substitute for it.
4. **Timeout/interruption ownership is incomplete.** Runner never installs flag-only handlers or uses finally shutdown. An ordinary exception or INT/TERM/HUP can abandon its child. Reused Owner shutdown deadlines can be stale across multiple timed-out commands. A HANG with incomplete reap continues into more mutants; validity-command HANG/BLOCKED is incorrectly counted INVALID and continued. Stop the whole run on lost ownership/unreaped cleanup; classify validity syntax failures separately from harness failures/timeouts. Bound git subprocesses too; they currently use unbounded subprocess.run outside the claimed owned-command path.
5. **Pinning/state verification is incomplete.** Runner imports the library from the mutable checkout, not a verified pinned copy. The final snapshot is skipped on baseline failure/exception/interrupt; porcelain plus diff --stat does not prove unchanged file/index bytes (same-length changes can leave both unchanged). Snapshot exact tracked/index differences and initial untracked-file identities/hashes, plus requirements DOCX, in finally; do not print secrets. Validate finite positive timeouts and precise run-root confinement.

### Command, retention and deletion scope

**No exact command is authorized now.** The README still has placeholders, so it is not a concrete approval request. After corrections, present one fully resolved argv command with the newly inspected driver/list hashes, pinned source SHA, explicit interpreter, timeout, complete baseline suite coverage and unique nonexistent absolute run root. Do not silently use HEAD or a changed checkout library. The currently inspected source candidate is `737a777e0618c9431df4ce9ec464a87f51a1af40`; later fixes require a new source pin and pattern inspection.

First authorized run, if later approved: retain every copy, diff, full log, manifest, baseline and classification under that single newly created run root, including on failure. Driver deletion scope is **none**. Test fixture deletion must be separately enumerated as exact test-created entries inside `<run-root>/tmp`, with descriptor ownership/refusal guarantees; no cleanup of the checkout, requirements DOCX, old scratch roots, other mutation directories or unrelated processes. Do not overwrite a previous output file. Subsequent deletion requires owner approval of the exact retained root and reviewed ownership checks, not a blanket "remove by hand" instruction. This r3 refusal does not reauthorize the old command by changing its tool, splitting it or dropping W10.
