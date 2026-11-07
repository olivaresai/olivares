// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/auth/authtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func createOwnedGroup(t *testing.T, h *harness, actor auth.Principal, tenant model.TenantID, origin string) model.UserGroup {
	t.Helper()
	g := authtest.Group(t, h.st, tenant, "Owned "+origin, "external-"+origin)
	return authtest.Edit(t, h.st, g.Group.ID, func(g *model.UserGroup) { g.ProvisionedBy = origin })
}

func TestGroupOriginOperatorProjection(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "origin")
	actor, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{}
	for _, origin := range []string{"", "operator", "directory-a"} {
		g := createOwnedGroup(t, h, actor, tenant, origin)
		expected[g.ID.String()] = origin
	}
	res := h.do(http.MethodGet, "/v1/groups", admin, nil, tenantHdr(tenant))
	if res.code != http.StatusOK {
		t.Fatalf("list: status=%d", res.code)
	}
	groups, _ := res.body["groups"].([]any)
	if len(groups) != len(expected) {
		t.Fatalf("groups=%d, want %d", len(groups), len(expected))
	}
	for _, row := range groups {
		g := row.(map[string]any)
		origin, present := g["provisioned_by"].(string)
		want, known := expected[g["id"].(string)]
		if !present || !known || origin != want {
			t.Errorf("origin projection: present=%v known=%v got=%q want=%q", present, known, origin, want)
		}
	}
}

func TestGroupOriginHTTPWritesRefuseClaimedGroups(t *testing.T) {
	// Group nesting is a Business route: the department service is wired to reach it.
	h := newHarnessOpts(t, groupDepartments)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "origin-http")
	ctx := context.Background()
	actor, err := h.authr.Authenticate(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	g := createOwnedGroup(t, h, actor, tenant, "directory-a")
	for _, route := range []struct {
		suffix string
		body   map[string]any
	}{
		{"parent", map[string]any{"parent_id": ""}},
	} {
		t.Run("native/"+route.suffix, func(t *testing.T) {
			res := h.do(http.MethodPut, "/v1/groups/"+g.ID.String()+"/"+route.suffix, admin, route.body, tenantHdr(tenant))
			if res.code != http.StatusForbidden {
				t.Fatalf("status=%d, want 403", res.code)
			}
			errBody := res.body["error"].(map[string]any)
			if errBody["code"] != "group_origin_read_only" || errBody["message"] != "group is managed by another provisioner" {
				t.Errorf("native refusal is not the fixed origin envelope: %v", errBody)
			}
		})
	}
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		current, err := as.Groups().Get(ctx, g.ID)
		if err != nil {
			return err
		}
		if current.Version != g.Version || current.ProvisionedBy != g.ProvisionedBy || current.DisplayName != g.DisplayName || current.MappedRole != g.MappedRole {
			t.Error("refused writes changed the claimed group")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
