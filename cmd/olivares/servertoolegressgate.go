// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// servertoolegressgate.go is the AGPL composition-root glue for the OPTIONAL commercial
// server-tool egress gate (enterprise/servertoolegress, P0 #1 — Claude's biggest
// unenforced egress hole). It defines the seam the inference-proxy decider consumes and
// translates the gate's verdict into the decider's existing primitives: a proxy deny, a
// rewritten req.Tools, a published posture finding, and (2026-06-19, D4) an OPENED
// governed approval via the existing bridge.
//
// The default AGPL build injects a nil gate (wire_noenterprise.go), so this glue is inert
// and the inline proxy keeps its prior observe-only behavior — NO rug-pull. Under
// `-tags enterprise` with an egress config, wire_enterprise.go injects the real gate
// (servertoolegress.Gate), the ONLY edge by which the AGPL root references the commercial
// module (build-tag-gated, like newFederation).
//
// HONESTY: this is verified-deployed enforcement AT THE PROXY only. A caller who points
// ANTHROPIC_BASE_URL elsewhere, or whose traffic never transits this proxy, evades it
// (modules/inferenceproxy/doc.go). The gate can only DENY or REWRITE tools; the decider
// runs it AFTER the deny-closed security gates and BEFORE the fail-open budget gate, and a
// Forward verdict never skips the remaining chain.
//
// MCP (C8 E2-3): with a gate installed, every request is captured by the connector's MCP
// snapshot first. The gate sees each MCP tools[] slot as a marker and each declared server
// as an index and canonical origin; a request declaring MCP forwards only with the exact
// acknowledgment of its fresh coverage, and the returned governed request carries the
// accepted binding the later checks and serializers verify. MCP refusals map through the
// closed code table below, never through adapter text. A missing origin grant opens a
// request-only approval notification whose subject, plan and reason Community derives from
// its own capture; no approval state ever authorizes MCP — only an explicit origin grant at
// a later admission does. A legacy-family deny of a request that also declares MCP keeps the
// adapter's identifier-only approval fields (action, subject, plan hash) but never its
// free-form reason.

// serverToolEgressGate is the narrow seam the decider depends on (Go structurally satisfies
// it; *servertoolegress.Gate implements it under -tags enterprise).
type serverToolEgressGate interface {
	GovernEgress(ctx context.Context, in claudeapi.ServerToolEgressInput) claudeapi.ServerToolEgressDecision
}

// governEgress is gate 5b: capture the request's MCP declaration (or its absence), ask the
// gate over the sanitized input, map a deny, and on Forward return the governed request with
// its accepted binding and the snapshot the later checks use. identity fields come from the
// admitted identity, never from the body.
func (d *inferenceProxyDecider) governEgress(ctx context.Context, req claudeapi.MessageRequest, tenant model.TenantID, actor, sessionRef string, unbindable bool) (claudeapi.MessageRequest, *claudeapi.MCPEgressSnapshot, gateResult, bool) {
	_, snap, err := claudeapi.SnapshotMCPEgress(req)
	if err != nil {
		return claudeapi.MessageRequest{}, nil, mcpGateDeny(mcpErrorCode(err)), false
	}
	in, err := snap.GateInput(tenant.String(), sessionRef, unbindable)
	if err != nil {
		return claudeapi.MessageRequest{}, nil, mcpGateDeny(mcpErrorCode(err)), false
	}
	declaresMCP := in.MCP != nil
	var dests []claudeapi.MCPDestination
	if declaresMCP {
		// A private copy: the gate receives in by value but shares its slices.
		dests = append(dests, in.MCP.Destinations...)
	}
	egDec := d.egress.GovernEgress(ctx, in)
	d.publishEgressFindings(ctx, tenant, req.Model, egDec.Findings, declaresMCP)
	if !egDec.Forward {
		if declaresMCP || egDec.MCPDeny != "" {
			return claudeapi.MessageRequest{}, nil, d.denyMCPEgress(ctx, tenant, actor, sessionRef, egDec, dests), false
		}
		d.openEgressApproval(ctx, tenant, actor, egDec.ApprovalIntent)
		status, errType := validatedEgressDeny(egDec.Status, egDec.ErrorType)
		return claudeapi.MessageRequest{}, nil, gateDeny(gateCodeServerToolEgress, sdk.FailurePolicyDeny, status,
			errType, firstNonEmpty(egDec.Reason, egressDenyReason)), false
	}
	gov, err := snap.ApplyDecision(egDec)
	if err != nil {
		return claudeapi.MessageRequest{}, nil, mcpGateDeny(mcpErrorCode(err)), false
	}
	return gov, snap, gateResult{}, true
}

// egressDenyReason is the fixed public reason of a legacy-family egress deny.
const egressDenyReason = "server-tool egress denied by policy"

// mixedEgressApprovalReason replaces the adapter's free-form approval reason when a request
// that also declares MCP is denied by a legacy family (Root R-MCP-1).
const mixedEgressApprovalReason = "server-tool egress approval requested for a request that also declares MCP"

// validatedEgressDeny is the one status and error-type mapping of a legacy-family egress deny,
// with or without MCP (Standards review E-2): a status outside 400-599, zero included, becomes
// 403, and an error type outside anthropicErrorTypes becomes the default for the status. A
// valid 4xx/5xx status and its valid error type stand.
func validatedEgressDeny(status int, errType string) (int, string) {
	if status < 400 || status > 599 {
		status = http.StatusForbidden
	}
	if !anthropicErrorTypes[errType] {
		errType = defaultProxyErrorType(status)
	}
	return status, errType
}

// mcpDenial is one row of the closed MCP refusal table (Root correction m1).
type mcpDenial struct {
	status  int
	errType string
	class   sdk.FailureClass
}

var mcpDenials = map[claudeapi.MCPDenialCode]mcpDenial{
	claudeapi.MCPDenyInvalidDeclaration:  {http.StatusBadRequest, "invalid_request_error", sdk.FailureProtocolError},
	claudeapi.MCPDenyOriginNotGranted:    {http.StatusForbidden, "permission_error", sdk.FailurePolicyDeny},
	claudeapi.MCPDenyPolicyDenied:        {http.StatusForbidden, "permission_error", sdk.FailurePolicyDeny},
	claudeapi.MCPDenyCoverageUnavailable: {http.StatusServiceUnavailable, "api_error", sdk.FailureCapabilityUnmet},
	claudeapi.MCPDenyBindingChanged:      {http.StatusInternalServerError, "api_error", sdk.FailureProtocolError},
}

// mcpGateDeny maps a closed MCP code to its fixed status, error type, public reason (the code
// itself), gate code and failure class. An unknown code is invalid coverage.
func mcpGateDeny(code claudeapi.MCPDenialCode) gateResult {
	row, ok := mcpDenials[code]
	if !ok {
		code = claudeapi.MCPDenyCoverageUnavailable
		row = mcpDenials[code]
	}
	return gateDeny(gateCode(code), row.class, row.status, row.errType, string(code))
}

// mcpRefusalCode reports whether err is a connector MCP refusal, and its closed code.
func mcpRefusalCode(err error) (claudeapi.MCPDenialCode, bool) {
	var me *claudeapi.MCPEgressError
	if errors.As(err, &me) {
		return me.Code, true
	}
	return "", false
}

// mcpErrorCode reads the closed code of a connector MCP refusal. Any other error from the MCP
// protocol is coverage the decider cannot establish.
func mcpErrorCode(err error) claudeapi.MCPDenialCode {
	if code, ok := mcpRefusalCode(err); ok {
		return code
	}
	return claudeapi.MCPDenyCoverageUnavailable
}

// checkMCPBinding requires req to carry the session's accepted MCP binding with an unchanged
// MCP projection. A session without an MCP snapshot (no egress gate) has nothing to check.
func checkMCPBinding(sess *proxySession, req claudeapi.MessageRequest) (gateResult, bool) {
	if sess == nil || sess.mcp == nil {
		return gateResult{}, true
	}
	if err := sess.mcp.CheckRequest(req); err != nil {
		return mcpGateDeny(mcpErrorCode(err)), false
	}
	return gateResult{}, true
}

// denyMCPEgress maps a gate deny of an MCP-bearing request (or any deny with an MCP code). A
// deny with no MCP code is a legacy-family deny: its validated status and error type stand
// with the fixed public reason, and its legacy approval notification opens with the adapter's
// identifier-only action, subject and plan hash but Community's fixed reason, on a copy (the
// adapter's intent is not mutated). A deny with a code maps through the closed table; only
// mcp_origin_not_granted with a target this decider can resolve opens a notification, with
// every field derived here. The adapter's top-level Reason, its approval Reason and its
// finding Kind/Title/Detail are never relayed for an MCP-bearing request.
func (d *inferenceProxyDecider) denyMCPEgress(ctx context.Context, tenant model.TenantID, actor, sessionRef string, egDec claudeapi.ServerToolEgressDecision, dests []claudeapi.MCPDestination) gateResult {
	if egDec.MCPDeny == "" {
		if intent := egDec.ApprovalIntent; intent != nil && intent.MCP == nil {
			legacy := *intent
			legacy.Reason = mixedEgressApprovalReason
			d.openEgressApproval(ctx, tenant, actor, &legacy)
		}
		status, errType := validatedEgressDeny(egDec.Status, egDec.ErrorType)
		return gateDeny(gateCodeServerToolEgress, sdk.FailurePolicyDeny, status, errType, egressDenyReason)
	}
	res := mcpGateDeny(egDec.MCPDeny)
	if res.code == gateCode(claudeapi.MCPDenyOriginNotGranted) && egDec.ApprovalIntent != nil && egDec.ApprovalIntent.MCP != nil {
		for _, dst := range dests {
			if dst.ServerIndex == egDec.ApprovalIntent.MCP.ServerIndex {
				d.notifyMCPOrigin(ctx, tenant, actor, sessionRef, dst.Origin)
				break
			}
		}
	}
	return res
}

// anthropicErrorTypes is the closed set of error types a relayed gate deny may carry.
var anthropicErrorTypes = map[string]bool{
	"invalid_request_error": true, "authentication_error": true, "permission_error": true,
	"not_found_error": true, "request_too_large": true, "rate_limit_error": true,
	"api_error": true, "overloaded_error": true, "billing_error": true,
}

func defaultProxyErrorType(status int) string {
	switch {
	case status == http.StatusPaymentRequired:
		return "billing_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status >= 500:
		return "api_error"
	default:
		return "permission_error"
	}
}

// mcpApprovalDomain separates MCP approval plan hashes from every other plan hash.
const mcpApprovalDomain = "olivares.mcp.approval.v1"

// notifyMCPOrigin opens the request-only notification for a missing origin grant. Community
// derives every field: Subject is "mcp-origin:" + hex(SHA-256(origin)); PlanHash is SHA-256
// over the length-framed domain, tenant, the actual approval actor, ActorRef and origin, so a
// different tenant or actor never shares the identity while a different path or token at the
// same origin does (the grant's scope is the origin); the reason names the canonical origin
// only (Root correction m2). No gateOnce, no break-glass, no authority.
func (d *inferenceProxyDecider) notifyMCPOrigin(ctx context.Context, tenant model.TenantID, actor, sessionRef, origin string) {
	if d.approvals == nil {
		return
	}
	requestedBy := firstNonEmpty(actor, model.ActorSystem)
	subject := "mcp-origin:" + hexSHA(origin)
	h := sha256.New()
	for _, part := range []string{mcpApprovalDomain, tenant.String(), requestedBy, sessionRef, origin} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(part))
	}
	plan := hex.EncodeToString(h.Sum(nil))
	reason := "MCP destination origin is not granted for this scope: " + origin
	if err := d.approvals.notify(ctx, tenant, "inference.servertool.egress", "anthropic.server_tool", subject, plan, reason, requestedBy); err != nil && d.log != nil {
		d.log.Warn("inference-proxy: MCP origin approval notification unavailable (deny stands)")
	}
}

// mcpSafeOWASP accepts only well-formed OWASP LLM references on an MCP-bearing request.
var mcpSafeOWASP = regexp.MustCompile(`^LLM[0-9]{2}:[0-9]{4}$`)

// mcpSafeFamilies is the closed family vocabulary a finding on an MCP-bearing request may carry.
var mcpSafeFamilies = map[string]bool{"web_search": true, "web_fetch": true, "code_execution": true, "mcp": true}

// publishEgressFindings turns the gate's posture/forensic findings into bus FindingReports.
// Minimal data: the decider hashes Detail into DetailHash; no prompt or domain value is
// stored. For a request that declares MCP the adapter's free-form Kind, Title, Detail and
// tool type are withheld: the finding uses a fixed template with the severity, a family from
// a closed set and well-formed OWASP references only. A nil bus (d.publish) makes this a
// no-op.
func (d *inferenceProxyDecider) publishEgressFindings(ctx context.Context, tenant model.TenantID, modelRef string, fs []claudeapi.ServerToolEgressFinding, declaresMCP bool) {
	for _, f := range fs {
		sev := sdkmodel.SeverityInfo
		if f.Severity == "high" {
			sev = sdkmodel.SeverityHigh
		}
		report := sdkmodel.FindingReport{
			Kind:        firstNonEmpty(f.Kind, "servertool_egress"),
			Severity:    sev,
			SubjectKind: "anthropic.server_tool",
			SubjectRef:  firstNonEmpty(f.ToolType, f.Family),
			Title:       f.Title,
			DetailHash:  hexSHA(modelRef + "|" + f.Detail),
			OccurredAt:  d.clock().UTC(),
			OWASPLLM:    f.OWASPLLM,
		}
		if declaresMCP {
			family := "servertool"
			if mcpSafeFamilies[f.Family] {
				family = f.Family
			}
			var owasp []string
			for _, ref := range f.OWASPLLM {
				if mcpSafeOWASP.MatchString(ref) {
					owasp = append(owasp, ref)
				}
			}
			report.Kind = "servertool_egress"
			report.SubjectRef = family
			report.Title = "Server-tool egress finding on a request that declares MCP"
			report.DetailHash = hexSHA(modelRef + "|mcp|" + family + "|" + string(sev))
			report.OWASPLLM = owasp
		}
		d.publish(ctx, tenant, report)
	}
}

// openEgressApproval opens a request-only approval NOTIFICATION for a denied legacy-family
// egress via the existing bridge (D4), so a human can grant it. Best-effort +
// nil-safe: no bridge configured ⇒ the deny stands with its HIGH finding only. The proxy is
// synchronous and this result can never authorize the call, so it must not consult or
// consume break-glass nor record an authorization (O-1): notify, never gateOnce.
func (d *inferenceProxyDecider) openEgressApproval(ctx context.Context, tenant model.TenantID, actor string, intent *claudeapi.ServerToolEgressApprovalIntent) {
	if d.approvals == nil || intent == nil {
		return
	}
	requestedBy := firstNonEmpty(actor, model.ActorSystem)
	if err := d.approvals.notify(ctx, tenant, intent.Action, "anthropic.server_tool", intent.Subject, intent.PlanHash, intent.Reason, requestedBy); err != nil && d.log != nil {
		d.log.Warn("inference-proxy: server-tool egress approval notification could not be opened (deny stands)", "err", err)
	}
}
