// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/sdk"
)

// verdict.go decides an admitted tools/call. The gates run in one fixed order:
// strict canonicalization, the Tasks precondition, the tool's entry (resolve,
// scope, role), the decision table, the retained-task reservation, the pin, the
// COAZ policy decision point, the human approval of an ask row, and the MRTR
// answers the call carries. The call ends as a refusal, which refuse records
// and writes, or as a grant, which handleToolsCall claims as evidence and
// dispatches.

// toolCallGrant is a tools/call every gate let through: what the evidence claim
// binds and the dispatch needs.
type toolCallGrant struct {
	canon    canonicalToolCallParams
	policy   ToolPolicy
	verdict  toolVerdict
	pin      pinBinding
	coaz     coazBinding
	approval askApproval
	// ticket holds a retained-task slot until the dispatch consumes or releases it.
	ticket *taskAdmissionTicket
}

// refusal is a request a gate stopped: the decision the audit records and the
// answer the client reads.
type refusal struct {
	record  ToolDecision
	status  int    // HTTP status of the JSON-RPC error
	code    int    // JSON-RPC error code
	message string // non-sensitive message for the client
	// stepUp answers with challengeScope's 403 for this scope (SEP-835) instead
	// of status, code and message.
	stepUp string
	// result answers with this tools/call result instead of an error: the
	// approval round trip.
	result json.RawMessage
	// inputError answers with message as a SEP-1303 tool error (HTTP 200,
	// isError) the model can correct. No gate decided anything, so nothing is
	// recorded.
	inputError bool
}

// refuse records a refusal as best-effort evidence (nothing ran) and writes its
// answer.
func (rs *ResourceServer) refuse(ctx context.Context, w http.ResponseWriter, id json.RawMessage, f *refusal) {
	if f.inputError {
		rs.writeToolError(w, id, f.message)
		return
	}
	rs.auditor.Record(ctx, f.record, sdk.EvidenceBinding{})
	switch {
	case f.result != nil:
		_ = rs.writeResult(w, "tools/call", id, f.result)
	case f.stepUp != "":
		rs.challengeScope(w, id, f.stepUp)
	default:
		rs.writeRPCError(w, f.status, id, f.code, f.message)
	}
}

// gatedCall is a tools/call as far as the gates have read it: the caller, the
// tool, its entry's scope and the decision-table row a refusal records.
type gatedCall struct {
	tok   validatedToken
	trace string
	tool  string
	scope string
	row   toolVerdict
}

// verdictRecord is the audit record of a tools/call decision: the minimal
// decision record with the row, the server and the OAuth client, as the
// contract requires of every decision.
func (rs *ResourceServer) verdictRecord(c gatedCall, allowed bool, reason, approvalRef, mcpTag string) ToolDecision {
	d := rs.decisionRecord(c.tok, c.tool, c.scope, allowed, reason, approvalRef, mcpTag, c.trace)
	d.Decision, d.RuleID, d.Server, d.ClientID = c.row.Decision, c.row.RuleID, rs.resource, c.tok.ClientID
	return d
}

// deny is a refusal of the call by its row.
func (rs *ResourceServer) deny(c gatedCall, reason, mcpTag string, status, code int, message string) *refusal {
	return &refusal{record: rs.verdictRecord(c, false, reason, "", mcpTag), status: status, code: code, message: message}
}

// entryReasons are the audit reasons of the entry gates, which say whether the
// body or the pre-body Mcp-Name check refused.
type entryReasons struct{ unknown, scope, role string }

var (
	bodyEntry = entryReasons{
		unknown: "deny-by-default (tool not in server toolset)",
		scope:   "insufficient scope",
		role:    "caller role not permitted for tool",
	}
	headerEntry = entryReasons{
		unknown: "deny-by-default at L7 (Mcp-Name not in server toolset)",
		scope:   "insufficient scope at L7",
		role:    "caller role not permitted at L7",
	}
)

// admitEntry resolves the call's tool to its server-owned entry and checks the
// caller's scope and role against it. It names the entry's scope and row on c.
func (rs *ResourceServer) admitEntry(c *gatedCall, reasons entryReasons) (ToolPolicy, *refusal) {
	policy, ok := rs.toolset.resolve(c.tool)
	if !ok {
		// Deny-by-default: a tool with no server-owned policy entry (or an explicit
		// deny, or a SEP-986-invalid name) is refused. This closes MCP01/MCP03 (tool
		// poisoning: the gate reads the server toolset, never the tool's annotation).
		c.row = toolVerdict{Decision: DecisionBlock, RuleID: rs.toolset.blockRule(c.tool)}
		return ToolPolicy{}, rs.deny(*c, reasons.unknown, "MCP01", http.StatusForbidden, rpcAccessDenied, "tool not permitted by server policy")
	}
	// The required scope and the allowed roles belong to the tool's entry, so
	// their refusals are a block by that row.
	c.scope, c.row = policy.RequiredScope, toolVerdict{Decision: DecisionBlock, RuleID: "tool:" + c.tool}

	// Scope enforcement (MCP02 scope creep) + step-up (SEP-835): an insufficient scope
	// is a 403 carrying a WWW-Authenticate scope challenge so the client can step up.
	if !c.tok.hasScope(policy.RequiredScope) {
		return ToolPolicy{}, &refusal{record: rs.verdictRecord(*c, false, reasons.scope, "", "MCP02"), stepUp: policy.RequiredScope}
	}

	// Per-role allowlist (E1): a role-restricted tool requires the caller's token to
	// carry one of the tool's AllowedRoles. Deny-closed — an empty or non-matching role set
	// is refused (least-privilege, MCP02). It is NOT a scope step-up (a role is an identity
	// attribute, not a scope the client can request), so it returns a plain 403, not a
	// scope challenge. The Claude MCP API has no native roles; this is the PEP-side layer.
	if !roleAllowed(policy, c.tok.Roles) {
		return ToolPolicy{}, rs.deny(*c, reasons.role, "MCP02", http.StatusForbidden, rpcAccessDenied, "tool not permitted for caller role")
	}
	return policy, nil
}

// decideToolCall runs the gates of an admitted tools/call in their fixed order
// and returns the grant, or the refusal of the first gate that stops the call.
// A grant holds the retained-task reservation; a refusal has released it.
func (rs *ResourceServer) decideToolCall(ctx context.Context, req rsRequest, tok validatedToken, trace string) (toolCallGrant, *refusal) {
	c := gatedCall{tok: tok, trace: trace}

	// (review round-1 P0): STRICT canonicalization is the FIRST thing that
	// touches the params — before the tool name is even resolved. It rejects
	// duplicate object keys at every depth AND case-variant aliases of the reserved
	// keys (name/arguments/_meta/op-key), extracts the operation key and STRIPS it,
	// and produces one deterministic canonical encoding used for the plan hash, the
	// EffectDigest AND the forwarded bytes (the bytes governed are the bytes sent).
	// The gate then reads the tool name from THIS strict tree with exact casing — a
	// case-insensitive struct unmarshal is exactly the smuggling vector (authorize
	// "search", forward "delete_db"). A structural failure is a protocol refusal
	// (400/-32602) BEFORE the claim and before any forward.
	canon, cerr := canonicalizeToolCallParams(req.Params)
	if cerr != nil {
		// No tool name can be read: the row that refuses an unknown tool.
		c.row = toolVerdict{Decision: DecisionBlock, RuleID: rs.toolset.blockRule("")}
		return toolCallGrant{}, rs.deny(c, "tools/call params refused by strict canonicalization (dup/case-alias/malformed keys)", "MCP02",
			http.StatusBadRequest, rpcInvalidParams, "malformed tools/call params (strict decoding refused)")
	}
	if strings.TrimSpace(canon.Name) == "" {
		// A well-formed request missing a string name is a SEP-1303 input failure →
		// HTTP 200 with isError:true, so the model can self-correct (not a protocol
		// error). Structural malformations took the 400 path above.
		return toolCallGrant{}, &refusal{inputError: true, message: "tools/call requires a string 'name'"}
	}
	c.tool = canon.Name

	// K5: Tasks is an optional extension backed by a durable authority. A
	// request that declares it may cause the upstream to create an asynchronous
	// task, so the persistence precondition is checked before any forward. The
	// ordinary standalone gateway remains compatible for non-Tasks calls.
	if canon.DeclaresTasks && rs.durableTasks == nil {
		c.row = toolVerdict{Decision: DecisionBlock, RuleID: "tasks:unavailable"}
		return toolCallGrant{}, rs.deny(c, "MCP Tasks requested without a durable task store; request refused before upstream dispatch", "MCP07",
			http.StatusServiceUnavailable, rpcEvidenceUnavailable, "MCP Tasks is unavailable because durable task persistence is not configured")
	}

	policy, f := rs.admitEntry(&c, bodyEntry)
	if f != nil {
		return toolCallGrant{}, f
	}

	// The decision table (decision.go): the toolset entry and its Cedar argument
	// conditions decide block, ask or allow for this admitted call. A block is
	// final, so it refuses here, before the admission ticket, the pin store and
	// the policy decision point see a call that cannot run.
	verdict := rs.toolset.decide(c.tool, policy, tok.Subject, canon.Args)
	c.row = verdict
	if verdict.Decision == DecisionBlock {
		return toolCallGrant{}, rs.deny(c, verdict.Reason, "MCP02", http.StatusForbidden, rpcAccessDenied, "tool call not permitted by server policy")
	}

	// ROUND-4 R4-05: the RETAINED task-inventory bound, checked BEFORE the forward.
	//
	// Any tools/call may answer with a durable task handle, and once the upstream
	// has created one the gateway has only two choices left: retain it (quarantine
	// deliberately BYPASSES the active caps — forgetting a live external task is the
	// failure being prevented) or forget it (a permanent invisible orphan). Round-3
	// therefore had no bound at all: a caller sitting at its active cap produced one
	// fresh, non-expiring quarantine per call and `byID` grew without limit, making
	// every lookup and every sweep scan an ever-growing map.
	//
	// The bound is enforced at the only point where a NEW task can still be
	// PREVENTED. It is deny-closed and it is not a license to drop anything: the
	// retained records leave only through a proven terminal confirmation or an
	// explicit operator retirement (the tasks/reconcile/* surface).
	//
	// ROUND-5 R5-02: the check RESERVES the slot atomically instead of reading a
	// snapshot. Round-4 read the counts and released the ledger mutex immediately,
	// so N concurrent callers all observed the same pre-forward count and all
	// passed — the reviewer's barrier probe reached 12 retained records under a
	// per-owner cap of 2. The ticket counts against the bound for the WHOLE window
	// in which this call's task may be created, and ends only after that task is
	// stored (consume) or provably absent (release). A refusal below releases it
	// here, panics included; a grant hands it to the dispatch, whose deferred
	// release covers every later path. Ending twice is a no-op.
	ticket, sat, admitted := rs.taskLedger.reserveAdmission(taskOwnerFromToken(rs.tenant, tok))
	if !admitted {
		reason := fmt.Sprintf("retained task inventory saturated (owner %d/%d, gateway %d/%d); task-producing forwards refused until reconciled",
			sat.OwnerRetained, sat.OwnerCap, sat.TotalRetained, sat.TotalCap)
		return toolCallGrant{}, rs.deny(c, reason, "MCP07", http.StatusTooManyRequests, rpcAccessDenied,
			"the retained task inventory is full; reconcile the retained tasks before issuing more calls")
	}
	granted := false
	defer func() {
		if !granted {
			ticket.release()
		}
	}()

	pin, f := rs.verifyPin(ctx, c, canon)
	if f != nil {
		return toolCallGrant{}, f
	}
	coaz, f := rs.evaluateCOAZ(ctx, c, policy)
	if f != nil {
		return toolCallGrant{}, f
	}

	// HITL for an ask row: a plan-bound human approval (authorizeAsk, ask.go).
	var approval askApproval
	if verdict.Decision == DecisionAsk {
		if approval, f = rs.authorizeAsk(ctx, c, req.rc, canon, policy); f != nil {
			return toolCallGrant{}, f
		}
	}

	// Round-1 F-07: the mediator inspects the EXACT-CASED inputResponses
	// member extracted from the strict tree — never a case-insensitive re-parse
	// of the forwarded bytes (a case-folding upstream would consume the other
	// alias). A case-variant alias was already refused by canonicalization.
	if rs.elicitationMediator != nil {
		if mv, reason := rs.evaluateMRTREntries(ctx, tok, ChannelMRTRInputResponse, canon.InputResponses, trace); mv != mediationPass {
			return toolCallGrant{}, rs.mediationRefusal(tok, mv, reason, ChannelMRTRInputResponse, trace)
		}
	}

	granted = true
	return toolCallGrant{canon: canon, policy: policy, verdict: verdict, pin: pin, coaz: coaz, approval: approval, ticket: ticket}, nil
}

// verifyPin is the pin gate: a tool whose definition changed since the
// operator approved it is a rug-pull signal (MCP04). Deny-closed on mismatch or
// error. The gate is additive: when PinVerifier is nil (community build) it is
// skipped and tools/call proceeds exactly as before.
func (rs *ResourceServer) verifyPin(ctx context.Context, c gatedCall, canon canonicalToolCallParams) (pinBinding, *refusal) {
	if rs.pinVerifier == nil {
		return pinBinding{State: "unwired"}, nil
	}
	unavailable := func() *refusal {
		return rs.deny(c, "pin verification error (fail-closed)", "MCP04", http.StatusForbidden, rpcAccessDenied, "tool pin verification unavailable")
	}
	changed := func(reason string) *refusal {
		return rs.deny(c, "pin mismatch: "+reason, "MCP04", http.StatusForbidden, rpcAccessDenied, "tool definition changed since approval (rug-pull detected)")
	}
	// The call-time fingerprint binds the tool name and the CANONICAL governed
	// params (the same bytes the digest binds and the upstream receives).
	// The enterprise verifier stores the full definition fingerprint at
	// introspection time (ToolFingerprint) and can compare against it; the
	// call-time hash tells the verifier WHICH call triggered the check. The
	// call-time hash is NOT bound into the EffectDigest (it is a hash of params
	// the digest already binds); the APPROVED pin identity is, through the
	// returned binding and toolCallPolicyDigest in handleToolsCall.
	fp := toolCallFingerprint(c.tool, canon.Forward)
	if attestor, ok := rs.pinVerifier.(ToolPinVerifyAttestor); ok && attestor != nil {
		// Round-3: the ATOMIC decision+attestation — both produced under
		// ONE pin-store snapshot, so the identity bound into the evidence is
		// exactly the identity that authorized (the removed two-step
		// ApprovedPin/Pins() bridge was a TOCTOU: a re-pin between Verify and a
		// separate attestation read bound an identity that never authorized).
		va, verr := attestor.VerifyAndAttest(ctx, rs.tenant, c.tool, fp)
		if verr != nil {
			return pinBinding{}, unavailable()
		}
		if !va.Allowed {
			return pinBinding{}, changed(va.Reason)
		}
		if !va.Attested {
			return pinBinding{State: "verified"}, nil // no pin at decision time (first-use TOFU)
		}
		// Attested implies a COMPLETE identity: an attestation with empty
		// fields would misstate the evidence — deny-closed.
		if strings.TrimSpace(va.Pin.Fingerprint) == "" || strings.TrimSpace(va.Pin.Version) == "" {
			return pinBinding{}, rs.deny(c, "pin attestation incomplete (fail-closed)", "MCP04", http.StatusForbidden, rpcAccessDenied, "tool pin attestation unavailable")
		}
		return pinBinding{State: "attested", Fingerprint: va.Pin.Fingerprint, Version: va.Pin.Version}, nil
	}
	allowed, pinReason, perr := rs.pinVerifier.Verify(ctx, rs.tenant, c.tool, fp)
	if perr != nil {
		return pinBinding{}, unavailable()
	}
	if !allowed {
		return pinBinding{}, changed(pinReason)
	}
	// No atomic attestation capability: bind posture with EXPLICIT-ABSENT
	// identity markers. Honest absence — never a separate re-read that could
	// bind an identity Verify did not authorize (the round-3 TOCTOU).
	return pinBinding{State: "verified"}, nil
}

// evaluateCOAZ is the gate: it calls the AuthZEN PDP with the COAZ-mapped
// request for centralized, policy-driven MCP tool authorization. The gate is
// ADDITIVE: a nil evaluator (community build) skips it; the toolset, scope,
// role and pin gates all ran first. Deny-closed on error.
func (rs *ResourceServer) evaluateCOAZ(ctx context.Context, c gatedCall, policy ToolPolicy) (coazBinding, *refusal) {
	if rs.coazEvaluator == nil {
		return coazBinding{State: "unwired"}, nil
	}
	coazScopes := make(map[string]struct{}, len(c.tok.Scopes))
	for k, v := range c.tok.Scopes {
		coazScopes[k] = v
	}
	dec, cerr := rs.coazEvaluator.EvaluateToolCall(ctx, COAZRequest{
		Subject:     c.tok.Subject,
		Issuer:      c.tok.Issuer,
		Tool:        c.tool,
		ServerURI:   rs.resource,
		Scopes:      coazScopes,
		Annotations: policy.Annotations,
		Tenant:      rs.tenant,
	})
	if cerr != nil {
		return coazBinding{}, rs.deny(c, "COAZ evaluation error (fail-closed)", "MCP02", http.StatusForbidden, rpcAccessDenied, "tool authorization evaluation unavailable")
	}
	if !dec.Allow {
		return coazBinding{}, rs.deny(c, "COAZ deny: "+dec.Reason, "MCP02", http.StatusForbidden, rpcAccessDenied, "tool not permitted by authorization policy")
	}
	// Bind the consulted-allow posture + the evaluator's STABLE references
	// (round-2: DecisionRef/PolicyVersion — empty = explicit absence). The
	// human-readable Reason text is deliberately NEVER bound (cosmetic edits
	// must not cause false rebinds).
	return coazBinding{State: "allow", DecisionRef: dec.DecisionRef, PolicyVersion: dec.PolicyVersion}, nil
}
