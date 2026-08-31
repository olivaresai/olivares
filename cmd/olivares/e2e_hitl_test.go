// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This is the round-trip proof: a SIGNED ITSM/ChatOps callback resolves a real
// governance.approval through the REAL engine (the same composition root the binary
// boots), and the engine's guardrails — separation of duty, the duplicate/late-decision
// rejection — are enforced by itself, NOT by any shortcut in the receiver. It drives
// the inbound HITL receiver exactly as the binary mounts it: apiDecider over the engine's
// own HTTP handler (full authenticate → tenant → authorize → handler → audit).

// createApprover provisions a second human (distinct from the seeded superadmin), grants
// them admin in tenant A, and returns their session token — the credential the receiver
// acts AS. Because a session principal's UserID is that human's SoD and
// duplicate-decider guards key on the real approver.
func (h *harness) createApprover(t *testing.T, email string) (userID, token string) {
	t.Helper()
	var u struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/users", h.adminToken, "", map[string]any{
		"email": email, "password": "approver-pw-123456",
	}, &u); code != http.StatusCreated || u.ID == "" {
		t.Fatalf("create user = %d id=%q", code, u.ID)
	}
	if code, body := h.req("POST", "/v1/memberships", h.adminToken, "", map[string]any{
		"user_id": u.ID, "tenant": h.tenantA, "role": "admin",
	}); code != http.StatusCreated {
		t.Fatalf("grant membership = %d: %s", code, body)
	}
	var login struct {
		Token string `json:"token"`
	}
	if code := h.reqInto("POST", "/v1/auth/login", "", "", map[string]any{
		"email": email, "password": "approver-pw-123456",
	}, &login); code != http.StatusOK || login.Token == "" {
		t.Fatalf("approver login = %d", code)
	}
	// a CRITICAL decision demands an AAL3 session; the approver is
	// step-up-verified like every suite operator.
	h.stepUp(login.Token)
	return u.ID, login.Token
}

// createApproval opens an approval as the given requester token and returns its id.
func (h *harness) createApproval(t *testing.T, requesterToken, action string) string {
	t.Helper()
	var dto struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if code := h.reqInto("POST", "/v1/m/governance/approvals", requesterToken, h.tenantA, map[string]any{
		"subject_kind": "deployment", "subject_ref": "svc/api", "action": action,
	}, &dto); code != http.StatusCreated || dto.ID == "" {
		t.Fatalf("create approval = %d id=%q", code, dto.ID)
	}
	return dto.ID
}

// approvalStatus reads an approval's effective status via the real API.
func (h *harness) approvalStatus(t *testing.T, token, id string) string {
	t.Helper()
	m := h.getJSON(token, h.tenantA, "/v1/m/governance/approvals/"+id)
	s, _ := m["status"].(string)
	return s
}

// buildE2EReceiver mounts the inbound receiver over the engine's real handler, mapping a
// Slack user and two webhook actors to their approver tokens.
func buildE2EReceiver(t *testing.T, h *harness, approverToken string) *hitlReceiver {
	t.Helper()
	cfg := hitlConfig{Providers: []hitlProviderSpec{
		{Name: "corp-slack", Kind: hitlKindSlack, SigningSecret: slackSecret, Approvers: []hitlApprover{
			{ExternalID: "U-B", Tenant: h.tenantA, Token: approverToken},
		}},
		{Name: "corp-snow", Kind: hitlKindWebhook, SigningSecret: hookSecret, Approvers: []hitlApprover{
			{ExternalID: "admin-ext", Tenant: h.tenantA, Token: h.adminToken}, // self-decide => SoD
			{ExternalID: "b-ext", Tenant: h.tenantA, Token: approverToken},
		}},
	}}
	r := newHITLReceiver(cfg, apiDecider{handler: h.h}, discardLog())
	if r == nil {
		t.Fatal("receiver should build")
	}
	return r
}

func TestHITLRoundTripEndToEnd(t *testing.T) {
	h := newHarness(t)
	_, bToken := h.createApprover(t, "approver-b@e2e.test")
	rcv := buildE2EReceiver(t, h, bToken)

	// (1) HAPPY PATH: admin opens an approval; a SIGNED Slack approve from the mapped
	// approver resolves it to "approved" through the real engine.
	a1 := h.createApproval(t, h.adminToken, "deploy-prod-a1")
	if got := h.approvalStatus(t, h.adminToken, a1); got != "pending" {
		t.Fatalf("a1 initial status = %q, want pending", got)
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := slackRequest(t, slackSecret, ts, blockActions("U-B", "approve", a1))
	rec := httptest.NewRecorder()
	rcv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("slack approve status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := h.approvalStatus(t, h.adminToken, a1); got != "approved" {
		t.Fatalf("a1 status after signed approve = %q, want approved", got)
	}

	// (2) BAD SIGNATURE: a tampered callback must NOT change engine state.
	a2 := h.createApproval(t, h.adminToken, "deploy-prod-a2")
	bad := slackRequest(t, slackSecret, ts, blockActions("U-B", "approve", a2))
	bad.Header.Set("X-Slack-Signature", "v0=deadbeef") // tamper
	badRec := httptest.NewRecorder()
	rcv.handler().ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered status = %d, want 401", badRec.Code)
	}
	if got := h.approvalStatus(t, h.adminToken, a2); got != "pending" {
		t.Fatalf("a2 must stay pending after a rejected callback, got %q", got)
	}

	// (3) SEPARATION OF DUTY enforced BY: the requester (admin) tries to approve
	// their own request via a SIGNED webhook callback. Rejects it (403), the receiver
	// reflects it, and the approval stays pending. No shortcut bypasses SoD.
	ts2 := strconv.FormatInt(time.Now().Unix(), 10)
	sod := webhookRequest(t, hookSecret, ts2, webhookCallback{ApprovalID: a2, Decision: "approve", ExternalID: "admin-ext"})
	sodRec := httptest.NewRecorder()
	rcv.handler().ServeHTTP(sodRec, sod)
	if sodRec.Code != http.StatusForbidden {
		t.Fatalf("SoD self-approve status = %d, want 403 from body=%s", sodRec.Code, sodRec.Body.String())
	}
	if !strings.Contains(strings.ToLower(sodRec.Body.String()), "separation of duty") {
		t.Fatalf("SoD body should explain the rejection: %s", sodRec.Body.String())
	}
	if got := h.approvalStatus(t, h.adminToken, a2); got != "pending" {
		t.Fatalf("a2 must stay pending after an SoD-rejected decision, got %q", got)
	}

	// (4) LATE / ALREADY-RESOLVED enforced BY: deciding the already-approved a1
	// again is rejected (409) by the engine — the receiver opens no path around the
	// machine's terminal-state guard.
	ts3 := strconv.FormatInt(time.Now().Unix(), 10)
	late := webhookRequest(t, hookSecret, ts3, webhookCallback{ApprovalID: a1, Decision: "approve", ExternalID: "b-ext"})
	lateRec := httptest.NewRecorder()
	rcv.handler().ServeHTTP(lateRec, late)
	if lateRec.Code != http.StatusConflict {
		t.Fatalf("decision on a resolved approval status = %d, want 409; body=%s", lateRec.Code, lateRec.Body.String())
	}
}

// TestHITLDecisionIsAuditedAsRealHuman proves the decision entered immutable trail
// attributed to the real approver (not a system/system-token actor) — the audit
// requirement of the governed path.
func TestHITLDecisionIsAuditedAsRealHuman(t *testing.T) {
	h := newHarness(t)
	bUserID, bToken := h.createApprover(t, "approver-c@e2e.test")
	rcv := buildE2EReceiver(t, h, bToken)

	a := h.createApproval(t, h.adminToken, "deploy-prod-audit")
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := slackRequest(t, slackSecret, ts, blockActions("U-B", "approve", a))
	rec := httptest.NewRecorder()
	rcv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d", rec.Code)
	}

	// The immutable decision trail must show one approve decided by the real human B.
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+a+"/decisions")
	var decs []map[string]any
	if raw, ok := m["items"].([]any); ok {
		for _, it := range raw {
			if obj, ok := it.(map[string]any); ok {
				decs = append(decs, obj)
			}
		}
	}
	if len(decs) != 1 {
		t.Fatalf("decision trail has %d entries, want 1", len(decs))
	}
	decider, _ := decs[0]["decider"].(string)
	if !strings.Contains(decider, bUserID) {
		t.Fatalf("decider = %q, want it to reference the real approver user %q", decider, bUserID)
	}
	if decision, _ := decs[0]["decision"].(string); decision != "approve" {
		t.Fatalf("decision = %q, want approve", decision)
	}
}
