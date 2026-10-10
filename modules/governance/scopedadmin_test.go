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
)

// rbac POSTs to /v1/m/governance/rbac/<path> with the tenant header.
func (h *harness) rbac(method, path, token string, tenant model.TenantID, body any) resp {
	return h.do(method, "/v1/m/governance/rbac/"+path, token, body, tenantHdr(tenant))
}

// principalOf authenticates a session/token string into a Principal (for engine-level
// Scoped() assertions that need the real UserID parent).
func (h *harness) principalOf(token string) auth.Principal {
	h.t.Helper()
	p, err := h.authr.Authenticate(context.Background(), token)
	if err != nil {
		h.t.Fatalf("authenticate: %v", err)
	}
	return p
}

// --- e2e: a workspace-admin grant authored via REST enforces on the real path -------

// --- a custom role + permission-group confers EXACTLY its bundle ---------------------

// --- the per-scope ceiling: a tenant admin cannot delegate above its own role --------

// --- scoped-admin SUB-delegation, bounded to its scope (incl. agent-group containment)

// --- the endpoint RBAC gate: a non-admin cannot reach the write API ------------------

// --- validation negatives (deny-closed) ---------------------------------------------

// --- a definition still in use cannot be deleted ------------------------------------

// --- the catalog endpoint feeds the role editor --------------------------------

// --- the managed projection composes (unions) with the free-form Cedar surface ------

// --- agent-group SCOPE projection (resource-side fold) ------------------------------

// --- a confined (non-member) workspace-admin: scope grant without a tenant floor -----

// --- the update path is ceilinged too (regression for the review's HIGH finding) -----

// --- revoke is ceilinged: a scoped-admin cannot revoke outside its authority ---------

// --- an agent_group scope rejects a non-agent resource-class (silently-inert guard) --

// --- the delegation-authority read surfaces the actor's ceiling ---------------

// anyEq reports whether a []any of strings contains s.
func anyEq(in []any, s string) bool {
	for _, v := range in {
		if str, ok := v.(string); ok && str == s {
			return true
		}
	}
	return false
}

func TestScopedAdminCatalog(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "catco")

	r := h.rbac("GET", "catalog", admin, tenant, nil)
	if r.code != http.StatusOK {
		t.Fatalf("catalog = %d %s", r.code, r.raw)
	}
	kinds, _ := r.body["kinds"].([]any)
	verbs, _ := r.body["verbs"].([]any)
	if !anyEq(kinds, "agent") || !anyEq(kinds, "model") {
		t.Errorf("catalog kinds must include agent/model, got %v", kinds)
	}
	if !anyEq(verbs, "admin") || !anyEq(verbs, "read") {
		t.Errorf("catalog verbs must include read/admin, got %v", verbs)
	}
}
