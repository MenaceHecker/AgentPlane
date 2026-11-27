# ADR-007: Owned-child supervision for the local smoke harness

Status: Accepted (the foreground-runner migration; the explicit supervisor-death and unreapable-child limits; the added ADR and test files); the ownership state machine's details Proposed; workdir removal follows the r3 option (b) decision below (owner-approved to implement, not a Codex APPROVE)
Requirements: NFR-8 (local foundation); resolves P0-T4 review r1 finding 4 and P0-T5 reviews r1 and r2 (finding 1)
Plan: [P0-T5](../../agents/plans/P0-T5.md), section "Supervisor revision"

## Context
The local smoke harness (`scripts/mock-smoke.sh`) starts two mock-model servers and must stop exactly those processes with a bounded wait, always. Three review rounds showed that a Bash helper cannot give that guarantee by signalling a numeric PID:

- A PID is not an identity. Bash can collect a child before an explicit wait, so no registry flag proves the PID is still ours (P0-T5 r1, finding 1).
- Parent plus a one-second start time plus the shell's job table cannot tell a registered child from a new child of the same shell that received the same PID in the same second. This was reproduced twice (review r2, finding 1; the executor's run report, "Finding 1 reproduced"), and the earlier claim that the remaining gap was "microseconds" was never measured and was withdrawn.
- Observation by `ps` and by the shell's job table are separate, non-atomic reads. On Linux with Bash 5.2 they disagreed intermittently (2 of 11 runs of the helper suite in a container; both runs ended in "ps shows pid running but the job table says it has finished"), and the helper then failed closed with a false failure.

Any design that signals a numeric PID from outside the process that owns the child has a window between its last check and the signal. Only the process that is the child's parent, and the only caller of `waitpid` for it, can close that window: until it collects the child, the kernel keeps the child's identity reserved, so the number cannot be reissued.

## Options considered
| Option | Summary | Main trade-offs |
| --- | --- | --- |
| A. Foreground runner owning the children | One Python process per smoke invocation is the parent, sole waiter and sole signaller of both model children; stop is a function call inside it, no PID crosses a process boundary | Migrates the smoke orchestration and its HTTP and SSE assertions to Python; no IPC; cannot survive its own death (see Consequences) |
| A'. Per-child supervisor plus a shell controller | Each child runs under its own supervisor; the shell asks it to stop over a pipe or FIFO | Two lifecycles and a protocol to stop safely; the controller's own FIFO open and connect can hang; a supervisor PID is still signalled as a last resort |
| B. Argv nonce checked through `ps` | Start each child with a random argv[0] and require it before each signal | Closes the reproduced case but not the check-then-signal gap; environment nonces are not visible through `ps` on macOS; `exec -a` broke a multi-call binary on a busybox image. Not adopted |
| C. Register-time collision detection | Mark a registration replaced when a new PID equals it | Only covers replacements registered through the same API. Not adopted |
| D. Accept and document the best-effort identity | Keep the Bash design, state the exposure | Leaves the reproduced case open and would relax the plan's guarantee. Not adopted |
| E. Stop through the server's own interface | A shutdown endpoint instead of SIGTERM | Changes the mock's product code and contract; the kill fallback is still by PID. Out of scope |
| F. Process-group signalling | `setsid` and signal the group | Excluded by the plan |

## Decision

### Owner-accepted
1. **Option A**: one foreground Python runner per smoke invocation owns both model children directly (spawn, poll, signal, reap). `scripts/mock-smoke.sh` becomes a small wrapper that `exec`s `python3 -I scripts/mock_smoke.py`; it starts no background process. Python 3 is already a prerequisite.
2. The **limits stated below** (supervisor death and an unreapable child).
3. The added ADR and test files (`scripts/lib/owned_process.py`, `scripts/mock_smoke.py`, `scripts/tests/`, this ADR).

### Proposed (specification detail of option A, in the plan; implementation and review pending)
- **Invariant.** The runner is single-threaded and the only caller of `waitpid` for its children. It never uses `waitpid(-1, ...)`, a SIGCHLD handler, a Popen destructor or an asynchronous reaper, and it sets SIGCHLD to its default disposition before any spawn. INT, TERM and HUP handlers only set a flag.
- **Object, not PID.** `spawn` returns an opaque owned-child object; `request_stop` accepts only that exact object from the owner that created it (a forged, foreign or completed object sends no signal). The PID is private state used only for the kernel calls.
- **State machine.** NEW, OWNED, TERM_SENT, KILL_SENT, REAPED, LOST. A returned exit status moves the object to REAPED **before anything else**, and a REAPED or LOST object is never signalled. An unexpected ECHILD is LOST: failure, and no signal.
- **Safety argument.** Immediately before TERM or KILL the owner polls the same child. If it is still uncollected it is signalled; if it exits between the poll and the signal, the kernel retains it as a zombie until this sole waiter collects it, so the signal cannot reach another process. The argument is the exclusive ownership, not timestamp uniqueness.
- **Spawn.** `os.fork` in the single-threaded runner; the child resets INT, TERM, HUP and SIGCHLD dispositions, starts its own session (`os.setsid`, which isolates terminal signals and is never used to signal a group), redirects stdin to the null device and stdout and stderr to owned log files, closes other descriptors and `execvpe`s an argv array (no shell). A setup or exec error writes a bounded diagnostic and `os._exit(127)`; the parent reaps and reports it.
- **Shutdown.** Absolute monotonic deadlines: TERM to every still-owned child, a shared 10 s graceful interval, then KILL to those still owned, a shared 5 s reap interval, polling in steps of at most 50 ms (the tests inject shorter deadlines). Stop requests are idempotent and never reset a deadline. Normal success requires every child reaped with status 0; a nonzero exit, an escalation, a malformed start, lost ownership or an unreaped timeout fails the run, the first failure is preserved and the rest reported. Interrupt exit codes are INT 130, TERM 143, HUP 129 and cleanup problems never turn them into success.

### Accepted limits (owner-accepted; there is no recovery fallback)
- A SIGKILL, interpreter or native crash, host failure or permanent suspension of the **runner** can leave live model children. There is no PID, `ps` or process-group recovery path, and nothing ever kills a PID copied from a log. The harness documents the failure and asks the owner to inspect.
- A child still uncollectible after KILL (for example a kernel stall) fails the run after the reap deadline and reports cleanup as incomplete; exiting may orphan it. Deadlines apply to responsive userspace operations; the tests record the tolerance they measured.
- Only directly owned, non-daemonizing mock processes are covered. Arbitrary descendants, hostile same-user code, reparenting tricks, external waiters and inherited SIGCHLD overrides are unsupported, and Go's build tool and its descendants get no kill guarantee. The Go build stays a disclosed preflight outside the model shutdown deadline.
- The identity invariant itself is **not** relaxed: no signal after reaping or on lost ownership.

### Workdir removal (follow-up, P0-T5 r3, option (b); implemented in `scripts/lib/owned_workdir.py`)
Provenance: Codex selected the design in `agents/reviews/P0-T5-r3.md` (section "R3 decision") at the owner's request, and the owner then confirmed approval of option (b) and the temporary Bash retention/failure policy **to implement**, not to merge or complete P0-T5. The earlier quote "I approve, commit, push and merge with main" covers the original supervisor revision only and is not the provenance for this follow-up.

Why the earlier safeguard failed: a device and inode recorded for a directory do not identify it once it is gone. On ext4 and overlayfs a replacement directory of the same name received the identical pair in 199 of 200 and 200 of 200 trials, and the Bash helper removed the replacement (`agents/runs/P0-T5.md`, "Linux CI failure"). Holding a descriptor on the original directory prevented reuse in every trial (0 of 600).

Decision: the runner owns the workdir through descriptors held for its whole life (the physical parent and the created 0700 child, non-inheritable), never through a recorded number. Only fixed single-component names in a manifest are created or adopted; cleanup first verifies (parent entry names the held child, physical parent unchanged, exactly the manifest names, each the recorded regular file and not a symlink) and then unlinks those files through the child descriptor and removes the empty directory with a nonrecursive rmdir through the parent descriptor. Any mismatch retains everything and fails the run. There is no recursion anywhere. Limit, stated plainly: POSIX has no compare-and-remove by directory descriptor, so a hostile same-user rename between the final check and the rmdir is outside the supported threat model; a non-empty replacement is still protected because rmdir fails on it. The device and inode comparison is kept only as a diagnostic.

Temporary Bash policy (superseded for kind by the persistent owner below): the Bash helper has no persistent descriptor owner, so it removes an **empty** directory only (nonrecursive rmdir) and refuses and retains every non-empty one, with a diagnostic that does not print the contents. Its process API is retired. After this follow-up no script calls it; it is kept, with its tests, only until Codex decides whether to delete it.

Kind follow-up (review P0-T5-r4, "Kind decision and next implementation step"; the owner approved implementing it, including regular-file command IO): `scripts/kind_cluster.py` is one foreground Python owner that holds every tool it starts (through the ownership library, with regular-file stdin, stdout and stderr) and its private workdir (through the held descriptors) for the whole run; `scripts/kind-cluster.sh` is a validating exec wrapper. Success returns 0 only after the operation succeeded, the user's kubectl context was restored and read back, every child was collected and the owned workdir was removed; any failure returns nonzero, a surprising or replaced workdir is retained and never recursively removed, and a lost or force-killed child fails the run. This replaces the temporary exit-3 retention policy; the stub suites no longer map exit 3. It was exercised with stub tools only: no live kind lifecycle was run, so historical live acceptance remains an owner-authorized step.

The word "always" earlier in this ADR (a run that "always stops what it started") applies within the stated limits above: supervisor death, an uncollectible child and hostile same-user action are not covered.

### Open (not decided here)
- Live kind lifecycle and clean-clone acceptance (stub-verified only; needs explicit owner authorization).
- Whether to delete the now-unused Bash temp-directory helper and its tests.
- The Go toolchain and CVE-2026-97054, and the added Go topology checker, are separate and not settled by this ADR.

## Consequences
- The reproduced replacement case and the check-then-signal gap disappear for the supervised children: after collection the object is terminal even if a later spawn receives the same number.
- Observation no longer depends on two non-atomic reads, so the Linux false failures of the Bash helper go away with the helper.
- Harder: smoke assertions move from Bash and curl to Python's standard library, the Bash process API and its tests are retired (their obligations are mapped to new tests, not dropped), and the harness cannot clean up after its own death.
- Changed: `kind-cluster.sh` is a wrapper around a persistent Python owner (exit 0 only after verified restoration and owned cleanup). Unchanged: the product code and every contract.

## Requirement mapping
| Requirement | How this decision addresses it | Enforced at (phase / component) |
| --- | --- | --- |
| NFR-8 (local foundation, reproducible checks) | A smoke run that always stops what it started, with bounded waits, reaped statuses and stated failure limits | Phase 0, `scripts/lib/owned_process.py`, `scripts/mock_smoke.py` |
| P0-T4 r1 finding 4, P0-T5 r1 and r2 finding 1 | Replaces PID signalling from outside the owner with sole-waiter ownership | Phase 0, same |

## Validation
Run results are recorded in `agents/runs/P0-T5.md`; this ADR states no measured number itself. Tests: `scripts/tests/test_owned_process.py` (fake adapter and real children), `test_owned_workdir.py` (replacement, inode reuse injected and probed, symlinks, unknown entries, descriptor closure), `test_mock_smoke.py` and `test_owned_process_integration.py` (the foreground runner as a child the test owns: INT, TERM and HUP before startup, during readiness and during cleanup, a failed second spawn, ordinary exceptions, a TERM-ignoring server, a lost child, and a killed supervisor reported as a documented limit), `test_repeat_owned_process.py`, plus the Bash directory and static-guard cases in `scripts/test-owned-process.sh`.

## Owner acceptance
- Evidence: `agents/plans/P0-T5.md`, section "Owner approval and directed merge": the owner approved the concrete revision in session with the words "I approve, commit, push and merge with main", covering the foreground-runner migration instead of per-child IPC supervisors, the crash and unreapable-child limitations, and the added ADR and test files. The same section states that this is not technical approval of the implementation and not a Codex verdict.
- Accepted by owner: partly. Accepted: option A, the stated limits, the added files; later, to implement: workdir option (b) and the temporary Bash policy. Proposed: the state machine and spawn details above. Open: kind cleanup acceptance, the Go toolchain and CVE, and the Go checker.
