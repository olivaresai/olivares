---
title: Model gateway contract
description: >-
  Driver × protocol × transport matrix for governed model invocation:
  CreateMessage, streaming with backpressure, cancellation, and usage.
  Cells are labeled implemented, tested, or design-only.
---

This page is the **invocation contract** for talking to a model endpoint
(OpenAI-compatible, Anthropic Messages, or Ollama) from Olivares. It is not a
replacement for LiteLLM, Bifrost, or another AI gateway. Those remain
destinations. Olivares governs the call: one `CreateMessage` shape, pull-based
streaming, cancel that closes the upstream request, token usage, and errors in
the product envelope.

There is **no** `core/proxy` package. Proxying and governed execution today live
in:

- `connectors/claude-api` — Anthropic `/v1/messages` protocol shell (including
  the optional in-band PEP)
- `cmd/olivares/inferenceproxy.go` — PEP decision (opt-in listener)
- `modules/inferenceproxy` — PEP policy, not the live request
- `modules/models` plus `cmd/olivares/modelsactuate.go` — routing resolve and
  governed execute (still Anthropic `CreateMessage` for every destination,
  including a gateway BaseURL)
- `connectors/local` — Ollama/vLLM **inventory**, not chat invocation
- `connectors/modelprovider` — shared HTTP client, catalog, and the pinned
  chat-text codec

The contract package is `connectors/modelprovider/gateway` (Apache-2.0). It is a
subpackage of the existing library so public integration counts do not change.

## Vertical slice

Run the conformance suite against local fake upstreams (`httptest`). No live
provider, no vendor benchmark:

```sh
go test -race ./connectors/modelprovider/gateway/
```

That suite covers non-stream `CreateMessage`, SSE/NDJSON streaming with
backpressure, cancel that the fake upstream observes as a closed request, token
usage, a stub `CostHook` (Reserve / Commit / Release), and HTTP errors mapped to
the product codes (`bad_request`, `unauthenticated`, `rate_limited`,
`unavailable`, `budget_denied`, `canceled`).

The live PEP and `models.Executor` are **not** switched onto this seam in this
slice. They still use protocol-specific clients. That wiring is the next
bounded task.

## Matrix

Labels are the honesty vocabulary: `tested` (code plus the suite above),
`implemented` (code without this suite), `design-only` (named, not built).

| Driver | Protocol | Transport | Status | Notes |
|---|---|---|---|---|
| `openai-compat` | OpenAI-compatible | HTTP | `tested` | `POST /v1/chat/completions`. LiteLLM and Bifrost OpenAI surfaces use this cell. |
| `openai-compat` | OpenAI-compatible | SSE | `tested` | Same path with `stream=true` and `stream_options.include_usage`. |
| `openai-compat` | OpenAI-compatible | WebSocket | `design-only` | Chat completions are not invoked over WebSocket here. |
| `anthropic-messages` | Anthropic Messages | HTTP | `tested` | `POST /v1/messages`. The live Claude PEP still uses `claude-api`. |
| `anthropic-messages` | Anthropic Messages | SSE | `tested` | Same path with `stream=true`. |
| `anthropic-messages` | Anthropic Messages | WebSocket | `design-only` | Messages is HTTP and SSE, not WebSocket. |
| `ollama` | Ollama | HTTP | `tested` | `POST /api/chat`. Native streaming is chunked NDJSON on this path. |
| `ollama` | Ollama | SSE | `design-only` | `/api/chat` does not speak `text/event-stream`. |
| `ollama` | Ollama | WebSocket | `design-only` | Not used. |

Tool use, vision, embeddings, the OpenAI Responses API, and structured output
are **design-only** on this contract. This slice is text `CreateMessage` plus
stream/cancel/usage.

## Cost hook

`CostHook` is Reserve before the upstream call, Commit with observed tokens on
success, Release on failure or cancel. When the FinOps admission calls
`ReserveBudget` / `CommitReservation` / `ReleaseReservation` are present, the composition root
adapts them (tenant, dimensions, micro-USD stay in that adapter). When they are
not, `NopCostHook` admits every call and records nothing.

This package does not convert tokens to money and does not copy vendor latency
or throughput figures.

## Errors

Failures are `gateway.Error` with a product envelope code and HTTP status.
Messages never include a credential or a prompt. `499` / `canceled` means the
caller canceled; it is not an upstream status.
