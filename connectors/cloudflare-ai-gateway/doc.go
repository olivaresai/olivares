// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package cfaigateway is a read-only SourceConnector that polls the Cloudflare
// AI Gateway REST API for per-request logs and emits one model.CostSample per
// log entry. It is the Cloudflare AI Gateway amplitude capture for the Olivares
// AI control plane: it turns the gateway's per-request usage telemetry into
// module XI (FinOps) cost samples with full per-request granularity.
//
// # What it observes (read-first, docs/SECURITY-HARDENING.md)
//
// The Cloudflare AI Gateway logs every request that transits through it,
// recording the model, provider, input/output token counts, cost, duration,
// status and operator-supplied custom metadata. This connector polls those logs
// via GET /accounts/{id}/ai-gateway/gateways/{gw}/logs and emits one CostSample
// per log entry. It is a batch poller: Gather lists gateways (or uses the
// configured one), pages through each gateway's logs, emits, and returns nil at
// EOF; the engine re-runs it on the next poll.
//
// # What it emits
//
// One model.CostSample per log entry (NOT an edge — this is cost, not access):
//
//   - ProviderRef: the provider the log names (e.g. "openai", "anthropic")
//   - ModelRef: the model served (e.g. "gpt-4o", "claude-sonnet-4-20250514")
//   - InputTokens, OutputTokens: from the log's token counts
//   - CostMicroUSD: from the log's cost field when present
//   - OccurredAt: the log's timestamp
//   - Gateway: "cloudflare-ai-gateway"
//   - Provenance: ProvenanceBilled when CF reports cost, ProvenanceEstimated otherwise
//   - WorkspaceRef, Actor, Labels: extracted from the log's custom metadata
//
// A log entry naming no model or carrying no usable token count is skipped.
//
// # Minimal data (docs/SECURITY-HARDENING.md-3)
//
// Only structural usage metadata is read: model, provider, tokens, cost,
// duration, status, timestamp, and operator-configured attribution metadata
// keys. The usageLog struct has NO field for a request body, response body,
// prompt, or completion, so none can travel — there is a negative test
// (TestNoPromptLeaks) that asserts an arbitrary prompt string embedded in a
// fixture NEVER appears in the marshaled CostSamples.
//
// It imports only the SDK and the standard library — never the engine (/core).
package cfaigateway
