// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"strings"
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
func TestCustomRoleWithModulePermsOverHTTP(t *testing.T) {
	t.Cleanup(auth.ResetModuleCatalog)
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	// "can read approvals and NHI, but cannot write NHI" — the carve-out that was
	// impossible before because none of these could enter a custom role at all.
	body := map[string]any{
		"name":        "approvals-reader",
		"permissions": []string{permApprovalRead, permNHIRead},
	}
	if r := h.do("POST", "/v1/m/governance/rbac/roles", admin, body, hdr); r.code != http.StatusCreated {
		t.Fatalf("create role with module perms = %d %s", r.code, r.raw)
	}
	r := h.do("GET", "/v1/m/governance/rbac/roles/approvals-reader", admin, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("get role = %d %s", r.code, r.raw)
	}
	got := strSet(r.body["permissions"])
	if !got[permApprovalRead] || !got[permNHIRead] {
		t.Errorf("role lost its module permissions on round-trip: %s", r.raw)
	}
	if got[permNHIWrite] {
		t.Error("the role must NOT carry the permission it deliberately omits")
	}

	// A permission whose KIND is registered but whose whole form is not: 400, not a
	// silently-inert entry in the role.
	bad := map[string]any{"name": "bogus", "permissions": []string{permExportReadUndecl}}
	if r := h.do("POST", "/v1/m/governance/rbac/roles", admin, bad, hdr); r.code != http.StatusBadRequest {
		t.Errorf("undeclared verb on a declared kind = %d, want 400: %s", r.code, r.raw)
	}
	// And a namespace no mounted module registered at all.
	bad2 := map[string]any{"name": "bogus2", "permissions": []string{"nosuch:thing:read"}}
	if r := h.do("POST", "/v1/m/governance/rbac/roles", admin, bad2, hdr); r.code != http.StatusBadRequest {
		t.Errorf("unregistered module permission = %d, want 400: %s", r.code, r.raw)
	}
}

// TestModuleOnlyGrantScopeRulesOverHTTP proves both refusals reach the wire: the
// role-shaped inert grant and the class-shaped one. A grant that authorizes nothing must
// not be storable, and the API must say why.
func TestModuleOnlyGrantScopeRulesOverHTTP(t *testing.T) {
	t.Cleanup(auth.ResetModuleCatalog)
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	role := map[string]any{"name": "approvals-reader", "permissions": []string{permApprovalRead}}
	if r := h.do("POST", "/v1/m/governance/rbac/roles", admin, role, hdr); r.code != http.StatusCreated {
		t.Fatalf("create role = %d %s", r.code, r.raw)
	}
	uid, _ := h.roleUser(admin, tenant, "op@acme.com", auth.RoleViewer)

	// At TENANT scope the module-only grant is exactly the point of the unit: accepted.
	tenantGrant := map[string]any{
		"subject_kind": "user", "subject_ref": uid,
		"role": "approvals-reader", "role_custom": true, "scope_tree": "tenant",
	}
	if r := h.do("POST", "/v1/m/governance/rbac/grants", admin, tenantGrant, hdr); r.code != http.StatusCreated {
		t.Fatalf("module-only grant at tenant scope = %d %s", r.code, r.raw)
	}

	if r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Payments", "slug": "payments"}, hdr); r.code != http.StatusCreated {
		t.Fatalf("create workspace = %d %s", r.code, r.raw)
	}
	// Bounded to a workspace it can never bite: refused rather than stored inert.
	wsGrant := map[string]any{
		"subject_kind": "user", "subject_ref": uid,
		"role": "approvals-reader", "role_custom": true, "scope_tree": "workspace", "scope_ref": "payments",
	}
	r := h.do("POST", "/v1/m/governance/rbac/grants", admin, wsGrant, hdr)
	if r.code != http.StatusBadRequest {
		t.Fatalf("module-only grant at workspace scope = %d, want 400 (it would authorize nothing): %s", r.code, r.raw)
	}
	if r.raw == "" {
		t.Error("the refusal must carry a reason the operator can act on")
	}

	// The class-shaped version of the same mistake is refused too.
	classGrant := map[string]any{
		"subject_kind": "user", "subject_ref": uid,
		"role": auth.RoleViewer, "scope_tree": "workspace", "scope_ref": "payments",
		"scope_class": "governance:approval",
	}
	if r := h.do("POST", "/v1/m/governance/rbac/grants", admin, classGrant, hdr); r.code != http.StatusBadRequest {
		t.Errorf("module scope_class on a workspace scope = %d, want 400: %s", r.code, r.raw)
	}
}

// --- structured subtraction over HTTP ---------------------------------------------

// TestSubtractionRoundTripsOverHTTP is the operator story of step 2 end to end: a role
// declared as a live BASE minus one permission, authored and read back through the real
// API. Without this, a JSON tag typo on base_role/excludes would store a role that
// silently confers everything the base does — the exact failure the feature must not have.
func TestSubtractionRoundTripsOverHTTP(t *testing.T) {
	t.Cleanup(auth.ResetModuleCatalog)
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	body := map[string]any{
		"name":      "editor-no-nhi-write",
		"base_role": auth.RoleEditor,
		"excludes":  []string{permNHIWrite},
	}
	if r := h.do("POST", "/v1/m/governance/rbac/roles", admin, body, hdr); r.code != http.StatusCreated {
		t.Fatalf("create role with base+excludes = %d %s", r.code, r.raw)
	}
	r := h.do("GET", "/v1/m/governance/rbac/roles/editor-no-nhi-write", admin, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("get role = %d %s", r.code, r.raw)
	}
	if got, _ := r.body["base_role"].(string); got != auth.RoleEditor {
		t.Errorf("base_role did not round-trip: %s", r.raw)
	}
	if ex := strSet(r.body["excludes"]); !ex[permNHIWrite] {
		t.Errorf("excludes did not round-trip: %s", r.raw)
	}

	// A grant of the role must be accepted (the subtracted set is inside a tenant admin's
	// domain), which is what proves the role is usable and not merely storable.
	uid, _ := h.roleUser(admin, tenant, "op@acme.com", auth.RoleViewer)
	grant := map[string]any{
		"subject_kind": "user", "subject_ref": uid,
		"role": "editor-no-nhi-write", "role_custom": true, "scope_tree": "tenant",
	}
	if r := h.do("POST", "/v1/m/governance/rbac/grants", admin, grant, hdr); r.code != http.StatusCreated {
		t.Fatalf("grant the subtracted role = %d %s", r.code, r.raw)
	}
}

// TestSubtractionValidationOverHTTP: a base that is not a built-in role, and an exclusion
// the catalog does not know, are both 400 — an operator who mistypes an exclusion must be
// told, not left believing they capped something.
func TestSubtractionValidationOverHTTP(t *testing.T) {
	t.Cleanup(auth.ResetModuleCatalog)
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	bad := []struct {
		name string
		body map[string]any
		want string
	}{
		{"a custom role as base", map[string]any{"name": "a", "base_role": "some-custom-role", "permissions": []string{permApprovalRead}}, "base_role"},
		{"a typo'd exclusion", map[string]any{"name": "b", "base_role": auth.RoleEditor, "excludes": []string{"governance:nhi:wrtie"}}, "not a scopeable permission"},
		{"an exclusion outside the catalog", map[string]any{"name": "c", "base_role": auth.RoleEditor, "excludes": []string{"nosuch:thing:read"}}, "not a scopeable permission"},
	}
	for _, c := range bad {
		r := h.do("POST", "/v1/m/governance/rbac/roles", admin, c.body, hdr)
		if r.code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", c.name, r.code, r.raw)
			continue
		}
		if !strings.Contains(r.raw, c.want) {
			t.Errorf("%s: the message must say why (want %q), got %s", c.name, c.want, r.raw)
		}
	}
}

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
func TestExcludingRbacAdminStopsRedelegation(t *testing.T) {
	t.Cleanup(auth.ResetModuleCatalog)
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	// Two admin-capable roles over the same surface; one may re-delegate, one may not.
	for _, r := range []map[string]any{
		{"name": "nhi-admin", "permissions": []string{permNHIAdmin}},
		{"name": "nhi-admin-no-redelegate", "permissions": []string{permNHIAdmin}, "excludes": []string{permRBACAdmin}},
	} {
		if resp := h.do("POST", "/v1/m/governance/rbac/roles", admin, r, hdr); resp.code != http.StatusCreated {
			t.Fatalf("create role %v = %d %s", r["name"], resp.code, resp.raw)
		}
	}

	try := func(email, role string) int {
		uid, tok := h.roleUser(admin, tenant, email, auth.RoleViewer)
		h.stepUp(tok)
		grant := map[string]any{
			"subject_kind": "user", "subject_ref": uid,
			"role": role, "role_custom": true, "scope_tree": "tenant",
		}
		if resp := h.do("POST", "/v1/m/governance/rbac/grants", admin, grant, hdr); resp.code != http.StatusCreated {
			t.Fatalf("admin granting %s = %d %s", role, resp.code, resp.raw)
		}
		// Now the delegatee itself tries to mint a further grant.
		sub := map[string]any{
			"subject_kind": "user", "subject_ref": uid,
			"role": role, "role_custom": true, "scope_tree": "tenant",
		}
		return h.do("POST", "/v1/m/governance/rbac/grants", tok, sub, hdr).code
	}

	// CONTROL: without the exclusion the delegation permit lets the delegatee re-delegate,
	// so the route is genuinely reachable and this test is exercising the right thing.
	if got := try("can@acme.com", "nhi-admin"); got == http.StatusForbidden {
		t.Fatalf("control failed: an admin-capable role WITHOUT the exclusion should reach the grant route, got %d", got)
	}
	// With the exclusion, re-delegation is refused.
	if got := try("cannot@acme.com", "nhi-admin-no-redelegate"); got != http.StatusForbidden {
		t.Errorf("a role that excludes %s must not be able to re-delegate, got %d", permRBACAdmin, got)
	}
}

// TestRemovingAnExclusionIsItselfADelegation: an exclusion can be undone by EDITING the
// role after it has been assigned, and that edit widens what a named subject holds. It has
// to pass the editor's own ceiling, exactly like minting the wider grant directly would —
// otherwise subtraction becomes a way to launder authority past canDelegate: author a
// narrow role, get it granted, then quietly widen it.
func TestRemovingAnExclusionIsItselfADelegation(t *testing.T) {
	t.Cleanup(auth.ResetModuleCatalog)
	h := newHarness(t)
	root := h.adminLogin() // superadmin: the unbounded root
	tenant := h.createOrg(root, "acme")
	hdr := tenantHdr(tenant)

	// A tenant ADMIN is the editor under test: it does NOT hold the tree `admin` verb
	// (owner-only), so a role based on owner is above its ceiling unless those verbs are
	// subtracted.
	_, adminTok := h.roleUser(root, tenant, "ta@acme.com", auth.RoleAdmin)
	h.stepUp(adminTok)

	var excl []string
	for _, k := range auth.TreeScopeableKinds() {
		excl = append(excl, k+":"+auth.VerbAdmin)
	}
	role := map[string]any{"name": "ownerish", "base_role": auth.RoleOwner, "excludes": excl}
	if r := h.do("POST", "/v1/m/governance/rbac/roles", adminTok, role, hdr); r.code != http.StatusCreated {
		t.Fatalf("create the subtracted role = %d %s", r.code, r.raw)
	}
	uid, _ := h.roleUser(root, tenant, "sub@acme.com", auth.RoleViewer)
	grant := map[string]any{
		"subject_kind": "user", "subject_ref": uid,
		"role": "ownerish", "role_custom": true, "scope_tree": "tenant",
	}
	// CONTROL: the SUBTRACTED role is inside the tenant admin's ceiling, so the grant is
	// accepted. Without this the 403 below could just mean "the admin could never grant it".
	if r := h.do("POST", "/v1/m/governance/rbac/grants", adminTok, grant, hdr); r.code != http.StatusCreated {
		t.Fatalf("control failed: the subtracted role must be grantable by a tenant admin, got %d %s", r.code, r.raw)
	}

	// Now the same admin tries to remove the exclusions from the ASSIGNED role. That would
	// hand the subject the owner-tier verbs the admin itself does not hold.
	widen := map[string]any{"name": "ownerish", "base_role": auth.RoleOwner}
	r := h.do("PUT", "/v1/m/governance/rbac/roles/ownerish", adminTok, widen, hdr)
	if r.code != http.StatusForbidden {
		t.Errorf("removing an exclusion from an assigned role is a delegation and must hit the ceiling, got %d %s", r.code, r.raw)
	}

	// The role must be unchanged — a refused edit that half-applied would be worse.
	g := h.do("GET", "/v1/m/governance/rbac/roles/ownerish", adminTok, nil, hdr)
	if g.code != http.StatusOK {
		t.Fatalf("get role = %d %s", g.code, g.raw)
	}
	if ex := strSet(g.body["excludes"]); len(ex) != len(excl) {
		t.Errorf("the refused edit must leave the exclusions intact, got %s", g.raw)
	}
}
