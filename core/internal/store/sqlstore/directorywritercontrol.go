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
	"reflect"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var (
	errDirectoryWriterControlInvalid = errors.New("directory writer control is invalid")
	errDirectoryWriterGuardInvalid   = errors.New("directory writer source guard is invalid")
)

type directoryWriterMode string

const (
	directoryWriterStaged   directoryWriterMode = "staged"
	directoryWriterEnforced directoryWriterMode = "enforced"
)

type directoryWriterControlState struct {
	Mode               directoryWriterMode
	ExpectedGeneration int64
}

type directoryWriterACLQuerier interface {
	dialect.Querier
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func directoryWriterRelation(dia dialect.Dialect, table string) string {
	if dia.Name() == store.EnginePostgres {
		return quoteIdent(dialect.EngineSchema) + "." + quoteIdent(table)
	}
	return "main." + quoteIdent(table)
}

// directoryWriterSourceTables is a closed inventory. Adding a mutable directory
// fact to the registry does not silently put it behind this capability: the K3
// writer protocol must name and test the new source deliberately.
var directoryWriterSourceTables = []string{
	"users",
	"memberships",
	"user_groups",
	"user_group_members",
	"identities",
	"agents",
	"agent_groups",
	"agent_group_members",
	"orgs",
}

func isDirectoryWriterSourceTable(table string) bool {
	for _, source := range directoryWriterSourceTables {
		if source == table {
			return true
		}
	}
	return false
}

// isDirectoryAuthorityTable pins every B4 authority relation past SQLite TEMP
// shadows and PostgreSQL search_path changes. Some are not epoch-bearing
// directory sources, so they deliberately stay out of the writer-trigger
// inventory above; qualification is nevertheless required for both the
// post-lock validation reads and the target DML they authorize.
func isDirectoryAuthorityTable(table string) bool {
	if isDirectoryWriterSourceTable(table) {
		return true
	}
	switch table {
	case authSessionDescriptor.Table,
		apiTokenDescriptor.Table,
		webauthnCredentialDescriptor.Table,
		delegationHandleDescriptor.Table,
		pepServiceCredentialDescriptor.Table,
		workspaceDescriptor.Table:
		return true
	default:
		return false
	}
}

// reconcileDirectoryWriterGuards runs after reconcileColumns, while Open still
// holds the migration lock. That ordering is load-bearing for old databases:
// v7 can create the control even when one of these descriptor tables does not
// exist yet, then the additive descriptor reconcile creates it, and only now do
// all nine guard targets exist.
//
// A staged control is the one repair window: missing expected objects are added,
// but a same-name object with different behavior is never replaced. Once the
// control is enforced, absence and drift are both refusals. The control and the
// SQLite marker are never repaired here; v7 is their only creator, so losing or
// replacing either after its tracking row exists is evidence of corruption, not
// an invitation to reseed staged.
func reconcileDirectoryWriterGuards(
	ctx context.Context,
	ownerDB dialect.Execer,
	appDB *sql.DB,
	dia dialect.Dialect,
	hardened bool,
	roles guardRoles,
) error {
	tx, err := ownerDB.BeginTx(ctx, directoryWriterTxOptions(dia))
	if err != nil {
		return fmt.Errorf("sqlstore: directory writer reconcile begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	state, err := verifyDirectoryWriterControl(ctx, tx, dia)
	if err != nil {
		return fmt.Errorf("sqlstore: directory writer reconcile: %w", err)
	}
	for _, table := range directoryWriterSourceTables {
		columns, cerr := dia.TableColumns(ctx, tx, table)
		if cerr != nil {
			return fmt.Errorf("sqlstore: directory writer reconcile: inspect source table %q: %w", table, cerr)
		}
		if len(columns) == 0 {
			return fmt.Errorf("sqlstore: directory writer reconcile: %w: source table %q is absent after core column reconciliation",
				errDirectoryWriterGuardInvalid, table)
		}
	}

	switch dia.Name() {
	case store.EngineSQLite:
		if err := reconcileSQLiteDirectoryWriterGuards(ctx, tx, dia, state); err != nil {
			return err
		}
	case store.EnginePostgres:
		if err := reconcilePostgresDirectoryWriterGuards(ctx, tx, state); err != nil {
			return err
		}
		if err := reconcilePostgresDirectoryWriterControlACL(ctx, tx, hardened, roles); err != nil {
			return err
		}
	default:
		return fmt.Errorf("sqlstore: directory writer reconcile: unsupported engine %q", dia.Name())
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlstore: directory writer reconcile commit: %w", err)
	}
	if err := verifyPostgresDirectoryWriterControlACL(ctx, appDB, dia, hardened, roles); err != nil {
		return err
	}

	switch {
	case dia.Name() == store.EngineSQLite:
		slog.Warn("store: directory writer control posture resolved",
			"posture", "sqlite_capability", "mode", state.Mode,
			"expected_generation", state.ExpectedGeneration)
	case hardened:
		slog.Info("store: directory writer control posture resolved",
			"posture", "split_owner", "mode", state.Mode,
			"expected_generation", state.ExpectedGeneration)
	default:
		slog.Warn("store: directory writer control posture resolved: the application role and migration authority are not independent, so the raw control is a capability boundary, not hardening against arbitrary SQL by that role",
			"posture", "single_role_capability", "mode", state.Mode,
			"expected_generation", state.ExpectedGeneration)
	}
	return nil
}

// verifyDirectoryWriterGuardsExact is activation's read-only gate. Open may
// create missing guards while the control is staged so an interrupted rollout
// can resume; activation is intentionally different: every control, marker,
// source, trigger, function and hardened ACL must already be exact. This helper
// never invokes a reconciler and therefore cannot turn a failed assertion into
// a passing one in the ceremony transaction.
func verifyDirectoryWriterGuardsExact(
	ctx context.Context,
	tx *sql.Tx,
	appAuthority directoryWriterACLQuerier,
	dia dialect.Dialect,
	hardened bool,
	roles guardRoles,
) error {
	if _, err := verifyDirectoryWriterControl(ctx, tx, dia); err != nil {
		return fmt.Errorf("sqlstore: directory activation exact control: %w", err)
	}
	for _, table := range directoryWriterSourceTables {
		columns, err := dia.TableColumns(ctx, tx, table)
		if err != nil {
			return fmt.Errorf("sqlstore: directory activation inspect source table %q: %w", table, err)
		}
		if len(columns) == 0 {
			return fmt.Errorf("sqlstore: directory activation: %w: source table %q is absent",
				errDirectoryWriterGuardInvalid, table)
		}
	}

	switch dia.Name() {
	case store.EngineSQLite:
		missing, err := inspectSQLiteDirectoryWriterGuards(
			ctx, tx, dia, sqliteDirectoryWriterGuardSpecs(),
		)
		if err != nil {
			return fmt.Errorf("sqlstore: directory activation exact SQLite guards: %w", err)
		}
		if len(missing) != 0 {
			return fmt.Errorf("sqlstore: directory activation: %w: missing SQLite triggers %v",
				errDirectoryWriterGuardInvalid, missing)
		}
		return nil
	case store.EnginePostgres:
		if err := verifyPostgresDirectoryWriterGuardsExact(ctx, tx); err != nil {
			return err
		}
		tables := directoryActivationAuthorityTables()
		if err := requireTableOwnership(ctx, tx, tables); err != nil {
			return fmt.Errorf("sqlstore: directory activation writer ownership: %w", err)
		}
		if err := requirePostgresDirectoryWriterFunctionAdministration(ctx, tx); err != nil {
			return fmt.Errorf("sqlstore: directory activation writer function ownership: %w", err)
		}
		if err := verifyPostgresDirectoryWriterControlACL(ctx, appAuthority, dia, hardened, roles); err != nil {
			return fmt.Errorf("sqlstore: directory activation exact ACL: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("sqlstore: directory activation exact guards: unsupported engine %q", dia.Name())
	}
}

func verifyPostgresDirectoryWriterGuardsExact(ctx context.Context, tx *sql.Tx) error {
	want := canonicalPostgresDirectoryWriterDefinition()
	function, present, err := projectGuardFunction(
		ctx, tx, dialect.EngineSchema, dialect.DirectoryWriterGuardFunction,
	)
	if err != nil {
		return fmt.Errorf("sqlstore: directory activation inspect PostgreSQL function: %w", err)
	}
	if !present {
		return fmt.Errorf("sqlstore: directory activation: %w: PostgreSQL writer function is absent",
			errDirectoryWriterGuardInvalid)
	}
	if diff := guardFunctionDiff(want.Function, function); len(diff) != 0 {
		return fmt.Errorf("sqlstore: directory activation: %w: PostgreSQL function drift: %v",
			errDirectoryWriterGuardInvalid, diff)
	}
	if err := verifyPostgresDirectoryWriterFunctionConfig(ctx, tx); err != nil {
		return fmt.Errorf("sqlstore: directory activation: %w: %v",
			errDirectoryWriterGuardInvalid, err)
	}
	unexpected, err := unexpectedPostgresDirectoryWriterAttachments(ctx, tx)
	if err != nil {
		return fmt.Errorf("sqlstore: directory activation inventory PostgreSQL attachments: %w", err)
	}
	if len(unexpected) != 0 {
		return fmt.Errorf("sqlstore: directory activation: %w: unexpected PostgreSQL writer attachments %v",
			errDirectoryWriterGuardInvalid, unexpected)
	}
	for _, table := range directoryWriterSourceTables {
		key := guardKey{
			Schema: dialect.EngineSchema, Relation: table,
			Trigger: table + "_directory_writer_guard",
		}
		row, err := projectGuardCatalogRow(ctx, tx, key)
		if err != nil {
			return fmt.Errorf("sqlstore: directory activation inspect PostgreSQL trigger on %q: %w",
				table, err)
		}
		diff := guardDefinitionDiff(want, row.definition())
		if !row.RelationExists || !row.GuardExists || !row.FunctionExists ||
			row.EnableState != string(dialect.TriggerFiresAlways) || len(diff) != 0 {
			return fmt.Errorf("sqlstore: directory activation: %w: PostgreSQL trigger on %q relation=%t trigger=%t function=%t state=%q drift=%v",
				errDirectoryWriterGuardInvalid, table, row.RelationExists,
				row.GuardExists, row.FunctionExists, row.EnableState, diff)
		}
	}
	return nil
}

// verifyDirectoryWriterControlPerBoot is the raw-state preflight used when v7
// is already tracked. It runs before rollout classification or any migration,
// so a missing/replaced singleton or SQLite marker cannot be hidden by later
// boot work. The source guards are intentionally not checked here: a crash
// after v7 commits but before the staged reconciler is a supported healing
// boundary.
func verifyDirectoryWriterControlPerBoot(
	ctx context.Context,
	db dialect.Execer,
	dia dialect.Dialect,
) error {
	tx, err := db.BeginTx(ctx, directoryWriterTxOptions(dia))
	if err != nil {
		return fmt.Errorf("sqlstore: directory writer raw preflight begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // verifier never commits state
	if _, err := verifyDirectoryWriterControl(ctx, tx, dia); err != nil {
		return fmt.Errorf("sqlstore: directory writer raw preflight: %w", err)
	}
	return nil
}

func verifyDirectoryWriterControl(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) (directoryWriterControlState, error) {
	var state directoryWriterControlState
	switch dia.Name() {
	case store.EngineSQLite:
		stmts := dia.DirectoryWriterControlStmts()
		if len(stmts) != 3 {
			return state, fmt.Errorf("%w: SQLite dialect rendered %d control statements, want 3",
				errDirectoryWriterControlInvalid, len(stmts))
		}
		if err := verifySQLiteDirectoryWriterRawTable(ctx, tx,
			dialect.DirectoryWriterControlTable, stmts[0]); err != nil {
			return state, err
		}
		if err := verifySQLiteDirectoryWriterRawTable(ctx, tx,
			dialect.DirectoryWriterMarkerTable, stmts[2]); err != nil {
			return state, err
		}
	case store.EnginePostgres:
		if err := verifyPostgresDirectoryWriterControlTable(ctx, tx, dia); err != nil {
			return state, err
		}
		var markerExists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2)`,
			dialect.EngineSchema, dialect.DirectoryWriterMarkerTable).Scan(&markerExists); err != nil {
			return state, fmt.Errorf("%w: inspect PostgreSQL marker absence: %v",
				errDirectoryWriterControlInvalid, err)
		}
		if markerExists {
			return state, fmt.Errorf("%w: PostgreSQL unexpectedly contains SQLite-only relation %q",
				errDirectoryWriterControlInvalid, dialect.DirectoryWriterMarkerTable)
		}
	default:
		return state, fmt.Errorf("%w: unsupported engine %q", errDirectoryWriterControlInvalid, dia.Name())
	}

	// #nosec G202 -- the FROM relation comes from directoryWriterRelation over a dialect constant; the selected columns are literal in the source
	query := "SELECT control_key, mode, expected_generation FROM " +
		directoryWriterRelation(dia, dialect.DirectoryWriterControlTable)
	if dia.Name() == store.EngineSQLite {
		query = "SELECT control_key, mode, expected_generation, " +
			"typeof(control_key), typeof(mode), typeof(expected_generation) FROM " +
			directoryWriterRelation(dia, dialect.DirectoryWriterControlTable)
	}
	query = dia.Rebind(query)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return state, fmt.Errorf("%w: read singleton: %v", errDirectoryWriterControlInvalid, err)
	}
	var count int
	var key, mode string
	var generation int64
	var keyType, modeType, generationType string
	for rows.Next() {
		count++
		if count > 1 {
			_ = rows.Close()
			return state, fmt.Errorf("%w: singleton relation contains more than one row",
				errDirectoryWriterControlInvalid)
		}
		var scanErr error
		if dia.Name() == store.EngineSQLite {
			scanErr = rows.Scan(&key, &mode, &generation, &keyType, &modeType, &generationType)
		} else {
			scanErr = rows.Scan(&key, &mode, &generation)
		}
		if scanErr != nil {
			_ = rows.Close()
			return state, fmt.Errorf("%w: scan singleton: %v", errDirectoryWriterControlInvalid, scanErr)
		}
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return state, fmt.Errorf("%w: read singleton: %v", errDirectoryWriterControlInvalid, err)
	}
	if count != 1 || key != dialect.DirectoryWriterControlKey || generation <= 0 ||
		(mode != string(directoryWriterStaged) && mode != string(directoryWriterEnforced)) {
		return state, fmt.Errorf("%w: singleton is count=%d key=%q mode=%q expected_generation=%d",
			errDirectoryWriterControlInvalid, count, key, mode, generation)
	}
	if dia.Name() == store.EngineSQLite &&
		(keyType != "text" || modeType != "text" || generationType != "integer") {
		return state, fmt.Errorf("%w: SQLite singleton storage classes are key=%q mode=%q expected_generation=%q, want text/text/integer",
			errDirectoryWriterControlInvalid, keyType, modeType, generationType)
	}
	state = directoryWriterControlState{
		Mode: directoryWriterMode(mode), ExpectedGeneration: generation,
	}

	if dia.Name() == store.EngineSQLite {
		var markerRows int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+directoryWriterRelation(dia, dialect.DirectoryWriterMarkerTable)).Scan(&markerRows); err != nil {
			return state, fmt.Errorf("%w: read SQLite marker baseline: %v",
				errDirectoryWriterControlInvalid, err)
		}
		if markerRows != 0 {
			return state, fmt.Errorf("%w: SQLite marker durable baseline contains %d row(s), want empty",
				errDirectoryWriterControlInvalid, markerRows)
		}
	}
	return state, nil
}

func verifySQLiteDirectoryWriterRawTable(
	ctx context.Context,
	tx *sql.Tx,
	table string,
	wantDefinition string,
) error {
	rows, err := tx.QueryContext(ctx, `SELECT schema_name, type, name, COALESCE(sql, '') FROM (
  SELECT 'main' AS schema_name, type, name, sql
  FROM main.sqlite_master
  WHERE name = ? OR (tbl_name = ? AND sql IS NOT NULL)
  UNION ALL
  SELECT 'temp' AS schema_name, type, name, sql
  FROM temp.sqlite_master
  WHERE name = ? OR (tbl_name = ? AND sql IS NOT NULL)
)
ORDER BY schema_name, type, name`, table, table, table, table)
	if err != nil {
		return fmt.Errorf("%w: inspect SQLite relation %q: %v",
			errDirectoryWriterControlInvalid, table, err)
	}
	type sqliteObject struct{ schema, typ, name, definition string }
	var objects []sqliteObject
	for rows.Next() {
		var object sqliteObject
		if err := rows.Scan(&object.schema, &object.typ, &object.name, &object.definition); err != nil {
			_ = rows.Close()
			return fmt.Errorf("%w: inspect SQLite relation %q: %v",
				errDirectoryWriterControlInvalid, table, err)
		}
		objects = append(objects, object)
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return fmt.Errorf("%w: inspect SQLite relation %q: %v",
			errDirectoryWriterControlInvalid, table, err)
	}
	want := []sqliteObject{{schema: "main", typ: "table", name: table, definition: wantDefinition}}
	if !reflect.DeepEqual(objects, want) {
		return fmt.Errorf("%w: SQLite relation %q objects=%v want=%v",
			errDirectoryWriterControlInvalid, table, objects, want)
	}
	return nil
}

func verifyPostgresDirectoryWriterControlTable(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
) error {
	columns, err := dia.TableColumns(ctx, tx, dialect.DirectoryWriterControlTable)
	if err != nil {
		return fmt.Errorf("%w: inspect PostgreSQL control columns: %v",
			errDirectoryWriterControlInvalid, err)
	}
	if len(columns) == 0 {
		return fmt.Errorf("%w: PostgreSQL relation %q is absent",
			errDirectoryWriterControlInvalid, dialect.DirectoryWriterControlTable)
	}

	const savepoint = "olv_k3_directory_writer_control_probe"
	const probe = "olv_k3_writer_control_probe"
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		return fmt.Errorf("%w: create PostgreSQL shape savepoint: %v",
			errDirectoryWriterControlInvalid, err)
	}
	var compareErr error
	stmts := dia.DirectoryWriterControlStmts()
	if len(stmts) != 3 {
		compareErr = fmt.Errorf("PostgreSQL dialect rendered %d control statements, want 3", len(stmts))
	} else {
		probeDDL := strings.ReplaceAll(stmts[0], dialect.DirectoryWriterControlTable, probe)
		if _, err := tx.ExecContext(ctx, probeDDL); err != nil {
			compareErr = fmt.Errorf("create PostgreSQL shape probe: %w", err)
		} else {
			want, wantErr := projectPostgresCoreDirectoryContract(ctx, tx, probe,
				dialect.DirectoryWriterControlTable)
			got, gotErr := projectPostgresCoreDirectoryContract(ctx, tx,
				dialect.DirectoryWriterControlTable, dialect.DirectoryWriterControlTable)
			switch {
			case wantErr != nil:
				compareErr = fmt.Errorf("project PostgreSQL shape probe: %w", wantErr)
			case gotErr != nil:
				compareErr = fmt.Errorf("project PostgreSQL control: %w", gotErr)
			default:
				compareErr = postgresCoreDirectoryContractDifference(want, got)
			}
		}
	}
	_, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint)
	_, releaseErr := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint)
	cleanupErr := errors.Join(rollbackErr, releaseErr)
	if err := errors.Join(compareErr, cleanupErr); err != nil {
		return fmt.Errorf("%w: PostgreSQL relation %q shape: %v",
			errDirectoryWriterControlInvalid, dialect.DirectoryWriterControlTable, err)
	}
	return nil
}

type sqliteDirectoryWriterGuardSpec struct {
	Table           string
	Name            string
	Definition      string
	CreateStatement string
}

func sqliteDirectoryWriterGuardSpecs() []sqliteDirectoryWriterGuardSpec {
	var specs []sqliteDirectoryWriterGuardSpec
	for _, table := range directoryWriterSourceTables {
		for _, event := range []struct {
			suffix string
			verb   string
		}{
			{suffix: "ins", verb: "INSERT"},
			{suffix: "upd", verb: "UPDATE"},
			{suffix: "del", verb: "DELETE"},
		} {
			needsGeneration := "1"
			if table == "orgs" {
				system := model.SystemTenantID.String()
				switch event.suffix {
				case "ins":
					needsGeneration = "NEW.tenant_id IS NOT '" + system + "' COLLATE BINARY"
				case "del":
					needsGeneration = "OLD.tenant_id IS NOT '" + system + "' COLLATE BINARY"
				case "upd":
					needsGeneration = "OLD.status IS NOT NEW.status AND NOT (OLD.tenant_id IS '" + system +
						"' COLLATE BINARY AND NEW.tenant_id IS '" + system + "' COLLATE BINARY)"
				}
			}
			name := table + "_directory_writer_guard_" + event.suffix
			definition := fmt.Sprintf("CREATE TRIGGER %s BEFORE %s ON %s", name, event.verb, table)
			definition += "\n" + sqliteDirectoryWriterGuardBody(needsGeneration)
			specs = append(specs, sqliteDirectoryWriterGuardSpec{
				Table: table, Name: name, Definition: definition,
				CreateStatement: strings.Replace(definition,
					"CREATE TRIGGER "+name, "CREATE TRIGGER main."+name, 1),
			})
		}
	}
	return specs
}

func sqliteDirectoryWriterGuardBody(needsGeneration string) string {
	return `BEGIN
  SELECT RAISE(ABORT, 'directory writer control invalid')
  WHERE (SELECT COUNT(*) FROM main.directory_writer_control) <> 1
     OR (SELECT COUNT(*) FROM main.directory_writer_control
         WHERE typeof(control_key) = 'text'
           AND control_key = 'core.directory.writer' COLLATE BINARY
           AND typeof(mode) = 'text'
           AND mode COLLATE BINARY IN ('staged', 'enforced')
           AND typeof(expected_generation) = 'integer'
           AND expected_generation > 0) <> 1;
  SELECT RAISE(ABORT, 'directory writer generation required')
  WHERE (SELECT mode FROM main.directory_writer_control
         WHERE control_key = 'core.directory.writer' COLLATE BINARY) COLLATE BINARY = 'enforced'
    AND (` + needsGeneration + `)
    AND ((SELECT COUNT(*) FROM main.directory_writer_marker) <> 1
      OR (SELECT COUNT(*)
          FROM main.directory_writer_marker m
          JOIN main.directory_writer_control c
            ON c.control_key COLLATE BINARY = m.control_key COLLATE BINARY
          WHERE typeof(m.control_key) = 'text'
            AND m.control_key = 'core.directory.writer' COLLATE BINARY
            AND typeof(m.generation) = 'integer'
            AND m.generation = c.expected_generation) <> 1);
END`
}

func reconcileSQLiteDirectoryWriterGuards(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	state directoryWriterControlState,
) error {
	specs := sqliteDirectoryWriterGuardSpecs()
	missing, err := inspectSQLiteDirectoryWriterGuards(ctx, tx, dia, specs)
	if err != nil {
		return err
	}
	if len(missing) != 0 && state.Mode == directoryWriterEnforced {
		return fmt.Errorf("sqlstore: directory writer reconcile: %w: enforced control has missing SQLite triggers %v",
			errDirectoryWriterGuardInvalid, missing)
	}
	missingSet := make(map[string]bool, len(missing))
	for _, name := range missing {
		missingSet[name] = true
	}
	for _, spec := range specs {
		if !missingSet[spec.Name] {
			continue
		}
		if _, err := tx.ExecContext(ctx, spec.CreateStatement); err != nil {
			return fmt.Errorf("sqlstore: directory writer reconcile: create SQLite trigger %q: %w",
				spec.Name, err)
		}
	}
	remaining, err := inspectSQLiteDirectoryWriterGuards(ctx, tx, dia, specs)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return fmt.Errorf("sqlstore: directory writer reconcile: %w: SQLite triggers remain absent after creation: %v",
			errDirectoryWriterGuardInvalid, remaining)
	}
	return nil
}

func inspectSQLiteDirectoryWriterGuards(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	specs []sqliteDirectoryWriterGuardSpec,
) ([]string, error) {
	live, err := dia.SchemaTriggers(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: directory writer reconcile: inspect SQLite triggers: %w", err)
	}
	want := make(map[dialect.TriggerKey]sqliteDirectoryWriterGuardSpec, len(specs))
	sources := make(map[string]bool, len(directoryWriterSourceTables))
	for _, table := range directoryWriterSourceTables {
		sources[table] = true
	}
	for _, spec := range specs {
		want[dialect.TriggerKey{Schema: "main", Table: spec.Table, Name: spec.Name}] = spec
	}
	var missing, drift []string
	for key, spec := range want {
		info, ok := live[key]
		if !ok {
			missing = append(missing, spec.Name)
			continue
		}
		if info.Definition != spec.Definition || info.Function != "" || !info.CanExecute ||
			info.EnableState != dialect.TriggerNoEnableState {
			drift = append(drift, fmt.Sprintf("%s got definition=%q function=%q executable=%t state=%s",
				key.String(), info.Definition, info.Function, info.CanExecute, info.EnableState))
		}
	}
	for key := range live {
		if !strings.Contains(key.Name, "_directory_writer_guard") {
			continue
		}
		if _, ok := want[key]; !ok {
			posture := "outside source inventory"
			if sources[key.Table] {
				posture = "reserved name with wrong identity"
			}
			drift = append(drift, "unexpected "+key.String()+" ("+posture+")")
		}
	}
	tempRows, err := tx.QueryContext(ctx, `SELECT name, tbl_name
FROM temp.sqlite_master
WHERE type='trigger' AND instr(name, '_directory_writer_guard') > 0
ORDER BY name, tbl_name`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: directory writer reconcile: inspect temp SQLite triggers: %w", err)
	}
	for tempRows.Next() {
		var name, table string
		if err := tempRows.Scan(&name, &table); err != nil {
			_ = tempRows.Close()
			return nil, fmt.Errorf("sqlstore: directory writer reconcile: inspect temp SQLite triggers: %w", err)
		}
		drift = append(drift, "unexpected temp."+table+"."+name)
	}
	if err := closeCoreDirectoryRows(tempRows); err != nil {
		return nil, fmt.Errorf("sqlstore: directory writer reconcile: inspect temp SQLite triggers: %w", err)
	}
	if len(drift) != 0 {
		sort.Strings(drift)
		return nil, fmt.Errorf("sqlstore: directory writer reconcile: %w: SQLite trigger drift: %v",
			errDirectoryWriterGuardInvalid, drift)
	}
	sort.Strings(missing)
	return missing, nil
}

// PostgreSQL exposes only the effective value of a custom GUC. The guard can
// therefore require an exact generation but cannot prove whether it came from
// SET LOCAL/set_config(..., true) or a session-level SET. The later writer
// wrapper owns that locality contract and must use set_config(..., true) plus
// commit/rollback reset tests; this raw guard deliberately claims no stronger
// database primitive than PostgreSQL provides.
const postgresDirectoryWriterGuardBody = `DECLARE
  control_rows bigint;
  stored_key text COLLATE pg_catalog."C";
  stored_mode text COLLATE pg_catalog."C";
  stored_generation bigint;
  presented_generation text COLLATE pg_catalog."C";
BEGIN
  IF TG_TABLE_SCHEMA COLLATE pg_catalog."C" IS DISTINCT FROM 'public' COLLATE pg_catalog."C"
     OR (TG_TABLE_NAME COLLATE pg_catalog."C") NOT IN (
       'users', 'memberships', 'user_groups', 'user_group_members',
       'identities', 'agents', 'agent_groups', 'agent_group_members', 'orgs'
     ) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'directory writer trigger target invalid';
  END IF;
  IF (TG_OP COLLATE pg_catalog."C") NOT IN ('INSERT', 'UPDATE', 'DELETE') THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'directory writer trigger operation invalid';
  END IF;

  SELECT pg_catalog.count(*), pg_catalog.min(c.control_key), pg_catalog.min(c.mode),
         pg_catalog.min(c.expected_generation)
    INTO control_rows, stored_key, stored_mode, stored_generation
    FROM public.directory_writer_control AS c;
  IF control_rows <> 1
     OR stored_key IS DISTINCT FROM 'core.directory.writer' COLLATE pg_catalog."C"
     OR stored_mode IS NULL
     OR NOT (stored_mode = 'staged' COLLATE pg_catalog."C"
             OR stored_mode = 'enforced' COLLATE pg_catalog."C")
     OR stored_generation IS NULL
     OR stored_generation <= 0 THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'directory writer control invalid';
  END IF;

  IF TG_TABLE_NAME COLLATE pg_catalog."C" = 'orgs' COLLATE pg_catalog."C" THEN
    IF TG_OP COLLATE pg_catalog."C" = 'INSERT' COLLATE pg_catalog."C"
       AND NEW.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff' THEN
      RETURN NEW;
    ELSIF TG_OP COLLATE pg_catalog."C" = 'DELETE' COLLATE pg_catalog."C"
          AND OLD.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff' THEN
      RETURN OLD;
    ELSIF TG_OP COLLATE pg_catalog."C" = 'UPDATE' COLLATE pg_catalog."C" THEN
      IF OLD.status IS NOT DISTINCT FROM NEW.status THEN
        RETURN NEW;
      END IF;
      IF OLD.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
         AND NEW.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff' THEN
        RETURN NEW;
      END IF;
    END IF;
  END IF;

  IF stored_mode = 'enforced' COLLATE pg_catalog."C" THEN
    presented_generation := pg_catalog.current_setting('app.directory_writer_generation', true);
    IF presented_generation IS DISTINCT FROM stored_generation::text COLLATE pg_catalog."C" THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'directory writer generation required';
    END IF;
  END IF;
  IF TG_OP COLLATE pg_catalog."C" = 'DELETE' COLLATE pg_catalog."C" THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END;`

func postgresDirectoryWriterFunctionDDL() string {
	return `CREATE FUNCTION public.` + dialect.DirectoryWriterGuardFunction + `()
RETURNS pg_catalog.trigger
LANGUAGE plpgsql
VOLATILE
SECURITY INVOKER
PARALLEL UNSAFE
SET search_path = pg_catalog
AS $olivares_directory_writer$
` + postgresDirectoryWriterGuardBody + `
$olivares_directory_writer$`
}

func postgresDirectoryWriterTriggerDDL(table string) string {
	return fmt.Sprintf(`CREATE TRIGGER %s_directory_writer_guard
BEFORE INSERT OR UPDATE OR DELETE ON public.%s
FOR EACH ROW EXECUTE FUNCTION public.%s()`, table, table, dialect.DirectoryWriterGuardFunction)
}

func postgresDirectoryWriterTriggerAlwaysDDL(table string) string {
	return fmt.Sprintf("ALTER TABLE ONLY public.%s ENABLE ALWAYS TRIGGER %s_directory_writer_guard", table, table)
}

func canonicalPostgresDirectoryWriterDefinition() guardDefinition {
	return guardDefinition{
		Relation: guardRelationForm{Kind: "r", Persistence: "p"},
		Trigger: guardTriggerForm{
			ParentID: "0", Type: 31, ConstrRelID: "0", ConstrIndID: "0", Constraint: "0",
		},
		Function: guardFunctionForm{
			Schema: dialect.EngineSchema, Name: dialect.DirectoryWriterGuardFunction,
			Kind: "f", ReturnTypeSchema: "pg_catalog", ReturnTypeName: "trigger",
			Language: "plpgsql", Variadic: "0", AllArgTypesNull: true,
			ArgModesNull: true, ArgNamesNull: true, ArgDefaultsNull: true,
			Src:      "\n" + postgresDirectoryWriterGuardBody + "\n",
			Volatile: "v", Parallel: "u", Cost: 100, Rows: 0, Support: "0",
			TransformsNull: true, ConfigNull: false,
		},
	}
}

func verifyPostgresDirectoryWriterFunctionConfig(ctx context.Context, tx *sql.Tx) error {
	var exact bool
	err := tx.QueryRowContext(ctx, `SELECT
  p.proconfig IS NOT DISTINCT FROM ARRAY['search_path=pg_catalog']::text[]
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2 AND p.pronargs = 0`,
		dialect.EngineSchema, dialect.DirectoryWriterGuardFunction).Scan(&exact)
	if err != nil {
		return fmt.Errorf("read PostgreSQL writer function proconfig: %w", err)
	}
	if !exact {
		return fmt.Errorf("PostgreSQL writer function proconfig is not exactly search_path=pg_catalog")
	}
	return nil
}

// unexpectedPostgresDirectoryWriterAttachments makes the inventory
// bidirectional. Looking up only the nine expected keys would miss the same
// shared function attached to a tenth table or a reserved trigger name planted
// in another schema; either makes the claimed closed set false.
func unexpectedPostgresDirectoryWriterAttachments(ctx context.Context, tx *sql.Tx) ([]string, error) {
	functionRows, err := tx.QueryContext(ctx, `SELECT n.nspname,
       pg_catalog.pg_get_function_identity_arguments(p.oid), p.prokind::text
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE p.proname = $1
ORDER BY n.nspname, p.oid`, dialect.DirectoryWriterGuardFunction)
	if err != nil {
		return nil, err
	}
	var unexpected []string
	for functionRows.Next() {
		var schema, arguments, kind string
		if err := functionRows.Scan(&schema, &arguments, &kind); err != nil {
			_ = functionRows.Close()
			return nil, err
		}
		if schema != dialect.EngineSchema || arguments != "" || kind != "f" {
			unexpected = append(unexpected,
				fmt.Sprintf("function %s.%s(%s) kind=%s", schema,
					dialect.DirectoryWriterGuardFunction, arguments, kind))
		}
	}
	if err := closeCoreDirectoryRows(functionRows); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT n.nspname, c.relname, t.tgname
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
JOIN pg_catalog.pg_namespace pn ON pn.oid = p.pronamespace
WHERE NOT t.tgisinternal
  AND (t.tgname LIKE '%\_directory\_writer\_guard%' ESCAPE '\'
       OR (pn.nspname = $1 AND p.proname = $2))
ORDER BY n.nspname, c.relname, t.tgname`,
		dialect.EngineSchema, dialect.DirectoryWriterGuardFunction)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(directoryWriterSourceTables))
	for _, table := range directoryWriterSourceTables {
		want[dialect.EngineSchema+"\x00"+table+"\x00"+table+"_directory_writer_guard"] = true
	}
	for rows.Next() {
		var schema, table, name string
		if err := rows.Scan(&schema, &table, &name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if !want[schema+"\x00"+table+"\x00"+name] {
			unexpected = append(unexpected, schema+"."+table+"."+name)
		}
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return nil, err
	}
	return unexpected, nil
}

func reconcilePostgresDirectoryWriterGuards(
	ctx context.Context,
	tx *sql.Tx,
	state directoryWriterControlState,
) error {
	want := canonicalPostgresDirectoryWriterDefinition()
	function, functionPresent, err := projectGuardFunction(ctx, tx,
		dialect.EngineSchema, dialect.DirectoryWriterGuardFunction)
	if err != nil {
		return fmt.Errorf("sqlstore: directory writer reconcile: inspect PostgreSQL function: %w", err)
	}
	functionMissing := !functionPresent
	if functionPresent {
		if diff := guardFunctionDiff(want.Function, function); len(diff) != 0 {
			return fmt.Errorf("sqlstore: directory writer reconcile: %w: PostgreSQL function drift: %v",
				errDirectoryWriterGuardInvalid, diff)
		}
		if err := verifyPostgresDirectoryWriterFunctionConfig(ctx, tx); err != nil {
			return fmt.Errorf("sqlstore: directory writer reconcile: %w: %v",
				errDirectoryWriterGuardInvalid, err)
		}
	}
	unexpected, err := unexpectedPostgresDirectoryWriterAttachments(ctx, tx)
	if err != nil {
		return fmt.Errorf("sqlstore: directory writer reconcile: inventory PostgreSQL attachments: %w", err)
	}
	if len(unexpected) != 0 {
		return fmt.Errorf("sqlstore: directory writer reconcile: %w: unexpected PostgreSQL writer attachments %v",
			errDirectoryWriterGuardInvalid, unexpected)
	}
	var missing []string
	for _, table := range directoryWriterSourceTables {
		key := guardKey{Schema: dialect.EngineSchema, Relation: table, Trigger: table + "_directory_writer_guard"}
		row, err := projectGuardCatalogRow(ctx, tx, key)
		if err != nil {
			return err
		}
		if !row.RelationExists {
			return fmt.Errorf("sqlstore: directory writer reconcile: %w: PostgreSQL source relation %q is absent",
				errDirectoryWriterGuardInvalid, table)
		}
		if !row.GuardExists {
			missing = append(missing, table)
			continue
		}
		if !row.FunctionExists {
			return fmt.Errorf("sqlstore: directory writer reconcile: %w: PostgreSQL trigger on %q does not invoke a legible function",
				errDirectoryWriterGuardInvalid, table)
		}
		if diff := guardDefinitionDiff(want, row.definition()); len(diff) != 0 {
			return fmt.Errorf("sqlstore: directory writer reconcile: %w: PostgreSQL trigger on %q drift: %v",
				errDirectoryWriterGuardInvalid, table, diff)
		}
		if row.EnableState != string(dialect.TriggerFiresAlways) {
			return fmt.Errorf("sqlstore: directory writer reconcile: %w: PostgreSQL trigger on %q has enable state %q, want ALWAYS",
				errDirectoryWriterGuardInvalid, table, row.EnableState)
		}
	}
	if state.Mode == directoryWriterEnforced && (functionMissing || len(missing) != 0) {
		return fmt.Errorf("sqlstore: directory writer reconcile: %w: enforced control has function_missing=%t trigger_tables_missing=%v",
			errDirectoryWriterGuardInvalid, functionMissing, missing)
	}
	if functionMissing {
		if _, err := tx.ExecContext(ctx, postgresDirectoryWriterFunctionDDL()); err != nil {
			return fmt.Errorf("sqlstore: directory writer reconcile: create PostgreSQL function: %w", err)
		}
	}
	for _, table := range missing {
		if _, err := tx.ExecContext(ctx, postgresDirectoryWriterTriggerDDL(table)); err != nil {
			return fmt.Errorf("sqlstore: directory writer reconcile: create PostgreSQL trigger on %q: %w", table, err)
		}
		if _, err := tx.ExecContext(ctx, postgresDirectoryWriterTriggerAlwaysDDL(table)); err != nil {
			return fmt.Errorf("sqlstore: directory writer reconcile: enable PostgreSQL trigger ALWAYS on %q: %w", table, err)
		}
	}

	function, functionPresent, err = projectGuardFunction(ctx, tx,
		dialect.EngineSchema, dialect.DirectoryWriterGuardFunction)
	if err != nil || !functionPresent {
		return fmt.Errorf("sqlstore: directory writer reconcile: %w: PostgreSQL function verification present=%t err=%v",
			errDirectoryWriterGuardInvalid, functionPresent, err)
	}
	if diff := guardFunctionDiff(want.Function, function); len(diff) != 0 {
		return fmt.Errorf("sqlstore: directory writer reconcile: %w: PostgreSQL function verification drift: %v",
			errDirectoryWriterGuardInvalid, diff)
	}
	if err := verifyPostgresDirectoryWriterFunctionConfig(ctx, tx); err != nil {
		return fmt.Errorf("sqlstore: directory writer reconcile: %w: %v",
			errDirectoryWriterGuardInvalid, err)
	}
	unexpected, err = unexpectedPostgresDirectoryWriterAttachments(ctx, tx)
	if err != nil || len(unexpected) != 0 {
		return fmt.Errorf("sqlstore: directory writer reconcile: %w: PostgreSQL attachment verification unexpected=%v err=%v",
			errDirectoryWriterGuardInvalid, unexpected, err)
	}
	for _, table := range directoryWriterSourceTables {
		key := guardKey{Schema: dialect.EngineSchema, Relation: table, Trigger: table + "_directory_writer_guard"}
		row, err := projectGuardCatalogRow(ctx, tx, key)
		if err != nil {
			return err
		}
		diff := guardDefinitionDiff(want, row.definition())
		if !row.RelationExists || !row.GuardExists || !row.FunctionExists ||
			row.EnableState != string(dialect.TriggerFiresAlways) || len(diff) != 0 {
			return fmt.Errorf("sqlstore: directory writer reconcile: %w: PostgreSQL trigger verification on %q relation=%t trigger=%t function=%t state=%q drift=%v",
				errDirectoryWriterGuardInvalid, table, row.RelationExists,
				row.GuardExists, row.FunctionExists, row.EnableState, diff)
		}
	}
	return nil
}

func reconcilePostgresDirectoryWriterControlACL(
	ctx context.Context,
	tx *sql.Tx,
	hardened bool,
	roles guardRoles,
) error {
	// Trigger functions are executable by PUBLIC by default. Revoke that default
	// in every topology so the separate BYPASSRLS enumeration witness remains
	// genuinely read-only during activation. The single-role application still
	// owns the SECURITY INVOKER function and therefore retains its declared raw
	// capability; this revoke does not mislabel that topology as hardened.
	if err := requirePostgresDirectoryWriterFunctionAdministration(ctx, tx); err != nil {
		return fmt.Errorf("sqlstore: directory writer ACL: %w", err)
	}
	function := quoteIdent(dialect.EngineSchema) + "." +
		quoteIdent(dialect.DirectoryWriterGuardFunction) + "()"
	if _, err := tx.ExecContext(ctx,
		"REVOKE EXECUTE ON FUNCTION "+function+" FROM PUBLIC",
	); err != nil {
		return fmt.Errorf("sqlstore: directory writer ACL reconcile PUBLIC function execute: %w", err)
	}
	if !hardened {
		return nil
	}
	if !roles.App.Known || roles.App.Role == "" {
		return fmt.Errorf("sqlstore: directory writer ACL: application role is unresolved")
	}
	tables := append([]string{dialect.DirectoryWriterControlTable}, directoryWriterSourceTables...)
	if err := requireTableOwnership(ctx, tx, tables); err != nil {
		return fmt.Errorf("sqlstore: directory writer ACL: %w", err)
	}
	target := quoteIdent(dialect.EngineSchema) + "." + quoteIdent(dialect.DirectoryWriterControlTable)
	sources := make([]string, 0, len(directoryWriterSourceTables))
	for _, table := range directoryWriterSourceTables {
		sources = append(sources, quoteIdent(dialect.EngineSchema)+"."+quoteIdent(table))
	}
	sourceList := strings.Join(sources, ", ")
	app := quoteIdent(roles.App.Role)
	for _, stmt := range []string{
		"REVOKE ALL PRIVILEGES ON TABLE " + target + " FROM PUBLIC",
		"REVOKE ALL PRIVILEGES ON TABLE " + target + " FROM " + app,
		"GRANT SELECT ON TABLE " + target + " TO " + app,
		"REVOKE TRIGGER, TRUNCATE ON TABLE " + sourceList + " FROM PUBLIC",
		"REVOKE TRIGGER, TRUNCATE ON TABLE " + sourceList + " FROM " + app,
		"REVOKE EXECUTE ON FUNCTION " + function + " FROM " + app,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlstore: directory writer ACL reconcile: %w", err)
		}
	}
	if err := verifyPostgresDirectoryWriterACLForRole(ctx, tx, roles.App.Role); err != nil {
		return err
	}
	if err := verifyPostgresDirectoryWriterRoleClosure(ctx, tx, roles.App.Role); err != nil {
		return err
	}
	return nil
}

func requirePostgresDirectoryWriterFunctionAdministration(ctx context.Context, q dialect.Querier) error {
	rows, err := q.QueryContext(ctx, `SELECT pg_catalog.pg_get_userbyid(p.proowner),
       pg_catalog.pg_has_role(current_user, p.proowner, 'USAGE')
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2 AND p.pronargs = 0`,
		dialect.EngineSchema, dialect.DirectoryWriterGuardFunction)
	if err != nil {
		return fmt.Errorf("inspect function ownership: %w", err)
	}
	defer rows.Close()
	var count int
	for rows.Next() {
		count++
		var owner string
		var canAdminister bool
		if err := rows.Scan(&owner, &canAdminister); err != nil {
			return fmt.Errorf("inspect function ownership: %w", err)
		}
		if !canAdminister {
			return fmt.Errorf("function %s.%s is owned by %q, which the migration role cannot administer",
				dialect.EngineSchema, dialect.DirectoryWriterGuardFunction, owner)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect function ownership: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("function %s.%s ownership projection returned %d rows, want 1",
			dialect.EngineSchema, dialect.DirectoryWriterGuardFunction, count)
	}
	return nil
}

func verifyPostgresDirectoryWriterACLForRole(
	ctx context.Context,
	q directoryWriterACLQuerier,
	appRole string,
) error {
	var owns, selectTable, insertTable, updateTable, deleteTable, truncateTable bool
	var referencesTable, triggerTable bool
	var insertColumn, updateColumn, referencesColumn bool
	err := q.QueryRowContext(ctx, `SELECT c.relowner = a.oid,
  pg_catalog.has_table_privilege(a.oid, c.oid, 'SELECT'),
  pg_catalog.has_table_privilege(a.oid, c.oid, 'INSERT'),
  pg_catalog.has_table_privilege(a.oid, c.oid, 'UPDATE'),
  pg_catalog.has_table_privilege(a.oid, c.oid, 'DELETE'),
  pg_catalog.has_table_privilege(a.oid, c.oid, 'TRUNCATE'),
  pg_catalog.has_table_privilege(a.oid, c.oid, 'REFERENCES'),
  pg_catalog.has_table_privilege(a.oid, c.oid, 'TRIGGER'),
  pg_catalog.has_any_column_privilege(a.oid, c.oid, 'INSERT'),
  pg_catalog.has_any_column_privilege(a.oid, c.oid, 'UPDATE'),
  pg_catalog.has_any_column_privilege(a.oid, c.oid, 'REFERENCES')
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
CROSS JOIN pg_catalog.pg_roles a
WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind = 'r' AND a.rolname = $3`,
		dialect.EngineSchema, dialect.DirectoryWriterControlTable, appRole).Scan(
		&owns, &selectTable, &insertTable, &updateTable, &deleteTable, &truncateTable,
		&referencesTable, &triggerTable, &insertColumn, &updateColumn, &referencesColumn)
	if err != nil {
		return fmt.Errorf("sqlstore: directory writer ACL verification for role %q: %w", appRole, err)
	}
	if owns || !selectTable || insertTable || updateTable || deleteTable || truncateTable ||
		referencesTable || triggerTable || insertColumn || updateColumn || referencesColumn {
		return fmt.Errorf("sqlstore: refusing to start: directory writer split-owner control ACL for role %q is not non-owner SELECT-only: owner=%t select=%t table=[insert:%t update:%t delete:%t truncate:%t references:%t trigger:%t] column=[insert:%t update:%t references:%t]",
			appRole, owns, selectTable, insertTable, updateTable, deleteTable,
			truncateTable, referencesTable, triggerTable, insertColumn, updateColumn,
			referencesColumn)
	}
	major, err := postgresMajorVia(ctx, q)
	if err != nil {
		return fmt.Errorf("sqlstore: directory writer ACL verification: %w", err)
	}
	if major >= 17 {
		var maintain bool
		if err := q.QueryRowContext(ctx, `SELECT pg_catalog.has_table_privilege(a.oid, c.oid, 'MAINTAIN')
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
CROSS JOIN pg_catalog.pg_roles a
WHERE n.nspname = $1 AND c.relname = $2 AND a.rolname = $3`,
			dialect.EngineSchema, dialect.DirectoryWriterControlTable, appRole).Scan(&maintain); err != nil {
			return fmt.Errorf("sqlstore: directory writer ACL verification for role %q MAINTAIN: %w", appRole, err)
		}
		if maintain {
			return fmt.Errorf("sqlstore: refusing to start: directory writer split-owner role %q holds MAINTAIN on the control", appRole)
		}
	}

	list, args := tableParams([]any{dialect.EngineSchema, appRole}, directoryWriterSourceTables)
	// #nosec G202 -- list is placeholder-only output from tableParams.
	rows, err := q.QueryContext(ctx, `SELECT c.relname, c.relowner = a.oid,
       pg_catalog.has_table_privilege(a.oid, c.oid, 'TRIGGER'),
       pg_catalog.has_table_privilege(a.oid, c.oid, 'TRUNCATE')
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
CROSS JOIN pg_catalog.pg_roles a
WHERE n.nspname = $1 AND a.rolname = $2 AND c.relkind IN ('r','p')
  AND c.relname IN (`+list+`)
ORDER BY c.relname`, args...)
	if err != nil {
		return fmt.Errorf("sqlstore: directory writer ACL verification for role %q sources: %w", appRole, err)
	}
	seen := 0
	for rows.Next() {
		seen++
		var table string
		var sourceOwner, canTrigger, canTruncate bool
		if err := rows.Scan(&table, &sourceOwner, &canTrigger, &canTruncate); err != nil {
			_ = rows.Close()
			return fmt.Errorf("sqlstore: directory writer ACL verification for role %q sources: %w", appRole, err)
		}
		if sourceOwner || canTrigger || canTruncate {
			_ = rows.Close()
			return fmt.Errorf("sqlstore: refusing to start: directory writer split-owner role %q can administer source %q: owner=%t trigger=%t truncate=%t",
				appRole, table, sourceOwner, canTrigger, canTruncate)
		}
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return fmt.Errorf("sqlstore: directory writer ACL verification for role %q sources: %w", appRole, err)
	}
	if seen != len(directoryWriterSourceTables) {
		return fmt.Errorf("sqlstore: directory writer ACL verification for role %q projected %d source rows, want %d",
			appRole, seen, len(directoryWriterSourceTables))
	}

	var ownsFunction, executesFunction bool
	if err := q.QueryRowContext(ctx, `SELECT p.proowner = a.oid,
       pg_catalog.has_function_privilege(a.oid, p.oid, 'EXECUTE')
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
CROSS JOIN pg_catalog.pg_roles a
WHERE n.nspname = $1 AND p.proname = $2 AND p.pronargs = 0 AND a.rolname = $3`,
		dialect.EngineSchema, dialect.DirectoryWriterGuardFunction, appRole).
		Scan(&ownsFunction, &executesFunction); err != nil {
		return fmt.Errorf("sqlstore: directory writer ACL verification for role %q function: %w", appRole, err)
	}
	if ownsFunction || executesFunction {
		return fmt.Errorf("sqlstore: refusing to start: directory writer split-owner role %q can administer writer function: owner=%t execute=%t",
			appRole, ownsFunction, executesFunction)
	}
	return nil
}

func verifyPostgresDirectoryWriterRoleClosure(
	ctx context.Context,
	q dialect.Querier,
	appRole string,
) error {
	major, err := postgresMajorVia(ctx, q)
	if err != nil {
		return fmt.Errorf("sqlstore: directory writer role closure: %w", err)
	}
	tables := append([]string{dialect.DirectoryWriterControlTable}, directoryWriterSourceTables...)
	list, args := tableParams([]any{dialect.EngineSchema, appRole}, tables)
	// #nosec G202 -- list and reachability fragments are closed placeholder/SQL constants.
	query := guardReachableCTE(major) + `SELECT r.rolname, r.rolsuper, r.rolcreaterole,
       c.relname, c.relowner = r.oid,
       pg_catalog.has_table_privilege(r.oid, c.oid, 'INSERT'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'UPDATE'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'DELETE'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'TRUNCATE'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'REFERENCES'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'TRIGGER'),
       p.proowner = r.oid,
       pg_catalog.has_function_privilege(r.oid, p.oid, 'EXECUTE')
FROM pg_catalog.pg_roles r
CROSS JOIN pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
CROSS JOIN pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace pn ON pn.oid = p.pronamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p') AND c.relname IN (` + list + `)
  AND pn.nspname = $1 AND p.proname = '` + dialect.DirectoryWriterGuardFunction + `' AND p.pronargs = 0
  AND r.rolname <> $2 AND ` + guardRoleReachability(major) + `
ORDER BY r.rolname, c.relname`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("sqlstore: directory writer role closure: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var role, table string
		var super, createRole, ownsTable, ins, upd, del, trunc, refs, trigger bool
		var ownsFunction, executeFunction bool
		if err := rows.Scan(&role, &super, &createRole, &table, &ownsTable,
			&ins, &upd, &del, &trunc, &refs, &trigger, &ownsFunction, &executeFunction); err != nil {
			return fmt.Errorf("sqlstore: directory writer role closure: %w", err)
		}
		var reason string
		switch {
		case super:
			reason = "is superuser"
		case createRole && major < 16:
			reason = "holds CREATEROLE on PostgreSQL " + fmt.Sprint(major)
		case ownsFunction:
			reason = "owns the writer function"
		case executeFunction:
			reason = "can execute the writer function"
		case ownsTable:
			reason = "owns " + table
		case table == dialect.DirectoryWriterControlTable && (ins || upd || del || trunc || refs || trigger):
			reason = "holds non-SELECT control privileges on " + table
		case table != dialect.DirectoryWriterControlTable && (trunc || trigger):
			reason = "holds TRUNCATE or TRIGGER on source " + table
		}
		if reason != "" {
			return fmt.Errorf("sqlstore: refusing to start: directory writer role %q is reachable from application role %q and %s",
				role, appRole, reason)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlstore: directory writer role closure: %w", err)
	}
	if major < 16 {
		createRole, err := guardRoleHasCreateRole(ctx, q, appRole)
		if err != nil {
			return err
		}
		if createRole {
			return fmt.Errorf("sqlstore: refusing to start: directory writer application role %q holds CREATEROLE on PostgreSQL %d",
				appRole, major)
		}
	}
	return nil
}

func verifyPostgresDirectoryWriterControlACL(
	ctx context.Context,
	appAuthority directoryWriterACLQuerier,
	dia dialect.Dialect,
	hardened bool,
	roles guardRoles,
) error {
	if dia.Name() != store.EnginePostgres || !hardened {
		return nil
	}
	if !roles.App.Known || roles.App.Role == "" {
		return fmt.Errorf("sqlstore: directory writer ACL verification: application role is unresolved")
	}
	if err := verifyPostgresDirectoryWriterACLForRole(ctx, appAuthority, roles.App.Role); err != nil {
		return err
	}
	return verifyPostgresDirectoryWriterRoleClosure(ctx, appAuthority, roles.App.Role)
}
