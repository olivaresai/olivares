// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"testing"
)

func TestVoiceLocalApprovalRefusesRequesterDecision(t *testing.T) {
	h := newHarness(t)
	// Mirror boot's store-dependent binding of the default local approval bridge.
	h.set.approvalBridge.UseLocalProposer(h.set.gov.EngineApprovals())
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	_, approver := h.createApprover(t, "voice-approver@qualification.test")
	if code, body := h.req("PUT", "/v1/m/voice/policies", h.adminToken, h.tenantA, map[string]any{
		"agent_ref": "voice-agent", "allowed_model_ref": "voice-model", "allowed_provider_ref": "openai",
	}); code != http.StatusOK {
		t.Fatalf("set policy = %d: %s", code, body)
	}
	open := map[string]any{"session_ref": "voice-session", "agent_ref": "voice-agent", "model_ref": "voice-model", "provider_ref": "openai"}
	var request struct {
		ApprovalRef string `json:"approval_ref"`
	}
	if code := h.reqInto("POST", "/v1/m/voice/sessions/open", h.adminToken, h.tenantA, open, &request); code != http.StatusAccepted || request.ApprovalRef == "" {
		t.Fatalf("request = %d, approval_ref=%q", code, request.ApprovalRef)
	}
	if code, body := h.decide(t, h.adminToken, request.ApprovalRef, "approve"); code != http.StatusForbidden {
		t.Fatalf("requester's decision = %d: %s; want forbidden", code, body)
	}
	if code, body := h.decide(t, h.mintBoundToken(t, "admin"), request.ApprovalRef, "approve"); code != http.StatusForbidden {
		t.Fatalf("requester using a different credential = %d: %s; want forbidden", code, body)
	}
	open["approval_ref"] = request.ApprovalRef
	if code, body := h.req("POST", "/v1/m/voice/sessions/open", h.adminToken, h.tenantA, open); code != http.StatusForbidden {
		t.Fatalf("open after self-approval refusal = %d: %s", code, body)
	}
	if code, body := h.decide(t, approver, request.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("independent approval = %d: %s", code, body)
	}
	var result struct {
		Status string `json:"op_status"`
	}
	if code := h.reqInto("POST", "/v1/m/voice/sessions/open", h.adminToken, h.tenantA, open, &result); code != http.StatusOK || result.Status != "declared_not_opened" {
		t.Fatalf("approved unconfigured open = %d, status=%q", code, result.Status)
	}
}
