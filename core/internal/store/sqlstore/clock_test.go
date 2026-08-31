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

type transactionClockFixedAppClock struct{ now model.Timestamp }

func (c transactionClockFixedAppClock) Now() model.Timestamp { return c.now }

var transactionStampedEntity = model.EntityDescriptor{
	Kind:  "rrw.transaction_stamp",
	Table: "rrw_transaction_stamp",
	Fields: []model.FieldSpec{
		{Name: "label", Kind: model.KindText},
	},
}

func registerTransactionStampedEntity(reg store.ExtensionRegistry) error {
	return reg.Register(transactionStampedEntity)
}

// TestSQLiteTransactionClockUsesEngineTime kills the tempting fallback to the
// store's injected application clock. The injected value is decades stale; the
// optional capability must still return a current value observed by SQLite in
// the surrounding transaction.
func TestSQLiteTransactionClockUsesEngineTime(t *testing.T) {
	ctx := context.Background()
	fixed := model.NewTimestamp(time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC))
	st, err := Open(ctx, store.Config{
		Engine: store.EngineSQLite,
		DSN:    ":memory:",
		Clock:  transactionClockFixedAppClock{now: fixed},
	}, nil)
	if err != nil {
		t.Fatalf("open SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	tenant := provisionTenant(t, st, "transaction-clock")
	before := time.Now().UTC().Add(-time.Second)
	var got model.Timestamp
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			t.Fatal("SQLite tenant Scope does not expose store.TransactionClock")
		}
		var clockErr error
		got, clockErr = clock.TransactionNow(ctx)
		return clockErr
	}); err != nil {
		t.Fatalf("read transaction clock: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)

	if got.String() == fixed.String() {
		t.Fatalf("transaction clock returned injected application time %s", got.String())
	}
	if got.Time().Before(before) || got.Time().After(after) {
		t.Fatalf("SQLite transaction time %s is outside [%s, %s]", got.String(), before, after)
	}
	// The SQLite query emits the same canonical representation persisted by the
	// model. Re-parsing also catches a precision/layout regression independently
	// of the wall-clock range assertion above.
	if _, err := model.ParseTimestamp(got.String()); err != nil {
		t.Fatalf("transaction time is not canonical: %v", err)
	}
}

// TestGenericRepoReusesObservedTransactionTimeForWriteStamps closes the seam
// between a module planner and the engine-owned base columns. A planner first
// observes DB time through TransactionClock; Create/Update later in that exact
// transaction must stamp the latest observed value, not a separately sampled
// process clock. Without this coupling, rows whose domain timestamp must equal
// created_at/updated_at are impossible to persist outside fixed-clock tests.
func TestGenericRepoReusesObservedTransactionTimeForWriteStamps(t *testing.T) {
	ctx := context.Background()
	fixed := model.NewTimestamp(time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC))
	st, err := Open(ctx, store.Config{
		Engine: store.EngineSQLite,
		DSN:    ":memory:",
		Clock:  transactionClockFixedAppClock{now: fixed},
	}, registerTransactionStampedEntity)
	if err != nil {
		t.Fatalf("open SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tenant := provisionTenant(t, st, "transaction-write-stamp")

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(transactionStampedEntity.Kind)
		if err != nil {
			return err
		}
		stamped, ok := repo.(store.TransactionStampedGenericRepo)
		if !ok {
			t.Fatal("SQL GenericRepo does not expose transaction-stamped writes")
		}
		if _, err := stamped.CreateAtTransactionTime(
			ctx, model.Record{"label": "missing-db-clock"},
		); !errors.Is(err, store.ErrTransactionTimeNotObserved) {
			t.Fatalf("transaction-stamped create before TransactionNow = %v, want sentinel", err)
		}
		created, err := repo.Create(ctx, model.Record{"label": "legacy-clock-path"})
		if err != nil {
			return err
		}
		if created.String(model.ColCreatedAt) != fixed.String() ||
			created.String(model.ColUpdatedAt) != fixed.String() {
			t.Fatalf("write without TransactionNow stamps = %s/%s, want application clock %s",
				created.String(model.ColCreatedAt), created.String(model.ColUpdatedAt), fixed.String())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			t.Fatal("tenant Scope does not expose TransactionClock")
		}
		dbNow, err := clock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		if dbNow.String() == fixed.String() {
			t.Fatal("TransactionNow returned the injected application clock")
		}
		repo, err := sc.Ext(transactionStampedEntity.Kind)
		if err != nil {
			return err
		}
		stamped, ok := repo.(store.TransactionStampedGenericRepo)
		if !ok {
			t.Fatal("SQL GenericRepo does not expose transaction-stamped writes")
		}
		created, err := stamped.CreateAtTransactionTime(ctx, model.Record{"label": "db-clock-path"})
		if err != nil {
			return err
		}
		if created.String(model.ColCreatedAt) != dbNow.String() ||
			created.String(model.ColUpdatedAt) != dbNow.String() {
			t.Fatalf("transaction-stamped create = %s/%s, want %s",
				created.String(model.ColCreatedAt), created.String(model.ColUpdatedAt), dbNow.String())
		}
		again, err := clock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		if again.Before(dbNow) {
			t.Fatalf("transaction clock moved backwards inside one scope: %s -> %s", dbNow.String(), again.String())
		}
		created["label"] = "db-clock-update"
		updated, err := stamped.UpdateAtTransactionTime(ctx, created)
		if err != nil {
			return err
		}
		if updated.String(model.ColCreatedAt) != dbNow.String() ||
			updated.String(model.ColUpdatedAt) != again.String() {
			t.Fatalf("transaction-stamped update = %s/%s, want created %s, updated %s",
				updated.String(model.ColCreatedAt), updated.String(model.ColUpdatedAt),
				dbNow.String(), again.String())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresTransactionClockUsesServerTime exercises the other production
// implementation against an isolated real server. It is environment-gated by
// isolatedPG like the rest of this package's PostgreSQL integration coverage.
func TestPostgresTransactionClockUsesServerTime(t *testing.T) {
	ctx := context.Background()
	fixed := model.NewTimestamp(time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC))
	dsn := isolatedPG(t).App
	st, err := Open(ctx, store.Config{
		Engine:   store.EnginePostgres,
		DSN:      dsn,
		MaxConns: 4,
		Clock:    transactionClockFixedAppClock{now: fixed},
	}, nil)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	tenant := provisionTenant(t, st, "postgres-transaction-clock")
	var got model.Timestamp
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			t.Fatal("PostgreSQL tenant Scope does not expose store.TransactionClock")
		}
		var clockErr error
		got, clockErr = clock.TransactionNow(ctx)
		return clockErr
	}); err != nil {
		t.Fatalf("read PostgreSQL transaction clock: %v", err)
	}
	if got.String() == fixed.String() {
		t.Fatalf("transaction clock returned injected application time %s", got.String())
	}
	if _, err := model.ParseTimestamp(got.String()); err != nil {
		t.Fatalf("transaction time is not canonical: %v", err)
	}
}

// TestTransactionClockSurvivesWorkspaceConfinement proves both directions of
// the optional capability boundary: the raw SQL Scope has the capability and
// ConfineWorkspace forwards that exact transactional source instead of hiding
// it or substituting an application clock.
func TestTransactionClockSurvivesWorkspaceConfinement(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "confined-transaction-clock")

	if err := st.View(ctx, tenant, func(raw store.Scope) error {
		workspace, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		// Embedding only the declared Scope interface deliberately masks optional
		// capabilities implemented by the dynamic SQL scope. Confinement must not
		// manufacture TransactionClock when its input does not expose it.
		masked := struct{ store.Scope }{Scope: raw}
		withoutClock, err := store.ConfineWorkspace(ctx, masked, workspace.ID)
		if err != nil {
			return err
		}
		if _, ok := withoutClock.(store.TransactionClock); ok {
			t.Fatal("workspace confinement manufactured TransactionClock for an incapable Scope")
		}

		confined, err := store.ConfineWorkspace(ctx, raw, workspace.ID)
		if err != nil {
			return err
		}
		rawClock, rawOK := raw.(store.TransactionClock)
		confinedClock, confinedOK := confined.(store.TransactionClock)
		if !rawOK || !confinedOK {
			t.Fatalf("TransactionClock availability raw=%v confined=%v", rawOK, confinedOK)
		}
		rawNow, err := rawClock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		confinedNow, err := confinedClock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		if confinedNow.Before(rawNow) {
			t.Fatalf("forwarded clock moved backwards: raw=%s confined=%s", rawNow.String(), confinedNow.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("read confined transaction clock: %v", err)
	}
}
