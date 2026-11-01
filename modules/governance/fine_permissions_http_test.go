// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

func TestAuditOnlyGrantWithoutMembershipHTTP(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "auditor-only")
	r := h.do("POST", "/v1/users", root, map[string]any{"email": "external@audit.test", "password": "auditpass1234"}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": "external@audit.test", "password": "auditpass1234"}, nil)
	token := r.body["token"].(string)
	p := h.principalOf(token)
	if _, ok := p.RoleIn(tenant); ok {
		t.Fatal("auditor has membership")
	}
	paths := []string{"/v1/audit", "/v1/audit/export"}
	// Export availability is edition policy; compare the grant-only caller with
	// the administrator, without treating a Community 501 as an authorization denial.
	exportWant := h.do("GET", "/v1/audit/export", root, nil, tenantHdr(tenant)).code
	if exportWant != http.StatusOK && exportWant != http.StatusNotImplemented {
		t.Fatalf("root export = %d", exportWant)
	}
	t.Logf("edition export status=%d", exportWant)

	for _, path := range paths {
		if r = h.do("GET", path, token, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
			t.Errorf("before %s = %d %s", path, r.code, r.raw)
		}
	}
	records := map[model.Kind][]model.Record{
		"governance.custom_role":  {{"name": "ledger-reader", "permissions": `["audit:read"]`, "created_by": "test"}},
		"governance.scoped_grant": {{"subject_kind": "user", "subject_ref": p.UserID.String(), "grant_role": "ledger-reader", "role_custom": true, "scope_tree": "tenant", "scope_ref": "", "scope_class": "", "created_by": "test", "note": ""}},
	}
	if err := governance.SeedStoredAuthorizationForTest(context.Background(), h.st, tenant, records); err != nil {
		t.Fatal(err)
	}
	if err := h.gov.ReloadActivePDP(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		want := http.StatusOK
		if path == "/v1/audit/export" {
			want = exportWant
		}
		if r = h.do("GET", path, token, nil, tenantHdr(tenant)); r.code != want {
			t.Errorf("granted %s = %d %s", path, r.code, r.raw)
		}
	}
	if r = h.do("POST", "/v1/memberships", token, map[string]any{"tenant": tenant.String(), "user_id": p.UserID.String(), "role": "editor"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("membership = %d %s", r.code, r.raw)
	}
}
