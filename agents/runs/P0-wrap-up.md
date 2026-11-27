# Run report P0-wrap-up
Branch: `phase-0/wrap-up`   Base: `main` at `556fab4ef91d1d31dfaf50a6463d396172205eca` (PR #10 merge)   Product-side commits: `f142d67`, `0d104a1`

Scope: section A of [agents/plans/P0-wrap-up.md](../plans/P0-wrap-up.md), approved by the owner's runner prompt for section A **only**. **Section C (live acceptance) was NOT run.** No product, CI, Go or supervisor/workdir/kind source file was changed; no mutation, cluster, image, cloud or provider operation ran; nothing was merged; Phase 0 is not declared complete and no exit checkbox is checked. Historical run reports and reviews were not edited.

## Commits (in order)
1. `f142d67 test: contain in-process repeat runner fixtures` — `scripts/tests/test_repeat_owned_process.py` only.
2. `0d104a1 docs: clarify local supervision diagnostics and limits` — `docs/local-development.md` only.
3. `docs(agents): record Phase 0 wrap-up evidence` — this report and `agents/runs/P0-wrap-up/`.
4. `docs: reconcile Phase 0 progress and remaining gates` — `docs/PROGRESS.md` only.

## Intake
Branch `phase-0/wrap-up` (HEAD `2a45dfd` = plan plus handoff, two commits ahead of `origin/main` `556fab4`); working tree held only the owner's untracked requirements DOCX and the owner's root prompt file, neither touched or staged. Plan and review read: `agents/plans/P0-wrap-up.md`, `agents/reviews/P0-exit-acceptance-r1.md`, `agents/reviews/P0-T5-r6.md`.

## Commit 1: fixture containment (exit-acceptance r1 minor 1)
**Defect (as diagnosed in r1):** the two in-process `rop.main` tests (lost children, uncollectible child) retained their intentional failure roots in the process-wide temp directory; a child's `TMPDIR` env does not affect an in-process call.

**Change (test file only):**
- `in_process()` now sets `tempfile.tempdir = self.dir` around every in-process `rop.main` call and restores the previous value in a `finally`. `test_lost_children_are_never_reported_as_reaped` was moved onto `in_process()` (it previously duplicated the adapter patching inline); its adapter, arguments and assertions are unchanged.
- `setUp` creates an isolated stand-in for the process-wide temp directory (`self.outer`), points `tempfile.tempdir` at it for the test, and registers restoration. `tearDown` fails the test if any `repeat-owned-process.*` root appears there. It runs before cleanups, so it applies to every test in the class.
- Two retention assertions were added: the lost-children test asserts exactly one retained root exists inside the fixture; the uncollectible test asserts exactly one root exists inside the fixture and holds `run01.log`.
- Cleanup registration order is unchanged (owner close/shutdown still registered before anything spawns). No ownership, escalation (exactly TERM then KILL), no-signal-for-LOST, bounded-time, descriptor-count or retention assertion was removed or loosened; the production runner is unchanged and still retains genuine failure evidence. No historical directory was touched.

**Checks (each with its own empty outer run root under the session scratch directory, retained):**
| Command | Result |
| --- | --- |
| `TMPDIR=<outer-1> python3 -I -B -m unittest discover -s scripts/tests -p test_repeat_owned_process.py -v` | exit 0, `Ran 15 tests ... OK`; outer root empty afterwards (log: `repeat-suite-verbose.log`) |
| `TMPDIR=<outer-2> make supervisor-test` | exit 0; `Ran 58 tests OK`, `Ran 23 tests OK`, `Ran 15 tests OK`; outer root empty afterwards (log: `supervisor-test.log`) |
The test count stays 15: the fix adds assertions, not test methods.

**Regression sensitivity (disposable copy, no checkout rewrite, no mutation driver):** `scripts/` was copied to the scratch directory and, in the copy only, the three containment lines in `in_process()` were removed (the old behavior) while the new assertions were kept. Result (`old-containment-demo.log`): exit 1, `Ran 15 tests`, `FAILED (failures=4)`:
- `test_an_uncollectible_run_fails_in_bounded_time_without_an_outside_guard`: `0 != 1 : the kept working directory must exist, inside the fixture`, and `Lists differ: ['repeat-owned-process.<id>'] != []` (an escaped root).
- `test_lost_children_are_never_reported_as_reaped`: `0 != 1 : the genuine failure evidence must still be retained, inside the fixture`, and the same escaped-root assertion.
In the committed copy of that log only, the session scratch-directory prefix in the traceback file paths was replaced with `<scratch>` (it contained the local user name); nothing else was edited. The same file in the real checkout passes 15 of 15. (This demonstration was run twice with identical failures; the log kept is the second, captured run.) `git status` of the checkout showed only the intended file modified.

## Commit 2: quickstart diagnostics (exit-acceptance r1 minors 2 and 3)
Every changed sentence was checked against `scripts/mock_smoke.py`, `scripts/kind_cluster.py`, `scripts/lib/owned_process.py` and ADR-007:
- Exact messages, quoted separately: smoke `some child is lost, uncollected or unregistered, so the workdir %s was kept for inspection` (`mock_smoke.py:361`); kind `... so the private workdir %s was kept for inspection` (`kind_cluster.py:396`), `<tool>: lost ownership (<reason>); it was not signalled again and may still be running` (`:354`), `<tool>: not collected within the reap deadline; cleanup is incomplete and it may still be running` (`:356`), `<name> ignored SIGTERM and had to be killed` (`:358`; the smoke runner's integration test asserts the same `ignored SIGTERM` text), `<command> did not finish within <N> s and was stopped` with optional suffix `; it could not be collected, so cleanup is incomplete` (`:160-162`).
- Timeout escalation (TERM then KILL, child collected) is now distinguished from LOST/uncollected (collection not confirmed, tool may still run).
- A "Manual inspection" subsection requires positive confirmation of identity/ownership before any manual stop or removal and says never to signal a PID copied from a log.
- Limitations now state, as a non-exhaustive summary pointing at ADR-007: other waiters, reparenting tricks, an inherited SIGCHLD override, and that the `go build` preflight has its own deadline and its compiler processes are untracked. The filesystem rename-race limit is preserved.
- Python 3.8 is labeled source-stated (docstrings `owned_process.py`, `owned_workdir.py`, `check-contracts.sh`) and **not tested**; only the versions in the tested-host table were measured.
- The kind lifecycle statement now says the Python kind owner was verified with stubs only, a live lifecycle with it is pending acceptance, and older live evidence for the Bash script does not cover it.
Preserved unchanged: the scan, context, placement and Make-variable warnings. `git diff --check`: clean for the new edits. The `adr/ADR-007-owned-process-supervision.md` link target exists.

## Predecessor reconciliation (raw: `P0-wrap-up/predecessor-and-ci.txt`)
Facts only; Codex records closure.
| Item | Verified fact |
| --- | --- |
| PR #2 (P0-T0) | `gh pr view 2`: MERGED 2026-11-23T00:03:58Z, merge commit `c458370df92229ebbb800bcaf1bc3e3f8f7330b6`, ancestor of `origin/main`. Merged head `f4fc4c3` (second parent). |
| P0-T0 approval | `c3474d4 docs(agents): approve corrected P0-T0 evidence` exists **only on branch `phase-0/agent-protocol`**; it is NOT an ancestor of `origin/main`. On main, `agents/reviews/P0-T0-r1.md` still reads `Verdict: CHANGES_REQUESTED`; at `c3474d4` it reads `Verdict: APPROVE`. The fix of its single minor (the existence-check record) `f4fc4c3` is on main. `c3474d4` differs from the merged head only in that review file. |
| PR #3 (P0-T1) | `gh pr view 3`: MERGED 2026-11-23T01:04:30Z, merge commit `dcb644c4b3d00e719e8f19998dcb43342022c14a`, ancestor of main. Merged head `7d5d133`. |
| P0-T1 r2 approval | `a967194 docs(agents): approve P0-T1 bootstrap review r2` (`Verdict: APPROVE`) and `058f2d9 docs: record P0-T1 review approval` exist **only on branch `phase-0/repo-bootstrap`**, not on main. Branch tip differs from the merged head `7d5d133` only by `agents/reviews/P0-T1-r2.md` (+59) and one line of `docs/PROGRESS.md`. `agents/reviews/P0-T1-r2.md` is absent from main; only `P0-T1-r1.md` is there. |
| Tracker on main | P0-T0 and P0-T1 rows still say READY_FOR_REVIEW although both PRs are merged. |
So the merge state of both tasks is established by PR and ancestry evidence; the approval files exist as immutable history on unmerged branches but are not on main, and the later changes to P0-T1 scripts, Makefile and CI by subsequent tasks are not shown to be covered by that r2 approval. I did not restore, copy or forge either review file. Whether to link the branch commits, restore the files by a Codex-authored change, or record the gap is for Codex and the owner.

## CI on exact current main
`gh run view 46082844858` (`gh run list --branch main`): `headSha` `556fab4ef91d1d31dfaf50a6463d396172205eca`, completed, success; lint, unit-test, contracts, image and gitleaks all `success`. https://github.com/MenaceHecker/AgentPlane/actions/runs/46082844858. This is the pre-fix main; it says nothing about commits 1 and 2 (final-head CI of this branch is recorded after the push).

## Host prerequisite inventory (read-only version queries; section C not run)
Collected with `go version`, `python3 --version`, `golangci-lint --version`, `kind version`, `kubectl version --client`, `docker version --format '{{.Server.Version}}'`, `which gitleaks trivy`. Disclosure: the `docker version` query contacts the Docker daemon read-only; it is also step 1 of section C, but it was run only as a prerequisite inventory, no cluster, image or context command was run.
| Tool | Found | Needed | Status |
| --- | --- | --- | --- |
| go | go1.27.3 darwin/arm64 | go.mod `go 1.27.3` | matches |
| python3 | 3.14.7 | source-stated 3.8+ | recorded |
| golangci-lint | 2.15.0 | 2.15.0 | matches |
| kind | v0.31.0 | exactly v0.31.0 | matches |
| kubectl | client v1.36.0 | per deploy/kind docs | recorded; compatibility not re-checked here |
| docker | server 29.9.3 | running daemon | reachable |
| **gitleaks** | **not installed** | 8.30.1 | **MISSING** (local secrets targets BLOCKED) |
| **trivy** | **not installed** | exactly 0.76.2 | **MISSING**: section C step 3 (`make mock-image-scan`) would be BLOCKED on this host unless the owner installs it |

## Section B: owner decisions, presented together
**None of these has an owner decision. The "Plan recommendation" column restates the plan; it is a recommendation, not an acceptance. Nothing here is closed.** Evidence below is gathered, not interpreted for the owner.

| # | Decision | Status | Plan recommendation (not a decision) | Evidence now available |
| --- | --- | --- | --- | --- |
| 1 | Live acceptance (section C, name `agentplane-p0-exit`, two named `make kind-down` calls, partial-create teardown) | UNRESOLVED, not authorized | Authorize the exact section C scope | Stub-only so far. Prerequisites: kind, kubectl, Docker present; **Trivy 0.76.2 missing**; gitleaks missing (not needed for live scope). Needs the owner to authorize or to run the commands and supply raw output. |
| 2 | Missing local gitleaks | UNRESOLVED | Accept verified exact-main CI evidence, keep local targets BLOCKED | Exact-main CI run 46082844858 gitleaks job success at `556fab4`; r1 review verified CI covers tree, history, go.sum and scanner self-test. Alternative: owner installs 8.30.1 and I run `make secrets`, `secrets-history`, `secrets-selftest` once each. |
| 3 | Incomplete long kind/mock repeats | UNRESOLVED | Accept historical stress evidence for the narrow reviewed fixes, defer omitted batches | Entries stay NOT COMPLETED in `agents/runs/P0-T5.md`; last full kind/mock repeat passes at `df8a840`. Alternative: owner names exact commands and counts from r6. |
| 4 | Mutation evidence | UNRESOLVED | Defer, no score or pass claim | mutation-r4 pinned to historical `df8a840`, inspection-only, never executed; r2/r3, the earlier rejected command including W10 and two unsafe real-child mutants unapproved. Execution needs a separate current-pin inspection and authorization. |
| 5 | Go toolchain / CVE-2026-97054 and extra Go topology checker | UNRESOLVED | Obtain fresh official evidence and an owner decision; keep the checker if accepted | See "Go advisory evidence" below. |
| 6 | Unused Bash helper | DEFERRED by owner (existing) | Keep deferral | Owner's explicit earlier instruction, quoted below. `scripts/lib/owned-process.sh` and its tests are untouched. |
| 7 | Git account email | UNRESOLVED | Owner confirms the configured email is verified | The local Git email is configured; verification on the GitHub account has not been confirmed by the owner, and I cannot verify it. |

### Go advisory evidence (primary sources, fetched 2026-11-27T23:40:55Z; raw in `P0-wrap-up/go-*.json`, `go-fetched-at.txt`)
Treated as retrieved facts, not as a vulnerability or risk conclusion for this repo.
- Go vulnerability database record `GO-2026-6859` (https://vuln.go.dev/ID/GO-2026-6859.json; page https://pkg.go.dev/vuln/GO-2026-6859): alias `CVE-2026-97054`; published and modified `2026-11-11T02:51:11Z`; `review_status: REVIEWED`; summary "HTTP/1 server crash due to MaxBytesReader header race in net/http"; affected: package `stdlib`, versions before `1.26.11`, and `1.27.0-0` up to before `1.27.4`; fixed in `1.26.11` and `1.27.4`; imported package `net/http`. Details: "For HTTP/1 servers, when MaxBytesReader's limit is exceeded, 'Connection: close' header is automatically set without synchronization, which might race with another header being set explicitly in the server handler. As a result, an HTTP/1 server that utilizes MaxBytesReader or MaxBytesHandler can crash due to runtime panic if its handler reads the request body concurrently with writing the response header." References: fix https://go.dev/cl/862185, report https://go.dev/issue/82563, announcement https://groups.google.com/g/golang-announce/c/iY8TY0wWgbM.
- Go download index (`https://go.dev/dl/?mode=json`, `go-dl-index.json`): stable `go1.27.4` and `go1.26.11` are listed, so the fixed releases exist.
- This repo, factual pins only: `go.mod` `go 1.27.3`; `mockmodel/Dockerfile` builds with `golang:1.27.3-bookworm@sha256:dc0963f3...`; the host Go is go1.27.3. Earlier recorded scanner findings (`agents/runs/P0-T5.md`): one MEDIUM `CVE-2026-97054` in the `mockmodel` binary, fixed in 1.26.11 and 1.27.4, which did not trip the fixable-HIGH/CRITICAL gate.
- Factual code observation for Codex to assess relevance, not a conclusion: `git grep MaxBytesReader MaxBytesHandler -- '*.go'` finds no use in the repository's Go sources; `mockmodel` imports `net/http` (`handler.go`, `stream.go`). Relevance, exploitability and whether to upgrade are for Codex and the owner. No Go version, Dockerfile, go.mod or checker change was made. No installation or toolchain change was performed.

### Owner statements recorded verbatim, with provenance
No timestamps are recorded for these; they are quoted from the owner's messages in this working session.
- Wrap-up runner prompt (this session): "I approve section A of the wrap-up plan only"; "Recommendations are not my decisions. Preserve existing deferrals."; "Do not run section C. No live cluster/image operations, cluster deletion, mutation execution, product/CI/Go changes, cloud/provider commands, merge, or Phase 1 work."; "Retain scratch evidence; never sweep historical TMPDIR directories."
- Earlier P0-T5 r5 round prompt: "I explicitly defer removal of the unused Bash helper. Do not delete it, refactor unrelated code, add features or expand scope." and "Live kind/clean-clone acceptance, mutation evidence, and the Go toolchain/CVE/checker decisions remain owner gates; passing CI does not close them."
No acceptance of any section B recommendation has been given.

## Not run
Section C (live acceptance on clean main) and everything in it; the final-main clean-clone `make fmt-check lint test contracts-check` plus two `make mock-smoke` runs (those belong to section C after the section A fixes merge); local `make secrets*` (gitleaks missing). Section A's own checks above are the only checks run in this round.

## Failures and fixes
No check failed. The only failing run is the intentional old-containment demonstration (4 failures in a disposable copy), recorded above as evidence. Trailing whitespace noted earlier in the raw `supervisor-test.log` of the exit-acceptance evidence is the r1-accepted evidence exception; the new raw logs here are unedited.

## Blocked
- Section C prerequisites on this host: Trivy 0.76.2 missing (required for the image scan step); owner authorization for the live scope not given.
- Local secret targets: gitleaks 8.30.1 missing.
- Owner decisions 1 to 5 and 7 above.
