//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
)

func TestCommunityAuthorizationManagementUnavailable(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "community")
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/rbac/roles", map[string]any{"name": "custom", "permissions": []string{"agent:read"}}},
		{"PUT", "/rbac/roles/custom", map[string]any{"permissions": []string{"agent:write"}}},
		{"POST", "/rbac/permission-groups", map[string]any{"name": "bundle", "permissions": []string{"agent:read"}}},
		{"PUT", "/rbac/permission-groups/bundle", map[string]any{"permissions": []string{"agent:write"}}},
		{"POST", "/rbac/grants", map[string]any{"subject_kind": "role", "subject_ref": "viewer", "role": "editor", "scope_tree": "tenant"}},
		{"POST", "/pdp/publish", map[string]any{"engine": "cedar", "source": `permit(principal, action, resource);`}},
		{"POST", "/pdp/rollback", map[string]any{"engine": "cedar", "revision": 1}},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := h.do(tc.method, "/v1/m/governance"+tc.path, admin, tc.body, tenantHdr(tenant))
			if r.code != http.StatusNotImplemented {
				t.Fatalf("Community authoring = %d %s; want 501", r.code, r.raw)
			}
		})
	}
	for _, path := range []string{"roles", "permission-groups", "grants"} {
		r := h.do("GET", "/v1/m/governance/rbac/"+path, admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK || len(items(r)) != 0 {
			t.Fatalf("refused management wrote data: %s = %d %s", path, r.code, r.raw)
		}
	}
}

func TestCommunityStoredCedarUpgrade(t *testing.T) {
	for _, effect := range []string{"permit", "forbid"} {
		t.Run(effect, func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "upgrade")
			_, viewer := h.roleUser(admin, tenant, "viewer@upgrade.io", auth.RoleViewer)
			agent := h.createAgent(tenant, "agent", "")
			source := effect + `(principal in Role::"viewer", action == Action::"agent:write", resource);`
			if effect == "forbid" {
				source = `forbid(principal, action == Action::"agent:write", resource);`
			}
			seedStoredCedar(t, h, tenant, "cedar", source)
			if err := h.gov.ReloadActivePDP(context.Background(), tenant); err != nil {
				t.Fatal(err)
			}
			actor, want := viewer, http.StatusNoContent
			if effect == "forbid" {
				// A forbid of a built-in grant must survive the upgrade too.
				actor, want = admin, http.StatusForbidden
			}
			r := h.do("DELETE", "/v1/agents/"+agent.ID.String(), actor, nil, tenantHdr(tenant))
			if r.code != want {
				t.Fatalf("stored %s enforcement = %d %s; want %d", effect, r.code, r.raw, want)
			}
			r = h.do("GET", "/v1/m/governance/pdp/active?engine=cedar", admin, nil, tenantHdr(tenant))
			if r.code != http.StatusOK {
				t.Fatalf("read retained policy: %d %s", r.code, r.raw)
			}
			r = h.do("DELETE", "/v1/m/governance/pdp/active?engine=cedar", admin, nil, tenantHdr(tenant))
			if r.code != http.StatusOK {
				t.Fatalf("disable stored policy: %d %s", r.code, r.raw)
			}
			// History stays readable, while the authored policy is empty now.
			r = h.do("GET", "/v1/m/governance/pdp/versions", admin, nil, tenantHdr(tenant))
			if r.code != http.StatusOK || len(items(r)) < 2 {
				t.Fatalf("history lost: %d %s", r.code, r.raw)
			}
			other := h.createAgent(tenant, "after-disable", "")
			want = http.StatusForbidden
			if effect == "forbid" {
				want = http.StatusNoContent
			}
			r = h.do("DELETE", "/v1/agents/"+other.ID.String(), actor, nil, tenantHdr(tenant))
			if r.code != want {
				t.Fatalf("disabled policy enforcement = %d %s; want %d", r.code, r.raw, want)
			}
		})
	}
}

// Seed an existing installation through the real store, rather than calling a paid
// management route in the Community fixture. The legacy active flag is supported.
func seedStoredCedar(t *testing.T, h *harness, tenant model.TenantID, surface, source string) {
	t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		epochs := sc.(store.AuthorizationEpochStore)
		current, err := epochs.ReadAuthorizationEpoch(context.Background())
		if err != nil {
			return err
		}
		if _, err := epochs.BumpAuthorizationEpoch(context.Background(), current); err != nil {
			return err
		}
		repo, err := sc.Ext(model.Kind("governance.policy_revision"))
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: 100})
		if err != nil {
			return err
		}
		var next int64 = 1
		for _, row := range rows {
			if row.String("surface") == surface && row.Int("revision") >= next {
				next = row.Int("revision") + 1
			}
		}
		_, err = repo.Create(context.Background(), model.Record{"surface": surface, "revision": next, "content": source, "author": "upgrade-fixture", "validated": true, "active": true, "note": "stored before upgrade"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) publishGrant(_ string, tenant model.TenantID, src string) {
	testsupport.SeedCedar(h.t, h.st, tenant, src, h.gov)
}

func createGrant(t *testing.T, h *harness, _ string, tenant model.TenantID, grant map[string]any) {
	t.Helper()
	record := model.Record{"subject_kind": grant["subject_kind"], "subject_ref": grant["subject_ref"], "grant_role": grant["role"], "role_custom": grant["role_custom"] == true, "scope_tree": grant["scope_tree"], "scope_ref": "", "scope_class": "", "created_by": "upgrade-fixture", "note": ""}
	for _, field := range []string{"scope_ref", "scope_class", "role_custom"} {
		if value, ok := grant[field]; ok {
			record[field] = value
		}
	}
	if err := governance.SeedStoredAuthorizationForTest(context.Background(), h.st, tenant, map[model.Kind][]model.Record{"governance.scoped_grant": {record}}); err != nil {
		t.Fatal(err)
	}
	if err := h.gov.ReloadActivePDP(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
}

func TestCommunityStoredCustomRoleAndGrantUpgrade(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "retained-roles")
	uid, viewer := h.roleUser(admin, tenant, "viewer@retained.io", auth.RoleViewer)
	ctx := context.Background()
	records := map[model.Kind][]model.Record{
		"governance.permission_group": {{"name": "writes", "permissions": `["agent:write"]`, "created_by": "upgrade-fixture"}},
		"governance.custom_role":      {{"name": "operator", "permissions": `["cost:read"]`, "groups": `["writes"]`, "created_by": "upgrade-fixture"}},
		"governance.scoped_grant":     {{"subject_kind": "user", "subject_ref": uid, "grant_role": "operator", "role_custom": true, "scope_tree": "tenant", "scope_ref": "", "scope_class": "", "created_by": "upgrade-fixture", "note": ""}},
	}
	if err := governance.SeedStoredAuthorizationForTest(ctx, h.st, tenant, records); err != nil {
		t.Fatal(err)
	}
	if err := h.gov.ReloadActivePDP(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	agent := h.createAgent(tenant, "before-revoke", "")
	if r := h.do("DELETE", "/v1/agents/"+agent.ID.String(), viewer, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("stored custom role/group grant lost: %d %s", r.code, r.raw)
	}
	var grantID string
	for _, path := range []string{"roles", "permission-groups", "grants"} {
		r := h.do("GET", "/v1/m/governance/rbac/"+path, admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK || len(items(r)) != 1 {
			t.Fatalf("stored %s not preserved: %d %s", path, r.code, r.raw)
		}
		if path == "grants" {
			grantID = items(r)[0].(map[string]any)["id"].(string)
		}
	}
	// An attempted edit leaves the effective permissions unchanged.
	if r := h.do("PUT", "/v1/m/governance/rbac/roles/operator", admin, map[string]any{"permissions": []string{}}, tenantHdr(tenant)); r.code != http.StatusNotImplemented {
		t.Fatalf("stored role edit: %d %s", r.code, r.raw)
	}
	other := h.createAgent(tenant, "after-refused-edit", "")
	if r := h.do("DELETE", "/v1/agents/"+other.ID.String(), viewer, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("refused edit changed enforcement: %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/governance/rbac/grants/"+grantID, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("revoke stored grant: %d %s", r.code, r.raw)
	}
	other = h.createAgent(tenant, "after-revoke", "")
	if r := h.do("DELETE", "/v1/agents/"+other.ID.String(), viewer, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("revoked grant still grants: %d %s", r.code, r.raw)
	}
	for _, path := range []string{"roles/operator", "permission-groups/writes"} {
		if r := h.do("DELETE", "/v1/m/governance/rbac/"+path, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
			t.Fatalf("delete retained %s: %d %s", path, r.code, r.raw)
		}
	}
}

func TestCommunityDisablePreservesManagedAndAdoptedEnforcement(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "disable-union")
	uid, viewer := h.roleUser(admin, tenant, "union-viewer@test.io", auth.RoleViewer)
	createGrant(t, h, admin, tenant, map[string]any{"subject_kind": "user", "subject_ref": uid, "role": "editor", "scope_tree": "tenant"})
	blocked := h.createAgent(tenant, "adopted-blocked", "")
	source := `forbid(principal, action == Action::"agent:write", resource == Resource::"` + blocked.ID.String() + `");`
	now := time.Now()
	if _, err := governance.AdoptBundlePolicy(t.Context(), h.st, tenant, ddilAdoption(source, now, time.Hour), now); err != nil {
		t.Fatal(err)
	}
	seedStoredCedar(t, h, tenant, "cedar", `forbid(principal, action == Action::"agent:write", resource);`)
	if err := h.gov.ReloadActivePDP(t.Context(), tenant); err != nil {
		t.Fatal(err)
	}
	before := h.createAgent(tenant, "authored-blocked", "")
	if r := h.do("DELETE", "/v1/agents/"+before.ID.String(), viewer, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("authored forbid=%d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/governance/pdp/active?engine=cedar", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("disable union=%d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/agents/"+before.ID.String(), viewer, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("managed grant afterdisable=%d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/agents/"+blocked.ID.String(), viewer, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("adopted forbid afterdisable=%d %s", r.code, r.raw)
	}
}
