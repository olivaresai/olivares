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

func TestGroupOriginHierarchyCannotInjectClaimedAncestor(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "sr-hierarchy")
	uid, _ := mustMember(t, ctx, a, super, tenant, "member@sr.test", auth.RoleViewer)
	child, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Unclaimed child", ExternalID: "sr-child", Members: []model.ID{uid}})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Claimed directory parent", ExternalID: "sr-parent"})
	if err != nil {
		t.Fatal(err)
	}
	setGroupOrigin(t, st, tenant, parent.Group.ID, "directory-a")
	_, err = a.ConfigureGroupParent(ctx, super, tenant, child.Group.ID, parent.Group.ID)
	if err != nil {
		if !errors.Is(err, auth.ErrGroupOriginReadOnly) {
			t.Fatalf("injection refusal must be ErrGroupOriginReadOnly: %v", err)
		}
		return
	}
	p := loginPrincipal(t, ctx, a, "member@sr.test")
	for _, gid := range p.GroupsIn(tenant) {
		if gid == parent.Group.ID.String() {
			t.Fatal("operator added a member to claimed directory-a group through an unclaimed child; principal carries the claimed ancestor")
		}
	}
}

func TestGroupOriginHierarchyCannotDetachClaimedAncestor(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "sr-detach")
	uid, _ := mustMember(t, ctx, a, super, tenant, "detach@sr.test", auth.RoleViewer)
	child, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Child", ExternalID: "sr-detach-child", Members: []model.ID{uid}})
	if err != nil {
		t.Fatal(err)
	}
	middle, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Middle", ExternalID: "sr-detach-mid"})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Parent", ExternalID: "sr-detach-parent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ConfigureGroupParent(ctx, super, tenant, child.Group.ID, middle.Group.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ConfigureGroupParent(ctx, super, tenant, middle.Group.ID, parent.Group.ID); err != nil {
		t.Fatal(err)
	}
	setGroupOrigin(t, st, tenant, parent.Group.ID, "directory-a")
	_, err = a.ConfigureGroupParent(ctx, super, tenant, middle.Group.ID, model.ID(""))
	if err != nil {
		if !errors.Is(err, auth.ErrGroupOriginReadOnly) {
			t.Fatalf("detachment refusal must be ErrGroupOriginReadOnly: %v", err)
		}
		return
	}
	p := loginPrincipal(t, ctx, a, "detach@sr.test")
	for _, gid := range p.GroupsIn(tenant) {
		if gid == parent.Group.ID.String() {
			return
		}
	}
	t.Fatal("operator removed inherited directory-a membership by clearing an unclaimed intermediate child")
}
