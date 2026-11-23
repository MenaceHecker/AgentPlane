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
Per (tenant, window) and per (tenant, agent, window), where *window* is the UTC date of admission (ADR-005): `used` (reconciled tokens) and `reserved` (sum of outstanding reservations). Per (tenant, agent) there is also `inflight`, the number of requests whose provider execution **may still be running**. This is not the number of non-terminal reservations: a request can settle (for example after a cancellation) while the provider keeps executing, so `inflight` has its own release rules (see Execution uncertainty). All of these live in one counter store that must support an atomic conditional update spanning the agent scope, the tenant scope and `inflight` across every gateway replica (see open question 1; per-replica in-memory counters would multiply every bound below by the replica count and are not acceptable). Each reservation record has: id, tenant, agent, window, reserved amount `R`, owner (gateway instance), lease expiry, a fencing `epoch` (integer), `forwarded_at` (durable timestamp set before the first byte is sent to the backend, null until then), flags `inflight_released` and `excess_applied`, and a state. The only non-terminal state is `reserved`. The terminal states are `settled` (charged authoritative actual usage), `settled_at_hard_bound` (consumption unknown, charged the hard bound `H` below) and `released` (proved no work, charge zero). See Settlement rules.

### Admission and reservation
1. Authenticate and map identity to (tenant, agent) from the token only (FR-GW-2/3; ADR-005).
2. Rate-limit (FR-GW-5, separate mechanism, Phase 2 task 4).
3. Compute the reservation `R`. `effective_max_output_tokens` is the request's `max_tokens` clamped to a platform maximum, and a platform default when absent (values specified in the OpenAPI contract, P0-T3; TBD). The input part depends on the model's configured **reservation mode**:
   - `bounded` (default for models where it is valid): `R = input_bound + effective_max_output_tokens`, where `input_bound` is the UTF-8 byte length of all prompt content in the request plus a fixed per-message overhead constant (TBD). This is an upper bound on input tokens **only if** every token of the model's tokenizer covers at least one input byte and the overhead constant covers special/template tokens. A model may use this mode only when its configuration asserts that assumption (A1 below); the mock model's counting rule is defined by P0-T4, so A1 holds there by construction.
   - `context_window`: `R = H`, the model's validated hard maximum total tokens (input plus output). This needs no estimate (assumption A3). It is the mode for any backend where A1 cannot be established. It is much more conservative, so small budgets will reject more requests.

   Assumptions the design relies on: **A1** (above, for `bounded`); **A2** (the backend honors `max_tokens`; during streaming the gateway also enforces it by cutting the stream); **A3** (a validated *hard per-request consumption bound* `H` exists for the model: its maximum total tokens, the context window or a lower validated limit, and the backend enforces it); **A4** (a validated hard execution-time bound `T_exec_max` exists: the backend or a proxy in front of it terminates any single request within `T_exec_max` after the gateway forwards it). `H` and `T_exec_max` are per-model configuration, values TBD. A model for which `H` and `T_exec_max` have not been validated by a conformance test **cannot be enabled for budgeted traffic**: the gateway rejects requests for it as a configuration error (fail closed). The mock model's `H` and `T_exec_max` are defined by its P0-T4 contract.
4. Atomically check **and** reserve on both scopes: admit only if, for the agent and for the tenant, `used + reserved + R <= cap`, and the agent's `inflight < maxInFlight`. The check-and-reserve on the two scopes plus `inflight` is one atomic operation (or ordered tenant-then-agent with rollback) so concurrent requests cannot both pass on a stale read. The tenant counter aggregates all agents and replicas of the tenant. `maxInFlight` is a per-agent platform/agent setting (default TBD); `maxInFlight = 1` serializes an agent's requests.
5. If a scope's budget check fails, respond 429 `budget_exhausted` and indicate the scope (`agent` or `tenant`). If only the in-flight limit fails, respond 429 `concurrency_limited` (ADR-005). Nothing is forwarded and nothing stays reserved.

### Settlement rules (reconciliation, cancellation, failure, crash)
**Principle: consumed budget is never refunded on uncertainty, and unknown consumption is charged the validated hard maximum `H`, not the reservation `R`.** `R` can be below true consumption whenever A1 or A2 fails; charging `R` for an unknown outcome would make such a failure a permanent undercount (a sequence of cancellations, each truly consuming more than `R`, would never be reflected in `used`; this was the review r2 counterexample).

**Classes.**
- *Authoritative usage*: the backend reports token usage for the request (a completed response, or a failed or cancelled request for which the backend returns usage). Charge `A`. If `A > R`, charge the full `A` and flag `overshoot`. If `A > H` (the validated hard bound was violated), also flag `hard_bound_violation`, charge `A`, raise an alert and quarantine the model (reject new requests for it until an operator clears it).
- *Proved no work*: the request was never sent to the backend (failure before forwarding, including a failed audit admission gate, ADR-005; or `forwarded_at` could not be persisted), or the backend's contract defines the returned failure as "no tokens consumed" (the set is model configuration; the mock model's set is defined in P0-T4). Release: charge zero.
- *Unknown consumption*: everything else: client cancellation, timeouts, connection loss after sending, backend errors not in the no-work set, partial output, gateway crash or lease loss. Charge the full hard bound `H` (state `settled_at_hard_bound`, `usage_source: hard_bound`). Observed output is irrelevant to this charge because the backend may have generated more than was observed.

Under A3, `H` is at least the true consumption of any single request, so every charge is at least true consumption. Without a validated `H` there is no such statement, which is why such a model cannot be enabled. Cost of this conservatism: a cancelled or timed-out request is charged `H` (up to the whole context window), which can exceed `R` and even the remaining daily budget; `used` may then pass the cap by up to `H - R` (an accounting overshoot that is also a bound on true consumption). Mitigations: a backend that returns usage on abort turns the case into *authoritative usage*; smaller `max_tokens` lowers `R` but not `H`; no usage feed is specified.

**Exactly-once terminal transition.** Settlement is a compare-and-set on the reservation: it succeeds only if the state is `reserved` and the caller's `epoch` equals the record's current `epoch`. Success atomically does `reserved -= R` and `used += charge` (charge per class) and sets the terminal state. Settlement is keyed by reservation id, so retrying it is a no-op. A reservation reaches exactly one terminal state, once.

**Execution uncertainty and in-flight accounting.** A terminal reservation does **not** mean the provider stopped working: after a cancellation, timeout or crash the provider may still be generating. One reserved request is therefore not the same thing as one possible outstanding execution, and `inflight` follows execution uncertainty, not reservation state.
1. *Increment* is part of the atomic admission step (`inflight += 1`), creating the reservation with `forwarded_at = null`.
2. *Before the first byte is sent* to the backend, the gateway durably sets `forwarded_at` (epoch-checked CAS). If it cannot, it does not forward: proved no work, release, decrement.
3. *Decrement exactly once* per reservation, guarded by `inflight_released`, atomically with the budget effect where one exists:
   - proved no work (never forwarded): decrement together with the release;
   - authoritative completion or a backend abort acknowledgement: decrement together with settlement;
   - unknown consumption: the reservation settles immediately at `H`, but the decrement is deferred until `forwarded_at + T_exec_max` (A4), done by a sweeper or recovery CAS that any gateway instance may run, or earlier if a late authoritative completion or acknowledgement arrives (which proves execution ended; it never refunds budget).
4. *Crash recovery* uses the durable `forwarded_at`: after lease expiry, recovery settles at `H` and schedules the decrement at `forwarded_at + T_exec_max`; if `forwarded_at` is null the crash happened before forwarding (proved no work).
5. Execution uncertainty ends at the earliest of the acknowledgement/completion or `forwarded_at + T_exec_max`. After that the request is treated as no longer executing; its charge stands.
Effect: after a cancellation the agent cannot start another request (`429 concurrency_limited`) until its previous execution is over, for up to `T_exec_max`. This protects the one-request bound at the cost of agent availability.

**Lease, fencing and recovery.**
- The owner extends its lease while the request runs. A gateway that cannot extend or sees its lease expire must treat this as cancellation: stop forwarding and stop writing to the client.
- Any gateway instance may recover a reservation whose lease has expired by CAS that bumps `epoch` while the state is `reserved`; the winner settles it as unknown consumption (`settled_at_hard_bound`). Concurrent recoverers: only one CAS succeeds. Fencing governs settlement; it does not stop remote execution, which is why `inflight` is held until `T_exec_max`.
- **Late completion.** If the original owner completes after recovery, its settlement CAS fails. It must not decrease any charge. The only permitted late correction is **excess-only**: if its authoritative actual `A` exceeds the charge already made (for `settled_at_hard_bound`, that means `A > H`, a hard-bound violation), the extra `A - charge` is added once, guarded by `excess_applied`, and an audit correction event is appended (ADR-005 section 6). Refunds and downward corrections never apply after a terminal state.

**Safety invariant (I2).** Every admitted request's charge is at least its known consumption and, when consumption is unknown, at least `H`; a reservation changes state at most once from `reserved`; only a proved no-work failure releases budget; no correction after a terminal state decreases `used`. Provided `H` and `T_exec_max` are validated (A3, A4), repeated crashes, cancellations, retries and recoveries cannot return consumed budget and the daily `used` is not below the true consumption of the day's requests (daily aggregate, not only per request). Without A3 and A4 no such statement is made.

| Situation | Class and charge | `inflight` released |
| --- | --- | --- |
| Normal completion with backend-reported usage | Authoritative: charge `A` | With settlement |
| Completion without backend usage (counted by the gateway) | Gateway-counted `A` is authoritative only if the backend contract guarantees the counting rule (mock: P0-T4); otherwise unknown: charge `H` | With settlement if authoritative; else at `forwarded_at + T_exec_max` |
| Client disconnects or cancels mid-request | Unknown unless the backend returns usage or acknowledges the abort; otherwise charge `H` | On acknowledgement, else at `forwarded_at + T_exec_max` |
| Backend error before any output | Proved no work only if never sent or in the contract's no-work set: release. Otherwise unknown: charge `H` | With release, else at `forwarded_at + T_exec_max` |
| Backend error or timeout after partial output | Unknown unless usage returned: charge `H` | At `forwarded_at + T_exec_max` unless acknowledged earlier |
| Gateway crash or lease loss with reservations outstanding | Recovery settles each at `H` (`settled_at_hard_bound`), or releases if `forwarded_at` is null | At `forwarded_at + T_exec_max`, or at once if never forwarded |
| Late completion after recovery | CAS fails; excess-only correction only if `A` exceeds the charge; no refund | Immediately, as proof execution ended |
| Audit failure around the response (FR-GW-10) | Specified in ADR-005 section 6 | As above for the outcome class |
| Midnight UTC passes while a request or stream is in flight | Settles into the window recorded at admission (ADR-005). Neither lost nor double-counted | As above |

### Concurrent requests and the overshoot bound
**Admission invariant (I1).** At every admission, for both scopes, `used + reserved + R <= cap` and `inflight < maxInFlight`, where `inflight` counts requests that may still be executing. This holds for any number of replicas as long as the counter store provides the atomic conditional update above.

**What I1 does not say.** I1 limits the *reserved* total, not the *final* `used`. When a request settles, `R_i` is replaced by its charge. For a request with known consumption `A_i`, define the excess `E_i = max(0, A_i - R_i)`. Then `used <= cap + sum(E_i)` over requests admitted in that window, plus the accounting overshoot (`H - R`) from unknown-consumption charges. A reservation bounds actual usage only under A1/A2 (mode `bounded`) or A3/A2 (mode `context_window`); if they hold, every `E_i = 0`.

**Overshoot guarantee, by condition.** "Overshoot" means true consumption beyond the cap.
| Condition | Overshoot |
| --- | --- |
| A3 and A4 validated, and A1/A2 hold | Zero for requests with known consumption. Unknown-consumption requests are charged `H`, so `used` is never below true consumption. NFR-3 is met without relying on the concurrency limit |
| A3 and A4 validated, A1/A2 may fail, `maxInFlight = 1` (counting possibly-executing requests) | At most one request's maximum consumption. At most one request may be executing; every other request's consumption is already in `used` (known `A`, or `H` when unknown); once `used >= cap` nothing more is admitted. Only the request that crosses the cap can carry true consumption beyond it, at most `H - R`. This is the literal NFR-3 bound |
| A3 and A4 validated, A1/A2 may fail, `maxInFlight = c > 1` | Up to `c` requests' excess. NFR-3 is **not** guaranteed in this configuration |
| `H` or `T_exec_max` not validated | The model cannot be enabled for budgeted traffic, so no guarantee is claimed or needed |

The tenant scope has no in-flight limit of its own; tenant overshoot is bounded by the sum over the tenant's agents of their per-agent bounds, so NFR-3 (stated per agent) is the guarantee claimed.

**Trade-offs, stated plainly.** `maxInFlight = 1` throttles an agent to one concurrent request and, after a cancellation or timeout, to waiting up to `T_exec_max` (limits per-agent throughput; effect on the NFR-2 load test TBD not yet measured). `context_window` mode removes estimation risk but rejects more requests near small caps. Charging `H` for unknown outcomes is harsh but is what makes the bound hold. None of these is chosen as a deployment default here. NFR-3 is not claimed as met by this ADR. It is claimed only as a design that meets the bound under the conditions in the table, to be demonstrated by the adversarial tests in Validation.

### Streaming (FR-GW-8)
The reservation is taken up front as above. While streaming, the gateway counts output tokens delivered (from per-chunk usage where the backend provides it, otherwise counted/estimated). When delivered output reaches `effective_max_output_tokens`, or when the reservation would otherwise be exceeded, the gateway stops reading from the backend, cancels the upstream request, sends a terminal error event (`budget_exhausted`) to the client, and settles per the Settlement rules (a gateway-initiated cancellation is *unknown consumption* unless the backend returns usage or acknowledges the abort, so it is charged the hard bound `H`, and the in-flight slot stays held until execution is over). Enforcement is bounded by the reservation taken at admission, so a mid-stream cut-off is a property of the reservation, not of a second check against the live counter.

### Observability
Gateway exposes per-tenant and per-agent `used`, `reserved`, remaining budget, and rejections by reason, scope and overshoot count (FR-OBS-1).

## Consequences
- Near the cap, reservations make the gateway stricter than the final usage would justify; concurrent large requests may be rejected although they would have fit. This is a deliberate trade for a bounded overshoot.
- Overall correctness depends on an atomic check-and-reserve across two scopes, on exactly-once settlement with lease fencing, and on trusting backend usage reports and the no-work classification. These are all test targets in Phase 2. Conservative settlement charges cancelled or timed-out requests the hard bound `H` and holds the agent's in-flight slot for up to `T_exec_max`; that cost is accepted in exchange for never refunding consumed budget and for keeping the one-request bound.
- The tenant cap binds even when the sum of agent caps exceeds it; agent cap not above tenant cap is separately rejected at admission by FR-POL-2/webhook.
- Input-token estimation error is a safety issue, not only a false-rejection issue: an underestimate in `bounded` mode lets admitted requests exceed their reservations (see the guarantee table). That is why `bounded` requires an asserted, testable assumption (A1) and `context_window` exists as the estimate-free alternative.
- Nothing in Phase 0 implements or measures this.

## Requirement mapping
| Requirement | How this decision addresses it | Enforced at |
| --- | --- | --- |
| FR-GW-4 | Per-agent and per-tenant caps checked at admission; 429 `budget_exhausted` | Phase 2 task 3 |
| FR-GW-7 | Reserve before forwarding, reconcile after with exactly-once, conservative settlement | Phase 2 task 3 |
| FR-GW-8 | Mid-stream cut-off bounded by reservation | Phase 2 task 6 |
| FR-GW-10 | Admission audit gate before forwarding; usage still settled when a later audit append fails (ADR-005 section 6) | Phase 2 task 7 |
| NFR-3 | Conditional on validated `H` and `T_exec_max` (A3, A4): zero excess when A1/A2 also hold; with `maxInFlight = 1` (counting possibly-executing requests), at most one request's excess when A1/A2 fail; not guaranteed for `maxInFlight > 1` with failed A1/A2; models without validated `H`/`T_exec_max` are not enabled. Not claimed as met until the adversarial tests below pass | Phase 2 admission, adversarial tests, looping-agent test, E2 |

## Validation
Phase 2: concurrent-admission test (N parallel requests near the cap must not all be admitted, run against several gateway replicas), adversarial underestimation tests, looping-agent cut-off test, stream cut-off test, repeated-crash and cancellation tests (a reservation must never be refunded when consumption is unknown, including across several crash/recovery cycles), **sequential unknown-usage tests** (review r2 case: cap 100, `R = 25`, true consumption 100 per request, four requests each ending in cancellation, timeout or crash: with the withdrawn rule "charge `R`" `used` would stay at 100 while true consumption reached 400; with the rules above the first unknown outcome is charged `H >= 100`, no further admission occurs, and the next request is also blocked by `concurrency_limited` until `forwarded_at + T_exec_max`), in-flight accounting tests (increment on admission, deferred decrement for unknown outcomes, exactly-once decrement under racing sweepers, late acknowledgement, crash before and after `forwarded_at`), a `T_exec_max` boundary test, a hard-bound violation test (quarantine and alert), a late-completion race (original owner finishes after recovery settled: no refund, excess-only correction applied once), concurrent-recoverer and idempotent-retry tests, midnight-boundary test with an injectable clock. Adversarial underestimation cases (illustrative arithmetic, from the P0-T2 review, not measurements): four requests from one agent each reserve 25 against a cap of 100, but each actually consumes 100 because input was underestimated while output stays within `max_tokens`. Expected results: with `maxInFlight = 1` only one request is in flight at a time, so after the first settles `used >= cap` and no further request is admitted; with `maxInFlight = 4` all four can be admitted (4 x 25 = 100 <= cap) and total usage reaches 400, an excess of 300, which this design does **not** prevent and which the guarantee table records as a violation of NFR-3 for that configuration. The test must also cover a tenant with several agents, multiple replicas, and a backend that exceeds `max_tokens`. Overshoot numbers go under `experiments/results/` and `agents/runs/`; none exist yet.

## Open questions (not decided, not owner-accepted)
1. **Counter store.** Where do `used`/`reserved`/`inflight` live so tenant totals are correct across gateway replicas? Whatever is chosen must provide a linearizable atomic conditional update across the agent scope, tenant scope and `inflight` (findings from review r1: otherwise the overshoot bound does not hold). Candidates: (a) PostgreSQL budget table with atomic conditional updates (shared, durable; adds a round trip per request that must fit inside NFR-1's 25 ms p95, TBD not yet measured), (b) a single gateway replica with in-memory counters plus a durable journal (simplest, no horizontal scaling, crash recovery via journal), (c) a separate shared cache. Needs an owner decision before Phase 2 task 3. It must not reuse the audit writer role (ADR-002).
2. Default and maximum `max_tokens`, lease duration, the estimation method, and per-model `H` and `T_exec_max` are TBD, specified in P0-T3/Phase 2 (the mock's in P0-T4). A backend with no way to validate `H` and `T_exec_max` is unsupported for budgeted traffic.

## Owner acceptance
- Evidence: none for this ADR as a whole. FR-GW-7 is a stated requirement; the fixed daily UTC window is owner-accepted (ADR-005). The plan lists this ADR's topic (reserve/reconcile, tenant aggregate accounting, concurrent/streamed usage, bounded overshoot, cancellation and crash recovery) as required content, not as an accepted decision.
- Accepted by owner: no. Status stays Proposed until reviewed.
