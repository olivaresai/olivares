// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	coreDirectoryMigrationVersion = 7
	coreDirectoryMigrationName    = "directory_fence"
)

var coreDirectoryRelationNames = [...]string{
	"core_directory_epoch",
	"core_directory_tombstone",
	"core_user_tombstone",
}

// coreDirectoryInitialDisposition is deliberately bool-backed: absent and
// present are its only representable values. It records what v7 observed before
// issuing any target DDL, so the continuation can correlate physical state with
// guard history instead of inferring an upgrade after the relations were made
// indistinguishable from a fresh install.
type coreDirectoryInitialDisposition bool

const (
	coreDirectoryInitiallyAbsent  coreDirectoryInitialDisposition = false
	coreDirectoryInitiallyPresent coreDirectoryInitialDisposition = true
)

func (d coreDirectoryInitialDisposition) String() string {
	if d == coreDirectoryInitiallyPresent {
		return "present"
	}
	return "absent"
}

// inspectCoreDirectoryInitialDisposition is shared by the two adjacent
// migrations that must agree about one physical fact. v6 records a direct-v7
// start seal only when all three relations are absent; v7 receives the same
// closed disposition before it creates anything. A partial set is not a crash
// state either migration can commit and is therefore refused, not completed.
func inspectCoreDirectoryInitialDisposition(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) (coreDirectoryInitialDisposition, error) {
	var have, missing []string
	for _, table := range coreDirectoryRelationNames {
		_, exists, err := inspectCoreDirectoryRelation(ctx, tx, dia, table)
		if err != nil {
			return coreDirectoryInitiallyAbsent,
				fmt.Errorf("inspect core directory relation %q: %w", table, err)
		}
		if exists {
			have = append(have, table)
		} else {
			missing = append(missing, table)
		}
	}
	switch len(have) {
	case 0:
		return coreDirectoryInitiallyAbsent, nil
	case len(coreDirectoryRelationNames):
		return coreDirectoryInitiallyPresent, nil
	default:
		return coreDirectoryInitiallyAbsent, fmt.Errorf(
			"partial core directory relation set before v6 (present=%v, absent=%v)", have, missing)
	}
}

// directoryMigrationAfter is the seam between the v7 relation expand and the
// rest of the directory-fence ceremony. The callback runs only after all three
// relations have their complete descriptor-rendered contract, on the SAME
// *sql.Tx that migrate.Apply will use for the v7 tracking row, and receives the
// relations' initial disposition rather than their now-converged state.
type directoryMigrationAfter func(
	context.Context,
	*sql.Tx,
	coreDirectoryInitialDisposition,
) error

// coreDirectoryMigration returns core v7 as an Exec-only migration. It cannot
// be expressed as Stmts: a fresh database has already created the three current
// descriptor tables in v2, while an upgraded database has v2 recorded and has
// none of them. The Exec must inspect that distinction inside the migration
// transaction, verify the fresh case, and create the upgrade case from the same
// descriptor renderer v2 uses.
func coreDirectoryMigration(
	dia dialect.Dialect,
	descs []model.EntityDescriptor,
	after directoryMigrationAfter,
) migrate.Migration {
	return migrate.Migration{
		Version: coreDirectoryMigrationVersion,
		Name:    coreDirectoryMigrationName,
		Exec: func(ctx context.Context, tx *sql.Tx) error {
			return ensureCoreDirectoryRelations(ctx, tx, dia, descs, after)
		},
	}
}

// ensureCoreDirectoryRelations accepts exactly two initial states:
//
//   - all three relations are absent (an in-place upgrade), so it creates all
//     three with dialect.CreateTableStmts and verifies what it created; or
//   - all three are present with the exact table, CHECK, index, isolation and
//     append-only shape emitted by their descriptors (a fresh v2 database), so
//     relation creation is a no-op.
//
// A subset is not an interrupted migration: v7 is transactional on both
// engines, so no execution authored by this binary can commit one. Treating a
// subset as resumable would instead launder out-of-band or malformed state.
// Inspection of every relation completes before the first DDL statement, so a
// refusal is read-only with respect to the directory relations.
func ensureCoreDirectoryRelations(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	descs []model.EntityDescriptor,
	after directoryMigrationAfter,
) error {
	ordered, err := exactCoreDirectoryDescriptors(descs)
	if err != nil {
		return err
	}

	found := make([]bool, len(ordered))
	for i, desc := range ordered {
		shape, exists, ierr := inspectCoreDirectoryRelation(ctx, tx, dia, desc.Table)
		if ierr != nil {
			return fmt.Errorf("core directory v7: inspect %q: %w", desc.Table, ierr)
		}
		found[i] = exists
		if exists {
			if verr := verifyCoreDirectoryRelationShape(dia, desc, shape); verr != nil {
				return fmt.Errorf("core directory v7: relation %q is malformed: %w", desc.Table, verr)
			}
		}
	}

	present := 0
	for _, exists := range found {
		if exists {
			present++
		}
	}
	var initial coreDirectoryInitialDisposition
	switch present {
	case 0:
		initial = coreDirectoryInitiallyAbsent
		for _, desc := range ordered {
			for statement, stmt := range dia.CreateTableStmts(desc) {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("core directory v7: create %q statement %d: %w",
						desc.Table, statement+1, err)
				}
			}
		}
		// Do not take successful DDL on faith. The same transaction must read
		// back the complete descriptor contract before it records v7 or hands
		// control to the guard-edition transition.
		for _, desc := range ordered {
			shape, exists, ierr := inspectCoreDirectoryRelation(ctx, tx, dia, desc.Table)
			if ierr != nil {
				return fmt.Errorf("core directory v7: verify created %q: %w", desc.Table, ierr)
			}
			if !exists {
				return fmt.Errorf("core directory v7: created relation %q is not visible in its transaction", desc.Table)
			}
			if verr := verifyCoreDirectoryRelationShape(dia, desc, shape); verr != nil {
				return fmt.Errorf("core directory v7: created relation %q is malformed: %w", desc.Table, verr)
			}
			if verr := verifyCoreDirectoryRelationContract(ctx, tx, dia, desc); verr != nil {
				return fmt.Errorf("core directory v7: created relation %q violates its descriptor contract: %w",
					desc.Table, verr)
			}
		}
	case len(ordered):
		initial = coreDirectoryInitiallyPresent
		// Fresh v2 already emitted every relation. Columns were verified above;
		// now prove the objects columns cannot speak for: CHECKs, indexes,
		// isolation and append-only guards. v7 issues no target DDL here.
		for _, desc := range ordered {
			if verr := verifyCoreDirectoryRelationContract(ctx, tx, dia, desc); verr != nil {
				return fmt.Errorf("core directory v7: relation %q violates its descriptor contract: %w",
					desc.Table, verr)
			}
		}
	default:
		var have, missing []string
		for i, desc := range ordered {
			if found[i] {
				have = append(have, desc.Table)
			} else {
				missing = append(missing, desc.Table)
			}
		}
		return fmt.Errorf("core directory v7: partial relation set (present=%v, absent=%v); refusing to complete state no transactional v7 can produce",
			have, missing)
	}

	// The writer control is intrinsic v7 state, not a later best-effort
	// reconcile. Create and seed it after the directory relations have converged
	// but before the guard-edition continuation, on this same transaction. A
	// continuation failure therefore rolls relations, control/marker, guard
	// history and the v7 tracking row back together.
	if dia.Name() == store.EnginePostgres {
		var markerExists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2)`,
			dialect.EngineSchema, dialect.DirectoryWriterMarkerTable).Scan(&markerExists); err != nil {
			return fmt.Errorf("core directory v7: inspect PostgreSQL marker absence: %w", err)
		}
		if markerExists {
			return fmt.Errorf("core directory v7: PostgreSQL unexpectedly contains SQLite-only relation %q",
				dialect.DirectoryWriterMarkerTable)
		}
	}
	for statement, stmt := range dia.DirectoryWriterControlStmts() {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("core directory v7: create directory writer control statement %d: %w",
				statement+1, err)
		}
	}
	if _, err := verifyDirectoryWriterControl(ctx, tx, dia); err != nil {
		return fmt.Errorf("core directory v7: verify newly-created directory writer control: %w", err)
	}

	if after != nil {
		if err := after(ctx, tx, initial); err != nil {
			return fmt.Errorf("core directory v7 continuation: %w", err)
		}
	}
	return nil
}

// verifyCoreDirectoryRelationsPerBoot repeats v7's full, non-healing contract
// check after reconcileColumns on EVERY Open. migrate.Apply skips a tracked v7;
// without this leg a later DROP/replace of a tombstone immutability trigger would
// never reach the migration verifier again, and the generic isolation self-test
// sees only tenant scope. PostgreSQL's descriptor probes live inside savepoints
// and roll back; SQLite reads sqlite_master. No target object is repaired here.
func verifyCoreDirectoryRelationsPerBoot(
	ctx context.Context,
	db dialect.Execer,
	dia dialect.Dialect,
	descs []model.EntityDescriptor,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("core directory per-boot verification: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // verification commits no state
	return verifyCoreDirectoryRelationsExact(ctx, tx, dia, descs)
}

// verifyCoreDirectoryRelationsExact is the transaction-local form used by the
// activation ceremony after it has acquired the global lock order. Keeping the
// inspection on that transaction avoids a second pool checkout (and a
// MaxConns=1 deadlock) while preserving the same non-healing contract as boot.
func verifyCoreDirectoryRelationsExact(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	descs []model.EntityDescriptor,
) error {
	ordered, err := exactCoreDirectoryDescriptors(descs)
	if err != nil {
		return err
	}
	for _, desc := range ordered {
		shape, exists, err := inspectCoreDirectoryRelation(ctx, tx, dia, desc.Table)
		if err != nil {
			return fmt.Errorf("core directory per-boot verification: inspect %q: %w", desc.Table, err)
		}
		if !exists {
			return fmt.Errorf("core directory per-boot verification: relation %q is absent", desc.Table)
		}
		if err := verifyCoreDirectoryRelationShape(dia, desc, shape); err != nil {
			return fmt.Errorf("core directory per-boot verification: relation %q is malformed: %w", desc.Table, err)
		}
		if err := verifyCoreDirectoryRelationContract(ctx, tx, dia, desc); err != nil {
			return fmt.Errorf("core directory per-boot verification: relation %q violates its descriptor contract: %w",
				desc.Table, err)
		}
	}
	return nil
}

// coreDirectoryMigrationIsTracked is called only after the core-version
// preflight has proved the tracking relation and every v7 row canonical. It
// answers whether the pre-reconcile directory contract must already exist,
// without running migrate.Apply (whose ensureTracking may perform DDL).
func coreDirectoryMigrationIsTracked(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
) (bool, error) {
	columns, err := dia.TableColumns(ctx, q, coreTrackingTable)
	if err != nil {
		return false, err
	}
	if len(columns) == 0 {
		return false, nil
	}
	query := dia.Rebind("SELECT COUNT(*) FROM " + coreTrackingRelation(dia) + " WHERE version = ?")
	rows, err := q.QueryContext(ctx, query, coreDirectoryMigrationVersion)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	var count int
	if err := rows.Scan(&count); err != nil {
		return false, err
	}
	if rows.Next() {
		return false, fmt.Errorf("core directory tracking count returned more than one row")
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return count == 1, nil
}

// exactCoreDirectoryDescriptors selects exactly v7's three relations from the
// full core census and fixes their order. The order is not inferred from
// coreDescriptors: it is durable migration behavior, and a later registry
// reorder must not reorder the v7 DDL or its failure boundary.
func exactCoreDirectoryDescriptors(descs []model.EntityDescriptor) ([]model.EntityDescriptor, error) {
	required := []struct {
		kind  model.Kind
		table string
	}{
		{model.DirectoryEpochKind, "core_directory_epoch"},
		{model.DirectoryTombstoneKind, "core_directory_tombstone"},
		{model.UserTombstoneKind, "core_user_tombstone"},
	}
	byTable := make(map[string]model.EntityDescriptor, len(descs))
	for _, desc := range descs {
		if _, duplicate := byTable[desc.Table]; duplicate {
			return nil, fmt.Errorf("core directory v7: descriptor table %q is duplicated", desc.Table)
		}
		byTable[desc.Table] = desc
	}
	ordered := make([]model.EntityDescriptor, 0, len(required))
	for _, want := range required {
		desc, ok := byTable[want.table]
		if !ok {
			return nil, fmt.Errorf("core directory v7: required descriptor %s/%s is absent", want.kind, want.table)
		}
		if desc.Kind != want.kind {
			return nil, fmt.Errorf("core directory v7: table %q has kind %q, want %q", want.table, desc.Kind, want.kind)
		}
		ordered = append(ordered, desc)
	}
	return ordered, nil
}

type coreDirectoryColumnShape struct {
	Name       string
	Type       string
	NotNull    bool
	PrimaryKey bool
}

type coreDirectoryRelationShape struct {
	Columns []coreDirectoryColumnShape
}

func inspectCoreDirectoryRelation(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	table string,
) (coreDirectoryRelationShape, bool, error) {
	switch dia.Name() {
	case store.EngineSQLite:
		return inspectSQLiteCoreDirectoryRelation(ctx, tx, table)
	case store.EnginePostgres:
		return inspectPostgresCoreDirectoryRelation(ctx, tx, table)
	default:
		return coreDirectoryRelationShape{}, false,
			fmt.Errorf("unsupported engine %q", dia.Name())
	}
}

func inspectSQLiteCoreDirectoryRelation(
	ctx context.Context,
	tx *sql.Tx,
	table string,
) (coreDirectoryRelationShape, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT schema_name, object_type FROM (
  SELECT 'main' AS schema_name, type AS object_type FROM sqlite_master WHERE name = ?
  UNION ALL
  SELECT 'temp' AS schema_name, type AS object_type FROM sqlite_temp_master WHERE name = ?
) ORDER BY schema_name, object_type`, table, table)
	if err != nil {
		return coreDirectoryRelationShape{}, false, err
	}
	var objects [][2]string
	for rows.Next() {
		var schemaName, objectType string
		if err := rows.Scan(&schemaName, &objectType); err != nil {
			_ = rows.Close()
			return coreDirectoryRelationShape{}, false, err
		}
		objects = append(objects, [2]string{schemaName, objectType})
	}
	if err := rows.Close(); err != nil {
		return coreDirectoryRelationShape{}, false, err
	}
	if len(objects) == 0 {
		return coreDirectoryRelationShape{}, false, nil
	}
	if len(objects) != 1 || objects[0] != [2]string{"main", "table"} {
		return coreDirectoryRelationShape{}, false,
			fmt.Errorf("name resolves to objects %v, want one ordinary main table", objects)
	}

	columns, err := tx.QueryContext(ctx,
		`SELECT name, type, "notnull", pk FROM pragma_table_info(?, 'main') ORDER BY cid`, table)
	if err != nil {
		return coreDirectoryRelationShape{}, false, err
	}
	defer columns.Close()
	shape := coreDirectoryRelationShape{}
	for columns.Next() {
		var col coreDirectoryColumnShape
		var notNull, primary int
		if err := columns.Scan(&col.Name, &col.Type, &notNull, &primary); err != nil {
			return coreDirectoryRelationShape{}, false, err
		}
		col.Type = normalizeCoreDirectoryType(col.Type)
		col.NotNull = notNull != 0
		col.PrimaryKey = primary != 0
		shape.Columns = append(shape.Columns, col)
	}
	if err := columns.Err(); err != nil {
		return coreDirectoryRelationShape{}, false, err
	}
	return shape, true, nil
}

func inspectPostgresCoreDirectoryRelation(
	ctx context.Context,
	tx *sql.Tx,
	table string,
) (coreDirectoryRelationShape, bool, error) {
	var relkind, persistence string
	var partition, hasParent, hasChild bool
	err := tx.QueryRowContext(ctx, `SELECT c.relkind::text, c.relpersistence::text,
       c.relispartition,
       EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid = c.oid),
       EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhparent = c.oid)
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, dialect.EngineSchema, table).
		Scan(&relkind, &persistence, &partition, &hasParent, &hasChild)
	if errors.Is(err, sql.ErrNoRows) {
		return coreDirectoryRelationShape{}, false, nil
	}
	if err != nil {
		return coreDirectoryRelationShape{}, false, err
	}
	if relkind != "r" || persistence != "p" || partition || hasParent || hasChild {
		return coreDirectoryRelationShape{}, false, fmt.Errorf(
			"relation form is relkind=%q persistence=%q partition=%t parent=%t child=%t, want ordinary permanent non-inherited table",
			relkind, persistence, partition, hasParent, hasChild)
	}

	columns, err := tx.QueryContext(ctx, `SELECT a.attname,
       pg_catalog.format_type(a.atttypid, a.atttypmod),
       a.attnotnull,
       EXISTS (
         SELECT 1 FROM pg_catalog.pg_constraint p
         WHERE p.conrelid = c.oid AND p.contype = 'p' AND a.attnum = ANY(p.conkey)
       ),
       a.attisdropped
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0
WHERE n.nspname = $1 AND c.relname = $2
ORDER BY a.attnum`, dialect.EngineSchema, table)
	if err != nil {
		return coreDirectoryRelationShape{}, false, err
	}
	defer columns.Close()
	shape := coreDirectoryRelationShape{}
	for columns.Next() {
		var col coreDirectoryColumnShape
		var dropped bool
		if err := columns.Scan(&col.Name, &col.Type, &col.NotNull, &col.PrimaryKey, &dropped); err != nil {
			return coreDirectoryRelationShape{}, false, err
		}
		if dropped {
			return coreDirectoryRelationShape{}, false,
				fmt.Errorf("relation has a dropped physical column at ordinal %d", len(shape.Columns)+1)
		}
		col.Type = normalizeCoreDirectoryType(col.Type)
		shape.Columns = append(shape.Columns, col)
	}
	if err := columns.Err(); err != nil {
		return coreDirectoryRelationShape{}, false, err
	}
	return shape, true, nil
}

func verifyCoreDirectoryRelationShape(
	dia dialect.Dialect,
	desc model.EntityDescriptor,
	got coreDirectoryRelationShape,
) error {
	want, err := expectedCoreDirectoryColumns(dia, desc)
	if err != nil {
		return err
	}
	if len(got.Columns) != len(want) {
		return fmt.Errorf("has %d columns, want %d", len(got.Columns), len(want))
	}
	for i := range want {
		if got.Columns[i] != want[i] {
			return fmt.Errorf("column %d is %+v, want %+v", i+1, got.Columns[i], want[i])
		}
	}
	return nil
}

func expectedCoreDirectoryColumns(
	dia dialect.Dialect,
	desc model.EntityDescriptor,
) ([]coreDirectoryColumnShape, error) {
	// SQLite reports `id TEXT PRIMARY KEY` as pk=1/notnull=0; PostgreSQL
	// materializes the primary key's implicit NOT NULL. Both are the exact live
	// shapes produced by CreateTableStmts, not a weakened equivalence.
	idNotNull := dia.Name() == store.EnginePostgres
	out := []coreDirectoryColumnShape{
		{Name: model.ColID, Type: expectedCoreDirectoryType(dia, model.KindUUID), NotNull: idNotNull, PrimaryKey: true},
		{Name: model.ColTenantID, Type: expectedCoreDirectoryType(dia, model.KindUUID), NotNull: true},
		{Name: model.ColCreatedAt, Type: expectedCoreDirectoryType(dia, model.KindTimestamp), NotNull: true},
		{Name: model.ColUpdatedAt, Type: expectedCoreDirectoryType(dia, model.KindTimestamp), NotNull: true},
		{Name: model.ColVersion, Type: expectedCoreDirectoryType(dia, model.KindInt), NotNull: true},
	}
	if desc.SoftDelete {
		out = append(out, coreDirectoryColumnShape{
			Name: model.ColDeletedAt, Type: expectedCoreDirectoryType(dia, model.KindTimestamp),
		})
	}
	seen := make(map[string]bool, len(out)+len(desc.Fields))
	for _, col := range out {
		seen[col.Name] = true
	}
	for _, field := range desc.Fields {
		if seen[field.Name] {
			return nil, fmt.Errorf("descriptor repeats column %q", field.Name)
		}
		seen[field.Name] = true
		out = append(out, coreDirectoryColumnShape{
			Name: field.Name, Type: expectedCoreDirectoryType(dia, field.Kind), NotNull: !field.Nullable,
		})
	}
	return out, nil
}

func expectedCoreDirectoryType(dia dialect.Dialect, kind model.SQLKind) string {
	return normalizeCoreDirectoryType(strings.TrimSuffix(dia.ColumnType(kind, true), " NOT NULL"))
}

func normalizeCoreDirectoryType(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}
