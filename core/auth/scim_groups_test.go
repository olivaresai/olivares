// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/auth/authtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// mustMember creates a password-bearing user and, when role is non-empty,
// grants it role in tenant. It returns the user id and a fresh session
// principal (re-login after changing grants — principals are snapshots).
func mustMember(t *testing.T, ctx context.Context, a *auth.Authenticator, super auth.Principal, tenant model.TenantID, email, role string) (model.ID, auth.Principal) {
	t.Helper()
	in := auth.NewUser{Email: email, Password: "password-123"}
	if role != "" {
		in.Tenant, in.Role = tenant, role
	}
	u, err := a.CreateUser(ctx, super, in)
	if err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	return u.ID, loginPrincipal(t, ctx, a, email)
}

// loginPrincipal logs email in and authenticates the minted session.
func loginPrincipal(t *testing.T, ctx context.Context, a *auth.Authenticator, email string) auth.Principal {
	t.Helper()
	tok, _, err := a.Login(ctx, email, "password-123", "10.1.1.1")
	if err != nil {
		t.Fatalf("login %s: %v", email, err)
	}
	p, err := a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatalf("authenticate %s: %v", email, err)
	}
	return p
}

func TestConfigureGroupRole(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	_, admin := mustMember(t, ctx, a, super, tenant, "adm@acme.com", auth.RoleAdmin)

	g := authtest.Group(t, st, tenant, "Engineering", "ent-1")
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, "captain"); !errors.Is(err, auth.ErrInvalidGroupRole) {
		t.Errorf("unknown role err = %v, want ErrInvalidGroupRole", err)
	}
	// An admin may map roles up to its own rank, not above.
	if _, err := a.ConfigureGroupRole(ctx, admin, tenant, g.Group.ID, auth.RoleOwner); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Errorf("admin mapping owner err = %v, want ErrRoleCeiling", err)
	}
	mapped, err := a.ConfigureGroupRole(ctx, admin, tenant, g.Group.ID, auth.RoleEditor)
	if err != nil || mapped.MappedRole != auth.RoleEditor {
		t.Errorf("admin mapping editor = (%q, %v)", mapped.MappedRole, err)
	}
	// Superadmin may map owner; clearing needs no ceiling (it only narrows).
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, auth.RoleOwner); err != nil {
		t.Fatal(err)
	}
	cleared, err := a.ConfigureGroupRole(ctx, admin, tenant, g.Group.ID, "")
	if err != nil || cleared.MappedRole != "" {
		t.Errorf("clear mapping = (%q, %v), want empty", cleared.MappedRole, err)
	}
}

func TestGroupMappingFoldsIntoGrants(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	uViewer, _ := mustMember(t, ctx, a, super, tenant, "viewer@acme.com", auth.RoleViewer)
	uOwner, _ := mustMember(t, ctx, a, super, tenant, "owner@acme.com", auth.RoleOwner)
	uOutsider, _ := mustMember(t, ctx, a, super, tenant, "outsider@x.com", "")

	adminGroup := authtest.Group(t, st, tenant, "Admins", "adm-1", uViewer)
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, adminGroup.Group.ID, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	viewerGroup := authtest.Group(t, st, tenant, "Everyone", "all-1", uOwner)
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, viewerGroup.Group.ID, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	// Seed a member row for a user WITHOUT a direct membership straight into the
	// store (the SCIM path would have skipped it): the fold must still grant
	// NOTHING — groups elevate, they never admit.
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.GroupMembers().Create(ctx, model.UserGroupMember{
			GroupID: adminGroup.Group.ID, UserID: uOutsider,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// viewer + admin-mapped group => the session principal acts as admin.
	p := loginPrincipal(t, ctx, a, "viewer@acme.com")
	if r, ok := p.RoleIn(tenant); !ok || r != auth.RoleAdmin {
		t.Errorf("viewer in admin-mapped group RoleIn = (%q, %v), want admin", r, ok)
	}
	// owner + viewer-mapped group => the higher direct role is never downgraded.
	p = loginPrincipal(t, ctx, a, "owner@acme.com")
	if r, ok := p.RoleIn(tenant); !ok || r != auth.RoleOwner {
		t.Errorf("owner in viewer-mapped group RoleIn = (%q, %v), want owner", r, ok)
	}
	// group member with NO direct membership => no grant at all in the tenant.
	p = loginPrincipal(t, ctx, a, "outsider@x.com")
	if p.IsMember(tenant) {
		t.Error("group membership alone granted tenant access; the per-tenant gate must deny it")
	}
}
