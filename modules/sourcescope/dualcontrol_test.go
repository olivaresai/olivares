// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// dualcontrol_test.go proves the two-person rule is about PEOPLE, not credentials.
//
// It compared Actor() with Actor(). Actor() is "user:<UserID>" for a session and
// "token:<CredID>" for an API token (core/auth/principal.go:126-131), so ONE person holds
// TWO strings — and any admin can mint their own token, because IssueToken caps the new
// token at the minting actor's own role and admin carries both the write permission and
// posture:admin. Propose with the session, approve with the token, and a two-person control
// was satisfied by one human. Reproduced on both surfaces before the fix.
//
// The identity that does not split is Principal.UserID: a token principal is constructed
// with its ISSUER's UserID (core/auth/principal_lookup.go:235), so it is the same value for
// both of that person's credentials.

// mintToken issues an API token bound to tenant with the given role, using `as` (a session
// token) as the minting credential — i.e. the token belongs to whoever `as` belongs to.
func (h *harness) mintToken(as string, tenant model.TenantID, name, role string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/tokens", as, map[string]any{
		"name": name, "tenant": tenant.String(), "role": role,
	}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("mint token %s = %d %s", name, r.code, r.raw)
	}
	tok, _ := r.body["token"].(string)
	if tok == "" {
		h.t.Fatalf("mint token %s returned no token: %s", name, r.raw)
	}
	return tok
}

// TestSelfApprovalWithOwnTokenIsRefusedOnTheAssignmentSurface is the P0, as reproduced.
func TestSelfApprovalWithOwnTokenIsRefusedOnTheAssignmentSurface(t *testing.T) {
	h, admin, _, tenant := assignHarness(t)
	h.createWorkspace(tenant, "sales")
	wsEng := h.createWorkspace(tenant, "engineering")
	agentEng := h.createAgent(tenant, "eng-bot", wsEng)
	h.createSession(tenant, "eng-session", agentEng.ID, wsEng)
	pEng := h.principalFor(admin, tenant, "eng@acme.io", "")

	ownToken := h.mintToken(admin, tenant, "self-approver", "admin")
	id := h.createAssignmentOK(admin, tenant, "github", "sales", true)
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pEng, "eng-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("engineering must be denied before anything, got %+v %v", d, err)
	}

	// The gate fires correctly: the last-row delete is proposed, not applied.
	del := h.do("DELETE", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant))
	if del.code != http.StatusAccepted {
		t.Fatalf("last-row delete = %d, want 202: %s", del.code, del.raw)
	}
	// THE BYPASS: the SAME human approving with a credential they minted seconds ago.
	a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+del.body["id"].(string)+"/approve", ownToken, nil, tenantHdr(tenant))
	if a.code != http.StatusConflict {
		t.Fatalf("self-approval with one's OWN token = %d, want 409: %s", a.code, a.raw)
	}
	if g := h.do("GET", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("the refused approval must leave the row, got %d %s", g.code, g.raw)
	}
	if d, err := h.resolver.ResolveForSession(t.Context(), tenant, pEng, "eng-session", "data", "github"); err != nil || d.Allowed {
		t.Fatalf("one human must not have opened the connector, got %+v %v", d, err)
	}
}

// TestSelfApprovalWithOwnTokenIsRefusedOnTheBindingSurface: the same defect, the same fix,
// on the surface hardened. One check serves both because both legs meet in
// decidePostureRequest — which is also why the defect was never binding-specific.
func TestSelfApprovalWithOwnTokenIsRefusedOnTheBindingSurface(t *testing.T) {
	h, admin, _, tenant := assignHarness(t)
	h.createWorkspace(tenant, "sales")
	h.createWorkspace(tenant, "engineering")
	ownToken := h.mintToken(admin, tenant, "self-approver", "admin")

	if c := h.createBinding(admin, tenant, map[string]any{
		"source_type": "knowledge", "source_ref": "kb1",
		"scope_tree": "workspace", "scope_ref": "sales", "enabled": true,
	}); c.code != http.StatusCreated {
		t.Fatalf("first allow = %d %s", c.code, c.raw)
	}
	second := h.createBinding(admin, tenant, map[string]any{
		"source_type": "knowledge", "source_ref": "kb1",
		"scope_tree": "workspace", "scope_ref": "engineering", "enabled": true,
	})
	if second.code != http.StatusAccepted {
		t.Fatalf("second allow = %d, want 202: %s", second.code, second.raw)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+second.body["id"].(string)+"/approve", ownToken, nil, tenantHdr(tenant)); a.code != http.StatusConflict {
		t.Fatalf("self-approval with one's OWN token = %d, want 409: %s", a.code, a.raw)
	}
}

// TestTwoDISTINCTHumansStillApprove is the over-refusal guard. A control that refuses
// everything is not a control, and swapping the compared field is exactly the change that
// could break the legitimate path while looking correct.
func TestTwoDISTINCTAccountsStillApprove(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	h.createWorkspace(tenant, "sales")
	h.createWorkspace(tenant, "engineering")
	id := h.createAssignmentOK(admin, tenant, "github", "sales", true)

	del := h.do("DELETE", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant))
	if del.code != http.StatusAccepted {
		t.Fatalf("delete = %d %s", del.code, del.raw)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+del.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("two distinct humans must still be able to approve, got %d %s", a.code, a.raw)
	}
	// And a DIFFERENT person's token works too — the rule is about people, so a second
	// human's API token is a valid second person.
	id2 := h.createAssignmentOK(admin, tenant, "gitlab", "sales", true)
	approverToken := h.mintToken(approver, tenant, "approver-token", "admin")
	del2 := h.do("DELETE", "/v1/m/sourcescope/assignments/"+id2, admin, nil, tenantHdr(tenant))
	if del2.code != http.StatusAccepted {
		t.Fatalf("delete 2 = %d %s", del2.code, del2.raw)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+del2.body["id"].(string)+"/approve", approverToken, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("a DIFFERENT human's token is a valid second person, got %d %s", a.code, a.raw)
	}
}

// TestBothIdentitiesAreOnTheRequestRow: the trail must expose the bypass without a join.
// Actor() alone made a self-approval read as two actors — "user:U" proposed, "token:X"
// decided — so the queue looked like a clean two-person trail and the only way to see one
// person was to join against core.api_token. A trail that needs a join is not a trail.
func TestBothIdentitiesAreOnTheRequestRow(t *testing.T) {
	h, admin, approver, tenant := assignHarness(t)
	h.createWorkspace(tenant, "sales")
	id := h.createAssignmentOK(admin, tenant, "github", "sales", true)

	del := h.do("DELETE", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant))
	if del.code != http.StatusAccepted {
		t.Fatalf("delete = %d %s", del.code, del.raw)
	}
	pu, _ := del.body["proposer_user"].(string)
	if pu == "" {
		t.Fatalf("the pending request must name the HUMAN who proposed: %s", del.raw)
	}
	a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+del.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant))
	if a.code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.code, a.raw)
	}
	du, _ := a.body["decided_by_user"].(string)
	if du == "" {
		t.Fatalf("the decided request must name the HUMAN who decided: %s", a.raw)
	}
	if du == pu {
		t.Fatalf("proposer and decider must be different humans, both %q", pu)
	}
	// And it survives a re-read: it is a stored column, not a response-only field.
	g := h.do("GET", "/v1/m/sourcescope/posture-requests/"+del.body["id"].(string), admin, nil, tenantHdr(tenant))
	if g.body["proposer_user"] != pu || g.body["decided_by_user"] != du {
		t.Errorf("both identities must be stored, got %s", g.raw)
	}
}
