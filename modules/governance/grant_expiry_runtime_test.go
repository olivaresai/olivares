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
)

func TestScopedGrantExpiryReloadsLiveCedar(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	uid, viewer := h.roleUser(admin, tenant, "viewer@acme.io", auth.RoleViewer)
	createGrant(t, h, admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uid, "role": auth.RoleEditor, "scope_tree": "tenant",
	})
	if r := h.do("POST", "/v1/agents", viewer, map[string]any{"name": "before-expiry", "kind": "claude-code"}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("active grant did not authorize the control launch: %d %s", r.code, r.raw)
	}
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.scoped_grant")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("scoped grants = %d, want 1", len(rows))
		}
		rows[0]["expires_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
		_, err = repo.Update(context.Background(), rows[0])
		return err
	}); err != nil {
		t.Fatalf("expire stored grant: %v", err)
	}
	if r := h.do("POST", "/v1/m/governance/approvals/sweep", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("expiry sweep = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/agents", viewer, map[string]any{"name": "after-expiry", "kind": "claude-code"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("expired grant still authorizes through the live Cedar cache: %d %s", r.code, r.raw)
	}
}
