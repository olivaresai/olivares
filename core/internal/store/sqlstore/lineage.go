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
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const lineageWriterTable = "core_lineage_writer"
const lineageTouchedTable = "core_lineage_touched"
const lineageControlTable = "core_lineage_control"
const lineageSeededTable = "core_lineage_seeded"

func lineageUnavailable(what string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s", store.ErrLineageUnavailable, what)
	}
	return fmt.Errorf("%w: %s: %w", store.ErrLineageUnavailable, what, err)
}

func (sc *tenantScope) ReadLineageEpoch(ctx context.Context, kind model.Kind) (store.AuthorizationFactRef, error) {
	for _, relation := range lineageRelations {
		if relation.kind != kind {
			continue
		}
		desc := relation.descriptor()
		var id, tenant string
		var version int64
		q := sc.s.dia.Rebind("SELECT id, tenant_id, version FROM " + directoryWriterRelation(sc.s.dia, desc.Table) + " WHERE tenant_id = ?")
		err := sc.tx.QueryRowContext(ctx, q, sc.tenant.String()).Scan(&id, &tenant, &version)
		if err != nil {
			return store.AuthorizationFactRef{}, lineageUnavailable("read "+relation.table, err)
		}
		epoch := model.AuthorizationEpoch{BaseFields: model.BaseFields{ID: model.ID(id), TenantID: model.TenantID(tenant), Version: version}}
		if err := epoch.Validate(); err != nil || epoch.TenantID != sc.tenant {
			return store.AuthorizationFactRef{}, lineageUnavailable("noncanonical "+relation.table, err)
		}
		return store.AuthorizationFactRef{Kind: desc.Kind, ID: epoch.ID, Version: epoch.Version}, nil
	}
	return store.AuthorizationFactRef{}, lineageUnavailable("unsupported relation", nil)
}

func insertLineageEpochs(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, tenant model.TenantID) error {
	if dia.Name() == store.EnginePostgres {
		_, err := tx.ExecContext(ctx, "SELECT public.olivares_lineage_seed($1)", tenant.String())
		return err
	}
	now, err := directoryTransactionNow(ctx, tx, dia)
	if err != nil {
		return err
	}
	if err := (model.AuthorizationEpoch{BaseFields: model.BaseFields{ID: model.ID(tenant), TenantID: tenant, Version: 1}}).Validate(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO main.core_lineage_seeded(tenant_id) VALUES (?)", tenant.String()); err != nil {
		return lineageUnavailable("tenant generation already seeded", err)
	}
	for _, relation := range lineageRelations {
		desc := relation.descriptor()
		q := dia.Rebind("INSERT INTO " + directoryWriterRelation(dia, desc.Table) + "(id, tenant_id, created_at, updated_at, version) VALUES (?, ?, ?, ?, 1)")
		if _, err := tx.ExecContext(ctx, q, tenant.String(), tenant.String(), now.String(), now.String()); err != nil {
			return lineageUnavailable("seed "+relation.table, err)
		}
	}
	return nil
}

// Reconciliation only creates previously uncovered tenants while the durable
// control is staged. Once complete coverage was recorded, losing any epoch is
// corruption, not an opportunity to reset an old fact to generation one.
func reconcileLineageEpochs(ctx context.Context, db, adminDB *sql.DB, dia dialect.Dialect, adminRole guardRoleFact) error {
	if dia.Name() == store.EnginePostgres && db == adminDB {
		return nil
	}
	tx, err := db.BeginTx(ctx, directoryWriterTxOptions(dia))
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	lineage := newLineageWriteTracker(tx, dia, true)
	if err := lineage.start(ctx, ""); err != nil {
		return err
	}
	if _, err := acquireDirectoryWriter(ctx, tx, dia); err != nil {
		return err
	}
	inventory, err := openDirectoryReconcileInventory(ctx, tx, adminDB, dia, adminRole)
	if err != nil {
		return err
	}
	defer inventory.close()
	tenants, err := enumerateDirectoryTenants(ctx, inventory.queryer, dia)
	if err != nil {
		return err
	}
	var complete bool
	if err := tx.QueryRowContext(ctx, "SELECT complete FROM "+directoryWriterRelation(dia, lineageControlTable)+" WHERE singleton = 1").Scan(&complete); err != nil {
		return err
	}
	for _, tenant := range tenants {
		if err := bindDirectoryTenant(ctx, tx, dia, tenant); err != nil {
			return err
		}
		found := 0
		for _, relation := range lineageRelations {
			var id string
			var version int64
			q := dia.Rebind("SELECT id, version FROM " + directoryWriterRelation(dia, relation.descriptor().Table) + " WHERE tenant_id = ?")
			err := tx.QueryRowContext(ctx, q, tenant.String()).Scan(&id, &version)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if id != tenant.String() || version < 1 {
				return lineageUnavailable("invalid backfill witness", nil)
			}
			found++
		}
		if found == len(lineageRelations) {
			var seeded int
			if err := tx.QueryRowContext(ctx, dia.Rebind("SELECT count(*) FROM "+directoryWriterRelation(dia, lineageSeededTable)+" WHERE tenant_id=?"), tenant.String()).Scan(&seeded); err != nil {
				return err
			}
			if seeded != 1 {
				return lineageUnavailable("covered tenant has no durable seed witness", nil)
			}
			continue
		}
		if found != 0 || complete {
			return lineageUnavailable("missing or partial covered tenant", nil)
		}
		if err := insertLineageEpochs(ctx, tx, dia, tenant); err != nil {
			return err
		}
	}
	completeSQL := "UPDATE main.core_lineage_control SET complete = 1"
	if dia.Name() == store.EnginePostgres {
		completeSQL = "SELECT public.olivares_lineage_complete()"
	}
	if _, err := tx.ExecContext(ctx, completeSQL); err != nil {
		return err
	}
	if err := restoreSystemDirectoryBaseline(ctx, tx, dia); err != nil {
		return err
	}
	if err := inventory.commit(); err != nil {
		return err
	}
	return tx.Commit()
}

func lineageSQLTrue(dia dialect.Dialect) string {
	if dia.Name() == store.EnginePostgres {
		return "true"
	}
	return "1"
}

type lineageSQLObject struct {
	name, table, statement, body, arguments, result string
	exposed                                         bool
}

func lineageControlDDL(dia dialect.Dialect) []lineageSQLObject {
	qual := func(table string) string { return directoryWriterRelation(dia, table) }
	fkWriter := lineageWriterTable
	if dia.Name() == store.EnginePostgres {
		fkWriter = qual(lineageWriterTable)
	}
	boolType, boolDefault := "INTEGER", "0"
	if dia.Name() == store.EnginePostgres {
		boolType, boolDefault = "boolean", "false"
	}
	return []lineageSQLObject{
		{name: lineageSeededTable, statement: "CREATE TABLE " + qual(lineageSeededTable) + " (tenant_id TEXT NOT NULL PRIMARY KEY)"},
		{name: lineageControlTable, statement: "CREATE TABLE " + qual(lineageControlTable) + " (singleton INTEGER PRIMARY KEY CHECK(singleton = 1), complete " + boolType + " NOT NULL DEFAULT " + boolDefault + ", guards_ready " + boolType + " NOT NULL DEFAULT " + boolDefault + ")"},
		{name: lineageWriterTable, statement: "CREATE TABLE " + qual(lineageWriterTable) + " (writer_id TEXT NOT NULL CHECK(length(writer_id) > 0), tenant_id TEXT NOT NULL, PRIMARY KEY(writer_id,tenant_id), UNIQUE(tenant_id))"},
		{name: lineageTouchedTable, statement: "CREATE TABLE " + qual(lineageTouchedTable) + " (writer_id TEXT NOT NULL, tenant_id TEXT NOT NULL, relation_name TEXT NOT NULL, PRIMARY KEY(writer_id,tenant_id,relation_name), FOREIGN KEY(writer_id,tenant_id) REFERENCES " + fkWriter + "(writer_id,tenant_id) ON DELETE CASCADE)"},
	}
}

func lineageGuardObjects(dia dialect.Dialect) []lineageSQLObject {
	var out []lineageSQLObject
	for _, relation := range lineageRelations {
		if dia.Name() == store.EnginePostgres {
			out = append(out, postgresLineageObjects(relation)...)
			continue
		}
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			name := relation.table + "_lineage_" + strings.ToLower(op)
			row := "NEW"
			if op == "DELETE" {
				row = "OLD"
			}
			relevant := "1"
			immutable := ""
			if op == "UPDATE" {
				var tests []string
				for _, col := range relation.columns {
					tests = append(tests, "OLD."+col+" IS NOT NEW."+col)
				}
				relevant = "(" + strings.Join(tests, " OR ") + ")"
				immutable = "SELECT RAISE(ABORT, 'lineage identity is immutable') WHERE OLD.id IS NOT NEW.id OR OLD.tenant_id IS NOT NEW.tenant_id;\n"
			}
			epoch := relation.descriptor().Table
			untouched := "NOT EXISTS (SELECT 1 FROM main." + lineageTouchedTable + " WHERE tenant_id = " + row + ".tenant_id AND writer_id = (SELECT writer_id FROM main.core_lineage_writer WHERE tenant_id = " + row + ".tenant_id) AND relation_name = '" + relation.table + "')"
			body := "BEGIN\n" + immutable +
				"SELECT RAISE(ABORT, 'lineage writer protocol required') WHERE (SELECT COUNT(*) FROM main." + lineageWriterTable + " WHERE tenant_id = " + row + ".tenant_id) <> 1;\n" +
				"SELECT RAISE(ABORT, 'lineage epoch unavailable') WHERE " + relevant + " AND " + untouched + " AND NOT EXISTS (SELECT 1 FROM main." + epoch + " WHERE tenant_id = " + row + ".tenant_id AND id = tenant_id AND typeof(version) = 'integer' AND version >= 1 AND version < 9223372036854775807);\n" +
				"UPDATE " + epoch + " SET version = version + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now') WHERE tenant_id = " + row + ".tenant_id AND " + relevant + " AND " + untouched + ";\n" +
				"INSERT INTO " + lineageTouchedTable + "(writer_id, tenant_id, relation_name) SELECT (SELECT writer_id FROM main.core_lineage_writer WHERE tenant_id = " + row + ".tenant_id), " + row + ".tenant_id, '" + relation.table + "' WHERE " + relevant + " AND " + untouched + ";\nEND"
			statement := "CREATE TRIGGER main." + name + " BEFORE " + op + " ON " + relation.table + "\n" + body
			out = append(out, lineageSQLObject{name: name, table: relation.table, statement: statement, body: body})
		}
	}
	if dia.Name() == store.EnginePostgres {
		out = append(out, postgresLineageRoutines()...)
	}
	return out
}

func postgresLineageObjects(relation lineageRelation) []lineageSQLObject {
	name := relation.table + "_lineage_guard"
	function := "olivares_" + name
	var tests []string
	for _, col := range relation.columns {
		tests = append(tests, "OLD."+col+" IS DISTINCT FROM NEW."+col)
	}
	epoch := relation.descriptor().Table
	body := `DECLARE
  target_tenant text;
  changed boolean := true;
BEGIN
  IF TG_TABLE_SCHEMA <> 'public' OR TG_TABLE_NAME <> '` + relation.table + `' THEN
    RAISE EXCEPTION 'lineage trigger target invalid';
  END IF;
  IF TG_OP = 'UPDATE' THEN
    IF OLD.id IS DISTINCT FROM NEW.id OR OLD.tenant_id IS DISTINCT FROM NEW.tenant_id THEN
      RAISE EXCEPTION 'lineage identity is immutable';
    END IF;
    changed := ` + strings.Join(tests, " OR ") + `;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM public.core_lineage_writer
                 WHERE tenant_id = COALESCE(NEW.tenant_id,OLD.tenant_id) AND writer_id = pg_catalog.txid_current()::text) THEN
    RAISE EXCEPTION 'lineage writer protocol required';
  END IF;
  IF TG_OP = 'DELETE' THEN target_tenant := OLD.tenant_id; ELSE target_tenant := NEW.tenant_id; END IF;
  IF changed AND NOT EXISTS (SELECT 1 FROM public.core_lineage_touched
                            WHERE writer_id = pg_catalog.txid_current()::text AND tenant_id = target_tenant AND relation_name = '` + relation.table + `') THEN
    UPDATE public.` + epoch + ` SET version = version + 1,
        updated_at = pg_catalog.to_char(pg_catalog.clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') || '000Z'
      WHERE tenant_id = target_tenant AND id = tenant_id AND version >= 1 AND version < 9223372036854775807;
    IF NOT FOUND THEN RAISE EXCEPTION 'lineage epoch unavailable'; END IF;
    INSERT INTO public.core_lineage_touched(writer_id, tenant_id, relation_name) VALUES (pg_catalog.txid_current()::text, target_tenant, '` + relation.table + `');
  END IF;
  IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;`
	return []lineageSQLObject{
		{name: function, table: relation.table, body: body, result: "trigger", statement: "CREATE FUNCTION public." + function + "() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $lineage$" + body + "$lineage$"},
		{name: name, table: relation.table, statement: "CREATE TRIGGER " + name + " BEFORE INSERT OR UPDATE OR DELETE ON public." + relation.table + " FOR EACH ROW EXECUTE FUNCTION public." + function + "()"},
	}
}
