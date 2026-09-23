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
// body, a matched value or a secret — only tool-type identifiers, families, counts and, for a
// declared remote MCP server, its canonical origin (scheme, host and port; never a path, a
// query, a server or tool name, or an authorization token).
package claudeapi

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"math"
)

// ServerToolEgressInput is the minimal-data input the decider hands the egress gate for
// one inbound request: the resolved tenant + acting agent reference (both derived from the
// inbound credential, never a body field) and the declared tools[]. Tenant/ActorRef are
// opaque string keys for grant resolution — this connector is identity-blind and models no
// principal type.
type ServerToolEgressInput struct {
	Tenant          string // resolved tenant key ("" = the global scope)
	ActorRef        string // authenticated acting agent ref ("" when none is bound)
	UnbindableAgent bool   // true for an API token whose authenticated identity has no agent binding
	// Tools is the request's declared tools[] (each a ServerTool/*ServerTool or
	// map[string]any). Every MCP slot is reduced to exactly
	// map[string]any{"type":"mcp_toolset"}; the original MCP names and configuration never
	// reach the gate. The slot positions and the non-MCP slots are preserved.
	Tools []any
	// MCP describes the request's declared remote MCP servers. nil means the request
	// declares no MCP; a gate must then refuse any MCP-looking tools[] slot.
	MCP *MCPEgressRequest
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
	// MCPAck is the exact copy of the input's MCP coverage, returned only when every MCP
	// destination and every other governed tool is allowed. A request that declares MCP
	// is refused without it; a request without MCP must not carry one.
	MCPAck *MCPEgressCoverage
	// MCPDeny names why an MCP declaration was refused. It is set only with Forward=false;
	// a Forward decision that carries one is invalid coverage.
	MCPDeny MCPDenialCode
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
	// MCP names the first denied MCP destination for a mcp_origin_not_granted refusal. The
	// decider validates it against its own capture and derives the approval subject, plan
	// and reason itself; Subject, Reason and PlanHash are ignored for an MCP target.
	MCP *MCPApprovalTarget
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

// MCPEgressVersion is the version of the MCP coverage protocol below. A gate refuses any
// other version.
const MCPEgressVersion uint32 = 1

// MCPMaxDestinations is the most mcp_servers[] entries one request (or one batch entry's
// params) may declare. A request above it is an invalid declaration.
const MCPMaxDestinations = 20

// MCPDestination is one declared remote MCP server as the egress gate sees it: its index in
// the request's mcp_servers[], the index of its one mcp_toolset in tools[], and the canonical
// origin of its URL ("https://" + host + ":" + effective port, see MCPOriginFromURL). It
// carries no server name, path, query, authorization token or per-tool configuration.
type MCPDestination struct {
	ServerIndex uint32
	ToolIndex   uint32
	Origin      string
}

// MCPEgressCoverage identifies one admission of one request's MCP declaration. Nonce is
// fresh for every admission (and every batch entry); Digest is MCPCoverageDigest over the
// input it was issued with. It is a consistency check between trusted in-process components:
// not a reusable grant, not a persistent receipt, and no proof against a malicious gate.
type MCPEgressCoverage struct {
	Version uint32
	Nonce   [32]byte
	Digest  [32]byte
}

// MCPEgressRequest is the MCP part of ServerToolEgressInput: the coverage the gate must
// acknowledge and the destinations in increasing ServerIndex order (one per mcp_servers[]
// entry, 1..MCPMaxDestinations).
type MCPEgressRequest struct {
	Coverage     MCPEgressCoverage
	Destinations []MCPDestination
}

// MCPApprovalTarget points an approval intent at one MCP destination of the request.
type MCPApprovalTarget struct {
	ServerIndex uint32
}

// MCPDenialCode is the closed set of MCP refusal codes. Each is also the public reason the
// proxy returns; the decider maps it to a fixed HTTP status and error type.
type MCPDenialCode string

// The MCP refusal codes. The decider treats any other non-empty value as
// MCPDenyCoverageUnavailable.
const (
	// MCPDenyInvalidDeclaration: malformed or ambiguous MCP wire (400 invalid_request_error).
	MCPDenyInvalidDeclaration MCPDenialCode = "mcp_invalid_declaration"
	// MCPDenyOriginNotGranted: a destination origin (host or port) is not granted
	// (403 permission_error). The only code that may carry an MCP approval target.
	MCPDenyOriginNotGranted MCPDenialCode = "mcp_origin_not_granted"
	// MCPDenyPolicyDenied: an unbindable scoped identity, a deny-all or a configuration
	// failure (403 permission_error, no approval).
	MCPDenyPolicyDenied MCPDenialCode = "mcp_policy_denied"
	// MCPDenyCoverageUnavailable: a forward without exact coverage, an unsupported coverage
	// version, an inconsistent input or nonce entropy failure (503 api_error, no approval).
	MCPDenyCoverageUnavailable MCPDenialCode = "mcp_coverage_unavailable"
	// MCPDenyBindingChanged: the accepted MCP binding changed or was dropped, an illegal
	// rewrite, or a serialized body/header mismatch (500 api_error, no approval, no
	// sizing fail-open).
	MCPDenyBindingChanged MCPDenialCode = "mcp_binding_changed"
)

// MCPCoverageDigest computes MCPEgressCoverage.Digest for in: SHA-256 over the length-framed
// domain "olivares.mcp.egress.v1", the coverage version and nonce, in.Tenant, in.ActorRef,
// in.UnbindableAgent (one byte), MCPBetaHeader, the destination count and each destination's
// ServerIndex, ToolIndex and Origin in order. uint32 values are big-endian; every string is
// its uint32 byte length followed by its bytes. in.MCP.Coverage.Digest itself is not an
// input. The decider computes it when it issues coverage; a gate recomputes it and refuses a
// mismatch before it selects a grant. It fails for a nil in.MCP or an unframeable length.
func MCPCoverageDigest(in ServerToolEgressInput) ([32]byte, error) {
	unframeable := &MCPEgressError{Code: MCPDenyCoverageUnavailable}
	if in.MCP == nil || uint64(len(in.MCP.Destinations)) > math.MaxUint32 {
		return [32]byte{}, unframeable
	}
	h := sha256.New()
	var n [4]byte
	u32 := func(v uint32) {
		binary.BigEndian.PutUint32(n[:], v)
		_, _ = h.Write(n[:])
	}
	framed := true
	str := func(s string) {
		if uint64(len(s)) > math.MaxUint32 {
			framed = false
			return
		}
		u32(uint32(len(s)))
		_, _ = io.WriteString(h, s)
	}
	str(mcpCoverageDomain)
	u32(in.MCP.Coverage.Version)
	_, _ = h.Write(in.MCP.Coverage.Nonce[:])
	str(in.Tenant)
	str(in.ActorRef)
	if in.UnbindableAgent {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
	str(MCPBetaHeader)
	u32(uint32(len(in.MCP.Destinations)))
	for _, d := range in.MCP.Destinations {
		u32(d.ServerIndex)
		u32(d.ToolIndex)
		str(d.Origin)
	}
	if !framed {
		return [32]byte{}, unframeable
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// mcpCoverageDomain separates MCP coverage digests from every other hash in the product.
const mcpCoverageDomain = "olivares.mcp.egress.v1"
