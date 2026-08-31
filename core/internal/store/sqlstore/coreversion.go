// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// ErrCoreSchemaVersionAhead is returned before any boot DDL when the database
// records a core migration newer than this binary understands. An older binary
// cannot safely infer what the newer migration changed, including when that
// migration was later marked reverted: migration history is forward-only and a
// reverted row remains proof that the database crossed that version.
var ErrCoreSchemaVersionAhead = errors.New("sqlstore: the database records a core schema version newer than this binary supports")

// coreSupportedMigrationVersion is the single boot authority for the newest
// core migration this binary understands. Individual migration constants stay
// immutable when a later migration is appended; only this alias advances.
const coreSupportedMigrationVersion = coreDirectoryMigrationVersion

// preflightCoreMigrationVersion is the read-only, pre-DDL half of core migration
// compatibility. It must remain the first operation in Open's migration-lock
// callback: migrate.Apply cannot enforce this boundary because its first action is
// ensureTracking, which creates or alters the tracking relation.
//
// A missing tracking relation is a fresh database and is therefore accepted without
// creating it. An existing relation must expose the minimum columns every historical
// core tracker had before it can be read. phase and reverted_at are deliberately not
// required: migrate.Apply additively introduced those columns and may reconcile them
// only after this read-only check succeeds.
func preflightCoreMigrationVersion(
	ctx context.Context,
	mdb dialect.Execer,
	dia dialect.Dialect,
	maxSupported int,
) error {
	if maxSupported <= 0 {
		return fmt.Errorf("sqlstore: core migration version preflight: invalid supported version %d", maxSupported)
	}

	exists, err := coreTrackingRelationExists(ctx, mdb, dia)
	if err != nil {
		return fmt.Errorf("sqlstore: core migration version preflight: inspect %s existence: %w",
			coreTrackingTable, err)
	}
	if !exists {
		return nil
	}
	cols, err := dia.TableColumns(ctx, mdb, coreTrackingTable)
	if err != nil {
		return fmt.Errorf("sqlstore: core migration version preflight: inspect %s: %w", coreTrackingTable, err)
	}
	if len(cols) == 0 {
		return fmt.Errorf(
			"sqlstore: core migration version preflight: %s exists but exposes no ordinary columns",
			coreTrackingTable)
	}

	hasCurrentColumns, err := verifyCoreTrackingRelationShape(ctx, mdb, dia, cols)
	if err != nil {
		return err
	}

	// Read every version as text and parse it canonically. MAX(version) is not a
	// safe compatibility witness until the column's affinity and primary key have
	// been proved: on a malformed SQLite TEXT tracker MAX('7','100') is '7'. A
	// complete read also makes duplicate/non-positive history an explicit refusal.
	versions, err := readCanonicalCoreTrackingVersions(ctx, mdb, dia)
	if err != nil {
		return err
	}
	var maxVersion int64
	for version := range versions {
		if version > maxVersion {
			maxVersion = version
		}
	}
	if maxVersion > int64(maxSupported) {
		return fmt.Errorf("%w: database=%d binary=%d",
			ErrCoreSchemaVersionAhead, maxVersion, maxSupported)
	}
	if maxVersion < int64(coreDirectoryMigrationVersion) {
		return nil
	}
	// v7 is forward-only and is the first migration whose tracking row gates a
	// separately healed control plane. If its row exists, accepting a renamed,
	// contracted or allegedly reverted record would let migrate.Apply skip the
	// Exec while attributing that skip to evidence this binary never wrote.
	if !hasCurrentColumns {
		return fmt.Errorf(
			"sqlstore: core migration version preflight: v%d is recorded but %s has the legacy three-column shape",
			coreDirectoryMigrationVersion, coreTrackingTable)
	}
	var (
		name       string
		appliedAt  string
		phase      string
		revertedAt sql.NullString
	)
	trackingRelation := coreTrackingRelation(dia)
	query := dia.Rebind("SELECT name, applied_at, phase, reverted_at FROM " + trackingRelation + " WHERE version = ?") // #nosec G202 -- internal constants, never user input
	if err := mdb.QueryRowContext(ctx, query, coreDirectoryMigrationVersion).
		Scan(&name, &appliedAt, &phase, &revertedAt); err != nil {
		return fmt.Errorf("sqlstore: core migration version preflight: read v%d tracking row: %w",
			coreDirectoryMigrationVersion, err)
	}
	if name != coreDirectoryMigrationName || strings.TrimSpace(appliedAt) == "" ||
		phase != "expand" || revertedAt.Valid {
		return fmt.Errorf(
			"sqlstore: core migration version preflight: v%d tracking row is not the exact active %q expand record",
			coreDirectoryMigrationVersion, coreDirectoryMigrationName)
	}
	return nil
}

type coreTrackingColumnShape struct {
	name         string
	typeName     string
	defaultValue string
	notNull      bool
	primaryKey   bool
}

// verifyCoreTrackingRelationShape accepts exactly the historical three-column
// tracker and the current five-column tracker. It rejects affinity/type drift,
// a lost primary key and extra/partial columns before any value is trusted.
func verifyCoreTrackingRelationShape(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
	columns map[string]bool,
) (bool, error) {
	shapes, err := inspectCoreTrackingColumns(ctx, q, dia)
	if err != nil {
		return false, fmt.Errorf("sqlstore: core migration version preflight: inspect %s shape: %w",
			coreTrackingTable, err)
	}

	want := []coreTrackingColumnShape{
		{name: "version", typeName: "integer", notNull: true, primaryKey: true},
		{name: "name", typeName: "text", notNull: true},
		{name: "applied_at", typeName: "text", notNull: true},
	}
	hasPhase := columns["phase"]
	hasReverted := columns["reverted_at"]
	if hasPhase != hasReverted {
		return false, fmt.Errorf(
			"sqlstore: core migration version preflight: %s is not a legible migration tracker: phase/reverted_at are partial",
			coreTrackingTable)
	}
	if hasPhase {
		phaseDefault := "'expand'"
		if dia.Name() == store.EnginePostgres {
			phaseDefault = "'expand'::text"
		}
		want = append(want,
			coreTrackingColumnShape{
				name: "phase", typeName: "text", defaultValue: phaseDefault, notNull: true,
			},
			coreTrackingColumnShape{name: "reverted_at", typeName: "text"},
		)
	}
	if len(shapes) != len(want) {
		return false, fmt.Errorf(
			"sqlstore: core migration version preflight: %s is not a legible migration tracker: columns=%d want=%d",
			coreTrackingTable, len(shapes), len(want))
	}
	for i := range want {
		if shapes[i] != want[i] {
			return false, fmt.Errorf(
				"sqlstore: core migration version preflight: %s column %d is %+v, want %+v",
				coreTrackingTable, i+1, shapes[i], want[i])
		}
	}
	return hasPhase, nil
}

func inspectCoreTrackingColumns(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
) ([]coreTrackingColumnShape, error) {
	var (
		rows *sql.Rows
		err  error
	)
	switch dia.Name() {
	case store.EngineSQLite:
		// table_xinfo, not table_info: generated/hidden columns are still part of
		// the durable relation shape and must not disappear from the exact census.
		rows, err = q.QueryContext(ctx, "PRAGMA main.table_xinfo("+coreTrackingTable+")") // #nosec G202 -- internal constant
		if err != nil {
			return nil, err
		}
		defer rows.Close() //nolint:errcheck // joined through rows.Err below
		var out []coreTrackingColumnShape
		for rows.Next() {
			var (
				cid       int
				name      string
				typeName  string
				notNull   int
				defaultV  sql.NullString
				primaryID int
				hidden    int
			)
			if err := rows.Scan(&cid, &name, &typeName, &notNull, &defaultV, &primaryID, &hidden); err != nil {
				return nil, err
			}
			_ = cid
			if hidden != 0 {
				return nil, fmt.Errorf("column %q has hidden/generated rank %d", name, hidden)
			}
			out = append(out, coreTrackingColumnShape{
				name: name, typeName: strings.ToLower(strings.TrimSpace(typeName)),
				defaultValue: func() string {
					if defaultV.Valid {
						return strings.TrimSpace(defaultV.String)
					}
					return ""
				}(),
				// SQLite reports INTEGER PRIMARY KEY as notnull=0 even though the
				// primary-key constraint makes NULL impossible.
				notNull: notNull != 0 || primaryID != 0, primaryKey: primaryID != 0,
			})
		}
		return out, rows.Err()
	case store.EnginePostgres:
		rows, err = q.QueryContext(ctx, `SELECT c.column_name, c.data_type, c.is_nullable,
       c.column_default,
       EXISTS (
         SELECT 1
         FROM information_schema.table_constraints tc
         JOIN information_schema.key_column_usage kcu
           ON kcu.constraint_catalog = tc.constraint_catalog
          AND kcu.constraint_schema = tc.constraint_schema
          AND kcu.constraint_name = tc.constraint_name
         WHERE tc.table_schema = c.table_schema
           AND tc.table_name = c.table_name
           AND tc.constraint_type = 'PRIMARY KEY'
           AND kcu.column_name = c.column_name
       )
FROM information_schema.columns c
WHERE c.table_schema = 'public' AND c.table_name = $1
ORDER BY c.ordinal_position`, coreTrackingTable)
		if err != nil {
			return nil, err
		}
		defer rows.Close() //nolint:errcheck // joined through rows.Err below
		var out []coreTrackingColumnShape
		for rows.Next() {
			var name, typeName, nullable string
			var defaultV sql.NullString
			var primary bool
			if err := rows.Scan(&name, &typeName, &nullable, &defaultV, &primary); err != nil {
				return nil, err
			}
			out = append(out, coreTrackingColumnShape{
				name: name, typeName: strings.ToLower(typeName),
				defaultValue: func() string {
					if defaultV.Valid {
						return strings.TrimSpace(defaultV.String)
					}
					return ""
				}(),
				notNull: nullable == "NO", primaryKey: primary,
			})
		}
		return out, rows.Err()
	default:
		return nil, fmt.Errorf("unsupported engine %q", dia.Name())
	}
}

func readCanonicalCoreTrackingVersions(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
) (map[int64]struct{}, error) {
	relation := coreTrackingRelation(dia)
	rows, err := q.QueryContext(ctx, "SELECT CAST(version AS TEXT) FROM "+relation) // #nosec G202 -- internal constants
	if err != nil {
		return nil, fmt.Errorf("sqlstore: core migration version preflight: read versions: %w", err)
	}
	defer rows.Close() //nolint:errcheck // joined through rows.Err below
	versions := make(map[int64]struct{})
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		version, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || version <= 0 || strconv.FormatInt(version, 10) != raw {
			return nil, fmt.Errorf(
				"sqlstore: core migration version preflight: non-canonical version %q", raw)
		}
		if _, duplicate := versions[version]; duplicate {
			return nil, fmt.Errorf(
				"sqlstore: core migration version preflight: duplicate version %d", version)
		}
		versions[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return versions, nil
}

func coreTrackingRelation(dia dialect.Dialect) string {
	if dia.Name() == store.EnginePostgres {
		return dialect.EngineSchema + "." + coreTrackingTable
	}
	return "main." + coreTrackingTable
}

func coreTrackingRelationExists(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
) (bool, error) {
	switch dia.Name() {
	case store.EngineSQLite:
		rows, err := q.QueryContext(ctx, `SELECT type,
       EXISTS (
         SELECT 1 FROM main.sqlite_master t
         WHERE t.type = 'trigger' AND t.tbl_name = ?
       )
FROM main.sqlite_master
WHERE name = ?
ORDER BY type`, coreTrackingTable, coreTrackingTable)
		if err != nil {
			return false, err
		}
		defer rows.Close() //nolint:errcheck // joined through rows.Err below
		var count int
		for rows.Next() {
			count++
			if count > 1 {
				return false, fmt.Errorf("multiple exact relations found")
			}
			var kind string
			var hasTrigger bool
			if err := rows.Scan(&kind, &hasTrigger); err != nil {
				return false, err
			}
			if kind != "table" {
				return false, fmt.Errorf("relation kind is %q, want ordinary table", kind)
			}
			if hasTrigger {
				return false, fmt.Errorf("tracking relation has a trigger")
			}
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
		return count == 1, nil
	case store.EnginePostgres:
		rows, err := q.QueryContext(ctx, `SELECT c.relkind::text,
       c.relpersistence::text,
       c.relispartition,
       c.relrowsecurity,
       c.relforcerowsecurity,
       EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid = c.oid),
       EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhparent = c.oid),
       EXISTS (SELECT 1 FROM pg_catalog.pg_policy p WHERE p.polrelid = c.oid),
       EXISTS (SELECT 1 FROM pg_catalog.pg_rewrite r WHERE r.ev_class = c.oid),
       EXISTS (
         SELECT 1 FROM pg_catalog.pg_trigger t
         WHERE t.tgrelid = c.oid AND NOT t.tgisinternal
       )
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, dialect.EngineSchema, coreTrackingTable)
		if err != nil {
			return false, err
		}
		defer rows.Close() //nolint:errcheck // joined through rows.Err below
		if !rows.Next() {
			return false, rows.Err()
		}
		var (
			kind, persistence                          string
			partition, rowSecurity, forceRowSecurity   bool
			hasParent, hasChild, hasPolicy, hasRewrite bool
			hasTrigger                                 bool
		)
		if err := rows.Scan(
			&kind, &persistence, &partition, &rowSecurity, &forceRowSecurity,
			&hasParent, &hasChild, &hasPolicy, &hasRewrite, &hasTrigger,
		); err != nil {
			return false, err
		}
		if rows.Next() {
			return false, fmt.Errorf("multiple exact relations found")
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
		if kind != "r" || persistence != "p" || partition || rowSecurity ||
			forceRowSecurity || hasParent || hasChild || hasPolicy || hasRewrite || hasTrigger {
			return false, fmt.Errorf(
				"relation is not an exact permanent standalone table: "+
					"kind=%q persistence=%q partition=%t row_security=%t force_row_security=%t "+
					"parent=%t child=%t policy=%t rewrite=%t trigger=%t",
				kind, persistence, partition, rowSecurity, forceRowSecurity,
				hasParent, hasChild, hasPolicy, hasRewrite, hasTrigger)
		}
		return true, nil
	default:
		return false, fmt.Errorf("unsupported engine %q", dia.Name())
	}
}
