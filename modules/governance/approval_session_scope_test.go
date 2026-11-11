// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

// A caller cannot turn a deployment, hook or forged provider request into a
// standing grant by supplying the provider action's name or a decision scope.
func TestApprovalSessionScopeRefusesOtherRequestsWithoutRecording(t *testing.T) {
	for _, action := range []string{"deploy", "claude.hook.tool", "sessions.run.launch", "sessions.provider.approval"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "scope-refusal")
			_, editor := h.roleUser(admin, tenant, "requester@x.io", "editor")
			r := h.createApproval(editor, tenant, map[string]any{"action": action, "subject_kind": "session_run", "subject_ref": "run-one"})
			if r.code != http.StatusCreated {
				t.Fatalf("request = %d %s", r.code, r.raw)
			}
			id := r.body["id"].(string)
			decision := h.do("POST", govPath+"/approvals/"+id+"/decisions", admin, map[string]any{"decision": "approve", "scope": "session"}, tenantHdr(tenant))
			if decision.code != http.StatusBadRequest {
				t.Fatalf("session scope on %s = %d %s, want 400", action, decision.code, decision.raw)
			}
			trail := h.do("GET", govPath+"/approvals/"+id+"/decisions", admin, nil, tenantHdr(tenant))
			if rows, _ := trail.body["items"].([]any); len(rows) != 0 {
				t.Fatalf("refused scope recorded decisions: %s", trail.raw)
			}
			if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
				return sc.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error {
					if e.Action == "governance.approval.decision" && e.TargetID.String() == id {
						t.Error("refused scope recorded a decision in the ledger")
					}
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionAllowanceMatchesExactActionAndCanBeRevoked(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "session-allowance")
	p := cancellationSession(t, h, tenant, admin, "allowance")
	service := h.gov.EngineApprovals()
	request := governance.ApprovalRequest{Action: "claude.tool.use", SubjectKind: "claude.tool", SubjectRef: "call-one", SessionRef: p.SessionIdentity, SessionScopeAllowed: true, SessionAllowanceKey: "pwd", Review: &governance.ApprovalReview{Tool: "Bash", Text: "pwd"}}
	// Keep the test compilable on the old implementation: the missing engine port is RED.
	type allowances interface {
		ReuseSessionAllowance(context.Context, model.TenantID, auth.Principal, governance.ApprovalRequest, string) (string, error)
	}
	reuse, ok := any(service).(allowances)
	if !ok {
		t.Fatal("engine cannot reuse a session-scoped decision")
	}
	approval, err := service.Request(t.Context(), tenant, p, request)
	if err != nil {
		t.Fatal(err)
	}
	// A new session and a different target must still ask.
	if code := h.do("POST", govPath+"/approvals/"+approval.ID+"/decisions", admin, map[string]any{"decision": "approve", "scope": "session"}, tenantHdr(tenant)).code; code != http.StatusOK {
		t.Fatalf("session decision = %d", code)
	}
	ref, err := reuse.ReuseSessionAllowance(t.Context(), tenant, p, request, "call-two")
	if err != nil || ref != approval.ID {
		t.Fatalf("same action = %q %v", ref, err)
	}
	otherTenant := h.createOrg(admin, "other-allowance-tenant")
	if ref, err = reuse.ReuseSessionAllowance(t.Context(), otherTenant, p, request, "wrong-tenant"); err != auth.ErrUnauthenticated || ref != "" {
		t.Fatalf("mismatched tenant = %q %v", ref, err)
	}
	otherPrincipal := cancellationSession(t, h, otherTenant, admin, "other-tenant")
	otherRequest := request
	otherRequest.SessionRef = otherPrincipal.SessionIdentity
	if ref, err = reuse.ReuseSessionAllowance(t.Context(), otherTenant, otherPrincipal, otherRequest, "other-tenant"); err != nil || ref != "" {
		t.Fatalf("different tenant = %q %v", ref, err)
	}
	request.Review.Text = "ls"
	request.SessionAllowanceKey = "ls"
	if ref, err = reuse.ReuseSessionAllowance(t.Context(), tenant, p, request, "different"); err != nil || ref != "" {
		t.Fatalf("different action = %q %v", ref, err)
	}
	request.Review.Text = "pwd"
	request.SessionAllowanceKey = "pwd"
	other := cancellationSession(t, h, tenant, admin, "new-session")
	request.SessionRef = other.SessionIdentity
	if ref, err = reuse.ReuseSessionAllowance(t.Context(), tenant, other, request, "new-session"); err != nil || ref != "" {
		t.Fatalf("new session = %q %v", ref, err)
	}
	request.SessionRef = p.SessionIdentity
	resumed := p
	resumed.SessionFence++
	if ref, err = reuse.ReuseSessionAllowance(t.Context(), tenant, resumed, request, "resumed"); err != nil || ref != "" {
		t.Fatalf("resumed process = %q %v", ref, err)
	}
	var uses int
	if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(t.Context(), 1, func(e model.AuditEvent) error {
			if e.Action == "governance.approval.session.use" && e.TargetID.String() == approval.ID {
				uses++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if uses != 1 {
		t.Fatalf("session use audit events = %d, want 1", uses)
	}
	if _, err = service.Cancel(t.Context(), tenant, p, approval.ID); err != nil {
		t.Fatal(err)
	}
	if ref, err = reuse.ReuseSessionAllowance(t.Context(), tenant, p, request, "revoked"); err != nil || ref != "" {
		t.Fatalf("revoked = %q %v", ref, err)
	}
	ending, err := service.Request(t.Context(), tenant, p, request)
	if err != nil {
		t.Fatal(err)
	}
	if code := h.do("POST", govPath+"/approvals/"+ending.ID+"/decisions", admin, map[string]any{"decision": "approve", "scope": "session"}, tenantHdr(tenant)).code; code != http.StatusOK {
		t.Fatalf("ending decision = %d", code)
	}
	if err := h.gov.CancelSessionApprovals(t.Context(), tenant, []string{p.SessionIdentity}, "operator", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	if ref, err := reuse.ReuseSessionAllowance(t.Context(), tenant, p, request, "ended"); err != nil || ref != "" {
		t.Fatalf("ended session = %q %v", ref, err)
	}
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(t.Context(), model.Policy{Name: "Every request", Kind: "approval", Enabled: true, Spec: map[string]any{"required_approvals": 1, "match": map[string]any{"action": request.Action}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	policyRequest, err := service.Request(t.Context(), tenant, p, request)
	if err != nil || policyRequest.SessionScopeAllowed {
		t.Fatalf("policy request = %+v %v", policyRequest, err)
	}
	if code := h.do("POST", govPath+"/approvals/"+policyRequest.ID+"/decisions", admin, map[string]any{"decision": "approve", "scope": "session"}, tenantHdr(tenant)).code; code != http.StatusBadRequest {
		t.Fatalf("policy session decision = %d", code)
	}
}
