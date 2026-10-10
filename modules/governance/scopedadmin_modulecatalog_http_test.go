// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// the HTTP half. Every other test in this unit works on Go values, which means a
// JSON tag typo on the catalog, or a handler that never reaches the new validation, would
// ship green while the console renders an empty grid. These drive the real
// /v1/m/governance/rbac/* surface through the server.
//
// The permissions used here are the governance module's OWN declarations, seeded into the
// catalog by mountModules when the harness builds its server — not a synthetic fixture.
// Registering by hand before newHarness would be pointless: mounting REBUILDS the catalog
// from the mounted set (that is the documented behavior), so the only permissions a
// harness server can confer are the ones its modules declare. Using them keeps the test
// honest about what an engine really offers.
const (
	permApprovalRead = "governance:approval:read" // declared: read, write, admin
	permNHIRead      = "governance:nhi:read"      // declared: read, write, admin
	permNHIWrite     = "governance:nhi:write"
	// governance declares agentcore-export at ADMIN only — so ":read" is a permission
	// whose KIND is registered and whose whole form is not. It is the case that separates
	// "match by kind" from "match the whole permission".
	permExportAdmin      = "governance:agentcore-export:admin"
	permExportReadUndecl = "governance:agentcore-export:read"
	permNHIAdmin         = "governance:nhi:admin"
	permRBACAdmin        = "governance:rbac:admin"
)

func strSet(v any) map[string]bool {
	out := map[string]bool{}
	arr, ok := v.([]any)
	if !ok {
		return out
	}
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out[s] = true
		}
	}
	return out
}

// TestRBACCatalogWireShape pins the JSON the console actually consumes. The console
// filters its permission grid on `permissions` and its scope-class picker on
// `tree_kinds`; if either key is missing or misspelled the grid silently offers nothing
// and no Go-level test notices.
func TestRBACCatalogWireShape(t *testing.T) {
	t.Cleanup(auth.ResetModuleCatalog)
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", "/v1/m/governance/rbac/catalog", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("catalog = %d %s", r.code, r.raw)
	}
	for _, key := range []string{"kinds", "tree_kinds", "permissions", "verbs", "builtin_roles", "scope_trees"} {
		if _, ok := r.body[key]; !ok {
			t.Errorf("catalog response is missing %q — the console reads this key: %s", key, r.raw)
		}
	}
	kinds, treeKinds, perms := strSet(r.body["kinds"]), strSet(r.body["tree_kinds"]), strSet(r.body["permissions"])

	if !treeKinds["agent"] || len(treeKinds) != 15 {
		t.Errorf("tree_kinds must be the 15 scope-tree kinds, got %d: %v", len(treeKinds), r.body["tree_kinds"])
	}
	if treeKinds["governance:approval"] {
		t.Error("a module kind must never appear in tree_kinds — the scope-class picker filters on it")
	}
	if !kinds["agent"] || !kinds["governance:approval"] || !kinds["governance:nhi"] {
		t.Errorf("kinds must carry tree kinds AND the mounted module's kinds, got %v", r.body["kinds"])
	}
	// `permissions` is the exact declared set, so the grid can tell that
	// governance:agentcore-export has an admin verb and no read verb.
	if !perms[permApprovalRead] || !perms[permNHIWrite] || !perms[permExportAdmin] {
		t.Errorf("permissions must list the mounted module's declared permissions, got %v", r.body["permissions"])
	}
	if perms[permExportReadUndecl] {
		t.Errorf("%s was never declared: publishing it would put a checkbox on screen that can only 400", permExportReadUndecl)
	}
	if perms["agent:read"] {
		t.Error("permissions is the MODULE set; core kinds are covered by kinds × verbs")
	}
}

// TestCustomRoleWithModulePermsOverHTTP is the operator story end to end: authoring a role
// that carries module permissions and omits one on purpose, and being refused one that no
// module declares.

// TestModuleOnlyGrantScopeRulesOverHTTP proves both refusals reach the wire: the
// role-shaped inert grant and the class-shaped one. A grant that authorizes nothing must
// not be storable, and the API must say why.

// --- structured subtraction over HTTP ---------------------------------------------

// TestSubtractionRoundTripsOverHTTP is the operator story of step 2 end to end: a role
// declared as a live BASE minus one permission, authored and read back through the real
// API. Without this, a JSON tag typo on base_role/excludes would store a role that
// silently confers everything the base does — the exact failure the feature must not have.

// TestSubtractionValidationOverHTTP: a base that is not a built-in role, and an exclusion
// the catalog does not know, are both 400 — an operator who mistypes an exclusion must be
// told, not left believing they capped something.

// TestExcludingRbacAdminStopsRedelegation is the explicit check for the residual this
// design could have inherited: the delegation permit is synthesized, and it does NOT pass
// through permSubset in canDelegate. The question is whether a role authored as "may
// administer this surface, may NOT re-delegate it" actually holds.
//
// It does, and the mechanism is the route table: every WRITE on the rbac surface requires
// governance:rbac:admin (modules/governance/governance.go:511, :513, :514, :516, :518,
// :519, :521, :523), so a subject whose role excludes it cannot mint any grant at all —
// the un-checked permit cannot be reached to be abused. The CONTROL below is what makes
// this a proof rather than a coincidence: the same role WITHOUT the exclusion does get
// through, so the exclusion is demonstrably what closed it.

// TestRemovingAnExclusionIsItselfADelegation: an exclusion can be undone by EDITING the
// role after it has been assigned, and that edit widens what a named subject holds. It has
// to pass the editor's own ceiling, exactly like minting the wider grant directly would —
// otherwise subtraction becomes a way to launder authority past canDelegate: author a
// narrow role, get it granted, then quietly widen it.
