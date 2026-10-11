// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// Verification never repairs custody objects. PostgreSQL reference DDL is confined
// to pg_temp in an always-rolled-back savepoint; it measures this server's canonical
// representation of the compiled constructor. An unavailable calibration refuses.
func verifyFinOpsCustodyControlExact(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) error {
	if dia.Name() == store.EngineSQLite {
		return verifySQLiteFinOpsCustodyControlExact(ctx, tx, dia)
	}
	want, err := calibrateFinOpsCustodyControl(ctx, tx, dia)
	if err != nil {
		return fmt.Errorf("sqlstore: custody reference calibration: %w", err)
	}
	got, err := readFinOpsCustodyPostgresShape(ctx, tx, dialect.EngineSchema)
	if err != nil {
		return fmt.Errorf("sqlstore: custody catalog: %w", err)
	}
	for i := range want {
		if want[i] != got[i] {
			return fmt.Errorf("sqlstore: custody exact shape differs in catalog projection %d", i)
		}
	}
	return nil
}

func verifySQLiteFinOpsCustodyControlExact(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) error {
	want := map[string]string{}
	for _, statement := range dia.FinOpsCustodyControlStmts() {
		fields := strings.Fields(statement)
		var kind, name string
		switch {
		case strings.HasPrefix(statement, "CREATE TABLE "):
			kind, name = "table", fields[2]
		case strings.HasPrefix(statement, "CREATE UNIQUE INDEX "):
			kind, name = "index", fields[3]
		case strings.HasPrefix(statement, "CREATE TRIGGER "):
			kind, name = "trigger", fields[2]
		default:
			return fmt.Errorf("sqlstore: custody SQLite constructor has an unrecognized statement")
		}
		want[kind+":"+name] = statement
	}
	// Table SQL retains the complete nullability, CHECK, PK and UNIQUE definitions.
	// Automatic indexes have no SQL text; their count follows those exact definitions.
	for table, count := range map[string]int{dialect.ControlCustodyEnrollmentTable: 1, dialect.ControlCustodyProofTable: 3, dialect.ControlCustodyTransitionTable: 1} {
		var automatic int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM main.sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql IS NULL", table).Scan(&automatic); err != nil || automatic != count {
			return fmt.Errorf("sqlstore: custody SQLite automatic indexes for %s: count=%d expected=%d: %w", table, automatic, count, err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT schema_name, type, name, sql FROM (
  SELECT 'main' AS schema_name, type, name, sql FROM main.sqlite_master
    WHERE tbl_name IN (?, ?, ?) AND sql IS NOT NULL
  UNION ALL
  SELECT 'temp' AS schema_name, type, name, sql FROM temp.sqlite_master
    WHERE tbl_name IN (?, ?, ?) AND sql IS NOT NULL
) ORDER BY schema_name, type, name`, dialect.ControlCustodyEnrollmentTable, dialect.ControlCustodyProofTable, dialect.ControlCustodyTransitionTable,
		dialect.ControlCustodyEnrollmentTable, dialect.ControlCustodyProofTable, dialect.ControlCustodyTransitionTable)
	if err != nil {
		return fmt.Errorf("sqlstore: custody SQLite catalog: %w", err)
	}
	for rows.Next() {
		var schema, kind, name, definition string
		if err := rows.Scan(&schema, &kind, &name, &definition); err != nil {
			_ = rows.Close()
			return err
		}
		key := kind + ":" + name
		if schema != "main" || definition != want[key] {
			_ = rows.Close()
			return fmt.Errorf("sqlstore: custody SQLite %s %s.%s is absent, extra or noncanonical", kind, schema, name)
		}
		delete(want, key)
	}
	if err := errorsJoinRows(rows); err != nil {
		return err
	}
	if len(want) != 0 {
		return fmt.Errorf("sqlstore: custody SQLite is missing %d canonical objects", len(want))
	}
	return nil
}

func finOpsCustodyCalibrationCapability(temporary, readOnly, recovery bool) error {
	if readOnly || recovery {
		return fmt.Errorf("custody calibration unavailable on a read-only or hot-standby server")
	}
	if !temporary {
		return fmt.Errorf("custody calibration requires database TEMP privilege")
	}
	return nil
}

func calibrateFinOpsCustodyControl(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) (out []string, err error) {
	var temporary, readOnly, recovery bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_catalog.has_database_privilege(pg_catalog.current_database(), 'TEMP'),
  pg_catalog.current_setting('transaction_read_only')::pg_catalog.bool, pg_catalog.pg_is_in_recovery()`).Scan(&temporary, &readOnly, &recovery); err != nil {
		return nil, err
	}
	if err := finOpsCustodyCalibrationCapability(temporary, readOnly, recovery); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT finops_custody_calibration"); err != nil {
		return nil, err
	}
	defer func() {
		// Cleanup must run even after a canceled statement, and cleanup failures
		// remain refusals even when a shape was already measured successfully.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, rollbackErr := tx.ExecContext(cleanupCtx, "ROLLBACK TO SAVEPOINT finops_custody_calibration")
		_, releaseErr := tx.ExecContext(cleanupCtx, "RELEASE SAVEPOINT finops_custody_calibration")
		err = errors.Join(err, rollbackErr, releaseErr)
	}()
	for _, original := range dia.FinOpsCustodyControlStmts() {
		statement, err := finOpsCustodyReferenceStatement(original)
		if err != nil {
			return nil, err
		}
		if statement == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return nil, err
		}
	}
	var temporarySchema string
	if err := tx.QueryRowContext(ctx, `SELECT nspname FROM pg_catalog.pg_namespace WHERE oid = pg_catalog.pg_my_temp_schema()`).Scan(&temporarySchema); err != nil {
		return nil, err
	}
	out, err = readFinOpsCustodyPostgresShape(ctx, tx, temporarySchema)
	for i := range out {
		out[i] = strings.ReplaceAll(out[i], temporarySchema+".", dialect.EngineSchema+".")
		// PostgreSQL deparses temporary relation/function identities through the
		// pg_temp alias even though their catalog namespace has a session suffix.
		out[i] = strings.ReplaceAll(out[i], "pg_temp.", dialect.EngineSchema+".")
		out[i] = strings.ReplaceAll(out[i], `"function_schema":"`+temporarySchema+`"`, `"function_schema":"`+dialect.EngineSchema+`"`)
	}
	return out, err
}

// Only known constructor forms can become references. Qualify every relation and
// the function declaration/attachment explicitly; the canonical function BODY
// stays unchanged and is never invoked by this calibration.
func finOpsCustodyReferenceStatement(original string) (string, error) {
	switch {
	case strings.HasPrefix(original, "CREATE TABLE "):
		return strings.Replace(original, "CREATE TABLE ", "CREATE TEMP TABLE pg_temp.", 1), nil
	case strings.HasPrefix(original, "CREATE UNIQUE INDEX "), strings.HasPrefix(original, "CREATE TRIGGER "), strings.HasPrefix(original, "ALTER TABLE ONLY "):
		statement := original
		qualified := false
		for _, table := range dialect.FinOpsCustodyControlTables() {
			before := statement
			statement = strings.Replace(statement, " ON "+table+" ", " ON pg_temp."+table+" ", 1)
			statement = strings.Replace(statement, "ALTER TABLE ONLY "+table+" ", "ALTER TABLE ONLY pg_temp."+table+" ", 1)
			qualified = qualified || before != statement
		}
		if !qualified {
			return "", fmt.Errorf("custody reference statement does not target a closed pg_temp relation")
		}
		statement = strings.Replace(statement, "EXECUTE FUNCTION "+dialect.EngineSchema+".", "EXECUTE FUNCTION pg_temp.", 1)
		return statement, nil
	case strings.HasPrefix(original, "CREATE FUNCTION "+dialect.EngineSchema+"."):
		return strings.Replace(original, "CREATE FUNCTION "+dialect.EngineSchema+".", "CREATE FUNCTION pg_temp.", 1), nil
	case strings.HasPrefix(original, "REVOKE ALL ON TABLE "):
		return "", nil // reference objects do not attest deployment ACLs
	default:
		return "", fmt.Errorf("custody reference constructor has an unrecognized statement")
	}
}

func readFinOpsCustodyPostgresShape(ctx context.Context, tx *sql.Tx, schema string) ([]string, error) {
	var out []string
	for _, table := range dialect.FinOpsCustodyControlTables() {
		var oid int64
		var kind, persistence, options string
		var partition, rls, forceRLS, rules, inherits bool
		if err := tx.QueryRowContext(ctx, `SELECT c.oid::pg_catalog.int8, c.relkind::text, c.relpersistence::text,
 c.relispartition, c.relrowsecurity, c.relforcerowsecurity, COALESCE(c.reloptions::text, ''),
 EXISTS (SELECT 1 FROM pg_catalog.pg_rewrite w WHERE w.ev_class = c.oid),
 EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid = c.oid OR i.inhparent = c.oid)
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, schema, table).Scan(&oid, &kind, &persistence, &partition, &rls, &forceRLS, &options, &rules, &inherits); err != nil {
			return nil, fmt.Errorf("relation %s: %w", table, err)
		}
		wantPersistence := "p"
		if schema != dialect.EngineSchema {
			wantPersistence = "t"
		}
		if kind != "r" || persistence != wantPersistence || partition || rls || forceRLS || options != "" || rules || inherits {
			return nil, fmt.Errorf("custody relation %s has noncanonical storage, RLS, rules or inheritance", table)
		}
		for _, query := range []string{
			`SELECT COALESCE(json_agg(x ORDER BY x.attnum)::text, '[]') FROM (
 SELECT a.attnum, a.attname, pg_catalog.format_type(a.atttypid, a.atttypmod) AS typ,
 a.attnotnull, a.attisdropped, a.attidentity, a.attgenerated, COALESCE(pg_catalog.pg_get_expr(d.adbin, d.adrelid), '') AS default_expr,
 COALESCE(n.nspname, '') AS collation_schema, COALESCE(co.collname, '') AS collation_name
 FROM pg_catalog.pg_attribute a LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
 LEFT JOIN pg_catalog.pg_collation co ON co.oid = a.attcollation
 LEFT JOIN pg_catalog.pg_namespace n ON n.oid = co.collnamespace
 WHERE a.attrelid = $1::pg_catalog.oid AND a.attnum > 0) x`,
			`SELECT COALESCE(json_agg(x ORDER BY x.key)::text, '[]') FROM (
 SELECT CASE WHEN c.contype = 'n' THEN 'NOT NULL:' ||
 (SELECT pg_catalog.string_agg(a.attname::text, ',' ORDER BY k.ord) FROM pg_catalog.unnest(c.conkey) WITH ORDINALITY k(attnum, ord)
 JOIN pg_catalog.pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum) ELSE c.conname::text END AS key,
 c.contype, c.convalidated, c.condeferrable, c.condeferred, c.connoinherit, pg_catalog.pg_get_constraintdef(c.oid) AS definition
 FROM pg_catalog.pg_constraint c WHERE c.conrelid = $1::pg_catalog.oid) x`,
			`SELECT COALESCE(json_agg(x ORDER BY x.name)::text, '[]') FROM (
 SELECT c.relname AS name, i.indisvalid, i.indisready, i.indislive, i.indisprimary, i.indisunique,
 (to_jsonb(i)->'indnullsnotdistinct') AS nulls_not_distinct, pg_catalog.pg_get_indexdef(i.indexrelid) AS definition
 FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid WHERE i.indrelid = $1::pg_catalog.oid) x`,
			`SELECT COALESCE(json_agg(x ORDER BY x.name)::text, '[]') FROM (
 SELECT t.tgname AS name, t.tgenabled, t.tgisinternal, pn.nspname AS function_schema, p.proname AS function_name,
 pg_catalog.regexp_replace(pg_catalog.pg_get_triggerdef(t.oid), 'EXECUTE FUNCTION [^(]+[(][)]$', 'EXECUTE FUNCTION canonical_guard()') AS definition
 FROM pg_catalog.pg_trigger t JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
 JOIN pg_catalog.pg_namespace pn ON pn.oid = p.pronamespace WHERE t.tgrelid = $1::pg_catalog.oid) x`,
		} {
			var projection string
			if err := tx.QueryRowContext(ctx, query, oid).Scan(&projection); err != nil {
				return nil, err
			}
			out = append(out, projection)
		}
	}
	_, exists, err := projectGuardFunction(ctx, tx, schema, dialect.PostgresCustodyGuardFunction)
	if err != nil || !exists {
		return nil, fmt.Errorf("custody guard function missing or ambiguous: %w", err)
	}
	var definition string
	if err := tx.QueryRowContext(ctx, `SELECT pg_catalog.pg_get_functiondef(p.oid)
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2`, schema, dialect.PostgresCustodyGuardFunction).Scan(&definition); err != nil {
		return nil, err
	}
	return append(out, definition), nil
}

func verifyFinOpsCustodyControlPerBoot(ctx context.Context, mdb dialect.Execer, dia dialect.Dialect, roles guardRoles) error {
	tx, err := mdb.BeginTx(ctx, directoryWriterTxOptions(dia))
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // never commits reference or deployment changes
	var name, appliedAt, phase string
	var reverted sql.NullString
	if err := tx.QueryRowContext(ctx, dia.Rebind("SELECT name, applied_at, phase, reverted_at FROM "+coreTrackingRelation(dia)+" WHERE version = ?"), coreFinOpsCustodyControlMigrationVersion).Scan(&name, &appliedAt, &phase, &reverted); err != nil {
		return fmt.Errorf("sqlstore: custody tracking row: %w", err)
	}
	if name != coreFinOpsCustodyControlMigrationName || strings.TrimSpace(appliedAt) == "" || phase != "expand" || reverted.Valid {
		return fmt.Errorf("sqlstore: custody tracking row is not the exact active %q expand record", coreFinOpsCustodyControlMigrationName)
	}
	if err := verifyFinOpsCustodyControlMigrationAfter(ctx, tx, dia, roles); err != nil {
		return err
	}
	return nil
}

func verifyFinOpsCustodyControlMigrationAfter(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, roles guardRoles) error {
	if err := verifyFinOpsCustodyControlExact(ctx, tx, dia); err != nil {
		return err
	}
	return verifyFinOpsCustodyControlOwnerACL(ctx, tx, dia, roles)
}

func warnFinOpsCustodySingleRole(dia dialect.Dialect, roles guardRoles) {
	if dia.Name() == store.EnginePostgres && guardMetadataTopologyOf(roles) == guardTopologySingleRole {
		slog.Warn("store: custody control uses one owner/application role; it has no SELECT-only application boundary")
	}
}
