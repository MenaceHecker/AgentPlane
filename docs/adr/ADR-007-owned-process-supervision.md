# ADR-007: Owned-child supervision for the local smoke harness

Status: Accepted (the foreground-runner migration; the explicit supervisor-death and unreapable-child limits; the added ADR and test files); the ownership state machine's details and the workdir-removal safeguard Proposed or open
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

### Open (not decided here)
- **Workdir removal safeguard.** The runner must remove only its own workdir. The reviewed safeguard for that, the helper's device and inode comparison in `scripts/lib/owned-process.sh`, does not hold on filesystems that reuse a freed inode: on ext4 and overlayfs a replacement directory of the same name received the identical identity in 199 of 200 and 200 of 200 trials, and the helper removed the replacement (`agents/runs/P0-T5.md`, "Linux CI failure"). Holding a descriptor on the original directory prevented reuse in every trial (0 of 600). What identity the removal must check is **a planner decision that has not been made**; until it is, the runner's workdir removal is not implemented and the Python migration of the smoke runner is blocked.
- The Go toolchain and CVE-2026-97054, and the added Go topology checker, are separate and not settled by this ADR.

## Consequences
- The reproduced replacement case and the check-then-signal gap disappear for the supervised children: after collection the object is terminal even if a later spawn receives the same number.
- Observation no longer depends on two non-atomic reads, so the Linux false failures of the Bash helper go away with the helper.
- Harder: smoke assertions move from Bash and curl to Python's standard library, the Bash process API and its tests are retired (their obligations are mapped to new tests, not dropped), and the harness cannot clean up after its own death.
- Unchanged: the filesystem helpers used by `kind-cluster.sh`, the product code, and every contract.

## Requirement mapping
| Requirement | How this decision addresses it | Enforced at (phase / component) |
| --- | --- | --- |
| NFR-8 (local foundation, reproducible checks) | A smoke run that always stops what it started, with bounded waits, reaped statuses and stated failure limits | Phase 0, `scripts/lib/owned_process.py`, `scripts/mock_smoke.py` |
| P0-T4 r1 finding 4, P0-T5 r1 and r2 finding 1 | Replaces PID signalling from outside the owner with sole-waiter ownership | Phase 0, same |

## Validation
Planned, not yet run (see the plan's acceptance commands): unit tests with a fake adapter that logs every wait and signal (retained identity before reap, same-parent same-second replacement, reuse of an old PID by a new spawn, repeated stop, forged and foreign handles, exit between poll and signal, no signal after REAPED, ECHILD to LOST, no unrelated wait); real-child cases (clean TERM, nonzero exit, early exit, TERM ignored then KILL, status collection); smoke and integration tests with INT, TERM and HUP before startup, during readiness and during cleanup; repeated and concurrent runs; mutations classified only after they parse. Nothing in this ADR is a measured result.

## Owner acceptance
- Evidence: `agents/plans/P0-T5.md`, section "Owner approval and directed merge": the owner approved the concrete revision in session with the words "I approve, commit, push and merge with main", covering the foreground-runner migration instead of per-child IPC supervisors, the crash and unreapable-child limitations, and the added ADR and test files. The same section states that this is not technical approval of the implementation and not a Codex verdict.
- Accepted by owner: partly. Accepted: option A, the stated limits, the added files. Proposed: the state machine and spawn details above. Open: the workdir-removal safeguard, the Go toolchain and CVE, and the Go checker.
