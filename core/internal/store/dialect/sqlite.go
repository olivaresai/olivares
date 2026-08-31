// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ScopeTenantTable is the SQLite scope-pin table. A scoped transaction writes
// the bound tenant into it; every tenant table's tripwire triggers compare a
// written/updated/deleted row's tenant_id against it and RAISE(ABORT) on a
// mismatch. An empty pin denotes the privileged System path. It is created by
// the SQLite tenancy migration, not declared as an entity.
const ScopeTenantTable = "_scope_tenant"

// sqliteDialect targets modernc.org/sqlite. SQLite has no row-level security, so
// isolation is achieved by the closed, descriptor-generated query layer (which
// always scopes by tenant_id) plus these tripwire triggers as the write
// backstop. The store runs SQLite single-writer (one connection), so the pin
// table needs no per-connection trickery.
type sqliteDialect struct{}

func (sqliteDialect) Name() store.Engine { return store.EngineSQLite }

// Rebind is the identity: SQLite uses '?' placeholders.
func (sqliteDialect) Rebind(query string) string { return query }

func (sqliteDialect) ColumnType(k model.SQLKind, nullable bool) string {
	var t string
	switch k {
	case model.KindInt, model.KindBool:
		t = "INTEGER"
	case model.KindFloat:
		t = "REAL"
	case model.KindBytes:
		t = "BLOB"
	default: // KindText, KindJSON, KindTimestamp, KindUUID
		t = "TEXT"
	}
	if !nullable {
		t += " NOT NULL"
	}
	return t
}

func (sqliteDialect) BindTenant(ctx context.Context, tx *sql.Tx, tenant model.TenantID) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+ScopeTenantTable); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO "+ScopeTenantTable+"(tenant_id) VALUES(?)", tenant.String())
	return err
}

func (sqliteDialect) ClearTenant(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM "+ScopeTenantTable)
	return err
}

func (sqliteDialect) TenancyStmts() []string {
	return []string{
		// The single-row scope pin checked by every tenant table's tripwire
		// triggers. Not an entity; not tenant-scoped itself.
		"CREATE TABLE " + ScopeTenantTable + " (tenant_id TEXT NOT NULL)",
	}
}

// AuditEventsTable is the name of the hash-chained evidence ledger.
const AuditEventsTable = "audit_events"

func (sqliteDialect) AuditTableStmts() []string {
	t := AuditEventsTable
	stmts := []string{
		`CREATE TABLE ` + t + ` (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  occurred_at TEXT NOT NULL,
  actor TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  action TEXT NOT NULL,
  target_kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  meta TEXT NOT NULL,
  -- meta_blind is the per-record 256-bit blind of the metadata commitment. It is
  -- NULLABLE on purpose, and the NULL is meaningful rather than missing data: a row
  -- sealed before blinding existed commits its metadata under the legacy unblinded
  -- rule, and the column is the discriminator that lets Verify apply the rule each
  -- row was actually sealed under. An append-only ledger cannot restate the hash
  -- rule of rows it already sealed without making a legitimate history look forged.
  -- The CHECK is the at-rest half of the discriminator: exactly two legal states,
  -- NULL or 32 bytes. Without it a zero-length blob is a third state that reads as
  -- len()==0 in Go and would be hashed under the LEGACY rule while the column
  -- claims the row is blinded. The in-flight half is canon.ValidateBlind.
  meta_blind BLOB CHECK (meta_blind IS NULL OR length(meta_blind) = 32),
  payload_hash BLOB,
  prev_hash BLOB NOT NULL,
  hash BLOB NOT NULL,
  sig BLOB,
  UNIQUE(tenant_id, seq)
)`,
		// Insert is tenant-checked; update/delete are blocked (append-only).
		sqliteScopeTriggers(t, true)[0],
		sqliteAppendOnlyTriggers(t)[0],
		sqliteAppendOnlyTriggers(t)[1],
	}
	// The per-tenant chain head (mutable): lets Verify detect tail truncation
	// (deletion of the most-recent events leaves no seq gap). It is
	// tenant-guarded but NOT append-only, since the head advances.
	stmts = append(stmts,
		"CREATE TABLE "+AuditHeadsTable+" (tenant_id TEXT PRIMARY KEY, seq INTEGER NOT NULL, hash BLOB NOT NULL)")
	stmts = append(stmts, sqliteScopeTriggers(AuditHeadsTable, false)...)
	return stmts
}

func (sqliteDialect) AuditSpoolStmts() []string {
	// audit_spool_usage is global mutable bookkeeping, not tenant data and not
	// evidence. It deliberately has neither scope tripwires nor append-only
	// triggers; the single seeded row is updated transactionally with appends.
	stmts := []string{
		"CREATE TABLE " + AuditSpoolUsageTable + " (id INTEGER PRIMARY KEY, bytes BIGINT NOT NULL)",
		"INSERT INTO " + AuditSpoolUsageTable + " (id, bytes) VALUES (1, 0)",
		// audit_spool_gaps is mutable per-tenant integrity bookkeeping. It mirrors
		// audit_heads exactly: tenant tripwires apply, append-only triggers do not.
		"CREATE TABLE " + AuditSpoolGapsTable + " (tenant_id TEXT PRIMARY KEY, dropped BIGINT NOT NULL, first_dropped_at TEXT NOT NULL)",
	}
	return append(stmts, sqliteScopeTriggers(AuditSpoolGapsTable, false)...)
}

// AppendOnlyACLStmts is nil for SQLite: the engine has no roles, so there is no ACL
// to re-assert. Its append-only enforcement is the trigger pair installed with the
// table (sqliteAppendOnlyTriggers), which applies to every connection unconditionally
// — and SQLite has no TRUNCATE statement, the one operation the Postgres ACL leg
// exists to stop.
func (sqliteDialect) AppendOnlyACLStmts([]string) []string { return nil }

// AppendOnlyACLTables is nil for the same reason: there is no ACL layer to maintain,
// so there is no set of tables to maintain it over. SQLite's append-only guard is the
// trigger pair, which the schema self-test covers.
func (sqliteDialect) AppendOnlyACLTables(context.Context, Querier) ([]string, error) {
	return nil, nil
}

// AuditHeadsTable records each tenant's current chain tip.
const AuditHeadsTable = "audit_heads"

func (d sqliteDialect) CreateTableStmts(desc model.EntityDescriptor) []string {
	t := desc.Table
	var cols []string
	cols = append(cols, "id TEXT PRIMARY KEY")
	cols = append(cols, "tenant_id TEXT NOT NULL")
	cols = append(cols, "created_at TEXT NOT NULL")
	cols = append(cols, "updated_at TEXT NOT NULL")
	cols = append(cols, "version INTEGER NOT NULL")
	if desc.SoftDelete {
		cols = append(cols, "deleted_at TEXT")
	}
	for _, f := range desc.Fields {
		cols = append(cols, columnDef(d, f.Name, f.Kind, f.Nullable))
	}
	// Table-level CHECK constraints (core-only, see EntityDescriptor.Checks):
	// the same syntax on both engines.
	for _, c := range desc.Checks {
		cols = append(cols, "CHECK ("+c+")")
	}

	stmts := []string{
		fmt.Sprintf("CREATE TABLE %s (\n  %s\n)", t, strings.Join(cols, ",\n  ")),
		fmt.Sprintf("CREATE INDEX %s_tenant_id_idx ON %s(tenant_id, id)", t, t),
	}
	stmts = append(stmts, indexStmts(desc)...)
	stmts = append(stmts, sqliteScopeTriggers(t, desc.AppendOnly)...)
	if desc.AppendOnly {
		stmts = append(stmts, sqliteAppendOnlyTriggers(t)...)
	}
	return stmts
}

// indexStmts renders secondary indexes for declared Indexed fields and explicit
// IndexSpecs. It is dialect-neutral (plain CREATE [UNIQUE] INDEX).
func indexStmts(desc model.EntityDescriptor) []string {
	var out []string
	for _, f := range desc.Fields {
		if f.Indexed {
			out = append(out, fmt.Sprintf(
				"CREATE INDEX %s_%s_idx ON %s(tenant_id, %s)",
				desc.Table, f.Name, desc.Table, f.Name))
		}
	}
	for _, ix := range desc.Indexes {
		unique := ""
		if ix.Unique {
			unique = "UNIQUE "
		}
		out = append(out, fmt.Sprintf("CREATE %sINDEX %s ON %s(%s)",
			unique, ix.Name, desc.Table, strings.Join(ix.Columns, ", ")))
	}
	return out
}

// sqliteScopeTriggers renders the tenant tripwire triggers. When the pin table
// is empty (the System path) the triggers allow the write; otherwise they
// require the row's tenant_id to equal the pinned tenant. For append-only tables
// the update/delete checks are redundant with the immutability triggers, so
// only the insert check is attached.
func sqliteScopeTriggers(t string, appendOnly bool) []string {
	pin := "(SELECT tenant_id FROM " + ScopeTenantTable + " LIMIT 1)"
	exists := "EXISTS(SELECT 1 FROM " + ScopeTenantTable + ")"
	ins := fmt.Sprintf(`CREATE TRIGGER %s_scope_ins BEFORE INSERT ON %s
BEGIN
  SELECT RAISE(ABORT,'tenant scope violation')
  WHERE %s AND (NEW.tenant_id IS NULL OR NEW.tenant_id <> %s);
END`, t, t, exists, pin)
	if appendOnly {
		return []string{ins}
	}
	upd := fmt.Sprintf(`CREATE TRIGGER %s_scope_upd BEFORE UPDATE ON %s
BEGIN
  SELECT RAISE(ABORT,'tenant scope violation')
  WHERE %s AND (NEW.tenant_id <> %s OR OLD.tenant_id <> %s);
END`, t, t, exists, pin, pin)
	del := fmt.Sprintf(`CREATE TRIGGER %s_scope_del BEFORE DELETE ON %s
BEGIN
  SELECT RAISE(ABORT,'tenant scope violation')
  WHERE %s AND OLD.tenant_id <> %s;
END`, t, t, exists, pin)
	return []string{ins, upd, del}
}

// sqliteAppendOnlyTriggers renders the immutability triggers for an append-only
// table: any UPDATE or DELETE aborts.
func sqliteAppendOnlyTriggers(t string) []string {
	return []string{
		fmt.Sprintf("CREATE TRIGGER %s_no_update BEFORE UPDATE ON %s\nBEGIN SELECT RAISE(ABORT,'%s is append-only'); END", t, t, t),
		fmt.Sprintf("CREATE TRIGGER %s_no_delete BEFORE DELETE ON %s\nBEGIN SELECT RAISE(ABORT,'%s is append-only'); END", t, t, t),
	}
}

// AppendOnlyGuardStmts renders the same trigger pair sqliteAppendOnlyTriggers does, in the
// re-issuable form: IF NOT EXISTS, because this runs on every boot.
//
// There is no ACL half. SQLite has no role layer, so the triggers ARE the boundary — and
// they apply to every connection, including the engine's own.
func (sqliteDialect) AppendOnlyGuardStmts(table string) []string {
	return []string{
		fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS %s_no_update BEFORE UPDATE ON %s\nBEGIN SELECT RAISE(ABORT,'%s is append-only'); END", table, table, table),
		fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS %s_no_delete BEFORE DELETE ON %s\nBEGIN SELECT RAISE(ABORT,'%s is append-only'); END", table, table, table),
	}
}

func (sqliteDialect) GuardedTables(ctx context.Context, q Querier) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT name, tbl_name FROM sqlite_master WHERE type='trigger'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	guarded := make(map[string]bool)
	for rows.Next() {
		var name, tbl string
		if err := rows.Scan(&name, &tbl); err != nil {
			return nil, err
		}
		if strings.HasSuffix(name, "_scope_ins") {
			guarded[tbl] = true
		}
	}
	return guarded, rows.Err()
}

// SchemaName is always "main" on SQLite: the store opens exactly one database and
// never ATTACHes another.
func (sqliteDialect) SchemaName(context.Context, Querier) (string, error) {
	return "main", nil
}

func (sqliteDialect) SchemaTriggers(ctx context.Context, q Querier) (map[TriggerKey]TriggerInfo, error) {
	// sqlite_schema.sql carries the trigger's own body, which is the only way to
	// notice that a trigger with the right name and table was replaced by a no-op.
	// It is NOT a byte copy of the migration text: SQLite uppercases the leading
	// keywords, drops TEMP, drops a database qualifier, strips leading whitespace
	// and collapses the run after the first two keywords. Callers must therefore
	// compare against a golden captured from a real migrated database, never
	// against the migration file.
	rows, err := q.QueryContext(ctx,
		"SELECT name, tbl_name, COALESCE(sql, '') FROM sqlite_master WHERE type='trigger'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	triggers := make(map[TriggerKey]TriggerInfo)
	for rows.Next() {
		var name, table, definition string
		if err := rows.Scan(&name, &table, &definition); err != nil {
			return nil, err
		}
		// SQLite has no per-trigger enable state and no function privilege layer: a
		// trigger present in the schema fires, on every connection, unconditionally.
		// TriggerNoEnableState says exactly that. It is deliberately NOT PostgreSQL's
		// ENABLE ALWAYS: reusing that value made every policy about the ALWAYS state
		// apply here too, on an engine where the state cannot be chosen at all.
		triggers[TriggerKey{Schema: "main", Table: table, Name: name}] = TriggerInfo{
			CanExecute: true, EnableState: TriggerNoEnableState, Definition: definition,
		}
	}
	return triggers, rows.Err()
}

// TableColumns lists a table's columns via the pragma_table_info table-valued
// function (which, unlike the PRAGMA statement, accepts a bound parameter, so
// the table name is never interpolated). A non-existent table yields an empty
// set, not an error.
//
// The schema argument is 'main', explicitly. It is the default when omitted and this
// store opens a fresh single-connection pool with nothing ATTACHed, so the two are
// equivalent today — but the invariant being expressed is "the engine's own database",
// not "whatever a future connection setup leaves implicit". The unit-G rollout
// witness decides once and permanently whether a deployment predates a control, and an
// attached database must never be able to answer that question.
func (sqliteDialect) TableColumns(ctx context.Context, q Querier, table string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM pragma_table_info(?, 'main')", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// ConnRolePosture reports an RLS-safe posture: SQLite has no roles, and its
// isolation is the closed query layer plus the tripwire triggers, which apply
// unconditionally to the single writer connection.
func (sqliteDialect) ConnRolePosture(context.Context, Querier) (RolePosture, error) {
	return RolePosture{Role: "sqlite", ReplicationRole: "origin"}, nil
}

// ConnRoleIdentity reports the same fixed name ConnRolePosture does. SQLite has no
// role layer, so the identity is never unknown here and never needs a fallback —
// but the method exists so the boot path has ONE shape on both engines rather than
// an engine test at the call site.
func (sqliteDialect) ConnRoleIdentity(context.Context, Querier) (string, error) {
	return "sqlite", nil
}
