// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// the writer persists Membership.WorkspaceID and the principal builder
// (loadGrants) surfaces it as ConfinedWorkspaceIn, for both the live authenticated session
// and the simulated PrincipalForUser (the honesty invariant). A tenant-wide membership and a
// token are never confined.

func TestMembershipWorkspaceConfinementOnPrincipal(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	ws := model.ID("ws-alpha") // loadGrants reads the stored id verbatim; existence is validated at the API layer

	u, err := a.CreateUser(ctx, admin, auth.NewUser{Email: "c@acme.com", DisplayName: "C", Password: "dev-password-1"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Grant a membership CONFINED to ws.
	if _, err := a.GrantMembership(ctx, admin, u.ID, tenant, auth.RoleAdmin, ws); err != nil {
		t.Fatalf("grant confined: %v", err)
	}

	// The live session principal is confined to ws.
	tok, _, err := a.Login(ctx, "c@acme.com", "dev-password-1", "10.0.0.1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	live, err := a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if got, confined := live.ConfinedWorkspaceIn(tenant); !confined || got != ws {
		t.Errorf("live principal ConfinedWorkspaceIn = (%q, %v), want (%q, true)", got, confined, ws)
	}
	// The simulated principal matches (the honesty invariant loadGrants guarantees).
	sim, found, err := a.PrincipalForUser(ctx, u.ID.String(), auth.AAL3)
	if err != nil || !found {
		t.Fatalf("PrincipalForUser = (found=%v, err=%v)", found, err)
	}
	if got, confined := sim.ConfinedWorkspaceIn(tenant); !confined || got != ws {
		t.Errorf("simulated principal ConfinedWorkspaceIn = (%q, %v), want (%q, true)", got, confined, ws)
	}

	// Re-granting to a DIFFERENT workspace updates the confinement (one row per user/tenant).
	ws2 := model.ID("ws-beta")
	if _, err := a.GrantMembership(ctx, admin, u.ID, tenant, auth.RoleAdmin, ws2); err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	p2, _, _ := a.PrincipalForUser(ctx, u.ID.String(), auth.AAL3)
	if got, ok := p2.ConfinedWorkspaceIn(tenant); !ok || got != ws2 {
		t.Errorf("re-grant to ws2 must move the confinement, got (%q, %v)", got, ok)
	}

	// Re-granting TENANT-WIDE (zero workspace) un-confines the member.
	if _, err := a.GrantMembership(ctx, admin, u.ID, tenant, auth.RoleAdmin, model.ID("")); err != nil {
		t.Fatalf("re-grant tenant-wide: %v", err)
	}
	p3, _, _ := a.PrincipalForUser(ctx, u.ID.String(), auth.AAL3)
	if _, ok := p3.ConfinedWorkspaceIn(tenant); ok {
		t.Error("re-grant to a zero workspace must un-confine the member")
	}
}

// A tenant-wide membership is never confined (the historical, back-compat behavior).
func TestTenantWideMembershipIsNotConfined(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	u, err := a.CreateUser(ctx, admin, auth.NewUser{Email: "w@acme.com", DisplayName: "W", Password: "dev-password-1"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := a.GrantMembership(ctx, admin, u.ID, tenant, auth.RoleEditor, model.ID("")); err != nil {
		t.Fatalf("grant: %v", err)
	}
	p, found, err := a.PrincipalForUser(ctx, u.ID.String(), auth.AAL3)
	if err != nil || !found {
		t.Fatalf("PrincipalForUser = (found=%v, err=%v)", found, err)
	}
	if _, confined := p.ConfinedWorkspaceIn(tenant); confined {
		t.Error("a tenant-wide membership must never be confined")
	}
}

// ADVERSARIAL-REVIEW FIX (critical): a workspace-confined principal cannot mint an API token
// (a token is tenant-wide in its bound tenant and carries no confinement, so it would escape
// the fence). A tenant-wide principal still can.
func TestConfinedPrincipalCannotIssueToken(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	u, err := a.CreateUser(ctx, admin, auth.NewUser{Email: "t@acme.com", DisplayName: "T", Password: "dev-password-1"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := a.GrantMembership(ctx, admin, u.ID, tenant, auth.RoleAdmin, model.ID("ws-alpha")); err != nil {
		t.Fatalf("grant confined: %v", err)
	}
	confined, _, _ := a.PrincipalForUser(ctx, u.ID.String(), auth.AAL3)
	if _, _, err := a.IssueToken(ctx, confined, auth.TokenSpec{BoundTenant: tenant, Role: auth.RoleAdmin}); !errors.Is(err, auth.ErrWorkspaceConfined) {
		t.Errorf("a confined principal must not issue a token, got %v", err)
	}

	// Un-confine → issuing a token is allowed again.
	if _, err := a.GrantMembership(ctx, admin, u.ID, tenant, auth.RoleAdmin, model.ID("")); err != nil {
		t.Fatalf("re-grant tenant-wide: %v", err)
	}
	wide, _, _ := a.PrincipalForUser(ctx, u.ID.String(), auth.AAL3)
	if _, _, err := a.IssueToken(ctx, wide, auth.TokenSpec{BoundTenant: tenant, Role: auth.RoleAdmin}); err != nil {
		t.Errorf("a tenant-wide admin must issue a token, got %v", err)
	}
}

// ADVERSARIAL-REVIEW FIX (high): a workspace-confined actor may author memberships ONLY within
// its own workspace — it cannot lift its own (or a target's) confinement by granting tenant-wide
// or in another workspace.
func TestConfinedActorCannotWidenMembership(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	u, _ := a.CreateUser(ctx, admin, auth.NewUser{Email: "actor@acme.com", DisplayName: "A", Password: "dev-password-1"})
	target, _ := a.CreateUser(ctx, admin, auth.NewUser{Email: "target@acme.com", DisplayName: "Tg", Password: "dev-password-1"})
	if _, err := a.GrantMembership(ctx, admin, u.ID, tenant, auth.RoleAdmin, model.ID("ws-alpha")); err != nil {
		t.Fatalf("grant confined admin: %v", err)
	}
	confined, _, _ := a.PrincipalForUser(ctx, u.ID.String(), auth.AAL3)

	// Tenant-wide grant (lift confinement) — refused.
	if _, err := a.GrantMembership(ctx, confined, target.ID, tenant, auth.RoleEditor, model.ID("")); !errors.Is(err, auth.ErrWorkspaceConfined) {
		t.Errorf("confined actor must not grant tenant-wide, got %v", err)
	}
	// Another workspace — refused.
	if _, err := a.GrantMembership(ctx, confined, target.ID, tenant, auth.RoleEditor, model.ID("ws-beta")); !errors.Is(err, auth.ErrWorkspaceConfined) {
		t.Errorf("confined actor must not grant in another workspace, got %v", err)
	}
	// Its OWN workspace — allowed.
	if _, err := a.GrantMembership(ctx, confined, target.ID, tenant, auth.RoleEditor, model.ID("ws-alpha")); err != nil {
		t.Errorf("confined actor must grant within its own workspace, got %v", err)
	}
}
