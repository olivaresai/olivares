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

// deleteDefaultWorkspace removes a tenant's default workspace to simulate a
// tenant provisioned before (no default workspace row, and rows that carry
// no workspace_id).
func deleteDefaultWorkspace(t *testing.T, st store.Store, tenant model.TenantID) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		def, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		// Test-only legacy fixture: the public repository intentionally makes the
		// reserved default assignment immutable. Simulate a pre database
		// through the same package-private raw seam used by boot backfill.
		ts := sc.(*tenantScope)
		return newTypedRepo(ts.repo(workspaceDescriptor), workspaceCodec).Delete(ctx, def.ID)
	}); err != nil {
		t.Fatalf("delete default workspace: %v", err)
	}
}

// TestBackCompatPreS192Operates proves the central invariant: a pre state —
// no default workspace, entities with an unset workspace_id — keeps working
// identically. Code that never heard of workspaces creates and reads rows
// normally; the unset workspace_id is simply zero and never orphans the row.
func TestBackCompatPreS192Operates(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "legacy")
	ctx := context.Background()

	deleteDefaultWorkspace(t, st, tenant)

	// Full CRUD with NO workspace_id, exactly like a pre-FASE-X caller, succeeds.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ag, err := sc.Agents().Create(ctx, model.Agent{Name: "legacy-bot", Kind: "mcp", Status: model.StatusActive})
		if err != nil {
			return err
		}
		if !ag.WorkspaceID.IsZero() {
			t.Errorf("agent workspace_id = %s, want zero (unset)", ag.WorkspaceID)
		}
		got, err := sc.Agents().Get(ctx, ag.ID)
		if err != nil || !got.WorkspaceID.IsZero() {
			t.Errorf("agent round-trip: got %+v err %v", got, err)
		}
		se, err := sc.Sessions().Create(ctx, model.Session{
			AgentID: ag.ID, State: model.SessionRunning, StartedAt: model.NewTimestamp(time.Now()),
		})
		if err != nil || !se.WorkspaceID.IsZero() {
			t.Errorf("session create: %+v err %v", se, err)
		}
		rs, err := sc.Resources().Create(ctx, model.Resource{Name: "legacy-db", Kind: "postgres.table"})
		if err != nil {
			return err
		}
		if !rs.WorkspaceID.IsZero() {
			t.Errorf("resource workspace_id = %s, want zero", rs.WorkspaceID)
		}
		return nil
	}); err != nil {
		t.Fatalf("legacy CRUD must work with no default workspace: %v", err)
	}

	// With no default workspace seeded, the resolver reports it honestly.
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		_, e := sc.DefaultWorkspace(ctx)
		return e
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DefaultWorkspace before backfill: err = %v, want ErrNotFound", err)
	}
}

// TestEnsureDefaultWorkspacesBackfill proves the boot backfill materializes a
// missing default workspace and is idempotent, leaving already-seeded tenants
// untouched and never creating a duplicate.
func TestEnsureDefaultWorkspacesBackfill(t *testing.T) {
	st := openSQLiteTest(t, nil)
	ss := st.(*sqlStore)
	ctx := context.Background()
	missing := provisionTenant(t, st, "needs-backfill")
	intact := provisionTenant(t, st, "already-has-it")

	deleteDefaultWorkspace(t, st, missing)
	if _, err := ss.db.ExecContext(ctx,
		"CREATE TEMP TABLE workspaces AS SELECT * FROM main.workspaces WHERE 0",
	); err != nil {
		t.Fatalf("create TEMP workspace shadow: %v", err)
	}
	if _, err := ss.db.ExecContext(ctx, `
INSERT INTO temp.workspaces
SELECT id, ?, created_at, updated_at, version,
       name, slug, status, settings
FROM main.workspaces
WHERE tenant_id = ? AND slug = ?`,
		missing.String(), intact.String(), model.DefaultWorkspaceSlug,
	); err != nil {
		t.Fatalf("seed misleading TEMP default workspace: %v", err)
	}

	// Backfill.
	runEnsure := func() {
		t.Helper()
		if err := st.System(ctx, func(sys store.SystemScope) error {
			return sys.EnsureDefaultWorkspaces(ctx)
		}); err != nil {
			t.Fatalf("EnsureDefaultWorkspaces: %v", err)
		}
	}
	runEnsure()
	runEnsure() // idempotent: a second pass must not duplicate.

	for _, tc := range []struct {
		name   string
		tenant model.TenantID
	}{{"backfilled", missing}, {"intact", intact}} {
		var ws []model.Workspace
		if err := st.View(ctx, tc.tenant, func(sc store.Scope) error {
			w, _, e := sc.Workspaces().List(ctx, model.Query{
				Filters: []model.Filter{{Column: "slug", Op: model.OpEq, Value: model.DefaultWorkspaceSlug}},
			})
			ws = w
			return e
		}); err != nil {
			t.Fatalf("%s list: %v", tc.name, err)
		}
		if len(ws) != 1 {
			t.Errorf("%s tenant has %d default workspaces, want exactly 1", tc.name, len(ws))
		}
	}
}

// TestEnsureDefaultWorkspacesSkipsSystemTenant confirms the reserved system
// tenant is not given a business default workspace.
func TestEnsureDefaultWorkspacesSkipsSystemTenant(t *testing.T) {
	st := openSQLiteTest(t, nil)
	ctx := context.Background()
	// Seed the system tenant (as boot does) then run the backfill.
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		return sys.EnsureDefaultWorkspaces(ctx)
	}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// The system tenant has no default workspace.
	if err := st.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		_, e := sc.DefaultWorkspace(ctx)
		if !errors.Is(e, store.ErrNotFound) {
			t.Errorf("system tenant default workspace: err = %v, want ErrNotFound", e)
		}
		return nil
	}); err != nil {
		t.Fatalf("view system tenant: %v", err)
	}
}
