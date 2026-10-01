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

func TestGroupOriginHierarchyMovesCompareBothClaimedClosures(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "hierarchy-move")
	member, _ := mustMember(t, ctx, a, super, tenant, "move@test.local", auth.RoleViewer)
	create := func(name string, members ...model.ID) model.UserGroup {
		t.Helper()
		g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: name, ExternalID: name, Members: members})
		if err != nil {
			t.Fatal(err)
		}
		return g.Group
	}
	child := create("child", member)
	old := create("old")
	newParent := create("new")
	claimedA := create("claimed-a")
	claimedB := create("claimed-b")
	for _, edge := range [][2]model.ID{{child.ID, old.ID}, {old.ID, claimedA.ID}, {newParent.ID, claimedB.ID}} {
		if _, err := a.ConfigureGroupParent(ctx, super, tenant, edge[0], edge[1]); err != nil {
			t.Fatal(err)
		}
	}
	setGroupOrigin(t, st, tenant, child.ID, "operator")
	setGroupOrigin(t, st, tenant, claimedA.ID, "directory-a")
	setGroupOrigin(t, st, tenant, claimedB.ID, "directory-b")
	if _, err := a.ConfigureGroupParent(ctx, super, tenant, child.ID, newParent.ID); !errors.Is(err, auth.ErrGroupOriginReadOnly) {
		t.Fatalf("move exchanges claimed ancestors: err=%v, want ErrGroupOriginReadOnly", err)
	}
	groups := loginPrincipal(t, ctx, a, "move@test.local").GroupsIn(tenant)
	seen := map[string]bool{}
	for _, id := range groups {
		seen[id] = true
	}
	if !seen[claimedA.ID.String()] || seen[claimedB.ID.String()] {
		t.Fatalf("refused move changed inherited claimed membership: %v", groups)
	}
}

func TestGroupOriginHierarchyChecksIndirectClaimedAncestors(t *testing.T) {
	for _, operation := range []string{"set", "clear", "move"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			a := auth.NewAuthenticator(st, nil)
			super := mustSuperadmin(t, ctx, a)
			tenant := provisionTenant(t, st, "hierarchy-indirect")
			create := func(name string) model.UserGroup {
				t.Helper()
				g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: name, ExternalID: name})
				if err != nil {
					t.Fatal(err)
				}
				return g.Group
			}
			child, parent, middle, claimed, elsewhere := create("child"), create("parent"), create("middle"), create("claimed"), create("elsewhere")
			for _, edge := range [][2]model.ID{{parent.ID, middle.ID}, {middle.ID, claimed.ID}} {
				if _, err := a.ConfigureGroupParent(ctx, super, tenant, edge[0], edge[1]); err != nil {
					t.Fatal(err)
				}
			}
			if operation != "set" {
				if _, err := a.ConfigureGroupParent(ctx, super, tenant, child.ID, parent.ID); err != nil {
					t.Fatal(err)
				}
			}
			setGroupOrigin(t, st, tenant, claimed.ID, "directory-a")
			setGroupOrigin(t, st, tenant, child.ID, "operator")
			var before model.UserGroup
			if err := st.AuthView(ctx, func(as store.AuthScope) error {
				var err error
				before, err = as.Groups().Get(ctx, child.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			next := parent.ID
			switch operation {
			case "clear":
				next = model.ID("")
			case "move":
				next = elsewhere.ID
			}
			if _, err := a.ConfigureGroupParent(ctx, super, tenant, child.ID, next); !errors.Is(err, auth.ErrGroupOriginReadOnly) {
				t.Fatalf("%s across indirect claimed ancestor: %v", operation, err)
			}
			if err := st.AuthView(ctx, func(as store.AuthScope) error {
				current, err := as.Groups().Get(ctx, child.ID)
				if err == nil && (current.ParentGroupID != before.ParentGroupID || current.Version != before.Version) {
					t.Error("refused edge change persisted a parent or version")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGroupOriginHierarchyAllowsUnchangedClaimedClosure(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "hierarchy-same")
	var ids []model.ID
	for _, name := range []string{"child", "left", "right", "claimed"} {
		g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: name, ExternalID: name})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, g.Group.ID)
	}
	for _, edge := range [][2]model.ID{{ids[0], ids[1]}, {ids[1], ids[3]}, {ids[2], ids[3]}} {
		if _, err := a.ConfigureGroupParent(ctx, super, tenant, edge[0], edge[1]); err != nil {
			t.Fatal(err)
		}
	}
	setGroupOrigin(t, st, tenant, ids[3], "directory-a")
	if _, err := a.ConfigureGroupParent(ctx, super, tenant, ids[0], ids[1]); err != nil {
		t.Fatalf("unchanged edge was refused: %v", err)
	}
	if _, err := a.ConfigureGroupParent(ctx, super, tenant, ids[0], ids[2]); err != nil {
		t.Fatalf("move retains the same claimed ancestor: %v", err)
	}
}
