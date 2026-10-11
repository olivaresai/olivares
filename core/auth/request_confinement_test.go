// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// forbidsIndeterminateWrites mimics the scoped engine's rule for a confined principal: a
// non-read request that names no workspace is forbidden, because the target cannot be told
// to be the principal's own.
type forbidsIndeterminateWrites struct{}

func (forbidsIndeterminateWrites) Scoped(_ context.Context, req Request) (ScopedDecision, error) {
	if _, confined := req.Principal.ConfinedWorkspaceIn(req.Tenant); confined &&
		req.Resource.WorkspaceID.IsZero() && req.Permission.Verb() != VerbRead {
		return ScopedDecision{Effect: EffectForbid, Reason: "confined principal, workspace indeterminate"}, nil
	}
	return ScopedDecision{Effect: EffectAbstain}, nil
}

// WithinConfinement asks the question inside the workspace a confined principal is bound
// to, so a confined admin is still an admin there, and changes nothing else: an unconfined
// principal and a request that names a workspace are returned as they were, and a confined
// viewer stays refused because the role, not the workspace, decides.
func TestRequestWithinConfinement(t *testing.T) {
	tenant, workspace, other := model.NewTenantID(), model.NewID(), model.NewID()
	confinedTo := func(role string) Principal {
		return ScopedPrincipal(model.NewID(), "p", tenant, role).withConfinements(map[model.TenantID]model.ID{tenant: workspace})
	}
	ask := func(p Principal, perm Permission, ws model.ID) Request {
		r := Request{Principal: p, Permission: perm, Tenant: tenant, Resource: ResourceFor(perm)}
		r.Resource.WorkspaceID = ws
		return r
	}
	az := NewAuthorizer(nil, WithScopedGrants(forbidsIndeterminateWrites{}))

	admin := ask(confinedTo(RoleAdmin), "sessions:work:admin", "")
	if az.Authorize(t.Context(), admin).Allow {
		t.Fatal("control: the engine must refuse a confined admin's workspace-less admin question, or this test shows nothing")
	}
	if !az.Authorize(t.Context(), admin.WithinConfinement()).Allow {
		t.Error("a confined admin must be admitted when asked inside its own workspace")
	}
	if az.Authorize(t.Context(), ask(confinedTo(RoleViewer), "sessions:work:admin", "").WithinConfinement()).Allow {
		t.Error("a confined viewer must stay refused: the workspace does not confer the role")
	}
	if got := admin.WithinConfinement().Resource.WorkspaceID; got != workspace {
		t.Errorf("asked in workspace %q, want the confining workspace %q", got, workspace)
	}
	named := ask(confinedTo(RoleAdmin), "sessions:work:admin", other)
	if got := named.WithinConfinement().Resource.WorkspaceID; got != other {
		t.Errorf("a request naming workspace %q was moved to %q", other, got)
	}
	free := ask(ScopedPrincipal(model.NewID(), "free", tenant, RoleAdmin), "sessions:work:admin", "")
	if got := free.WithinConfinement(); !got.Resource.WorkspaceID.IsZero() {
		t.Errorf("an unconfined principal was given workspace %q", got.Resource.WorkspaceID)
	}
}
