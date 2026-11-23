# ADR-003: Workload isolation baseline: hardened pods, Kata as a timeboxed experiment

Status: Accepted (baseline and timebox); hardening details Proposed
Requirements: FR-POL-1, FR-POL-3, FR-CTL-1, requirements section 3.3 (isolation model), NFR-9
Plan: [P0-T2](../../agents/plans/P0-T2.md)

## Context
Tenant agents run arbitrary tenant-supplied images. The isolation model (requirements section 3.3) combines a namespace per tenant, ResourceQuota/LimitRange, default-deny NetworkPolicy, Pod Security Standards "restricted", and a dedicated tenant node pool. Kata Containers / pod sandboxing would add a VM boundary but depends on VM size support and stability on AKS, and the requirements say to timebox it to 2 days. Hard rule 5 lists tenant isolation as never cut and, if behind schedule, cuts Kata second (after canary analysis polish, before extra experiments).

## Options considered
| Option | Summary | Main trade-offs |
| --- | --- | --- |
| A. Hardened pods (baseline) | Standard runc containers with restricted PSS, seccomp, dropped capabilities, read-only root filesystem, quotas, NetworkPolicy, node-pool separation. | Works on kind and AKS today. Shares the host kernel: a kernel or runtime escape is not mitigated. |
| B. Kata / pod sandboxing | Each pod in a lightweight VM. | Stronger boundary; depends on node SKU/nested virtualization, adds overhead, may not work on kind. Feasibility unknown. |

## Decision
**Accepted (owner):**
- The baseline is **hardened pods** (option A). All tenant isolation claims in docs and the README must rest on this baseline.
- **Kata is a 2-day timeboxed experiment in Phase 3 (task 5).** Its result, positive or negative, is recorded in a new ADR. This ADR does not decide Kata feasibility and Phase 0 does not test it.

**Proposed (specification detail, not yet owner-reviewed):**
- Baseline controls for tenant pods: `runAsNonRoot`, `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false`, all capabilities dropped, `seccompProfile: RuntimeDefault`, CPU/memory requests and limits, no `hostPath`, no `hostNetwork`/`hostPID`, images only from the approved registry and not `latest`.
- Controls are enforced by (a) Pod Security Admission `restricted` on tenant namespaces, (b) Gatekeeper constraints (FR-POL-1, FR-POL-3), and (c) the Agent controller's generated pod spec. Admission is the authority; the controller's defaults are not a security boundary.
- **No isolation claims from labels.** Node labels, taints and tenant namespace names are scheduling or organizational hints. They are not evidence of isolation. A claim such as "tenant X is isolated from tenant Y" is only allowed when backed by an enforced control (admission policy, NetworkPolicy, quota) and a test that exercises it (Phase 3 noisy-neighbor and policy corpus, Phase 2 egress test).

## Consequences
- Hardened pods reduce, but do not eliminate, container-escape risk. Kernel and runtime vulnerabilities remain a residual risk; this must appear in the threat model (Phase 3 task 6) and README limitations.
- The dedicated tenant node pool and taints limit noisy-neighbor and co-location with system pods but are not a security boundary against a compromised node.
- If the Kata experiment fails or runs out of time, the platform ships with the baseline unchanged; the ADR records why.
- Phase 0 contains no running workloads and no isolation measurements. TBD (not yet measured).

## Requirement mapping
| Requirement | How this decision addresses it | Enforced at |
| --- | --- | --- |
| FR-POL-1 | Gatekeeper rejects privileged/root/no-limits/latest/hostPath/no-seccomp pods | Phase 3 |
| FR-POL-3 | Registry allowlist | Phase 3 |
| Section 3.3 | Baseline controls plus experiment boundary | Phases 1-3 |
| FR-CTL-1 | Tenant controller creates quota, LimitRange, default-deny NetworkPolicy | Phase 1 |

## Validation
Phase 3 exit gate (owner runs): 15 of 15 bad manifests rejected and good manifests admitted; noisy-neighbor baseline captured; threat model committed; Kata outcome recorded in an ADR either way.

## Owner acceptance
- Evidence: P0-T2 plan, "Accepted owner decisions": "Hardened pods as the baseline; Kata remains a 2-day timeboxed experiment in Phase 3."
- Accepted by owner: yes for the baseline and the timebox. The specific control list and the "no isolation claims from labels" wording follow the plan's commit description and requirements section 3.3; the exact control list is Proposed.
