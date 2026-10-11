// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// forbidsIndeterminateWrites mimics the scoped engine's rule for a confined principal: a
// non-read request that names no workspace is forbidden, because the target cannot be told
// to be the principal's own.
type forbidsIndeterminateWrites struct{}

func (forbidsIndeterminateWrites) Scoped(_ context.Context, req auth.Request) (auth.ScopedDecision, error) {
	if _, confined := req.Principal.ConfinedWorkspaceIn(req.Tenant); confined &&
		req.Resource.WorkspaceID.IsZero() && req.Permission.Verb() != auth.VerbRead {
		return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "confined principal, workspace indeterminate"}, nil
	}
	return auth.ScopedDecision{Effect: auth.EffectAbstain}, nil
}

// doorModule mounts one read route whose handler asks the admission seam, through its
// ModuleContext, a question the route's own permission does not cover.
type doorModule struct{}

func (doorModule) APINamespace() string { return "door" }

func (doorModule) Permissions() []auth.Permission {
	return []auth.Permission{"door:thing:read", "door:thing:admin"}
}

func (doorModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/ask", "door:thing:read", func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"admin": mc.Admits(r.Context(), "door:thing:admin", auth.ResourceFor("door:thing:admin")),
		})
	})
}

// A module route's ModuleContext carries the admission door: a handler past its route
// permission asks the seam and gets the answer the algebra gives for that permission.
// The viewer passes the route (read) and is refused the admin question; the admin is
// admitted to both. A context built by hand, with no door, refuses.
func TestModuleContextCarriesTheAdmissionDoor(t *testing.T) {
	h := newHarness(t, doorModule{})
	root := h.adminLogin()
	tenant := h.createOrg(root, "door")
	login := func(email, role string) string {
		t.Helper()
		if r := h.do("POST", "/v1/users", root, map[string]any{"email": email, "password": "memberpass1", "tenant": tenant.String(), "role": role}, nil); r.code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", email, r.code, r.raw)
		}
		return h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil).body["token"].(string)
	}
	for _, tc := range []struct {
		email, role string
		want        bool
	}{
		{"viewer@example.test", auth.RoleViewer, false},
		{"admin@example.test", auth.RoleAdmin, true},
	} {
		r := h.do("GET", "/v1/m/door/ask", login(tc.email, tc.role), nil, tenantHdr(tenant))
		if r.code != http.StatusOK || r.body["admin"] != tc.want {
			t.Errorf("%s: got %d %s, want admin=%v", tc.role, r.code, r.raw, tc.want)
		}
	}

	if (api.ModuleContext{Tenant: tenant}).Admits(t.Context(), "door:thing:read", auth.ResourceFor("door:thing:read")) {
		t.Error("a ModuleContext with no admission door must refuse")
	}
}

// A workspace-confined admin is still an admin where it is confined. A scoped engine that
// forbids a confined principal's workspace-less write would otherwise turn its admin
// question into a refusal, though the route (an entity route naming the workspace)
// admitted it; the door asks inside the confining workspace. A confined viewer stays
// refused, and so does a confined admin asked about another workspace's permission it was
// never given (checked at the algebra by TestRequestWithinConfinement); here the wiring.
func TestModuleContextDoorKeepsAConfinedAdminAnAdmin(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Authorizer = auth.NewAuthorizer(nil, auth.WithScopedGrants(forbidsIndeterminateWrites{}))
	}, doorModule{})
	root := h.adminLogin()
	tenant := h.createOrg(root, "door-confined")
	ctx := context.Background()
	var workspace model.ID
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Ops", Slug: "ops", Status: model.StatusActive})
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	confined := func(email, role string) string {
		t.Helper()
		r := h.do("POST", "/v1/users", root, map[string]any{"email": email, "password": "memberpass1"}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", email, r.code, r.raw)
		}
		if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
			_, err := as.Memberships().Create(ctx, model.Membership{
				UserID: model.ID(r.body["id"].(string)), TargetTenantID: tenant, Role: role, WorkspaceID: workspace,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil).body["token"].(string)
	}
	for _, tc := range []struct {
		email, role string
		want        bool
	}{
		{"confined-admin@example.test", auth.RoleAdmin, true},
		{"confined-viewer@example.test", auth.RoleViewer, false},
	} {
		r := h.do("GET", "/v1/m/door/ask", confined(tc.email, tc.role), nil, tenantHdr(tenant))
		if r.code != http.StatusOK || r.body["admin"] != tc.want {
			t.Errorf("confined %s: got %d %s, want admin=%v", tc.role, r.code, r.raw, tc.want)
		}
	}
}
