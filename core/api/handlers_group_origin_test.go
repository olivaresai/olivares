// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func createOwnedGroup(t *testing.T, h *harness, actor auth.Principal, tenant model.TenantID, origin string) model.UserGroup {
	t.Helper()
	ctx := context.Background()
	out, err := h.authr.SCIMCreateGroup(ctx, actor, tenant, auth.SCIMGroupInput{
		DisplayName: "Owned " + origin, ExternalID: "external-" + origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
		g, err := as.Groups().Get(ctx, out.Group.ID)
		if err != nil {
			return err
		}
		g.ProvisionedBy = origin
		out.Group, err = as.Groups().Update(ctx, g)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out.Group
}

func TestGroupOriginHTTPScimAncestryUsesFixedForbiddenEnvelope(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "origin-scim-ancestry")
	ctx := context.Background()
	actor, err := h.authr.Authenticate(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	tok := h.scimToken(actor, tenant)
	first := h.scimUser(tok, "first@ancestry.test")
	second := h.scimUser(tok, "second@ancestry.test")
	groups := map[string]model.UserGroup{}
	for _, name := range []string{"child", "middle", "parent"} {
		var members []model.ID
		if name == "child" {
			members = []model.ID{model.ID(first)}
		}
		g, err := h.authr.SCIMCreateGroup(ctx, actor, tenant, auth.SCIMGroupInput{DisplayName: name, ExternalID: name, Members: members})
		if err != nil {
			t.Fatal(err)
		}
		groups[name] = g.Group
	}
	for _, edge := range [][2]string{{"child", "middle"}, {"middle", "parent"}} {
		if _, err := h.authr.ConfigureGroupParent(ctx, actor, tenant, groups[edge[0]].ID, groups[edge[1]].ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
		g, err := as.Groups().Get(ctx, groups["parent"].ID)
		if err != nil {
			return err
		}
		g.ProvisionedBy = "directory-a"
		_, err = as.Groups().Update(ctx, g)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, op := range []struct{ name, method, group, body string }{
		{"replace", http.MethodPut, "child", `{"displayName":"child","externalId":"child","members":[]}`},
		{"add", http.MethodPatch, "child", fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, second)},
		{"remove", http.MethodPatch, "child", fmt.Sprintf(`{"Operations":[{"op":"remove","path":"members[value eq \"%s\"]"}]}`, first)},
		{"delete-child", http.MethodDelete, "child", ""},
		{"delete-intermediate", http.MethodDelete, "middle", ""},
	} {
		t.Run(op.name, func(t *testing.T) {
			res := h.scim(op.method, scimBase+"/Groups/"+groups[op.group].ID.String(), tok, op.body)
			if res.code != http.StatusForbidden || res.body["status"] != "403" || res.body["scimType"] != "mutability" || res.body["detail"] != "group is managed by another provisioner" {
				t.Errorf("SCIM ancestry refusal: status=%d body=%v", res.code, res.body)
			}
		})
	}
	res := h.scim(http.MethodPatch, scimBase+"/Groups/"+groups["child"].ID.String(), tok, `{"Operations":[{"op":"replace","path":"displayName","value":"renamed"}]}`)
	if res.code != http.StatusOK || !memberIDs(res)[first] || memberIDs(res)[second] || res.body["displayName"] != "renamed" {
		t.Errorf("attribute-only NULL update: status=%d body=%v", res.code, res.body)
	}
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
	h := newHarness(t)
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
	tok := h.scimToken(actor, tenant)
	for _, op := range []struct{ method, body string }{
		{http.MethodPut, `{"displayName":"Renamed","externalId":"replacement","members":[]}`},
		{http.MethodPatch, `{"Operations":[{"op":"replace","path":"displayName","value":"Renamed"}]}`},
		{http.MethodDelete, ""},
	} {
		t.Run("scim/"+op.method, func(t *testing.T) {
			res := h.scim(op.method, scimBase+"/Groups/"+g.ID.String(), tok, op.body)
			if res.code != http.StatusForbidden || res.body["status"] != "403" || res.body["detail"] != "group is managed by another provisioner" {
				t.Errorf("SCIM refusal: status=%d body=%v", res.code, res.body)
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

func TestGroupOriginHTTPHierarchyUsesFixedForbiddenEnvelope(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "origin-hierarchy-http")
	ctx := context.Background()
	actor, err := h.authr.Authenticate(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string]model.UserGroup{}
	for _, name := range []string{"child", "middle", "top", "elsewhere", "loose"} {
		g, err := h.authr.SCIMCreateGroup(ctx, actor, tenant, auth.SCIMGroupInput{DisplayName: name, ExternalID: name})
		if err != nil {
			t.Fatal(err)
		}
		groups[name] = g.Group
	}
	for _, edge := range [][2]string{{"child", "middle"}, {"middle", "top"}} {
		if _, err := h.authr.ConfigureGroupParent(ctx, actor, tenant, groups[edge[0]].ID, groups[edge[1]].ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
		g, err := as.Groups().Get(ctx, groups["top"].ID)
		if err != nil {
			return err
		}
		g.ProvisionedBy = "directory-a"
		_, err = as.Groups().Update(ctx, g)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, edge := range [][2]string{{"loose", "middle"}, {"middle", ""}, {"child", "elsewhere"}} {
		t.Run(edge[0]+"-to-"+edge[1], func(t *testing.T) {
			parent := ""
			if edge[1] != "" {
				parent = groups[edge[1]].ID.String()
			}
			res := h.do(http.MethodPut, "/v1/groups/"+groups[edge[0]].ID.String()+"/parent", admin, map[string]any{"parent_id": parent}, tenantHdr(tenant))
			if res.code != http.StatusForbidden {
				t.Fatalf("hierarchy refusal: status=%d", res.code)
			}
			body := res.body["error"].(map[string]any)
			if body["code"] != "group_origin_read_only" || body["message"] != "group is managed by another provisioner" {
				t.Errorf("hierarchy refusal is not fixed: %v", body)
			}
		})
	}
}
