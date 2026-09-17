// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/olivaresai/olivares/core/model"
	"reflect"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

const coreLineageMigrationVersion = 8

func coreLineageMigration(dia dialect.Dialect) migrate.Migration {
	return migrate.Migration{Version: coreLineageMigrationVersion, Name: "lineage_authority", Exec: func(ctx context.Context, tx *sql.Tx) error {
		for _, desc := range lineageDescriptors() {
			for _, stmt := range dia.CreateTableStmts(desc) {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
		}
		for _, object := range lineageControlDDL(dia) {
			if _, err := tx.ExecContext(ctx, object.statement); err != nil {
				return err
			}
		}
		// #nosec G202 -- the only interpolated text is directoryWriterRelation(dia, lineageControlTable): the package constant "core_lineage_control" in engine identifier quoting; the inserted value is the literal 1.
		_, err := tx.ExecContext(ctx, "INSERT INTO "+directoryWriterRelation(dia, lineageControlTable)+"(singleton) VALUES (1)")
		return err
	}}
}

// Guard installation follows descriptor reconciliation, because upgrades may
// have acquired a source table after the original core v2. A committed ready
// marker closes that one installation window; later absence/drift is refused.
func reconcileLineageGuards(ctx context.Context, db dialect.Execer, dia dialect.Dialect, hardened bool, roles guardRoles) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if err := verifyLineageRelations(ctx, tx, dia); err != nil {
		return err
	}
	var ready bool
	if err := tx.QueryRowContext(ctx, "SELECT guards_ready FROM "+directoryWriterRelation(dia, lineageControlTable)+" WHERE singleton = 1").Scan(&ready); err != nil {
		return err
	}
	for _, object := range lineageGuardObjects(dia) {
		present, err := verifyLineageGuard(ctx, tx, dia, object)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if ready {
			return lineageUnavailable("missing guard "+object.name, nil)
		}
		if _, err := tx.ExecContext(ctx, object.statement); err != nil {
			return lineageUnavailable("install "+object.name, err)
		}
		if dia.Name() == store.EnginePostgres && object.body == "" {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE public."+object.table+" ENABLE ALWAYS TRIGGER "+object.name); err != nil {
				return err
			}
		}
		if present, err := verifyLineageGuard(ctx, tx, dia, object); err != nil || !present {
			return lineageUnavailable("new guard did not verify "+object.name, err)
		}
	}
	if err := verifyLineageSources(ctx, tx, dia); err != nil {
		return err
	}
	if !ready {
		// #nosec G202 -- both interpolations are closed: directoryWriterRelation over the lineageControlTable constant, and lineageSQLTrue's per-engine "true"/"1" literal.
		if _, err := tx.ExecContext(ctx, "UPDATE "+directoryWriterRelation(dia, lineageControlTable)+" SET guards_ready = "+lineageSQLTrue(dia)); err != nil {
			return err
		}
	}
	if dia.Name() == store.EnginePostgres {
		if err := reconcileLineageACL(ctx, tx, dia, hardened, roles); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func verifyLineageRelations(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) error {
	for _, desc := range lineageDescriptors() {
		shape, found, err := inspectCoreDirectoryRelation(ctx, tx, dia, desc.Table)
		if err != nil || !found {
			return lineageUnavailable("missing epoch relation "+desc.Table, err)
		}
		if err := verifyCoreDirectoryRelationShape(dia, desc, shape); err != nil {
			return err
		}
		if err := verifyCoreDirectoryRelationContract(ctx, tx, dia, desc); err != nil {
			return err
		}
	}
	for _, object := range lineageControlDDL(dia) {
		if dia.Name() == store.EngineSQLite {
			definition := strings.Replace(object.statement, "main.", "", 1)
			if err := verifySQLiteDirectoryWriterRawTable(ctx, tx, object.name, definition); err != nil {
				return err
			}
		} else if err := verifyPostgresLineageControl(ctx, tx, object); err != nil {
			return err
		}
	}
	return nil
}

func verifyPostgresLineageControl(ctx context.Context, tx *sql.Tx, object lineageSQLObject) (retErr error) {
	const savepoint = "lineage_contract_probe"
	const probe = "olv_lineage_probe"
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		return err
	}
	defer func() {
		_, rollback := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint)
		_, release := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint)
		retErr = errors.Join(retErr, rollback, release)
	}()
	if _, err := tx.ExecContext(ctx, strings.ReplaceAll(object.statement, object.name, probe)); err != nil {
		return err
	}
	want, err := projectPostgresCoreDirectoryContract(ctx, tx, probe, object.name)
	if err != nil {
		return err
	}
	got, err := projectPostgresCoreDirectoryContract(ctx, tx, object.name, object.name)
	if err != nil {
		return err
	}
	return postgresCoreDirectoryContractDifference(want, got)
}

func verifyLineageGuard(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, object lineageSQLObject) (bool, error) {
	if dia.Name() == store.EngineSQLite {
		var actual string
		err := tx.QueryRowContext(ctx, "SELECT sql FROM main.sqlite_master WHERE type='trigger' AND name=? AND tbl_name=?", object.name, object.table).Scan(&actual)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if actual != strings.Replace(object.statement, "main.", "", 1) {
			return false, lineageUnavailable("guard drift "+object.name, nil)
		}
		return true, nil
	}
	if object.body != "" {
		var body, language, volatility, config, owner, current, args, result string
		var security bool
		err := tx.QueryRowContext(ctx, `SELECT p.prosrc, l.lanname, p.provolatile::text, p.prosecdef, COALESCE(p.proconfig::text, ''),pg_catalog.pg_get_userbyid(p.proowner),current_user,pg_catalog.pg_get_function_identity_arguments(p.oid),pg_catalog.pg_get_function_result(p.oid)
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
JOIN pg_catalog.pg_language l ON l.oid=p.prolang
WHERE n.nspname='public' AND p.proname=$1 AND p.prokind='f'`, object.name).Scan(&body, &language, &volatility, &security, &config, &owner, &current, &args, &result)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		var overloads int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname=$1", object.name).Scan(&overloads); err != nil {
			return false, err
		}
		if overloads != 1 {
			return false, lineageUnavailable("overloaded lineage routine", nil)
		}
		if body != object.body || language != "plpgsql" || volatility != "v" || !security || config != "{search_path=pg_catalog}" || owner != current || args != object.arguments || result != object.result {
			return false, lineageUnavailable("function drift "+object.name, nil)
		}
		return true, nil
	}
	var enabled, function, schema, args, qualifier string
	var typ, nargs int
	err := tx.QueryRowContext(ctx, `SELECT t.tgenabled::text, p.proname, pn.nspname, t.tgtype::int, t.tgnargs::int, pg_catalog.encode(t.tgargs,'hex'), COALESCE(pg_catalog.pg_get_expr(t.tgqual,t.tgrelid),'')
FROM pg_catalog.pg_trigger t JOIN pg_catalog.pg_class c ON c.oid=t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace JOIN pg_catalog.pg_proc p ON p.oid=t.tgfoid
JOIN pg_catalog.pg_namespace pn ON pn.oid=p.pronamespace
WHERE n.nspname='public' AND c.relname=$1 AND t.tgname=$2`, object.table, object.name).Scan(&enabled, &function, &schema, &typ, &nargs, &args, &qualifier)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if enabled != "A" || function != "olivares_"+object.name || schema != "public" || typ != 31 || nargs != 0 || args != "" || qualifier != "" {
		return false, fmt.Errorf("%w: trigger drift %s", store.ErrLineageUnavailable, object.name)
	}
	return true, nil
}

// A tracked v8 is checked before additive reconciliation can recreate a lost
// authority relation. The ready marker permits only the initial guard install.
func preflightLineage(ctx context.Context, db dialect.Execer, dia dialect.Dialect) error {
	columns, err := dia.TableColumns(ctx, db, coreTrackingTable)
	if err != nil || len(columns) == 0 {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var tracked int
	if err := tx.QueryRowContext(ctx, dia.Rebind("SELECT COUNT(*) FROM "+coreTrackingRelation(dia)+" WHERE version=?"), coreLineageMigrationVersion).Scan(&tracked); err != nil {
		return err
	}
	if tracked == 0 {
		return nil
	}
	var name, phase, applied string
	var reverted sql.NullString
	if err := tx.QueryRowContext(ctx, dia.Rebind("SELECT name,phase,applied_at,reverted_at FROM "+coreTrackingRelation(dia)+" WHERE version=?"), coreLineageMigrationVersion).Scan(&name, &phase, &applied, &reverted); err != nil {
		return err
	}
	if name != "lineage_authority" || phase != "expand" || strings.TrimSpace(applied) == "" || reverted.Valid {
		return lineageUnavailable("noncanonical v8 tracking record", nil)
	}
	if err := verifyLineageRelations(ctx, tx, dia); err != nil {
		return err
	}
	var ready bool
	if err := tx.QueryRowContext(ctx, "SELECT guards_ready FROM "+directoryWriterRelation(dia, lineageControlTable)+" WHERE singleton=1").Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return nil
	}
	for _, object := range lineageGuardObjects(dia) {
		present, err := verifyLineageGuard(ctx, tx, dia, object)
		if err != nil || !present {
			return lineageUnavailable("tracked guard missing or changed: "+object.name, err)
		}
	}
	return verifyLineageSources(ctx, tx, dia)
}

// Extra RLS policies can hide dependencies; extra source triggers can alter an
// authority column after our guard observes it. Both inventories are closed.
func verifyLineageSources(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) error {
	writerSpecs := sqliteDirectoryWriterGuardSpecs()
	if dia.Name() == store.EngineSQLite {
		tracked, err := coreVersionIsTracked(ctx, tx, dia, coreUserAuthorityMigrationVersion)
		if err != nil {
			return err
		}
		if !tracked {
			writerSpecs = sqliteDirectoryWriterGuardSpecsFor(legacyDirectoryWriterSourceTables, legacySQLiteDirectoryWriterGuardBody)
		}
	}
	for _, rel := range lineageRelations {
		expected := map[string]string{}
		if dia.Name() == store.EngineSQLite {
			for _, stmt := range dia.CreateTableStmts(model.EntityDescriptor{Table: rel.table}) {
				if strings.HasPrefix(stmt, "CREATE TRIGGER ") {
					expected[strings.Fields(stmt)[2]] = stmt
				}
			}
			for _, spec := range writerSpecs {
				if spec.Table == rel.table {
					expected[spec.Name] = spec.Definition
				}
			}
		} else {
			if err := verifyLineageSourceRLS(ctx, tx, dia, rel.table); err != nil {
				return err
			}
			for _, table := range directoryWriterSourceTables {
				if table == rel.table {
					expected[table+"_directory_writer_guard"] = ""
				}
			}
		}
		for _, object := range lineageGuardObjects(dia) {
			if object.table == rel.table && (dia.Name() == store.EngineSQLite || object.body == "") {
				expected[object.name] = strings.Replace(object.statement, "main.", "", 1)
			}
		}
		var rows *sql.Rows
		var err error
		if dia.Name() == store.EngineSQLite {
			rows, err = tx.QueryContext(ctx, "SELECT name,sql FROM main.sqlite_master WHERE type='trigger' AND tbl_name=?", rel.table)
		} else {
			rows, err = tx.QueryContext(ctx, `SELECT t.tgname,'' FROM pg_catalog.pg_trigger t JOIN pg_catalog.pg_class c ON c.oid=t.tgrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname=$1 AND NOT t.tgisinternal`, rel.table)
		}
		if err != nil {
			return err
		}
		for rows.Next() {
			var name, definition string
			if err := rows.Scan(&name, &definition); err != nil {
				rows.Close()
				return err
			}
			want, exists := expected[name]
			if !exists || (dia.Name() == store.EngineSQLite && definition != want) {
				rows.Close()
				return lineageUnavailable("source trigger census drift: "+rel.table+"/"+name, nil)
			}
			delete(expected, name)
		}
		if err := closeCoreDirectoryRows(rows); err != nil {
			return err
		}
		if len(expected) != 0 {
			return lineageUnavailable("source trigger census incomplete: "+rel.table, nil)
		}
	}
	return nil
}

func verifyLineageSourceRLS(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, table string) (retErr error) {
	const savepoint = "lineage_source_contract"
	const probe = "olv_lineage_source_probe"
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		return err
	}
	defer func() {
		_, a := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint)
		_, b := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint)
		retErr = errors.Join(retErr, a, b)
	}()
	for _, stmt := range dia.CreateTableStmts(model.EntityDescriptor{Table: probe}) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	want, err := projectPostgresCoreDirectoryContract(ctx, tx, probe, table)
	if err != nil {
		return err
	}
	got, err := projectPostgresCoreDirectoryContract(ctx, tx, table, table)
	if err != nil {
		return err
	}
	if want.Relation != got.Relation || !reflect.DeepEqual(want.Policies, got.Policies) || !reflect.DeepEqual(want.Rules, got.Rules) {
		return lineageUnavailable("source tenant policy or relation drift: "+table, nil)
	}
	return nil
}
