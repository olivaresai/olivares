// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// dropUserAuthorityForHistoricalFixture rewinds a database this build has just
// created back to the exact v9 predecessor core v10 observed before it ran.
//
// WHY IT IS NOT "DELETE THE v10 TRACKING ROW". A historical fixture reconstructs a
// checkpoint that a real deployment stood on, and no v9 deployment carried H, the
// four-column writer control, the protocol-aware source guards or the two v10
// PostgreSQL routines. Removing the row alone stages a history whose ledger says
// v9 and whose objects say v10 — which is precisely the state coreUserAuthorityMigration
// refuses ("untracked User authority relation already exists") and the state that made
// v7 verify its own freshly-created three-column control against the v10 shape.
//
// HOW IT STAYS AUTHENTIC. Every object below is dropped or rendered from the same
// production definition coreUserAuthorityMigration itself uses — dia.DirectoryWriterControlStmts
// for the frozen three-column control (and, on SQLite, its marker), the preserved
// legacySQLiteDirectoryWriterGuardBody / postgresDirectoryWriterGuardBody source guards,
// and the migration's own object names. Nothing here invents a shape: the steps are
// coreUserAuthorityMigration read backwards. The predecessor's mode and expected
// generation are carried across unchanged, because v10 carries them across unchanged.
//
// Given the valid current fixture at entry, this restores its v10-owned predecessor
// surface after first removing core v13's login capability relation and tracking row
// together (newest first). A caller that already stands pre-v13 — both halves absent —
// continues; a half-present v13 surface is refused rather than silently repaired.
// A complete K2 reconstruction must also remove v9 access evidence and the
// v8/v7 state and history; rewindPostgresToK2 owns that complete fixture operation.
// Intermediate disposable DDL states are not deployable historical checkpoints.
func dropUserAuthorityForHistoricalFixture(t *testing.T, db *sql.DB, dia dialect.Dialect) {
	t.Helper()
	ctx := context.Background()
	exec := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("rewind core v10: %s: %v", strings.SplitN(query, "\n", 2)[0], err)
		}
	}

	dropLoginCapabilityForHistoricalFixture(t, db, dia)

	// A pre-v10 database is pre-v11 as well. Forget the v11 record first; the
	// seven-word journal it leaves is the untracked verify-only v11 input, so the
	// next boot records v11 again without DDL.
	if _, err := db.ExecContext(ctx, dia.Rebind(
		"DELETE FROM "+coreTrackingRelation(dia)+" WHERE version = ?"), coreEvidenceRefusedMigrationVersion); err != nil {
		t.Fatalf("rewind core v11 before v10: %v", err)
	}
	tracked, err := coreVersionIsTracked(ctx, db, dia, coreUserAuthorityMigrationVersion)
	if err != nil {
		t.Fatalf("rewind core v10: read the v%d tracking row: %v", coreUserAuthorityMigrationVersion, err)
	}
	if !tracked {
		t.Fatalf("rewind core v10: the fixture database does not record v%d; there is no v10 to undo",
			coreUserAuthorityMigrationVersion)
	}
	// The predecessor is a LEGACY-protocol control. A database whose maintenance
	// transaction has already cut over to user-authority-v1 has crossed a rollout
	// this rewind cannot honestly reverse — its directory epochs were recorded under
	// the target protocol — so it is refused rather than downgraded in place.
	state, err := readDirectoryWriterControlState(ctx, db, dia)
	if err != nil {
		t.Fatalf("rewind core v10: read the v10 writer control: %v", err)
	}
	if state.CoverageProtocol != coverageProtocolLegacy {
		t.Fatalf("rewind core v10: the control stands at coverage protocol %q; a completed %s rollout has no %s predecessor to rewind to",
			state.CoverageProtocol, coverageProtocolTarget, coverageProtocolLegacy)
	}

	// 1. The source guards v10 authored, over its own eleven-table inventory. These
	//    go first for the same reason v10 dropped the legacy ones first: while they
	//    exist they read the control this rewind is about to replace.
	switch dia.Name() {
	case store.EngineSQLite:
		for _, spec := range sqliteDirectoryWriterGuardSpecs() {
			exec("DROP TRIGGER IF EXISTS main." + quoteIdent(spec.Name))
		}
	case store.EnginePostgres:
		for _, table := range directoryWriterSourceTables {
			exec("DROP TRIGGER IF EXISTS " + quoteIdent(table+"_directory_writer_guard") +
				" ON public." + quoteIdent(table))
		}
		exec("DROP FUNCTION " + directoryWriterRelation(dia, dialect.DirectoryWriterGuardFunction) + "()")
	default:
		t.Fatalf("rewind core v10: unsupported engine %q", dia.Name())
	}

	// 2. H itself. Its retention guard and, on PostgreSQL, its writer-guard trigger
	//    are attached to the relation and go with it; the two v10 routines do not.
	exec("DROP TABLE " + directoryWriterRelation(dia, userAuthorityDescriptor.Table))
	if dia.Name() == store.EnginePostgres {
		exec("DROP FUNCTION public.olivares_retain_user_authority()")
		exec("DROP FUNCTION public.olivares_lock_core_user_authority(text)")
	}

	// 3. The control back to the frozen three-column v7 render, carrying the
	//    predecessor's mode and generation across as v10 did. stmts[1] is v7's
	//    initial staged/generation-1 seed and would silently downgrade an enforced
	//    predecessor, so the tuple is reinserted explicitly instead.
	stmts := dia.DirectoryWriterControlStmts()
	if len(stmts) != 3 {
		t.Fatalf("rewind core v10: %s dialect rendered %d control statements, want 3", dia.Name(), len(stmts))
	}
	exec("DROP TABLE " + directoryWriterRelation(dia, dialect.DirectoryWriterControlTable))
	exec(stmts[0])
	if _, err := db.ExecContext(ctx, dia.Rebind(
		"INSERT INTO "+directoryWriterRelation(dia, dialect.DirectoryWriterControlTable)+
			"(control_key,mode,expected_generation) VALUES (?,?,?)"),
		directoryWriterLockKey, string(state.Mode), state.ExpectedGeneration); err != nil {
		t.Fatalf("rewind core v10: restore the predecessor control tuple: %v", err)
	}
	if dia.Name() == store.EngineSQLite {
		exec("DROP TABLE " + directoryWriterRelation(dia, dialect.DirectoryWriterMarkerTable))
	}
	// SQLite: the legacy marker. PostgreSQL: v7's control ACL, re-closed on the new
	// relation exactly where v10 re-closes it on its own.
	exec(stmts[2])

	// 4. The legacy source guards v10 replaced, over the nine-table legacy inventory.
	//    A staged predecessor may lawfully be missing them, but a booted one is not:
	//    reconcileDirectoryWriterGuards creates them on every open, and the v8 lineage
	//    verifier requires exactly these definitions on its sources while v10 is
	//    untracked (verifyLineageSources).
	switch dia.Name() {
	case store.EngineSQLite:
		for _, spec := range sqliteDirectoryWriterGuardSpecsFor(
			legacyDirectoryWriterSourceTables, legacySQLiteDirectoryWriterGuardBody) {
			exec(spec.CreateStatement)
		}
	case store.EnginePostgres:
		exec(legacyPostgresDirectoryWriterFunctionDDL(t))
		for _, table := range legacyDirectoryWriterSourceTables {
			exec(postgresDirectoryWriterTriggerDDL(table))
			exec(postgresDirectoryWriterTriggerAlwaysDDL(table))
		}
	}

	// 5. The tracking row, last: while any v10 object survived, the row was the only
	//    honest record of it.
	exec(fmt.Sprintf("DELETE FROM %s WHERE version=%d",
		coreTrackingRelation(dia), coreUserAuthorityMigrationVersion))

	assertHistoricalUserAuthorityPredecessor(t, db, dia)
}

// dropLoginCapabilityForHistoricalFixture removes core v13's relation and tracking
// row together, newest-first, using the dialect table identity and
// coreLoginCapabilityMigrationVersion. Both-absent is already the reconstructed
// predecessor; either half alone is an inconsistent fixture, not a pre-v13 checkpoint.
func dropLoginCapabilityForHistoricalFixture(t *testing.T, db *sql.DB, dia dialect.Dialect) {
	t.Helper()
	if err := rewindLoginCapabilityForHistoricalFixture(context.Background(), db, dia); err != nil {
		t.Fatal(err)
	}
}

func rewindLoginCapabilityForHistoricalFixture(ctx context.Context, db *sql.DB, dia dialect.Dialect) error {
	present, tracked, err := inspectLoginCapabilityHistoricalFixtureHalves(ctx, db, dia)
	if err != nil {
		return fmt.Errorf("rewind core v%d: %w", coreLoginCapabilityMigrationVersion, err)
	}
	switch {
	case present && tracked:
		if _, err := db.ExecContext(ctx, "DROP TABLE "+directoryWriterRelation(dia, dialect.LoginCapabilityObservationTable)); err != nil {
			return fmt.Errorf("rewind core v%d: drop %s: %w",
				coreLoginCapabilityMigrationVersion, dialect.LoginCapabilityObservationTable, err)
		}
		if _, err := db.ExecContext(ctx, dia.Rebind(
			"DELETE FROM "+coreTrackingRelation(dia)+" WHERE version = ?"), coreLoginCapabilityMigrationVersion); err != nil {
			return fmt.Errorf("rewind core v%d: delete tracking row: %w",
				coreLoginCapabilityMigrationVersion, err)
		}
		return nil
	case !present && !tracked:
		return nil
	case present && !tracked:
		return fmt.Errorf("rewind core v%d: %s is present without a v%d tracking row",
			coreLoginCapabilityMigrationVersion, dialect.LoginCapabilityObservationTable, coreLoginCapabilityMigrationVersion)
	default:
		return fmt.Errorf("rewind core v%d: v%d is tracked without %s",
			coreLoginCapabilityMigrationVersion, coreLoginCapabilityMigrationVersion, dialect.LoginCapabilityObservationTable)
	}
}

func inspectLoginCapabilityHistoricalFixtureHalves(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
) (present, tracked bool, err error) {
	columns, err := dia.TableColumns(ctx, q, dialect.LoginCapabilityObservationTable)
	if err != nil {
		return false, false, fmt.Errorf("inspect %s: %w", dialect.LoginCapabilityObservationTable, err)
	}
	tracked, err = coreVersionIsTracked(ctx, q, dia, coreLoginCapabilityMigrationVersion)
	if err != nil {
		return false, false, fmt.Errorf("read the v%d tracking row: %w", coreLoginCapabilityMigrationVersion, err)
	}
	return len(columns) != 0, tracked, nil
}

func loginCapabilityHistoricalPredecessorAbsence(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
) error {
	present, tracked, err := inspectLoginCapabilityHistoricalFixtureHalves(ctx, q, dia)
	if err != nil {
		return fmt.Errorf("reconstructed predecessor: inspect v%d: %w",
			coreLoginCapabilityMigrationVersion, err)
	}
	switch {
	case !present && !tracked:
		return nil
	case present && tracked:
		return fmt.Errorf("reconstructed predecessor: %s and v%d tracking row both remain",
			dialect.LoginCapabilityObservationTable, coreLoginCapabilityMigrationVersion)
	case present:
		return fmt.Errorf("reconstructed predecessor: %s remains without a v%d tracking row",
			dialect.LoginCapabilityObservationTable, coreLoginCapabilityMigrationVersion)
	default:
		return fmt.Errorf("reconstructed predecessor: v%d is still tracked without %s",
			coreLoginCapabilityMigrationVersion, dialect.LoginCapabilityObservationTable)
	}
}

// legacyPostgresDirectoryWriterFunctionDDL renders the v9 writer guard: the same
// production wrapper the current function is created from, with the preserved legacy
// body substituted back for the v10 one.
//
// Deriving it this way rather than transcribing a CREATE FUNCTION keeps every attribute
// the verifier compares — language, volatility, parallel safety, security posture and
// the search_path proconfig — identical to the current render by construction. The
// substitution is checked: if the v10 body ever stops being a literal substring of the
// wrapper, this fails loudly instead of quietly recreating the v10 function.
func legacyPostgresDirectoryWriterFunctionDDL(t *testing.T) string {
	t.Helper()
	current := postgresDirectoryWriterFunctionDDL()
	legacy := strings.Replace(current, postgresUserAuthorityWriterGuardBody(), postgresDirectoryWriterGuardBody, 1)
	if legacy == current {
		t.Fatal("the v10 writer guard body is no longer a literal substring of the rendered writer function; derive the legacy render again")
	}
	return legacy
}

// assertHistoricalUserAuthorityPredecessor proves the reconstruction with the
// production verifiers, not with a fixture's own idea of the shape.
//
// These four checks cover the reconstructed v10-owned surface: legacy control and
// guards, absent H and untracked v10. They rely on a valid current fixture at entry;
// they do not establish arbitrary predecessor admission, tracked v9 or contiguous
// whole history. The actual migration and Open retain those separate prerequisites.
func assertHistoricalUserAuthorityPredecessor(t *testing.T, db *sql.DB, dia dialect.Dialect) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, directoryWriterTxOptions(dia))
	if err != nil {
		t.Fatalf("reconstructed v9 predecessor: begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only proof, never committed
	state, err := verifyDirectoryWriterControlShape(ctx, tx, dia)
	if err != nil {
		t.Fatalf("reconstructed v9 predecessor: writer control: %v", err)
	}
	if err := verifyLegacyDirectoryWriterGuards(ctx, tx, dia, state); err != nil {
		t.Fatalf("reconstructed v9 predecessor: legacy writer guards: %v", err)
	}
	columns, err := dia.TableColumns(ctx, tx, userAuthorityDescriptor.Table)
	if err != nil {
		t.Fatalf("reconstructed v9 predecessor: inspect %s: %v", userAuthorityDescriptor.Table, err)
	}
	if len(columns) != 0 {
		t.Fatalf("reconstructed v9 predecessor: %s still exposes %d columns",
			userAuthorityDescriptor.Table, len(columns))
	}
	tracked, err := coreVersionIsTracked(ctx, tx, dia, coreUserAuthorityMigrationVersion)
	if err != nil {
		t.Fatalf("reconstructed v9 predecessor: read the v%d tracking row: %v",
			coreUserAuthorityMigrationVersion, err)
	}
	if tracked {
		t.Fatalf("reconstructed v9 predecessor: v%d is still tracked", coreUserAuthorityMigrationVersion)
	}
	if err := loginCapabilityHistoricalPredecessorAbsence(ctx, tx, dia); err != nil {
		t.Fatalf("reconstructed v9 predecessor: %v", err)
	}
}

// assertDirectV7FixtureHistory makes the whole-history precondition explicit even
// when the test invokes v7 directly instead of reaching it through Open.
func assertDirectV7FixtureHistory(t *testing.T, db *sql.DB, dia dialect.Dialect) {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, "SELECT version FROM "+coreTrackingRelation(dia)+" ORDER BY version")
	if err != nil {
		t.Fatalf("direct v7 fixture history: %v", err)
	}
	defer rows.Close()
	next := 1
	for rows.Next() {
		var got int
		if err := rows.Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != next || got > 6 {
			t.Fatalf("direct v7 fixture history: got version %d at position %d, want [1..6]", got, next)
		}
		next++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if next != 7 {
		t.Fatalf("direct v7 fixture history: got %d rows, want [1..6]", next-1)
	}
	if err := preflightCoreMigrationVersion(ctx, db, dia, coreSupportedMigrationVersion); err != nil {
		t.Fatalf("direct v7 fixture rejected by ordinary core preflight: %v", err)
	}
}
