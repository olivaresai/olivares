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

// wsuItem is a synthetic module entity whose only job is to carry workspace
// lineage through confined GenericRepo updates. It is not a product descriptor.
var wsuItemDescriptor = model.EntityDescriptor{
	Kind:  "wsu.item",
	Table: "wsu_item",
	WorkspaceLineage: model.WorkspaceLineageSpec{
		Column:   "workspace_id",
		Encoding: model.WorkspaceLineageID,
		Unset:    model.WorkspaceUnsetMeansDefault,
	},
	Fields: []model.FieldSpec{
		{Name: "workspace_id", Kind: model.KindUUID, Nullable: true},
		{Name: "label", Kind: model.KindText},
	},
}

func registerWsuItem(reg store.ExtensionRegistry) error {
	return reg.Register(wsuItemDescriptor)
}

func TestWorkspaceUpdateConfinementSQLite(t *testing.T) {
	testWorkspaceUpdateConfinement(t, openSQLiteTest(t, registerWsuItem))
}

func TestWorkspaceUpdateConfinementPostgres(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 4,
	}, registerWsuItem)
	if err != nil {
		t.Fatalf("open split-owner workspace-update store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testWorkspaceUpdateConfinement(t, st)
}

func testWorkspaceUpdateConfinement(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	tenant := provisionTenant(t, st, "wsu-update")
	var defaultWS, otherWS model.Workspace
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		def, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		defaultWS = def
		created, err := sc.Workspaces().Create(ctx, model.Workspace{
			Name: "Other", Slug: "other", Status: model.StatusActive,
		})
		otherWS = created
		return err
	}); err != nil {
		t.Fatalf("workspaces: %v", err)
	}

	var home, foreign, unset model.Record
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(wsuItemDescriptor.Kind)
		if err != nil {
			return err
		}
		home, err = repo.Create(ctx, model.Record{
			"workspace_id": defaultWS.ID.String(), "label": "home",
		})
		if err != nil {
			return err
		}
		foreign, err = repo.Create(ctx, model.Record{
			"workspace_id": otherWS.ID.String(), "label": "foreign",
		})
		if err != nil {
			return err
		}
		unset, err = repo.Create(ctx, model.Record{"label": "unset"})
		return err
	}); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	assertUnchanged := func(t *testing.T, want model.Record) {
		t.Helper()
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(wsuItemDescriptor.Kind)
			if err != nil {
				return err
			}
			got, err := repo.Get(ctx, model.ID(want.String(model.ColID)))
			if err != nil {
				return err
			}
			if got.String("workspace_id") != want.String("workspace_id") ||
				got.Int(model.ColVersion) != want.Int(model.ColVersion) ||
				got.String("label") != want.String("label") {
				t.Fatalf("foreign row mutated: got workspace=%q version=%d label=%q, want %q/%d/%q",
					got.String("workspace_id"), got.Int(model.ColVersion), got.String("label"),
					want.String("workspace_id"), want.Int(model.ColVersion), want.String("label"))
			}
			return nil
		}); err != nil {
			t.Fatalf("read back: %v", err)
		}
	}

	t.Run("inward relabel of a foreign stored row", func(t *testing.T) {
		err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
			confined, err := store.ConfineWorkspace(ctx, raw, defaultWS.ID)
			if err != nil {
				return err
			}
			repo, err := confined.Ext(wsuItemDescriptor.Kind)
			if err != nil {
				return err
			}
			proposed := model.Record{}
			for k, v := range foreign {
				proposed[k] = v
			}
			proposed["workspace_id"] = defaultWS.ID.String()
			proposed["label"] = "stolen"
			_, err = repo.Update(ctx, proposed)
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("inward relabel = %v, want ErrNotFound", err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
		assertUnchanged(t, foreign)
	})

	t.Run("stamped inward relabel of a foreign stored row", func(t *testing.T) {
		err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
			confined, err := store.ConfineWorkspace(ctx, raw, defaultWS.ID)
			if err != nil {
				return err
			}
			clock, ok := confined.(store.TransactionClock)
			if !ok {
				t.Fatal("confined scope lost TransactionClock")
			}
			if _, err := clock.TransactionNow(ctx); err != nil {
				return err
			}
			repo, err := confined.Ext(wsuItemDescriptor.Kind)
			if err != nil {
				return err
			}
			stamped, ok := repo.(store.TransactionStampedGenericRepo)
			if !ok {
				t.Fatal("confined Ext lost TransactionStampedGenericRepo")
			}
			proposed := model.Record{}
			for k, v := range foreign {
				proposed[k] = v
			}
			proposed["workspace_id"] = defaultWS.ID.String()
			_, err = stamped.UpdateAtTransactionTime(ctx, proposed)
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("stamped inward relabel = %v, want ErrNotFound", err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
		assertUnchanged(t, foreign)
	})

	t.Run("outgoing reassignment is refused", func(t *testing.T) {
		err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
			confined, err := store.ConfineWorkspace(ctx, raw, defaultWS.ID)
			if err != nil {
				return err
			}
			repo, err := confined.Ext(wsuItemDescriptor.Kind)
			if err != nil {
				return err
			}
			proposed := model.Record{}
			for k, v := range home {
				proposed[k] = v
			}
			proposed["workspace_id"] = otherWS.ID.String()
			_, err = repo.Update(ctx, proposed)
			if !errors.Is(err, store.ErrWorkspaceConfinement) {
				t.Fatalf("outgoing = %v, want ErrWorkspaceConfinement", err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
		assertUnchanged(t, home)
	})

	t.Run("same-workspace generic and stamped updates", func(t *testing.T) {
		err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
			confined, err := store.ConfineWorkspace(ctx, raw, defaultWS.ID)
			if err != nil {
				return err
			}
			clock, ok := confined.(store.TransactionClock)
			if !ok {
				t.Fatal("confined scope lost TransactionClock")
			}
			if _, err := clock.TransactionNow(ctx); err != nil {
				return err
			}
			repo, err := confined.Ext(wsuItemDescriptor.Kind)
			if err != nil {
				return err
			}
			stamped, ok := repo.(store.TransactionStampedGenericRepo)
			if !ok {
				t.Fatal("confined Ext lost TransactionStampedGenericRepo")
			}
			if _, ok := repo.(store.RowLocker[model.Record]); !ok {
				t.Fatal("confined Ext lost RowLocker")
			}
			if _, ok := repo.(store.DistinctProjector); !ok {
				t.Fatal("confined Ext lost DistinctProjector")
			}
			home["label"] = "home-2"
			updated, err := repo.Update(ctx, home)
			if err != nil {
				return err
			}
			if updated.String("workspace_id") != defaultWS.ID.String() ||
				updated.String("label") != "home-2" ||
				updated.Int(model.ColVersion) != home.Int(model.ColVersion)+1 {
				t.Fatalf("same-workspace update = %+v", updated)
			}
			home = updated
			home["label"] = "home-3"
			stampedUpdated, err := stamped.UpdateAtTransactionTime(ctx, home)
			if err != nil {
				return err
			}
			if stampedUpdated.String("label") != "home-3" ||
				stampedUpdated.Int(model.ColVersion) != home.Int(model.ColVersion)+1 {
				t.Fatalf("stamped same-workspace update = %+v", stampedUpdated)
			}
			home = stampedUpdated
			return nil
		})
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
	})

	t.Run("default unset row stays on the default workspace", func(t *testing.T) {
		err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
			confined, err := store.ConfineWorkspace(ctx, raw, defaultWS.ID)
			if err != nil {
				return err
			}
			repo, err := confined.Ext(wsuItemDescriptor.Kind)
			if err != nil {
				return err
			}
			got, err := repo.Get(ctx, model.ID(unset.String(model.ColID)))
			if err != nil {
				return err
			}
			if got.String("workspace_id") != "" {
				t.Fatalf("unset stored workspace_id = %q, want empty", got.String("workspace_id"))
			}
			got["label"] = "unset-kept"
			updated, err := repo.Update(ctx, got)
			if err != nil {
				return err
			}
			if updated.String("workspace_id") != "" || updated.String("label") != "unset-kept" {
				t.Fatalf("unset update = %+v", updated)
			}
			unset = updated
			return nil
		})
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}

		err = st.Mutate(ctx, tenant, func(raw store.Scope) error {
			confined, err := store.ConfineWorkspace(ctx, raw, otherWS.ID)
			if err != nil {
				return err
			}
			repo, err := confined.Ext(wsuItemDescriptor.Kind)
			if err != nil {
				return err
			}
			proposed := model.Record{}
			for k, v := range unset {
				proposed[k] = v
			}
			proposed["workspace_id"] = otherWS.ID.String()
			_, err = repo.Update(ctx, proposed)
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("non-default inward relabel of unset = %v, want ErrNotFound", err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
		assertUnchanged(t, unset)
	})

	t.Run("stale version conflicts after the stored-lineage check", func(t *testing.T) {
		err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
			confined, err := store.ConfineWorkspace(ctx, raw, defaultWS.ID)
			if err != nil {
				return err
			}
			repo, err := confined.Ext(wsuItemDescriptor.Kind)
			if err != nil {
				return err
			}
			stale := model.Record{}
			for k, v := range home {
				stale[k] = v
			}
			stale[model.ColVersion] = home.Int(model.ColVersion) - 1
			_, err = repo.Update(ctx, stale)
			if !errors.Is(err, store.ErrConflict) {
				t.Fatalf("stale version = %v, want ErrConflict", err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
		assertUnchanged(t, home)
	})

	t.Run("read-only view still conceals a foreign stored row", func(t *testing.T) {
		err := st.View(ctx, tenant, func(raw store.Scope) error {
			confined, err := store.ConfineWorkspace(ctx, raw, defaultWS.ID)
			if err != nil {
				return err
			}
			repo, err := confined.Ext(wsuItemDescriptor.Kind)
			if err != nil {
				return err
			}
			proposed := model.Record{}
			for k, v := range foreign {
				proposed[k] = v
			}
			proposed["workspace_id"] = defaultWS.ID.String()
			_, err = repo.Update(ctx, proposed)
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("view inward relabel = %v, want ErrNotFound (not ErrReadOnly)", err)
			}
			home["label"] = "from-view"
			_, err = repo.Update(ctx, home)
			if !errors.Is(err, store.ErrReadOnly) {
				t.Fatalf("view own-row update = %v, want ErrReadOnly", err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("view: %v", err)
		}
		assertUnchanged(t, foreign)
	})

	t.Run("typed agent inward relabel is concealed", func(t *testing.T) {
		var foreignAgent model.Agent
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			created, err := sc.Agents().Create(ctx, model.Agent{
				Name: "b-agent", Kind: "claude-code", Status: model.StatusActive,
				WorkspaceID: otherWS.ID,
			})
			foreignAgent = created
			return err
		}); err != nil {
			t.Fatalf("create foreign agent: %v", err)
		}
		err := st.Mutate(ctx, tenant, func(raw store.Scope) error {
			confined, err := store.ConfineWorkspace(ctx, raw, defaultWS.ID)
			if err != nil {
				return err
			}
			stolen := foreignAgent
			stolen.WorkspaceID = defaultWS.ID
			stolen.Name = "stolen-agent"
			_, err = confined.Agents().Update(ctx, stolen)
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("typed inward relabel = %v, want ErrNotFound", err)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			got, err := sc.Agents().Get(ctx, foreignAgent.ID)
			if err != nil {
				return err
			}
			if got.WorkspaceID != otherWS.ID || got.Name != "b-agent" || got.Version != foreignAgent.Version {
				t.Fatalf("foreign agent mutated: %+v", got)
			}
			return nil
		}); err != nil {
			t.Fatalf("typed read back: %v", err)
		}
	})
}
