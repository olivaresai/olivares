// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// Workspace bindings declare a collection resource to the scoped engine. The
// shared collection UID cannot supply an explicit grant across the filter.
func TestInheritanceFilterCollectionGrantOnWorkspaceBinding(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")
	viewer := h.principalFor(admin, tenant, "viewer@acme.io", auth.RoleViewer)
	bot := h.createAgent(tenant, "default-bot", model.ID(""))
	h.createSession(tenant, "default-session", bot.ID, model.ID(""))
	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "collection-filter", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
		"cred_name": "payments", "cred_ref_kind": "vault", "cred_ref": "sources/payments",
	}); r.code != http.StatusCreated {
		t.Fatalf("binding = %d %s", r.code, r.raw)
	}
	cases := []struct {
		name, source  string
		allowFiltered bool
	}{
		{"head", `permit(principal, action == Action::"model:read", resource == Resource::"*");`, false},
		{"when", `permit(principal, action == Action::"model:read", resource) when { resource == Resource::"*" };`, false},
		{"head and workspace", `permit(principal, action == Action::"model:read", resource == Resource::"*") when { resource in Workspace::"payments" };`, true},
		{"when and workspace", `permit(principal, action == Action::"model:read", resource) when { resource == Resource::"*" && resource in Workspace::"payments" };`, true},
	}
	for _, filtered := range []bool{false, true} {
		if filtered {
			if r := h.do("POST", "/v1/m/governance/rbac/inheritance-filters", admin,
				map[string]any{"scope_tree": "workspace", "scope_ref": "payments", "scope_class": "model"}, tenantHdr(tenant)); r.code != http.StatusCreated {
				t.Fatalf("filter = %d %s", r.code, r.raw)
			}
		}
		for _, tc := range cases {
			h.publishGrant(admin, tenant, tc.source)
			for _, who := range []string{"agent", "session"} {
				var d sourcescope.Decision
				var err error
				if who == "agent" {
					d, err = h.resolver.ResolveForAgent(t.Context(), tenant, viewer, bot.ExternalID, sourcescope.SourceModel, "collection-filter")
				} else {
					d, err = h.resolver.ResolveForSession(t.Context(), tenant, viewer, "default-session", sourcescope.SourceModel, "collection-filter")
				}
				want := !filtered || tc.allowFiltered
				if err != nil || d.Allowed != want {
					t.Errorf("%s/%s filtered=%v: allow=%v (%s), err=%v, want %v", tc.name, who, filtered, d.Allowed, d.Reason, err, want)
				}
				if !want && d.Cred != nil {
					t.Errorf("%s/%s: denied source exposed credential", tc.name, who)
				}
				if want && (d.Cred == nil || d.Cred.Name != "payments" || d.Cred.Ref != "sources/payments") {
					t.Errorf("%s/%s: allowed source lost its binding credential", tc.name, who)
				}
			}
		}
	}
}

// The resolver re-derives the tenant-wide RBAC term of the authorization algebra
// (TestTenantRBACSeesAll), so an inheritance filter has to remove it here too: a filter on
// the scope a binding names says a role from above no longer reaches that class there.
func TestInheritanceFilterRemovesTenantWideRBACFromSourceScoping(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")
	viewer := h.principalFor(admin, tenant, "viewer@acme.io", auth.RoleViewer)
	inDefault := h.createAgent(tenant, "v-bot", model.ID("")) // out of payments

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-filter", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
	}); r.code != http.StatusCreated {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}
	if d := h.resolveAgent(tenant, viewer, inDefault.ExternalID, sourcescope.SourceModel, "m-filter"); !d.Allowed {
		t.Fatalf("baseline: a tenant viewer sees a bound model by role, got %+v", d)
	}

	r := h.do("POST", "/v1/m/governance/rbac/inheritance-filters", admin,
		map[string]any{"scope_tree": "workspace", "scope_ref": "payments", "scope_class": "model"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create filter = %d %s", r.code, r.raw)
	}
	if d := h.resolveAgent(tenant, viewer, inDefault.ExternalID, sourcescope.SourceModel, "m-filter"); d.Allowed {
		t.Errorf("with a filter on the binding's scope the role must not open the source, got %+v", d)
	}

	// Another class on the same node is untouched: the filter names one class.
	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "provider", "source_ref": "p-filter", "scope_tree": "workspace", "scope_ref": "payments", "enabled": true,
	}); r.code != http.StatusCreated {
		t.Fatalf("create provider binding = %d %s", r.code, r.raw)
	}
	if d := h.resolveAgent(tenant, viewer, inDefault.ExternalID, sourcescope.SourceProvider, "p-filter"); !d.Allowed {
		t.Errorf("a filter on class model must not touch a provider source, got %+v", d)
	}

	if rr := h.do("DELETE", "/v1/m/governance/rbac/inheritance-filters/"+r.body["id"].(string), admin, nil, tenantHdr(tenant)); rr.code != http.StatusNoContent {
		t.Fatalf("delete filter = %d %s", rr.code, rr.raw)
	}
	if d := h.resolveAgent(tenant, viewer, inDefault.ExternalID, sourcescope.SourceModel, "m-filter"); !d.Allowed {
		t.Errorf("with the filter gone the role opens the source again, got %+v", d)
	}
}

// Two allow bindings are two routes to the source. A filter removes RBAC and its
// credential only from that binding, while a matching forbid still denies absolutely.
func TestInheritanceFilterOnOneBindingPreservesSiblingRBACAndCredential(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")
	h.createWorkspace(tenant, "research")
	viewer := h.principalFor(admin, tenant, "viewer@acme.io", auth.RoleViewer)
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)
	bot := h.createAgent(tenant, "default-bot", model.ID(""))
	h.createSession(tenant, "default-session", bot.ID, model.ID(""))
	body := func(scope string) map[string]any {
		return map[string]any{"source_type": "model", "source_ref": "multi-filter", "scope_tree": "workspace", "scope_ref": scope, "enabled": true,
			"cred_name": scope, "cred_ref_kind": "vault", "cred_ref": "sources/" + scope}
	}
	if r := h.createBinding(admin, tenant, body("payments")); r.code != http.StatusCreated {
		t.Fatalf("first binding = %d %s", r.code, r.raw)
	}
	h.createBindingApproved(admin, approver, tenant, body("research"))
	assertDecision := func(wantAllow bool, wantCred string) {
		t.Helper()
		for _, who := range []string{"agent", "session"} {
			var d sourcescope.Decision
			var err error
			if who == "agent" {
				d, err = h.resolver.ResolveForAgent(t.Context(), tenant, viewer, bot.ExternalID, sourcescope.SourceModel, "multi-filter")
			} else {
				d, err = h.resolver.ResolveForSession(t.Context(), tenant, viewer, "default-session", sourcescope.SourceModel, "multi-filter")
			}
			if err != nil || d.Allowed != wantAllow {
				t.Errorf("%s: allow=%v, err=%v, want %v (%s)", who, d.Allowed, err, wantAllow, d.Reason)
			}
			if wantCred != "" && (d.Cred == nil || d.Cred.Name != wantCred || d.Cred.Ref != "sources/"+wantCred) {
				t.Errorf("%s: credential=%+v, want %s", who, d.Cred, wantCred)
			}
			if !wantAllow && d.Cred != nil {
				t.Errorf("%s: denied source exposed credential %+v", who, d.Cred)
			}
		}
	}
	filter := func(scope string) string {
		t.Helper()
		r := h.do("POST", "/v1/m/governance/rbac/inheritance-filters", admin,
			map[string]any{"scope_tree": "workspace", "scope_ref": scope, "scope_class": "model"}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("filter = %d %s", r.code, r.raw)
		}
		return r.body["id"].(string)
	}
	assertDecision(true, "payments")
	payFilter := filter("payments")
	assertDecision(true, "research")
	filter("research")
	assertDecision(false, "")
	if r := h.do("DELETE", "/v1/m/governance/rbac/inheritance-filters/"+payFilter, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete filter = %d %s", r.code, r.raw)
	}
	assertDecision(true, "payments")
	if r := h.createBinding(admin, tenant, map[string]any{"source_type": "model", "source_ref": "multi-filter", "scope_tree": "user", "scope_ref": viewer.UserID.String(), "effect": "forbid", "enabled": true}); r.code != http.StatusCreated {
		t.Fatalf("forbid = %d %s", r.code, r.raw)
	}
	assertDecision(false, "")
}
