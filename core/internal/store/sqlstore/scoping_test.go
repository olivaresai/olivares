// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestDefaultWorkspaceSeededOnCreateOrg proves CreateOrg materializes the
// tenant's default workspace, so a freshly-provisioned tenant always has the
// workspace an unset WorkspaceID resolves to.
func TestDefaultWorkspaceSeededOnCreateOrg(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	var ws model.Workspace
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		w, err := sc.DefaultWorkspace(ctx)
		ws = w
		return err
	}); err != nil {
		t.Fatalf("default workspace: %v", err)
	}
	if ws.Slug != model.DefaultWorkspaceSlug {
		t.Errorf("default workspace slug = %q, want %q", ws.Slug, model.DefaultWorkspaceSlug)
	}
	if ws.Status != model.StatusActive {
		t.Errorf("default workspace status = %q, want active", ws.Status)
	}
	if ws.TenantID != tenant {
		t.Errorf("default workspace tenant = %s, want %s", ws.TenantID, tenant)
	}
	// Exactly one workspace exists for a new tenant: its default.
	var all []model.Workspace
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		ws, _, err := sc.Workspaces().List(ctx, model.Query{})
		all = ws
		return err
	}); err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("new tenant has %d workspaces, want 1 (the default)", len(all))
	}
}

// TestWorkspaceCRUDAndUniqueSlug exercises the workspace repository and the
// tenant-unique slug, including the reserved "default" slug backstop.
func TestWorkspaceCRUDAndUniqueSlug(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	var created model.Workspace
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		w, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "Team A", Slug: "team-a", Status: model.StatusActive,
			Settings: map[string]any{"color": "orange"},
		})
		created = w
		return err
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if created.ID.IsZero() || created.Version != 1 {
		t.Fatalf("create stamped bad base fields: id=%s version=%d", created.ID, created.Version)
	}

	// Read back through a fresh scope; Settings round-trip.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		got, err := sc.Workspaces().Get(ctx, created.ID)
		if err != nil {
			return err
		}
		if got.Slug != "team-a" || got.Settings["color"] != "orange" {
			t.Errorf("round-trip mismatch: %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("get workspace: %v", err)
	}

	// A duplicate slug is rejected, and so is a second reserved "default".
	for _, slug := range []string{"team-a", model.DefaultWorkspaceSlug} {
		err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, e := sc.Workspaces().Create(ctx, model.Workspace{
				Name: "dup", Slug: slug, Status: model.StatusActive,
			})
			return e
		})
		if !errors.Is(err, store.ErrConflict) {
			t.Errorf("duplicate slug %q: err = %v, want ErrConflict", slug, err)
		}
	}

	// Update (rename) and delete.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		created.Name = "Team Alpha"
		_, e := sc.Workspaces().Update(ctx, created)
		return e
	}); err != nil {
		t.Fatalf("update workspace: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		return sc.Workspaces().Delete(ctx, created.ID)
	}); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
}

// TestAgentGroupAndMembers exercises the agent-group repository and its
// membership table: roster by group, an agent's groups, and the (group, agent)
// uniqueness.
func TestAgentGroupAndMembers(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	a1 := mustCreateAgent(t, st, tenant, "bot-1")
	a2 := mustCreateAgent(t, st, tenant, "bot-2")

	var group model.AgentGroup
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		// A workspace-scoped group: point it at the default workspace.
		def, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		g, err := sc.AgentGroups().Create(ctx, model.AgentGroup{
			WorkspaceID: def.ID, Name: "Reviewers", Slug: "reviewers", Status: model.StatusActive,
		})
		group = g
		if err != nil {
			return err
		}
		for _, ag := range []model.Agent{a1, a2} {
			if _, err := sc.AgentGroupMembers().Create(ctx, model.AgentGroupMember{
				GroupID: g.ID, AgentID: ag.ID,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("create group + members: %v", err)
	}
	if group.WorkspaceID.IsZero() {
		t.Fatalf("group workspace_id not persisted")
	}

	// Roster by group_id == 2; a1's groups by agent_id == 1.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		roster, _, err := sc.AgentGroupMembers().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "group_id", Op: model.OpEq, Value: group.ID.String()}},
		})
		if err != nil {
			return err
		}
		if len(roster) != 2 {
			t.Errorf("roster = %d, want 2", len(roster))
		}
		mine, _, err := sc.AgentGroupMembers().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "agent_id", Op: model.OpEq, Value: a1.ID.String()}},
		})
		if err != nil {
			return err
		}
		if len(mine) != 1 {
			t.Errorf("a1 groups = %d, want 1", len(mine))
		}
		return nil
	}); err != nil {
		t.Fatalf("enumerate members: %v", err)
	}

	// (group, agent) is unique.
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, e := sc.AgentGroupMembers().Create(ctx, model.AgentGroupMember{GroupID: group.ID, AgentID: a1.ID})
		return e
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate member: err = %v, want ErrConflict", err)
	}

	// Agent-group slug is unique per tenant (agent_groups_slug_uniq).
	dupErr := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, e := sc.AgentGroups().Create(ctx, model.AgentGroup{Name: "dup", Slug: "reviewers", Status: model.StatusActive})
		return e
	})
	if !errors.Is(dupErr, store.ErrConflict) {
		t.Errorf("duplicate agent-group slug: err = %v, want ErrConflict", dupErr)
	}
}

// TestWorkspaceScopeRoundTrips proves the additive workspace_id columns persist
// on agents, sessions, resources and memberships.
func TestWorkspaceScopeRoundTrips(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ctx := context.Background()

	var wsID model.ID
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		def, err := sc.DefaultWorkspace(ctx)
		wsID = def.ID
		return err
	}); err != nil {
		t.Fatalf("default workspace: %v", err)
	}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ag, err := sc.Agents().Create(ctx, model.Agent{Name: "a", Kind: "mcp", Status: model.StatusActive, WorkspaceID: wsID})
		if err != nil {
			return err
		}
		if got, _ := sc.Agents().Get(ctx, ag.ID); got.WorkspaceID != wsID {
			t.Errorf("agent workspace_id = %s, want %s", got.WorkspaceID, wsID)
		}
		se, err := sc.Sessions().Create(ctx, model.Session{
			AgentID: ag.ID, State: model.SessionRunning, StartedAt: model.NewTimestamp(time.Now()), WorkspaceID: wsID,
		})
		if err != nil {
			return err
		}
		if got, _ := sc.Sessions().Get(ctx, se.ID); got.WorkspaceID != wsID {
			t.Errorf("session workspace_id = %s, want %s", got.WorkspaceID, wsID)
		}
		rs, err := sc.Resources().Create(ctx, model.Resource{Name: "db", Kind: "postgres.table", WorkspaceID: wsID})
		if err != nil {
			return err
		}
		if got, _ := sc.Resources().Get(ctx, rs.ID); got.WorkspaceID != wsID {
			t.Errorf("resource workspace_id = %s, want %s", got.WorkspaceID, wsID)
		}
		return nil
	}); err != nil {
		t.Fatalf("scoped entity round-trips: %v", err)
	}

	// Membership lives in the auth partition; its workspace_id is a reference into
	// the granted tenant's workspace space.
	if err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		u, err := a.Users().Create(ctx, model.User{Email: "u@acme.test", Status: model.StatusActive})
		if err != nil {
			return err
		}
		m, err := a.Memberships().Create(ctx, model.Membership{
			UserID: u.ID, TargetTenantID: tenant, Role: "editor", WorkspaceID: wsID,
		})
		if err != nil {
			return err
		}
		got, err := a.Memberships().Get(ctx, m.ID)
		if err != nil {
			return err
		}
		if got.WorkspaceID != wsID {
			t.Errorf("membership workspace_id = %s, want %s", got.WorkspaceID, wsID)
		}
		return nil
	}); err != nil {
		t.Fatalf("membership workspace scope: %v", err)
	}
}

// TestScopingTenantIsolation proves the new tables are isolated like every other
// tenant table: one tenant's workspaces/groups are invisible to another.
func TestScopingTenantIsolation(t *testing.T) {
	st := openSQLiteTest(t, nil)
	a := provisionTenant(t, st, "tenant-a")
	b := provisionTenant(t, st, "tenant-b")
	ctx := context.Background()

	var aWS model.ID
	if err := st.Mutate(ctx, a, func(sc store.Scope) error {
		w, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "secret", Slug: "secret", Status: model.StatusActive})
		aWS = w.ID
		return err
	}); err != nil {
		t.Fatalf("create A workspace: %v", err)
	}

	// B cannot Get A's workspace (indistinguishable from absent).
	err := st.View(ctx, b, func(sc store.Scope) error {
		_, e := sc.Workspaces().Get(ctx, aWS)
		return e
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant Get: err = %v, want ErrNotFound", err)
	}

	// B's workspace list is its own default only (not A's secret, not A's default).
	if err := st.View(ctx, b, func(sc store.Scope) error {
		ws, _, e := sc.Workspaces().List(ctx, model.Query{})
		if e != nil {
			return e
		}
		for _, w := range ws {
			if w.ID == aWS || w.TenantID != b {
				t.Errorf("tenant B sees foreign workspace %s (tenant %s)", w.ID, w.TenantID)
			}
		}
		if len(ws) != 1 {
			t.Errorf("tenant B workspace count = %d, want 1 (its default)", len(ws))
		}
		return nil
	}); err != nil {
		t.Fatalf("list B workspaces: %v", err)
	}
}
