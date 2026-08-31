// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// migstatus.go backs `olivares migrate status`. It is the READ-ONLY twin of the
// boot-time migrate.Apply: it opens a transient connection, enumerates the version-
// tracking tables (schema_migrations*) and reads their rows, so an operator can see
// which migrations a database carries — and in which expand-contract phase — without
// a SQL client and without booting the engine. It never writes, migrates or reverts.

// trackingTableRE guards a catalog-sourced table name before it is interpolated into
// a SELECT: only the engine's own version-tracking tables match. The name-based
// module entity registry ("applied_module_tables") is deliberately NOT a
// schema_migrations* table and is not reported here.
var trackingTableRE = regexp.MustCompile(`^schema_migrations[a-z0-9_]*$`)

// MigrationStatus opens a transient connection for cfg and returns every recorded
// schema-migration row across the core and per-module tracking tables, ordered by
// table then version. A connection failure is returned as an error (there is no
// per-DSN posture to report here, unlike the db-check probe). It applies nothing.
func MigrationStatus(ctx context.Context, cfg store.Config) ([]store.MigrationRecord, error) {
	dia, ok := dialect.New(cfg.Engine)
	if !ok {
		return nil, fmt.Errorf("sqlstore: unsupported engine %q", cfg.Engine)
	}
	db, err := openForRead(cfg)
	if err != nil {
		return nil, err
	}
	defer db.Close() //nolint:errcheck // transient read-only pool

	tables, err := listMigrationTrackingTables(ctx, db, dia)
	if err != nil {
		return nil, err
	}
	var out []store.MigrationRecord
	for _, t := range tables {
		if !trackingTableRE.MatchString(t) {
			continue // defense-in-depth: never interpolate an unexpected catalog name
		}
		recs, err := readTrackingTable(ctx, db, dia, t)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: read %s: %w", t, err)
		}
		out = append(out, recs...)
	}
	return out, nil
}

// openForRead opens a transient connection used strictly to READ. For SQLite it
// enforces read-only at the connection level (`PRAGMA query_only = ON`) and — unlike
// the engine's openSQLite — does NOT run `PRAGMA journal_mode = WAL`, so a status query
// never writes to the database file or its journal, even against a database last left in
// a non-WAL mode by another tool. `busy_timeout` lets it coexist with a live writer
// (WAL allows concurrent readers). For Postgres the status queries are SELECT-only, so
// the standard pool is used.
func openForRead(cfg store.Config) (*sql.DB, error) {
	if cfg.Engine != store.EngineSQLite {
		return openDB(cfg)
	}
	dsn := cfg.DSN
	if dsn == "" {
		dsn = ":memory:"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA query_only = ON", "PRAGMA busy_timeout = 5000"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("sqlstore: read-only open %q: %w", pragma, err)
		}
	}
	return db, nil
}

// listMigrationTrackingTables returns the names of the schema_migrations* tracking
// tables present, from the engine's own catalog (sqlite_master / pg_tables).
func listMigrationTrackingTables(ctx context.Context, db *sql.DB, dia dialect.Dialect) ([]string, error) {
	var q string
	switch dia.Name() {
	case store.EngineSQLite:
		q = "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'schema_migrations%' ORDER BY name"
	case store.EnginePostgres:
		q = "SELECT tablename FROM pg_tables WHERE schemaname = pg_catalog.current_schema() AND tablename LIKE 'schema_migrations%' ORDER BY tablename"
	default:
		return nil, fmt.Errorf("sqlstore: unsupported engine %q", dia.Name())
	}
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(tables)
	return tables, nil
}

// readTrackingTable reads one tracking table's rows. version/name/applied_at always
// exist; phase/reverted_at were added by and may be absent on a table created by
// an older engine that has not yet booted this binary (boot reconciles them
// additively), so they are selected only when present.
func readTrackingTable(ctx context.Context, db *sql.DB, dia dialect.Dialect, table string) ([]store.MigrationRecord, error) {
	cols, err := dia.TableColumns(ctx, db, table)
	if err != nil {
		return nil, err
	}
	hasPhase := cols["phase"]
	hasReverted := cols["reverted_at"]
	sel := "version, name, applied_at"
	if hasPhase {
		sel += ", phase"
	}
	if hasReverted {
		sel += ", reverted_at"
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s ORDER BY version", sel, table)) //nolint:gosec // table is catalog-sourced and matched against trackingTableRE
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.MigrationRecord
	for rows.Next() {
		rec := store.MigrationRecord{Table: table}
		var phase, revertedAt sql.NullString
		dest := []any{&rec.Version, &rec.Name, &rec.AppliedAt}
		if hasPhase {
			dest = append(dest, &phase)
		}
		if hasReverted {
			dest = append(dest, &revertedAt)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		rec.Phase = phase.String
		rec.Reverted = revertedAt.Valid && revertedAt.String != ""
		out = append(out, rec)
	}
	return out, rows.Err()
}
