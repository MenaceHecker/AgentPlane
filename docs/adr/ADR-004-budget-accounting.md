# ADR-004: Token budget accounting: reserve, then reconcile

Status: Proposed (mechanism required by FR-GW-7; details not yet owner-reviewed). The fixed daily UTC window it relies on is owner-accepted in ADR-005.
Requirements: FR-GW-4, FR-GW-7, FR-GW-8, FR-GW-10, FR-AUD-1, NFR-3, FR-OBS-1
Plan: [P0-T2](../../agents/plans/P0-T2.md)

## Context
The gateway enforces per-agent and per-tenant daily token budgets (FR-GW-4). Actual token usage is only known after the model responds, so the gateway reserves an estimate before forwarding and reconciles with actual usage afterwards, keeping overshoot bounded (FR-GW-7). Streaming responses must be cut off mid-stream when the budget is reached (FR-GW-8). NFR-3 requires overshoot of at most one in-flight request per agent. This is a security-sensitive path (budget bypass, "never cut" in hard rule 5).

## Options considered
| Option | Summary | Main trade-offs |
| --- | --- | --- |
| A. Check-then-charge | Check remaining budget, forward, charge actual afterwards. | Simple, but N concurrent requests can all pass the check, so overshoot is unbounded. Rejected. |
| B. Reserve-then-reconcile | Atomically reserve an upper-bound estimate, forward, then replace the reservation with actual usage. | Overshoot bounded; near the cap, outstanding reservations can reject requests that would have fit once actual usage is known (conservative false rejections). |
| C. Charge-up-front for the whole cap | Charge the max possible and refund after. | Equivalent to B in effect; B is the same idea with explicit reservation state. |

## Decision (Proposed)
Use **reserve-then-reconcile (B)** with the state and rules below. The counter store (where state lives) is an **open question** (see end); everything else is specified independent of it.

### State
Per (tenant, window) and per (tenant, agent, window), where *window* is the UTC date of admission (ADR-005): `used` (reconciled tokens) and `reserved` (sum of outstanding reservations). Each reservation record has: id, tenant, agent, window, reserved amount, owner (gateway instance), lease expiry, state (`reserved`, `settled`, `released`, `expired`).

### Admission and reservation
1. Authenticate and map identity to (tenant, agent) from the token only (FR-GW-2/3; ADR-005).
2. Rate-limit (FR-GW-5, separate mechanism, Phase 2 task 4).
3. Compute the reservation: `estimated_input_tokens + effective_max_output_tokens`. `effective_max_output_tokens` is the request's `max_tokens` clamped to a platform maximum, and a platform default when absent (values specified in the OpenAPI contract, P0-T3; TBD). This makes the reservation a worst-case upper bound as long as the backend honors `max_tokens`.
4. Atomically check **and** reserve on both scopes: admit only if, for the agent and for the tenant, `used + reserved + reservation <= cap`. The check-and-reserve on the two scopes is one atomic operation (or ordered tenant-then-agent with rollback) so concurrent requests cannot both pass on a stale read. The tenant counter aggregates all agents and replicas of the tenant.
5. If either scope fails, respond 429 with code `budget_exhausted` and indicate the scope (`agent` or `tenant`) that failed. Nothing is forwarded and nothing stays reserved.

### Reconciliation
On completion the gateway computes actual usage (from the backend's reported usage; if missing, from counted tokens), then in one atomic step: `reserved -= reservation`, `used += actual`. Reconcile charges the **window recorded on the reservation**, never the window current at completion. If `actual > reservation` (backend exceeded `max_tokens`), the full actual is charged, the request is flagged `overshoot` in the audit record and a metric, and later requests see the higher `used`.

### Concurrent requests
Each in-flight request holds its own reservation, so `used + reserved <= cap` holds at every admission. With a worst-case reservation and a compliant backend, `used` cannot exceed `cap`. Any overshoot comes only from backend non-compliance or a bad input-token estimate, and is bounded by that request's excess over its own reservation. NFR-3's allowance (at most one in-flight request per agent) is therefore treated as slack for estimate error, not as a normal operating mode. The measured overshoot is TBD (not yet measured); it is demonstrated by the Phase 2 looping-agent test.

### Streaming (FR-GW-8)
The reservation is taken up front as above. While streaming, the gateway counts output tokens delivered (from per-chunk usage where the backend provides it, otherwise counted/estimated). When delivered output reaches `effective_max_output_tokens`, or when the reservation would otherwise be exceeded, the gateway stops reading from the backend, cancels the upstream request, sends a terminal error event (`budget_exhausted`) to the client, and reconciles with what was actually delivered. Enforcement is bounded by the reservation taken at admission, so a mid-stream cut-off is a property of the reservation, not of a second check against the live counter.

### Cancellation, failure and crash
| Situation | Settlement (Proposed) |
| --- | --- |
| Client disconnects mid-request | Cancel upstream; charge input tokens plus output tokens observed so far; release the rest. Output the backend generated but the gateway never observed is not charged (known gap; bounded by one reservation). |
| Backend error before any output | Release the full reservation; charge nothing. Record the failure status in the audit record. |
| Backend error or timeout after partial output | Charge input plus observed output. |
| Gateway crashes or loses its lease with reservations outstanding | Reservations carry a lease (duration TBD, longer than the maximum request time). On expiry a recovery step settles each at the input estimate plus any observed output journaled, and marks it `expired`. This favours not losing usage over refunding. Unobserved output is lost, bounded by one reservation per in-flight request at crash time. |
| Audit append fails after the backend responded (FR-GW-10) | The request is rejected to the client but usage is still reconciled, so the budget reflects tokens actually consumed. |
| Midnight UTC passes while a request or stream is in flight | Settles into the window recorded at admission (ADR-005). Neither lost nor double-counted. |

### Observability
Gateway exposes per-tenant and per-agent `used`, `reserved`, remaining budget, and rejections by reason, scope and overshoot count (FR-OBS-1).

## Consequences
- Near the cap, reservations make the gateway stricter than the final usage would justify; concurrent large requests may be rejected although they would have fit. This is a deliberate trade for a bounded overshoot.
- Overall correctness depends on an atomic check-and-reserve across two scopes, on crash recovery, and on trusting backend usage reports. These are all test targets in Phase 2.
- The tenant cap binds even when the sum of agent caps exceeds it; agent cap not above tenant cap is separately rejected at admission by FR-POL-2/webhook.
- Token estimation quality affects false rejections only, not the bound, as long as the output cap is honored.
- Nothing in Phase 0 implements or measures this.

## Requirement mapping
| Requirement | How this decision addresses it | Enforced at |
| --- | --- | --- |
| FR-GW-4 | Per-agent and per-tenant caps checked at admission; 429 `budget_exhausted` | Phase 2 task 3 |
| FR-GW-7 | Reserve before forwarding, reconcile after | Phase 2 task 3 |
| FR-GW-8 | Mid-stream cut-off bounded by reservation | Phase 2 task 6 |
| FR-GW-10 | Audit failure rejects but still reconciles | Phase 2 task 7 |
| NFR-3 | Upper-bound reservation; overshoot only from estimate/backend non-compliance | Phase 2 looping-agent test, E2 |

## Validation
Phase 2: concurrent-admission test (N parallel requests near the cap must not all be admitted), looping-agent cut-off test, stream cut-off test, cancellation and crash-recovery tests, midnight-boundary test with an injectable clock. Overshoot numbers go under `experiments/results/` and `agents/runs/`; none exist yet.

## Open questions (not decided, not owner-accepted)
1. **Counter store.** Where do `used`/`reserved` live so tenant totals are correct across gateway replicas? Candidates: (a) PostgreSQL budget table with atomic conditional updates (shared, durable; adds a round trip per request that must fit inside NFR-1's 25 ms p95, TBD not yet measured), (b) a single gateway replica with in-memory counters plus a durable journal (simplest, no horizontal scaling, crash recovery via journal), (c) a separate shared cache. Needs an owner decision before Phase 2 task 3. It must not reuse the audit writer role (ADR-002).
2. Default and maximum `max_tokens`, lease duration, and the estimation method are TBD, specified in P0-T3/Phase 2.

## Owner acceptance
- Evidence: none for this ADR as a whole. FR-GW-7 is a stated requirement; the fixed daily UTC window is owner-accepted (ADR-005). The plan lists this ADR's topic (reserve/reconcile, tenant aggregate accounting, concurrent/streamed usage, bounded overshoot, cancellation and crash recovery) as required content, not as an accepted decision.
- Accepted by owner: no. Status stays Proposed until reviewed.
