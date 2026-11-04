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
)

// useApprovalAuthority wires the authority the module enumerates deciders through, as
// the composition root does (cmd/olivares/boot.go).
func (h *harness) useApprovalAuthority() {
	h.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.gov.RequestEvaluator(), auth.WithScopedGrants(h.gov.ScopedGrants())))
}

// approvalRead returns the list entry and the detail read of one approval for a caller.
func (h *harness) approvalRead(token string, tenant model.TenantID, id string) (listed, detail map[string]any) {
	h.t.Helper()
	list := h.do("GET", govPath+"/approvals", token, nil, tenantHdr(tenant))
	if list.code != http.StatusOK {
		h.t.Fatalf("list approvals = %d %s", list.code, list.raw)
	}
	for _, it := range items(list) {
		if m := it.(map[string]any); m["id"] == id {
			listed = m
		}
	}
	if listed == nil {
		h.t.Fatalf("approval %s missing from the list: %s", id, list.raw)
	}
	get := h.do("GET", govPath+"/approvals/"+id, token, nil, tenantHdr(tenant))
	if get.code != http.StatusOK {
		h.t.Fatalf("get approval = %d %s", get.code, get.raw)
	}
	return listed, get.body
}

// mayDecide reads may_decide from both reads and fails when they disagree: the list and
// the detail answer from one rule.
func (h *harness) mayDecide(token string, tenant model.TenantID, id string) bool {
	h.t.Helper()
	listed, detail := h.approvalRead(token, tenant, id)
	l, lok := listed["may_decide"].(bool)
	d, dok := detail["may_decide"].(bool)
	if !lok || !dok {
		h.t.Fatalf("an approval read must carry may_decide: list=%v detail=%v", listed["may_decide"], detail["may_decide"])
	}
	if l != d {
		h.t.Fatalf("list says may_decide=%v, detail says %v", l, d)
	}
	return l
}

// deciderIDs returns the user ids named in a read's deciders; ok is false when the read
// carries no deciders at all.
func deciderIDs(m map[string]any) (ids map[string]bool, ok bool) {
	raw, present := m["deciders"].([]any)
	if !present {
		return nil, false
	}
	ids = map[string]bool{}
	for _, p := range raw {
		ids[p.(map[string]any)["user_id"].(string)] = true
	}
	return ids, true
}

func TestApprovalReadSaysWhetherTheCallerMayDecide(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")
	_, a3 := h.roleUser(admin, tenant, "a3@x.io", "admin")

	id := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "required_approvals": 2}).body["id"].(string)

	for name, token := range map[string]string{"first admin": a2, "second admin": a3} {
		if !h.mayDecide(token, tenant, id) {
			t.Fatalf("%s may decide a request nobody has decided", name)
		}
	}
	if h.mayDecide(editor, tenant, id) {
		t.Fatal("the requester's own request must not be decidable by the requester")
	}

	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "pending" {
		t.Fatalf("first of two approvals: %d %s", rr.code, rr.raw)
	}
	// The decision handler now answers this caller 409; the read must say so first.
	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusConflict {
		t.Fatalf("a second decision by the same admin must be 409, got %d", rr.code)
	}
	if h.mayDecide(a2, tenant, id) {
		t.Fatal("after approving, the same admin may not decide again: may_decide must be false")
	}
	if !h.mayDecide(a3, tenant, id) {
		t.Fatal("a second admin still may decide the open request")
	}

	if rr := h.decide(a3, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "approved" {
		t.Fatalf("second approval: %d %s", rr.code, rr.raw)
	}
	if h.mayDecide(a3, tenant, id) || h.mayDecide(admin, tenant, id) {
		t.Fatal("a resolved request has nobody left to decide it")
	}
}

func TestApprovalReadRequesterNeverMayDecideEvenThroughAToken(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")

	own := h.createApproval(a2, tenant, map[string]any{"action": "deploy"}).body["id"].(string)
	if h.mayDecide(a2, tenant, own) {
		t.Fatal("an admin must not be told they may decide their own request")
	}

	// The same person opening the request with an API token: the actor string differs
	// from the session's, the person does not, and the decision handler refuses (403).
	tok := h.do("POST", "/v1/tokens", a2, map[string]any{"name": "mine", "tenant": tenant.String(), "role": "editor"}, nil)
	if tok.code != http.StatusCreated {
		t.Fatalf("mint token = %d %s", tok.code, tok.raw)
	}
	viaToken := h.createApproval(tok.body["token"].(string), tenant, map[string]any{"action": "deploy"}).body["id"].(string)
	if rr := h.decide(a2, tenant, viaToken, "approve"); rr.code != http.StatusForbidden {
		t.Fatalf("deciding a request opened by your own token must be 403, got %d %s", rr.code, rr.raw)
	}
	if h.mayDecide(a2, tenant, viaToken) {
		t.Fatal("a request opened by the caller's own token must answer may_decide=false")
	}
	if !h.mayDecide(admin, tenant, viaToken) {
		t.Fatal("another admin may decide it")
	}
}

func TestApprovalReadNeedsTheDecidePermission(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, requester := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_, otherEditor := h.roleUser(admin, tenant, "ed2@x.io", "editor")
	_, viewer := h.roleUser(admin, tenant, "v@x.io", "viewer")

	id := h.createApproval(requester, tenant, map[string]any{"action": "deploy"}).body["id"].(string)
	for name, token := range map[string]string{"editor": otherEditor, "viewer": viewer} {
		if rr := h.decide(token, tenant, id, "approve"); rr.code != http.StatusForbidden {
			t.Fatalf("%s must be refused the decision, got %d", name, rr.code)
		}
		if h.mayDecide(token, tenant, id) {
			t.Fatalf("%s lacks governance:approval:admin: may_decide must be false", name)
		}
	}
}

func TestApprovalReadNamesWhoElseMayDecide(t *testing.T) {
	h := newHarness(t)
	h.useApprovalAuthority()
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editorID, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	a2ID, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")
	a3ID, a3 := h.roleUser(admin, tenant, "a3@x.io", "admin")

	id := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "required_approvals": 2}).body["id"].(string)

	listed, detail := h.approvalRead(a2, tenant, id)
	for name, m := range map[string]map[string]any{"list": listed, "detail": detail} {
		got, ok := deciderIDs(m)
		if !ok || !got[a2ID] || !got[a3ID] {
			t.Fatalf("%s deciders = %v, want both admins", name, got)
		}
		if got[editorID] {
			t.Fatalf("%s names the requester as someone who may decide", name)
		}
	}

	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusOK {
		t.Fatalf("approve: %d %s", rr.code, rr.raw)
	}
	_, detail = h.approvalRead(a3, tenant, id)
	if got, _ := deciderIDs(detail); got[a2ID] || !got[a3ID] {
		t.Fatalf("whoever already decided is not asked again: deciders = %v", got)
	}

	// An admin who opens a request holds the right yet is never named for it, whether
	// they opened it with their session or with their own token.
	tok := h.do("POST", "/v1/tokens", a2, map[string]any{"name": "mine", "tenant": tenant.String(), "role": "editor"}, nil)
	if tok.code != http.StatusCreated {
		t.Fatalf("mint token = %d %s", tok.code, tok.raw)
	}
	for name, opener := range map[string]string{"session": a2, "token": tok.body["token"].(string)} {
		own := h.createApproval(opener, tenant, map[string]any{"action": "deploy"}).body["id"].(string)
		_, detail := h.approvalRead(a3, tenant, own)
		if got, _ := deciderIDs(detail); got[a2ID] || !got[a3ID] {
			t.Fatalf("a request a2 opened with their %s: deciders = %v, want a3 and never a2", name, got)
		}
	}

	// A caller who may not read users is not handed the roster of who holds the right.
	_, forEditor := h.approvalRead(editor, tenant, id)
	if d, present := forEditor["deciders"]; present {
		t.Fatalf("a caller without user:read must not see the deciders, got %v", d)
	}
}

func TestApprovalReadWithoutAuthorityNamesNoDeciders(t *testing.T) {
	h := newHarness(t) // no UseApprovalAuthority: the module cannot enumerate people
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")
	id := h.createApproval(editor, tenant, map[string]any{"action": "deploy"}).body["id"].(string)

	_, detail := h.approvalRead(a2, tenant, id)
	if d, present := detail["deciders"]; present {
		t.Fatalf("with no authority wired the read must name nobody, got %v", d)
	}
	if detail["may_decide"] != true {
		t.Fatalf("may_decide does not depend on the roster: %v", detail["may_decide"])
	}
}

// auditRows counts the tenant's audit events: the trail a read must leave as it found it.
func (h *harness) auditRows(tenant model.TenantID) int {
	h.t.Helper()
	n := 0
	err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(model.AuditEvent) error { n++; return nil })
	})
	if err != nil {
		h.t.Fatalf("walk audit: %v", err)
	}
	return n
}

// The console polls the list every few seconds; enumerating people to name the deciders
// must not append the checks it runs to the tenant's audit chain.
func TestApprovalReadLeavesTheAuditTrailAlone(t *testing.T) {
	h := newHarness(t)
	h.useApprovalAuthority()
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_, viewer := h.roleUser(admin, tenant, "v@x.io", "viewer")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")
	id := h.createApproval(editor, tenant, map[string]any{"action": "deploy"}).body["id"].(string)

	for name, token := range map[string]string{"admin": a2, "editor": editor, "viewer": viewer} {
		before := h.auditRows(tenant)
		h.approvalRead(token, tenant, id)
		if after := h.auditRows(tenant); after != before {
			t.Errorf("a %s reading the list and the detail of an approval added %d audit rows", name, after-before)
		}
	}
}

// Nobody else may decide is an answer, not a missing one: the console must show a stuck
// request, and absent deciders mean the server could not say.
func TestApprovalReadSaysNobodyElseMayDecide(t *testing.T) {
	h := newHarness(t)
	h.useApprovalAuthority()
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.roleUser(admin, tenant, "ed@x.io", "editor")

	// The only holder of the right opens the request, so no one else can decide it.
	id := h.createApproval(admin, tenant, map[string]any{"action": "deploy"}).body["id"].(string)
	listed, detail := h.approvalRead(admin, tenant, id)
	for name, m := range map[string]map[string]any{"list": listed, "detail": detail} {
		if m["may_decide"] != false {
			t.Fatalf("%s: the requester may not decide their own request: %v", name, m["may_decide"])
		}
		got, ok := deciderIDs(m)
		if !ok || len(got) != 0 {
			t.Fatalf("%s: deciders = %v (present=%v), want present and empty", name, got, ok)
		}
	}
}
