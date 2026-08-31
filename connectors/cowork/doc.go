// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package cowork is the cooperative-telemetry connector for Claude Cowork — the
// sibling of Claude Code for knowledge work (A1). It is a
// streaming SourceConnector whose Gather runs an OTLP/HTTP logs receiver for
// Cowork's own OpenTelemetry output and emits normalized observations through the
// SDK Sink: attributed R/RW access edges (a Cowork session touched a file, an MCP
// connector or a skill), cost samples (per api_request model usage), and findings
// — most importantly, an AUTO-APPROVED-HIGH-RISK finding for an AI-initiated
// action that ran without a human in the loop (decision_source ∈ {config, hook})
// on a write/destructive tool, the governance signal a control plane that GOVERNS
// Cowork must not miss.
//
// Cowork is NOT Claude Code, and the differences shape this connector (verified
// against claude.com/docs/cowork/monitoring + the Claude Enterprise blog, jun-2026):
//   - Transport is OTLP/HTTP ONLY (http/json or http/protobuf); there is no gRPC,
//     and Cowork emits LOGS/EVENTS only (no metrics, no traces). The collector
//     endpoint, protocol and auth headers are set in the Cowork admin console —
//     Anthropic's cloud PUSHES OTLP to the configured endpoint, so unlike Claude
//     Code (a local agent posting to loopback) this receiver is an INBOUND endpoint
//     that MUST be auth-gated when reachable off-host (otlp.go; deny-closed in Open).
//   - Five event types: user_prompt, tool_result, api_request, api_error,
//     tool_decision. Resource attribute service.name = "cowork" distinguishes it
//     from Claude Code ("claude-code"). The on-the-wire event name may be bare
//     ("tool_result") or carry Claude Code's "claude_code." prefix; both are
//     normalized to the canonical bare name (events.go) so a real Cowork event is
//     never missed.
//   - Every event carries prompt.id (a turn-correlation UUID) and the shared
//     account identifier (user.account_uuid / user.account_id) that lets an auditor
//     CORRELATE Cowork OTEL with the Anthropic Compliance API records the Compliance
//     feed itself does not capture for Cowork (the documented cowork-not-captured
//     gap, claude-compliance/taxonomy.go). The connector materializes that account
//     as a clear identity edge (observations.go) so the join happens at the account
//     entity in inventory.
//   - Cowork ALWAYS includes prompt content and tool details in its events (it is
//     not the opt-in posture Claude Code's OTEL_LOG_* flags gate). The connector is
//     therefore minimal-data by construction: a raw tool input is reduced to a
//     redacted resource reference before it ever reaches an observation, and the
//     prompt text is never read (internal/redact, docs/SECURITY-HARDENING.md). A startup posture
//     finding records this on the ledger so an auditor can prove what was retained.
//
// It imports only the SDK (and go.opentelemetry.io/proto/otlp for the OTLP wire
// types) and connectors/internal/redact, never the engine, so it ships under
// Apache-2.0 (LICENSING.md). The Cowork governance authoring surface (plugin /
// marketplace lockdown, managed MCP connector allow/deny, group spend limits) lives
// in governance.go as pure functions the AGPL governance module calls; the
// file-authorable keys are rendered by reusing the managedsettings connector,
// and the console-only controls (per-plugin install state, the GA per-tool
// connector controls, group spend) are modeled and verified honestly rather than
// faked as managed-settings keys. The GA per-tool connector controls in particular
// (role editor, "Connectors" tab: Always allow / Needs approval / Blocked) are
// modeled in controls.go as the operator-authored org-EFFECTIVE policy
// (connector_controls): its non-blocked entries become PERMITTED policy edges for
// the access-map diff, and a tool_result contradicting it (a blocked connector/tool
// executed, or a needs-approval one auto-approved) emits a live drift finding.
package cowork
