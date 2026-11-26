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
