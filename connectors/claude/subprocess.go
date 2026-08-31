// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

// subprocess.go MODELS the OTEL subprocess-environment caveat (B3) — a fidelity
// fact the OBSERVE surface must state HONESTLY so the control plane never claims coverage
// it does not have. It is modeling only (no behavior): the receiver ingests whatever the
// Claude Code process exports; this records WHAT that stream structurally cannot contain.
//
// VERIFIED 2026-06-09 (code.claude.com/docs/en/monitoring-usage), recorded in
// (verbatim):
//
//	"Claude Code does not pass OTEL_* environment variables to the subprocesses it spawns,
//	 including the Bash tool, hooks, MCP servers, and language servers. An OpenTelemetry-
//	 instrumented application that you run through the Bash tool does not inherit Claude
//	 Code's exporter endpoint or headers…"
//
// Nuance (verbatim): "When tracing is active, Bash and PowerShell subprocesses
// automatically inherit a TRACEPARENT environment variable containing the W3C trace
// context of the active tool execution span." — so trace CONTEXT crosses the Bash
// boundary (a subprocess span can be correlated to the parent) even though the EXPORTER
// CONFIG (OTEL_* endpoint/headers) does not propagate.

// subprocessKindsUncovered are the subprocess kinds Claude Code spawns that do NOT inherit
// the OTEL_* exporter configuration — so their own internal telemetry never reaches the
// control-plane collector via the parent's managed env (verbatim list).
var subprocessKindsUncovered = []string{
	"Bash tool",
	"hooks",
	"MCP servers",
	"language servers",
}

// SubprocessTelemetryCaveat is the structured, non-sensitive statement of the OTEL
// subprocess-env caveat, for the console/posture to render alongside the managed
// telemetry authoring. It asserts only what the live docs state — never a coverage claim.
type SubprocessTelemetryCaveat struct {
	// Caveat is the headline fidelity limitation (one sentence).
	Caveat string `json:"caveat"`
	// UncoveredKinds are the subprocess kinds whose internal telemetry is NOT carried by
	// the parent's OTEL_* env (the managed telemetry env governs the Claude Code process
	// only).
	UncoveredKinds []string `json:"uncovered_kinds"`
	// TraceContextInherited records the nuance that TRACEPARENT (W3C trace context) IS
	// inherited by Bash/PowerShell subprocesses when tracing is active — so a subprocess
	// span remains correlatable even though its exporter config is not inherited.
	TraceContextInherited bool `json:"trace_context_inherited"`
	// Mitigation states the honest remedy: a subprocess that must export its own telemetry
	// needs the OTEL_* variables set directly in its command, not relied upon from the
	// parent.
	Mitigation string `json:"mitigation"`
}

// SubprocessOTELCaveat returns the verified subprocess-env caveat. The control plane
// surfaces it wherever it authors or explains the managed telemetry env (the sanctioned
// OBSERVE path), so an operator knows the managed OTEL_* env instruments the Claude Code
// process ONLY — a tool/MCP/hook subprocess's own telemetry is invisible unless its
// command sets the exporter variables itself.
func SubprocessOTELCaveat() SubprocessTelemetryCaveat {
	return SubprocessTelemetryCaveat{
		Caveat: "OTEL_* environment variables are NOT propagated to the subprocesses Claude Code spawns; the managed telemetry env instruments the Claude Code process only",
		// Return a copy so a caller cannot mutate the package-level list.
		UncoveredKinds:        append([]string(nil), subprocessKindsUncovered...),
		TraceContextInherited: true,
		Mitigation:            "a subprocess that must export its own telemetry must set the OTEL_* exporter variables directly in its command — the parent's managed env does not reach it; trace correlation across a Bash subprocess is still possible via the inherited TRACEPARENT (W3C trace context)",
	}
}
