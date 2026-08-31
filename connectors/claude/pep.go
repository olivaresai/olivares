// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
)

// pep.go is the GOVERNED Policy-Enforcement-Point surface for Claude Code's
// PreToolUse/PostToolUse hooks — the half that turns "observe" into "govern". It is
// the sibling of the inline MCP Resource-Server PEP (connectors/mcp): a thin
// PROTOCOL shell that owns the Claude Code hook wire format and the deny-closed
// defaults, and delegates the GOVERNED decision (PDP consult, firm-identity gate,
// ask→HITL, ledger audit — all of which need /core) to a HookDecider seam the
// composition root implements. The connector NEVER imports /core; the seam is the
// boundary (LICENSING.md).
//
// It is DISTINCT from the connector's cooperative-by-default hook telemetry endpoint
// (hooks.go / enforce.go), which evaluates a LOCAL opt-in policy in-process with no
// engine round-trip (read-first, docs/SECURITY-HARDENING.md). This PEP is the opposite posture by
// design: it is the deliberately-interposed, deny-closed enforcement path an operator
// turns on (managed-settings) when the control plane must GOVERN the agent — so a
// missing decider, a decider error or a malformed request all fail CLOSED here, not
// open (docs/SECURITY-HARDENING.md: interposing in the data-path is asymmetric risk → deny-closed,
// governed, audited).
//
// Wire contract (verified against code.claude.com/docs/en/hooks, 2026-06-09):
//   - PreToolUse  → hookSpecificOutput.permissionDecision ∈ allow|deny|ask|defer, with
//     permissionDecisionReason, optional updatedInput (the governed REWRITE) and
//     optional additionalContext. Precedence is deny > defer > ask > allow.
//   - PostToolUse → there is NO output-rewrite field in Claude Code; a PostToolUse hook
//     can only block further processing (top-level decision:"block" + reason) and add
//     additionalContext. So "PostToolUse redaction" here is HONEST: the PEP redacts what
//     IT retains/audits (minimal data, never raw secrets/PII) and can BLOCK on a
//     policy-flagged output — it does not pretend to rewrite the tool result in-band.

// Governed permission-decision values the HookDecider returns. They are the connector's
// public mirror of the internal permAllow/permDeny/permAsk constants, so the AGPL
// composition root can name a decision without importing the connector's internals.
const (
	DecisionAllow = permAllow
	DecisionDeny  = permDeny
	DecisionAsk   = permAsk
)

// Identity-hint headers the managed hook client stamps from its environment so the PEP
// can attribute a decision to a firm agent identity even though the Claude Code
// PreToolUse payload itself carries no org/account/agent. They are HINTS (advisory): the
// AUTHORITATIVE firm identity is the bearer principal the decider resolves; a hint that
// disagrees with — or is absent under — a policy that requires firm identity denies.
const (
	hdrHookTenant  = "X-Olivares-Hook-Tenant"
	hdrHookAgent   = "X-Olivares-Hook-Agent"
	hdrHookOrg     = "X-Olivares-Hook-Org"
	hdrHookAccount = "X-Olivares-Hook-Account"
)

// HookIdentity is the firm-identity attribution context for a tool-call: the
// tenant the call belongs to and the agent/org/account hints. The decider resolves the
// AUTHORITATIVE principal from the bearer credential; these refine attribution and let
// a policy bind a rule to a specific agent.
type HookIdentity struct {
	Tenant  string
	Agent   string
	Org     string
	Account string
}

// HookDecisionInput is the redacted, minimal-data context the governed decider sees for
// one tool-call (docs/SECURITY-HARDENING.md). It carries NO raw tool arguments — only the derived,
// sanitized resource reference and structural fields. The raw tool input is held
// privately and used ONLY to compute a governed rewrite (updatedInput) when the decider
// asks for one; it is never stored, logged or audited.
type HookDecisionInput struct {
	Event        string // PreToolUse | PostToolUse | PermissionRequest
	SessionID    string
	Tool         string
	ToolUseID    string
	ResourceKind string // file | shell | http.url | web.search | mcp.tool | agent.task | claude.tool
	ResourceRef  string // sanitized (no secrets), bounded
	Mode         string // read | write | unknown
	// PlanHash binds the decision to the EXACT tool-call (anti-TOCTOU): a re-planned
	// call hashes differently, so an approval bound to this hash does not authorize a
	// different one. It is a fingerprint of (tool, resourceKind, resourceRef, mode), not
	// a secret.
	PlanHash string
	Identity HookIdentity
	At       time.Time

	// rawInput is the untrusted tool_input. It is unexported so it can never be audited;
	// the decider reads it only via RewriteBase to compute a sanitized replacement.
	rawInput map[string]any
}

// RewriteBase returns a shallow copy of the original tool input for the decider to base
// a governed rewrite on (e.g. set "--dry-run", narrow a path). It is a copy, so the
// decider cannot mutate the connector's parsed payload, and it is the ONLY accessor to
// the raw input — keeping raw arguments out of every audited/logged path.
func (in HookDecisionInput) RewriteBase() map[string]any {
	if in.rawInput == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in.rawInput))
	for k, v := range in.rawInput {
		out[k] = v
	}
	return out
}

// HookDecisionResult is the governed verdict for a tool-call. Its zero value is a DENY
// (empty Permission renders deny-closed), so a decider that returns a zero value on a
// path it forgot still fails closed.
type HookDecisionResult struct {
	Permission        string         // DecisionAllow | DecisionDeny | DecisionAsk
	Reason            string         // short, non-sensitive explanation surfaced to the user/agent
	PolicyVersion     string         // the active policy version that decided (for the receipt)
	UpdatedInput      map[string]any // PreToolUse governed rewrite (updatedInput); nil = none
	AdditionalContext string         // optional context appended to the model's conversation
	Block             bool           // PostToolUse: block further processing (decision:"block")
	// ContinueOnBlock softens a PostToolUse Block (VERIFIED 2026-06-10;
	// changelog 2.1.139): the reason is fed back to Claude and the TURN CONTINUES
	// instead of ending (the wire form is "continue": true alongside
	// decision:"block" — the same mechanism prompt hooks' continueOnBlock config
	// uses). The zero value (false) is the STRICTEST behavior: omission == hard
	// block. It is only meaningful with Block on PostToolUse; it NEVER rides a
	// synthetic deny-closed verdict, and PostToolBatch/UserPromptSubmit end the
	// turn regardless of continue (per the verified per-event semantics).
	ContinueOnBlock bool
	// PrincipalActor is the resolved real principal ("token:…" / "user:…") the decision
	// is attributed to in the ledger (docs/SECURITY-HARDENING.md action→identity). Empty = unresolved.
	PrincipalActor string
	// IdentityTier is the firm-attribution tier the decider resolved: firm |
	// approximate | unknown. It is recorded so the audit trail is honest about how firmly
	// the action is attributed.
	IdentityTier string
}

// HookDecider is the GOVERNED decision seam (the /core boundary). The composition root
// implements it against the live PDP (Cedar/ABAC), the firm-identity plane, the
// HITL approval bridge and the tamper-evident ledger. bearer is the inbound
// credential the decider resolves to a real principal; it is opaque to the connector and
// MUST NOT be logged or stored. A nil decider, or any returned error, is treated as a
// DENY by the PEP (deny-closed).
type HookDecider interface {
	Decide(ctx context.Context, in HookDecisionInput, bearer string) (HookDecisionResult, error)
}

// HookAuditor records each governed decision (minimal data) for the SOC trail. The
// HITL/ask path additionally self-audits to the hash-chain ledger via the bridge
// (the decider opens the approval); this captures the allow/deny of every call. It never
// receives the bearer credential or the raw tool input. Optional (nil = no SOC log).
type HookAuditor interface {
	Record(ctx context.Context, in HookDecisionInput, res HookDecisionResult, denyClosed bool)
}

// maxPEPBody bounds a hook request body. A PreToolUse payload is small; tool_input is
// the only unbounded part and only a few fields are read from it.
const maxPEPBody = 1 << 20 // 1 MiB

// HookPEP is the governed PreToolUse/PostToolUse enforcement endpoint. It owns the wire
// protocol and the deny-closed defaults; the decision is the injected HookDecider's.
type HookPEP struct {
	decider HookDecider
	auditor HookAuditor
	now     func() time.Time
}

var _ http.Handler = (*HookPEP)(nil)

// NewHookPEP builds the governed hook PEP. A nil decider is allowed (the endpoint then
// denies every call — a visible deny-closed posture, never a silent open door); a nil
// clock uses time.Now.
func NewHookPEP(decider HookDecider, auditor HookAuditor, now func() time.Time) *HookPEP {
	if now == nil {
		now = time.Now
	}
	return &HookPEP{decider: decider, auditor: auditor, now: now}
}

// ServeHTTP gates one Claude Code hook call. POST only; any malformed request answers a
// deny (it is the governed surface — a request it cannot understand is blocked, never
// waved through). The response is the Claude Code hook decision the agent enforces.
func (p *HookPEP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPEPBody))
	if err != nil {
		p.writeDeny(w, hookPreToolUse, "could not read hook request body")
		return
	}
	ev, ok := parseHook(body, p.now())
	if !ok {
		// Cannot identify the tool-call: deny-closed (we will not guess a tool we cannot
		// see). Default the event to PreToolUse so the agent reads a permission deny.
		p.writeDeny(w, hookPreToolUse, "malformed hook payload (no session/tool to attribute)")
		return
	}

	in := p.inputFrom(ev, r)

	if p.decider == nil {
		// The governed surface is mounted without a decider: refuse rather than wave
		// through. This is unreachable in production wiring (the composition root only
		// mounts the PEP WITH a decider), but it makes the deny-closed contract total.
		res := HookDecisionResult{Permission: DecisionDeny, Reason: "governed enforcement is not wired (deny-closed)"}
		p.audit(r.Context(), in, res, true)
		p.write(w, in, res)
		return
	}

	res, derr := p.decider.Decide(r.Context(), in, bearerToken(r))
	if derr != nil {
		// Any decision error — PDP unreachable, identity unresolved, HITL open failed —
		// is a DENY (docs/SECURITY-HARDENING.md). The reason is non-sensitive (never the raw error).
		deny := HookDecisionResult{Permission: DecisionDeny, Reason: "governed decision unavailable (deny-closed)"}
		p.audit(r.Context(), in, deny, true)
		p.write(w, in, deny)
		return
	}
	p.audit(r.Context(), in, res, false)
	p.write(w, in, res)
}

// inputFrom builds the redacted decision context from a parsed hook event and the
// request headers (identity hints). The resource is derived and sanitized at ingest
// (resourceFromTool), and the plan hash binds the exact tool-call.
func (p *HookPEP) inputFrom(ev hookEvent, r *http.Request) HookDecisionInput {
	kind, ref, mode := resourceFromTool(ev.toolName, ev.input)
	return HookDecisionInput{
		Event:        ev.event,
		SessionID:    ev.sessionID,
		Tool:         ev.toolName,
		ToolUseID:    ev.toolUseID,
		ResourceKind: kind,
		ResourceRef:  ref,
		Mode:         string(mode),
		PlanHash:     planHash(ev.toolName, kind, ref, string(mode), ev.sessionID, ev.input),
		Identity: HookIdentity{
			Tenant:  strings.TrimSpace(r.Header.Get(hdrHookTenant)),
			Agent:   strings.TrimSpace(r.Header.Get(hdrHookAgent)),
			Org:     strings.TrimSpace(r.Header.Get(hdrHookOrg)),
			Account: strings.TrimSpace(r.Header.Get(hdrHookAccount)),
		},
		At:       ev.at,
		rawInput: ev.input,
	}
}

// planHash fingerprints the exact tool-call for the anti-TOCTOU approval binding. It
// folds the structural, already-derived fields (tool/kind/ref/mode) TOGETHER with the
// session and a canonical digest of the raw tool_input — so two calls that differ only
// in their ARGUMENTS (same tool/kind/ref/mode) no longer collapse to one hash, and a
// human approval bound to one call can never be replayed to authorize a materially
// different one (F-02). The digest is over Go's json.Marshal of the input map,
// whose object keys are emitted sorted, so it is STABLE across retries of the SAME call
// yet DISTINCT for any different argument set (or session). The raw arguments never
// appear in the hash — only their digest — so it carries no secret. An input that cannot
// be marshaled fails closed to a distinct, non-matching digest.
func planHash(tool, kind, ref, mode, session string, input map[string]any) string {
	argsDigest := "none"
	if len(input) > 0 {
		if b, err := json.Marshal(input); err == nil {
			argsDigest = redact.Hash(string(b))
		} else {
			argsDigest = "unmarshalable-input"
		}
	}
	// Encode the fields as a JSON array so no field value (a path in ref, a program in
	// mode) can forge the separator and collapse onto a different field split. json.Marshal
	// of a []string never fails, so the error is safely discarded.
	pre, _ := json.Marshal([]string{"claude.tool.v2", tool, kind, ref, mode, session, argsDigest})
	return redact.Hash(string(pre))
}

// write renders the governed decision in the Claude Code hook wire format for the event.
func (p *HookPEP) write(w http.ResponseWriter, in HookDecisionInput, res HookDecisionResult) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(p.render(in, res))
}

// writeDeny answers a bare deny for the given event (used before a decider runs).
func (p *HookPEP) writeDeny(w http.ResponseWriter, event, reason string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(p.render(HookDecisionInput{Event: event}, HookDecisionResult{Permission: DecisionDeny, Reason: reason}))
}

// render serializes the governed verdict in the wire mechanism the event HONORS (hooks.go
// hookSpecs, the single source of truth; VERIFIED 2026-06-19, code.claude.com/docs/en/hooks).
// Emitting the wrong shape means the deny is silently IGNORED, so the mechanism is part of
// the security contract:
//
//   - mechNeutral → context/observe events AND the INVERTED Stop/SubagentStop: never a
//     block (a "block" on Stop KEEPS the agent running). Neutral, carrying additionalContext
//     when the decider supplied feedback.
//   - mechPostToolUse → PostToolUse decision:"block" on a flagged output, softened by
//     continue:true (2.1.139).
//   - mechTopLevelDecision → top-level decision:"block" + reason (UserPromptSubmit,
//     UserPromptExpansion, PreCompact, ConfigChange, PostToolBatch).
//   - mechContinueFalse → continue:false + stopReason (TaskCreated, TaskCompleted,
//     TeammateIdle).
//   - mechPermissionDecision / mechPermissionBehavior → PreToolUse (permissionDecision) and
//     PermissionRequest (decision.behavior), via hookDecision.json(); an UNKNOWN event stays
//     on the permissionDecision deny-closed path BY DESIGN.
func (p *HookPEP) render(in HookDecisionInput, res HookDecisionResult) []byte {
	return renderHookDecision(in.Event, res)
}

// renderHookDecision serializes a verdict for an event in the wire mechanism it honors. It is
// the SINGLE source of truth for hook-decision encoding, shared by the governed PEP's render
// and the managed client's fail-closed denyClosedDecision — so the two never drift (a deny in
// the wrong shape is silently ignored).
func renderHookDecision(event string, res HookDecisionResult) []byte {
	switch hookMechFor(event) {
	case mechNeutral:
		return neutralContextJSON(event, res)
	case mechPostToolUse:
		return postToolUseJSON(res)
	case mechTopLevelDecision:
		return topLevelDecisionJSON(event, res)
	case mechContinueFalse:
		return continueFalseJSON(event, res)
	}
	// mechPermissionDecision (PreToolUse + UNKNOWN) / mechPermissionBehavior (PermissionRequest).
	perm := res.Permission
	if !permissionValueValid(event, perm) {
		perm = permDeny // deny-closed on an unknown/empty verdict
	}
	d := hookDecision{
		event:             event,
		permission:        perm,
		reason:            res.Reason,
		updatedInput:      res.UpdatedInput,
		additionalContext: res.AdditionalContext,
	}
	return d.json()
}

// neutralContextJSON renders a NON-blocking verdict: the only meaningful output is
// hookSpecificOutput.additionalContext (when the decider supplied feedback), else the
// neutral "{}". It serves the inverted Stop/SubagentStop, the context/observe events, and
// the allow path of the top-level-decision / continue-false events. The client caps hook
// output strings at 10,000 chars, spilling overflow to a file; the cap is the client's.
func neutralContextJSON(event string, res HookDecisionResult) []byte {
	if res.AdditionalContext == "" {
		return emptyHookResponse
	}
	b, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     event,
			"additionalContext": res.AdditionalContext,
		},
	})
	if err != nil {
		return emptyHookResponse
	}
	return b
}

// topLevelDecisionJSON renders a verdict for an event that blocks via the TOP-LEVEL
// decision:"block" + reason shape (UserPromptSubmit, UserPromptExpansion, PreCompact,
// ConfigChange, PostToolBatch). A non-allow verdict (deny, or an unresolved ask — these
// events have no interactive ask UI, so a pending HITL is deny-closed) BLOCKS; an allow is
// neutral, carrying additionalContext when supplied.
func topLevelDecisionJSON(event string, res HookDecisionResult) []byte {
	if res.Permission == permAllow {
		return neutralContextJSON(event, res)
	}
	out := map[string]any{"decision": "block"}
	if res.Reason != "" {
		out["reason"] = res.Reason
	}
	b, err := json.Marshal(out)
	if err != nil {
		return emptyHookResponse
	}
	return b
}

// continueFalseJSON renders a verdict for an event that blocks via continue:false +
// stopReason (TaskCreated, TaskCompleted, TeammateIdle — these do NOT honor a top-level
// decision:"block"). A non-allow verdict halts the action; an allow is neutral.
func continueFalseJSON(event string, res HookDecisionResult) []byte {
	if res.Permission == permAllow {
		return neutralContextJSON(event, res)
	}
	out := map[string]any{"continue": false}
	if res.Reason != "" {
		out["stopReason"] = res.Reason
	}
	b, err := json.Marshal(out)
	if err != nil {
		return emptyHookResponse
	}
	return b
}

// postToolUseJSON renders a PostToolUse verdict. Claude Code cannot rewrite a tool result
// that already ran (verified: there is no updatedOutput field); the connector can only
// BLOCK further processing on a policy-flagged output and add context. The actual
// redaction is of what the PEP RETAINS/AUDITS (minimal-data, in the auditor), not of
// what the model already saw.
func postToolUseJSON(res HookDecisionResult) []byte {
	out := map[string]any{}
	// A governed POLICY deny (or ask, or an empty/invalid verdict) must BLOCK further
	// processing of the flagged output, not just an explicit allow-path Block flag — else a
	// deny-closed default / PDP forbid / deny rule on PostToolUse would render the neutral
	// "{}" and fail OPEN (the connector's own fail-closed denyClosedDecision blocks here too).
	if res.Block || res.Permission != DecisionAllow {
		out["decision"] = "block"
		if res.Reason != "" {
			out["reason"] = res.Reason
		}
		// continueOnBlock (2.1.139): feed the reason back to Claude and continue the
		// turn instead of ending it. It softens a flagged-output block; a policy DENY is a
		// HARD block (never softened), and the deny-closed synthetic verdicts construct bare
		// results (ContinueOnBlock=false) — so a deny can never carry it.
		if res.ContinueOnBlock && res.Permission != DecisionDeny {
			out["continue"] = true
		}
	}
	if res.AdditionalContext != "" {
		out["hookSpecificOutput"] = map[string]any{
			"hookEventName":     hookPostToolUse,
			"additionalContext": res.AdditionalContext,
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return emptyHookResponse
	}
	return b
}

// audit records the decision via the auditor (when wired), never the bearer or raw input.
func (p *HookPEP) audit(ctx context.Context, in HookDecisionInput, res HookDecisionResult, denyClosed bool) {
	if p.auditor != nil {
		p.auditor.Record(ctx, in, res, denyClosed)
	}
}

// bearerToken extracts the inbound bearer credential (the agent's PEP token). It is
// returned to the decider for principal resolution and never retained by the connector.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// --- permissionPromptToolName route -----------------------------------------
//
// The Agent SDK option `permissionPromptToolName` ("MCP tool name for permission
// prompts") names an MCP tool the SDK calls to resolve a permission prompt — the
// MCP-delegate of the in-process `canUseTool` callback, occupying the SAME step-6
// fall-through of the permission flow (verified: code.claude.com/docs/en/agent-sdk/
// {permissions,user-input,typescript}, 2026-06-19). Pointing it at THIS route makes every
// permission request a customer-owned SDK-built agent raises pass through the SAME
// governed HookDecider the PreToolUse PEP uses — so a customer's own agent FLEET is
// governed by the control plane, not just the Claude Code that Olivares launches. It is
// the governed destination an operator should set permissionPromptToolName to.
//
// HONESTY (docs/SECURITY-HARDENING.md): this route emits the VERIFIED PermissionResult decision payload —
// {"behavior":"allow","updatedInput":…} | {"behavior":"deny","message":…,"interrupt":…} —
// which is the documented canUseTool return shape. The MCP tool-result ENVELOPE the SDK
// wraps a permission-prompt tool's reply in is NOT published by Anthropic; we model only
// the decision payload we can verify, and the serving MCP-tool layer applies the envelope.
// Enforcement is VERIFIED-DEPLOYED, NOT impossible to bypass: a custom ANTHROPIC_BASE_URL
// routes the SDK program's traffic past this route entirely.

// eventPermissionPrompt labels the decision context's origin as the permissionPromptToolName
// route (distinct from the PreToolUse/PermissionRequest hooks) for the audit trail.
const eventPermissionPrompt = "PermissionPrompt"

// permissionPromptInput is the tool-call a permissionPromptToolName resolver receives. The
// wire envelope is unpublished, so both the camelCase (SDK canUseTool) and snake_case
// (PermissionRequest hook) spellings of each field are accepted. Raw input is never stored
// — it is reduced to a sanitized resource at ingest, exactly as the PreToolUse PEP does.
type permissionPromptInput struct {
	toolName  string
	toolInput map[string]any
	toolUseID string
	sessionID string
}

// parsePermissionPrompt decodes a permission-prompt request. It returns false when the JSON
// is invalid or carries no tool name (nothing to attribute → cannot govern → deny-closed).
func parsePermissionPrompt(body []byte) (permissionPromptInput, bool) {
	var p struct {
		ToolName   string         `json:"tool_name"`
		ToolNameC  string         `json:"toolName"`
		ToolInput  map[string]any `json:"tool_input"`
		Input      map[string]any `json:"input"`
		ToolUseID  string         `json:"tool_use_id"`
		ToolUseIDC string         `json:"toolUseId"`
		SessionID  string         `json:"session_id"`
		SessionIDC string         `json:"sessionId"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return permissionPromptInput{}, false
	}
	in := permissionPromptInput{
		toolName:  firstNonEmpty(p.ToolName, p.ToolNameC),
		toolUseID: firstNonEmpty(p.ToolUseID, p.ToolUseIDC),
		sessionID: firstNonEmpty(p.SessionID, p.SessionIDC),
		toolInput: p.ToolInput,
	}
	if in.toolInput == nil {
		in.toolInput = p.Input
	}
	if in.toolName == "" {
		return permissionPromptInput{}, false
	}
	return in, true
}

// ServePermissionPrompt gates one Agent SDK permissionPromptToolName request. It is
// the permission-prompt analog of ServeHTTP: POST only; a malformed request, a missing
// decider or a decider error all answer a DENY (deny-closed, docs/SECURITY-HARDENING.md). The response is
// the PermissionResult the SDK enforces.
func (p *HookPEP) ServePermissionPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPEPBody))
	if err != nil {
		p.writePermissionPromptDeny(w, "could not read permission-prompt request body")
		return
	}
	pin, ok := parsePermissionPrompt(body)
	if !ok {
		p.writePermissionPromptDeny(w, "malformed permission-prompt payload (no tool to attribute)")
		return
	}
	in := p.permissionPromptInputFrom(pin, r)

	if p.decider == nil {
		res := HookDecisionResult{Permission: DecisionDeny, Reason: "governed enforcement is not wired (deny-closed)"}
		p.audit(r.Context(), in, res, true)
		p.writePermissionPrompt(w, res)
		return
	}
	res, derr := p.decider.Decide(r.Context(), in, bearerToken(r))
	if derr != nil {
		deny := HookDecisionResult{Permission: DecisionDeny, Reason: "governed decision unavailable (deny-closed)"}
		p.audit(r.Context(), in, deny, true)
		p.writePermissionPrompt(w, deny)
		return
	}
	p.audit(r.Context(), in, res, false)
	p.writePermissionPrompt(w, res)
}

// permissionPromptInputFrom builds the redacted decision context from a permission-prompt
// request: the resource is derived/sanitized at ingest and the plan hash binds the exact
// tool-call, exactly as inputFrom does for a hook. Event is eventPermissionPrompt.
func (p *HookPEP) permissionPromptInputFrom(pin permissionPromptInput, r *http.Request) HookDecisionInput {
	kind, ref, mode := resourceFromTool(pin.toolName, pin.toolInput)
	return HookDecisionInput{
		Event:        eventPermissionPrompt,
		SessionID:    pin.sessionID,
		Tool:         pin.toolName,
		ToolUseID:    pin.toolUseID,
		ResourceKind: kind,
		ResourceRef:  ref,
		Mode:         string(mode),
		PlanHash:     planHash(pin.toolName, kind, ref, string(mode), pin.sessionID, pin.toolInput),
		Identity: HookIdentity{
			Tenant:  strings.TrimSpace(r.Header.Get(hdrHookTenant)),
			Agent:   strings.TrimSpace(r.Header.Get(hdrHookAgent)),
			Org:     strings.TrimSpace(r.Header.Get(hdrHookOrg)),
			Account: strings.TrimSpace(r.Header.Get(hdrHookAccount)),
		},
		At:       p.now(),
		rawInput: pin.toolInput,
	}
}

// writePermissionPrompt renders the governed verdict in the verified PermissionResult wire
// shape. An ASK (HITL pending/unresolved) maps to a DENY with an explanatory message: the
// permission-prompt tool is BINARY (allow|deny), so a not-yet-approved governed decision is
// deny-closed, never a silent allow.
func (p *HookPEP) writePermissionPrompt(w http.ResponseWriter, res HookDecisionResult) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(permissionPromptJSON(res))
}

// writePermissionPromptDeny answers a bare deny (used before a decider runs).
func (p *HookPEP) writePermissionPromptDeny(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(permissionPromptDenyBytes(reason))
}

// permissionPromptJSON marshals a HookDecisionResult to the verified PermissionResult wire
// shape ({"behavior":"allow",…} | {"behavior":"deny",…}). Only an explicit allow yields
// "allow" (carrying a governed updatedInput rewrite when present); every other verdict —
// deny, ask (pending HITL), or an empty/invalid permission — is deny-closed.
func permissionPromptJSON(res HookDecisionResult) []byte {
	if res.Permission == DecisionAllow {
		out := map[string]any{"behavior": "allow"}
		if len(res.UpdatedInput) > 0 {
			out["updatedInput"] = res.UpdatedInput
		}
		b, err := json.Marshal(out)
		if err != nil {
			return permissionPromptDenyBytes("could not encode allow decision")
		}
		return b
	}
	reason := res.Reason
	if reason == "" {
		reason = "denied by Olivares governance (deny-closed)"
	}
	if res.Permission == DecisionAsk {
		// The SDK permission-prompt tool cannot represent "pending"; an unresolved HITL is
		// surfaced honestly as a deny with its reason, never a silent allow.
		reason = "human approval required and not yet granted (deny-closed): " + reason
	}
	return permissionPromptDenyBytes(reason)
}

// permissionPromptDenyBytes renders a PermissionResult deny. interrupt is false (block this
// one tool-call and let Claude adjust, the cooperative default — never kill the session).
func permissionPromptDenyBytes(message string) []byte {
	b, err := json.Marshal(map[string]any{"behavior": "deny", "message": message, "interrupt": false})
	if err != nil {
		return []byte(`{"behavior":"deny","message":"deny-closed","interrupt":false}`)
	}
	return b
}
