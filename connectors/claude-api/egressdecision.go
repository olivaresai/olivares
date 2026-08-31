// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// egressdecision.go defines the pure DATA envelope the inline-proxy decider
// (cmd/olivares, AGPL) and the OPTIONAL commercial server-tool egress gate
// (enterprise/servertoolegress, closed) exchange — the server-tool sibling of proxy.go's
// ProxyDecision. It carries NO policy: the egress grant format, the allowed/blocked-domain
// validation+clamp algebra and the deny-closed decision all live in the commercial add-on.
// This connector only defines the SHAPE the two sides agree on, so neither imports the
// other's package — exactly as it already defines ProxyDecision for the proxy shell.
//
// The zero value is a DENY (Forward=false): a gate that returns a zero value on a path it
// forgot fails closed. Minimal data (docs/SECURITY-HARDENING.md): nothing here carries a prompt, a response
// body, a matched value or a secret — only tool-type identifiers, families and counts.
package claudeapi

// ServerToolEgressInput is the minimal-data input the decider hands the egress gate for
// one inbound request: the resolved tenant + acting agent reference (both derived from the
// inbound credential, never a body field) and the declared tools[]. Tenant/ActorRef are
// opaque string keys for grant resolution — this connector is identity-blind and models no
// principal type.
type ServerToolEgressInput struct {
	Tenant          string // resolved tenant key ("" = the global scope)
	ActorRef        string // authenticated acting agent ref ("" when none is bound)
	UnbindableAgent bool   // true for an API token whose authenticated identity has no agent binding
	Tools           []any  // the request's declared tools[] (each a ServerTool/*ServerTool or map[string]any)
}

// ServerToolEgressDecision is the egress gate's verdict for one inbound request's declared
// tools[]. The decider NEVER lets it FORCE an allow — on Forward the request still runs the
// remaining (budget, recording) chain; a non-Forward (zero/deny) value maps Status/
// ErrorType/Reason onto a proxy deny. The gate may only DENY or REWRITE the forwarded
// tools[]; it cannot bypass a gate that already ran ahead of it.
type ServerToolEgressDecision struct {
	// Forward allows the request to continue. The zero value (false) is a DENY.
	Forward bool
	// GovernedTools, when Rewritten is true, REPLACES the forwarded request's tools[] (the
	// gate validated + clamped allowed_domains/blocked_domains/max_uses against the egress
	// grant). When Rewritten is false the decider leaves the original tools[] untouched.
	GovernedTools []any
	Rewritten     bool
	// Deny mapping (used only when Forward is false).
	Status    int    // e.g. 403 / 402
	ErrorType string // Anthropic-style error type ("permission_error", "billing_error", …)
	Reason    string // short, non-sensitive (never enumerates policy internals)
	// ApprovalIntent, when non-nil, asks the decider to OPEN a governed approval for this
	// denied server-tool egress via the existing approval seam. It does NOT resume the
	// request inline (the proxy is synchronous — the call still denies); it lets a future
	// HITL plane pick the intent up so a human can grant the egress. nil = a clean deny.
	ApprovalIntent *ServerToolEgressApprovalIntent
	// Findings the decider should publish on the bus (posture/forensic): an unrecognized
	// tool version, a code_execution allow whose real network egress is Anthropic-org-
	// governed, a denied egress. nil = none.
	Findings []ServerToolEgressFinding
}

// ServerToolEgressApprovalIntent is the minimal-data request to open a governed approval
// for a denied server-tool egress (the decider binds it to the existing approval
// bridge). Identifiers only — never a prompt, a domain value or a secret.
type ServerToolEgressApprovalIntent struct {
	Action   string // the governed action label (e.g. "inference.servertool.egress")
	Family   string // the denied server-tool family (web_search | web_fetch | code_execution)
	ToolType string // the dated tool type id as declared on the request
	Subject  string // the approval subject reference (the family or the type)
	Reason   string // short, non-sensitive
	PlanHash string // anti-TOCTOU binding hash the gate computed over the denied intent
}

// ServerToolEgressFinding is a posture/forensic observation the gate asks the decider to
// publish. Detail is non-sensitive context the decider hashes into a DetailHash; Severity
// is "high" | "info". It carries no prompt, response or matched value (minimal data).
type ServerToolEgressFinding struct {
	Kind     string
	Severity string // "high" | "info"
	Title    string
	Family   string
	ToolType string
	Detail   string   // non-sensitive context, hashed by the decider
	OWASPLLM []string // e.g. ["LLM02:2025"]
}
