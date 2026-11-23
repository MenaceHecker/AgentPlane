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
| B. Reserve-then-reconcile | Atomically reserve a bound on usage (an input bound plus the output cap), forward, then replace the reservation with actual usage. | Overshoot bounded; near the cap, outstanding reservations can reject requests that would have fit once actual usage is known (conservative false rejections). |
| C. Charge-up-front for the whole cap | Charge the max possible and refund after. | Equivalent to B in effect; B is the same idea with explicit reservation state. |

## Decision (Proposed)
Use **reserve-then-reconcile (B)** with the state and rules below. The counter store (where state lives) is an **open question** (see end); everything else is specified independent of it.

### State
Per (tenant, window) and per (tenant, agent, window), where *window* is the UTC date of admission (ADR-005): `used` (reconciled tokens) and `reserved` (sum of outstanding reservations). Per (tenant, agent) there is also `inflight`, the number of reservations currently in state `reserved`. All of these live in one counter store that must support an atomic conditional update spanning the agent scope, the tenant scope and `inflight` across every gateway replica (see open question 1; per-replica in-memory counters would multiply every bound below by the replica count and are not acceptable). Each reservation record has: id, tenant, agent, window, reserved amount, owner (gateway instance), lease expiry, state (`reserved`, `settled`, `released`, `expired`).

### Admission and reservation
1. Authenticate and map identity to (tenant, agent) from the token only (FR-GW-2/3; ADR-005).
2. Rate-limit (FR-GW-5, separate mechanism, Phase 2 task 4).
3. Compute the reservation `R`. `effective_max_output_tokens` is the request's `max_tokens` clamped to a platform maximum, and a platform default when absent (values specified in the OpenAPI contract, P0-T3; TBD). The input part depends on the model's configured **reservation mode**:
   - `bounded` (default for models where it is valid): `R = input_bound + effective_max_output_tokens`, where `input_bound` is the UTF-8 byte length of all prompt content in the request plus a fixed per-message overhead constant (TBD). This is an upper bound on input tokens **only if** every token of the model's tokenizer covers at least one input byte and the overhead constant covers special/template tokens. A model may use this mode only when its configuration asserts that assumption (A1 below); the mock model's counting rule is defined by P0-T4, so A1 holds there by construction.
   - `context_window`: `R` = the model's maximum total tokens (input plus output), taken from model configuration. This needs no estimate: a backend cannot process more than its own context window (assumption A3). It is the mode for any backend where A1 cannot be established. It is much more conservative, so small budgets will reject more requests.

   Assumptions the bound relies on: **A1** (above, for `bounded`), **A2** (the backend honors `max_tokens`; during streaming the gateway also enforces it by cutting the stream), **A3** (the backend enforces its own context window, for `context_window`).
4. Atomically check **and** reserve on both scopes: admit only if, for the agent and for the tenant, `used + reserved + R <= cap`, and the agent's `inflight < maxInFlight`. The check-and-reserve on the two scopes plus `inflight` is one atomic operation (or ordered tenant-then-agent with rollback) so concurrent requests cannot both pass on a stale read. The tenant counter aggregates all agents and replicas of the tenant. `maxInFlight` is a per-agent platform/agent setting (default TBD); `maxInFlight = 1` serializes an agent's requests.
5. If a scope's budget check fails, respond 429 `budget_exhausted` and indicate the scope (`agent` or `tenant`). If only the in-flight limit fails, respond 429 `concurrency_limited` (ADR-005). Nothing is forwarded and nothing stays reserved.

### Reconciliation
On completion the gateway computes actual usage (from the backend's reported usage; if missing, from counted tokens), then in one atomic step: `reserved -= reservation`, `used += actual`. Reconcile charges the **window recorded on the reservation**, never the window current at completion. If `actual > reservation` (backend exceeded `max_tokens`), the full actual is charged, the request is flagged `overshoot` in the audit record and a metric, and later requests see the higher `used`.

### Concurrent requests and the overshoot bound
**Admission invariant (I1).** At every admission, for both scopes, `used + reserved + R <= cap` and `inflight < maxInFlight`. This holds for any number of replicas as long as the counter store provides the atomic conditional update above.

**What I1 does not say.** I1 limits the *reserved* total, not the *final* `used`. When a request settles, its reservation `R_i` is replaced by its actual usage `A_i`. Define the excess `E_i = max(0, A_i - R_i)`. Then at the end of a window `used <= cap + sum(E_i)` over requests admitted in that window. A reservation is therefore a bound on actual usage only under the assumptions A1/A2 (mode `bounded`) or A3/A2 (mode `context_window`); if they hold, every `E_i = 0` and `used <= cap`.

**Overshoot guarantee, by condition.**
| Condition | Overshoot |
| --- | --- |
| A1 (or A3) and A2 hold | Zero by construction; NFR-3 is met without relying on concurrency limits |
| Assumptions may fail (unvalidated tokenizer, non-compliant backend) and `maxInFlight = 1` | At most one request's excess: once `used >= cap` no further request is admitted in the window (`used + reserved + R > cap` for any `R > 0`), and only one request can be in flight when `used` first crosses the cap. This is the literal NFR-3 bound ("at most one in-flight request per agent") and does not depend on estimation quality |
| Assumptions may fail and `maxInFlight = c > 1` | Up to `c` requests' excess, because `c` already-admitted requests can all exceed their reservations at once. NFR-3 is **not** guaranteed in this configuration |

The tenant scope has no in-flight limit of its own; tenant overshoot is bounded by the sum over the tenant's agents of their per-agent bounds, so NFR-3 (stated per agent) is the guarantee claimed.

**Trade-offs, stated plainly.** `maxInFlight = 1` throttles an agent to one concurrent request, which limits per-agent throughput (and may constrain the NFR-2 load test, TBD not yet measured). `context_window` mode removes estimation risk but rejects more requests near small caps. Neither is chosen as a deployment default here; Phase 2 must pick per model and record the result. NFR-3 is not claimed as met by this ADR. It is claimed only as a design that meets the bound under the conditions in the table, to be demonstrated by the adversarial tests in Validation.

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
- Input-token estimation error is a safety issue, not only a false-rejection issue: an underestimate in `bounded` mode lets admitted requests exceed their reservations (see the guarantee table). That is why `bounded` requires an asserted, testable assumption (A1) and `context_window` exists as the estimate-free alternative.
- Nothing in Phase 0 implements or measures this.

## Requirement mapping
| Requirement | How this decision addresses it | Enforced at |
| --- | --- | --- |
| FR-GW-4 | Per-agent and per-tenant caps checked at admission; 429 `budget_exhausted` | Phase 2 task 3 |
| FR-GW-7 | Reserve before forwarding, reconcile after | Phase 2 task 3 |
| FR-GW-8 | Mid-stream cut-off bounded by reservation | Phase 2 task 6 |
| FR-GW-10 | Audit failure rejects but still reconciles | Phase 2 task 7 |
| NFR-3 | Conditional: zero excess under A1/A2 (or A3/A2); with `maxInFlight = 1`, at most one request's excess when assumptions fail; not guaranteed for `maxInFlight > 1` with failed assumptions. Not claimed as met until the adversarial tests below pass | Phase 2 admission, adversarial tests, looping-agent test, E2 |

## Validation
Phase 2: concurrent-admission test (N parallel requests near the cap must not all be admitted, run against several gateway replicas), adversarial underestimation tests, looping-agent cut-off test, stream cut-off test, cancellation and crash-recovery tests, midnight-boundary test with an injectable clock. Adversarial underestimation cases (illustrative arithmetic, from the P0-T2 review, not measurements): four requests from one agent each reserve 25 against a cap of 100, but each actually consumes 100 because input was underestimated while output stays within `max_tokens`. Expected results: with `maxInFlight = 1` only one request is in flight at a time, so after the first settles `used >= cap` and no further request is admitted; with `maxInFlight = 4` all four can be admitted (4 x 25 = 100 <= cap) and total usage reaches 400, an excess of 300, which this design does **not** prevent and which the guarantee table records as a violation of NFR-3 for that configuration. The test must also cover a tenant with several agents, multiple replicas, and a backend that exceeds `max_tokens`. Overshoot numbers go under `experiments/results/` and `agents/runs/`; none exist yet.

## Open questions (not decided, not owner-accepted)
1. **Counter store.** Where do `used`/`reserved`/`inflight` live so tenant totals are correct across gateway replicas? Whatever is chosen must provide a linearizable atomic conditional update across the agent scope, tenant scope and `inflight` (findings from review r1: otherwise the overshoot bound does not hold). Candidates: (a) PostgreSQL budget table with atomic conditional updates (shared, durable; adds a round trip per request that must fit inside NFR-1's 25 ms p95, TBD not yet measured), (b) a single gateway replica with in-memory counters plus a durable journal (simplest, no horizontal scaling, crash recovery via journal), (c) a separate shared cache. Needs an owner decision before Phase 2 task 3. It must not reuse the audit writer role (ADR-002).
2. Default and maximum `max_tokens`, lease duration, and the estimation method are TBD, specified in P0-T3/Phase 2.

## Owner acceptance
- Evidence: none for this ADR as a whole. FR-GW-7 is a stated requirement; the fixed daily UTC window is owner-accepted (ADR-005). The plan lists this ADR's topic (reserve/reconcile, tenant aggregate accounting, concurrent/streamed usage, bounded overshoot, cancellation and crash recovery) as required content, not as an accepted decision.
- Accepted by owner: no. Status stays Proposed until reviewed.
