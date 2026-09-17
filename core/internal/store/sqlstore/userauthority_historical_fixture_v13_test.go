// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

func historicalFixtureSQLite(t *testing.T) (*sqlStore, dialect.Dialect) {
	t.Helper()
	st := openSQLiteTest(t, nil)
	sqlSt, ok := st.(*sqlStore)
	if !ok {
		t.Fatal("openSQLiteTest did not return *sqlStore")
	}
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("SQLite dialect unavailable")
	}
	return sqlSt, dia
}

func TestHistoricalFixtureRewindsCompleteV13OnSQLite(t *testing.T) {
	st, dia := historicalFixtureSQLite(t)
	ctx := context.Background()
	present, tracked, err := inspectLoginCapabilityHistoricalFixtureHalves(ctx, st.db, dia)
	if err != nil {
		t.Fatalf("inspect current fixture: %v", err)
	}
	if !present || !tracked {
		t.Fatalf("current fixture missing v%d halves: relation=%v tracked=%v",
			coreLoginCapabilityMigrationVersion, present, tracked)
	}

	if err := rewindLoginCapabilityForHistoricalFixture(ctx, st.db, dia); err != nil {
		t.Fatalf("complete v%d rewind: %v", coreLoginCapabilityMigrationVersion, err)
	}
	if err := loginCapabilityHistoricalPredecessorAbsence(ctx, st.db, dia); err != nil {
		t.Fatal(err)
	}

	// Both-absent is a valid reconstructed entry, not a second drop.
	if err := rewindLoginCapabilityForHistoricalFixture(ctx, st.db, dia); err != nil {
		t.Fatalf("already pre-v%d rewind: %v", coreLoginCapabilityMigrationVersion, err)
	}
	dropUserAuthorityForHistoricalFixture(t, st.db, dia)
	if err := loginCapabilityHistoricalPredecessorAbsence(ctx, st.db, dia); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalFixtureRefusesHalfPresentV13OnSQLite(t *testing.T) {
	ctx := context.Background()
	t.Run("relation without tracking row", func(t *testing.T) {
		st, dia := historicalFixtureSQLite(t)
		if _, err := st.db.ExecContext(ctx, dia.Rebind(
			"DELETE FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
			coreLoginCapabilityMigrationVersion); err != nil {
			t.Fatalf("remove the v%d tracking row: %v", coreLoginCapabilityMigrationVersion, err)
		}
		err := rewindLoginCapabilityForHistoricalFixture(ctx, st.db, dia)
		if err == nil {
			t.Fatal("a relation-only v13 fixture was accepted")
		}
		if !strings.Contains(err.Error(), dialect.LoginCapabilityObservationTable) ||
			!strings.Contains(err.Error(), "without a v13 tracking row") {
			t.Fatalf("causal error = %v", err)
		}
	})
	t.Run("tracking row without relation", func(t *testing.T) {
		st, dia := historicalFixtureSQLite(t)
		if _, err := st.db.ExecContext(ctx, "DROP TABLE "+directoryWriterRelation(dia, dialect.LoginCapabilityObservationTable)); err != nil {
			t.Fatalf("drop %s: %v", dialect.LoginCapabilityObservationTable, err)
		}
		err := rewindLoginCapabilityForHistoricalFixture(ctx, st.db, dia)
		if err == nil {
			t.Fatal("a tracking-only v13 fixture was accepted")
		}
		if !strings.Contains(err.Error(), "tracked without") ||
			!strings.Contains(err.Error(), dialect.LoginCapabilityObservationTable) {
			t.Fatalf("causal error = %v", err)
		}
	})
}

func TestHistoricalPredecessorAssertsRetainedV13HalvesOnSQLite(t *testing.T) {
	ctx := context.Background()
	t.Run("retained relation", func(t *testing.T) {
		st, dia := historicalFixtureSQLite(t)
		dropUserAuthorityForHistoricalFixture(t, st.db, dia)
		stmts := dia.LoginCapabilityControlStmts()
		if len(stmts) == 0 {
			t.Fatal("dialect rendered no login capability statements")
		}
		if _, err := st.db.ExecContext(ctx, stmts[0]); err != nil {
			t.Fatalf("reintroduce %s: %v", dialect.LoginCapabilityObservationTable, err)
		}
		err := loginCapabilityHistoricalPredecessorAbsence(ctx, st.db, dia)
		if err == nil {
			t.Fatal("a retained v13 relation was accepted as absent")
		}
		if !strings.Contains(err.Error(), dialect.LoginCapabilityObservationTable) ||
			!strings.Contains(err.Error(), "remains without a v13 tracking row") {
			t.Fatalf("causal error = %v", err)
		}
	})
	t.Run("retained tracking row", func(t *testing.T) {
		st, dia := historicalFixtureSQLite(t)
		dropUserAuthorityForHistoricalFixture(t, st.db, dia)
		if _, err := st.db.ExecContext(ctx, dia.Rebind(
			"INSERT INTO "+coreTrackingRelation(dia)+
				"(version,name,applied_at,phase) VALUES (?,?,?,?)"),
			coreLoginCapabilityMigrationVersion, coreLoginCapabilityMigrationName,
			"2026-09-14T00:00:00.000Z", "expand"); err != nil {
			t.Fatalf("reintroduce the v%d tracking row: %v", coreLoginCapabilityMigrationVersion, err)
		}
		err := loginCapabilityHistoricalPredecessorAbsence(ctx, st.db, dia)
		if err == nil {
			t.Fatal("a retained v13 tracking row was accepted as absent")
		}
		if !strings.Contains(err.Error(), "still tracked without") ||
			!strings.Contains(err.Error(), dialect.LoginCapabilityObservationTable) {
			t.Fatalf("causal error = %v", err)
		}
	})
}
