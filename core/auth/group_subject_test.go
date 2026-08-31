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

	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{
		DisplayName: "Engineering", ExternalID: "eng-1", Members: []model.ID{memberID},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Seed the outsider's group member row directly (the SCIM path would have skipped a
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
	if _, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{
		DisplayName: "Engineering", ExternalID: "eng-1", Members: []model.ID{uid},
	}); err != nil {
		t.Fatal(err)
	}

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

// Group nesting (S256): a member of a child group is `in` every group the chain nests
// under, so the principal carries the full ancestor closure as subjects.
func TestGroupNestingClosureOnPrincipal(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	memberID, _ := mustMember(t, ctx, a, super, tenant, "m@acme.com", auth.RoleViewer)

	child, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Backend", ExternalID: "be", Members: []model.ID{memberID}})
	if err != nil {
		t.Fatal(err)
	}
	mid, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Eng", ExternalID: "eng"})
	if err != nil {
		t.Fatal(err)
	}
	top, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "AllStaff", ExternalID: "all"})
	if err != nil {
		t.Fatal(err)
	}
	// child → mid → top
	if _, err := a.ConfigureGroupParent(ctx, super, tenant, child.Group.ID, mid.Group.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupParent(ctx, super, tenant, mid.Group.ID, top.Group.ID); err != nil {
		t.Fatal(err)
	}

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

// ConfigureGroupParent: reshaping the hierarchy needs OWNER/superadmin, the parent must
// live in the same tenant, the edge must stay acyclic, and clearing un-nests.
func TestConfigureGroupParent(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	other := provisionTenant(t, st, "other")
	_, owner := mustMember(t, ctx, a, super, tenant, "owner@acme.com", auth.RoleOwner)
	_, admin := mustMember(t, ctx, a, super, tenant, "adm@acme.com", auth.RoleAdmin)

	child, _ := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Child", ExternalID: "c"})
	parent, _ := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Parent", ExternalID: "p"})
	foreign, _ := a.SCIMCreateGroup(ctx, super, other, auth.SCIMGroupInput{DisplayName: "Foreign", ExternalID: "f"})

	// An admin (read+write, not owner) may NOT reshape the hierarchy.
	if _, err := a.ConfigureGroupParent(ctx, admin, tenant, child.Group.ID, parent.Group.ID); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Errorf("admin nesting err = %v, want ErrRoleCeiling", err)
	}
	// An owner may.
	g, err := a.ConfigureGroupParent(ctx, owner, tenant, child.Group.ID, parent.Group.ID)
	if err != nil || g.ParentGroupID != parent.Group.ID {
		t.Fatalf("owner nesting = (%v, %v), want parent %s", g.ParentGroupID, err, parent.Group.ID)
	}
	// A cross-tenant parent is invisible (no oracle): ErrNotFound, never persisted.
	if _, err := a.ConfigureGroupParent(ctx, super, tenant, child.Group.ID, foreign.Group.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant parent err = %v, want ErrNotFound", err)
	}
	// A cycle is refused: parent is already a descendant-root of child.
	if _, err := a.ConfigureGroupParent(ctx, super, tenant, parent.Group.ID, child.Group.ID); !errors.Is(err, auth.ErrGroupCycle) {
		t.Errorf("cycle err = %v, want ErrGroupCycle", err)
	}
	// Self-parent is a cycle too.
	if _, err := a.ConfigureGroupParent(ctx, super, tenant, child.Group.ID, child.Group.ID); !errors.Is(err, auth.ErrGroupCycle) {
		t.Errorf("self-parent err = %v, want ErrGroupCycle", err)
	}
	// Clearing un-nests (owner authority).
	cleared, err := a.ConfigureGroupParent(ctx, super, tenant, child.Group.ID, model.ID(""))
	if err != nil || !cleared.ParentGroupID.IsZero() {
		t.Errorf("clear parent = (%v, %v), want zero", cleared.ParentGroupID, err)
	}
}
