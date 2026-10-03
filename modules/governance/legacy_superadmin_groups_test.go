// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

func TestSR2ImplicitSuperadminOwnerHonorsStoredGroupForbid(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "sr2-superadmin-group-forbid")
	workspace := h.createWorkspace(tenant, "project")
	agent := h.createAgentIn(tenant, "project-agent", workspace)
	p, err := h.authr.Authenticate(t.Context(), admin)
	if err != nil {
		t.Fatal(err)
	}
	var group model.UserGroup
	if err := h.st.AuthMutate(t.Context(), func(as store.AuthScope) error {
		rows, _, err := as.Memberships().List(t.Context(), model.Query{})
		if err != nil {
			return err
		}
		for _, m := range rows {
			if m.UserID == p.UserID && m.TargetTenantID == tenant {
				if err := as.Memberships().Delete(t.Context(), m.ID); err != nil {
					return err
				}
			}
		}
		group, err = as.Groups().Create(t.Context(), model.UserGroup{TargetTenantID: tenant, DisplayName: "sr2-denied-operators"})
		if err != nil {
			return err
		}
		_, err = as.GroupMembers().Create(t.Context(), model.UserGroupMember{GroupID: group.ID, UserID: p.UserID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, key, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(key)
	srv, err := api.New(api.Options{
		Store: h.st, Authenticator: h.authr, PrincipalEvidenceProducer: h.authr,
		Authorizer: auth.NewAuthorizer(h.gov.RequestEvaluator(), auth.WithScopedGrants(h.gov.ScopedGrants())),
		Signer:     signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")),
		Modules: []api.Module{h.gov}, UnconditionalGrants: h.gov.UnconditionalGrants(),
	})
	if err != nil {
		t.Fatal(err)
	}
	read := func() int {
		r := httptest.NewRequest(http.MethodGet, "/v1/agents/"+agent.ID.String(), nil)
		r.Header.Set("Authorization", "Bearer "+admin)
		r.Header.Set("X-Olivares-Tenant", tenant.String())
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if status := read(); status != http.StatusOK {
		t.Fatalf("implicit-owner positive control returned %d", status)
	}
	h.publishGrant(admin, tenant, `forbid(principal in Group::"`+group.ID.String()+`", action == Action::"agent:read", resource);`)
	if status := read(); status != http.StatusForbidden {
		t.Fatalf("stored group forbid did not bind the implicit superadmin owner: status %d", status)
	}
}
