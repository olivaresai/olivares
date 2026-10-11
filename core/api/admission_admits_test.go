// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// grantsEveryone is a scoped engine that grants every request, as a tenant-scope admin
// grant would.
type grantsEveryone struct{}

func (grantsEveryone) Scoped(context.Context, auth.Request) (auth.ScopedDecision, error) {
	return auth.ScopedDecision{Effect: auth.EffectGrant}, nil
}

// deniesEveryone is a deny overlay that refuses every request it is asked about.
type deniesEveryone struct{}

func (deniesEveryone) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allow: false, Reason: "test deny", Class: auth.ClassPolicy}, nil
}

// Admits is the seam's in-handler question: true exactly when admit admits. It never
// answers from the rank of a role alone: a scoped grant admits a viewer, and a deny overlay
// refuses an owner.
func TestAdmitsAnswersTheSeamsQuestion(t *testing.T) {
	tenant := model.NewTenantID()
	viewer := auth.ScopedPrincipal(model.NewID(), "viewer", tenant, auth.RoleViewer)
	owner := auth.ScopedPrincipal(model.NewID(), "owner", tenant, auth.RoleOwner)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ask := func(p auth.Principal, perm auth.Permission) auth.Request {
		return auth.Request{Principal: p, Permission: perm, Tenant: tenant, Resource: auth.ResourceFor(perm)}
	}
	for _, c := range []struct {
		name string
		az   *auth.Authorizer
		req  auth.Request
		want bool
	}{
		{"a role that grants the permission", auth.NewAuthorizer(nil), ask(viewer, "agent:read"), true},
		{"a role that does not grant it", auth.NewAuthorizer(nil), ask(viewer, "agent:write"), false},
		{"a scoped grant admits a viewer", auth.NewAuthorizer(nil, auth.WithScopedGrants(grantsEveryone{})), ask(viewer, "agent:write"), true},
		{"a deny overlay refuses an owner", auth.NewAuthorizer(deniesEveryone{}), ask(owner, "agent:write"), false},
		{"another tenant's member holds nothing here", auth.NewAuthorizer(nil), ask(auth.ScopedPrincipal(model.NewID(), "x", model.NewTenantID(), auth.RoleOwner), "agent:read"), false},
	} {
		s := &Server{authz: c.az, log: log}
		if got := s.Admits(t.Context(), c.req); got != c.want {
			t.Errorf("%s: Admits = %v, want %v", c.name, got, c.want)
		}
	}
}

// A ModuleContext asks with its own principal and tenant, passes the permission and
// resource it was given, and refuses when it has no door.
func TestModuleContextAdmitsAsksForItsOwnPrincipalAndTenant(t *testing.T) {
	tenant := model.NewTenantID()
	p := auth.ScopedPrincipal(model.NewID(), "viewer", tenant, auth.RoleViewer)
	var got auth.Request
	mc := ModuleContext{Principal: p, Tenant: tenant, Admission: func(_ context.Context, req auth.Request) bool {
		got = req
		return true
	}}
	resource := auth.ResourceAttrs{Kind: "agent", ID: "a1"}
	if !mc.Admits(t.Context(), "agent:write", resource) {
		t.Fatal("Admits did not return the door's answer")
	}
	if got.Principal.UserID != p.UserID || got.Tenant != tenant || got.Permission != "agent:write" || !reflect.DeepEqual(got.Resource, resource) {
		t.Errorf("the door was asked %+v, want the context's principal and tenant with the given permission and resource", got)
	}
	if (ModuleContext{Principal: p, Tenant: tenant}).Admits(t.Context(), "agent:read", resource) {
		t.Error("a ModuleContext with no door must refuse")
	}
}
