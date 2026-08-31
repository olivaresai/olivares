// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// claudehookpep_test.go is the E2E proof for the governed Claude Code hooks PEP
// drives the REAL engine the binary boots — the real authenticator (firm identity), the
// real approval engine via the bridge (ask→HITL), and a live PDP overlay — and
// turns "observe" into "govern". It proves allow/deny/ask/rewrite, the deny-closed edges
// (no policy, unknown identity, no HITL bridge), the firm-identity gate, the PDP
// hard-deny overlay, and the full HITL loop (pending→approved→allow) where a
// human approval flips the verdict. No guardrail is weakened to pass a test.

// fixedEval is a stand-in PDP overlay: it allows or forbids every request, proving the
// PEP consults the evaluator and that a forbid is a hard deny regardless of disposition.
type fixedEval struct{ allow bool }

func (e fixedEval) Evaluate(_ context.Context, _ auth.Request) (auth.Decision, error) {
	return auth.Decision{Allow: e.allow, Reason: "test pdp"}, nil
}

type erroringEval struct{ err error }

func (e erroringEval) Evaluate(_ context.Context, _ auth.Request) (auth.Decision, error) {
	return auth.Decision{}, e.err
}

type failingAuthenticator struct{ err error }

func (a *failingAuthenticator) Authenticate(_ context.Context, _ string) (auth.Principal, error) {
	return auth.Principal{}, a.err
}

// hookPEPFixture wires a governed decider over the harness's real engine.
type hookPEPFixture struct {
	h      *harness
	pep    *claude.HookPEP
	dec    *claudeHookDecider
	tenant model.TenantID
}

// newHookPEPFixture builds the PEP for tenant A with the given policy, PDP overlay and an
// optional real bridge (proposing as the admin/superadmin service token). requireFirm
// makes the deny-closed posture explicit.
func newHookPEPFixture(t *testing.T, h *harness, pol hookPolicyDoc, requireFirm bool, eval auth.PolicyEvaluator, withBridge bool) *hookPEPFixture {
	t.Helper()
	tid, err := model.ParseTenantID(h.tenantA)
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	dec := &claudeHookDecider{
		tenants: map[model.TenantID]resolvedTenant{
			tid: {tenant: tid, requireFirm: requireFirm, policy: pol},
		},
		authr: auth.NewAuthenticator(h.st, nil),
		eval:  eval,
		store: h.st,
		clock: time.Now,
		log:   discardLog(),
	}
	if withBridge {
		br := newApprovalBridge(approvalBridgeConfig{
			Tenants: []approvalBridgeTenant{{Tenant: h.tenantA, Token: h.adminToken}},
		}, discardLog())
		if br == nil {
			t.Fatal("approval bridge should build")
		}
		br.useHandler(h.h)
		dec.bridge = br
	}
	return &hookPEPFixture{
		h:      h,
		pep:    claude.NewHookPEP(dec, claudeHookAuditor{log: discardLog()}, time.Now),
		dec:    dec,
		tenant: tid,
	}
}

// call drives one PreToolUse decision through the PEP and returns the hookSpecificOutput.
func (f *hookPEPFixture) call(t *testing.T, tool string, input map[string]any, token, tenant string) map[string]any {
	t.Helper()
	return f.event(t, "PreToolUse", tool, input, token, tenant)
}

func (f *hookPEPFixture) event(t *testing.T, event, tool string, input map[string]any, token, tenant string) map[string]any {
	t.Helper()
	payload := map[string]any{
		"session_id": "sess-e2e", "hook_event_name": event,
		"tool_name": tool, "tool_use_id": "tu-e2e", "tool_input": input,
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant != "" {
		req.Header.Set("X-Olivares-Hook-Tenant", tenant)
	}
	rec := httptest.NewRecorder()
	f.pep.ServeHTTP(rec, req)
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response not JSON: %q", rec.Body.String())
	}
	if hso, ok := m["hookSpecificOutput"].(map[string]any); ok {
		return hso
	}
	return m
}

func decisionOf(m map[string]any) string {
	if v, ok := m["permissionDecision"].(string); ok {
		return v
	}
	return ""
}

// reasonOf returns the hook's permissionDecisionReason so a test can pin WHY the PEP
// refused, not just that it did. The verdict alone cannot carry that: identity, policy,
// firewall and fallback denials all render the same "deny".
func reasonOf(m map[string]any) string {
	if v, ok := m["permissionDecisionReason"].(string); ok {
		return v
	}
	return ""
}

// firmAgentToken returns a real per-tenant credential (a member of tenant A) — a firm
// identity the PEP can attribute the tool-call to.
func (h *harness) firmAgentToken(t *testing.T, email string) string {
	t.Helper()
	_, tok := h.createApprover(t, email)
	return tok
}

// --- full hook-event enforcement (per-event default posture + event rules) -------

// TestHookPEP_ConfigChangeMutationDefaultDeny: a state-MUTATING gating event with no rule
// defaults DENY (deny-closed), rendered as the top-level decision:"block" it honors.
func TestHookPEP_ConfigChangeMutationDefaultDeny(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "ops@a.test")
	f := newHookPEPFixture(t, h, hookPolicyDoc{}, false, fixedEval{allow: true}, false)
	m := f.event(t, "ConfigChange", "", nil, tok, h.tenantA)
	if m["decision"] != "block" {
		t.Fatalf("ConfigChange with no rule must default DENY (decision:block); got %v", m)
	}
}

// TestHookPEP_UXGatingEventDefaultNeutral: a UX/agent-loop gating event defaults NEUTRAL —
// even under a deny-closed, deny-all-tools policy (blocking every prompt would break the
// session) — while the SAME policy still denies a real tool call.
func TestHookPEP_UXGatingEventDefaultNeutral(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "ops@a.test")
	pol := hookPolicyDoc{Default: "deny", Rules: []hookPolicyRule{{Tool: "*", Decision: "deny"}}}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, false)
	m := f.event(t, "UserPromptSubmit", "", nil, tok, h.tenantA)
	if m["decision"] == "block" {
		t.Fatalf("UserPromptSubmit must default NEUTRAL (an event-less tool rule must not gate it); got %v", m)
	}
	pm := f.call(t, "Bash", map[string]any{"command": "rm"}, tok, h.tenantA)
	if decisionOf(pm) != "deny" {
		t.Fatalf("the deny-all tool rule must still deny a PreToolUse Bash; got %v", pm)
	}
}

// TestHookPEP_EventTargetedRules: an event-targeted rule decides its event (deny blocks,
// allow passes), independent of the tool-rule path.
func TestHookPEP_EventTargetedRules(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "ops@a.test")
	pol := hookPolicyDoc{Rules: []hookPolicyRule{
		{Event: "UserPromptSubmit", Decision: "deny", Reason: "prompts disabled"},
		{Event: "ConfigChange", Decision: "allow"},
	}}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, false)

	ups := f.event(t, "UserPromptSubmit", "", nil, tok, h.tenantA)
	if ups["decision"] != "block" || ups["reason"] == "" {
		t.Fatalf("an explicit deny rule for UserPromptSubmit must block; got %v", ups)
	}
	cc := f.event(t, "ConfigChange", "", nil, tok, h.tenantA)
	if cc["decision"] == "block" {
		t.Fatalf("an explicit allow rule for ConfigChange must NOT block (overrides the mutation default); got %v", cc)
	}
	// TaskCreated has no rule → neutral default; render is continue:false ONLY on a deny.
	tc := f.event(t, "TaskCreated", "", nil, tok, h.tenantA)
	if _, blocked := tc["continue"]; blocked {
		t.Fatalf("TaskCreated with no rule must default NEUTRAL; got %v", tc)
	}
}

// TestHookPEP_NonEnforceableEventsNeutralVerdict proves the decider SHORT-CIRCUITS the
// non-enforceable events (context/observe + the inverted Stop/SubagentStop) to a NEUTRAL
// allow — bypassing even a deny-all policy AND a PDP hard-forbid — because the PEP cannot
// (and must not) block them.
// TestHookPEP_PostToolUseDenyClosedBlocks: a deny-closed tenant (default:"deny") must BLOCK a
// PostToolUse with no allow rule — it is a ClassicGate, so it must not silently fail open.
func TestHookPEP_PostToolUseDenyClosedBlocks(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "ops@a.test")
	f := newHookPEPFixture(t, h, hookPolicyDoc{Default: "deny"}, false, fixedEval{allow: true}, false)
	m := f.event(t, "PostToolUse", "Bash", map[string]any{"command": "x"}, tok, h.tenantA)
	if m["decision"] != "block" {
		t.Fatalf("a deny-closed PostToolUse must render decision:block (not a silent allow); got %v", m)
	}
}

func TestHookPEP_NonEnforceableEventsNeutralVerdict(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "ops@a.test")
	pol := hookPolicyDoc{Default: "deny", Rules: []hookPolicyRule{{Tool: "*", Decision: "deny"}}}
	f := newHookPEPFixture(t, h, pol, true, fixedEval{allow: false}, false)
	for _, ev := range []string{"Stop", "SubagentStop", "Notification", "SessionEnd", "PostCompact", "MessageDisplay"} {
		in := claude.HookDecisionInput{Event: ev, SessionID: "s", Identity: claude.HookIdentity{Tenant: h.tenantA}}
		res, err := f.dec.Decide(context.Background(), in, tok)
		if err != nil {
			t.Fatalf("%s Decide err: %v", ev, err)
		}
		if res.Permission != claude.DecisionAllow {
			t.Errorf("%s must yield a NEUTRAL allow verdict despite deny-all policy + PDP forbid; got %q (%s)", ev, res.Permission, res.Reason)
		}
	}
}

func TestHookPEP_PermitAllows(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-allow@e2e.test")
	f := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, false, fixedEval{allow: true}, false)
	out := f.call(t, "Read", map[string]any{"file_path": "/repo/README.md"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAllow {
		t.Fatalf("permit policy must allow, got %q (%v)", got, out)
	}
}

func TestHookPEP_DenyRuleBlocks(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-deny@e2e.test")
	pol := hookPolicyDoc{Default: "allow", Rules: []hookPolicyRule{
		{Tool: "Bash", Decision: "deny", Reason: "shell is forbidden by policy"},
	}}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, false)
	out := f.call(t, "Bash", map[string]any{"command": "rm -rf /"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("deny rule must block, got %q (%v)", got, out)
	}
}

// TestHookPEP_DenyClosedWithoutPolicyForTenant pins the REASON as well as the verdict.
// "deny" alone is not the contract: this PEP also denies for an unknown identity under
// require_firm, for a content-firewall block, for a central scoped forbid and for an
// unresolved path — so `permissionDecision == deny` is satisfied by refusals that prove
// nothing about the empty-policy fallback. defaultHookDisposition emits a reason that
// names it; assert that, so only the deny-closed default can turn this green.
func TestHookPEP_DenyClosedWithoutPolicyForTenant(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-nopolicy@e2e.test")
	// Empty default ⇒ deny-closed: a governed surface with no allowlist denies.
	f := newHookPEPFixture(t, h, hookPolicyDoc{}, false, fixedEval{allow: true}, false)
	out := f.call(t, "Read", map[string]any{"file_path": "/x"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("empty default must deny-closed, got %q (%v)", got, out)
	}
	// Anchored on "tool-call (deny-closed default)", not on "deny-closed default" alone:
	// the identity refusal ALSO ends in "deny-closed" (claudehookpep.go), and the
	// state-mutating-event default carries the shorter phrase too. The narrower anchor
	// discriminates both without relying on the event shape to do it.
	if r := reasonOf(out); !strings.Contains(r, "tool-call (deny-closed default)") {
		t.Fatalf("the deny must name the deny-closed default so another refusal cannot pass for it; got %q (%v)", r, out)
	}
}

func TestHookPEP_DenyClosedOnUnknownIdentityWhenFirmRequired(t *testing.T) {
	h := newHarness(t)
	f := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, true /*require firm*/, fixedEval{allow: true}, false)
	// No bearer ⇒ unknown attribution ⇒ deny (never enforce on a guessed principal).
	out := f.call(t, "Read", map[string]any{"file_path": "/x"}, "", h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("require_firm + unknown identity must deny, got %q (%v)", got, out)
	}
}

func TestHookPEP_FirmIdentityAllowsWhenRequired(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-firm@e2e.test") // member of tenant A ⇒ firm
	f := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, true, fixedEval{allow: true}, false)
	out := f.call(t, "Read", map[string]any{"file_path": "/x"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAllow {
		t.Fatalf("a firm identity must satisfy require_firm, got %q (%v)", got, out)
	}
}

func TestHookPEP_GovernedRewriteApplied(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-rewrite@e2e.test")
	pol := hookPolicyDoc{Default: "deny", Rules: []hookPolicyRule{
		{Tool: "Bash", Decision: "allow", Rewrite: map[string]any{"command": "ls --dry-run"}},
	}}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, false)
	out := f.call(t, "Bash", map[string]any{"command": "ls /"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAllow {
		t.Fatalf("rewrite rule must allow, got %q", got)
	}
	ui, ok := out["updatedInput"].(map[string]any)
	if !ok || ui["command"] != "ls --dry-run" {
		t.Fatalf("governed rewrite not applied: %v", out)
	}
}

func TestHookPEP_PDPOverlayHardDenies(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-pdp@e2e.test")
	// Disposition allows, but the live PDP forbids ⇒ hard deny (the overlay can only
	// further-restrict, never widen).
	f := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, false, fixedEval{allow: false}, false)
	out := f.call(t, "Read", map[string]any{"file_path": "/x"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("PDP forbid must hard-deny, got %q (%v)", got, out)
	}
}

func TestHookPEP_GovernanceOverlayDeniesClosedOnAuthenticationStoreError(t *testing.T) {
	h := newHarness(t)
	f := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, false, fixedEval{allow: false}, false)
	f.dec.authr = &failingAuthenticator{err: errors.New("store list failed")}

	out := f.call(t, "Read", map[string]any{"file_path": "/x"}, "store-backed-token", h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("an unresolved principal must deny when the governance overlay is configured, got %q (%v)", got, out)
	}
}

func TestHookPEP_PDPErrorDeniesClosed(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-pdp-error@e2e.test")
	f := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, false, erroringEval{err: errors.New("PDP unavailable")}, false)

	out := f.call(t, "Read", map[string]any{"file_path": "/x"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("a PDP evaluation error must deny-closed, got %q (%v)", got, out)
	}
}

func TestHookPEP_AskOpensHITLAndApprovalFlipsToAllow(t *testing.T) {
	h := newHarness(t)
	agentTok := h.firmAgentToken(t, "agent-hitl@e2e.test")
	_, reviewerTok := h.createApprover(t, "reviewer-hitl@e2e.test")

	pol := hookPolicyDoc{Default: "deny", Rules: []hookPolicyRule{
		{Tool: "Bash", Decision: "ask"},
	}}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, true /*real bridge*/)

	// (1) ask ⇒ the PEP opens a governed approval and reports ask (pending). The tool-call
	//     does NOT proceed.
	out := f.call(t, "Bash", map[string]any{"command": "deploy"}, agentTok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAsk {
		t.Fatalf("ask policy must open HITL and return ask, got %q (%v)", got, out)
	}

	// (2) Find the pending approval the bridge opened and approve it as a DIFFERENT human
	//     (the requester is the bridge's service principal; SoD is enforced by not
	//     bypassed). A human approval flips the verdict.
	id := h.firstPendingApproval(t, hookActionCapability)
	if id == "" {
		t.Fatal("expected a pending governed approval opened by the PEP")
	}
	if code, body := h.req("POST", "/v1/m/governance/approvals/"+id+"/decisions", reviewerTok, h.tenantA,
		map[string]any{"decision": "approve", "note": "approved in test"}); code != http.StatusOK {
		t.Fatalf("approve = %d: %s", code, body)
	}

	// (3) Re-run the SAME tool-call (same plan hash): the bridge idempotently finds the
	//     now-approved approval ⇒ the PEP allows. The HITL loop is complete.
	out = f.call(t, "Bash", map[string]any{"command": "deploy"}, agentTok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAllow {
		t.Fatalf("after human approval the same call must allow, got %q (%v)", got, out)
	}
}

// an ask gated tool-call may proceed under an ACTIVE break-glass grant —
// loudly: the allow reason names BREAK-GLASS and the reference, and the engine
// recorded the use. Never silently.
func TestHookPEP_BreakGlassAuthorizesAskExplicitly(t *testing.T) {
	h := newHarness(t)
	agentTok := h.firmAgentToken(t, "agent-bg@e2e.test")

	pol := hookPolicyDoc{Default: "deny", Rules: []hookPolicyRule{
		{Tool: "Bash", Decision: "ask"},
	}}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, true /*real bridge*/)

	// ask ⇒ pending, the call does not proceed.
	out := f.call(t, "Bash", map[string]any{"command": "deploy"}, agentTok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAsk {
		t.Fatalf("ask policy must queue HITL, got %q (%v)", got, out)
	}

	// An admin opens an emergency window covering the hook action.
	h.activateBreakGlassE2E(t, "claude.*", "approvers unreachable, incident response")

	// The SAME call now proceeds, explicitly attributed to break-glass.
	out = f.call(t, "Bash", map[string]any{"command": "deploy"}, agentTok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAllow {
		t.Fatalf("under an active grant the ask must allow, got %q (%v)", got, out)
	}
	reason, _ := out["permissionDecisionReason"].(string)
	if !strings.Contains(reason, "BREAK-GLASS") || !strings.Contains(reason, breakGlassRefPrefix) {
		t.Fatalf("the allow must name BREAK-GLASS and its reference, got %q", reason)
	}
}

func TestHookPEP_AskDeniesClosedWithoutBridge(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-nobridge@e2e.test")
	pol := hookPolicyDoc{Default: "deny", Rules: []hookPolicyRule{{Tool: "Bash", Decision: "ask"}}}
	// No bridge wired ⇒ an ask cannot open HITL ⇒ deny-closed.
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, false)
	out := f.call(t, "Bash", map[string]any{"command": "x"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("ask without a HITL bridge must deny-closed, got %q (%v)", got, out)
	}
}

func TestHookPEP_AskDeniesClosedOnHITLOpenError(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-hitl-error@e2e.test")
	pol := hookPolicyDoc{Default: "allow", Rules: []hookPolicyRule{{Tool: "Bash", Decision: "ask"}}}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, false)
	f.dec.bridge = &fakeOpener{err: errors.New("approval store unavailable")}

	out := f.call(t, "Bash", map[string]any{"command": "deploy"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("an HITL open error must deny-closed, got %q (%v)", got, out)
	}
}

// --- permissionPromptToolName route through the REAL governed decider --------

// prompt drives one Agent SDK permission-prompt request through the governed PEP and
// returns the PermissionResult body ({"behavior":...}).
func (f *hookPEPFixture) prompt(t *testing.T, tool string, input map[string]any, token, tenant string) map[string]any {
	t.Helper()
	payload := map[string]any{"session_id": "sess-sdk", "tool_name": tool, "tool_use_id": "tu-sdk", "tool_input": input}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/permission-prompt", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant != "" {
		req.Header.Set("X-Olivares-Hook-Tenant", tenant)
	}
	rec := httptest.NewRecorder()
	f.pep.ServePermissionPrompt(rec, req)
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response not JSON: %q", rec.Body.String())
	}
	return m
}

func TestPermissionPromptRoute_AllowDenyAsk(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-sdk-prompt@e2e.test")

	// A permit policy → behavior allow (the SAME governed decider as the PreToolUse PEP).
	f := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, false, fixedEval{allow: true}, false)
	if m := f.prompt(t, "Read", map[string]any{"file_path": "/repo/x"}, tok, h.tenantA); m["behavior"] != "allow" {
		t.Fatalf("permit policy must allow the prompt, got %v", m)
	}

	// A deny rule → behavior deny.
	pol := hookPolicyDoc{Default: "allow", Rules: []hookPolicyRule{{Tool: "Bash", Decision: "deny", Reason: "shell forbidden"}}}
	f = newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, false)
	if m := f.prompt(t, "Bash", map[string]any{"command": "rm -rf /"}, tok, h.tenantA); m["behavior"] != "deny" {
		t.Fatalf("deny rule must deny the prompt, got %v", m)
	}

	// An ask policy with NO HITL bridge → deny-closed (the prompt tool is binary).
	pol = hookPolicyDoc{Default: "deny", Rules: []hookPolicyRule{{Tool: "Bash", Decision: "ask"}}}
	f = newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, false)
	if m := f.prompt(t, "Bash", map[string]any{"command": "x"}, tok, h.tenantA); m["behavior"] != "deny" {
		t.Fatalf("ask without HITL must deny-closed on the prompt route, got %v", m)
	}
}

func TestPermissionPromptRoute_DenyClosedOnUnknownIdentity(t *testing.T) {
	h := newHarness(t)
	// require_firm + no bearer ⇒ unknown attribution ⇒ deny.
	f := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, true, fixedEval{allow: true}, false)
	if m := f.prompt(t, "Read", map[string]any{"file_path": "/x"}, "", h.tenantA); m["behavior"] != "deny" {
		t.Fatalf("require_firm + unknown identity must deny the prompt, got %v", m)
	}
}

func TestPermissionPromptRoute_GovernedRewrite(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-sdk-rw@e2e.test")
	pol := hookPolicyDoc{Default: "deny", Rules: []hookPolicyRule{
		{Tool: "Bash", Decision: "allow", Rewrite: map[string]any{"command": "ls --dry-run"}},
	}}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, false)
	m := f.prompt(t, "Bash", map[string]any{"command": "ls /"}, tok, h.tenantA)
	if m["behavior"] != "allow" {
		t.Fatalf("rewrite rule must allow, got %v", m)
	}
	ui, ok := m["updatedInput"].(map[string]any)
	if !ok || ui["command"] != "ls --dry-run" {
		t.Fatalf("governed rewrite must apply on the prompt route (PermissionResult.updatedInput): %v", m)
	}
}

func TestHookRuleMatchPathGlobDenyEtcSecrets(t *testing.T) {
	r := hookPolicyRule{
		Tool:         "Read",
		ResourceKind: hookResourceKindFile,
		Mode:         "read",
		Paths:        []string{"/etc/secrets/**"},
		Decision:     "deny",
	}
	in := claude.HookDecisionInput{
		Event:        "PreToolUse",
		Tool:         "Read",
		ResourceKind: hookResourceKindFile,
		ResourceRef:  "/etc/secrets/prod.key",
		Mode:         "read",
	}
	if !hookRuleMatches(r, in) {
		t.Fatal("/etc/secrets/** path-scoped deny rule must match a file under that tree")
	}
	in.ResourceRef = "/etc/public/prod.key"
	if hookRuleMatches(r, in) {
		t.Fatal("/etc/secrets/** path-scoped deny rule must not match a sibling path")
	}
	in.ResourceKind = "shell"
	in.ResourceRef = "/etc/secrets/prod.key"
	if hookRuleMatches(r, in) {
		t.Fatal("path-scoped rules must not match non-file resources")
	}
}

func TestHookRuleMatchSubtreeSegmentBoundary(t *testing.T) {
	r := hookPolicyRule{
		ResourceKind: hookResourceKindFile,
		Subtree:      "/a/b",
		Decision:     "deny",
	}
	in := claude.HookDecisionInput{Event: "PreToolUse", Tool: "Read", ResourceKind: hookResourceKindFile, ResourceRef: "/a/b/c", Mode: "read"}
	if !hookRuleMatches(r, in) {
		t.Fatal("subtree rule must match a descendant")
	}
	in.ResourceRef = "/a/b"
	if !hookRuleMatches(r, in) {
		t.Fatal("subtree rule must match the subtree root itself")
	}
	in.ResourceRef = "/a/bc"
	if hookRuleMatches(r, in) {
		t.Fatal("subtree rule must not match a path that only shares a string prefix")
	}
}

func TestHookPolicyDenyOverridesPathRule(t *testing.T) {
	pol := hookPolicyDoc{
		Default:        "allow",
		PathPrecedence: "deny-overrides",
		Rules: []hookPolicyRule{
			{Tool: "Read", ResourceKind: hookResourceKindFile, Paths: []string{"/etc/**"}, Decision: "allow", Reason: "broad allow"},
			{Tool: "Read", ResourceKind: hookResourceKindFile, Paths: []string{"/etc/secrets/**"}, Decision: "deny", Reason: "secret subtree"},
		},
	}
	disp, matched := evalHookPolicy(pol, claude.HookDecisionInput{
		Event:        "PreToolUse",
		Tool:         "Read",
		ResourceKind: hookResourceKindFile,
		ResourceRef:  "/etc/secrets/key",
		Mode:         "read",
	})
	if !matched || disp.decision != claude.DecisionDeny || disp.reason != "secret subtree" {
		t.Fatalf("deny-overrides must let a later path deny beat an earlier allow, got matched=%v disp=%+v", matched, disp)
	}
}

func TestHookPolicyFirstMatchPathRuleDefault(t *testing.T) {
	pol := hookPolicyDoc{
		Default: "deny",
		Rules: []hookPolicyRule{
			{Tool: "Read", ResourceKind: hookResourceKindFile, Paths: []string{"/etc/**"}, Decision: "allow", Reason: "broad allow"},
			{Tool: "Read", ResourceKind: hookResourceKindFile, Paths: []string{"/etc/secrets/**"}, Decision: "deny", Reason: "secret subtree"},
		},
	}
	disp, matched := evalHookPolicy(pol, claude.HookDecisionInput{
		Event:        "PreToolUse",
		Tool:         "Read",
		ResourceKind: hookResourceKindFile,
		ResourceRef:  "/etc/secrets/key",
		Mode:         "read",
	})
	if !matched || disp.decision != claude.DecisionAllow || disp.reason != "broad allow" {
		t.Fatalf("first-match default must keep the earlier allow, got matched=%v disp=%+v", matched, disp)
	}
}

func TestHookPolicyOnUnresolvedPathAskAndDeny(t *testing.T) {
	in := claude.HookDecisionInput{
		Event:        "PreToolUse",
		Tool:         "Read",
		ResourceKind: hookResourceKindFile,
		ResourceRef:  "relative/secret.txt",
		Mode:         "read",
	}
	pol := hookPolicyDoc{
		Default: "allow",
		Rules: []hookPolicyRule{
			{Tool: "Read", ResourceKind: hookResourceKindFile, Paths: []string{"/repo/**"}, Decision: "allow"},
		},
	}
	disp, matched := evalHookPolicy(pol, in)
	if !matched || disp.decision != claude.DecisionAsk {
		t.Fatalf("unresolved file path under a path-scoped policy must ask by default, got matched=%v disp=%+v", matched, disp)
	}
	if strings.Contains(disp.reason, in.ResourceRef) {
		t.Fatalf("unresolved-path reason must not echo the raw path, got %q", disp.reason)
	}

	pol.OnUnresolvedPath = "deny"
	disp, matched = evalHookPolicy(pol, in)
	if !matched || disp.decision != claude.DecisionDeny {
		t.Fatalf("on_unresolved_path=deny must deny, got matched=%v disp=%+v", matched, disp)
	}
}

// firstPendingApproval returns the id of the first pending approval for an action.
func (h *harness) firstPendingApproval(t *testing.T, action string) string {
	t.Helper()
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals?status=pending&action="+action)
	items, _ := m["items"].([]any)
	for _, it := range items {
		if obj, ok := it.(map[string]any); ok {
			if id, _ := obj["id"].(string); id != "" {
				return id
			}
		}
	}
	return ""
}
