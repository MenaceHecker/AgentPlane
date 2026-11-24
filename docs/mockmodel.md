# Deterministic mock model

A local, loopback-only stand-in for a chat model backend, for developing and testing AgentPlane without any real model provider, credential or network access. It implements the OpenAI-compatible subset of the gateway contract (`POST /v1/chat/completions`, [`docs/gateway/openapi.yaml`](gateway/openapi.yaml)). It is a **test backend**: no authentication, simulated token counts, not for production.

Status: Phase 0 (P0-T4). It is not wired into any gateway or cluster yet; image packaging and scanning are P0-T5.

## Run it
```
make mock-run                          # go run ./cmd/mockmodel on 127.0.0.1:8081
make mock-run MOCK_ARGS="--latency=200ms --error-rate=0.1 --seed=7"
make mock-smoke                        # start a disposable server, check it, stop it
```
The server prints `mockmodel listening on <addr>` on stdout when it is ready, and answers `GET /readyz` with 200. `SIGINT`/`SIGTERM` shut it down gracefully (an in-flight request finishes first).

## Flags
| Flag | Default | Meaning |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8081` | Address to bind. The default is loopback only; any other address is an explicit choice. Port `0` picks a free port. |
| `--latency` | `0` | Simulated latency added to every valid request, `0` to `30s`. |
| `--prompt-tokens` | `0` | Simulated prompt token count. `0` derives it from the request (the number of whitespace-separated words, at least 1). |
| `--completion-tokens` | `16` | Simulated completion length, `0` to `10000`, capped by the request's `max_tokens`. |
| `--error-rate` | `0` | Fraction of requests that fail deterministically with HTTP 500, `0` to `1`. |
| `--seed` | `1` | Seed for response content and failure selection. |

Every range is validated at startup. A bad value (for example `--error-rate=2` or `--completion-tokens=-1`) prints a configuration error and exits with status 2 before anything listens.

## What it guarantees
- **Determinism.** A response is a pure function of the seed and the *canonical* request (parsed fields, so JSON whitespace and key order do not matter). The same request gives byte-identical output across repeats, concurrent callers and restarts. The id (`chatcmpl-` plus 16 hex characters), the content words, the usage, and a fixed `created` timestamp (`1700000000`) never depend on the clock, arrival order or process state.
- **Streaming equals non-streaming.** A request with `"stream": true` returns the same id, content, usage and finish reason, delivered as server-sent events. The `stream` flag is not part of a request's identity.
- **Failures are deterministic too.** A request fails when a score derived from the seed and the request is below `--error-rate`. There is no shared random generator: rate `0` never fails, rate `1` always fails, intermediate rates give a stable per-request outcome that does not depend on arrival order, and a request that fails at one rate fails at every higher rate. Change `--seed` to select a different set. A failure is a `500` with `{"error": {"code": "internal_error", ...}}`, sent before any streaming starts.

## Request and response
Accepted request fields are the contract's subset: `model`, `messages` (`role`: `system`, `user`, `assistant`; `content` string), `max_tokens` (1 to 9007199254740991), `temperature` (0 to 2), `top_p` (0 to 1), `stop` (up to 4 non-empty strings), `stream`. Any other field, including identity-like ones (`tenant`, `agent`, `user`), trailing data or a body over the limit (1 MiB) is `400` with `{"error": {"code": "invalid_request", ...}}`. The mock accepts any model name of 1 to 128 characters; whether a model is enabled is a gateway concern.

Usage is a **simulation**, not tokenizer output: `completion_tokens` is `--completion-tokens` (capped by `max_tokens`, with `finish_reason` `length` when capped, `stop` otherwise) and `prompt_tokens` is `--prompt-tokens` or the derived word count. The content is that many words from a fixed list. Do not read these numbers as measurements of any real model.

A stream is: one chunk per completion token (the first carries `role: assistant`, content chunks have `finish_reason: null`), a final chunk with the finish reason, one terminal usage chunk (`choices: []`), then `data: [DONE]`. Each event is flushed as written. The mock stops writing as soon as the client disconnects.

## Security and limits
Loopback by default, no authentication, no TLS, no external traffic of any kind (no provider APIs, no internet). Bounded: request body 1 MiB, header and read timeouts, a write timeout, simulated latency at most 30 s, completion length at most 10000 tokens. Running it on a non-loopback address exposes an unauthenticated server; only do that deliberately (for example inside a cluster network, later).

## Tests
`go test -race ./mockmodel/... ./cmd/...` covers determinism and concurrency, a golden response, strict errors, the body limit, configuration validation, failure selection and monotonicity, latency through an injected wait (no real sleeping), cancellation and write errors, stream framing and equivalence, real-connection flushing, graceful shutdown, and the real binary (invalid configuration exits 2 before serving). The published gateway request fixtures are replayed and the responses are validated against the OpenAPI schemas. `make mock-smoke` additionally runs the built binary end to end and is part of CI.
