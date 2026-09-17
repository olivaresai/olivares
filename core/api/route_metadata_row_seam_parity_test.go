// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// The SHARED ROW SEAM must honour the route's RBAC narrowing, and it is a seam with a real
// consumer rather than an unused type: an add-on route captures a RowAuthorizationPort and
// hands it the SAME coreapi.RouteMetadata it was registered with, so the floor and the
// grant-required flag reach DecideRows exactly as they reach the governed door.
//
// ⛔ THIS IS THE SEAM AND NOT THE DEPLOYMENT. The principal here is reconstructed from this
// package's real store harness through RefreshReadPrincipal, and the metadata shapes are the
// ones a route can declare — including the viewer floor an actually-mounted add-on catalog
// route carries. It says nothing about any deployed installation, which would need that
// artifact, its composition and an HTTP exercise; none of the three is done here.
//
// Both ports are covered because a consumer may hold either: the read port decides through
// DecideRouteRead and the older port through AuthorizeRoute, and BOTH reach the one
// authorizeEvidence switch this delivery corrects.
func TestSharedRowSeamHonoursTheRoutesRBACNarrowing(t *testing.T) {
	policy := &decisionHorizonPolicy{}
	var az *auth.Authorizer
	h := newHarnessOpts(t, func(o *api.Options) {
		policy.store = o.Store
		az = auth.NewAuthorizer(policy)
		o.Authorizer = az
	})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "row-seam-metadata")
	token := witnessTenantPrincipal(t, h, admin, tenant) // a VIEWER in this tenant
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	authenticated, err := h.authr.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("authenticate the tenant member: %v", err)
	}
	readPort := api.NewReadRowAuthorizationPort(az, h.authr)
	ctx, principal, err := readPort.RefreshReadPrincipal(ctx, authenticated, tenant)
	if err != nil {
		t.Fatalf("reconstruct the principal for the read seam: %v", err)
	}
	if role, ok := principal.RoleIn(tenant); !ok || role != auth.RoleViewer {
		t.Fatalf("reconstructed role = %q/%t, want the fixture's %q", role, ok, auth.RoleViewer)
	}

	meta := func(m auth.RouteMetadata) api.RouteMetadata {
		return api.RouteMetadata{RouteMetadata: m}
	}
	cases := []struct {
		name string
		meta api.RouteMetadata
		want bool
	}{
		// Back-compat: the state of every route that has not opted in.
		{"zero metadata", api.RouteMetadata{}, true},
		// The shape an actually-mounted add-on catalog route declares. It must STAY allowed:
		// a correction that denied here would take a live read surface down.
		{"viewer floor, satisfied", meta(auth.RouteMetadata{
			CedarAction: "session:read", RBACMinimumRole: auth.RoleViewer, MinimumAAL: auth.AAL1,
			SessionInheritsAgentGroups: true,
		}), true},
		// The shape the add-on's stricter families declare. The floor must bite.
		{"editor floor, unsatisfied", meta(auth.RouteMetadata{
			CedarAction: "session:read", RBACMinimumRole: auth.RoleEditor,
		}), false},
		{"admin floor, unsatisfied", meta(auth.RouteMetadata{
			CedarAction: "session:read", RBACMinimumRole: auth.RoleAdmin,
		}), false},
		// Grant-required, with no scoped engine wired: breadth of role must not reach it.
		{"scoped grant required", meta(auth.RouteMetadata{
			CedarAction: "session:stop", RequireScopedGrant: true,
		}), false},
	}

	rows := []auth.ResourceAttrs{{Kind: "agent", ID: model.NewID().String()}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := auth.Request{Principal: principal, Tenant: tenant, Permission: "agent:read", Route: tc.meta.RouteMetadata}
			ordinary, err := api.NewRowAuthorizationPort(az).DecideRows(ctx, principal, tenant, "agent:read", tc.meta, rows)
			if err != nil || len(ordinary.Allowed) != 1 || ordinary.Allowed[0] != tc.want {
				t.Fatalf("ordinary row decision = %+v / %v", ordinary, err)
			}
			if err := api.CheckRowSet(ordinary, time.Now(), req, rows); err != nil {
				t.Fatal(err)
			}
			complete, err := readPort.(api.CompleteReadRowAuthorizationPort).DecideReadRows(ctx, principal, tenant, "agent:read", tc.meta, rows)
			if err != nil || len(complete.Allowed) != 1 || complete.Allowed[0] != tc.want {
				t.Fatalf("complete read decision = %+v / %v", complete, err)
			}
			if err := api.CheckCompleteReadRowSet(complete, time.Now(), req, rows); err != nil {
				t.Fatal(err)
			}
		})
	}
}
