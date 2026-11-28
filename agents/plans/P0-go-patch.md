# Plan: Phase 0 Go security patch
Status: DRAFT — proposed owner approval is in the root Claude prompt; not accepted by writing it
Branch: phase-0/final-acceptance
Requirements: NFR-9 (toolchain/image hygiene), NFR-8 (reproducible local tools)

## Goal
Move the current Go 1.27.3 pins to Go 1.27.4, the fixed patch in the existing minor line identified by [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859). The official advisory lists the affected range as before 1.26.11 and 1.27 prereleases through before 1.27.4; independently checked during this planning turn. This is preventive dependency remediation, not a claim that the mock's handler was shown exploitable. Keep application behavior, dependencies and the existing topology checker unchanged.

## Out of scope
Major/minor Go upgrades, dependency updates, go.sum churn, checker redesign/removal, application/security API changes, CI tool/action upgrades, new scanners, unverified risk waiver, mutation or long stress batches. Do not edit historical run reports or turn historical tested-version records into new measurements.

## Commit list
1. `build: pin Phase 0 Go builds to 1.27.4` — go.mod and mockmodel/Dockerfile only. Change the go directive from 1.27.3 to 1.27.4; change the builder tag/comment and its actual multi-architecture OCI index digest to golang:1.27.4-bookworm. Resolve the digest via `docker buildx imagetools inspect golang:1.27.4-bookworm`, save the full output/provenance and verify amd64/arm64 entries. Never retain the old digest with the new tag or invent a digest. Preserve the runtime base digest, USER, hardened flags, GOTOOLCHAIN=local and read-only module build. CI already reads go.mod; no workflow change is required or allowed. go.sum must remain unchanged; stop if a dependency update appears necessary.
2. `docs: align local Go prerequisites and timeout guidance` — docs/local-development.md only. Update current prerequisites and builder pin documentation to 1.27.4 with observed provenance; retain old measured host values as historical until a new recorded run supports a current tested-version row. Resolve P0-wrap-up-r1 finding 1: timeout-suffix absence is not proof of collection; LOST is terminal but not REAPED, so read final ownership diagnostics. Describe TERM/KILL as escalation when needed, not an unconditional promise of both signals. Preserve all scan/context/placement/filesystem and supervision limits.
3. Results are committed in the single final-acceptance report and separate progress commit specified in P0-final-acceptance.md, with the exact source/tool/image SHAs. No duplicate historical report edits.

## Acceptance checks (the test runner executes these)
After explicit approval, use an isolated retained tools directory, not a global Go installation replacement. Fetch https://go.dev/dl/?mode=json, locate the exact go1.27.4 archive for the detected host, download from go.dev/dl and compare its SHA-256 to that official index before extracting. Do not silently substitute another version or architecture. Put this SDK's bin first in PATH for the entire check/build session; set GOTOOLCHAIN=local. Record `go version` and `go env GOVERSION GOTOOLCHAIN`, compiler archive checksum and source URL. Record cache use. Stop if the pinned release is unavailable or its hash/digest cannot be established.

- [ ] `git diff --check` on new edits; historical raw-log whitespace exceptions are reported separately.
- [ ] `go mod verify` and `git diff --exit-code -- go.sum` — verified dependencies, no dependency change.
- [ ] `make fmt-check`, `make lint`, `make test`, `make contracts-check`, `make supervisor-test`, `make smoke-cleanup-test`, `make kind-config-test`, `make make-args-test`, `make image-scan-test` — once each, individual exit 0 and raw logs. Run the two real mock-smoke invocations in the final plan, not a duplicate batch here. Missing tool or compatibility failures return to Codex; no assertion weakening or unsolicited lint/CI upgrade.
- [ ] `make secrets`, `make secrets-history`, `make secrets-selftest` — once each with checksum-verified pinned gitleaks; secret logs redacted.
- [ ] `make mock-image`; `make mock-image-scan SCAN_EVIDENCE_DIR=<new-owned-root>/scan-evidence` — record local image ID, index/builder/runtime pins, binary Go build version and full scan findings. Reuse this exact build/scan for final live acceptance if the source tree is identical; no registry push.
- [ ] `docker image inspect agentplane/mockmodel:p0 --format '{{.Id}}'`; `docker create agentplane/mockmodel:p0 --help`, then `docker cp <created-container-id>:/mockmodel <root>/mockmodel-binary`; `go version -m <root>/mockmodel-binary` — binary reports go1.27.4. Record the created container ID and remove only that stopped inspection container with `docker rm <created-container-id>` (included in the proposed approval); no arbitrary container cleanup. Do not execute the copied binary for metadata. Keep the copy/evidence root.
- [ ] Scan evidence no longer attributes CVE-2026-97054 to Go 1.27.3 in the newly built binary. If the scanner still reports it, preserve the report and investigate source/version mismatch; do not suppress it. Gate success alone is not proof of absence: review all findings and any unrelated new issues.
- [ ] Final-head PR CI succeeds for the new source. After owner-directed merge, final-main acceptance must cover the same relevant tree and verify exact-main CI; see the final plan.

## Security-sensitive
Yes: compiler download integrity, OCI digest pinning, dependency preservation, binary provenance and image/secret scan evidence. A patch-version bump needs actual build/test/scan results. No signature-verification claim unless performed. No secrets, kubeconfigs or tool archives committed.

## Risks and open questions
The advisory's described MaxBytesReader/MaxBytesHandler race was not demonstrated in this mock; absence of direct calls is not a full reachability audit. Owner has not yet chosen this proposed patch over explicit scoped risk acceptance. Missing local Trivy/gitleaks and compatibility of the existing lint binary may block checks. A failed compatibility check requires a small plan revision rather than scope invention. Planning measured no patched-build result.
