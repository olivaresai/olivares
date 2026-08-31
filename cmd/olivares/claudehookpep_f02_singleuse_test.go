// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// claudehookpep_f02_singleuse_test.go is the (F-02) red→green repro at the REAL
// governed decider + bridge + engine. It proves a human HITL approval of a
// destructive tool-call is SINGLE-USE — the class of bug where one approval was
// replayable for the whole ~24h validity window without a fresh human decision.
//
// The bug, before the fix: gateViaHITL reused an APPROVED grant (reuse-approved within
// the time-box) for ANY later call whose (session, args) hashed to the same plan hash —
// so a SECOND, distinct destructive tool-call (a new tool_use_id, identical arguments)
// found the still-approved grant and was allowed WITHOUT a new human decision.
//
// The fix: on an approved grant the decider SPENDS it single-use, keyed to the exact
// caller (tool_use_id). It separates the two things F-02 conflated:
//   - result-idempotency  a transport retry of the SAME tool_use_id re-obtains its grant
//     (allow) — it does NOT re-authorize;
//   - permission-reuse    a NEW tool_use_id reusing an already-consumed approval is a
//     would-replay DENY (a fresh human decision is required), recorded to the signed
//     ledger + a finding.

// hookCallToolUseID drives one PreToolUse decision through the PEP with an explicit
// tool_use_id (the field the single-use consume keys on) and returns the
// hookSpecificOutput. It mirrors hookPEPFixture.event but lets the test vary the
// per-call correlation id, which is exactly the discriminator F-02 turns on.
func (f *hookPEPFixture) hookCallToolUseID(t *testing.T, tool, toolUseID string, input map[string]any, token, tenant string) map[string]any {
	t.Helper()
	payload := map[string]any{
		"session_id": "sess-f02", "hook_event_name": "PreToolUse",
		"tool_name": tool, "tool_use_id": toolUseID, "tool_input": input,
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

// hookCallNoToolUseID drives one PreToolUse decision through the PEP with NO tool_use_id
// at all — exactly the payload the Claude Code CLI emits (tool_use_id is an OPTIONAL body
// field the CLI omits by design; connectors/claude/hooks.go:267-269,275). This is the
// adversarial case the first fix MISSED: with no tool_use_id the consumer degraded
// to the constant plan hash, so a replay hit the result-idempotency branch and was
// granted for the whole ~24h window.
func (f *hookPEPFixture) hookCallNoToolUseID(t *testing.T, tool string, input map[string]any, token, tenant string) map[string]any {
	t.Helper()
	payload := map[string]any{
		"session_id": "sess-f02-noid", "hook_event_name": "PreToolUse",
		"tool_name": tool, "tool_input": input, // NOTE: no "tool_use_id" key
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

// TestHookPEP_F02_AbsentToolUseIDIsStrictSingleUse is the-FIX BLOCKER repro: the
// adversarial path the FIRST fix left open. A destructive tool-call that carries NO
// tool_use_id (the CLI payload) must be STRICTLY single-use — the approval is spent on the
// first execution and ANY later call (no id, same session+args, well inside the ~24h
// window) is denied would-replay. It is RED on the pre-fix decider (the consumer fell back
// to the constant plan hash → the second call hit result-idempotency and was ALLOWED) and
// GREEN after: with no non-forgeable transport id, the PEP mints a fresh server-side nonce
// per call, so the second consume is a would-replay DENY.
func TestHookPEP_F02_AbsentToolUseIDIsStrictSingleUse(t *testing.T) {
	h := newHarness(t)
	agentTok := h.firmAgentToken(t, "agent-f02-noid@e2e.test")
	_, reviewerTok := h.createApprover(t, "reviewer-f02-noid@e2e.test")

	pol := hookPolicyDoc{Default: "deny", Rules: []hookPolicyRule{{Tool: "Bash", Decision: "ask"}}}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, true /*real bridge*/)
	destructive := map[string]any{"command": "rm -rf /var/lib/app/data"}

	// STEP 0 — the FIRST call (no tool_use_id) opens HITL and asks (pending).
	out := f.hookCallNoToolUseID(t, "Bash", destructive, agentTok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAsk {
		t.Fatalf("first destructive call must open HITL and ask, got %q (%v)", got, out)
	}

	// A human approves the exact plan.
	id := h.firstPendingApproval(t, hookActionCapability)
	if id == "" {
		t.Fatal("expected a pending governed approval opened by the PEP")
	}
	if code, body := h.req("POST", "/v1/m/governance/approvals/"+id+"/decisions", reviewerTok, h.tenantA,
		map[string]any{"decision": "approve", "note": "approved once"}); code != http.StatusOK {
		t.Fatalf("approve = %d: %s", code, body)
	}

	// STEP 1 — the APPROVED call (still no tool_use_id) proceeds exactly ONCE.
	out = f.hookCallNoToolUseID(t, "Bash", destructive, agentTok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAllow {
		t.Fatalf("the approved call must allow once, got %q (%v)", got, out)
	}

	// STEP 2 — THE BLOCKER: a SECOND call with NO tool_use_id (same session+args, well
	// inside the ~24h window) MUST be denied would-replay. The pre-fix decider ALLOWED this
	// (consumer = constant plan hash → result-idempotency), reopening F-02 trivially and
	// automatically under the CLI.
	out = f.hookCallNoToolUseID(t, "Bash", destructive, agentTok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("single-use blocker: a SECOND tool-call with NO tool_use_id MUST be denied "+
			"(strict single-use), got %q (%v) — the single-use human approval was replayed", got, out)
	}
	if reason, _ := out["permissionDecisionReason"].(string); !strings.Contains(reason, "replay") {
		t.Fatalf("the deny must name the replay, got %q", reason)
	}

	// STEP 3 — the signed ledger carries the would-replay-denied event (the evidence leg).
	actions := h.tenantLedgerActions(t, f.tenant)
	if !actions["governance.approval.replay_denied"] {
		t.Fatal("a would-replay must leave a governance.approval.replay_denied event in the signed ledger")
	}
	if !actions["governance.approval.consume"] {
		t.Fatal("the single-use spend must leave a governance.approval.consume event in the signed ledger")
	}
}

// TestHookPEP_F02_ApprovalIsSingleUseNotReplayable is the F-02 repro. It is RED on the
// pre-fix decider (STEP 3 allowed a second, distinct destructive tool-call by replaying
// the same approval) and GREEN after: the second call is denied would-replay, while a
// transport retry of the first call still re-obtains its grant.
func TestHookPEP_F02_ApprovalIsSingleUseNotReplayable(t *testing.T) {
	h := newHarness(t)
	agentTok := h.firmAgentToken(t, "agent-f02@e2e.test")
	_, reviewerTok := h.createApprover(t, "reviewer-f02@e2e.test")

	// A destructive shell command routed to human approval.
	pol := hookPolicyDoc{Default: "deny", Rules: []hookPolicyRule{{Tool: "Bash", Decision: "ask"}}}
	f := newHookPEPFixture(t, h, pol, false, fixedEval{allow: true}, true /*real bridge*/)
	destructive := map[string]any{"command": "rm -rf /var/lib/app/data"}

	// STEP 0 — the FIRST call (tool_use_id A) opens HITL and reports ask (pending). The
	// tool-call does NOT proceed until a human decides.
	out := f.hookCallToolUseID(t, "Bash", "toolu_A", destructive, agentTok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAsk {
		t.Fatalf("first destructive call must open HITL and ask, got %q (%v)", got, out)
	}

	// A human approves the exact plan.
	id := h.firstPendingApproval(t, hookActionCapability)
	if id == "" {
		t.Fatal("expected a pending governed approval opened by the PEP")
	}
	if code, body := h.req("POST", "/v1/m/governance/approvals/"+id+"/decisions", reviewerTok, h.tenantA,
		map[string]any{"decision": "approve", "note": "approved once"}); code != http.StatusOK {
		t.Fatalf("approve = %d: %s", code, body)
	}

	// STEP 1 — the APPROVED call (tool_use_id A) proceeds exactly once.
	out = f.hookCallToolUseID(t, "Bash", "toolu_A", destructive, agentTok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAllow {
		t.Fatalf("the approved call must allow once, got %q (%v)", got, out)
	}
	if reason, _ := out["permissionDecisionReason"].(string); !strings.Contains(reason, "single-use") {
		t.Fatalf("the allow reason must record the single-use spend, got %q", reason)
	}

	// STEP 2 — result-idempotency: a TRANSPORT RETRY of the SAME tool_use_id re-obtains its
	// grant. It re-reads the recorded single-use consume; it does NOT re-authorize.
	out = f.hookCallToolUseID(t, "Bash", "toolu_A", destructive, agentTok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionAllow {
		t.Fatalf("a transport retry of the SAME tool_use_id must re-obtain the grant, got %q (%v)", got, out)
	}

	// STEP 3 — the REPLAY the bug allowed: a SECOND, DISTINCT destructive call (a NEW
	// tool_use_id, identical session+args, well inside the ~24h window) must NOT reuse the
	// spent approval. It is DENIED would-replay; a fresh human decision is required.
	out = f.hookCallToolUseID(t, "Bash", "toolu_B", destructive, agentTok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("F-02: a NEW tool-call reusing an already-consumed approval MUST be denied "+
			"(would-replay), got %q (%v) — the single-use human approval was replayed", got, out)
	}
	if reason, _ := out["permissionDecisionReason"].(string); !strings.Contains(reason, "replay") {
		t.Fatalf("the deny must name the replay, got %q", reason)
	}

	// STEP 4 — the signed ledger carries a would-replay-denied event AND the original
	// single-use spend for the approval (the evidence leg).
	actions := h.tenantLedgerActions(t, f.tenant)
	if !actions["governance.approval.replay_denied"] {
		t.Fatal("a would-replay must leave a governance.approval.replay_denied event in the signed ledger")
	}
	if !actions["governance.approval.consume"] {
		t.Fatal("the single-use spend must leave a governance.approval.consume event in the signed ledger")
	}
}

// tenantLedgerActions walks the tenant's signed audit ledger and returns the set of
// event actions present — the evidence leg of the single-use consume / would-replay.
func (h *harness) tenantLedgerActions(t *testing.T, tenant model.TenantID) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(ev model.AuditEvent) error {
			seen[ev.Action] = true
			return nil
		})
	})
	if err != nil {
		t.Fatalf("walk tenant ledger: %v", err)
	}
	return seen
}
