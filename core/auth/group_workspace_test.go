// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func mkWorkspaceIn(t *testing.T, st store.Store, tenant model.TenantID, slug string) model.Workspace {
	t.Helper()
	var ws model.Workspace
	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		w, err := sc.Workspaces().Create(context.Background(), model.Workspace{
			Name: slug, Slug: slug, Status: model.StatusActive,
		})
		ws = w
		return err
	})
	if err != nil {
		t.Fatalf("create workspace %q: %v", slug, err)
	}
	return ws
}

func storedGroup(t *testing.T, a *auth.Authenticator, tenant model.TenantID, id model.ID) model.UserGroup {
	t.Helper()
	gs, err := a.SCIMListGroups(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.Group.ID == id {
			return g.Group
		}
	}
	t.Fatalf("group %s not listed in tenant %s", id, tenant)
	return model.UserGroup{}
}

// ConfigureGroupWorkspace is the operator's one write of a user group's place.
func TestConfigureGroupWorkspace(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	other := provisionTenant(t, st, "other")
	sales := mkWorkspaceIn(t, st, tenant, "sales")
	ops := mkWorkspaceIn(t, st, tenant, "ops")
	foreign := mkWorkspaceIn(t, st, other, "sales")
	_, admin := mustMember(t, ctx, a, super, tenant, "adm@acme.com", auth.RoleAdmin)
	_, editor := mustMember(t, ctx, a, super, tenant, "ed@acme.com", auth.RoleEditor)
	if _, err := a.CreateUser(ctx, super, auth.NewUser{
		Email: "confined@acme.com", Password: "password-123",
		Tenant: tenant, Role: auth.RoleAdmin, WorkspaceID: sales.ID,
	}); err != nil {
		t.Fatal(err)
	}
	confined := loginPrincipal(t, ctx, a, "confined@acme.com")

	parent, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "All", ExternalID: "all"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Sales staff", ExternalID: "sales-staff"})
	if err != nil {
		t.Fatal(err)
	}
	id := g.Group.ID
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, id, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupParent(ctx, super, tenant, id, parent.Group.ID); err != nil {
		t.Fatal(err)
	}
	if got := storedGroup(t, a, tenant, id); !got.WorkspaceID.IsZero() {
		t.Fatalf("a new group is placed in %q, want tenant-wide", got.WorkspaceID)
	}

	// An editor may not, and a confined admin may not: the place is a
	// tenant-wide directory fact.
	if _, err := a.ConfigureGroupWorkspace(ctx, editor, tenant, id, sales.ID); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Errorf("editor err = %v, want ErrRoleCeiling", err)
	}
	if _, err := a.ConfigureGroupWorkspace(ctx, confined, tenant, id, sales.ID); !errors.Is(err, auth.ErrWorkspaceConfined) {
		t.Errorf("confined admin err = %v, want ErrWorkspaceConfined", err)
	}
	// A workspace of another organization, and one that does not exist, answer alike.
	for name, ws := range map[string]model.ID{"foreign": foreign.ID, "unknown": model.NewID()} {
		if _, err := a.ConfigureGroupWorkspace(ctx, admin, tenant, id, ws); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s workspace err = %v, want ErrNotFound", name, err)
		}
	}
	// A group of another organization is not found either.
	fg, err := a.SCIMCreateGroup(ctx, super, other, auth.SCIMGroupInput{DisplayName: "Foreign", ExternalID: "f"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupWorkspace(ctx, super, tenant, fg.Group.ID, sales.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("foreign group err = %v, want ErrNotFound", err)
	}
	if got := storedGroup(t, a, tenant, id); !got.WorkspaceID.IsZero() {
		t.Fatalf("a refused write placed the group in %q", got.WorkspaceID)
	}

	// An admin places it; nothing else about the group moves.
	placed, err := a.ConfigureGroupWorkspace(ctx, admin, tenant, id, sales.ID)
	if err != nil || placed.WorkspaceID != sales.ID {
		t.Fatalf("admin place = (%q, %v), want %s", placed.WorkspaceID, err, sales.ID)
	}
	got := storedGroup(t, a, tenant, id)
	if got.WorkspaceID != sales.ID || got.MappedRole != auth.RoleViewer || got.ParentGroupID != parent.Group.ID ||
		got.ExternalID != "sales-staff" || got.DisplayName != "Sales staff" {
		t.Fatalf("stored group = %+v, want the place set and every other column kept", got)
	}
	// It moves, then clears back to tenant-wide.
	if moved, err := a.ConfigureGroupWorkspace(ctx, super, tenant, id, ops.ID); err != nil || moved.WorkspaceID != ops.ID {
		t.Fatalf("move = (%q, %v), want %s", moved.WorkspaceID, err, ops.ID)
	}
	if cleared, err := a.ConfigureGroupWorkspace(ctx, super, tenant, id, model.ID("")); err != nil || !cleared.WorkspaceID.IsZero() {
		t.Fatalf("clear = (%q, %v), want none", cleared.WorkspaceID, err)
	}

	// Every move is on the audit trail.
	var places int
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		cw, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("audit log does not expose WalkCanonical")
		}
		return cw.WalkCanonical(ctx, 1, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action == "scim.group.place" && ev.TargetID == id && strings.Contains(meta, `"workspace"`) {
				places++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if places != 3 {
		t.Errorf("scim.group.place audit events = %d, want 3 (place, move, clear)", places)
	}
}

// The place is locally managed on every origin, like the role mapping: the
// provisioner owns a group's identity, roster and hierarchy, not where the
// organization files it.
func TestConfigureGroupWorkspaceOnANamedOriginGroup(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	ws := mkWorkspaceIn(t, st, tenant, "sales")
	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Directory", ExternalID: "d"})
	if err != nil {
		t.Fatal(err)
	}
	setGroupOrigin(t, st, tenant, g.Group.ID, "ldap")
	placed, err := a.ConfigureGroupWorkspace(ctx, super, tenant, g.Group.ID, ws.ID)
	if err != nil || placed.WorkspaceID != ws.ID || placed.ProvisionedBy != "ldap" {
		t.Fatalf("place on a named-origin group = %+v, %v", placed, err)
	}
}

// The IdP never writes the place: a SCIM replace keeps it, as it keeps the
// role mapping and the hierarchy.
func TestSCIMReplaceKeepsTheGroupWorkspace(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	ws := mkWorkspaceIn(t, st, tenant, "sales")
	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Sales", ExternalID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupWorkspace(ctx, super, tenant, g.Group.ID, ws.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SCIMReplaceGroup(ctx, super, tenant, g.Group.ID, auth.SCIMGroupInput{DisplayName: "Sales (renamed)", ExternalID: "s"}, 0); err != nil {
		t.Fatal(err)
	}
	if got := storedGroup(t, a, tenant, g.Group.ID); got.WorkspaceID != ws.ID || got.DisplayName != "Sales (renamed)" {
		t.Fatalf("after a SCIM replace the group = %+v, want the new name and the same place", got)
	}
}

// Membership unchanged: placing a group moves no one's groups and no one's role.
func TestGroupPlaceLeavesMembershipAlone(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")
	ws := mkWorkspaceIn(t, st, tenant, "sales")
	memberID, _ := mustMember(t, ctx, a, super, tenant, "m@acme.com", auth.RoleViewer)
	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{
		DisplayName: "Sales", ExternalID: "s", Members: []model.ID{memberID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, auth.RoleEditor); err != nil {
		t.Fatal(err)
	}
	before := loginPrincipal(t, ctx, a, "m@acme.com")
	if _, err := a.ConfigureGroupWorkspace(ctx, super, tenant, g.Group.ID, ws.ID); err != nil {
		t.Fatal(err)
	}
	after := loginPrincipal(t, ctx, a, "m@acme.com")
	if !slices.Equal(before.GroupsIn(tenant), after.GroupsIn(tenant)) {
		t.Errorf("GroupsIn changed from %v to %v", before.GroupsIn(tenant), after.GroupsIn(tenant))
	}
	rb, _ := before.RoleIn(tenant)
	ra, _ := after.RoleIn(tenant)
	if rb != ra {
		t.Errorf("effective role changed from %q to %q", rb, ra)
	}
	if got := storedGroup(t, a, tenant, g.Group.ID); got.WorkspaceID != ws.ID {
		t.Errorf("stored place = %q, want %s", got.WorkspaceID, ws.ID)
	}
}
