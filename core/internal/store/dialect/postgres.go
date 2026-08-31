// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// DefaultAppRole is the conventional non-superuser, no-BYPASSRLS Postgres role the
// application connects as — the name `olivares db init` defaults to and every
// deployment artifact provisions. Row-level security is FORCE'd so even this role's
// table owner cannot see across tenants.
//
// It is the DEFAULT, no longer the hard-coded target: the append-only revoke is
// aimed at the role the application pool actually authenticates as (NewForAppRole,
// fed from the connection's own posture at boot). It used to be a compile-time
// constant, which made the revoke a silent no-op on any deployment that used
// `--app-role` with a different name — the ACL was emitted for a role that did not
// exist, the `IF EXISTS` gate swallowed it, and nothing read the result back.
//
// It stays EXPORTED because the name is still load-bearing elsewhere: the fixtures
// in core/internal/pgtest provision their isolated databases for THIS role, and the
// deployment SQL creates it.
const DefaultAppRole = "olivares_app"

// blockMutationFn is the shared trigger function (created by the core Postgres
// tenancy migration) that immutability triggers call to reject UPDATE/DELETE.
const blockMutationFn = "olivares_block_mutation"

// EngineSchema is the schema the engine creates and guards its tables in. It is a
// FIXED name, not search_path: GuardedTables (the isolation self-test's inventory)
// and the tenant-policy lookup have always searched it literally, and `db init`
// grants there. Anything that inventories or verifies engine tables must use this,
// so that a connection whose search_path points elsewhere cannot make a catalog
// question answer about the wrong schema — or about nothing at all.
const EngineSchema = "public"

// postgresDialect targets Postgres via the pgx/stdlib database/sql driver. UUIDs
// and timestamps are TEXT (portable, identical to SQLite and to the audit
// preimage); isolation is FORCE row-level security plus a per-tenant policy,
// with the per-transaction app.tenant_id GUC as the binding.
type postgresDialect struct {
	appRole string
}

func (postgresDialect) Name() store.Engine { return store.EnginePostgres }

func (postgresDialect) Rebind(query string) string { return rebindPositional(query) }

func (postgresDialect) ColumnType(k model.SQLKind, nullable bool) string {
	var t string
	switch k {
	case model.KindInt:
		t = "BIGINT"
	case model.KindFloat:
		t = "DOUBLE PRECISION"
	case model.KindBool:
		t = "BOOLEAN"
	case model.KindBytes:
		t = "BYTEA"
	default: // KindText, KindJSON, KindTimestamp, KindUUID
		t = "TEXT"
	}
	if !nullable {
		t += " NOT NULL"
	}
	return t
}

func (postgresDialect) BindTenant(ctx context.Context, tx *sql.Tx, tenant model.TenantID) error {
	// true => set the GUC transaction-locally, so it auto-resets on commit or
	// rollback and never leaks across pooled connections.
	_, err := tx.ExecContext(ctx, "SELECT pg_catalog.set_config('app.tenant_id', $1, true)", tenant.String())
	return err
}

func (postgresDialect) ClearTenant(ctx context.Context, tx *sql.Tx) error {
	// Reset the GUC for the transaction. With it empty the FORCE'd policy matches
	// no rows, which is fail-safe for the app role: tenant-bound System ops rebind
	// per call, and a genuinely cross-tenant read (ListOrgs) runs on the dedicated
	// BYPASSRLS admin pool when configured (Config.AdminDSN / openAdminPool), not on
	// this RLS-scoped transaction (docs/SECURITY-HARDENING.md).
	_, err := tx.ExecContext(ctx, "SELECT pg_catalog.set_config('app.tenant_id', '', true)")
	return err
}

func (postgresDialect) TenancyStmts() []string {
	return []string{
		// Shared trigger function that immutability triggers call to reject any
		// UPDATE/DELETE on an append-only table.
		`CREATE OR REPLACE FUNCTION ` + blockMutationFn + `() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'table is append-only';
END;
$$ LANGUAGE plpgsql`,
	}
}

// pgAuditEventsTable is the name of the hash-chained evidence ledger.
const pgAuditEventsTable = "audit_events"

func (d postgresDialect) AuditTableStmts() []string {
	t := pgAuditEventsTable
	stmts := []string{
		`CREATE TABLE ` + t + ` (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  seq BIGINT NOT NULL,
  occurred_at TEXT NOT NULL,
  actor TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  action TEXT NOT NULL,
  target_kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  meta TEXT NOT NULL,
  -- meta_blind: see the sqlite dialect for why this is nullable and why the NULL
  -- carries meaning (the pre-blinding hash rule of an already-sealed row).
  -- CHECK: see the sqlite dialect — exactly two legal states, and a zero-length
  -- value is the illegal third one.
  meta_blind BYTEA CHECK (meta_blind IS NULL OR octet_length(meta_blind) = 32),
  payload_hash BYTEA,
  prev_hash BYTEA NOT NULL,
  hash BYTEA NOT NULL,
  sig BYTEA,
  UNIQUE(tenant_id, seq)
)`,
	}
	stmts = append(stmts, pgTenantGuard(t)...)
	stmts = append(stmts,
		fmt.Sprintf("CREATE TRIGGER %s_immutable BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", t, t, blockMutationFn),
		pgRevokeMutations(t, d.appRole),
	)
	// The per-tenant chain head (mutable): lets Verify detect tail truncation.
	// Tenant-guarded by RLS but not append-only, since the head advances.
	stmts = append(stmts,
		"CREATE TABLE "+AuditHeadsTable+" (tenant_id TEXT PRIMARY KEY, seq BIGINT NOT NULL, hash BYTEA NOT NULL)")
	stmts = append(stmts, pgTenantGuard(AuditHeadsTable)...)
	return stmts
}

func (postgresDialect) AuditSpoolStmts() []string {
	// audit_spool_usage is global mutable bookkeeping, not tenant data and not
	// evidence. It deliberately has neither RLS nor an immutability trigger; the
	// single seeded row is updated transactionally with appends.
	stmts := []string{
		"CREATE TABLE " + AuditSpoolUsageTable + " (id INTEGER PRIMARY KEY, bytes BIGINT NOT NULL)",
		"INSERT INTO " + AuditSpoolUsageTable + " (id, bytes) VALUES (1, 0)",
		// audit_spool_gaps is mutable per-tenant integrity bookkeeping. It mirrors
		// audit_heads exactly: FORCE RLS applies, append-only enforcement does not.
		"CREATE TABLE " + AuditSpoolGapsTable + " (tenant_id TEXT PRIMARY KEY, dropped BIGINT NOT NULL, first_dropped_at TEXT NOT NULL)",
	}
	return append(stmts, pgTenantGuard(AuditSpoolGapsTable)...)
}

func (d postgresDialect) CreateTableStmts(desc model.EntityDescriptor) []string {
	t := desc.Table
	var cols []string
	cols = append(cols, "id TEXT PRIMARY KEY")
	cols = append(cols, "tenant_id TEXT NOT NULL")
	cols = append(cols, "created_at TEXT NOT NULL")
	cols = append(cols, "updated_at TEXT NOT NULL")
	cols = append(cols, "version BIGINT NOT NULL")
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
	stmts = append(stmts, pgTenantGuard(t)...)
	if desc.AppendOnly {
		stmts = append(stmts,
			fmt.Sprintf("CREATE TRIGGER %s_immutable BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", t, t, blockMutationFn),
			pgRevokeMutations(t, d.appRole),
		)
	}
	return stmts
}

// pgRevokeMutations revokes UPDATE/DELETE/TRUNCATE from the application role, but
// only if that role exists — so the schema applies cleanly on deployments (and test
// databases) that have not yet provisioned it. The immutability trigger is the
// primary enforcement for UPDATE/DELETE; for TRUNCATE, which is a statement-level
// operation that no row trigger can see, this revoke is the only ACL-layer defense.
//
// The role name is RUNTIME data (the effective role the application pool
// authenticates as, see New/NewForAppRole), not a compile-time constant, so the
// statement is rendered so that ANY name PostgreSQL accepts is safe: the name
// crosses into SQL exactly once as an escaped literal, and the identifier is quoted
// SERVER-side by pg_catalog.format('%I'). Interpolating it into the identifier position from Go
// would break on a legal-but-quoted name (mixed case, a hyphen) and would be an
// injection surface the moment the name stops being a constant.
//
// The pg_roles existence gate is load-bearing and must stay: REVOKE naming a role
// that does not exist is a hard ERROR, so an ungated statement would abort the
// migration if the role were dropped between introspection and execution.
// AppendOnlyGuardStmts renders the immutability guard for one table, idempotently.
//
// CREATE OR REPLACE TRIGGER, not CREATE TRIGGER: this is issued on EVERY boot, and the
// creation-time form would abort the second one. It is available on 15, 16, 17 and 18,
// which is the whole supported range.
//
// AND IT EMITS ENABLE ALWAYS, which an earlier version of this function omitted on the
// reasoning that the enable state belongs to the guard rollout's attested inventory. That
// reasoning was wrong in the direction that matters, and the repository's own regression
// says so in one line: "at 'O' a logical-replication apply mutates evidence in silence".
// A trigger left in its default ORIGIN state does not fire for replication apply, so a
// guard installed and left at 'O' is a guard that is absent on precisely the path an
// operator cannot watch. The relations this converging form exists for are NOT in the
// rollout — that is why they never acquired a guard — so nothing else will ever raise them.
//
// ALTER TABLE ... ENABLE ALWAYS TRIGGER is idempotent, like the CREATE OR REPLACE above.
func (d postgresDialect) AppendOnlyGuardStmts(table string) []string {
	return []string{
		fmt.Sprintf("CREATE OR REPLACE TRIGGER %s_immutable BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()",
			table, table, blockMutationFn),
		fmt.Sprintf("ALTER TABLE ONLY %s ENABLE ALWAYS TRIGGER %s_immutable", table, table),
		pgRevokeMutations(table, d.appRole),
	}
}

func pgRevokeMutations(table, role string) string {
	body := fmt.Sprintf(`
DECLARE target text := %s;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = target) THEN
    EXECUTE pg_catalog.format('REVOKE UPDATE, DELETE, TRUNCATE ON %%I FROM %%I', %s, target);
  END IF;
END `, pgDollarQuote(role), pgDollarQuote(table))
	tag := pgDollarTagNotIn(body)
	return "DO " + tag + body + tag
}

// pgDollarQuote wraps s in a dollar-quoted string whose tag does not occur in s.
//
// Dollar quoting rather than a quoted literal, because a quoted literal is only as
// safe as the escaping rules in force: doubling single quotes is correct under
// standard_conforming_strings=on, and a value containing a backslash then breaks the
// statement when that setting is off. Inside dollar quotes there are no escapes at
// all, so the literal can only end early where the tag appears again — which is what
// choosing the tag against the value prevents. Note "against", not "in": see
// pgDollarTagNotIn for why absence from the value is NOT sufficient.
//
// This matters because the value is now RUNTIME data: an earlier revision used a
// fixed `$olv_ao$` tag and claimed it could not be terminated. The role `x$olv_ao$x`
// is a legal PostgreSQL role name and made the rendered block fail to parse, turning
// a legitimate least-privilege deployment into a boot failure.
func pgDollarQuote(s string) string {
	tag := pgDollarTagNotIn(s)
	return tag + s + tag
}

// pgDollarTagNotIn returns a dollar-quote tag that cannot close the literal early
// when s is wrapped in it.
//
// It is NOT enough for the tag to be absent from s. The tag `$olvN$` opens and closes
// with the same character, so a value whose tail is `$olvN` — the tag minus its final
// `$` — joins the closing delimiter into a complete tag one character early:
// `$olv$` + `svc$olv` + `$olv$` renders `$olv$svc$olv$olv$`, which PostgreSQL reads as
// the literal `svc` followed by the bare identifier `olv$` (a `$` is legal in a
// non-leading identifier position, which is why nothing complains).
//
// Measured on PostgreSQL 15.18 with the real renderer: with the role `svc$olv` the DO
// block runs WITHOUT error and revokes from a role named `svc`, leaving the real role
// holding TRUNCATE — a silently ineffective revoke, the exact failure this whole path
// exists to remove. With a hostile TABLE name the leftover lands inside pg_catalog.format()'s
// argument list, where no alias is allowed, so the statement fails to parse and the
// deployment cannot boot at all.
//
// Probing the join `s + "$"` is exactly necessary and sufficient. A premature match
// requires a proper suffix of s to be a proper prefix of the tag AND the tag to have a
// border of the complementary length; `$olvN$` has exactly one border (`$`), so the
// only possible overlap is the whole tag minus its last character — and s ends with
// that if and only if `s + "$"` contains the tag.
func pgDollarTagNotIn(s string) string {
	probe := s + "$"
	for i := 0; ; i++ {
		tag := "$olv$"
		if i > 0 {
			tag = fmt.Sprintf("$olv%d$", i)
		}
		if !strings.Contains(probe, tag) {
			return tag
		}
	}
}

// AppendOnlyCatalogRevokeStmt renders a self-contained statement that revokes
// UPDATE/DELETE/TRUNCATE from role on every table in schema carrying the engine's
// immutability trigger, discovering them from the CATALOG rather than from a
// registry.
//
// Exported because provisioning (`olivares db init`) needs it and runs outside the
// engine — often before any schema exists, in which case the loop finds nothing. The
// schema is named explicitly rather than taken from pg_catalog.current_schema(): the grants it
// repairs are themselves written against a fixed schema, and a maintenance connection
// with a search_path of its own would otherwise scan somewhere else entirely and
// silently repair nothing.
func AppendOnlyCatalogRevokeStmt(role, schema string) string {
	// THE SAME CIRCULARITY C4-15 NAMED, IN THE PROVISIONING PATH. This loop derived its
	// membership from the trigger too, so a relation whose guard had been dropped was not
	// merely left unprotected by the engine's boot — the repair path skipped it as well,
	// and an operator running provisioning to fix the ACL would be told nothing was wrong.
	// The durable scope inventory is UNIONed in, restricted to relations that still exist
	// in this schema, so a table the engine has ever protected keeps being reachable by the
	// repair even after its guard is gone. The inventory is created by the engine at boot,
	// so provisioning may legitimately run before it exists: to_regclass gates the second
	// arm rather than assuming it.
	body := fmt.Sprintf(`
DECLARE r record; target text := %s; sch text := %s; inv text := %s;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = target) THEN RETURN; END IF;
  FOR r IN
    SELECT DISTINCT c.relname
    FROM pg_trigger t
    JOIN pg_class c ON c.oid = t.tgrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN pg_proc p ON p.oid = t.tgfoid
    JOIN pg_namespace fn ON fn.oid = p.pronamespace
    WHERE n.nspname = sch AND NOT t.tgisinternal
      AND p.proname = %s AND fn.nspname = sch
  LOOP
    EXECUTE pg_catalog.format('REVOKE UPDATE, DELETE, TRUNCATE ON %%I.%%I FROM %%I', sch, r.relname, target);
  END LOOP;
  IF pg_catalog.to_regclass(pg_catalog.quote_ident(sch) || '.' || pg_catalog.quote_ident(inv)) IS NOT NULL THEN
    FOR r IN EXECUTE pg_catalog.format('SELECT table_name FROM %%I.%%I', sch, inv)
    LOOP
      IF pg_catalog.to_regclass(pg_catalog.quote_ident(sch) || '.' || pg_catalog.quote_ident(r.table_name)) IS NOT NULL THEN
        EXECUTE pg_catalog.format('REVOKE UPDATE, DELETE, TRUNCATE ON %%I.%%I FROM %%I', sch, r.table_name, target);
      END IF;
    END LOOP;
  END IF;
END `, pgDollarQuote(role), pgDollarQuote(schema), pgDollarQuote(ControlAppendOnlyScopeTable), pgDollarQuote(blockMutationFn))
	tag := pgDollarTagNotIn(body)
	return "DO " + tag + body + tag
}

func (postgresDialect) AppendOnlyACLTables(ctx context.Context, q Querier) ([]string, error) {
	// EngineSchema, not pg_catalog.current_schema(). The engine's tables live in a fixed schema —
	// GuardedTables, the RLS self-test's inventory, has always looked there, and
	// provisioning grants there. Resolving this from search_path instead would let an
	// application role whose search_path points elsewhere produce an EMPTY inventory,
	// which the caller would then read as "nothing to protect" while the real ledger
	// sat open in public. That is not hypothetical: it was measured.
	// The trigger function is matched by SCHEMA as well as name. Matching the bare name
	// misclassifies any table whose trigger calls a DIFFERENT function that merely shares
	// it — `other.olivares_block_mutation()` — and the reconcile would then strip
	// UPDATE/DELETE/TRUNCATE from an ordinary MUTABLE table the engine has no business
	// touching. Measured: a public table went from true/true/true to false/false/false on
	// a boot that reported no error.
	const query = `SELECT DISTINCT c.relname
FROM pg_trigger t
JOIN pg_class c ON c.oid = t.tgrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_proc p ON p.oid = t.tgfoid
JOIN pg_namespace fn ON fn.oid = p.pronamespace
WHERE n.nspname = $1 AND NOT t.tgisinternal
  AND p.proname = $2 AND fn.nspname = $1
ORDER BY 1`
	rows, err := q.QueryContext(ctx, query, EngineSchema, blockMutationFn)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (d postgresDialect) AppendOnlyACLStmts(tables []string) []string {
	if len(tables) == 0 {
		return nil
	}
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		// One statement per table rather than one loop server-side: a failure then
		// names the table it happened on, which is what an operator needs.
		out = append(out, pgRevokeMutations(t, d.appRole))
	}
	return out
}

// pgTenantGuard renders the FORCE row-level-security guard and tenant policy for
// a table. current_setting is called with missing_ok=false (no second
// argument), so an unbound transaction RAISES rather than silently returning
// zero rows — a forgotten bind fails loudly (docs/SECURITY-HARDENING.md).
func pgTenantGuard(t string) []string {
	return []string{
		fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY", t),
		fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY", t),
		fmt.Sprintf(`CREATE POLICY tenant_isolation ON %s
  USING (tenant_id = pg_catalog.current_setting('app.tenant_id'))
  WITH CHECK (tenant_id = pg_catalog.current_setting('app.tenant_id'))`, t),
	}
}

// ConnRolePosture queries the current role's superuser/BYPASSRLS attributes.
// pg_roles is readable by any role for these (non-secret) columns, so this works
// even for the deliberately-unprivileged application role.
// TableColumns lists a table's columns from information_schema, bound to the
// ENGINE'S schema. A non-existent table yields an empty set, not an error. It powers
// the additive-column reconcile in sqlstore and the unit-G rollout witness probe.
//
// It binds EngineSchema rather than pg_catalog.current_schema(), which is the same
// rule every other catalog check in this file follows (GuardedTables, the append-only
// ACL reconcile). The two resolve identically in this store today, because every pool
// it opens pins its search_path — but they MEAN different things: pg_catalog.current_schema() is
// "the first existing schema in the current search path", and this engine's contract
// is a literal fixed name. The difference matters most for the rollout witness, whose
// answer decides once and permanently whether a deployment predates a control: a
// same-named relation in another schema must not be able to stand in for the engine's
// table, and a search_path a future connection setup changes must not be able to move
// the answer.
func (postgresDialect) TableColumns(ctx context.Context, q Querier, table string) (map[string]bool, error) {
	const query = `SELECT column_name FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2`
	rows, err := q.QueryContext(ctx, query, EngineSchema, table)
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

func (postgresDialect) ConnRolePosture(ctx context.Context, q Querier) (RolePosture, error) {
	// session_user is the login identity; current_user can be a startup-time or
	// runtime SET ROLE disguise. The session can recover its login authority with
	// SET ROLE NONE, so certifying only current_user
	// would let an owner/superuser
	// authenticate and masquerade as an app/admin role. No store pool has a
	// legitimate assumed-role posture: require equality and report the login's
	// attributes as the posture that actually bounds the connection.
	const query = `SELECT session_user, current_user,
       sr.rolsuper, sr.rolbypassrls,
       cr.rolsuper, cr.rolbypassrls,
       pg_catalog.current_setting('session_replication_role')
FROM pg_catalog.pg_roles AS sr, pg_catalog.pg_roles AS cr
WHERE sr.rolname = session_user
  AND cr.rolname = current_user`
	var p RolePosture
	var sessionUser, currentUser string
	var currentSuperuser, currentBypassRLS bool
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return RolePosture{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return RolePosture{}, err
		}
		return RolePosture{}, fmt.Errorf(
			"postgres: role posture: session_user/current_user not found in pg_roles",
		)
	}
	if err := rows.Scan(
		&sessionUser, &currentUser,
		&p.Superuser, &p.BypassRLS,
		&currentSuperuser, &currentBypassRLS,
		&p.ReplicationRole,
	); err != nil {
		return RolePosture{}, err
	}
	if err := rows.Err(); err != nil {
		return RolePosture{}, err
	}
	p.Role = sessionUser
	if sessionUser == "" || currentUser == "" {
		return RolePosture{}, fmt.Errorf(
			"postgres: role posture: empty session_user %q or current_user %q",
			sessionUser, currentUser,
		)
	}
	if sessionUser != currentUser {
		return RolePosture{}, fmt.Errorf(
			"postgres: role posture: connection authenticates as session_user %q (SUPERUSER=%t BYPASSRLS=%t) but assumes current_user %q (SUPERUSER=%t BYPASSRLS=%t); SET ROLE NONE would recover the login authority",
			sessionUser, p.Superuser, p.BypassRLS,
			currentUser, currentSuperuser, currentBypassRLS,
		)
	}
	return p, nil
}

// ConnRoleIdentity asks the ONE question that needs no catalog privilege.
//
// session_user and current_user are SQL special functions, not views: they are
// answerable with zero
// grants, which is precisely what makes it usable as the fallback when pg_roles is
// unreadable. MEASURED on 15.18/16.14/17.10/18.4 after `REVOKE SELECT ON
// pg_catalog.pg_roles FROM PUBLIC`: the posture query returns 42501 "permission
// denied for view pg_roles"; this one returns the login/active role names. They
// must match: returning only current_user would preserve the SET ROLE disguise
// precisely on the fallback meant to recover identity safely.
//
// An empty result is treated as a FAILURE rather than an empty role, because the
// caller's next move is to bind an ACL to this name and "" is not a role.
func (postgresDialect) ConnRoleIdentity(ctx context.Context, q Querier) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT session_user, current_user`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("postgres: role identity: session_user/current_user returned no row")
	}
	var sessionUser, currentUser string
	if err := rows.Scan(&sessionUser, &currentUser); err != nil {
		return "", err
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if sessionUser == "" || currentUser == "" {
		return "", fmt.Errorf(
			"postgres: role identity: empty session_user %q or current_user %q",
			sessionUser, currentUser,
		)
	}
	if sessionUser != currentUser {
		return "", fmt.Errorf(
			"postgres: role identity: connection authenticates as session_user %q but assumes current_user %q; SET ROLE NONE would recover the login authority",
			sessionUser, currentUser,
		)
	}
	return sessionUser, nil
}

func (postgresDialect) GuardedTables(ctx context.Context, q Querier) (map[string]bool, error) {
	const query = `
SELECT c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind = 'r'
  AND c.relrowsecurity AND c.relforcerowsecurity
  AND EXISTS (
    SELECT 1 FROM pg_policies p
    WHERE p.schemaname = 'public' AND p.tablename = c.relname
      AND p.policyname = 'tenant_isolation'
  )`
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	guarded := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		guarded[name] = true
	}
	return guarded, rows.Err()
}

// SchemaName reports the schema the connection resolves unqualified objects in.
func (postgresDialect) SchemaName(ctx context.Context, q Querier) (string, error) {
	rows, err := q.QueryContext(ctx, "SELECT pg_catalog.current_schema()")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("postgres: pg_catalog.current_schema() returned no row")
	}
	var schema sql.NullString
	if err := rows.Scan(&schema); err != nil {
		return "", err
	}
	if !schema.Valid || schema.String == "" {
		return "", fmt.Errorf("postgres: the connection has no current schema (empty search_path)")
	}
	return schema.String, rows.Err()
}

func (d postgresDialect) SchemaTriggers(ctx context.Context, q Querier) (map[TriggerKey]TriggerInfo, error) {
	// tgenabled is returned RAW, not folded into a boolean here: 'O' fires in
	// origin/local sessions, 'A' always, 'D' never and 'R' only under replica, and a
	// disabled or replica-only trigger stays in the catalog — so a presence check
	// alone accepts a boundary that never runs. Deciding which of those states counts
	// as "fires" is TriggerEnableState.Fires, a pure function this repository can
	// unit-test; a `tgenabled IN ('O','A')` predicate inside this string could only
	// ever be exercised against a live server.
	//
	// The cast to text is deliberate, and the reason is the RESULT TYPE rather than the
	// wire format: tgenabled is the internal one-byte "char" (OID 18), which drivers are
	// free to surface as a byte, a rune or a string. Asking the server for text means
	// this mapping depends on PostgreSQL's rendering of the value, not on a pgx
	// decoding choice that a version bump could change under it.
	// CanExecute is asked for d.appRole explicitly. During a split-role boot q is the
	// OWNER migration transaction, whose current_user commonly owns the function and
	// would answer true even after EXECUTE was revoked from the runtime application
	// role. The role is a bound value, never rendered into SQL.
	const query = `
SELECT n.nspname, c.relname, t.tgname,
       p.oid::pg_catalog.regprocedure::pg_catalog.text,
       pn.nspname, p.proname,
	   pg_catalog.has_function_privilege($1::pg_catalog.text, p.oid, 'EXECUTE'),
	   t.tgenabled::pg_catalog.text,
	   pg_catalog.pg_get_triggerdef(t.oid, false),
	   pg_catalog.pg_get_functiondef(p.oid)
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
JOIN pg_catalog.pg_namespace pn ON pn.oid = p.pronamespace
WHERE n.nspname = pg_catalog.current_schema() AND NOT t.tgisinternal`
	rows, err := q.QueryContext(ctx, query, d.appRole)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPostgresSchemaTriggers(rows)
}

// SchemaTriggerCallers returns every non-internal trigger, in every schema, that
// invokes one of functions. The selected tuples enter as parameters; unrelated
// functions are neither deparsed nor privilege-checked.
func (d postgresDialect) SchemaTriggerCallers(
	ctx context.Context,
	q Querier,
	functions []SchemaTriggerFunctionKey,
) (map[TriggerKey]TriggerInfo, error) {
	if len(functions) == 0 {
		return map[TriggerKey]TriggerInfo{}, nil
	}
	unique := make(map[SchemaTriggerFunctionKey]bool, len(functions))
	ordered := make([]SchemaTriggerFunctionKey, 0, len(functions))
	for _, function := range functions {
		if function.Schema == "" || function.Name == "" {
			return nil, fmt.Errorf("postgres: schema-trigger caller inventory received an empty function identity")
		}
		if unique[function] {
			continue
		}
		unique[function] = true
		ordered = append(ordered, function)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Schema != ordered[j].Schema {
			return ordered[i].Schema < ordered[j].Schema
		}
		return ordered[i].Name < ordered[j].Name
	})

	args := make([]any, 1, 1+2*len(ordered))
	args[0] = d.appRole
	values := make([]string, 0, len(ordered))
	for _, function := range ordered {
		schemaParam := len(args) + 1
		args = append(args, function.Schema)
		nameParam := len(args) + 1
		args = append(args, function.Name)
		values = append(values, fmt.Sprintf(
			"($%d::pg_catalog.text, $%d::pg_catalog.text)",
			schemaParam, nameParam,
		))
	}
	query := `
WITH selected(function_schema, function_name) AS (VALUES ` + strings.Join(values, ", ") + `)
SELECT n.nspname, c.relname, t.tgname,
       p.oid::pg_catalog.regprocedure::pg_catalog.text,
       pn.nspname, p.proname,
	   pg_catalog.has_function_privilege($1::pg_catalog.text, p.oid, 'EXECUTE'),
	   t.tgenabled::pg_catalog.text,
	   pg_catalog.pg_get_triggerdef(t.oid, false),
	   pg_catalog.pg_get_functiondef(p.oid)
FROM selected s
JOIN pg_catalog.pg_namespace pn
  ON pn.nspname::pg_catalog.text = s.function_schema
JOIN pg_catalog.pg_proc p ON p.pronamespace = pn.oid
              AND p.proname::pg_catalog.text = s.function_name
              AND p.pronargs = 0
JOIN pg_catalog.pg_trigger t ON t.tgfoid = p.oid
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE NOT t.tgisinternal`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPostgresSchemaTriggers(rows)
}

// SchemaTriggerFunction projects exactly one zero-argument routine identity.
// The query is directed by bound schema/name values and never parses a
// regprocedure rendering. A non-trigger routine still counts as present: it
// occupies the same PostgreSQL routine signature and must make reservation fail.
func (d postgresDialect) SchemaTriggerFunction(
	ctx context.Context,
	q Querier,
	function SchemaTriggerFunctionKey,
) (SchemaTriggerFunctionInfo, bool, error) {
	const query = `
SELECT p.oid::pg_catalog.int8,
       pg_catalog.pg_get_functiondef(p.oid),
       COALESCE(p.proacl, pg_catalog.acldefault('f', p.proowner))::pg_catalog.text,
       (
           SELECT COUNT(*) = CASE
                                 WHEN pg_catalog.pg_get_userbyid(p.proowner) = $3::pg_catalog.text
                                 THEN 1 ELSE 2
                             END
              AND COUNT(*) FILTER (
                      WHERE acl.grantee = p.proowner
                        AND acl.privilege_type = 'EXECUTE'
                        AND NOT acl.is_grantable
                  ) = 1
              AND COUNT(*) FILTER (
                      WHERE acl.grantee <> 0
                        AND pg_catalog.pg_get_userbyid(acl.grantee) = $3::pg_catalog.text
                        AND acl.privilege_type = 'EXECUTE'
                        AND NOT acl.is_grantable
                  ) = 1
             FROM pg_catalog.aclexplode(
                      COALESCE(p.proacl, pg_catalog.acldefault('f', p.proowner))
                  ) acl
       ),
       pg_catalog.has_function_privilege($3::pg_catalog.text, p.oid, 'EXECUTE'),
       EXISTS (
           SELECT 1
             FROM pg_catalog.aclexplode(
                      COALESCE(p.proacl, pg_catalog.acldefault('f', p.proowner))
                  ) acl
            WHERE acl.grantee <> 0
              AND pg_catalog.pg_get_userbyid(acl.grantee) = $3::pg_catalog.text
              AND acl.privilege_type = 'EXECUTE'
       ),
       EXISTS (
           SELECT 1
             FROM pg_catalog.aclexplode(
                      COALESCE(p.proacl, pg_catalog.acldefault('f', p.proowner))
                  ) acl
            WHERE acl.grantee = 0
              AND acl.privilege_type = 'EXECUTE'
       )
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1::pg_catalog.text
  AND p.proname = $2::pg_catalog.text
  AND p.pronargs = 0`
	rows, err := q.QueryContext(ctx, query, function.Schema, function.Name, d.appRole)
	if err != nil {
		return SchemaTriggerFunctionInfo{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return SchemaTriggerFunctionInfo{}, false, rows.Err()
	}
	var info SchemaTriggerFunctionInfo
	if err := rows.Scan(
		&info.OID,
		&info.Definition,
		&info.ACL,
		&info.ACLIsExact,
		&info.CanExecute,
		&info.AppRoleDirectExecute,
		&info.PublicCanExecute,
	); err != nil {
		return SchemaTriggerFunctionInfo{}, false, err
	}
	if rows.Next() {
		return SchemaTriggerFunctionInfo{}, false, fmt.Errorf(
			"postgres: zero-argument routine identity %q.%q is not unique",
			function.Schema, function.Name,
		)
	}
	if err := rows.Err(); err != nil {
		return SchemaTriggerFunctionInfo{}, false, err
	}
	return info, true, nil
}

// ReserveSchemaTriggerFunction creates an uncommitted, fail-closed placeholder
// for the next trigger-function identity. Plain CREATE FUNCTION is the name
// reservation: a concurrent creator either wins before this statement (and makes
// it fail) or waits/fails on PostgreSQL's routine-signature uniqueness. No other
// session can resolve a placeholder created by this transaction before COMMIT.
//
// The migration replaces this function with CREATE OR REPLACE, preserving its OID
// and the ACL established here. A function can inherit hostile ALTER DEFAULT
// PRIVILEGES entries at CREATE time, including unrelated grantees or grant option.
// The normalization block therefore revokes every explicit non-PUBLIC grantee,
// restores the owner's non-grantable EXECUTE entry, and only then grants the exact
// configured application role. Runtime values are identifiers or dynamically safe
// dollar-quoted literals, so standard_conforming_strings is irrelevant.
func (d postgresDialect) ReserveSchemaTriggerFunction(
	ctx context.Context,
	q Querier,
	function SchemaTriggerFunctionKey,
) error {
	target := pgQuoteIdentifier(function.Schema) + "." + pgQuoteIdentifier(function.Name) + "()"
	const body = `$olivares_schema_transition_reservation$
BEGIN
  RAISE EXCEPTION USING
    ERRCODE = '55000',
    MESSAGE = 'reserved schema-trigger function was not replaced by its migration';
END
$olivares_schema_transition_reservation$`
	if _, err := q.ExecContext(ctx,
		"CREATE FUNCTION "+target+" RETURNS pg_catalog.trigger LANGUAGE plpgsql AS "+body); err != nil {
		return fmt.Errorf("create reserved trigger function: %w", err)
	}
	if _, err := q.ExecContext(ctx,
		"REVOKE ALL ON FUNCTION "+target+" FROM PUBLIC"); err != nil {
		return fmt.Errorf("revoke PUBLIC from reserved trigger function: %w", err)
	}
	if _, err := q.ExecContext(ctx,
		postgresReservedFunctionACLNormalization(function.Schema, function.Name)); err != nil {
		return fmt.Errorf("normalize inherited ACL on reserved trigger function: %w", err)
	}
	if _, err := q.ExecContext(ctx,
		"GRANT EXECUTE ON FUNCTION "+target+" TO "+pgQuoteIdentifier(d.appRole)); err != nil {
		return fmt.Errorf("grant reserved trigger function to application role: %w", err)
	}
	return nil
}

// postgresReservedFunctionACLNormalization removes every explicit role ACL that
// CREATE FUNCTION may have inherited from ALTER DEFAULT PRIVILEGES. The owner is
// included in the revoke and then restored explicitly, making the resulting ACL
// representation deterministic instead of relying on implicit ownership rights.
func postgresReservedFunctionACLNormalization(schema, name string) string {
	body := `
DECLARE
  target_schema pg_catalog.text := ` + pgDollarQuote(schema) + `;
  target_name pg_catalog.text := ` + pgDollarQuote(name) + `;
  owner_name pg_catalog.text;
  grantee_name pg_catalog.text;
BEGIN
  SELECT pg_catalog.pg_get_userbyid(p.proowner)
    INTO STRICT owner_name
    FROM pg_catalog.pg_proc p
    JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
   WHERE n.nspname = target_schema
     AND p.proname = target_name
     AND p.pronargs = 0;

  FOR grantee_name IN
    SELECT DISTINCT pg_catalog.pg_get_userbyid(acl.grantee)
      FROM pg_catalog.pg_proc p
      JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
      CROSS JOIN LATERAL pg_catalog.aclexplode(
          COALESCE(p.proacl, pg_catalog.acldefault('f', p.proowner))
      ) acl
     WHERE n.nspname = target_schema
       AND p.proname = target_name
       AND p.pronargs = 0
       AND acl.grantee <> 0
     ORDER BY 1
  LOOP
    EXECUTE pg_catalog.format(
        'REVOKE ALL PRIVILEGES ON FUNCTION %I.%I() FROM %I',
        target_schema, target_name, grantee_name
    );
  END LOOP;

  EXECUTE pg_catalog.format(
      'GRANT EXECUTE ON FUNCTION %I.%I() TO %I',
      target_schema, target_name, owner_name
  );
END `
	tag := pgDollarTagNotIn(body)
	return "DO " + tag + body + tag
}

func pgQuoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func scanPostgresSchemaTriggers(rows *sql.Rows) (map[TriggerKey]TriggerInfo, error) {
	triggers := make(map[TriggerKey]TriggerInfo)
	for rows.Next() {
		var schema, table, name, function, functionSchema, functionName string
		var enableState, triggerDef, functionDef string
		var canExecute bool
		if err := rows.Scan(
			&schema, &table, &name, &function, &functionSchema, &functionName,
			&canExecute, &enableState,
			&triggerDef, &functionDef,
		); err != nil {
			return nil, err
		}
		triggers[TriggerKey{Schema: schema, Table: table, Name: name}] = TriggerInfo{
			Function: function, FunctionSchema: functionSchema, FunctionName: functionName,
			CanExecute: canExecute, EnableState: TriggerEnableState(enableState),
			Definition: postgresTriggerDefinition(triggerDef, functionDef),
		}
	}
	return triggers, rows.Err()
}

// PostgresFunctionFenceStatement renders a catalog-tuple fence for a zero-argument
// function whose schema and name came from the live catalog.
//
// ALTER FUNCTION cannot bind identifiers, so the statement resolves the tuple by
// bound-equivalent server-side text comparisons and quotes the final identifiers
// with format('%I'). Runtime values cross into the block as dollar-quoted literals;
// unlike ordinary quoted strings, those remain data when
// standard_conforming_strings=off and a name contains a backslash. The outer DO tag
// is then chosen against the complete body, including every inner tag and value, so
// a legal identifier containing a formerly fixed tag cannot terminate the block.
//
// The fence writes back the function's existing cost. That advances the pg_proc
// tuple without normalising drift and makes a concurrent CREATE OR REPLACE wait
// until the surrounding migration transaction ends.
func PostgresFunctionFenceStatement(schema, name string) string {
	body := `
DECLARE cost_now pg_catalog.float4;
BEGIN
  SELECT p.procost INTO STRICT cost_now
    FROM pg_catalog.pg_proc p
    JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
   WHERE n.nspname = ` + pgDollarQuote(schema) + `
     AND p.proname = ` + pgDollarQuote(name) + `
     AND p.pronargs = 0;
  EXECUTE pg_catalog.format('ALTER FUNCTION %I.%I() COST %s',
                 ` + pgDollarQuote(schema) + `, ` + pgDollarQuote(name) + `,
                 cost_now::pg_catalog.text);
END `
	tag := pgDollarTagNotIn(body)
	return "DO " + tag + body + tag
}

// postgresTriggerDefinition frames the two catalog objects that make one
// PostgreSQL trigger invariant. pg_get_triggerdef binds the trigger name, table,
// timing, events and invoked function; pg_get_functiondef binds the executable
// body and its security/language attributes. Hashing only one lets an attacker
// keep that half byte-identical while replacing the other with a no-op.
//
// Length prefixes make the composition injective even when a function body
// contains the separator text. The catalog renderings, rather than migration
// source, are deliberate: PostgreSQL qualifies and normalizes object text.
func postgresTriggerDefinition(triggerDef, functionDef string) string {
	return fmt.Sprintf("trigger:%d:%sfunction:%d:%s",
		len(triggerDef), triggerDef, len(functionDef), functionDef)
}
