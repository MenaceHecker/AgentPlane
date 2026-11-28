# Run report P0-final-acceptance
Branch: `phase-0/final-acceptance`   Base: `main` at `2d31fbf894bedcb14a86e2d9ddce7faaa3d137ca` (PR #11 merge)   **TESTED_SHA: `f6dd6826b814d70d70b40555026bcfc5c2c44be5`**

Plans: [P0-go-patch](../plans/P0-go-patch.md), [P0-final-acceptance](../plans/P0-final-acceptance.md). Section A (fixture containment, quickstart evidence, historical reconciliation) was merged in PR #11 and was not reimplemented. Nothing here is merged, and **Phase 0 is not declared complete**: only the owner may do that, no exit checkbox was changed, and Phase 1 is not started.

## Owner authorization and dispositions (verbatim, from the owner's runner prompt for this round; no timestamp was recorded)
1. "Apply the narrowly scoped Go 1.27.4 patch plan, including the verified Docker builder index digest and current prerequisite docs. Keep dependencies, the runtime base digest and the existing Go topology checker unchanged. This is patch remediation, not acceptance of the old advisory risk."
2. "Download and verify Go 1.27.4, gitleaks 8.30.1 and Trivy 0.76.2 using the plans' official-source checksums into retained isolated tool directories. No global installation replacement, sudo, unverified installer or other tool upgrade."
3. "Run the planned bounded clean-clone checks and local image build/scan. Run all three local secrets targets; do not substitute a missing-tool PASS. Fix the deferred timeout-suffix prose issue without changing runner behavior."
4. "I explicitly authorize the final plan's exact live scope: create one NEW `agentplane-p0-exit`, verify readiness/topology/context/cleanup/idempotence, load the local mock image into it, and run `make kind-down KIND_CLUSTER_NAME=agentplane-p0-exit TMPDIR=<recorded-root>/live-tmp` twice. This permission includes teardown of that run's partial failed creation. Stop if the name or a conflicting kubeconfig context already exists; never adopt/recreate/delete a foreign or default cluster. No registry push. I also authorize removal of only the one stopped binary-inspection container created under the Go patch plan, using its recorded exact ID."
5. "I accept the historical long-repeat evidence for the narrowly reviewed fixes and explicitly defer the omitted long kind/mock batches. Keep them recorded as NOT COMPLETED at the newer head. Do not run automatic stress loops."
6. "I explicitly defer optional mutation work with no score/pass claim. Historical mutation-r4 stays pinned to df8a840 and unexecuted; r2/r3, the earlier rejected command including W10 and the two unsafe real-child mutants remain unapproved."
7. "Keep the existing Bash-helper deletion deferral. I am not confirming my Git email's verified-account status through this prompt; ask me for that fact before final owner exit sign-off, without publishing account details."

## Commits
1. `8f6b52d build: pin Phase 0 Go builds to 1.27.4` — `go.mod` (`go 1.27.3` to `go 1.27.4`), `mockmodel/Dockerfile` (builder `golang:1.27.4-bookworm@sha256:ce59b41415e2524ae99926fe1f37ce48ba8c194dcbd2a56a65436cdcefa2bd34`, the OCI index digest from `docker buildx imagetools inspect golang:1.27.4-bookworm`, saved as `golang-1.27.4-index.txt`; amd64 and arm64 entries present; runtime digest, USER, hardening flags, `GOTOOLCHAIN=local` and `-mod=readonly` unchanged). `go.sum` unchanged.
2. `f6dd682 docs: align local Go prerequisites and timeout guidance` — `docs/local-development.md` only. Go prerequisite and base-pin docs updated with provenance; the 1.27.3 tested-host row kept as historical; the deferred finding (P0-wrap-up-r1 minor 1) resolved: absence of the timeout suffix does not prove collection, LOST is terminal but not REAPED, read the final ownership diagnostics; TERM/KILL described as escalation only when needed. Runner behavior unchanged.
3. This report and the raw evidence (docs commit).
4. `docs/PROGRESS.md` handoff (separate commit).
`scripts/test-image-scan.sh` still contains the string `v1.27.3` inside scanner test fixtures; those are synthetic test data, not pins, and were left unchanged (out of the plan's file scope).

## Tool provenance (retained root; archives and binaries are not committed)
All downloaded and hash-checked before extraction; no sudo, no installer script, no package-manager change, no global replacement. Hashes and fetch time: `P0-final-acceptance/tools/` (fetched 2026-11-28T00:08:20Z).
| Tool | Source | Verification | Result |
| --- | --- | --- | --- |
| Go 1.27.4 darwin/arm64 | https://go.dev/dl/go1.27.4.darwin-arm64.tar.gz | SHA-256 `91a44a2b8a716600ad94a4f143e156116620db8828086a6e3602291be9bd0bb4` taken from the official `https://go.dev/dl/?mode=json&include=all` index and matched with `shasum -a 256 -c` | `go version go1.27.4 darwin/arm64`; session used `GOTOOLCHAIN=local`, `go env GOVERSION` = go1.27.4 |
| gitleaks 8.30.1 darwin_arm64 | https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_darwin_arm64.tar.gz | SHA-256 `b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5` from the release's `gitleaks_8.30.1_checksums.txt`, matched | `gitleaks version` = 8.30.1 |
| Trivy 0.76.2 macOS-ARM64 | https://github.com/aquasecurity/trivy/releases/download/v0.76.2/trivy_0.76.2_macOS-ARM64.tar.gz | SHA-256 `ae7ea358326960406b703c77b2ac490190797d80b306b5b50c2a27221da0a5b8` from the release's `trivy_0.76.2_checksums.txt`, matched | `Version: 0.76.2` |
No sigstore/cosign signature verification was performed; the claim is checksum verification against the official release files only. The tool directories are on PATH only inside the check session.
Pre-existing tools used unchanged: golangci-lint 2.15.0 (built with go1.27.2; it ran cleanly against Go 1.27.4), kind v0.31.0, kubectl client v1.36.0, Docker 29.9.3 (Docker Desktop 4.100.0), GNU Make 3.81, Git 2.50.1, Python 3.14.7. Host: Darwin 25.6.0 arm64. Raw: `versions.txt`. **Cache use:** normal Go build and module caches and Docker layer cache were used; this is not a cold or offline build.

## Pinned clean source
Fresh retained root `<EVIDENCE_ROOT>` (`agentplane-p0-final.*` under the host temp dir, redacted in the committed logs), `git clone --no-local /Users/tm2508/Dev/AgentPlane <root>/repo`, `git checkout --detach f6dd6826b814d70d70b40555026bcfc5c2c44be5`; `git status --porcelain` empty before and after every command (recorded per command in `summary.txt`). Nothing untracked was copied into the clone.

## Non-live acceptance checks (each run once on the pinned clone; raw logs in `P0-final-acceptance/`)
| Command | Exit | Result |
| --- | --- | --- |
| `go mod verify` | 0 | all modules verified |
| `git diff --exit-code -- go.sum` | 0 | go.sum unchanged, no dependency change |
| `make fmt-check` | 0 | OK |
| `make lint` | 0 | `0 issues.` |
| `make test` | 0 | 6 Go packages `ok` |
| `make contracts-check` | 0 | go mod verify OK, contract tests ok, Python reference `88 files, 0 problems` |
| `make supervisor-test` | 0 | 58, 23 and 15 tests, each OK |
| `make smoke-cleanup-test` | 0 | wrapped counts 58, 23, 26, 6 plus static guards PASS |
| `make kind-config-test` | 0 | **stub-only** suite; 48 PASS lines incl. test_kind_cluster.py 27 tests |
| `make make-args-test` | 0 | 679 checks passed |
| `make image-scan-test` | 0 | scanner self-test (fixtures), not a scan of the real image |
| `make secrets` | 0 | `scanned 362 tracked files, exit 0` |
| `make secrets-history` | 0 | `go.sum history: scanned 78 distinct historical lines, exit 0` |
| `make secrets-selftest` | 0 | `secrets self-test: all passed` |
| `make mock-smoke` (1) | 0 | JSON, SSE, 400/500 checks pass; `build=REAPED(0), main=REAPED(0), failing=REAPED(0)`; `mock-smoke: OK` |
| `make mock-smoke` (2) | 0 | same |
Contract/ADR presence (`contracts-present.log`): `test -s docs/gateway/openapi.yaml`, `docs/audit/record.schema.json`, `docs/audit/hash-chain.md` all 0; ADR-001..007, README, template listed. This shows files exist; it makes no future runtime-compliance claim. The three local `make secrets*` targets ran with the checksum-verified pinned gitleaks, so the earlier BLOCKED entries are superseded for this pinned source.

## Image build, scan and binary provenance
- `make mock-image` exit 0. Build log shows `golang:1.27.4-bookworm@sha256:ce59b414...bd34` and the unchanged runtime digest `sha256:8ce6dd87...a279b5`. Local image `agentplane/mockmodel:p0`: ID `sha256:7b7e94278ff29f9f360c6159465a631337d5553e26edf669e552adbee10f741c`, config digest `sha256:68ed67e5ccba2619c6e8840c266db3a2d5e63c6a14434d05f7c311bb60fc5abd`, linux/arm64. Nothing was pushed.
- `make mock-image-scan SCAN_EVIDENCE_DIR=<root>/scan-evidence` exit 0, Trivy 0.76.2, database v2 updated 2026-11-27T19:05:47Z, downloaded 2026-11-28T00:15:33Z. Both targets analyzed (debian 13.8 os-pkgs, `mockmodel` gobinary). **All severities: fixable 0, unfixed 0** (CRITICAL, HIGH, MEDIUM, LOW, UNKNOWN); `trivy-report.json` parsed: 0 vulnerabilities in both results and the string `CVE-2026-97054` is absent. GATE PASSED, nothing suppressed. The gate result alone is not the evidence; the full report was inspected.
- Binary inspection (authorized): `docker create agentplane/mockmodel:p0 --help` created container `47d381c1a42c215910bb6b6492c9e96b4561ed05d42153287b6def58230826f4`; `docker cp <id>:/mockmodel <root>/mockmodel-binary` exit 0; `go version -m` reports **`go1.27.4`** (`-trimpath=true`, `CGO_ENABLED=0`, linux/arm64); binary SHA-256 `11f6f9c988fe7da288da991ad26eb44b763c4dd150310b403fd9c3853e5e7bf8`. The copied binary was not executed. Only that container was removed (`docker rm <id>` printed the same ID; a filtered `docker ps -a` for it then returned 0 rows). The binary copy is retained in the root, not committed.
- Platform: the local image and scan are linux/arm64 on this Apple-silicon host. The project's CI image job (linux/amd64) is separate; no amd64 local result is claimed.

## Live acceptance: `agentplane-p0-exit` (raw: `live-*.log`, `live-summary.txt`, `live-comparisons.txt`)
Every command was run individually with its own exit status; no chained script. All kubectl calls used an explicit `--context`. Context names of the owner's other kubeconfig entries are redacted in the committed copies (`<PRIOR_CONTEXT_REDACTED>`, `<FOREIGN_CONTEXT_REDACTED>`); comparisons were computed on the unredacted values (`live-comparisons.txt`).
1. Prerequisites: `docker version`, `kind version` (v0.31.0), `kubectl version --client` (v1.36.0), `trivy --version` (0.76.2): all exit 0.
2. Initial reads, all exit 0: `kind get clusters` = "No kind clusters found."; current context read succeeded and was **non-empty and a pre-existing non-kind context (the prior current context)**; `kubectl config get-contexts -o name` listed 2 contexts, neither is `kind-agentplane-p0-exit`. Stop conditions (name present or conflicting context) did not apply. One pre-existing stale context entry with no cluster behind it was left untouched.
3. `<root>/live-tmp` created new; `find ... -mindepth 1 -maxdepth 1 -print` returned 0 entries.
4. `make kind-up KIND_CLUSTER_NAME=agentplane-p0-exit TMPDIR=<root>/live-tmp`: **exit 0**, 22 s; output ends `kind cluster 'agentplane-p0-exit' created` and `kind-cluster: your kubectl current context is '<prior>' (verified)`. Independent after-reads: context equals the initial read (YES), live-tmp 0 entries, `kind get clusters` = `agentplane-p0-exit`.
5. `kubectl --context kind-agentplane-p0-exit wait --for=condition=Ready nodes --all --timeout=180s` exit 0 (3 nodes condition met); `get nodes -L agentplane.io/pool`: control-plane (no pool), worker `system`, worker2 `tenant`, all Ready, kubelet v1.35.0; tenant node JSON: label `agentplane.io/pool=tenant` and taint `agentplane.io/pool=tenant:NoSchedule`; node UIDs saved (`live-node-uids-1.log`). This is placement simulation, **not** a network-isolation claim.
6. Second `make kind-up` (same command): **exit 0**, 3 s, output `already exists and matches the supported topology; nothing to do`. After: context equals initial (YES), live-tmp 0 entries, node UIDs byte-identical to the first read (YES), `kind get clusters` still only the one cluster (no duplicate, no recreation).
7. `kind load docker-image agentplane/mockmodel:p0 --name agentplane-p0-exit`: **exit 0**, loaded into all three nodes (image ID `sha256:7b7e9427...741c`); context unchanged after. An image load does not show in-cluster serving; none is claimed.
8. `make kind-down KIND_CLUSTER_NAME=agentplane-p0-exit TMPDIR=<root>/live-tmp` (first): **exit 0**, deleted the three nodes of this cluster. Second call: **exit 0**, `does not exist; nothing to do`. After each: context equals initial (YES / YES), live-tmp 0 entries. Final `kind get clusters` = "No kind clusters found."; the kubeconfig context list is identical before and after the whole lifecycle (2 and 2, identical); no `agentplane-p0-exit*` container remains (`docker ps -a --filter` returned 0).
No partial create or failed teardown occurred. No registry push, no foreign or default cluster touched, no cloud or provider call.

## Observations
1. **Host TMPDIR after the fix:** `repeat-owned-process.*` directories under the host temp dir stayed at 22 across this round's `make supervisor-test`/`smoke-cleanup-test`; the only two recent ones are dated 18:14, before the containment fix merged. No new leak was observed. Historical directories were not touched.
2. **Redactions:** committed logs replace the owner's home path, the evidence/tool root paths and two kubeconfig context names (private cluster names of another project) with placeholders. `go version -m`, scan JSON and test logs are otherwise raw. The prior-context read being a production-style context is why restoration was verified independently, not only by the script's own message.
3. `CLAUDE_CODE_PROMPT.md` is tracked on this branch but shows as deleted in the working tree (it appeared after my first status check; I did not delete it). It is not staged or committed by me; the requirements DOCX is untracked and untouched.
4. `git diff --check` on the new text edits is clean; raw logs are committed as captured, so trailing whitespace in raw output (as in the earlier accepted `supervisor-test.log` exception) is not edited.

## Deferred, not run, and still open (none closed by this report)
- Long kind/mock repeat batches: **NOT COMPLETED at this head**; the owner accepted the historical stress evidence for the narrowly reviewed fixes and deferred the omitted batches (item 5). No stress loop was run.
- Mutation: deferred by the owner (item 6); nothing executed; no score or pass claim. mutation-r4 remains pinned to df8a840 and unexecuted; r2/r3, the earlier rejected command including W10 and the two unsafe real-child mutants remain unapproved.
- Unused Bash helper: deletion remains deferred (item 7).
- **Git account email:** the owner has not confirmed verification; the owner must be asked for this fact before final exit sign-off (no account details published here).
- Go advisory: patched to 1.27.4; whether `MaxBytesReader` race was exploitable in the mock was never demonstrated and was not claimed. The topology checker was kept unchanged; no decision on its long-term inclusion is made here.
- Final-head PR CI (all five jobs) and Codex review: pending at the time this report was written; recorded after the push.
- Owner exit-gate sign-off and the three Phase 0 checkboxes: not touched.

## Failures and fixes
No command failed in this round. One narrow edit during commit 2: a speculative sentence about `GOTOOLCHAIN` behavior was removed before committing because it was not verified here.

## Blocked
None for the authorized scope.
