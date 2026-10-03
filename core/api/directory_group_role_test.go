// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func directoryRoleUser(t *testing.T, h *harness, actor auth.Principal, tenant model.TenantID, email, role string) (model.ID, string) {
	t.Helper()
	u, err := h.authr.CreateUser(context.Background(), actor, auth.NewUser{
		Email: email, Password: "password-123", Tenant: tenant, Role: role,
	})
	if err != nil {
		t.Fatal(err)
	}
	login := h.do(http.MethodPost, "/v1/auth/login", "", map[string]any{"email": email, "password": "password-123"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("login = %d %s", login.code, login.raw)
	}
	return u.ID, login.body["token"].(string)
}

func TestLDAPGroupRoleMappingHTTP(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		ctx := context.Background()
		superToken := h.adminLogin()
		tenant := h.createOrg(superToken, "directory-role")
		super, err := h.authr.Authenticate(ctx, superToken)
		if err != nil {
			t.Fatal(err)
		}
		adminID, adminToken := directoryRoleUser(t, h, super, tenant, "admin@role.test", auth.RoleAdmin)
		memberID, _ := directoryRoleUser(t, h, super, tenant, "member@role.test", auth.RoleViewer)
		group := createOwnedGroup(t, h, super, tenant, "ldap")
		// The directory provisioner owns the roster. The local operator below
		// changes only the authority attached to that existing roster.
		if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
			_, err := as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: group.ID, UserID: memberID})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		mapped := h.do(http.MethodPut, "/v1/groups/"+group.ID.String()+"/role", adminToken,
			map[string]any{"role": auth.RoleAdmin}, tenantHdr(tenant))
		if mapped.code != http.StatusOK || mapped.body["mapped_role"] != auth.RoleAdmin {
			t.Fatalf("tenant admin maps LDAP group = %d %s, want 200/admin", mapped.code, mapped.raw)
		}
		current, err := h.authr.SCIMGetGroup(ctx, tenant, group.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Group.ProvisionedBy != group.ProvisionedBy || current.Group.ExternalID != group.ExternalID ||
			current.Group.DisplayName != group.DisplayName || current.Group.TargetTenantID != group.TargetTenantID ||
			current.Group.ParentGroupID != group.ParentGroupID || len(current.Members) != 1 || current.Members[0].ID != memberID {
			t.Fatalf("local role mapping changed directory identity or roster: %+v", current)
		}
		login := h.do(http.MethodPost, "/v1/auth/login", "", map[string]any{"email": "member@role.test", "password": "password-123"}, nil)
		if login.code != http.StatusOK {
			t.Fatalf("member next login = %d %s", login.code, login.raw)
		}
		member, err := h.authr.Authenticate(ctx, login.body["token"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if role, ok := member.RoleIn(tenant); !ok || role != auth.RoleAdmin {
			t.Fatalf("member next-login role = %q/%v, want admin/true", role, ok)
		}
		var mappedAudits int
		if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
			return as.Audit().(store.CanonicalWalker).WalkCanonical(ctx, 1, func(event model.AuditEvent, raw string, _ []byte) error {
				if event.Action != "scim.group.role.map" || event.TargetID != group.ID {
					return nil
				}
				mappedAudits++
				var meta map[string]any
				if err := json.Unmarshal([]byte(raw), &meta); err != nil {
					return err
				}
				if event.Actor != "user:"+adminID.String() || meta["role"] != auth.RoleAdmin {
					t.Errorf("mapping audit actor/role = %q/%v", event.Actor, meta["role"])
				}
				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}
		if mappedAudits != 1 {
			t.Fatalf("mapping audit count = %d, want 1", mappedAudits)
		}
	})
}

func TestDirectoryGroupRoleMappingPreservesAuthorizationHTTP(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		ctx := context.Background()
		superToken := h.adminLogin()
		tenant := h.createOrg(superToken, "directory-role-guards")
		super, err := h.authr.Authenticate(ctx, superToken)
		if err != nil {
			t.Fatal(err)
		}
		_, adminToken := directoryRoleUser(t, h, super, tenant, "admin@guards.test", auth.RoleAdmin)
		_, editorToken := directoryRoleUser(t, h, super, tenant, "editor@guards.test", auth.RoleEditor)
		_, viewerToken := directoryRoleUser(t, h, super, tenant, "viewer@guards.test", auth.RoleViewer)
		for _, origin := range []string{"ldap", ""} {
			name := origin
			if name == "" {
				name = "scim"
			}
			t.Run(name, func(t *testing.T) {
				group := createOwnedGroup(t, h, super, tenant, origin)
				path := "/v1/groups/" + group.ID.String() + "/role"
				for _, tc := range []struct {
					name, token, role string
					status            int
				}{
					{"editor cannot map even viewer", editorToken, auth.RoleViewer, http.StatusForbidden},
					{"viewer cannot map", viewerToken, auth.RoleViewer, http.StatusForbidden},
					{"editor cannot clear", editorToken, "", http.StatusForbidden},
					{"admin cannot confer owner", adminToken, auth.RoleOwner, http.StatusForbidden},
					{"unknown role", adminToken, "captain", http.StatusBadRequest},
				} {
					t.Run(tc.name, func(t *testing.T) {
						res := h.do(http.MethodPut, path, tc.token, map[string]any{"role": tc.role}, tenantHdr(tenant))
						if res.code != tc.status {
							t.Fatalf("mapping refusal = %d %s, want %d", res.code, res.raw, tc.status)
						}
					})
				}
				for _, field := range []string{"external_id", "display_name", "provisioned_by", "parent_id", "members"} {
					t.Run("role route refuses "+field, func(t *testing.T) {
						res := h.do(http.MethodPut, path, adminToken, map[string]any{"role": auth.RoleEditor, field: "replacement"}, tenantHdr(tenant))
						if res.code != http.StatusBadRequest {
							t.Fatalf("non-role field = %d %s, want 400", res.code, res.raw)
						}
					})
				}
				current, err := h.authr.SCIMGetGroup(ctx, tenant, group.ID)
				if err != nil || current.Group.Version != group.Version || current.Group.MappedRole != "" {
					t.Fatalf("refused role writes changed group = %+v, %v", current, err)
				}
				res := h.do(http.MethodPut, path, adminToken, map[string]any{"role": auth.RoleEditor}, tenantHdr(tenant))
				if res.code != http.StatusOK || res.body["mapped_role"] != auth.RoleEditor {
					t.Fatalf("admin mapping = %d %s, want 200/editor", res.code, res.raw)
				}
				// The directory-owned roster and identity still reject SCIM and
				// operator hierarchy writes after the local mapping is changed.
				if origin == "ldap" {
					tok := h.scimToken(super, tenant)
					scimRes := h.scim(http.MethodPut, scimBase+"/Groups/"+group.ID.String(), tok,
						`{"displayName":"Renamed","externalId":"replacement","members":[]}`)
					if scimRes.code != http.StatusForbidden {
						t.Fatalf("directory identity/roster replace = %d %s, want 403", scimRes.code, scimRes.raw)
					}
					res = h.do(http.MethodPut, "/v1/groups/"+group.ID.String()+"/parent", superToken,
						map[string]any{"parent_id": ""}, tenantHdr(tenant))
					if res.code != http.StatusForbidden {
						t.Fatalf("directory hierarchy write = %d %s, want 403", res.code, res.raw)
					}
					current, err = h.authr.SCIMGetGroup(ctx, tenant, group.ID)
					if err != nil || current.Group.MappedRole != auth.RoleEditor || current.Group.ProvisionedBy != origin ||
						current.Group.ExternalID != group.ExternalID || current.Group.DisplayName != group.DisplayName {
						t.Fatalf("refused provisioner writes changed mapping/identity = %+v, %v", current, err)
					}
				}
				if res = h.do(http.MethodPut, path, superToken, map[string]any{"role": auth.RoleOwner}, tenantHdr(tenant)); res.code != http.StatusOK {
					t.Fatalf("owner mapping = %d %s", res.code, res.raw)
				}
				if res = h.do(http.MethodPut, path, adminToken, map[string]any{"role": ""}, tenantHdr(tenant)); res.code != http.StatusOK || res.body["mapped_role"] != "" {
					t.Fatalf("admin clears mapping = %d %s", res.code, res.raw)
				}
			})
		}
		other := h.createOrg(superToken, "other-directory-role")
		foreign := createOwnedGroup(t, h, super, other, "ldap")
		res := h.do(http.MethodPut, "/v1/groups/"+foreign.ID.String()+"/role", adminToken,
			map[string]any{"role": auth.RoleViewer}, tenantHdr(tenant))
		if res.code != http.StatusNotFound {
			t.Fatalf("foreign group through local tenant = %d %s, want 404", res.code, res.raw)
		}
	})
}
