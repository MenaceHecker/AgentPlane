# Agent handoff conventions

This directory is the message bus between the agents. They do not talk to each other directly: the repo carries every handoff and the owner triggers each step. `AGENTS.md` is the source of truth; this file is a short operating summary. If they disagree, `AGENTS.md` wins.

## Roles

| Role | Agent | Writes | Never |
| --- | --- | --- | --- |
| Planner | Codex | `agents/plans/` | Writes product code |
| Executor + test runner | Claude | product code, `agents/runs/`, `docs/PROGRESS.md` | Plans its own task, edits `agents/plans/` or `agents/reviews/` |
| Reviewer | Codex | `agents/reviews/` | Edits product code |
| Owner | Tushar | merges, exit gates, cloud approvals | |

Claude never marks a task APPROVED, and neither agent declares a phase done. Only the owner runs an exit gate.

## Files and names

```
agents/templates/   plan.md, run.md, review.md
agents/plans/       P<phase>-T<n>.md       (Codex)
agents/runs/        P<phase>-T<n>.md       (Claude)
agents/reviews/     P<phase>-T<n>-r<k>.md  (Codex)
```

Branches are `phase-<N>/<short-slug>`, one per task, created when the plan is committed. Only one agent works on a branch at a time; the owner hands it off explicitly. Never commit to `main`.

## Lifecycle

```
PLANNED -> IN_PROGRESS -> READY_FOR_REVIEW -> CHANGES_REQUESTED -> READY_FOR_REVIEW -> APPROVED -> MERGED
```

1. **Plan (Codex):** commits the plan from `templates/plan.md`; owner sets PLANNED.
2. **Execute (Claude):** works only from the approved plan, one small commit at a time, explicit `git add <file>`, reading `git diff --cached` first.
3. **Test (Claude):** runs every acceptance check and commits `agents/runs/<id>.md` with raw command output, trimmed only for length.
4. **Review (Codex):** compares plan, run report and diff; commits `agents/reviews/<id>-r<k>.md` with a verdict.
5. **Fix loop:** each finding is its own new commit (never an amend); Claude reruns affected checks and appends results to the run report.
6. **Merge (owner):** confirms CI is green and merges.

## Evidence rules

- Paste the command and its raw output. A failing check is reported as FAIL, never summarized away.
- No number appears in any doc unless it came from a recorded run; otherwise write `TBD (not yet measured)`.
- Tests are never deleted, skipped or weakened to get a pass.
- No cloud command without explicit owner approval in the current session.

## Blocked work

If a plan is wrong or incomplete, Claude records it under "Blocked" in the run report and stops. The owner takes it back to Codex for a revised plan. Claude does not change scope, the commit list or acceptance checks.

## Example handoff

1. Owner to Codex: "Plan P0-T1." Codex commits `agents/plans/P0-T1.md` on `phase-0/repo-bootstrap`.
2. Owner to Claude: "Execute P0-T1." Claude restates the commit list, implements it, writes `agents/runs/P0-T1.md`, pushes, stops.
3. Owner to Codex: "Review P0-T1." Codex writes `agents/reviews/P0-T1-r1.md`.
4. If CHANGES_REQUESTED, owner to Claude: fix, rerun, append; repeat from step 3 with `-r2`.
5. On APPROVE the owner merges.
