// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var workspaceInitializerSeedDescriptor = model.EntityDescriptor{
	Kind: "initprobe.seed", Table: "initprobe_seed",
	WorkspaceLineage: model.WorkspaceLineageSpec{
		Column: "workspace_id", Encoding: model.WorkspaceLineageID,
		Unset: model.WorkspaceUnsetHidden,
	},
	Fields: []model.FieldSpec{
		{Name: "workspace_id", Kind: model.KindUUID},
		{Name: "value", Kind: model.KindText},
	},
	Indexes: []model.IndexSpec{{
		Name:    "initprobe_seed_workspace_uniq",
		Columns: []string{model.ColTenantID, "workspace_id"}, Unique: true,
	}},
}

var workspaceInitializerForeignDescriptor = model.EntityDescriptor{
	Kind: "foreignprobe.seed", Table: "foreignprobe_seed",
	WorkspaceLineage: model.WorkspaceLineageSpec{
		Column: "workspace_id", Encoding: model.WorkspaceLineageID,
		Unset: model.WorkspaceUnsetHidden,
	},
	Fields: []model.FieldSpec{{Name: "workspace_id", Kind: model.KindUUID}},
}

func registerWorkspaceInitializerProbe(
	fail *atomic.Bool,
) func(store.ExtensionRegistry) error {
	return func(reg store.ExtensionRegistry) error {
		if err := reg.Register(workspaceInitializerSeedDescriptor); err != nil {
			return err
		}
		if err := reg.Register(workspaceInitializerForeignDescriptor); err != nil {
			return err
		}
		return reg.WorkspaceInitializer(store.WorkspaceInitializer{
			Key: "initprobe.seed.v1",
			Initialize: func(
				ctx context.Context,
				scope store.WorkspaceInitializationScope,
			) error {
				if _, widened := any(scope).(store.Scope); widened {
					return errors.New("workspace initializer widened back to store.Scope")
				}
				workspace := scope.Workspace()
				if workspace.ID.IsZero() || workspace.TenantID != scope.Tenant() {
					return errors.New("workspace initializer received inconsistent lineage")
				}
				if _, err := scope.Ext(workspaceInitializerForeignDescriptor.Kind); !errors.Is(err, store.ErrUnknownEntity) {
					return fmt.Errorf("foreign namespace Ext error = %v, want ErrUnknownEntity", err)
				}
				if err := scope.LockTransaction(
					ctx, "initprobe.seed.v1:"+scope.Tenant().String()+":"+workspace.ID.String(),
				); err != nil {
					return err
				}
				if _, err := scope.TransactionNow(ctx); err != nil {
					return err
				}
				if fail != nil && fail.Load() {
					return store.ErrConflict
				}
				repo, err := scope.Ext(workspaceInitializerSeedDescriptor.Kind)
				if err != nil {
					return err
				}
				stamped, ok := repo.(store.TransactionStampedGenericRepo)
				if !ok {
					return errors.New("workspace initializer repository lacks stamped writes")
				}
				_, err = stamped.CreateAtTransactionTime(ctx, model.Record{
					"workspace_id": workspace.ID.String(), "value": workspace.Slug,
				})
				return err
			},
		})
	}
}

func TestWorkspaceInitializerCoversEveryWorkspaceCreationPathAtomically(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerWorkspaceInitializerProbe(nil))
	tenant := provisionTenant(t, st, "initializer-paths")

	defaultWorkspace := workspaceInitializerDefaultWorkspace(t, st, tenant)
	workspaceInitializerAssertSeeds(t, st, tenant, map[model.ID]string{
		defaultWorkspace.ID: model.DefaultWorkspaceSlug,
	})

	var team model.Workspace
	if err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		created, err := scope.Workspaces().Create(ctx, model.Workspace{
			Name: "Team", Slug: "team", Status: model.StatusActive,
		})
		team = created
		return err
	}); err != nil {
		t.Fatalf("create ordinary workspace: %v", err)
	}
	workspaceInitializerAssertSeeds(t, st, tenant, map[model.ID]string{
		defaultWorkspace.ID: model.DefaultWorkspaceSlug,
		team.ID:             "team",
	})

	// Model a pre-default/pre-initializer tenant by removing both rows through
	// package-private raw repositories, then exercise the boot backfill.
	if err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		raw := scope.(*tenantScope)
		seedRepo, err := raw.Ext(workspaceInitializerSeedDescriptor.Kind)
		if err != nil {
			return err
		}
		seeds, _, err := seedRepo.List(ctx, model.Query{Filters: []model.Filter{{
			Column: "workspace_id", Op: model.OpEq, Value: defaultWorkspace.ID.String(),
		}}})
		if err != nil || len(seeds) != 1 {
			return fmt.Errorf("legacy seed lookup: rows=%d: %w", len(seeds), err)
		}
		if err := seedRepo.Delete(ctx, model.ID(seeds[0].String(model.ColID))); err != nil {
			return err
		}
		return newTypedRepo(raw.repo(workspaceDescriptor), workspaceCodec).Delete(
			ctx, defaultWorkspace.ID,
		)
	}); err != nil {
		t.Fatalf("construct legacy default-workspace state: %v", err)
	}
	if err := st.System(ctx, func(system store.SystemScope) error {
		return system.EnsureDefaultWorkspaces(ctx)
	}); err != nil {
		t.Fatalf("ensure default workspaces: %v", err)
	}
	backfilled := workspaceInitializerDefaultWorkspace(t, st, tenant)
	if backfilled.ID == defaultWorkspace.ID {
		t.Fatal("backfill unexpectedly reused deleted workspace ID")
	}
	workspaceInitializerAssertSeeds(t, st, tenant, map[model.ID]string{
		backfilled.ID: model.DefaultWorkspaceSlug,
		team.ID:       "team",
	})
}

func TestWorkspaceInitializerFailurePoisonsDiscardedCreates(t *testing.T) {
	ctx := context.Background()
	var fail atomic.Bool
	st := openSQLiteTest(t, registerWorkspaceInitializerProbe(&fail))
	tenant := provisionTenant(t, st, "initializer-poison")

	fail.Store(true)
	err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		_, _ = scope.Workspaces().Create(ctx, model.Workspace{
			Name: "Must Roll Back", Slug: "must-roll-back", Status: model.StatusActive,
		})
		return nil
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("discarded ordinary initializer error = %v, want ErrConflict", err)
	}
	if workspaceInitializerWorkspaceCount(t, st, tenant, "must-roll-back") != 0 {
		t.Fatal("workspace committed after its initializer error was discarded")
	}

	// The System path has its own sticky poison. In particular, its expected
	// ErrConflict handling must not turn an initializer conflict into a committed
	// default workspace with missing module state.
	fail.Store(false)
	oldDefault := workspaceInitializerDefaultWorkspace(t, st, tenant)
	if err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		raw := scope.(*tenantScope)
		seedRepo, err := raw.Ext(workspaceInitializerSeedDescriptor.Kind)
		if err != nil {
			return err
		}
		seeds, _, err := seedRepo.List(ctx, model.Query{Filters: []model.Filter{{
			Column: "workspace_id", Op: model.OpEq, Value: oldDefault.ID.String(),
		}}})
		if err != nil || len(seeds) != 1 {
			return fmt.Errorf("default seed lookup: rows=%d: %w", len(seeds), err)
		}
		if err := seedRepo.Delete(ctx, model.ID(seeds[0].String(model.ColID))); err != nil {
			return err
		}
		return newTypedRepo(raw.repo(workspaceDescriptor), workspaceCodec).Delete(ctx, oldDefault.ID)
	}); err != nil {
		t.Fatalf("remove default fixture: %v", err)
	}
	fail.Store(true)
	err = st.System(ctx, func(system store.SystemScope) error {
		_ = system.EnsureDefaultWorkspaces(ctx)
		return nil
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("discarded System initializer error = %v, want ErrConflict", err)
	}
	if workspaceInitializerWorkspaceCount(t, st, tenant, model.DefaultWorkspaceSlug) != 0 {
		t.Fatal("default workspace committed after its initializer error was discarded")
	}
}

func TestCreateOrgRollsBackWhenWorkspaceInitializerFails(t *testing.T) {
	ctx := context.Background()
	var fail atomic.Bool
	fail.Store(true)
	st := openSQLiteTest(t, registerWorkspaceInitializerProbe(&fail))
	err := st.System(ctx, func(system store.SystemScope) error {
		_, _ = system.CreateOrg(ctx, model.Org{
			Name: "Rejected", Slug: "rejected", Status: model.StatusActive,
		})
		return nil
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("discarded CreateOrg initializer error = %v, want ErrConflict", err)
	}
	if err := st.System(ctx, func(system store.SystemScope) error {
		orgs, err := system.ListOrgs(ctx)
		if err != nil {
			return err
		}
		for _, org := range orgs {
			if org.Slug == "rejected" {
				t.Error("organization committed without its default workspace initializer")
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("list organizations: %v", err)
	}
}

func TestWorkspaceInitializerRegistryValidationAndOrder(t *testing.T) {
	callback := func(context.Context, store.WorkspaceInitializationScope) error { return nil }
	for _, initializer := range []store.WorkspaceInitializer{
		{Key: "bad", Initialize: callback},
		{Key: "initprobe.missing.v1"},
	} {
		if err := newRegistry().WorkspaceInitializer(initializer); err == nil {
			t.Fatalf("WorkspaceInitializer(%q) succeeded, want validation error", initializer.Key)
		}
	}

	reg := newRegistry()
	if err := reg.Register(workspaceInitializerSeedDescriptor); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"initprobe.z.v1", "initprobe.a.v1"} {
		if err := reg.WorkspaceInitializer(store.WorkspaceInitializer{Key: key, Initialize: callback}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.WorkspaceInitializer(store.WorkspaceInitializer{
		Key: "initprobe.a.v1", Initialize: callback,
	}); err == nil {
		t.Fatal("duplicate workspace initializer succeeded")
	}
	ordered := reg.registeredWorkspaceInitializers()
	if len(ordered) != 2 || ordered[0].Key != "initprobe.a.v1" || ordered[1].Key != "initprobe.z.v1" {
		t.Fatalf("initializer order = %#v, want key order", ordered)
	}
	if err := reg.validateWorkspaceInitializers(); err != nil {
		t.Fatalf("validate owned initializers: %v", err)
	}

	foreign := newRegistry()
	if err := foreign.Register(workspaceInitializerSeedDescriptor); err != nil {
		t.Fatal(err)
	}
	if err := foreign.WorkspaceInitializer(store.WorkspaceInitializer{
		Key: "unowned.seed.v1", Initialize: callback,
	}); err != nil {
		t.Fatal(err)
	}
	if err := foreign.validateWorkspaceInitializers(); err == nil {
		t.Fatal("unowned workspace initializer passed close validation")
	}
	foreign.closed = true
	if err := foreign.WorkspaceInitializer(store.WorkspaceInitializer{
		Key: "initprobe.closed.v1", Initialize: callback,
	}); err == nil {
		t.Fatal("closed registry accepted workspace initializer")
	}
}

func workspaceInitializerDefaultWorkspace(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
) model.Workspace {
	t.Helper()
	ctx := context.Background()
	var result model.Workspace
	if err := st.View(ctx, tenant, func(scope store.Scope) error {
		workspace, err := scope.DefaultWorkspace(ctx)
		result = workspace
		return err
	}); err != nil {
		t.Fatalf("default workspace: %v", err)
	}
	return result
}

func workspaceInitializerWorkspaceCount(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	slug string,
) int {
	t.Helper()
	ctx := context.Background()
	count := 0
	if err := st.View(ctx, tenant, func(scope store.Scope) error {
		workspaces, _, err := scope.Workspaces().List(ctx, model.Query{Filters: []model.Filter{{
			Column: "slug", Op: model.OpEq, Value: slug,
		}}})
		count = len(workspaces)
		return err
	}); err != nil {
		t.Fatalf("list workspaces for slug %q: %v", slug, err)
	}
	return count
}

func workspaceInitializerAssertSeeds(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	want map[model.ID]string,
) {
	t.Helper()
	ctx := context.Background()
	if err := st.View(ctx, tenant, func(scope store.Scope) error {
		repo, err := scope.Ext(workspaceInitializerSeedDescriptor.Kind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: 1000})
		if err != nil {
			return err
		}
		if len(rows) != len(want) {
			t.Errorf("seed rows = %d, want %d", len(rows), len(want))
		}
		for _, row := range rows {
			workspace, parseErr := model.ParseID(row.String("workspace_id"))
			if parseErr != nil {
				t.Errorf("seed workspace ID: %v", parseErr)
				continue
			}
			value, ok := want[workspace]
			if !ok || row.String("value") != value {
				t.Errorf("unexpected seed row %#v", row)
			}
			if row.String(model.ColCreatedAt) == "" ||
				row.String(model.ColCreatedAt) != row.String(model.ColUpdatedAt) {
				t.Errorf("seed is not transaction-stamped: %#v", row)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("list initializer seeds: %v", err)
	}
}
