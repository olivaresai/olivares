// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func loginCapabilitySQLiteDialect(t *testing.T) dialect.Dialect {
	t.Helper()
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("SQLite dialect unavailable")
	}
	return dia
}

func openLoginCapabilitySQLite(t *testing.T) (store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capability.db")
	st, err := openSQLiteDestination(t, path)
	if err != nil {
		t.Fatalf("open the SQLite store: %v", err)
	}
	return st, path
}

func observeLoginCapability(t *testing.T, st store.Store, version string) store.LoginCapabilityObservation {
	t.Helper()
	ctx := context.Background()
	var out store.LoginCapabilityObservation
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		if _, err := as.LoginCapability().Lock(ctx); err != nil {
			return err
		}
		obs, err := as.LoginCapability().Observe(ctx, version)
		out = obs
		return err
	}); err != nil {
		t.Fatalf("observe %q: %v", version, err)
	}
	return out
}

func readLoginCapabilityView(t *testing.T, st store.Store) store.LoginCapabilityObservation {
	t.Helper()
	ctx := context.Background()
	var out store.LoginCapabilityObservation
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		obs, err := as.LoginCapability().Read(ctx)
		out = obs
		return err
	}); err != nil {
		t.Fatalf("read the capability: %v", err)
	}
	return out
}

func trackedCoreVersions(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "SELECT version FROM "+coreTrackingTable+" ORDER BY version")
	if err != nil {
		t.Fatalf("read core tracking: %v", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan core tracking: %v", err)
		}
		out = append(out, v)
	}
	return out
}

func TestLoginCapabilitySQLitePlanIsElevenThenThirteenWithNoSeed(t *testing.T) {
	st, _ := openLoginCapabilitySQLite(t)
	got := trackedCoreVersions(t, st.(*sqlStore).db)
	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 13}
	if len(got) != len(want) {
		t.Fatalf("tracked core versions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tracked core versions = %v, want %v", got, want)
		}
	}
	if obs := readLoginCapabilityView(t, st); obs.Present {
		t.Fatalf("a fresh store manufactured capability history: %+v", obs)
	}
	compiled := compiledCoreMigrationVersions(loginCapabilitySQLiteDialect(t))
	if _, ok := compiled[12]; ok {
		t.Fatal("the compiled plan registers reserved v12")
	}
	if _, ok := compiled[13]; !ok || coreSupportedMigrationVersion != 13 {
		t.Fatalf("the compiled plan or ceiling lacks v13: ceiling=%d", coreSupportedMigrationVersion)
	}
}

func TestLoginCapabilitySQLiteObserveIsIdempotentPerTransactionAndSurvivesRestart(t *testing.T) {
	st, path := openLoginCapabilitySQLite(t)
	ctx := context.Background()
	var first, repeat, other store.LoginCapabilityObservation
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		port := as.LoginCapability()
		locked, err := port.Lock(ctx)
		if err != nil {
			return err
		}
		if locked.Present {
			t.Errorf("lock on a fresh store reported history: %+v", locked)
		}
		if first, err = port.Observe(ctx, "artifact-1"); err != nil {
			return err
		}
		if repeat, err = port.Observe(ctx, "artifact-1"); err != nil {
			return err
		}
		if _, err := as.LoginCapability().Lock(ctx); err != nil {
			return err
		}
		other, err = as.LoginCapability().Observe(ctx, "artifact-other")
		return err
	}); err != nil {
		t.Fatalf("first transaction: %v", err)
	}
	if !first.Present || first.ObservationCount != 1 || repeat != first || other != first {
		t.Fatalf("observations first=%+v repeat=%+v other=%+v, want one shared increment", first, repeat, other)
	}
	second := observeLoginCapability(t, st, "artifact-2")
	if second.ObservationCount != 2 || !second.FirstObservedAt.Equal(first.FirstObservedAt) || second.LastArtifactVersion != "artifact-2" {
		t.Fatalf("second observation %+v does not preserve the first time %v", second, first.FirstObservedAt)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := openSQLiteDestination(t, path)
	if err != nil {
		t.Fatalf("restart on the same database: %v", err)
	}
	if got := readLoginCapabilityView(t, reopened); got.ObservationCount != 2 || !got.FirstObservedAt.Equal(first.FirstObservedAt) {
		t.Fatalf("after restart %+v", got)
	}
}

func TestLoginCapabilitySQLiteViewRefusesLockAndObserve(t *testing.T) {
	st, _ := openLoginCapabilitySQLite(t)
	ctx := context.Background()
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		if _, err := as.LoginCapability().Lock(ctx); !errors.Is(err, store.ErrReadOnly) {
			t.Errorf("Lock in AuthView = %v, want ErrReadOnly", err)
		}
		if _, err := as.LoginCapability().Observe(ctx, "v"); !errors.Is(err, store.ErrReadOnly) {
			t.Errorf("Observe in AuthView = %v, want ErrReadOnly", err)
		}
		_, err := as.LoginCapability().Read(ctx)
		return err
	}); err != nil {
		t.Fatalf("AuthView: %v", err)
	}
}

func TestLoginCapabilitySQLiteDiscardedObserveWithoutLockRefusesCommit(t *testing.T) {
	st, _ := openLoginCapabilitySQLite(t)
	ctx := context.Background()
	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, _ = as.LoginCapability().Observe(ctx, "artifact") // discarded on purpose
		return nil
	})
	if !errors.Is(err, store.ErrLoginCapabilityNotLocked) {
		t.Fatalf("AuthMutate = %v, want the retained ErrLoginCapabilityNotLocked", err)
	}
	if obs := readLoginCapabilityView(t, st); obs.Present {
		t.Fatalf("a refused transaction left history: %+v", obs)
	}
}

func TestLoginCapabilitySQLiteSwallowedGoSideErrorRollsBackEarlierWrite(t *testing.T) {
	st, _ := openLoginCapabilitySQLite(t)
	ctx := context.Background()
	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		if _, err := as.LoginCapability().Lock(ctx); err != nil {
			return err
		}
		if _, err := as.LoginCapability().Observe(ctx, "artifact"); err != nil {
			return err
		}
		// A Go-side refusal that sends no SQL, obtained through a NEW port wrapper and
		// discarded: the transaction-owned first error must still refuse the commit.
		_, _ = as.LoginCapability().Observe(ctx, "")
		return nil
	})
	if !errors.Is(err, store.ErrLoginCapabilityArtifactVersion) {
		t.Fatalf("AuthMutate = %v, want the retained ErrLoginCapabilityArtifactVersion", err)
	}
	if obs := readLoginCapabilityView(t, st); obs.Present {
		t.Fatalf("the earlier observation committed despite the discarded failure: %+v", obs)
	}
}

func countAuthAudit(t *testing.T, st store.Store) int {
	t.Helper()
	ctx := context.Background()
	n := 0
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		return as.Audit().Walk(ctx, 0, func(model.AuditEvent) error { n++; return nil })
	}); err != nil {
		t.Fatalf("walk auth audit: %v", err)
	}
	return n
}

func TestLoginCapabilitySQLiteLockAfterAuditIsRefusedAndRollsBack(t *testing.T) {
	st, _ := openLoginCapabilitySQLite(t)
	ctx := context.Background()
	before := countAuthAudit(t, st)
	var lockErr error
	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		if _, err := as.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:" + model.NewID().String(), ActorKind: model.ActorUser,
			Action: "r5.capability.order.test", TargetKind: "core.user", TargetID: model.NewID(),
		}); err != nil {
			return err
		}
		_, lockErr = as.LoginCapability().Lock(ctx)
		return nil // discard the refusal
	})
	if !errors.Is(lockErr, store.ErrLoginCapabilityLockOrder) {
		t.Fatalf("Lock after audit = %v, want ErrLoginCapabilityLockOrder", lockErr)
	}
	if !errors.Is(err, store.ErrLoginCapabilityLockOrder) {
		t.Fatalf("AuthMutate = %v, want the retained order refusal", err)
	}
	if after := countAuthAudit(t, st); after != before {
		t.Fatalf("audit events %d -> %d: the refused transaction committed its audit append", before, after)
	}
}

func TestLoginCapabilitySQLiteClockReversalKeepsHistory(t *testing.T) {
	st, _ := openLoginCapabilitySQLite(t)
	db := st.(*sqlStore).db
	if _, err := db.ExecContext(context.Background(), `INSERT INTO login_capability_observation
VALUES ('global/default/login-enforcement', '2099-01-01T00:00:00.000Z', '2099-01-01T00:00:00.000Z', 'future', 5)`); err != nil {
		t.Fatalf("seed a future observation fixture: %v", err)
	}
	obs := observeLoginCapability(t, st, "artifact")
	if obs.ObservationCount != 6 || obs.FirstObservedAt.Year() != 2099 || !obs.LastObservedAt.Before(obs.FirstObservedAt) || obs.LastArtifactVersion != "artifact" {
		t.Fatalf("observation after clock reversal = %+v", obs)
	}
}

func TestLoginCapabilitySQLiteMalformedStoredValueIsAnError(t *testing.T) {
	st, _ := openLoginCapabilitySQLite(t)
	ctx := context.Background()
	db := st.(*sqlStore).db
	for _, stmt := range []string{
		"PRAGMA ignore_check_constraints = ON",
		`INSERT INTO login_capability_observation VALUES ('global/default/login-enforcement', 'yesterday', '2026-01-01T00:00:00.000Z', 'v', 1)`,
		"PRAGMA ignore_check_constraints = OFF",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	err := st.AuthView(ctx, func(as store.AuthScope) error {
		_, err := as.LoginCapability().Read(ctx)
		return err
	})
	if !errors.Is(err, store.ErrLoginCapabilityMalformed) {
		t.Fatalf("Read of a malformed row = %v, want ErrLoginCapabilityMalformed", err)
	}
	err = st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, _ = as.LoginCapability().Lock(ctx)
		return nil
	})
	if !errors.Is(err, store.ErrLoginCapabilityMalformed) {
		t.Fatalf("AuthMutate after a malformed Lock read = %v, want the retained error", err)
	}
}

func TestLoginCapabilitySQLiteConcurrentObservationsOnMissingRowSerialize(t *testing.T) {
	st, _ := openLoginCapabilitySQLite(t)
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			errs <- st.AuthMutate(ctx, func(as store.AuthScope) error {
				if _, err := as.LoginCapability().Lock(ctx); err != nil {
					return err
				}
				_, err := as.LoginCapability().Observe(ctx, "artifact")
				return err
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent observation: %v", err)
		}
	}
	if obs := readLoginCapabilityView(t, st); obs.ObservationCount != workers {
		t.Fatalf("observation count = %d, want %d", obs.ObservationCount, workers)
	}
}

func TestLoginCapabilitySQLitePerBootRefusesTrackingAndShapeDrift(t *testing.T) {
	for name, drift := range map[string]string{
		"tracking identity": "UPDATE " + coreTrackingTable + " SET name = 'foreign' WHERE version = 13",
		"relation shape":    "ALTER TABLE login_capability_observation ADD COLUMN extra TEXT",
	} {
		t.Run(name, func(t *testing.T) {
			st, path := openLoginCapabilitySQLite(t)
			if _, err := st.(*sqlStore).db.ExecContext(context.Background(), drift); err != nil {
				t.Fatalf("apply drift: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if _, err := openSQLiteDestination(t, path); err == nil {
				t.Fatal("a drifted v13 was accepted on restart")
			}
		})
	}
}

func TestCoreMigrationVersionPreflightRefusesVersionsAbsentFromCompiledPlan(t *testing.T) {
	ctx := context.Background()
	dia := loginCapabilitySQLiteDialect(t)
	fixture := func(t *testing.T, versions ...int) *sql.DB {
		t.Helper()
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "preflight.db"))
		if err != nil {
			t.Fatalf("open fixture: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		createCoreVersionTracking(t, ctx, db)
		for _, v := range versions {
			insertCoreVersion(t, ctx, db, dia, v, nil)
		}
		return db
	}
	base := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	if err := preflightCoreMigrationVersion(ctx, fixture(t, append(base, 13)...), dia, coreSupportedMigrationVersion); err != nil {
		t.Fatalf("the legitimate 1..11,13 history was refused: %v", err)
	}
	if err := preflightCoreMigrationVersion(ctx, fixture(t, base...), dia, coreSupportedMigrationVersion); err != nil {
		t.Fatalf("the 1..11 predecessor was refused: %v", err)
	}
	err := preflightCoreMigrationVersion(ctx, fixture(t, append(base, 12, 13)...), dia, coreSupportedMigrationVersion)
	if !errors.Is(err, ErrCoreSchemaVersionUnrecognized) || errors.Is(err, ErrCoreSchemaVersionAhead) {
		t.Fatalf("a foreign v12 below ceiling 13 = %v, want ErrCoreSchemaVersionUnrecognized", err)
	}
	if err := preflightCoreMigrationVersion(ctx, fixture(t, append(base, 13)...), dia, 11); !errors.Is(err, ErrCoreSchemaVersionAhead) {
		t.Fatalf("an old ceiling-11 binary on v13 = %v, want ErrCoreSchemaVersionAhead", err)
	}
}
