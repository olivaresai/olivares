// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package aigateway is the Olivares AI connector for the Envoy AI Gateway
//. It OBSERVES the usage telemetry the gateway already exports —
// it never calls the gateway's API, never opens a listener, never proxies a request,
// and never reads a prompt or a completion. It is gateway-side FinOps: it turns the
// gateway's per-request token meter into module XXI cost telemetry, Anthropic-first.
//
// # What it observes (read-first, docs/SECURITY-HARDENING.md)
//
// The Envoy AI Gateway meters input/output tokens per backend/model for every
// request it routes and surfaces that as AI metadata an operator ships to a log
// sink. This connector parses that EXPORTED artifact (a JSON-lines access log /
// usage export the operator points it at via the "path" config — a file or a
// directory of *.json / *.jsonl / *.ndjson / *.log, optionally .gz). It is a batch
// poller: Gather lists the files, parses each record, emits, and returns nil at EOF;
// the engine re-runs it on the next poll. Close holds no handle; there is no live
// connection.
//
// # What it emits
//
// One model.CostSample per usage record (NOT an edge — this is cost, not access):
//
//	ProviderRef            the provider/backend the record names (e.g. "anthropic"
//	                       for the native Anthropic backend), taken verbatim — a
//	                       Bedrock/Vertex-hosted backend keeps its own name.
//	ModelRef               the served model (response model, else request/original).
//	InputTokens            gen_ai.usage.input_tokens   (or llm_input_token).
//	OutputTokens           gen_ai.usage.output_tokens  (or llm_output_token).
//	CacheReadTokens        the gateway CachedInputToken split, ONLY if the record
//	                       carries it (0 = not reported, never invented).
//	CacheCreation5mTokens  the gateway CacheCreationInputToken split, ONLY if present
//	                       (the gateway has no TTL split, so the 5m/default tier).
//	OccurredAt             the record's start_time/timestamp (else the connector clock).
//	Gateway                GatewayEnvoyAI = "envoy-ai-gateway".
//	Provenance             ProvenanceEstimated by default (the gateway reports tokens,
//	                       not money — the engine derives cost from list pricing);
//	                       ProvenanceBilled + CostMicroUSD when the operator configured
//	                       an LLMRequestCost CEL field that the record carries.
//
// A record naming no model, or carrying no usable token count and no authoritative
// cost (a non-LLM proxy line in the same log), is skipped — never a zero sample. The
// package-local SignalAIGateway = "ai_gateway" identifies the collector (CostSample
// has no Source field; the const exists for log/doc consistency).
//
// # Minimal data (docs/SECURITY-HARDENING.md-3)
//
// Only structural usage metadata is read: token counts, the model name, the provider/
// backend name, the cost, and the timestamp. The usageRecord struct has NO field for
// a request body, a response body, a prompt, a completion, or a header value, so none
// can travel — there is a negative test (TestNoPromptLeaks) that an arbitrary prompt/
// response string embedded in a fixture record NEVER appears in the marshaled
// CostSamples. The read/write nature is irrelevant to a cost sample, so no mode is
// guessed.
//
// # Verified schema (anti-fabrication)
//
// The record shape is a TOLERANT, DOCUMENTED struct, not an invented standard. The
// gateway emits token usage two ways and the operator chooses which fields land in
// the access log, so this connector accepts BOTH documented surfaces and prefers the
// first:
//
//  1. OpenTelemetry GenAI semantic-convention access-log fields:
//     gen_ai.usage.{input,output,total}_tokens, gen_ai.{request,response,original}.model,
//     gen_ai.provider.name, gen_ai.operation.name.
//  2. The io.envoy.ai_gateway dynamic-metadata keys an operator references in an
//     access-log format string: llm_input_token, llm_output_token, llm_total_token,
//     response_model, model_name_override, backend_name.
//
// Both surfaces, the provider value "anthropic", and the cost model (per-request
// LLMRequestCost with a CEL field and the InputToken/OutputToken/CachedInputToken/
// CacheCreationInputToken/TotalToken cost types) were verified against
// https://github.com/envoyproxy/ai-gateway and aigateway.envoyproxy.io
// (observability metrics + access logs + the AIGatewayRoute llmRequestCosts API). The
// connector tolerates version drift by treating every numeric token field as optional
// (*int64) and skipping a record that yields no usable usage, so a renamed or absent
// field degrades to fewer samples, never a fabricated one.
//
// It imports only the SDK and the standard library — never the engine (/core).
package aigateway
