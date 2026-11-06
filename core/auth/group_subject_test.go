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

// S256 — a session principal carries the directory groups it is a GATED member of
// (Principal.GroupsIn), so a scoped grant whose subject is a group matches it. The
// SAME deny-closed gate that governs MappedRole elevation governs this: a group member
// who holds no direct membership in the group's tenant carries NO group identity.
func TestGroupMembershipsCarriedOnPrincipal(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	memberID, _ := mustMember(t, ctx, a, super, tenant, "m@acme.com", auth.RoleViewer)
	outsiderID, _ := mustMember(t, ctx, a, super, tenant, "o@x.com", "") // no direct membership

	g := authtest.Group(t, st, tenant, "Engineering", "eng-1", memberID)
	// Seed the outsider's group member row too (a directory write would have skipped a
	// non-member): the principal must STILL carry no group — the gate denies it.
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, e := as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: g.Group.ID, UserID: outsiderID})
		return e
	}); err != nil {
		t.Fatal(err)
	}

	// The tenant member carries exactly the group as a subject.
	p := loginPrincipal(t, ctx, a, "m@acme.com")
	if got := p.GroupsIn(tenant); len(got) != 1 || got[0] != g.Group.ID.String() {
		t.Errorf("member GroupsIn = %v, want [%s]", got, g.Group.ID)
	}
	// A returned slice is a defensive copy — mutating it never corrupts the principal.
	if got := p.GroupsIn(tenant); len(got) > 0 {
		got[0] = "tampered"
		if again := p.GroupsIn(tenant); again[0] == "tampered" {
			t.Error("GroupsIn must return a defensive copy")
		}
	}
	// The group member with NO direct membership carries no group (and is no member).
	po := loginPrincipal(t, ctx, a, "o@x.com")
	if po.IsMember(tenant) {
		t.Fatal("the outsider must hold no direct membership")
	}
	if got := po.GroupsIn(tenant); len(got) != 0 {
		t.Errorf("a group member with no direct membership must carry NO group (the gate), got %v", got)
	}
}

// A token principal carries NO group memberships (least privilege — a token is its
// single bound grant, never the owner's whole group set).
func TestTokenPrincipalCarriesNoGroups(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	uid, member := mustMember(t, ctx, a, super, tenant, "m@acme.com", auth.RoleViewer)
	authtest.Group(t, st, tenant, "Engineering", "eng-1", uid)

	// A token the member issues for itself (UserID == the member) is its bound grant only.
	tokStr, _, err := a.IssueToken(ctx, member, auth.TokenSpec{Name: "ci", BoundTenant: tenant, Role: auth.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Authenticate(ctx, tokStr)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != auth.KindToken {
		t.Fatalf("expected a token principal, got %v", p.Kind)
	}
	// Control: the member's fresh SESSION DOES carry the group, so the token's emptiness
	// is least privilege (the bound grant only), not just an absent membership.
	if got := loginPrincipal(t, ctx, a, "m@acme.com").GroupsIn(tenant); len(got) != 1 {
		t.Fatalf("control: the member's session must carry the group, got %v", got)
	}
	if got := p.GroupsIn(tenant); len(got) != 0 {
		t.Errorf("a token principal must carry no groups, got %v", got)
	}
}

// Group-derived elevation follows the CURRENT roster on every token authentication;
// the user's direct role is unchanged.
func TestGroupDerivedTokenRoleTracksMembership(t *testing.T) {
	for _, change := range []string{"remove", "child-remove", "delete", "downgrade"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			a := auth.NewAuthenticator(st, nil)
			super := mustSuperadmin(t, ctx, a)
			tenant := provisionTenant(t, st, "acme")
			uid, _ := mustMember(t, ctx, a, super, tenant, "m@acme.com", auth.RoleViewer)
			group := authtest.Group(t, st, tenant, "Admins", "admins", uid)
			if _, err := a.ConfigureGroupRole(ctx, super, tenant, group.Group.ID, auth.RoleAdmin); err != nil {
				t.Fatal(err)
			}
			if change != "remove" {
				parent := authtest.Group(t, st, tenant, "Parent", "parent")
				authtest.Nest(t, st, group.Group.ID, parent.Group.ID)
			}
			tok, _, err := a.IssueToken(ctx, loginPrincipal(t, ctx, a, "m@acme.com"), auth.TokenSpec{Name: "ci", BoundTenant: tenant, Role: auth.RoleAdmin})
			if err != nil {
				t.Fatal(err)
			}
			principal, err := a.Authenticate(ctx, tok)
			if err != nil {
				t.Fatal(err)
			}
			if role, ok := principal.RoleIn(tenant); !ok || role != auth.RoleAdmin {
				t.Fatalf("initial group-derived role = %q, %v, want admin", role, ok)
			}
			switch change {
			case "remove", "child-remove":
				authtest.SetMembers(t, st, group.Group.ID)
			case "delete":
				authtest.Delete(t, st, group.Group.ID)
			case "downgrade":
				_, err = a.ConfigureGroupRole(ctx, super, tenant, group.Group.ID, auth.RoleEditor)
			}
			if err != nil {
				t.Fatal(err)
			}
			principal, err = a.Authenticate(ctx, tok)
			if err != nil {
				t.Fatal(err)
			}
			wantRole := auth.RoleViewer
			if change == "downgrade" {
				wantRole = auth.RoleEditor
			}
			if role, ok := principal.RoleIn(tenant); !ok || role != wantRole {
				t.Fatalf("role after %s = %q, %v, want %s", change, role, ok, wantRole)
			}
		})
	}
}

func TestGroupDerivedTokenWithoutDirectMembershipIsRefused(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	uid, _ := mustMember(t, ctx, a, super, tenant, "m@acme.com", auth.RoleViewer)
	group := authtest.Group(t, st, tenant, "Admins", "admins", uid)
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, group.Group.ID, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	tok, _, err := a.IssueToken(ctx, loginPrincipal(t, ctx, a, "m@acme.com"), auth.TokenSpec{Name: "ci", BoundTenant: tenant, Role: auth.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Memberships().List(ctx, model.Query{Limit: 1, Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: uid.String()}}})
		if err != nil || len(rows) != 1 {
			return err
		}
		return as.Memberships().Delete(ctx, rows[0].ID)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, tok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("token without direct membership authenticated with %v, want ErrUnauthenticated", err)
	}
}

// A direct token role is not a group-derived one: changing the membership's role does not
// invalidate the token's own grant.
func TestDirectTokenRoleSurvivesMembershipDowngrade(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	uid, member := mustMember(t, ctx, a, super, tenant, "m@acme.com", auth.RoleAdmin)
	tok, _, err := a.IssueToken(ctx, member, auth.TokenSpec{Name: "ci", BoundTenant: tenant, Role: auth.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Memberships().List(ctx, model.Query{Limit: 1, Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: uid.String()}}})
		if err != nil || len(rows) != 1 {
			return err
		}
		rows[0].Role = auth.RoleViewer
		_, err = as.Memberships().Update(ctx, rows[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	principal, err := a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if role, ok := principal.RoleIn(tenant); !ok || role != auth.RoleAdmin {
		t.Fatalf("direct role after a membership downgrade = %q, %v, want admin", role, ok)
	}
}

// Provenance must not bypass the existing malformed-grant validation.
func TestDirectTokenValidationRejectsMalformedGrant(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withTenant bool
		role       string
	}{
		{name: "missing tenant", role: auth.RoleViewer},
		{name: "invalid role", withTenant: true, role: "not-a-role"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			a := auth.NewAuthenticator(st, nil)
			tenant := provisionTenant(t, st, "acme")
			cred, err := auth.NewCredential(auth.PrefixToken)
			if err != nil {
				t.Fatal(err)
			}
			var bound model.TenantID
			if tc.withTenant {
				bound = tenant
			}
			if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
				_, err := as.Tokens().Create(ctx, model.APIToken{
					Name: "malformed", Selector: cred.Selector, SecretHash: cred.SecretHash,
					BoundTenantID: bound, Role: tc.role,
				})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Authenticate(ctx, cred.Token); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("malformed direct token authenticated with %v, want ErrUnauthenticated", err)
			}
		})
	}
}

// Group nesting (S256): a member of a child group is `in` every group the chain nests
// under, so the principal carries the full ancestor closure as subjects.
func TestGroupNestingClosureOnPrincipal(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	memberID, _ := mustMember(t, ctx, a, super, tenant, "m@acme.com", auth.RoleViewer)

	child := authtest.Group(t, st, tenant, "Backend", "be", memberID)
	mid := authtest.Group(t, st, tenant, "Eng", "eng")
	top := authtest.Group(t, st, tenant, "AllStaff", "all")
	// child → mid → top
	authtest.Nest(t, st, child.Group.ID, mid.Group.ID)
	authtest.Nest(t, st, mid.Group.ID, top.Group.ID)

	p := loginPrincipal(t, ctx, a, "m@acme.com")
	got := map[string]bool{}
	for _, gid := range p.GroupsIn(tenant) {
		got[gid] = true
	}
	for _, want := range []model.ID{child.Group.ID, mid.Group.ID, top.Group.ID} {
		if !got[want.String()] {
			t.Errorf("nested member must carry %s in its closure, got %v", want, p.GroupsIn(tenant))
		}
	}
	if len(got) != 3 {
		t.Errorf("closure must be exactly the 3 chain groups, got %v", p.GroupsIn(tenant))
	}
}
