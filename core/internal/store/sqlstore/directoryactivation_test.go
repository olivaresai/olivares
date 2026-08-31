// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestDirectoryActivationSQLiteCutoverRetryAndReopen(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{
		Engine: store.EngineSQLite,
		DSN:    filepath.Join(t.TempDir(), "directory-activation.db"),
	}
	raw, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("Open staged store: %v", err)
	}
	ss := raw.(*sqlStore)
	tenant := provisionTenant(t, raw, "directory-activation")
	legacyTarget := mustCreateAgent(t, raw, tenant, "before-activation")
	directoryActivationTestWantSQLitePresentation(t, ss.db, tenant.String(), "text", 1)

	cached, supported, err := ss.DirectoryStatus(ctx)
	if err != nil || !supported {
		t.Fatalf("initial DirectoryStatus = %+v supported=%t err=%v", cached, supported, err)
	}
	if cached.Enabled || !cached.EpochCoverageComplete ||
		cached.ControlMode != store.DirectoryControlStaged ||
		cached.WriterPosture != store.DirectoryWriterSQLiteCapability ||
		cached.ExpectedGeneration != 1 {
		t.Fatalf("initial DirectoryStatus = %+v", cached)
	}

	before, after, changed, err := ActivateDirectoryWriter(ctx, raw, cfg, 1)
	if err != nil {
		t.Fatalf("ActivateDirectoryWriter: %v", err)
	}
	if !changed || before != cached || after.Enabled || !after.EpochCoverageComplete ||
		after.ControlMode != store.DirectoryControlEnforced ||
		after.WriterPosture != store.DirectoryWriterSQLiteCapability ||
		after.ExpectedGeneration != 2 {
		t.Fatalf("activation = before:%+v after:%+v changed:%t", before, after, changed)
	}
	directoryActivationTestWantSQLitePresentation(t, ss.db, tenant.String(), "text", 1)
	stillCached, _, err := ss.DirectoryStatus(ctx)
	if err != nil || stillCached != cached {
		t.Fatalf("activation rewrote immutable boot witness: got %+v err=%v want %+v",
			stillCached, err, cached)
	}

	// A raw/old writer carries no marker and is fenced immediately after the
	// durable cutover. The supported wrapper reads generation 2 from durable
	// control and continues to write even though this process's boot witness is
	// intentionally still staged/off.
	if _, err := ss.db.ExecContext(ctx,
		"UPDATE main.agents SET name = name WHERE id = ?", legacyTarget.ID.String(),
	); err == nil || !strings.Contains(err.Error(), "directory writer generation required") {
		t.Fatalf("raw old writer after activation error = %v", err)
	}
	mustCreateAgent(t, raw, tenant, "after-activation")
	directoryActivationTestWantSQLiteMarkerBaseline(t, ss.db)

	// The original precondition is an idempotent retry; the durable generation
	// itself is also an explicit verify-only invocation. Neither advances again.
	for name, expected := range map[string]int64{"retry": 1, "verify-only": 2} {
		t.Run(name, func(t *testing.T) {
			gotBefore, gotAfter, gotChanged, err := ActivateDirectoryWriter(ctx, raw, cfg, expected)
			if err != nil {
				t.Fatalf("ActivateDirectoryWriter(%d): %v", expected, err)
			}
			if gotChanged || gotBefore.ControlMode != store.DirectoryControlEnforced ||
				gotAfter.ControlMode != store.DirectoryControlEnforced ||
				gotAfter.ExpectedGeneration != 2 || gotAfter.Enabled {
				t.Fatalf("ActivateDirectoryWriter(%d) = before:%+v after:%+v changed:%t",
					expected, gotBefore, gotAfter, gotChanged)
			}
			directoryActivationTestWantSQLitePresentation(t, ss.db, tenant.String(), "text", 1)
		})
	}
	if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, 3); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale generation error = %v, want %v", err, store.ErrConflict)
	}

	if err := raw.Close(); err != nil {
		t.Fatalf("close activated store: %v", err)
	}
	reopened, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("reopen activated store: %v", err)
	}
	defer reopened.Close() //nolint:errcheck
	reopenedStatus, ok, err := reopened.(store.DirectoryStatuser).DirectoryStatus(ctx)
	if err != nil || !ok || reopenedStatus.Enabled ||
		reopenedStatus.ControlMode != store.DirectoryControlEnforced ||
		reopenedStatus.ExpectedGeneration != 2 || !reopenedStatus.EpochCoverageComplete {
		t.Fatalf("reopened DirectoryStatus = %+v supported=%t err=%v",
			reopenedStatus, ok, err)
	}
}

func TestDirectoryActivationSQLiteRejectsGenerationOverflowWithoutMutation(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "max.db")}
	raw, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	ss := raw.(*sqlStore)
	if _, err := ss.db.ExecContext(ctx, `UPDATE main.directory_writer_control
SET mode = 'staged', expected_generation = ?`, int64(math.MaxInt64)); err != nil {
		t.Fatalf("seed maximum generation: %v", err)
	}
	if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, math.MaxInt64); err == nil {
		t.Fatal("maximum expected generation unexpectedly activated")
	}
	var mode, storageClass string
	var generation int64
	if err := ss.db.QueryRowContext(ctx, `SELECT mode, expected_generation,
typeof(expected_generation) FROM main.directory_writer_control`).Scan(
		&mode, &generation, &storageClass,
	); err != nil {
		t.Fatalf("read maximum generation baseline: %v", err)
	}
	if mode != string(directoryWriterStaged) || generation != math.MaxInt64 || storageClass != "integer" {
		t.Fatalf("maximum generation baseline = mode:%q generation:%d storage:%q",
			mode, generation, storageClass)
	}
}

func TestDirectoryActivationSQLiteAssertionsAreExactAndNeverHeal(t *testing.T) {
	ctx := context.Background()

	t.Run("missing writer guard", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		name := sqliteDirectoryWriterGuardSpecs()[0].Name
		if _, err := ss.db.ExecContext(ctx, "DROP TRIGGER main."+name); err != nil {
			t.Fatalf("drop writer trigger: %v", err)
		}
		if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, 1); err == nil {
			t.Fatal("activation healed a missing writer trigger")
		}
		var count int
		if err := ss.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM main.sqlite_master WHERE type='trigger' AND name=?", name,
		).Scan(&count); err != nil || count != 0 {
			t.Fatalf("missing trigger after refusal count=%d err=%v", count, err)
		}
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterStaged, 1)
	})

	t.Run("durable marker contamination", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		if _, err := ss.db.ExecContext(ctx, `INSERT INTO main.directory_writer_marker
(control_key, generation) VALUES (?, ?)`, dialect.DirectoryWriterControlKey, 1); err != nil {
			t.Fatalf("contaminate marker: %v", err)
		}
		if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, 1); err == nil {
			t.Fatal("activation accepted a contaminated marker")
		}
		var rows int
		if err := ss.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM main.directory_writer_marker").Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("marker after refusal rows=%d err=%v", rows, err)
		}
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterStaged, 1)
	})

	t.Run("directory tombstone baseline drift", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		const trigger = "core_directory_tombstone_no_delete"
		if _, err := ss.db.ExecContext(ctx, "DROP TRIGGER main."+trigger); err != nil {
			t.Fatalf("drop tombstone trigger: %v", err)
		}
		if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, 1); err == nil ||
			!strings.Contains(err.Error(), "exact directory baseline") {
			t.Fatalf("activation with tombstone drift error = %v", err)
		}
		var count int
		if err := ss.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM main.sqlite_master WHERE type='trigger' AND name=?", trigger,
		).Scan(&count); err != nil || count != 0 {
			t.Fatalf("tombstone trigger after refusal count=%d err=%v", count, err)
		}
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterStaged, 1)
	})

	t.Run("organization epoch coverage", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		tenant := provisionTenant(t, raw, "activation-missing-epoch")
		tx, err := ss.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin epoch corruption: %v", err)
		}
		if err := bindDirectoryTenant(ctx, tx, ss.dia, tenant); err != nil {
			_ = tx.Rollback()
			t.Fatalf("bind epoch corruption: %v", err)
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM main.core_directory_epoch WHERE tenant_id = ?", tenant.String(),
		); err != nil {
			_ = tx.Rollback()
			t.Fatalf("delete epoch: %v", err)
		}
		if err := restoreSystemDirectoryBaseline(ctx, tx, ss.dia); err != nil {
			_ = tx.Rollback()
			t.Fatalf("restore epoch corruption baseline: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit epoch corruption: %v", err)
		}
		if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, 1); !errors.Is(err, store.ErrDirectoryUnavailable) {
			t.Fatalf("activation coverage error = %v, want %v", err, store.ErrDirectoryUnavailable)
		}
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterStaged, 1)
	})

	t.Run("orphan epoch coverage", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		orphan := model.NewTenantID()
		tx, err := ss.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin orphan epoch corruption: %v", err)
		}
		if err := bindDirectoryTenant(ctx, tx, ss.dia, orphan); err != nil {
			_ = tx.Rollback()
			t.Fatalf("bind orphan epoch corruption: %v", err)
		}
		if err := insertDirectoryEpochRow(ctx, tx, ss.dia, orphan); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert orphan epoch: %v", err)
		}
		if err := restoreSystemDirectoryBaseline(ctx, tx, ss.dia); err != nil {
			_ = tx.Rollback()
			t.Fatalf("restore orphan epoch corruption baseline: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit orphan epoch corruption: %v", err)
		}
		if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, 1); !errors.Is(err, store.ErrDirectoryUnavailable) {
			t.Fatalf("activation orphan coverage error = %v, want %v",
				err, store.ErrDirectoryUnavailable)
		}
		var count int
		if err := ss.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM main.core_directory_epoch WHERE tenant_id = ?",
			orphan.String(),
		).Scan(&count); err != nil || count != 1 {
			t.Fatalf("orphan epoch after refusal count=%d err=%v", count, err)
		}
		directoryActivationTestWantSQLitePresentation(
			t, ss.db, model.SystemTenantID.String(), "text", 1,
		)
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterStaged, 1)
	})

	t.Run("missing presentation", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		if _, err := ss.db.ExecContext(ctx,
			"DELETE FROM main."+dialect.ScopeTenantTable); err != nil {
			t.Fatalf("delete SQLite presentation pin: %v", err)
		}
		if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, 1); err == nil ||
			!strings.Contains(err.Error(), "presentation pin") {
			t.Fatalf("activation with missing SQLite presentation error = %v", err)
		}
		directoryActivationTestWantSQLitePresentation(t, ss.db, "", "", 0)
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterStaged, 1)
	})

	t.Run("multiple presentation rows", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		second := model.NewTenantID().String()
		if _, err := ss.db.ExecContext(ctx, "INSERT INTO main."+
			dialect.ScopeTenantTable+"(tenant_id) VALUES (?)", second); err != nil {
			t.Fatalf("insert second SQLite presentation pin: %v", err)
		}
		if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, 1); err == nil ||
			!strings.Contains(err.Error(), "more than one row") {
			t.Fatalf("activation with multiple SQLite presentation rows error = %v", err)
		}
		var total, expected int
		if err := ss.db.QueryRowContext(ctx, "SELECT COUNT(*), "+
			"SUM(CASE WHEN tenant_id IN (?, ?) AND typeof(tenant_id) = 'text' THEN 1 ELSE 0 END) "+
			"FROM main."+dialect.ScopeTenantTable,
			model.SystemTenantID.String(), second,
		).Scan(&total, &expected); err != nil || total != 2 || expected != 2 {
			t.Fatalf("SQLite multiple presentation rows after refusal total=%d expected=%d err=%v",
				total, expected, err)
		}
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterStaged, 1)
	})

	t.Run("noncanonical presentation tenant", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		const invalid = "11111111-1111-1111-1111-111111111111"
		if _, err := ss.db.ExecContext(ctx, "UPDATE main."+
			dialect.ScopeTenantTable+" SET tenant_id = ?", invalid); err != nil {
			t.Fatalf("set noncanonical SQLite presentation pin: %v", err)
		}
		if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, 1); err == nil ||
			!strings.Contains(err.Error(), "UUIDv7") {
			t.Fatalf("activation with noncanonical SQLite presentation error = %v", err)
		}
		directoryActivationTestWantSQLitePresentation(t, ss.db, invalid, "text", 1)
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterStaged, 1)
	})

	t.Run("non-text presentation tenant", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		if _, err := ss.db.ExecContext(ctx, "UPDATE main."+
			dialect.ScopeTenantTable+" SET tenant_id = X'0102'"); err != nil {
			t.Fatalf("set blob SQLite presentation pin: %v", err)
		}
		if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, 1); err == nil ||
			!strings.Contains(err.Error(), "canonical text tenant row") {
			t.Fatalf("activation with blob SQLite presentation error = %v", err)
		}
		var storageClass, encoded string
		if err := ss.db.QueryRowContext(ctx, "SELECT typeof(tenant_id), hex(tenant_id) FROM main."+
			dialect.ScopeTenantTable).Scan(&storageClass, &encoded); err != nil ||
			storageClass != "blob" || encoded != "0102" {
			t.Fatalf("SQLite blob presentation after refusal type=%q hex=%q err=%v",
				storageClass, encoded, err)
		}
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterStaged, 1)
	})
}

func TestDirectoryActivationSQLiteCommitBoundary(t *testing.T) {
	ctx := context.Background()
	original := directoryActivationCommitTestHook
	t.Cleanup(func() { directoryActivationCommitTestHook = original })

	t.Run("lost acknowledgement reconciles committed", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		directoryActivationCommitTestHook = func(tx *sql.Tx) error {
			if err := tx.Commit(); err != nil {
				return err
			}
			return errors.New("synthetic acknowledgement loss")
		}
		_, after, changed, err := ActivateDirectoryWriter(ctx, raw, cfg, 1)
		directoryActivationCommitTestHook = nil
		if err != nil || !changed || after.ControlMode != store.DirectoryControlEnforced ||
			after.ExpectedGeneration != 2 {
			t.Fatalf("lost acknowledgement result after=%+v changed=%t err=%v", after, changed, err)
		}
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterEnforced, 2)
	})

	t.Run("57014 at commit reconciles committed", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		directoryActivationCommitTestHook = func(tx *sql.Tx) error {
			if err := tx.Commit(); err != nil {
				return err
			}
			return &pgconn.PgError{Code: sqlStateQueryCanceled, Message: "synthetic commit cancel"}
		}
		_, after, changed, err := ActivateDirectoryWriter(ctx, raw, cfg, 1)
		directoryActivationCommitTestHook = nil
		if err != nil || !changed || after.ExpectedGeneration != 2 {
			t.Fatalf("57014 commit result after=%+v changed=%t err=%v", after, changed, err)
		}
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterEnforced, 2)
	})

	t.Run("ambiguous rollback is reported not committed", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		directoryActivationCommitTestHook = func(tx *sql.Tx) error {
			if err := tx.Rollback(); err != nil {
				return err
			}
			return errors.New("synthetic transport failure after rollback")
		}
		_, _, changed, err := ActivateDirectoryWriter(ctx, raw, cfg, 1)
		directoryActivationCommitTestHook = nil
		if err == nil || changed || errors.Is(err, ErrDirectoryWriterActivationIndeterminate) ||
			!strings.Contains(err.Error(), "did not commit") {
			t.Fatalf("rolled-back ambiguous commit changed=%t err=%v", changed, err)
		}
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterStaged, 1)
	})

	t.Run("known server rejection does not reconcile", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		directoryActivationCommitTestHook = func(tx *sql.Tx) error {
			if err := tx.Rollback(); err != nil {
				return err
			}
			return &pgconn.PgError{Code: "23514", Message: "synthetic rejected commit"}
		}
		before, after, changed, err := ActivateDirectoryWriter(ctx, raw, cfg, 1)
		directoryActivationCommitTestHook = nil
		if err == nil || changed || errors.Is(err, ErrDirectoryWriterActivationIndeterminate) ||
			before != after || after.ControlMode != store.DirectoryControlStaged {
			t.Fatalf("known rejected commit before=%+v after=%+v changed=%t err=%v",
				before, after, changed, err)
		}
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterStaged, 1)
	})

	t.Run("successful commit requires fresh exact postcondition", func(t *testing.T) {
		cfg, raw, ss := directoryActivationTestOpenSQLite(t)
		directoryActivationCommitTestHook = func(tx *sql.Tx) error {
			if err := tx.Commit(); err != nil {
				return err
			}
			_, err := ss.db.ExecContext(ctx, `UPDATE main.directory_writer_control
SET mode = 'staged', expected_generation = 1`)
			return err
		}
		_, after, changed, err := ActivateDirectoryWriter(ctx, raw, cfg, 1)
		directoryActivationCommitTestHook = nil
		if !errors.Is(err, ErrDirectoryWriterActivationIndeterminate) || changed ||
			after.ControlMode != store.DirectoryControlStaged || after.ExpectedGeneration != 1 {
			t.Fatalf("drifted postcondition after=%+v changed=%t err=%v", after, changed, err)
		}
		directoryActivationTestWantSQLiteControl(t, ss.db, directoryWriterStaged, 1)
	})
}

func directoryActivationTestOpenSQLite(
	t *testing.T,
) (store.Config, store.Store, *sqlStore) {
	t.Helper()
	cfg := store.Config{
		Engine: store.EngineSQLite,
		DSN:    filepath.Join(t.TempDir(), "activation.db"),
	}
	raw, err := Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Open activation store: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return cfg, raw, raw.(*sqlStore)
}

func directoryActivationTestWantSQLiteControl(
	t *testing.T,
	db *sql.DB,
	mode directoryWriterMode,
	generation int64,
) {
	t.Helper()
	var gotMode string
	var gotGeneration int64
	if err := db.QueryRowContext(context.Background(), `SELECT mode, expected_generation
FROM main.directory_writer_control`).Scan(&gotMode, &gotGeneration); err != nil {
		t.Fatalf("read directory writer control: %v", err)
	}
	if gotMode != string(mode) || gotGeneration != generation {
		t.Fatalf("directory writer control = %s/%d, want %s/%d",
			gotMode, gotGeneration, mode, generation)
	}
}

func directoryActivationTestWantSQLiteMarkerBaseline(t *testing.T, db *sql.DB) {
	t.Helper()
	var rows int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM main.directory_writer_marker").Scan(&rows); err != nil {
		t.Fatalf("read marker baseline: %v", err)
	}
	if rows != 0 {
		t.Fatalf("marker baseline rows = %d, want 0", rows)
	}
}

func directoryActivationTestWantSQLitePresentation(
	t *testing.T,
	db *sql.DB,
	tenant string,
	storageClass string,
	count int,
) {
	t.Helper()
	var gotCount int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM main."+dialect.ScopeTenantTable,
	).Scan(&gotCount); err != nil {
		t.Fatalf("read SQLite presentation count: %v", err)
	}
	if gotCount != count {
		t.Fatalf("SQLite presentation rows = %d, want %d", gotCount, count)
	}
	if count == 0 {
		return
	}
	var gotTenant, gotStorageClass string
	if err := db.QueryRowContext(context.Background(),
		"SELECT tenant_id, typeof(tenant_id) FROM main."+dialect.ScopeTenantTable,
	).Scan(&gotTenant, &gotStorageClass); err != nil {
		t.Fatalf("read SQLite presentation row: %v", err)
	}
	if gotTenant != tenant || gotStorageClass != storageClass {
		t.Fatalf("SQLite presentation = tenant:%q storage:%q, want tenant:%q storage:%q",
			gotTenant, gotStorageClass, tenant, storageClass)
	}
}
