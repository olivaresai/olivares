// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

// Core v13 (ROOT-CONSTRUCTION-R5-1 §2–§3): the global login capability control relation.
// Expand, forward only. v12 stays reserved for FinOps and unregistered.
const (
	coreLoginCapabilityMigrationVersion = 13
	coreLoginCapabilityMigrationName    = "login_capability_observation_v1"
)

// loginCapabilityUpdatableColumns are the ONLY columns the application role may update.
var loginCapabilityUpdatableColumns = []string{"last_observed_at", "last_artifact_version", "observation_count"}

func coreLoginCapabilityMigration(dia dialect.Dialect, roles ...guardRoles) migrate.Migration {
	m := migrate.Migration{
		Version: coreLoginCapabilityMigrationVersion,
		Name:    coreLoginCapabilityMigrationName,
		Phase:   migrate.Expand,
		Stmts:   dia.LoginCapabilityControlStmts(),
	}
	if dia.Name() == store.EngineSQLite {
		m.After = func(ctx context.Context, tx *sql.Tx) error {
			return verifyLoginCapabilityRelation(ctx, tx, dia, guardRoles{})
		}
		return m
	}
	m.Before = func(context.Context, *sql.Tx) error {
		_, err := loginCapabilityTopology(roles)
		return err
	}
	// Default privileges (ALTER DEFAULT PRIVILEGES ... ON TABLES) hand a freshly created
	// relation to other operational roles, such as the admin read pool; measured in
	// correction 1 as a SELECT grant to the split fixture's admin role. The boundary is
	// established ONCE, at birth: every non-owner grantee is reduced to AT MOST SELECT
	// before the application grants. Boots never reset grants; the verifier refuses drift.
	//
	// AT MOST SELECT, not nothing, and the difference is a whole product capability. Until
	// this statement kept the read, it revoked the operator-provisioned admin SELECT that
	// `olivares db init` (sqlstore/dbsetup.go grantAdminRead), deploy/postgres/01-app-role.sql
	// and the CI provisioning all establish with ALTER DEFAULT PRIVILEGES. `olivares dr backup`
	// runs pg_dump on that admin DSN and on no other — pg_dump keeps row_security=off and
	// ABORTS as the NOBYPASSRLS application role under FORCE RLS (cmd/olivares/cmd_dr.go) —
	// and pg_dump's first act is to LOCK EVERY relation of the schema in ONE statement. So one
	// unreadable relation does not degrade a dump, it aborts it: measured on 2026-09-15 as
	// `pg_dump: error: query failed: ERROR:  permission denied for table
	// login_capability_observation` in BOTH postures, with this relation the ONLY one of 306
	// the admin role could not read. What R5 §3 closes is the WRITE boundary — the application
	// role must not delete or rewrite the login-capability history — and that is unchanged and
	// still exact. Read is what every other engine relation already gives this role.
	m.Stmts = append(m.Stmts, loginCapabilityBirthRevokeStmt)
	if len(roles) == 1 && guardMetadataTopologyOf(roles[0]) == guardTopologySplit {
		m.Stmts = append(m.Stmts, loginCapabilitySplitACLStmts(roles[0].App.bindable())...)
	}
	m.After = func(ctx context.Context, tx *sql.Tx) error {
		r, err := loginCapabilityTopology(roles)
		if err != nil {
			return err
		}
		return verifyLoginCapabilityRelation(ctx, tx, dia, r)
	}
	return m
}

// loginCapabilityBirthRevokeStmt reduces every non-owner grantee of the relation created in
// this same transaction — including any role handed it by default privileges — to AT MOST a
// plain SELECT, and removes PUBLIC entirely.
//
// A grantee that arrived holding SELECT keeps exactly SELECT, re-granted by the owner without
// grant option, so the entry in the relation's ACL is one this migration issued rather than one
// it inherited. A grantee that arrived with no read at all gets none. Every other privilege,
// named or not by this file — INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER, and
// MAINTAIN on PostgreSQL 17+ — is revoked, which is why the statement revokes ALL first and
// then re-grants: a REVOKE that enumerates privileges ages badly against a server that adds one.
//
// PUBLIC is the one grantee that is never restored: a read pool is a role an operator
// provisioned and can name, and "everyone" is not one.
const loginCapabilityBirthRevokeStmt = `DO $login_capability_birth$
DECLARE
  grantee_name pg_catalog.text;
  grantee_reads pg_catalog.bool;
BEGIN
  FOR grantee_name, grantee_reads IN
    SELECT CASE WHEN a.grantee OPERATOR(pg_catalog.=) 0 THEN NULL
                ELSE pg_catalog.pg_get_userbyid(a.grantee) END,
           pg_catalog.bool_or(a.privilege_type OPERATOR(pg_catalog.=) 'SELECT')
    FROM pg_catalog.pg_class c
    CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(c.relacl, pg_catalog.acldefault('r', c.relowner))) a
    WHERE c.oid OPERATOR(pg_catalog.=) 'public.login_capability_observation'::pg_catalog.regclass
      AND a.grantee OPERATOR(pg_catalog.<>) c.relowner
    GROUP BY a.grantee
  LOOP
    IF grantee_name IS NULL THEN
      REVOKE ALL PRIVILEGES ON TABLE public.login_capability_observation FROM PUBLIC;
      CONTINUE;
    END IF;
    EXECUTE pg_catalog.format('REVOKE ALL PRIVILEGES ON TABLE public.login_capability_observation FROM %I', grantee_name);
    IF grantee_reads THEN
      EXECUTE pg_catalog.format('GRANT SELECT ON TABLE public.login_capability_observation TO %I', grantee_name);
    END IF;
  END LOOP;
END
$login_capability_birth$`

func loginCapabilityTopology(roles []guardRoles) (guardRoles, error) {
	if len(roles) != 1 {
		return guardRoles{}, fmt.Errorf("sqlstore: core v13 login capability requires exactly one guard roles value, got %d: %w",
			len(roles), store.ErrAppendOnlyACLUnverifiable)
	}
	switch guardMetadataTopologyOf(roles[0]) {
	case guardTopologySingleRole, guardTopologySplit:
		return roles[0], nil
	default:
		return guardRoles{}, fmt.Errorf("sqlstore: core v13 login capability cannot establish its ACL: could not resolve %s: %w",
			describeUnresolvedGuardRoles(roles[0]), store.ErrAppendOnlyACLUnverifiable)
	}
}

func loginCapabilitySplitACLStmts(app string) []string {
	rel := quoteIdent(dialect.EngineSchema) + "." + quoteIdent(dialect.LoginCapabilityObservationTable)
	role := quoteIdent(app)
	return []string{
		"REVOKE ALL PRIVILEGES ON TABLE " + rel + " FROM " + role,
		"GRANT SELECT, INSERT ON TABLE " + rel + " TO " + role,
		"GRANT UPDATE (" + strings.Join(loginCapabilityUpdatableColumns, ", ") + ") ON TABLE " + rel + " TO " + role,
	}
}

// verifyLoginCapabilityPerBoot re-verifies v13 on every boot after the plan applied:
// presence by tracking membership, the exact active tracking identity, then the exact
// relation shape and privileges. Drift is refused, never repaired.
func verifyLoginCapabilityPerBoot(ctx context.Context, mdb dialect.Execer, dia dialect.Dialect, roles guardRoles) error {
	tx, err := mdb.BeginTx(ctx, directoryWriterTxOptions(dia))
	if err != nil {
		return fmt.Errorf("sqlstore: core v13 login capability preflight begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // verifier never commits
	tracked, err := coreVersionIsTracked(ctx, tx, dia, coreLoginCapabilityMigrationVersion)
	if err != nil {
		return fmt.Errorf("sqlstore: core v13 login capability: read tracking membership: %w", err)
	}
	if !tracked {
		return fmt.Errorf("sqlstore: core v13 login capability is not tracked after the compiled plan applied")
	}
	var (
		name, appliedAt, phase string
		reverted               sql.NullString
	)
	// #nosec G202 -- internal constants only.
	if err := tx.QueryRowContext(ctx, dia.Rebind("SELECT name, applied_at, phase, reverted_at FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
		coreLoginCapabilityMigrationVersion).Scan(&name, &appliedAt, &phase, &reverted); err != nil {
		return fmt.Errorf("sqlstore: core v13 login capability: read tracking row: %w", err)
	}
	if name != coreLoginCapabilityMigrationName || strings.TrimSpace(appliedAt) == "" || phase != "expand" || reverted.Valid {
		return fmt.Errorf("sqlstore: core v13 tracking row is not the exact active %q expand record", coreLoginCapabilityMigrationName)
	}
	if dia.Name() == store.EnginePostgres {
		r, err := loginCapabilityTopology([]guardRoles{roles})
		if err != nil {
			return err
		}
		roles = r
	}
	return verifyLoginCapabilityRelation(ctx, tx, dia, roles)
}

func verifyLoginCapabilityRelation(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, roles guardRoles) error {
	if dia.Name() == store.EngineSQLite {
		stmts := dia.LoginCapabilityControlStmts()
		if len(stmts) != 1 {
			return fmt.Errorf("sqlstore: core v13 login capability: SQLite rendered %d statements, want 1", len(stmts))
		}
		if err := verifySQLiteDirectoryWriterRawTable(ctx, tx, dialect.LoginCapabilityObservationTable, stmts[0]); err != nil {
			return fmt.Errorf("sqlstore: core v13 login capability shape: %w", err)
		}
		return nil
	}
	return verifyPostgresLoginCapabilityRelation(ctx, tx, roles)
}

type loginCapabilityColumnShape struct {
	name, typ, collation string
	notNull              bool
}

func verifyPostgresLoginCapabilityRelation(ctx context.Context, tx *sql.Tx, roles guardRoles) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("sqlstore: core v13 login capability relation drift: "+format, args...)
	}
	// The intended owner is the resolved DDL authority: the owner role in split topology,
	// the single role otherwise. The application grantee exists only in split topology.
	var expectedOwner, expectedApp string
	switch guardMetadataTopologyOf(roles) {
	case guardTopologySplit:
		expectedOwner, expectedApp = roles.Owner.bindable(), roles.App.bindable()
		if expectedApp == "" || expectedApp == expectedOwner {
			return fail("split topology needs distinct resolved owner %q and application %q roles", expectedOwner, expectedApp)
		}
	case guardTopologySingleRole:
		expectedOwner = roles.App.bindable()
	default:
		return fail("the role topology is unresolved: %s", describeUnresolvedGuardRoles(roles))
	}
	if expectedOwner == "" {
		return fail("the intended owner role is unresolved")
	}
	var (
		oid, ownerOID                                   int64
		kind, owner                                     string
		partition, rls, forceRLS, policy, trigger, rule bool
		inherit                                         bool
	)
	err := tx.QueryRowContext(ctx, `SELECT c.oid::pg_catalog.int8, c.relowner::pg_catalog.int8, c.relkind::pg_catalog.text, c.relispartition,
  c.relrowsecurity, c.relforcerowsecurity, r.rolname::pg_catalog.text,
  EXISTS (SELECT 1 FROM pg_catalog.pg_policy p WHERE p.polrelid = c.oid),
  EXISTS (SELECT 1 FROM pg_catalog.pg_trigger t WHERE t.tgrelid = c.oid AND NOT t.tgisinternal),
  EXISTS (SELECT 1 FROM pg_catalog.pg_rewrite w WHERE w.ev_class = c.oid),
  EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid = c.oid OR i.inhparent = c.oid)
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_roles r ON r.oid = c.relowner
WHERE n.nspname = $1 AND c.relname = $2`, dialect.EngineSchema, dialect.LoginCapabilityObservationTable).
		Scan(&oid, &ownerOID, &kind, &partition, &rls, &forceRLS, &owner, &policy, &trigger, &rule, &inherit)
	if err != nil {
		return fail("read relation: %w", err)
	}
	if kind != "r" || partition || rls || forceRLS || policy || trigger || rule || inherit {
		return fail("kind=%s partition=%t rls=%t force_rls=%t policy=%t trigger=%t rule=%t inherits=%t",
			kind, partition, rls, forceRLS, policy, trigger, rule, inherit)
	}
	if owner != expectedOwner {
		return fail("relation owner %q, expected the resolved DDL authority %q", owner, expectedOwner)
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.attname::pg_catalog.text, pg_catalog.format_type(a.atttypid, a.atttypmod),
  COALESCE(co.collname::pg_catalog.text, ''), a.attnotnull, a.atthasdef, a.attidentity::pg_catalog.text, a.attgenerated::pg_catalog.text
FROM pg_catalog.pg_attribute a LEFT JOIN pg_catalog.pg_collation co ON co.oid = a.attcollation AND a.attcollation <> 0
WHERE a.attrelid = $1::pg_catalog.oid AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, oid)
	if err != nil {
		return fail("read columns: %w", err)
	}
	want := []loginCapabilityColumnShape{
		{"capability_key", "text", "C", true},
		{"first_observed_at", "timestamp with time zone", "", true},
		{"last_observed_at", "timestamp with time zone", "", true},
		{"last_artifact_version", "text", "C", true},
		{"observation_count", "bigint", "", true},
	}
	var got []loginCapabilityColumnShape
	for rows.Next() {
		var c loginCapabilityColumnShape
		var hasDef bool
		var identity, generated string
		if err := rows.Scan(&c.name, &c.typ, &c.collation, &c.notNull, &hasDef, &identity, &generated); err != nil {
			_ = rows.Close()
			return fail("scan column: %w", err)
		}
		if hasDef || identity != "" || generated != "" {
			_ = rows.Close()
			return fail("column %s has a default, identity or generation", c.name)
		}
		got = append(got, c)
	}
	if err := errorsJoinRows(rows); err != nil {
		return fail("read columns: %w", err)
	}
	if !slices.Equal(got, want) {
		return fail("columns %v, want %v", got, want)
	}
	// WHOLE constraint semantics: the server's own deparse of the compiled body, taken from
	// a TEMP probe in a rolled-back savepoint (the pgCanonicalStateCheckDef mechanism), must
	// equal the relation's constraint set exactly — name, type, validation and definition.
	wantConstraints, err := calibrateLoginCapabilityConstraints(ctx, tx)
	if err != nil {
		return fail("calibrate constraints: %w", err)
	}
	gotConstraints, err := readLoginCapabilityConstraints(ctx, tx, oid)
	if err != nil {
		return fail("read constraints: %w", err)
	}
	if !slices.Equal(gotConstraints, wantConstraints) {
		return fail("constraints %v, want exactly %v", gotConstraints, wantConstraints)
	}
	var indexes int
	if err := tx.QueryRowContext(ctx, `SELECT pg_catalog.count(*) FROM pg_catalog.pg_index WHERE indrelid = $1::pg_catalog.oid`, oid).Scan(&indexes); err != nil {
		return fail("read indexes: %w", err)
	}
	if indexes != 1 {
		return fail("indexes=%d, want only the primary key", indexes)
	}
	if err := verifyPostgresLoginCapabilityACL(ctx, tx, oid, ownerOID, expectedApp); err != nil {
		return fail("%w", err)
	}
	if guardMetadataTopologyOf(roles) != guardTopologySplit {
		return nil
	}
	app := roles.App.bindable()
	if app == "" || owner == app {
		return fail("split topology: application role %q must not own the relation (owner %q)", app, owner)
	}
	// MAINTAIN exists only on PostgreSQL 17+. Naming it on an older server raises an error
	// that aborts the transaction, and an aborted transaction cannot be retried, so the
	// server version decides the statement BEFORE anything is sent.
	var versionNum int
	if err := tx.QueryRowContext(ctx, `SELECT pg_catalog.current_setting('server_version_num')::pg_catalog.int4`).Scan(&versionNum); err != nil {
		return fail("read server version: %w", err)
	}
	maintain := "false"
	if versionNum >= 170000 {
		maintain = "pg_catalog.has_table_privilege($1, $2::pg_catalog.oid, 'MAINTAIN')"
	}
	var p [17]bool
	err = tx.QueryRowContext(ctx, `SELECT
  pg_catalog.has_table_privilege($1, $2::pg_catalog.oid, 'SELECT'),
  pg_catalog.has_table_privilege($1, $2::pg_catalog.oid, 'INSERT'),
  pg_catalog.has_column_privilege($1, $2::pg_catalog.oid, 'last_observed_at', 'UPDATE'),
  pg_catalog.has_column_privilege($1, $2::pg_catalog.oid, 'last_artifact_version', 'UPDATE'),
  pg_catalog.has_column_privilege($1, $2::pg_catalog.oid, 'observation_count', 'UPDATE'),
  pg_catalog.has_table_privilege($1, $2::pg_catalog.oid, 'UPDATE'),
  pg_catalog.has_column_privilege($1, $2::pg_catalog.oid, 'first_observed_at', 'UPDATE'),
  pg_catalog.has_column_privilege($1, $2::pg_catalog.oid, 'capability_key', 'UPDATE'),
  pg_catalog.has_table_privilege($1, $2::pg_catalog.oid, 'DELETE'),
  pg_catalog.has_table_privilege($1, $2::pg_catalog.oid, 'TRUNCATE'),
  pg_catalog.has_table_privilege($1, $2::pg_catalog.oid, 'REFERENCES'),
  pg_catalog.has_any_column_privilege($1, $2::pg_catalog.oid, 'REFERENCES'),
  pg_catalog.has_table_privilege($1, $2::pg_catalog.oid, 'TRIGGER'),
  pg_catalog.has_table_privilege($1, $2::pg_catalog.oid, 'SELECT WITH GRANT OPTION'),
  pg_catalog.has_table_privilege($1, $2::pg_catalog.oid, 'INSERT WITH GRANT OPTION'),
  pg_catalog.has_any_column_privilege($1, $2::pg_catalog.oid, 'UPDATE WITH GRANT OPTION'),
  `+maintain, app, oid).
		Scan(&p[0], &p[1], &p[2], &p[3], &p[4], &p[5], &p[6], &p[7], &p[8], &p[9], &p[10], &p[11], &p[12], &p[13], &p[14], &p[15], &p[16])
	if err != nil {
		return fail("read application privileges: %w", err)
	}
	required := p[0] && p[1] && p[2] && p[3] && p[4]
	forbidden := p[5] || p[6] || p[7] || p[8] || p[9] || p[10] || p[11] || p[12] || p[13] || p[14] || p[15] || p[16]
	if !required || forbidden {
		return fail("application role %q effective privileges %v are not exactly SELECT, INSERT and UPDATE(%s)",
			app, p, strings.Join(loginCapabilityUpdatableColumns, ", "))
	}
	return nil
}

type loginCapabilityConstraint struct {
	name, typ, def string
	validated      bool
}

func readLoginCapabilityConstraints(ctx context.Context, tx *sql.Tx, oid int64) ([]loginCapabilityConstraint, error) {
	rows, err := tx.QueryContext(ctx, `SELECT conname::pg_catalog.text, contype::pg_catalog.text, convalidated, pg_catalog.pg_get_constraintdef(oid)
FROM pg_catalog.pg_constraint WHERE conrelid = $1::pg_catalog.oid ORDER BY conname`, oid)
	if err != nil {
		return nil, err
	}
	var out []loginCapabilityConstraint
	for rows.Next() {
		var c loginCapabilityConstraint
		if err := rows.Scan(&c.name, &c.typ, &c.validated, &c.def); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	return out, errorsJoinRows(rows)
}

// calibrateLoginCapabilityConstraints asks THIS server to deparse the compiled body on a TEMP
// probe inside a savepoint that is always rolled back and released, so no residue remains and
// a probe failure cannot leave the caller's transaction aborted.
func calibrateLoginCapabilityConstraints(ctx context.Context, tx *sql.Tx) ([]loginCapabilityConstraint, error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT login_capability_calibration"); err != nil {
		return nil, err
	}
	out, err := func() ([]loginCapabilityConstraint, error) {
		if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE login_capability_calibration "+dialect.LoginCapabilityPostgresTableBody()); err != nil {
			return nil, err
		}
		var probe int64
		if err := tx.QueryRowContext(ctx, `SELECT 'pg_temp.login_capability_calibration'::pg_catalog.regclass::pg_catalog.oid::pg_catalog.int8`).Scan(&probe); err != nil {
			return nil, err
		}
		return readLoginCapabilityConstraints(ctx, tx, probe)
	}()
	if _, rerr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT login_capability_calibration"); rerr != nil && err == nil {
		err = rerr
	}
	if _, rerr := tx.ExecContext(ctx, "RELEASE SAVEPOINT login_capability_calibration"); rerr != nil && err == nil {
		err = rerr
	}
	return out, err
}

// verifyPostgresLoginCapabilityACL closes the privilege boundary with the verifyDRControlACL
// mechanism, and closes it where verifyDRControlACL closes it: on WRITE. The relation ACL
// (explicit or default) holds the owner's own entries; in split topology exactly the application
// role's SELECT and INSERT; and, for any other named role, at most a plain SELECT — the read
// pool an operator provisions for cross-tenant reads and for the pg_dump that `olivares dr
// backup` runs (deploy/postgres/01-app-role.sql, sqlstore/dbsetup.go grantAdminRead). Column
// ACLs hold exactly the application role's UPDATE on the three mutable columns. Every entry is
// granted by the owner without grant option, and no non-superuser user role other than the owner
// and the application role holds MORE than read — no INSERT, UPDATE, DELETE, TRUNCATE,
// REFERENCES, TRIGGER, MAINTAIN, column-level write or grant option — including through
// membership. PUBLIC is never a grantee.
//
// ADMITTING READ IS NOT A HOLE IN THE CONTRACT THIS RELATION EXISTS FOR. What R5 §3 forbids is a
// second role that can DELETE or rewrite the login-capability history; the relation carries no
// tenant rows and no secret (a fixed capability key, two timestamps, an artifact version and a
// count). Denying read bought nothing and cost every Postgres backup of a v13 estate, because
// pg_dump LOCKs the whole schema in one statement and aborts on the first relation it cannot
// read (measured 2026-09-15, CI run 35004444187 and locally in both postures). It is the DR
// restore control — the closest relation in this tree — that already reads this way:
// verifyDRControlACL admits roles.admin with SELECT and refuses it anything more.
func verifyPostgresLoginCapabilityACL(ctx context.Context, tx *sql.Tx, oid, ownerOID int64, app string) error {
	rows, err := tx.QueryContext(ctx, `SELECT a.grantor::pg_catalog.int8, a.grantee::pg_catalog.int8,
  COALESCE(g.rolname::pg_catalog.text, ''), a.privilege_type::pg_catalog.text, a.is_grantable
FROM pg_catalog.pg_class c
CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(c.relacl, pg_catalog.acldefault('r', c.relowner))) a
LEFT JOIN pg_catalog.pg_roles g ON g.oid = a.grantee
WHERE c.oid = $1::pg_catalog.oid ORDER BY a.grantee, a.privilege_type`, oid)
	if err != nil {
		return fmt.Errorf("read relation ACL: %w", err)
	}
	appGrants := map[string]bool{}
	readPool := map[string]bool{}
	for rows.Next() {
		var grantor, grantee int64
		var name, privilege string
		var grantable bool
		if err := rows.Scan(&grantor, &grantee, &name, &privilege, &grantable); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan relation ACL: %w", err)
		}
		switch {
		case grantor != ownerOID || grantable:
			_ = rows.Close()
			return fmt.Errorf("relation ACL entry %s to %q is not owner-granted without grant option", privilege, name)
		case grantee == ownerOID:
		case app != "" && grantee != 0 && name == app && (privilege == "SELECT" || privilege == "INSERT") && !appGrants[privilege]:
			appGrants[privilege] = true
		// A named read pool: exactly one plain SELECT, and never a second entry for the
		// same role — the one shape the birth statement issues and an operator's
		// `GRANT SELECT ON ALL TABLES IN SCHEMA public TO <admin>` reproduces.
		case grantee != 0 && name != app && privilege == "SELECT" && !readPool[name]:
			readPool[name] = true
		default:
			if grantee == 0 {
				name = "PUBLIC"
			}
			_ = rows.Close()
			return fmt.Errorf("relation ACL grants %s to %q", privilege, name)
		}
	}
	if err := errorsJoinRows(rows); err != nil {
		return fmt.Errorf("read relation ACL: %w", err)
	}
	if app != "" && (!appGrants["SELECT"] || !appGrants["INSERT"]) {
		return fmt.Errorf("relation ACL lacks the application role's SELECT and INSERT: %v", appGrants)
	}
	updatable := map[string]bool{}
	for _, c := range loginCapabilityUpdatableColumns {
		updatable[c] = true
	}
	rows, err = tx.QueryContext(ctx, `SELECT a.attname::pg_catalog.text, x.grantor::pg_catalog.int8, x.grantee::pg_catalog.int8,
  COALESCE(g.rolname::pg_catalog.text, ''), x.privilege_type::pg_catalog.text, x.is_grantable
FROM pg_catalog.pg_attribute a
CROSS JOIN LATERAL pg_catalog.aclexplode(a.attacl) x
LEFT JOIN pg_catalog.pg_roles g ON g.oid = x.grantee
WHERE a.attrelid = $1::pg_catalog.oid AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum, x.grantee, x.privilege_type`, oid)
	if err != nil {
		return fmt.Errorf("read column ACLs: %w", err)
	}
	columnGrants := map[string]bool{}
	for rows.Next() {
		var column, name, privilege string
		var grantor, grantee int64
		var grantable bool
		if err := rows.Scan(&column, &grantor, &grantee, &name, &privilege, &grantable); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan column ACLs: %w", err)
		}
		if app == "" || grantee == 0 || name != app || privilege != "UPDATE" || grantor != ownerOID || grantable || !updatable[column] || columnGrants[column] {
			if grantee == 0 {
				name = "PUBLIC"
			}
			_ = rows.Close()
			return fmt.Errorf("column ACL on %s grants %s to %q (grantable=%t)", column, privilege, name, grantable)
		}
		columnGrants[column] = true
	}
	if err := errorsJoinRows(rows); err != nil {
		return fmt.Errorf("read column ACLs: %w", err)
	}
	if app != "" && len(columnGrants) != len(loginCapabilityUpdatableColumns) {
		return fmt.Errorf("column ACLs grant the application role UPDATE on %v, want exactly %v", columnGrants, loginCapabilityUpdatableColumns)
	}
	// PostgreSQL 17 added MAINTAIN, and its pg_maintain predefined role confers it on every
	// relation without any ACL entry. Naming MAINTAIN on an older server raises an error that
	// aborts the transaction, so the server version selects one of two constant statements
	// before anything is sent.
	var versionNum int
	if err := tx.QueryRowContext(ctx, `SELECT pg_catalog.current_setting('server_version_num')::pg_catalog.int4`).Scan(&versionNum); err != nil {
		return fmt.Errorf("read server version: %w", err)
	}
	effectiveAccess := loginCapabilityEffectiveAccessSQL
	if versionNum >= 170000 {
		effectiveAccess = loginCapabilityEffectiveAccessWithMaintainSQL
	}
	rows, err = tx.QueryContext(ctx, effectiveAccess, oid, ownerOID, app)
	if err != nil {
		return fmt.Errorf("read effective role access: %w", err)
	}
	var others []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan effective role access: %w", err)
		}
		others = append(others, name)
	}
	if err := errorsJoinRows(rows); err != nil {
		return fmt.Errorf("read effective role access: %w", err)
	}
	if len(others) != 0 {
		return fmt.Errorf("roles other than the owner and application role hold more than read access: %v", others)
	}
	return nil
}

// The other-role effective-access audit: any non-superuser user role other than the owner and
// application role holding MORE than a plain read is refused — every write privilege, every
// column-level write, and the grant option on the read itself, however it is reached, including
// through membership. A SELECT-only role is the provisioned read pool and passes; it is the only
// thing this audit stopped refusing, and the only thing `dr backup` needs. The PostgreSQL 17+
// statement adds effective MAINTAIN, which pg_maintain membership confers without an ACL entry
// and which carries LOCK TABLE and the vacuum/analyze family — not a read.
const (
	loginCapabilityEffectiveAccessHead = `SELECT r.rolname::pg_catalog.text FROM pg_catalog.pg_roles r
WHERE NOT r.rolsuper AND r.oid >= 16384 AND r.oid <> $2::pg_catalog.oid AND r.rolname <> $3
  AND (pg_catalog.has_table_privilege(r.oid, $1::pg_catalog.oid, 'INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER')
    OR pg_catalog.has_any_column_privilege(r.oid, $1::pg_catalog.oid, 'INSERT, UPDATE, REFERENCES')
    OR pg_catalog.has_table_privilege(r.oid, $1::pg_catalog.oid, 'SELECT WITH GRANT OPTION')
    OR pg_catalog.has_any_column_privilege(r.oid, $1::pg_catalog.oid, 'SELECT WITH GRANT OPTION')`
	loginCapabilityEffectiveAccessTail = `)
ORDER BY r.rolname`
	loginCapabilityEffectiveAccessSQL             = loginCapabilityEffectiveAccessHead + loginCapabilityEffectiveAccessTail
	loginCapabilityEffectiveAccessWithMaintainSQL = loginCapabilityEffectiveAccessHead + `
    OR pg_catalog.has_table_privilege(r.oid, $1::pg_catalog.oid, 'MAINTAIN')` + loginCapabilityEffectiveAccessTail
)

func errorsJoinRows(rows *sql.Rows) error {
	err := rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return err
}
