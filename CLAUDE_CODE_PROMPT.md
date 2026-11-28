# Claude Code prompt: finish Phase 0 acceptance

Owner note: the prompt below proposes approvals for the Go patch, isolated tool
installation, and creation/deletion of **only `agentplane-p0-exit`**, plus explicit
stress/mutation deferrals. Review these choices before sending it to Claude.
Saving this file does not itself approve or execute anything. Git email verification
and the final Phase 0 exit decision are deliberately not asserted here.

---

Mode: execute the remaining Phase 0 wrap-up tasks.
Branch: `phase-0/final-acceptance`.

Read `AGENTS.md`, `CLAUDE.md`, `docs/PROGRESS.md`,
`agents/plans/P0-go-patch.md`, `agents/plans/P0-final-acceptance.md`,
`agents/reviews/P0-wrap-up-r1.md`, and the existing wrap-up run report.
The completed section A work was merged in PR #11. Do not reimplement it.

I approve the two new plans with these explicit choices:

1. Apply the narrowly scoped Go 1.27.4 patch plan, including the verified Docker
   builder index digest and current prerequisite docs. Keep dependencies, the
   runtime base digest and the existing Go topology checker unchanged. This is
   patch remediation, not acceptance of the old advisory risk.
2. Download and verify Go 1.27.4, gitleaks 8.30.1 and Trivy 0.76.2 using the plans'
   official-source checksums into retained isolated tool directories. No global
   installation replacement, sudo, unverified installer or other tool upgrade.
3. Run the planned bounded clean-clone checks and local image build/scan. Run all
   three local secrets targets; do not substitute a missing-tool PASS. Fix the
   deferred timeout-suffix prose issue without changing runner behavior.
4. I explicitly authorize the final plan's exact live scope: create one NEW
   `agentplane-p0-exit`, verify readiness/topology/context/cleanup/idempotence,
   load the local mock image into it, and run
   `make kind-down KIND_CLUSTER_NAME=agentplane-p0-exit TMPDIR=<recorded-root>/live-tmp`
   twice. This permission includes teardown of that run's partial failed creation.
   Stop if the name or a conflicting kubeconfig context already exists; never
   adopt/recreate/delete a foreign or default cluster. No registry push. I also
   authorize removal of only the one stopped binary-inspection container created
   under the Go patch plan, using its recorded exact ID.
5. I accept the historical long-repeat evidence for the narrowly reviewed fixes
   and explicitly defer the omitted long kind/mock batches. Keep them recorded as
   NOT COMPLETED at the newer head. Do not run automatic stress loops.
6. I explicitly defer optional mutation work with no score/pass claim. Historical
   mutation-r4 stays pinned to df8a840 and unexecuted; r2/r3, the earlier rejected
   command including W10 and the two unsafe real-child mutants remain unapproved.
7. Keep the existing Bash-helper deletion deferral. I am not confirming my Git
   email's verified-account status through this prompt; ask me for that fact
   before final owner exit sign-off, without publishing account details.

Confirm branch/status, record this authorization and restate the four planned
commits. Leave the user-managed requirements DOCX and this root prompt untouched.
Execute only the listed source/doc/run/progress changes, one logical commit at a
time, with the specified checks. No product behavior changes, test weakening,
dependency churn, CI redesign, new checker, cloud/Azure or real model provider.

Use one clean pinned source clone and retained evidence roots. Record individual
commands, exit codes, tool/archive/digest provenance, scan findings and real
before/after inventory/context/node-UID/empty-live-tmp evidence. Never dump
credentials, full kubeconfig or the environment. Do not sweep historical TMPDIR
entries or remove retained roots. Preserve failures, report blockers, and return
for a precise Codex plan revision if product changes or extra tool upgrades are
needed. Do not repeatedly ask for permissions explicitly granted above.

Commit the source/docs, run report and separate progress update; push and open a
draft PR for final-head CI. Return the diff, raw evidence, PR/CI links and any
remaining blockers for Codex review. Do not merge or declare Phase 0 complete.
After a later owner-directed merge, verify source equivalence and exact-main CI
as specified by the plan. Only my explicit final exit-gate confirmation permits
checking the Phase 0 boxes; Phase 1 implementation is not authorized.
