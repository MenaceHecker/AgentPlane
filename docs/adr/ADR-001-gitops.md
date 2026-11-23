# ADR-001: GitOps controller: Flux over ArgoCD

Status: Accepted
Requirements: NFR-5 (git push to serving agent under 3 min, target not yet measured), NFR-8, FR-DEL-1 to FR-DEL-4
Plan: [P0-T2](../../agents/plans/P0-T2.md)

## Context
Platform components and tenant Agent manifests are to be delivered from Git to the cluster without manual `kubectl` (requirements section 3.2, Phase 5 exit gate). The requirements name "Flux (or ArgoCD)". Progressive delivery uses Argo Rollouts, which is a separate controller and does not depend on ArgoCD.

## Options considered
| Option | Summary | Main trade-offs |
| --- | --- | --- |
| A. Flux | Set of Kubernetes controllers (source, kustomize, helm) configured by CRDs; no required UI server. | Fewer moving parts and no separate API/UI server to secure; less visual tooling out of the box. |
| B. ArgoCD | Application-centric controller with an API server and UI. | Strong visualization and app-level UX; larger footprint and an additional authenticated surface to operate. |

## Decision
Use **Flux** for GitOps (platform repo and tenant repo sync). Argo Rollouts remains the progressive-delivery controller, independent of Flux. ArgoCD is not installed.

## Consequences
- Delivery is declared as Flux CRDs (`GitRepository`, `Kustomization`, optionally `HelmRelease`) stored in the repo under `deploy/`.
- No GitOps dashboard comes for free; operational visibility relies on Flux status conditions, Prometheus metrics and Grafana (FR-OBS-2).
- Flux (sync) and Argo Rollouts (canary delivery) have different roles but are easy to confuse with ArgoCD; repo documentation must keep their roles distinct.
- Nothing is installed in Phase 0. Flux bootstrap is Phase 5, on AKS. Git push-to-serving timing (NFR-5) is TBD (not yet measured).

## Requirement mapping
| Requirement | How this decision addresses it | Enforced at |
| --- | --- | --- |
| FR-DEL-1 (push to serving without manual kubectl) | Flux reconciles Git into the cluster | Phase 5 |
| NFR-5 | Target only; measurement happens in Phase 5/6 | Phase 5, E6 |
| NFR-8 | Declarative, repo-resident delivery config | Phase 5 |

## Validation
Phase 5 exit gate (owner runs): pushing an Agent manifest yields a serving agent with no manual kubectl. Timing results go under `experiments/results/`.

## Owner acceptance
- Evidence: P0-T2 plan, "Accepted owner decisions": "Flux over ArgoCD", recorded from the owner's instruction.
- Accepted by owner: yes for the choice of Flux. The comparison and consequences above were drafted by the executor from general characteristics of the tools; the owner's reasons were not recorded, so this rationale is not owner-attested.
