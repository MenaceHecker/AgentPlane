# ADR-006: Audit hash chain and signed checkpoint verification model

Status: Accepted (one chain per tenant; signed checkpoint concept, contents, separate store, tenant binding, format-only Phase 0, uncheckpointed-tail limitation); encoding, hash, signature algorithm and procedures Proposed
Requirements: FR-AUD-1 to FR-AUD-5, FR-GW-10, NFR-9
Plan: [P0-T2](../../agents/plans/P0-T2.md); consumed by P0-T3 (`docs/audit/`)

## Context
Audit records must be append-only and hash-chained, and a verification command must detect any modification, deletion or reordering and report the first broken index (FR-AUD-2/3). The gateway writes with an INSERT/SELECT-only database role (FR-AUD-5, ADR-002). A hash chain alone cannot detect deletion of the *last* records, and an attacker who can rewrite the database can recompute every hash. This ADR defines the chain, the verification procedure and a signed checkpoint anchored outside the audit database, and states what they do and do not protect. Phase 0 defines formats and fixtures only; there is no signer, key management, checkpoint publication, storage or runtime verifier yet.

## Options considered
| Topic | Options | Outcome |
| --- | --- | --- |
| Chain scope | One global chain; one chain per tenant | Per tenant (owner-accepted): tenant-scoped verification and no cross-tenant reads. Costs more chains and checkpoints |
| Tail-truncation defense | None (chain only); signed checkpoints in a separate store; external append-only log service | Signed checkpoints in a separate store (owner-accepted). An external log service is not chosen |
| Canonical encoding | RFC 8785 JSON canonicalization; custom length-prefixed binary | JCS (Proposed), reviewed in P0-T3 |
| Signature | Ed25519; ECDSA P-256; HMAC | Ed25519 (Proposed). HMAC is rejected because the verifier would hold the signing secret |
| Append concurrency | Per-chain advisory lock with unique index backstop; mutable head row; single writer | Advisory lock plus unique index (Proposed); a mutable head row needs UPDATE, which ADR-002 forbids |

## Decision

### 1. One chain per tenant (ACCEPTED by owner)
Each tenant has its own independent chain, keyed by tenant name. Records of different tenants never link to each other, so one tenant's verification never reads another tenant's data and a fault in one chain does not invalidate the rest. The tenant is taken from the gateway's validated identity mapping (ADR-005).
**Proposed:** a reserved chain id `_platform` (not a valid tenant name) holds records for requests with no attributable tenant (for example 401s), so that "every request produces one record" (FR-AUD-1) can hold. Unconfirmed; see ADR-005 open question 1.

### 2. Record and chain fields (Proposed; exact schema in P0-T3)
Each record has `chain_id` (tenant), a contiguous `index` starting at 0, `prev_hash`, `record_hash`, and the FR-AUD-1 content: timestamp (UTC), tenant, agent, kind (`model`|`tool`), target name, `tokens_in`, `tokens_out`, status, latency, `request_hash`, `request_size`, `response_hash`, `response_size`, plus the ADR-005 fields (`request_id`, `reserved_tokens`, budget `window`, `usage_source`, `overshoot`). Prompt and response bodies are **not stored by default**, only hashes and sizes (FR-AUD-4). The request hash is SHA-256 over the raw request bytes as received; the response hash is SHA-256 over the bytes sent to the client. Limitation: a hash of a short or guessable prompt can be confirmed by guessing; hashes are not secrecy.

### 3. Canonical bytes and hashing (Proposed)
- Hash: SHA-256. Hex digests are lowercase, 64 characters.
- Canonical form: the record as a JSON object canonicalized with RFC 8785 (JSON Canonicalization Scheme), **excluding** `record_hash`, with `prev_hash` included. Fields use integers (no floating point), strings, and booleans only; timestamps are RFC 3339 strings in UTC (`Z`).
- `record_hash = SHA-256( "agentplane.audit.record.v1\n" || canonical_bytes )`. The prefix is a domain separator so record hashes can never be confused with checkpoint or other hashes.
- Whether JCS is the final choice (versus a custom length-prefixed binary form) is a specification decision reviewed in P0-T3; the properties required are determinism across languages and no ambiguity in field boundaries. Test vectors are produced by an independent reference calculation in P0-T3. No digests are written in this ADR.

### 4. Genesis and linkage (Proposed)
- The first record has `index = 0` and `prev_hash = SHA-256("agentplane.audit.genesis.v1:" || chain_id)`. Binding the genesis to the chain id prevents splicing a valid chain from one tenant under another.
- For `index = n > 0`, `prev_hash` equals the `record_hash` of index `n - 1`.
- **Ordering authority is the `index`, not the timestamp.** Timestamps may be non-monotonic across gateway replicas (clock skew); the verifier does not require monotonic timestamps.

### 5. Concurrency and restricted writes (Proposed)
- Several gateway replicas may write the same tenant's chain. Each append runs in one transaction holding a per-chain lock (for example a PostgreSQL advisory transaction lock on the chain id, which needs no table privileges), reads the current head (`SELECT`), computes the next `index` and `prev_hash`, and `INSERT`s. `UNIQUE(chain_id, index)` is the backstop so a race fails instead of forking the chain.
- The writer role has INSERT and SELECT only (ADR-002). No mutable head row is needed.
- Cost: appends to one tenant's chain are serialized. Per-tenant append throughput and its effect on NFR-2 are TBD (not yet measured); batching is an option left to Phase 2.
- If the append fails, the request is rejected (FR-GW-10) while usage is still reconciled (ADR-004/005).

### 6. Verification procedure and "first broken index" (Proposed; FR-AUD-3)
The verifier (Phase 2 `agentplane audit verify`) reads one chain with the SELECT-only verifier role, and for expected index `e = 0, 1, 2, ...` checks, in order: (a) a record with index `e` exists (if later indexes exist but `e` does not, that is a gap), (b) there is exactly one such record, (c) `prev_hash` equals the previous record's `record_hash` (or the genesis hash for `e = 0`), (d) the recomputed `record_hash` equals the stored one. The first `e` failing any check is the **first broken index**, reported with the reason.

| Tampering | Detected by the chain alone? | How / what is reported |
| --- | --- | --- |
| Modify a record's content | Yes | Stored `record_hash` no longer matches recomputation: broken at that index. If the attacker also recomputes that record's hash, the next record's `prev_hash` no longer matches: broken at the next index |
| Delete a record in the middle | Yes | The expected index is missing, or the following record's `prev_hash` mismatches: broken at the deleted index |
| Reorder records (swap `index` values or contents) | Yes | `prev_hash` / `record_hash` mismatch at the first swapped index |
| Insert or duplicate a record | Yes | Duplicate index violates the unique key; a forged insert fails linkage |
| **Delete the last k records (tail truncation)** | **No** | The remaining prefix is a valid shorter chain. Detectable only against a trusted checkpoint (section 7) |
| **Rewrite a suffix and recompute every later hash** | **No** | A consistent forged chain verifies. Detectable only if a trusted checkpoint covers an affected record (head hash mismatch) |
| Gateway never logs a request (omission) | No | Chain integrity is not completeness. Metrics and rejection counters are a separate cross-check, not specified here |

### 7. Signed checkpoints (format ACCEPTED in outline; details Proposed)
**Accepted by owner:** a checkpoint contains the chain's **head hash, record count and timestamp**, is **signed**, is **bound to its tenant**, and is **persisted to a separate store** from the audit database. It detects tail truncation relative to a trusted checkpoint. Phase 0 defines the format, documentation and fixtures only.

**Proposed format.**
- **Signed payload (all fields required, no others allowed):** `version` (`1`), `chain_id` (tenant), `count` (number of records, so the head index is `count - 1`), `head_hash` (the `record_hash` of index `count - 1`, or the genesis hash when `count = 0`), `timestamp` (RFC 3339 UTC, seconds precision), and the signature metadata `alg` (`Ed25519` initially; pure Ed25519, RFC 8032, is Proposed) and `key_id` (identifier of the signing key).
- **Envelope:** `{"payload": {...}, "sig": "<base64 signature>"}`. The envelope carries nothing but the payload and the signature. **All signature metadata (`alg`, `key_id`) is inside the payload and therefore inside the signed bytes.** There is no unsigned metadata that a verifier may trust.
- **Canonical signed bytes:** `"agentplane.audit.checkpoint.v1\n" || JCS(payload)` (same canonicalization rules as section 3), where `payload` includes `alg` and `key_id`. The signature covers exactly those bytes. The `chain_id` inside the payload binds a checkpoint to its tenant: a checkpoint for tenant A cannot validate against tenant B's chain.
- **Trusted key registry (out of band).** The verifier holds a registry mapping `key_id` to `(alg, public key, permitted chain ids)`. The registry, not the checkpoint, is the authority on which algorithm a key uses and which chains it may attest. Registry content and distribution are not specified in Phase 0 (TBD).
- **Allowed metadata changes: none.** Altering any payload field, including `alg` or `key_id`, changes the signed bytes and invalidates the signature. Key rotation means new checkpoints name a new `key_id`; old checkpoints remain verifiable while their key stays in the registry. Removing a key from the registry makes its checkpoints untrusted (retention policy TBD).
- **Separate store.** Checkpoints and the public verification keys live outside the audit database and are writable by a different identity than the gateway's audit writer (the store technology is not chosen; TBD). The verifier must obtain trusted public keys out of band, never from the audited database.

**Verification rules (Proposed).** Given a chain and a checkpoint, the verifier: (1) parses the envelope and requires exactly `payload` and `sig`, with exactly the required payload fields (unknown or missing fields are rejected) and a supported `version`; (2) requires `payload.alg` to be an allowed algorithm (not `none`, not empty); (3) looks up `payload.key_id` in the trusted registry and rejects an unknown key, a key whose registered algorithm differs from `payload.alg` (algorithm confusion or downgrade), or a key not permitted for this `chain_id`. The `key_id` is read before the signature is checked only to *select* a candidate trusted key; it is authenticated by the next step, because a substituted `key_id` selects a different key (or none) and verification then fails; (4) verifies `sig` over the canonical signed bytes with the registry's public key; (5) requires `payload.chain_id` equals the chain being verified; (6) runs the chain verification of section 6 over the records, then (7) requires `chain_length >= count`, else reports **tail truncation** (records the checkpoint attests are missing); (8) requires the record at index `count - 1` to exist with `record_hash == head_hash` (genesis check for `count = 0`), else reports history rewritten or truncated and regrown; (9) with several checkpoints, requires non-decreasing `count` and mutually consistent head hashes. Any failure is reported with the specific rule that failed. An invalid or untrusted checkpoint is a verification failure, never ignored.

### 8. Limitations that must be documented (README and threat model)
- **Uncheckpointed tail.** Records appended after the latest trusted checkpoint (indexes `>= count`) are **not protected against tail truncation** by that checkpoint. An attacker can delete them and the chain and checkpoint both still verify. The exposure window is the checkpoint interval (cadence TBD, not specified here).
- A checkpoint does not prove completeness of logging, only that the first `count` records are intact and that head matches.
- Key compromise, or an attacker who controls the separate checkpoint store and keys, defeats the anchor.
- A database owner can recompute the chain; the chain plus INSERT/SELECT grants make tampering by the gateway's own credentials detectable or prevented, not impossible for a privileged operator.
- Retention and pruning are not specified: chains are append-only with no deletion in Phases 0 to 3. A retention design would need checkpointed truncation and is an open question.

## Consequences
- Tamper detection is only as strong as the externally held checkpoints and the integrity of the trusted keys. Without checkpoints the system detects partial tampering but not tail truncation or suffix rewrite.
- Per-tenant chains keep verification local and tenant-scoped but serialize writes per tenant and multiply the number of checkpoints to manage.
- Nothing is implemented in Phase 0: no database, no signer, no key management, no checkpoint publication or storage, no verifier. No tamper-detection result exists yet.

## Requirement mapping
| Requirement | How this decision addresses it | Enforced at |
| --- | --- | --- |
| FR-AUD-1 | Record fields; `_platform` chain proposal for unattributable requests | Phase 2 task 7 |
| FR-AUD-2 | Per-tenant append-only chain, `prev_hash` linkage | Phase 2 task 7 |
| FR-AUD-3 | Verification procedure and first-broken-index rule (section 6); checkpoint rules for tail truncation | Phase 2 task 8 (CLI); format fixtures in P0-T3 |
| FR-AUD-4 | Hashes and sizes only, no bodies by default | Phase 2 |
| FR-AUD-5 | INSERT/SELECT-only writer, SELECT-only verifier (ADR-002) | Phase 2 |
| FR-GW-10 | Failed append rejects the request | Phase 2 |

## Validation
P0-T3 provides canonical byte and hash test vectors from an independent reference calculation, negative fixtures (modified, deleted, reordered, truncated), and checkpoint fixtures: valid; altered `count`, `head_hash` or `timestamp`; wrong tenant (`chain_id`); altered `alg` (including `none`, empty and a different supported-looking value); altered `key_id` selecting another trusted key; unknown `key_id`; a valid signature by a key that is not in the trusted registry; registry algorithm different from `payload.alg`; key not permitted for the chain; missing, empty or malformed `sig`; an extra field in the envelope or payload; stale (older, lower `count`); plus the uncheckpointed-tail example. Phase 2 implements the verifier and the E5 tamper experiment (edit a row, `audit verify` names the first broken index). No results exist yet.

## Owner acceptance
- Evidence: P0-T2 plan, "Accepted owner decisions": "One audit hash chain per tenant" and "Signed checkpoints containing head hash, count, and timestamp, persisted to a separate store, detect tail truncation against a trusted checkpoint", including tenant binding, canonical signed bytes, signature metadata, verification rules, format-only Phase 0 and the uncheckpointed-tail limitation.
- Accepted by owner: yes for sections 1, 7 (contents, separate store, tenant binding, purpose) and 8 (uncheckpointed-tail limitation). The concrete encoding (JCS), hash (SHA-256), signature algorithm (Ed25519), genesis construction, locking scheme, `_platform` chain and verification procedure details are Proposed specification, not owner-accepted.
