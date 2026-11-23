# ADR-002: Audit store: PostgreSQL over Cosmos DB

Status: Accepted (choice of PostgreSQL); role and deployment details Proposed
Requirements: FR-AUD-1 to FR-AUD-5, FR-GW-10, NFR-8, NFR-9
Plan: [P0-T2](../../agents/plans/P0-T2.md)

## Context
Every gateway request must produce one audit record that is append-only and tamper-evident (FR-AUD-1/2/3), the database role used by the gateway must not be able to modify or delete records (FR-AUD-5), and the gateway fails closed when the audit store is unavailable (FR-GW-10). The requirements list "PostgreSQL (in-cluster)". The store must run on kind (Phases 0-3, no cloud) and later on AKS.

## Options considered
| Option | Summary | Main trade-offs |
| --- | --- | --- |
| A. PostgreSQL, in-cluster | Relational store, runs identically on kind and AKS. Fine-grained `GRANT`s (INSERT/SELECT only) and transactions. | We operate it ourselves (backups, upgrades); in-cluster state needs a persistent volume. |
| B. Azure Cosmos DB | Managed, globally distributed document store. | Cloud-only (no kind parity, conflicts with "no Azure until Phase 4"), cost, weaker fit for per-column privilege grants and relational integrity. |

## Decision
**Accepted (owner):** use PostgreSQL for the audit store. It runs in-cluster; kind locally in Phases 0-3, and the same manifests on AKS later. No managed or cloud database is used.

**Proposed (specification detail, not yet owner-reviewed):**
- Three database roles: a *writer* role used by the gateway (`INSERT`, `SELECT` on the audit table only; no `UPDATE`, `DELETE`, `TRUNCATE`), a *verifier* role (`SELECT` only) for `agentplane audit verify`, and a *migration/owner* role used only to create the schema and never by the gateway. `PUBLIC` privileges on the audit table are revoked.
- The writer must not need `UPDATE`-like privileges to function: record indexes are assigned by the application inside a per-tenant critical section (see ADR-006), not by a mutable counter row.
- Optionally, a database trigger that rejects `UPDATE`/`DELETE`/`TRUNCATE` as defence in depth. This is not a substitute for the grants or for the hash chain.
- Table layout, column types and migrations are specified in P0-T3 (audit record schema).
- Credentials come from a Kubernetes Secret created out of band for the local environment; nothing is committed (hard rule 2).

## Consequences
- `INSERT`/`SELECT` grants stop the gateway (or an attacker who compromises it) from rewriting history through its own credentials. They do **not** stop a database superuser, the owner role, or someone with storage-level access. That residual risk is why records are hash-chained and why signed checkpoints are kept in a separate store (ADR-006).
- Fail-closed (FR-GW-10): an unreachable or non-writable audit database causes gateway requests to be rejected. The demo-only fail-open switch must be off by default and visible in configuration.
- Availability and backup of the audit database are the platform's responsibility; no backup design is decided here. TBD (not yet designed).
- Nothing is deployed or measured in Phase 0.

## Requirement mapping
| Requirement | How this decision addresses it | Enforced at |
| --- | --- | --- |
| FR-AUD-2 | Append-only table plus chain (ADR-006) | Phase 2 |
| FR-AUD-5 | Writer role has INSERT and SELECT only | Phase 2 (DB setup), verified by a test that attempts UPDATE/DELETE with the writer role |
| FR-GW-10 | Store write failure rejects the request | Phase 2 |
| NFR-9 | No static secrets in the repo for DB credentials | Phase 2 |

## Validation
Phase 2: an integration test connects as the writer role and confirms `UPDATE`, `DELETE` and `TRUNCATE` are denied; a tamper experiment (E5) edits a row as the owner role and `audit verify` must name the first broken index. Phase 0 provides no results.

## Owner acceptance
- Evidence: P0-T2 plan, "Accepted owner decisions": "PostgreSQL (Postgres) for the audit store". The plan's commit description also records "local deployment, restricted DB role".
- Accepted by owner: yes for PostgreSQL, in-cluster/local deployment and a restricted writer role. The three-role split, trigger and credential handling above are Proposed specification detail, not owner-accepted.
