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

func f3CarriesGroup(p auth.Principal, tenant model.TenantID, id model.ID) bool {
	for _, got := range p.GroupsIn(tenant) {
		if got == id.String() {
			return true
		}
	}
	return false
}

// This explicitly requires a preexisting hierarchy subsequently claimed by an
// installed provisioner. Fresh-only production creation does not construct it.
// Every attempted mutation below uses the real current native SCIM owner and
// every resulting closure is loaded by fresh native password authentication.
func TestF3GroupOriginSCIMCannotChangePreexistingNamedAncestry(t *testing.T) {
	for _, operation := range []string{"replace_add", "replace_remove", "delete_child", "delete_intermediate", "delete_above_claimed_descendant"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			a := auth.NewAuthenticator(st, nil)
			super := mustSuperadmin(t, ctx, a)
			tenant := provisionTenant(t, st, "f3-origin-followup")
			uid, _ := mustMember(t, ctx, a, super, tenant, "followup@group.test", auth.RoleViewer)
			create := func(name string, members []model.ID) model.UserGroup {
				t.Helper()
				g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: name, ExternalID: name, Members: members})
				if err != nil {
					t.Fatal(err)
				}
				return g.Group
			}
			members := []model.ID{uid}
			if operation == "replace_add" {
				members = nil
			}
			child, middle, root := create("child", members), create("middle", nil), create("root", nil)
			for _, edge := range [][2]model.ID{{child.ID, middle.ID}, {middle.ID, root.ID}} {
				if _, err := a.ConfigureGroupParent(ctx, super, tenant, edge[0], edge[1]); err != nil {
					t.Fatal(err)
				}
			}
			setGroupOrigin(t, st, tenant, root.ID, "directory-a")
			if operation == "delete_above_claimed_descendant" {
				setGroupOrigin(t, st, tenant, child.ID, "directory-b")
			}
			before := f3CarriesGroup(loginPrincipal(t, ctx, a, "followup@group.test"), tenant, root.ID)
			if before != (operation != "replace_add") {
				t.Fatal("fixture did not produce exact expected original inherited membership")
			}
			var err error
			switch operation {
			case "replace_add":
				_, err = a.SCIMReplaceGroup(ctx, super, tenant, child.ID, auth.SCIMGroupInput{DisplayName: child.DisplayName, ExternalID: child.ExternalID, Members: []model.ID{uid}}, 0)
			case "replace_remove":
				_, err = a.SCIMReplaceGroup(ctx, super, tenant, child.ID, auth.SCIMGroupInput{DisplayName: child.DisplayName, ExternalID: child.ExternalID}, 0)
			case "delete_child":
				err = a.SCIMDeleteGroup(ctx, super, tenant, child.ID)
			default:
				err = a.SCIMDeleteGroup(ctx, super, tenant, middle.ID)
			}
			if !errors.Is(err, auth.ErrGroupOriginReadOnly) {
				t.Errorf("native %s must refuse with ErrGroupOriginReadOnly: %v", operation, err)
			}
			after := f3CarriesGroup(loginPrincipal(t, ctx, a, "followup@group.test"), tenant, root.ID)
			if after != before {
				t.Fatalf("real SCIM %s changed inherited named directory-a membership: before=%v after=%v", operation, before, after)
			}
		})
	}
}

func TestF3GroupOriginMutableAncestorDoesNotChangeClaimedDescendantMembership(t *testing.T) {
	for _, operation := range []string{"replace_roster", "mapped_role", "delete_ancestor", "move_ancestor"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			a := auth.NewAuthenticator(st, nil)
			super := mustSuperadmin(t, ctx, a)
			tenant := provisionTenant(t, st, "f3-descendant")
			uid, _ := mustMember(t, ctx, a, super, tenant, "descendant@group.test", auth.RoleViewer)
			other, _ := mustMember(t, ctx, a, super, tenant, "other@group.test", auth.RoleViewer)
			create := func(name string, members []model.ID) model.UserGroup {
				t.Helper()
				g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: name, ExternalID: name, Members: members})
				if err != nil {
					t.Fatal(err)
				}
				return g.Group
			}
			child, middle, upper, alternate := create("claimed-child", []model.ID{uid}), create("middle", nil), create("upper", nil), create("alternate", nil)
			for _, edge := range [][2]model.ID{{child.ID, middle.ID}, {middle.ID, upper.ID}} {
				if _, err := a.ConfigureGroupParent(ctx, super, tenant, edge[0], edge[1]); err != nil {
					t.Fatal(err)
				}
			}
			setGroupOrigin(t, st, tenant, child.ID, "directory-a")
			var err error
			switch operation {
			case "replace_roster":
				_, err = a.SCIMReplaceGroup(ctx, super, tenant, middle.ID, auth.SCIMGroupInput{DisplayName: middle.DisplayName, ExternalID: middle.ExternalID, Members: []model.ID{other}}, 0)
			case "mapped_role":
				_, err = a.ConfigureGroupRole(ctx, super, tenant, middle.ID, auth.RoleOwner)
			case "delete_ancestor":
				err = a.SCIMDeleteGroup(ctx, super, tenant, middle.ID)
			case "move_ancestor":
				_, err = a.ConfigureGroupParent(ctx, super, tenant, middle.ID, alternate.ID)
			}
			if err != nil {
				t.Fatalf("mutable ancestor operation failed: %v", err)
			}
			p := loginPrincipal(t, ctx, a, "descendant@group.test")
			role, member := p.RoleIn(tenant)
			if !f3CarriesGroup(p, tenant, child.ID) || !member || role != auth.RoleViewer {
				t.Fatal("ancestor-only change altered named child membership or inherited ancestor role")
			}
			if f3CarriesGroup(loginPrincipal(t, ctx, a, "other@group.test"), tenant, child.ID) {
				t.Fatal("ancestor roster membership propagated down into named child")
			}
		})
	}
}

func TestGroupOriginSCIMAttributeOnlyKeepsProtectedAncestry(t *testing.T) {
	for _, origin := range []string{"operator", "directory-a"} {
		t.Run(origin, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			a := auth.NewAuthenticator(st, nil)
			super := mustSuperadmin(t, ctx, a)
			tenant := provisionTenant(t, st, "ancestry-attributes")
			uid, _ := mustMember(t, ctx, a, super, tenant, "attributes@group.test", auth.RoleViewer)
			child, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "child", ExternalID: "child", Members: []model.ID{uid}})
			if err != nil {
				t.Fatal(err)
			}
			parent, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "parent", ExternalID: "parent"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.ConfigureGroupParent(ctx, super, tenant, child.Group.ID, parent.Group.ID); err != nil {
				t.Fatal(err)
			}
			setGroupOrigin(t, st, tenant, parent.Group.ID, origin)
			if _, err := a.SCIMReplaceGroup(ctx, super, tenant, child.Group.ID, auth.SCIMGroupInput{DisplayName: "renamed", ExternalID: "renamed", Members: []model.ID{uid}}, 0); err != nil {
				t.Fatalf("attribute-only NULL update was refused: %v", err)
			}
			if !f3CarriesGroup(loginPrincipal(t, ctx, a, "attributes@group.test"), tenant, parent.Group.ID) {
				t.Fatal("attribute-only update changed inherited membership")
			}
			if _, err := a.SCIMReplaceGroup(ctx, super, tenant, child.Group.ID, auth.SCIMGroupInput{DisplayName: "renamed", ExternalID: "renamed"}, 0); !errors.Is(err, auth.ErrGroupOriginReadOnly) {
				t.Fatalf("SCIM removed membership through a %s ancestor: %v", origin, err)
			}
		})
	}
}

func TestGroupOriginReconcileSkipsProtectedAncestry(t *testing.T) {
	for _, origin := range []string{"operator", "directory-a"} {
		t.Run(origin, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			a := auth.NewAuthenticator(st, nil).WithGroupMapper(fakeGroupMapper{})
			super := mustSuperadmin(t, ctx, a)
			tenant := provisionTenant(t, st, "ancestry-reconcile")
			seedConfig(t, st, tenant, "default", "https://idp.ancestry.test", "ancestry.test")
			mustMember(t, ctx, a, super, tenant, "member@ancestry.test", auth.RoleViewer)
			groups := map[string]model.ID{}
			for _, name := range []string{"child", "middle", "protected", "mutable"} {
				g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: name, ExternalID: name})
				if err != nil {
					t.Fatal(err)
				}
				groups[name] = g.Group.ID
			}
			for _, edge := range [][2]string{{"child", "middle"}, {"middle", "protected"}} {
				if _, err := a.ConfigureGroupParent(ctx, super, tenant, groups[edge[0]], groups[edge[1]]); err != nil {
					t.Fatal(err)
				}
			}
			setGroupOrigin(t, st, tenant, groups["protected"], origin)
			credential, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{Subject: "ancestry-member", Email: "member@ancestry.test", Groups: []string{"child", "mutable"}}, "10.1.1.9", tenant, false)
			if err != nil {
				t.Fatal(err)
			}
			p, err := a.Authenticate(ctx, credential)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"child", "middle", "protected"} {
				if f3CarriesGroup(p, tenant, groups[name]) {
					t.Errorf("assertion reconciled %s through a protected %s ancestor", name, origin)
				}
			}
			if !f3CarriesGroup(p, tenant, groups["mutable"]) {
				t.Fatal("unprotected NULL assertion did not reconcile")
			}
		})
	}
}
