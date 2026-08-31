// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package claude is the high-fidelity cooperative-telemetry connector for Claude
// Code (ARCHITECTURE.md, §6; README.md modules I, II, III). It is a streaming
// SourceConnector whose Gather runs an OTLP receiver (gRPC + HTTP) for Claude
// Code's own OpenTelemetry output and an HTTP endpoint for its PreToolUse /
// PostToolUse hooks, correlates the two by tool_use_id, and emits normalized
// observations through the SDK Sink: access edges (an agent session touched a
// resource, read or write), cost samples (per-request model usage), and findings
// (anti-evasion telemetry gaps).
//
// It imports only the SDK (and go.opentelemetry.io/proto/otlp for the OTLP wire
// types), never the engine, so it ships under Apache-2.0 (LICENSING.md). It is
// read-first and minimal-data: it persists relationships, never payloads. A raw
// tool input or shell command (which can carry a secret or PII) is reduced to a
// redacted resource reference before it ever reaches an observation
// (internal/redact, docs/SECURITY-HARDENING.md).
//
// Why a connector and not core ingest: the SDK contract notes that a generic
// OTLP receiver is engine ingest, but a streaming source is explicitly allowed to
// block in Gather running a receiver (sdk.SourceConnector). The value here is the
// Claude-Code semantic-convention mapping, hook correlation and redaction — that
// is connector territory. The mapping is transport-independent (see
// observations.go), so it is reusable as the "Claude profile" if the engine later
// grows a shared multi-tenant OTLP ingest endpoint.
package claude
