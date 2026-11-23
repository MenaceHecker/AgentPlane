# Audit records and chain verification contract

This is the Phase 0 contract for audit records, per-tenant hash chains and signed checkpoints. It applies ADR-004, ADR-005 and ADR-006. Machine-readable parts: [`record.schema.json`](record.schema.json) and [`checkpoint.schema.json`](checkpoint.schema.json). Fixtures: [`examples/`](examples/).

**Status.** Format and fixtures only. There is no audit database, library, verifier CLI, signer, key management, checkpoint publication or checkpoint store, and nothing here has been tampered with or measured in a running system. The Go checks that exercise the fixtures are test-only reference models of the rules below (see "How the fixtures are checked").

## What is decided and what is only specified
| Item | Status |
| --- | --- |
| One hash chain per tenant | Owner-accepted (ADR-006) |
| Signed checkpoint with head hash, count and timestamp, tenant-bound, kept in a separate store, detecting tail truncation against a trusted checkpoint; format-only in Phase 0; the uncheckpointed-tail limitation | Owner-accepted (ADR-006) |
| One logical audit trail per request (admitted, completed, correction, rejected) and its streaming failure boundary | Owner-approved interpretation of FR-AUD-1 (ADR-005 6.4) |
| RFC 8785 canonicalization, SHA-256, Ed25519, genesis construction, locking, verification procedure, stable event keys and idempotent append, the `_platform` chain | Specified in ADR-005/006 as **Proposed**; written down here as specified, **not owner-approved** |
| Exact field set, millisecond timestamps, `charged_tokens`, status enums, base64 signature encoding, test key derivation, rule ordering and reason names in this document | **Specification choices made while writing this contract. Not owner-approved** |

## Chain model
- Each tenant has its own chain, keyed by `chain_id` (the tenant name). Records of different tenants never link. The reserved id `_platform` holds requests with no attributable tenant (for example a 401); that is Proposed and unconfirmed.
- A **chain entry** is one event. A **request** has one logical audit trail made of entries that share `request_id`:
  - `admitted`: appended before anything is forwarded (the audit gate). At most one per request.
  - `completed`: appended after terminal settlement. At most one per request. For `usage_source: hard_bound` (consumption unknown) `tokens_in` and `tokens_out` are `null` and `charged_tokens` is the hard bound charged.
  - `correction`: zero or more, append-only, when a late usage correction applies after a terminal state (ADR-004). `delta_tokens` is at least 1: corrections only add.
  - `rejected`: a single-event trail for a request rejected before admission.
- Chain entries are not requests. A request's audited usage is `charged_tokens` of its `completed` entry plus the `delta_tokens` of its distinct `correction` entries. The number of requests is the number of distinct `request_id` values. The checkpoint `count` counts entries.
- No prompt or response bodies are stored (FR-AUD-4): only `request_hash`, `request_size`, `response_hash`, `response_size`. The schema forbids extra properties, so a body field cannot be added. A hash of a short or guessable prompt can be confirmed by guessing; hashes are not secrecy.

## Fields
All entries have `version` (1), `chain_id`, `index` (contiguous from 0), `prev_hash`, `record_hash`, `event`, `event_key`, `request_id`, `timestamp`, `tenant`, `agent` (both `null` only on the `_platform` chain). `timestamp` is UTC with exactly millisecond precision and is fixed when the event is created, never regenerated on retry. The per-event fields are in `record.schema.json`:

| Event | Additional fields |
| --- | --- |
| `admitted` | `kind`, `target`, `request_hash`, `request_size`, `reserved_tokens`, `window` |
| `completed` | `kind`, `target`, `status`, `tokens_in`, `tokens_out`, `charged_tokens`, `latency_ms`, `usage_source`, `overshoot`, `response_hash`, `response_size`, `window` |
| `correction` | `reservation_id`, `delta_tokens`, `corrected_total`, `reason`, `window` |
| `rejected` | `kind`, `target`, `status` (the error code), `request_hash`, `request_size`, `latency_ms` |
| any | optional `audit_mode: "fail_open"` (demo-only setting) |

`window` is the UTC date of budget admission; settlement always uses the admission window (ADR-005 section 1).

## Identity rules
A request is **attributable** only if both its tenant and its agent were established from the token (ADR-005). The schema and the semantic checks below enforce:
- **Tenant chain** (any `chain_id` other than `_platform`): `tenant` and `agent` are both non-null strings, and `tenant` equals `chain_id`. Every entry of the chain has `chain_id` equal to the chain it is in.
- **`_platform` chain** (Proposed, unconfirmed): holds only `rejected` entries for requests that were not attributable (for example a 401, or a token that maps to no agent). There `tenant` and `agent` are both `null`. A `_platform` entry with a tenant or agent, or with any other event, is invalid.
- All entries sharing a `request_id` agree on `tenant` and `agent`.
JSON Schema encodes the null/non-null part (an `if`/`then` on `chain_id`). That `tenant` equals `chain_id`, and that entries of one request agree, compare fields and are semantic rules, applied by the test-only layer described under "Semantic validation" below. The audit database role never decides identity: it comes from the validated token mapping (ADR-005).

## Event keys and idempotent append (ADR-005 6.2)
- `event_key` is `<request_id>:admitted`, `<request_id>:completed`, `<request_id>:rejected` or `<request_id>:correction:<seq>`, where `seq` starts at 1 and is assigned atomically with the budget correction. `request_id` uses only `A-Z a-z 0-9 _ -` (no colon), so the key can be derived and split unambiguously. The schema enforces the per-event shape of the key; that its prefix equals the record's own `request_id` is a semantic rule.
- Correction sequences of one request must start at 1 with no gaps, in index order (a repeat of an existing key is a duplicate, handled by the duplicate rule below).
- An append is "append or return existing": under the per-chain lock the writer selects an entry with the same `(chain_id, event_key)`. Identical content (every field except `index`, `prev_hash`, `record_hash`) returns the existing entry; different content is rejected as `conflicting_event`; otherwise the entry is inserted. A `UNIQUE(chain_id, event_key)` constraint backs this up. INSERT and SELECT privileges suffice.
- Totals are computed over distinct event keys, so a retried correction never counts twice.

## Canonical bytes and hashes
- **Canonical form:** RFC 8785 (JSON Canonicalization Scheme). The data uses only ASCII keys, strings, booleans, `null` and integers (no floating point), for which the canonical form is: object keys sorted, no insignificant whitespace, minimal string escaping.
- **Numeric domain (lossless integers only).** Every number in a record or checkpoint is a non-negative integer written as plain decimal digits (no fraction, exponent, sign or leading zero) in the range **0 to 9007199254740991** (2^53 - 1). RFC 8785 prints numbers as IEEE-754 doubles, so any integer above 2^53 - 1 is rounded: for example `9007199254740992` and `9007199254740993` both canonicalize as `9007199254740992`, making two distinct values share the same canonical bytes and hash. The contract therefore **rejects out-of-range numbers before canonicalization** instead of rounding them, in the JSON Schemas (`maximum`), in the Python reference (`ValueError`) and in the Go checks (`unsafe_integer` for a chain entry, `malformed` for a checkpoint). `1.0`, `1e3`, `-0` and `01` are refused as non-canonical number text, although a generic JSON Schema validator treats `1.0` as an integer, so the canonicalization guard, not the schema alone, enforces plain decimal text. A quantity that could legitimately exceed 2^53 - 1 would have to be encoded as a canonical decimal string by a future schema version; nothing in version 1 needs that (token counts, sizes, latencies, indexes and counts are far below the limit), and no runtime budget is changed by this rule. A gateway that would have to charge or report more than 9007199254740991 tokens must fail closed rather than write the record. Boundary vectors: [`examples/boundary-vectors.json`](examples/boundary-vectors.json).
- **Record hash:** `record_hash = SHA-256( "agentplane.audit.record.v1\n" || JCS(record without record_hash) )`, written as 64 lowercase hex characters. `index` and `prev_hash` are part of what is hashed.
- **Genesis:** the first entry has `index = 0` and `prev_hash = SHA-256("agentplane.audit.genesis.v1:" || chain_id)`. Binding it to the chain id prevents splicing a chain under another tenant. For `index = n > 0`, `prev_hash` is the `record_hash` of `index n - 1`.
- The index, not the timestamp, orders entries. Timestamps may be non-monotonic across replicas.
- Vectors: [`examples/vectors.json`](examples/vectors.json) holds the canonical bytes and hash of every entry of the valid chain, the genesis hash and the checkpoint signed-bytes digest and signature.

## Chain verification and the first broken index (FR-AUD-3)
For expected index `e = 0, 1, 2, ...` up to the highest index present, in this order:
1. an entry with index `e` exists, otherwise `missing_index` at `e` (a gap: later indexes exist);
2. exactly one such entry, otherwise `duplicate_index` at `e`;
3. its `prev_hash` equals the previous entry's `record_hash` (or the genesis hash for `e = 0`), otherwise `prev_hash_mismatch` at `e`;
4. its recomputed `record_hash` equals the stored one, otherwise `record_hash_mismatch` at `e`.

The first `e` that fails is the **first broken index**. Then one semantic rule over the whole chain: each `event_key` appears at most once. A repeat with identical content is `duplicate_event` and a repeat with different content is `conflicting_event`, reported at the index of the second occurrence. Duplicates link correctly, so hashing cannot see them; this rule can.

### Semantic validation (test-only layer, after the hash checks)
A chain whose links and hashes are consistent can still be malformed: a record can be consistently re-hashed with a forged identity or event key, so hashing proves nothing about those fields. After the linkage checks, and before any usage totals are accepted, the semantic layer checks in index order and reports the first violation:
`record_chain_mismatch` (entry `chain_id` differs from the chain), `platform_event_violation`, `platform_identity_violation`, `identity_null_in_tenant_chain`, `tenant_chain_mismatch`, `request_identity_inconsistent`, `event_key_mismatch` (key is not `request_id:event`, or a correction key without a valid sequence), `correction_seq_gap`. Usage totals are computed only for chains that pass linkage and these rules; duplicates alone do not block totals because totals already count distinct event keys. This layer is test support that proves the contract is checkable; it is not a gateway, an audit service or a runtime verifier. Fixtures: `chain-null-identity-in-tenant-chain.json`, `chain-null-agent-in-tenant-chain.json`, `chain-tenant-mismatch.json`, `chain-record-chain-id-mismatch.json`, `chain-platform-with-tenant.json`, `chain-platform-non-rejected-event.json`, `chain-event-key-other-request.json` (the review counterexample: first key replaced by `different-request:admitted`, chain consistently re-hashed), `chain-event-key-wrong-event.json`, `chain-event-key-correction-seq-zero.json`, `chain-event-key-correction-seq-gap.json` and `chain-request-identity-inconsistent.json`.

| Tampering | Detected by the chain alone | Result (fixture) |
| --- | --- | --- |
| Modify an entry without rehashing | Yes | `record_hash_mismatch` at that index (`chain-modified.json`, index 3) |
| Modify an entry and recompute its hash | Yes | `prev_hash_mismatch` at the next index (`chain-modified-rehashed.json`, index 4) |
| Delete a middle entry | Yes | `missing_index` at the deleted index (`chain-deleted-middle.json`, index 2) |
| Reorder entries (swap index values) | Yes | `prev_hash_mismatch` at the first swapped index (`chain-reordered.json`, index 2) |
| Splice a chain from another tenant | Yes | `prev_hash_mismatch` at index 0 (`chain-wrong-genesis.json`) |
| Replay or duplicate an event | By the event-key rule, not by linkage | `duplicate_event` or `conflicting_event` at index 8 (`chain-duplicate-correction.json`, `chain-conflicting-event.json`) |
| **Delete the last entries (tail truncation)** | **No** | `chain-tail-truncated.json` verifies; only a checkpoint with a higher count detects it |
| **Rewrite a suffix and recompute every later hash** | **No** | `chain-suffix-rewritten.json` verifies; only a checkpoint covering a rewritten entry detects it |

## Signed checkpoints (format only)
A checkpoint attests that, at a time, a tenant chain had `count` entries with a given head. It is stored separately from the audit database. Schema: `checkpoint.schema.json`.
- **Signed payload** (exactly these fields): `version` (1), `chain_id`, `count`, `head_hash` (the `record_hash` of index `count - 1`, or the genesis hash when `count` is 0), `timestamp` (UTC, seconds), `alg` and `key_id`. **The signature metadata is inside the signed payload.**
- **Envelope:** `{"payload": {...}, "sig": "<base64>"}`, nothing else. `sig` is the 64-byte Ed25519 signature in padded standard base64.
- **Canonical signed bytes:** `"agentplane.audit.checkpoint.v1\n" || JCS(payload)`.
- **Trusted key registry (out of band, never read from the audited database):** maps `key_id` to `(alg, public key, permitted chain ids)`. Fixture: [`examples/key-registry.json`](examples/key-registry.json) (public keys only). The registry, not the checkpoint, decides which algorithm a key uses and which chains it may attest.
- **Allowed metadata changes: none.** Changing any payload field, including `alg` or `key_id`, changes the signed bytes.

**Verification rules, in order** (each failure names the rule, nothing is skipped):
1. `malformed`: the envelope is exactly `payload` and `sig`; the payload has exactly the required fields with well-formed values; `version` is 1; `sig` is base64 of 64 bytes.
2. `alg_not_allowed`: `alg` is `Ed25519` (not `none`, not empty, not anything else).
3. `unknown_key`: `key_id` is in the registry; `alg_mismatch`: the registered algorithm equals `alg`; `key_not_permitted`: the key may attest this `chain_id`. (`key_id` is read before the signature check only to select a candidate trusted key; the signature then authenticates it, because a substituted `key_id` selects a different key and verification fails.)
4. `bad_signature`: the signature verifies over the canonical signed bytes with the registry key.
5. `chain_mismatch`: the payload `chain_id` equals the chain being verified (tenant binding).
6. The chain verification above, with its own failure reasons.
7. `tail_truncation`: the chain has at least `count` entries.
8. `head_hash_mismatch`: the entry at index `count - 1` has `record_hash` equal to `head_hash` (the genesis hash when `count` is 0).
9. With several checkpoints, `count` must not decrease as `timestamp` increases (`checkpoint_regression`).

### What a checkpoint does not protect
- **Uncheckpointed tail.** Entries at index `count` and above are not protected against truncation by that checkpoint. Fixture: against the stale checkpoint (`count` 4), `chain-tail-truncated.json` still verifies, and only the latest checkpoint (`count` 8) reports `tail_truncation`. The exposure is the checkpoint interval (cadence not specified here).
- A checkpoint does not prove completeness of logging, only that the first `count` entries are intact and the head matches.
- Key compromise, or control of both the checkpoint store and the registry keys, defeats the anchor.
- A database owner can recompute a chain; INSERT/SELECT grants and the chain make tampering by the gateway's own credentials detectable or prevented, not impossible for a privileged operator.
- Retention and pruning are not specified: chains are append-only with no deletion in Phases 0 to 3.

## Fixtures
Everything in `examples/` except `manifest.json` is generated by `scripts/contracts/audit_reference.py` from the specification above, using only the Python standard library (including a from-scratch RFC 8032 Ed25519). `manifest.json` is hand-maintained: it lists, per case, the expected outcome (ok, reason, first broken index) and the expected schema validity of each record and checkpoint fixture.
- **Test keys are synthetic.** Each seed is derived from a label (`SHA-256("agentplane contract fixture key: " + label)`), so no private key is stored in the repository. Never use these keys for anything real. The registry holds only public keys.
- Valid: `chain-valid.json` (8 entries, 4 requests, covering all four event types, a hard-bound charge and a correction), `chain-platform-valid.json`, `checkpoint-valid.json`, `checkpoint-stale-valid.json`.
- Tampered chains, checkpoint attacks (altered count/head/timestamp; wrong tenant; algorithm `none` or changed; swapped, unknown or forged `key_id`; registry algorithm mismatch; malformed envelopes and signatures) and `records/` schema negatives (for example a prompt body field, a zero or negative correction delta, uppercase hashes, hard-bound entries with token counts) are each expected to fail in a specific way.

## How the fixtures are checked
`make contracts-check` runs (`scripts/check-contracts.sh`):
1. `go test ./internal/contractcheck/...`: validates every record and checkpoint fixture against its JSON Schema (negatives must be rejected), recomputes every canonical byte string, hash, genesis hash and the checkpoint signature with `gowebpki/jcs` and `crypto/ed25519`, re-derives the test keys and re-signs the checkpoint (Ed25519 is deterministic, so the signature must match the Python-produced one), and applies the verification rules above to each case in the manifest;
2. `python3 scripts/contracts/audit_reference.py --check`: regenerates the fixtures in memory and requires the committed files to match exactly.
Agreement between the two independent implementations is evidence the published bytes are reproducible. It is not evidence about a running system.
