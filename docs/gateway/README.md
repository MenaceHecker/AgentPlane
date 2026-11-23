# Gateway contract

[`openapi.yaml`](openapi.yaml) is the Phase 0 contract for the AgentPlane gateway (OpenAPI 3.0.3). This page explains the behavior behind it and is the place to read what is decided, what is only specified, and what is not defined.

**Status.** Contract only. No gateway exists. Authentication, budgets, rate limits, tool allowlists, audit appends and streaming are all Phase 2 work, and nothing in this directory is implemented, enforced or measured. The contract follows the accepted and proposed decisions in [`docs/adr/`](../adr/README.md); this page says which is which.

## Endpoints (FR-GW-1)
| Endpoint | Purpose |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI-style chat completion, optionally streamed with server-sent events |
| `POST /v1/tools/{name}` | Call a named tool through the gateway |

No health endpoints are defined by this contract.

## Identity comes from the token only (FR-GW-2/3, ADR-005 section 4)
- Callers authenticate with a projected Kubernetes ServiceAccount token as a bearer token. The gateway validates it with TokenReview. There are no static API keys.
- The authenticated ServiceAccount maps to (tenant, agent). Request bodies and headers never establish or override identity. The request schemas set `additionalProperties: false` and declare no identity fields, so a body that carries `tenant`, `agent`, `user` or similar is rejected as `400 invalid_request`.
- `401 unauthenticated`: missing, malformed, expired or rejected token. `403 forbidden`: authenticated but not mapped to an active Tenant and Agent. `403 tool_not_allowed`: tool not permitted. `503 identity_unavailable`: TokenReview unreachable (fail closed).

## OpenAI-compatible subset
The chat endpoint accepts only this subset; any other field is rejected.
| Field | Supported | Notes |
| --- | --- | --- |
| `model` | yes | A model that is not enabled for budgeted traffic is `400 invalid_request` (ADR-004) |
| `messages[]` (`role`: `system`, `user`, `assistant`; `content`: string) | yes | At least one message. No `tool` role, no multimodal content, no `name` |
| `max_tokens` | yes | Integer at least 1. Absent means a platform default; above the platform maximum is clamped. Default and maximum are deployment configuration, not fixed here |
| `temperature` (0 to 2), `top_p` (0 to 1), `stop` (up to 4 strings) | yes | |
| `stream` | yes | Boolean, default false |
| `tools`, `functions`, `tool_choice`, `n`, `logprobs`, `response_format`, `seed`, `user`, penalties, `stream_options` | **no** | Rejected as `invalid_request`. The terminal usage chunk is always sent when streaming, so `stream_options` is unnecessary |

Token counts (`max_tokens` and every `usage` field) are limited to the safe-integer range `0..9007199254740991` (2^53-1), the same domain as the audit contract: larger integers cannot be canonicalized losslessly (`docs/audit/hash-chain.md`). A request above it is `400 invalid_request`.

Responses use OpenAI field names (`usage.prompt_tokens`, `completion_tokens`, `total_tokens`). The completion `id` equals the gateway-assigned `request_id`, which is also the `X-Request-Id` response header and the audit `request_id` (ADR-005 section 6).

## Errors (ADR-005 section 5)
Errors use `{"error": {"code", "message", "request_id"}}`. Each status has its own response schema in the OpenAPI file, so a wrong code for a status fails validation.
| HTTP | `error.code` | Meaning | Extra |
| --- | --- | --- | --- |
| 400 | `invalid_request` | Malformed body, unsupported or identity-like field, bad `max_tokens`, unknown tool, model not enabled | |
| 401 | `unauthenticated` | Bad or missing token | |
| 403 | `forbidden`, `tool_not_allowed` | Unmapped identity; tool not allowed | |
| 429 | `budget_exhausted` | Daily token budget of the `agent` or `tenant` scope would be exceeded (FR-GW-4) | `scope`, `reset_at` (next 00:00 UTC) |
| 429 | `rate_limited` | Per-agent token bucket empty (FR-GW-5) | `Retry-After` |
| 429 | `concurrency_limited` | Agent already has its maximum requests in flight (ADR-004) | `Retry-After` |
| 500 | `internal_error` | Unexpected | |
| 502 / 504 | `upstream_error` / `upstream_timeout` | Backend failure or timeout | |
| 503 | `audit_unavailable` | Audit append failed (see audit below) | |
| 503 | `identity_unavailable` | TokenReview unreachable | |

Messages never contain tokens, prompt text or another tenant's data. Error objects carry no extra fields. `budget_exhausted` is valid only with both `scope` (`agent` or `tenant`) and `reset_at`; the generic error detail cannot carry that code, so a budget error missing either field (or both) is rejected, including as a stream `error` event.

## Budget window and accounting (ADR-004, ADR-005)
- **Fixed daily UTC window (owner-accepted).** A window is the UTC calendar day `[00:00:00Z, 24:00:00Z)`, identified by the UTC date of the request's admission time. It resets at 00:00 UTC. It is not a rolling 24-hour window.
- **Reset-boundary edge case.** A caller can use the whole remaining allowance of day D just before 00:00 UTC and the full allowance of day D+1 just after. Up to 2 times the daily cap can therefore be consumed within a few seconds around midnight, and in any 24-hour span that straddles midnight at most 2 times the cap. This is accepted behavior, not a defect. Per-minute rate limits still apply.
- **Admission and reservation.** The gateway reserves a bound on the request's tokens before forwarding, checking the agent scope and the tenant scope atomically, then reconciles after the response. `budget_exhausted` names the failing scope.
- **Windows spanning midnight.** A reservation settles into the window of its admission, whenever the request or stream ends. A request admitted at 23:59:59Z that finishes at 00:00:05Z charges day D only: it is neither lost nor double-counted, and a stream is not cut off just because midnight passed.
- **Unknown consumption** (cancellation, timeout, crash) is charged a validated hard maximum per request, never refunded; while the provider may still be executing, the agent's in-flight slot stays held and new requests get `429 concurrency_limited`. NFR-3 (overshoot at most one in-flight request per agent) is conditional on the assumptions in ADR-004 and is not claimed as met by this contract.

## Streaming (FR-GW-8)
With `"stream": true` the response is `text/event-stream`. Each event is one line `data: <payload>` and a blank line. A stream has exactly one of these two endings:
1. **Normal completion:** zero or more `chat.completion.chunk` events with content, then **one terminal usage chunk** (`choices: []`, `usage` set), then `data: [DONE]`.
2. **Failure or budget cut-off after the stream started:** one `error` event (`{"error": {...}}`), then the stream closes. **No `[DONE]` follows an error.** The HTTP status was already 200 and cannot change.

Nothing follows `[DONE]` or an error event. When the delivered output reaches the reserved output bound, the gateway stops the upstream request and sends `budget_exhausted` (ADR-004). If the client disconnects, the gateway cancels the upstream request and sends nothing further; consumption is then settled as unknown unless the backend reports usage (ADR-004).

## Audit availability (FR-GW-10, ADR-005 section 6)
- Before forwarding anything, the gateway must append an `admitted` audit event. If that fails, the response is `503 audit_unavailable` and nothing was forwarded or charged.
- A non-streaming response is held until its `completed` event is appended; if that cannot be done the response is discarded and the client gets `503 audit_unavailable`.
- **Streaming limit, stated plainly.** Strict fail-closed holds at admission. If the audit store fails after streaming has started, bytes already delivered cannot be retracted. A durable `admitted` event exists, usage is charged, and the missing `completed` event is appended by recovery later. It is not guaranteed that a `completed` event exists before bytes are delivered.
- Each request has one logical audit trail (`admitted`, `completed`, optional `correction` events; `rejected` for requests rejected before admission), an owner-approved reading of FR-AUD-1. See [`docs/audit/`](../audit/hash-chain.md).

## Tool calls
`POST /v1/tools/{name}` takes `{"arguments": {...}}` and returns `{"id", "object": "tool.result", "tool", "result"}`. The tool must be in both the Tenant's `egress.allowedTools` and the Agent's `tools` (FR-GW-6). A tool call consumes the per-agent request rate limit but no token budget. A tool name the platform does not know is `400 invalid_request`; a known tool that is not allowed for this agent is `403 tool_not_allowed`. Note this lets an authenticated caller distinguish unknown tools from disallowed ones.

## Trace context (FR-GW-9)
Both operations accept `traceparent` (and pass `tracestate` through). The pattern in the OpenAPI file checks the W3C format in lowercase hex. The W3C rule that all-zero trace and parent ids are invalid cannot be written as a pattern and is left to implementations.

## Fixtures and how they are checked
`examples/` holds valid and invalid request, response, header and SSE stream fixtures, listed in `examples/manifest.json` with the reason each invalid one is invalid and a substring of the expected rejection. `make contracts-check` loads `openapi.yaml` with kin-openapi, requires it to be a valid OpenAPI document, and validates every fixture against the declared schemas: each valid fixture must pass, each invalid one must be rejected for the stated reason, response fixtures are checked against the schema the operation declares for that status, and the SSE parser enforces the stream rules above (terminal usage chunk then `[DONE]`, or one error event with no `[DONE]`, nothing afterwards). Tests also check that no request schema declares identity fields, that every status and every error code has a valid fixture, and that no fixture file is unlisted. These checks show the document and fixtures are consistent; they say nothing about a running gateway.

## Decision status of this contract
| Item | Status |
| --- | --- |
| Fixed daily UTC window, reset-boundary edge case, cross-midnight accounting | Owner-accepted (ADR-005) |
| One logical audit trail per request and its streaming failure boundary | Owner-approved (ADR-005 6.4) |
| Token-only identity, error codes and the 401/403 split, reservation and settlement rules, burst and zero-value rules | Specified in ADR-004/005 as Proposed; implemented here as written, not owner-approved |
| Gateway audience name `agentplane-gateway`; `finish_reason` values `stop` and `length`; completion `id` equals `request_id`; tool calls use no token budget; unknown tool is 400; SSE framing; field names of the tool result; the supported OpenAI subset | **Specification choices made while writing this contract. Not owner-approved.** Cheap to change before Phase 2 |
| Default and maximum `max_tokens`, per-model hard bound `H` and `T_exec_max`, tenant-wide requests-per-minute enforcement, burst size | Not defined here. Deployment configuration or open ADR questions |
