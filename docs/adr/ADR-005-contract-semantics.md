# ADR-005: Gateway and budget contract semantics

Status: Accepted (fixed daily UTC window and its documented edge case; the audit trail model of section 6.2 to 6.4, approved by the owner); all other sections Proposed
Requirements: FR-GW-1 to FR-GW-10, FR-AUD-1, FR-AUD-4, NFR-3, FR-CTL-5/6 (spec fields)
Plan: [P0-T2](../../agents/plans/P0-T2.md); consumed by P0-T3 (OpenAPI, audit schema)

## Context
Phase 0 locks the contracts before the gateway exists. Several behaviors are not fixed by the requirements and would otherwise force the Phase 2 implementer to guess: when the budget window resets, what a burst looks like, what zero means, how identity is derived, which errors exist, and when usage becomes final. This ADR resolves them. Enforcement is Phase 2 work; nothing here is implemented or measured.

## Options considered
| Topic | Options | Outcome |
| --- | --- | --- |
| Budget window | Fixed UTC day; rolling 24 hours; per-tenant local-time day | Fixed UTC day (owner-accepted). Rolling windows avoid the midnight burst but need per-request history; local-time days make the reset depend on tenant configuration |
| Burst | Bucket capacity equal to requests per minute; a smaller configurable burst | Capacity equal to requests per minute (Proposed); smaller burst left open |
| Zero or absent limits | Zero means unlimited; zero means deny; absent means default | Zero denies, absent is invalid (Proposed), so no accidental unlimited budget |
| Identity source | Validated ServiceAccount token only; request-body or header identity | Token only (required by FR-GW-2/3) |
| Usage finalization | Charge at admission only; reconcile after response | Reconcile after response, tied to ADR-004 |

## Decision

### 1. Budget window: fixed daily UTC (ACCEPTED by owner)
- A budget window is the UTC calendar day `[00:00:00Z, 24:00:00Z)`. Its id is the UTC date `YYYY-MM-DD` of the request's **admission time**. The counters reset at 00:00 UTC by starting a new window; there is no rolling window.
- Admission time is read from a single clock source for the budget decision (the counter store's clock if the store is shared, otherwise the gateway clock; this choice follows ADR-004's open counter-store question). Tests must inject the clock.

**Reset-boundary edge case (documented limitation).** A fixed daily cap is not a rolling 24-hour cap. A caller can consume the entire remaining allowance of day D immediately before 00:00 UTC and the full allowance of day D+1 immediately after, so up to 2 times the daily cap can be consumed within a few seconds around midnight. In any 24-hour span that straddles midnight the maximum is at most 2 times the daily cap. This is accepted behavior, not a defect; README limitations and the threat model (budget bypass) must say so. Per-agent rate limits (requests per minute) are independent of the window and still apply.

**Reservations and streams spanning midnight (specification; enforcement is later work).**
- A reservation records the window id of its admission and settles into that window regardless of when the request completes. A request admitted at 23:59:59Z on day D that completes at 00:00:05Z on day D+1 charges day D only. It neither consumes day D+1's allowance (no double count) nor vanishes (no loss).
- A stream is bounded by its reservation (ADR-004). It is not cut off merely because midnight passed; all of its tokens charge the admission window.
- Consequently the admission invariant (`used + reserved + R <= cap`, ADR-004) is evaluated against day D's counters even for requests finishing after midnight, and at midnight the new window starts at `used = 0`, `reserved = 0` for new admissions. Final `used` can exceed the cap only by the excess defined in ADR-004 (zero when its assumptions hold), not because of the reset. Outstanding reservations from day D remain attached to day D until settled or expired (ADR-004 crash recovery).
- Budget configuration is read at admission. A changed cap applies to later admissions only; in-flight requests are not retroactively cancelled. If a cap is lowered below current `used`, further admissions in that window are rejected until reset.

### 2. Rate limits and burst (Proposed)
- Per-agent token bucket (FR-GW-5): refill `requestsPerMinute / 60` tokens per second; **bucket capacity (burst) equals `requestsPerMinute`**, so an idle agent may send up to one minute's allowance back to back, then is limited to the refill rate. Each request consumes one bucket token before budget admission. A smaller burst is a possible tightening but is not chosen here.
- With N gateway replicas and per-replica buckets the aggregate limit is up to N times the configured rate. This per-replica approximation must be documented in Phase 2 (requirements Phase 2 task 4); a shared bucket is not specified here.
- A Tenant also carries `requestsPerMinute` (requirements section 4.1). FR-GW-5 requires only per-agent buckets; whether the gateway also enforces a tenant-wide bucket is an **open question** (below). Until decided, only per-agent buckets are specified.
- Rate-limited requests reserve no tokens and consume no budget.

### 3. Zero, absent and invalid values (Proposed)
| Value | Meaning |
| --- | --- |
| `tokensPerDay: 0` or `requestsPerMinute: 0` | A real limit of zero: every request is rejected (`budget_exhausted` or `rate_limited`). Zero never means "unlimited". |
| Absent budget fields | Not valid configuration for a running Agent/Tenant (CRD validation, Phase 1). If the gateway nevertheless finds no usable limit, it rejects (fail closed). There is no "unlimited" sentinel. |
| Negative or non-integer values | Invalid; rejected at admission control (Phase 1) and treated as zero (deny) by the gateway if encountered. |
| Request `max_tokens: 0` or negative | `400 invalid_request`. |
| Request without `max_tokens` | The platform default output cap applies (value TBD in OpenAPI, P0-T3). |
| Agent cap greater than tenant cap | Rejected at admission (FR-POL-2/webhook). If it exists anyway, the tenant cap still binds (ADR-004). |

### 4. Identity (Proposed; FR-GW-2/3)
- The only identity source is the projected ServiceAccount token, validated through the Kubernetes TokenReview API for a gateway-specific audience (name TBD in P0-T3). No static API keys, no client-supplied identity headers.
- The authenticated username `system:serviceaccount:<namespace>:<serviceaccount>` is mapped: namespace to tenant (via the tenant label the Tenant controller sets on the namespace) and ServiceAccount to agent (the Agent controller creates a ServiceAccount named after the Agent). The gateway resolves these against the cluster's own objects and then reads the Agent/Tenant spec for limits and tool allowlists.
- **Request-body fields never establish or override identity.** A body containing identity-like fields (for example `tenant`, `agent`) is not used for authorization and is rejected with `400 invalid_request` to avoid ambiguity. The audit record's tenant and agent come from the validated mapping only.
- `401 unauthenticated`: missing, malformed, expired or rejected token, or TokenReview says not authenticated. `403 forbidden`: authenticated but not mapped to an active Tenant/Agent (unknown identity), or tenant/agent disabled. `403 tool_not_allowed`: tool not in the allowlist (FR-GW-6).
- If TokenReview itself is unreachable the gateway fails closed with `503 identity_unavailable`. It never admits on a cached positive past the token's expiry. Review result caching is permitted only up to the token's remaining validity; TTL is TBD.

### 5. Error contract (Proposed)
Errors use an OpenAI-style envelope: `{"error": {"code": "<code>", "message": "<text>", "request_id": "<id>"}}`, plus `scope` (`agent`|`tenant`) and `reset_at` (RFC 3339 UTC, next 00:00Z) for `budget_exhausted`, and a `Retry-After` header for `rate_limited`. Messages never contain tokens, prompt text or other tenants' data.

| HTTP | `code` | Meaning |
| --- | --- | --- |
| 400 | `invalid_request` | Malformed body, bad `max_tokens`, identity-like fields, unknown tool |
| 401 | `unauthenticated` | See section 4 |
| 403 | `forbidden`, `tool_not_allowed` | See section 4 |
| 429 | `budget_exhausted` | Daily token budget (agent or tenant scope) would be exceeded (FR-GW-4) |
| 429 | `rate_limited` | Per-agent token bucket empty. Distinct code from `budget_exhausted` |
| 429 | `concurrency_limited` | Agent already has `maxInFlight` requests in flight (ADR-004). Distinct from `budget_exhausted` and `rate_limited`; includes `Retry-After` |
| 502 / 504 | `upstream_error`, `upstream_timeout` | Model/tool backend failure |
| 503 | `audit_unavailable` | Admission audit append failed before forwarding, or the final append failed for a non-streaming response that was then withheld (FR-GW-10; section 6) |
| 503 | `identity_unavailable` | TokenReview unavailable |
| 500 | `internal_error` | Unexpected |

For streaming, a failure after the response started cannot change the HTTP status. The gateway sends a terminal SSE `data:` event containing the same `error` object, then closes the stream (OpenAI-style `data: [DONE]` follows only on normal completion). A mid-stream budget cut-off uses `budget_exhausted`.

### 6. Usage finalization and audit availability (audit trail model owner-approved, see 6.4; remaining details Proposed)

**6.1 Usage finalization.**
- Usage becomes **final** when the reservation reaches a terminal state (`settled`, `settled_at_hard_bound` or `released`, ADR-004), not when the backend starts replying. Non-streaming: after the full backend response. Streaming: when the stream ends by completion, client cancellation, budget cut-off or error. Unknown consumption is charged at the validated hard bound `H` (ADR-004), never refunded.
- The gateway assigns a unique `request_id` per request. Client retries are new requests and consume budget again; no idempotency key is specified in Phase 0.
- The request hash is the SHA-256 of the raw request body bytes as received, stored with its size; bodies themselves are not stored by default (FR-AUD-4). Details in ADR-006.

**6.2 Audit model: one logical audit trail per request (OWNER-APPROVED, see 6.4).** FR-AUD-1 ("every gateway request shall produce one audit record") is implemented as **one logical audit trail per request**, stored as one or more append-only entries (events) in the tenant's chain that share the request's `request_id`. A request counts once; its trail has these events:
1. `admitted`: request id, agent, target, `request_hash`/size, `reserved_tokens`, budget `window`. The gateway must append it successfully **before forwarding anything to the backend**. If the append fails, the gateway rejects with `503 audit_unavailable`, forwards nothing, and releases the reservation as *proved no work* (ADR-004). A store that is already down is therefore detected before any work or bytes.
2. `completed`: appended after terminal settlement, with final `tokens_in`, `tokens_out`, `status` (including `abandoned` or `unfinalized` when written by recovery), `latency`, `usage_source` (`backend_reported`, `gateway_counted` or `hard_bound`; the last means consumption was unknown and the hard bound `H` was charged) and an `overshoot` flag.
3. `correction` (zero or more): an append-only adjustment tied to `request_id` and `reservation_id`, written when a late usage correction is applied after the reservation reached a terminal state (ADR-004 excess-only rule, including a hard-bound violation). It carries a non-negative `delta_tokens`, a `reason`, and the resulting corrected total. Earlier events are never modified; the trail is truthful because later events add what was learned.
4. `rejected`: a single-event trail for a request rejected before admission (400, 401, 403, 429, ...). Because the request is already being rejected, a failed append here does not change the outcome; it increments a counter metric. This completeness gap is documented, not hidden.

**Totals and counts.** A request's audited usage is the `completed` tokens plus the sum of the `correction` deltas **of distinct event keys** (see Event identity below); a duplicated entry is never summed twice. The number of requests is the number of distinct `request_id` values; the number of physical chain entries is larger. Documentation, metrics and the checkpoint `count` (ADR-006) must say which they mean and must not equate chain length with request count.

**Incomplete trails are detectable.** The verifier and operators can find (a) an `admitted` event with no `completed` event after the lease plus `T_exec_max` plus a margin, and (b) a reservation whose `excess_applied` flag is set but which has no matching `correction` event. When a correction is applied, the obligation to append its event is persisted with the reservation (`correction_pending`) and retried until it succeeds; until then the trail is flagged incomplete. Retrying must be idempotent, as specified next.

**Event identity and idempotent append.** Budget settlement is exactly-once (ADR-004), but the audit append is a separate store, so an append can commit while its acknowledgement is lost, and a naive retry would add the same event at a new chain index (the chain would still verify and summed corrections would double-count). Therefore:
1. **Stable event key.** Every event has an `event_key`, part of the entry's content and hashed with it (ADR-006): `<request_id>:admitted`, `<request_id>:completed`, `<request_id>:rejected`, and `<request_id>:correction:<seq>`. `seq` is a per-request correction counter persisted on the reservation and incremented in the same atomic step that applies the correction to the budget counters, so a retried correction reuses its `seq`. A request has at most one `admitted`, one `completed` and one `rejected` entry.
2. **Content fixed at creation.** The complete event content, including its timestamp, is created once and persisted before the first append attempt (in the reservation record, as the pending event). A retry resends exactly that content; it never regenerates it. The `completed` event is built and persisted atomically with the terminal settlement CAS, so only the settlement winner can produce it. A recovery-written `abandoned` completion and a late completion by the original owner cannot both exist: the loser's CAS fails and its extra usage can only travel through the correction path (ADR-004), never as a second `completed`.
3. **Atomic append-or-return-existing.** Inside the append transaction, under the per-chain lock (ADR-006), the writer first `SELECT`s an existing entry with the same `(chain_id, event_key)`. If one exists with identical content (every field except `index`, `prev_hash` and `record_hash`), the append returns the existing index as success without inserting. If one exists with different content, the append is rejected as `conflicting_event`, nothing is inserted, and an alert is raised (same key with different content signals a bug or an attack). Otherwise it inserts at the next index. `UNIQUE(chain_id, event_key)` is the database backstop, so even a lock failure cannot create a duplicate. This needs only INSERT and SELECT privileges; the writer role is unchanged (ADR-002).
4. **Clearing pending state.** The pending marker is cleared (compare-and-set in the counter store) only after the append returned success, either "inserted" or "already exists, identical". An ambiguous outcome (timeout, lost acknowledgement) keeps the marker and retries the identical event. A crash after the append but before the clear is therefore safe: the retry returns the existing entry and then clears. Clearing is itself idempotent.
5. The counter store and the audit database cannot be updated atomically together. This design gives effectively-once audit effects through stable identity plus idempotent append, not a distributed transaction.

**6.3 Behavior when the audit store fails.**
| Case | Behavior | What is guaranteed |
| --- | --- | --- |
| Store already down before the request | `admitted` append fails: `503 audit_unavailable`, nothing forwarded, reservation released | No work done, no bytes delivered, no usage charged |
| Store goes down after admission, non-streaming response | The gateway holds the (already complete) response until the `completed` append succeeds. If it cannot append within a bounded time (TBD), it discards the response and returns `503 audit_unavailable` | No unaudited response bytes reach the client. Usage is still charged (ADR-004), so the budget stays honest |
| Store goes down after admission, **streaming** response | Bytes already sent cannot be retracted. The gateway attempts the `completed` append at stream end (bounded retries, TBD) and, on failure, sends a terminal error event. A recovery step appends the missing `completed` event (status `unfinalized`) when the store returns | A durable `admitted` event names the request, agent, request hash and reserved tokens. Usage is charged. No new request is admitted while the store is down (the next admission gate fails). **Not guaranteed:** that a `completed` event exists before bytes are delivered, or that bytes are withheld when the store fails mid-stream |
| Gateway crashes between admission and completion | Recovery settles at the hard bound `H` (ADR-004) and appends `completed` with status `abandoned` | The request remains auditable from its `admitted` event |
| Late usage correction while the store is down | The budget counter is corrected first (budget safety does not wait for the audit store); the `correction` event is retried until it is appended | The trail is flagged incomplete until the event exists |
The verifier (ADR-006) reports the incomplete-trail conditions above as anomalies; they are not chain breaks. The demo-only fail-open setting (FR-GW-10) skips the admission gate. When enabled, events carry `audit_mode: fail_open`; it must be off by default and visible in configuration.

**Consequence for streaming (stated plainly).** Strict fail-closed holds at admission for every request. For streams it does not extend to bytes already delivered when the store fails mid-stream. Closing that gap would require either buffering the whole stream until the `completed` append succeeds (which defeats streaming) or durable periodic progress events (cost and complexity not specified here).

**6.4 Owner decision (recorded).** Question put to the owner: FR-AUD-1 says "one audit record" per request; with INSERT-only audit access (FR-AUD-5) a pending record cannot be updated, so a strict pre-forward gate needs more than one entry. Options were: (A) one logical audit trail per request with admission, completion and correction events; (B) one record plus a pre-forward writability probe; (C) one record with all output held until durable.
- **Decision: the owner approved option A**, including its streaming failure boundary (fail-closed at admission; bytes delivered before a mid-stream outage cannot be retracted), when asked directly by the executor in this task's session after the Codex r2 review recommended it. Evidence: the owner's reply to that question in the session; no document or date is recorded for it beyond the run report entry. The review recommendation itself is not the approval.
- **What is not decided:** the exact event schema (fields, encodings), retry and timeout values, and the checkpoint cadence, all specification work for P0-T3 and Phase 2.
- B and C were not chosen. B has a race between probe and final write; C removes incremental streaming.
- The requirements document still says "one audit record" in FR-AUD-1. The executor did not edit the owner's requirements document. The owner may amend its wording to match this decision; until then this ADR is the record of the approved interpretation.

## Consequences
- The midnight edge case lets a caller burst to 2 times the daily cap around the reset. A rolling window would remove it but was not chosen; this must be disclosed, not hidden.
- Treating zero as deny and absent as invalid avoids accidental unlimited budgets at the cost of stricter configuration.
- Mapping identity through cluster objects ties gateway correctness to the Tenant/Agent controllers' labels and ServiceAccount naming, so those are security-sensitive in Phase 1.
- Nothing is measured or enforced in Phase 0.

## Requirement mapping
| Requirement | How this decision addresses it | Enforced at |
| --- | --- | --- |
| FR-GW-2/3 | Token-only identity, 401/403 split, fail closed on TokenReview outage | Phase 2 task 2 |
| FR-GW-4 | UTC-day caps, `budget_exhausted` 429 with scope and `reset_at` | Phase 2 task 3 |
| FR-GW-5 | Per-agent bucket with burst = RPM, per-replica caveat | Phase 2 task 4 |
| FR-GW-7/8 | Reservations bound to admission window; stream error semantics | Phase 2 tasks 3, 6 |
| FR-GW-10 | Admission audit gate before forwarding; usage still settled when a later append fails; mid-stream gap stated (section 6) | Phase 2 task 7 |
| FR-AUD-1/4 | One logical audit trail per request (admitted, completed, corrections; rejected for unadmitted requests), owner-approved interpretation of FR-AUD-1 (6.4); body hash and size only | Phase 2 task 7 |
| NFR-3 | Conditional overshoot bound via reservations and an in-flight limit (ADR-004); not claimed as met yet | Phase 2 |

## Validation
P0-T3 turns this into OpenAPI plus positive and negative fixtures and checks them with `make contracts-check`. Phase 2 adds a midnight-boundary test (injectable clock), identity tests including spoofed body fields, error-code tests, and stream cut-off tests. Audit retry cases, each asserting that request totals and entry counts do not change on retry: the append commits but its acknowledgement is lost (retry returns the existing index); crash after append but before the pending marker is cleared; concurrent retries of the same event from two replicas; duplicate `completed` attempts (recovery versus late original owner: exactly one entry); duplicate `correction` attempts with the same `seq`; the same `event_key` with different content (rejected as `conflicting_event`, nothing inserted); a retry after the store returns from an outage; and a correction whose `seq` is reused across a gateway restart.

## Open questions (not decided, not owner-accepted)
1. **Requests with no attributable tenant.** FR-AUD-1 says every gateway request produces an audit record, but a 401 has no tenant and therefore no per-tenant chain (ADR-006). Proposed: such records go to a reserved platform chain (chain id `_platform`, not a valid tenant name). Needs owner confirmation.
2. Whether the gateway enforces the Tenant's `requestsPerMinute` as an aggregate bucket in addition to per-agent buckets.
3. Burst capacity: `requestsPerMinute` (chosen here) versus a smaller configurable burst.
4. TokenReview audience name, result-cache TTL, platform default and maximum `max_tokens` (TBD in P0-T3).
5. ~~Audit record model~~ **Resolved by the owner (6.4):** one logical audit trail per request. The P0-T3 audit schema may be built on it; event field details remain P0-T3 specification work.

## Owner acceptance
- Evidence: P0-T2 plan, "Accepted owner decisions": "Fixed daily UTC budget window, resetting at 00:00 UTC", with the reset-boundary edge case and the requirement to specify how reservations and streams spanning midnight retain their accounting window. Section 1 implements exactly that.
- Accepted by owner: yes for section 1. Also yes for the audit trail model in sections 6.2 to 6.4: the owner approved one logical audit trail per request (admission, completion and correction events) and its streaming failure boundary; evidence is the owner's reply in the executor's session recorded in 6.4 and in `agents/runs/P0-T2.md`, not a plan document. All other content (sections 2 to 5, and the rest of 6: gate wording, timeouts, event fields) is specification detail the plan assigns to the executor to document ("do not label unspecified choices as owner-accepted"); it is Proposed.
