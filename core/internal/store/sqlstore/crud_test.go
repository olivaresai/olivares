// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestViewTransactionOptionsAreEngineExact(t *testing.T) {
	if options := viewTxOptions(store.EngineSQLite); options != nil {
		t.Fatalf("SQLite View options = %+v, want default transaction for scope binding", options)
	}
	options := viewTxOptions(store.EnginePostgres)
	if options == nil || options.Isolation != sql.LevelRepeatableRead || !options.ReadOnly {
		t.Fatalf("PostgreSQL View options = %+v, want repeatable-read/read-only", options)
	}
}

func TestCRUDRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")

	var created model.Agent
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(ctx, model.Agent{
			Name: "ci-bot", Kind: "claude-code", ExternalID: "ext-1",
			Status: model.StatusActive, Labels: map[string]any{"team": "infra"},
		})
		created = a
		return err
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID.IsZero() {
		t.Fatal("create: id not stamped")
	}
	if created.TenantID != tenant {
		t.Fatalf("create: tenant = %s, want %s", created.TenantID, tenant)
	}
	if created.Version != 1 {
		t.Fatalf("create: version = %d, want 1", created.Version)
	}
	if created.CreatedAt.IsZero() {
		t.Fatal("create: created_at not stamped")
	}

	// Read back and compare round-tripped fields.
	var got model.Agent
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Get(ctx, created.ID)
		got = a
		return err
	}); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "ci-bot" || got.ExternalID != "ext-1" || got.Labels["team"] != "infra" {
		t.Fatalf("get: round-trip mismatch: %+v", got)
	}

	// Update bumps version and updated_at.
	var updated model.Agent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		got.Name = "ci-bot-2"
		a, err := sc.Agents().Update(ctx, got)
		updated = a
		return err
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Version != 2 {
		t.Fatalf("update: version = %d, want 2", updated.Version)
	}
	if updated.Name != "ci-bot-2" {
		t.Fatalf("update: name = %q", updated.Name)
	}
	if !updated.CreatedAt.Time().Equal(created.CreatedAt.Time()) {
		t.Fatal("update: created_at must not change")
	}

	// Delete (soft) then Get must be ErrNotFound.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		return sc.Agents().Delete(ctx, created.ID)
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	err = st.View(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Agents().Get(ctx, created.ID)
		return err
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get after delete: err = %v, want ErrNotFound", err)
	}
}

func TestOptimisticConcurrency(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	agent := mustCreateAgent(t, st, tenant, "bot")

	// Two readers load version 1; the first update wins, the second conflicts.
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		agent.Name = "first"
		_, err := sc.Agents().Update(ctx, agent)
		return err
	})
	if err != nil {
		t.Fatalf("first update: %v", err)
	}
	err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
		agent.Name = "second" // still holds version 1
		_, err := sc.Agents().Update(ctx, agent)
		return err
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update: err = %v, want ErrConflict", err)
	}
}

func TestSoftDeleteVisibility(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	agent := mustCreateAgent(t, st, tenant, "bot")

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		return sc.Agents().Delete(ctx, agent.ID)
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		live, _, err := sc.Agents().List(ctx, model.Query{})
		if err != nil {
			return err
		}
		if len(live) != 0 {
			t.Fatalf("List after soft-delete returned %d rows, want 0", len(live))
		}
		all, _, err := sc.Agents().List(ctx, model.Query{IncludeDeleted: true})
		if err != nil {
			return err
		}
		if len(all) != 1 {
			t.Fatalf("List(IncludeDeleted) returned %d rows, want 1", len(all))
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

func TestViewIsReadOnly(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")

	err := st.View(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Agents().Create(ctx, model.Agent{Name: "x", Status: model.StatusActive})
		return err
	})
	if !errors.Is(err, store.ErrReadOnly) {
		t.Fatalf("write in View: err = %v, want ErrReadOnly", err)
	}
}

func TestMutateRollbackOnError(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")

	sentinel := errors.New("boom")
	var id model.ID
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(ctx, model.Agent{Name: "ghost", Status: model.StatusActive})
		if err != nil {
			return err
		}
		id = a.ID
		return sentinel // force rollback
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("mutate: err = %v, want sentinel", err)
	}
	// The create must have been rolled back.
	err = st.View(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Agents().Get(ctx, id)
		return err
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after rollback: err = %v, want ErrNotFound", err)
	}
}

func TestCursorWithSortRejected(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		_, _, e := sc.Agents().List(ctx, model.Query{
			Sort:   []model.Sort{{Column: "name"}},
			Cursor: "some-id",
		})
		return e
	})
	if !errors.Is(err, store.ErrCursorWithSort) {
		t.Fatalf("cursor+sort: err = %v, want ErrCursorWithSort", err)
	}
}

func TestNoTenantFailsClosed(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	err := st.Mutate(ctx, model.TenantID(""), func(store.Scope) error { return nil })
	if !errors.Is(err, store.ErrNoTenant) {
		t.Fatalf("zero tenant: err = %v, want ErrNoTenant", err)
	}
}
