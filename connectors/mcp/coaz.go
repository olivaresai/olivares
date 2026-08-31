// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
)

// coaz.go defines the COAZ (AuthZEN Profile for MCP Tool Authorization) evaluator
// seam on the MCP Resource Server PEP. When wired, the RS calls the evaluator as
// an ADDITIONAL gate in the tools/call path — after the toolset deny-by-default,
// scope and role checks, before HITL. The evaluator maps the MCP tools/call
// request to an AuthZEN Subject-Action-Resource-Context evaluation and returns the
// PDP's decision.
//
// The seam follows the same pattern as PinVerifier, RenderInspector,
// and ElicitationMediator: nil = no COAZ evaluation (the community build;
// the existing gates keep working — no rug-pull). The concrete implementation
// (in cmd/olivares/) calls the plane's AuthZEN evaluation endpoint over HTTP.
//
// COAZ vocabulary (documented, not enforced here — the PDP handles the mapping):
//   Subject:  {type: "mcp_client", id: "<user-external-id-or-email>"}
//   Action:   {name: "mcp:tools/call"}
//   Resource: {type: "mcp_tool", id: "<tool-name>"}
//   Context:  {mcp_server: "<server-resource-uri>", mcp_scope: [...], tenant: "..."}

// COAZEvaluator evaluates an MCP tools/call request against the AuthZEN PDP using
// the COAZ profile vocabulary. Implementations are safe for concurrent use.
type COAZEvaluator interface {
	EvaluateToolCall(ctx context.Context, req COAZRequest) (COAZDecision, error)
}

// COAZRequest is the input for a COAZ evaluation. It carries enough information
// to build the AuthZEN Subject-Action-Resource-Context tuple.
type COAZRequest struct {
	// Subject is the validated token's subject (user id or external identity).
	Subject string
	// Issuer is the validated token's issuer.
	Issuer string
	// Tool is the name of the tool being called.
	Tool string
	// ServerURI is the MCP server's canonical resource URI (RFC 8707).
	ServerURI string
	// Scopes is the validated token's granted scope set.
	Scopes map[string]struct{}
	// Annotations are the tool's behavioral annotations (server-owned, from the
	// toolset policy — NOT the untrusted tool definition).
	Annotations *ToolAnnotations
	// Tenant is the RS's configured tenant identifier.
	Tenant string
}

// COAZDecision is the result of a COAZ evaluation.
type COAZDecision struct {
	// Allow is the PDP's authorization decision (true = permit, false = deny).
	Allow bool
	// Reason is the PDP's non-sensitive explanation (surfaced in audit, never
	// leaked to the MCP client). It is human-readable, UNSTABLE text: the
	// evidence EffectDigest deliberately never binds it (a cosmetic wording change
	// must not change an effect identity).
	Reason string
	// DecisionRef is the PDP's STABLE, replayable reference for THIS decision
	// (e.g. a decision id the PDP can reproduce on demand).: bound into the
	// evidence EffectDigest when the evaluator provides it; empty is the explicit
	// stable absent marker (the digest binds the absence, never fabricates a ref).
	DecisionRef string
	// PolicyVersion is the STABLE version of the policy set the PDP evaluated,
	// bound into the EffectDigest like DecisionRef (empty = explicit absence).
	PolicyVersion string
}
