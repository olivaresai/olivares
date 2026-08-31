// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/event"
)

// busEvents returns the captured bus events of one type.
func (h *fakeHost) ofType(t event.Type) []event.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []event.Event
	for _, e := range h.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// opening an approval publishes approval.requested post-commit, with the
// minimal-data payload (ids and decision parameters — never the reason, never
// the subject ref) — and a FAILED create publishes nothing.
func TestApprovalCreatePublishesApprovalRequested(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")

	r := h.createApproval(editor, tenant, map[string]any{
		"subject_kind": "deployment", "subject_ref": "deploy-7", "action": "deploy",
		"required_approvals": 2, "reason": "ship the hotfix", "expires_in_seconds": 3600,
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create approval = %d %s", r.code, r.raw)
	}
	evs := h.host.ofType(event.TypeApprovalRequested)
	if len(evs) != 1 {
		t.Fatalf("approval.requested events = %d, want 1", len(evs))
	}
	e := evs[0]
	if e.Tenant != tenant.String() {
		t.Errorf("event tenant = %q, want %q", e.Tenant, tenant)
	}
	a, ok := event.ApprovalRequestOf(e)
	if !ok {
		t.Fatalf("payload is not an ApprovalRequest: %T", e.Payload)
	}
	if a.ApprovalID != r.body["id"].(string) {
		t.Errorf("ApprovalID = %q, want %q", a.ApprovalID, r.body["id"])
	}
	if a.Action != "deploy" || a.SubjectKind != "deployment" || a.RequiredApprovals != 2 {
		t.Errorf("payload fields wrong: %+v", a)
	}
	if a.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should be stamped from expires_in_seconds")
	}
	if a.RiskTier == "" {
		t.Error("RiskTier should be stamped")
	}
	// Minimal data: the payload STRUCT has no Reason/SubjectRef field, so the
	// wire form must not carry the operator prose or the subject reference.
	wire, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"ship the hotfix", "deploy-7"} {
		if strings.Contains(string(wire), leak) {
			t.Errorf("approval.requested wire payload leaks %q: %s", leak, wire)
		}
	}

	// A rejected create publishes nothing further.
	if r := h.createApproval(editor, tenant, map[string]any{"subject_kind": "x", "action": ""}); r.code != http.StatusBadRequest {
		t.Fatalf("invalid create = %d, want 400", r.code)
	}
	if got := len(h.host.ofType(event.TypeApprovalRequested)); got != 1 {
		t.Errorf("a failed create must not publish (events = %d, want 1)", got)
	}
}

// policy create/update/delete each publish policy.changed post-commit
// with {id, kind, op, enabled} — never the name or the spec.
func TestPolicyMutationsPublishPolicyChanged(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	spec := map[string]any{"rules": []any{map[string]any{"deny": true, "verb": "write"}}}
	r := h.do("POST", "/v1/m/governance/policies", admin, map[string]any{
		"name": "deny-agent-writes", "kind": "abac", "enabled": true, "spec": spec,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create policy = %d %s", r.code, r.raw)
	}
	pid := r.body["id"].(string)

	if r := h.do("PUT", "/v1/m/governance/policies/"+pid, admin, map[string]any{
		"name": "deny-agent-writes", "kind": "abac", "enabled": false, "spec": spec,
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("update policy = %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/governance/policies/"+pid, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete policy = %d %s", r.code, r.raw)
	}

	evs := h.host.ofType(event.TypePolicyChanged)
	if len(evs) != 3 {
		t.Fatalf("policy.changed events = %d, want 3", len(evs))
	}
	wantOps := []string{event.PolicyOpCreated, event.PolicyOpUpdated, event.PolicyOpDeleted}
	wantEnabled := []bool{true, false, false}
	for i, e := range evs {
		p, ok := event.PolicyChangeOf(e)
		if !ok {
			t.Fatalf("event %d payload is not a PolicyChange: %T", i, e.Payload)
		}
		if p.PolicyID != pid || p.Kind != "abac" || p.Op != wantOps[i] || p.Enabled != wantEnabled[i] {
			t.Errorf("event %d = %+v, want op=%s enabled=%v id=%s", i, p, wantOps[i], wantEnabled[i], pid)
		}
		if e.Tenant != tenant.String() {
			t.Errorf("event %d tenant = %q, want %q", i, e.Tenant, tenant)
		}
	}
}
