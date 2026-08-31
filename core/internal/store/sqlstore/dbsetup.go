// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// dbsetup.go backs `olivares db check` / `olivares db init`. ProbeRolePosture is a
// read-only privilege probe (no schema, no migrations) so an operator can confirm
// BEFORE booting that the engine will accept the DSN. ProvisionPostgres idempotently
// creates the least-privilege role model the engine documents — an application role,
// optionally a SEPARATE owner role that owns the schema and runs DDL (so
// store.Config.OwnerDSN is reachable AND the app role is least-privilege), and an
// optional cross-tenant admin role. It is the in-binary equivalent of running
// deploy/postgres/01-app-role.sql by hand with a superuser DSN.

// safeIdentRE is the conservative SQL-identifier shape provisioning accepts for a
// role or database name. By rejecting anything outside [a-z_][a-z0-9_]* (≤63, the
// Postgres NAMEDATALEN-1 limit) we can interpolate the name into DDL directly
// without quoting — there is no character that could break out of the identifier
// position — instead of attempting to quote an arbitrary identifier client-side.
var safeIdentRE = regexp.MustCompile(store.SafeIdentPattern)

// validIdent returns name if it is a safe plain identifier, else an error naming
// the offending value (never a secret).
func validIdent(kind, name string) (string, error) {
	if !safeIdentRE.MatchString(name) {
		return "", fmt.Errorf("%s %q must be a plain lower-case SQL identifier ([a-z_][a-z0-9_]*, ≤63 chars); provisioning will not quote an arbitrary identifier", kind, name)
	}
	return name, nil
}

// openOwnerPool opens the dedicated owner pool from cfg.OwnerDSN (Postgres only) —
// the role that owns the schema and runs DDL/migrations. It is held to the SAME
// RLS-safe bar as the application role: FORCE row-level security applies to the
// table owner too, so a superuser/BYPASSRLS owner would silently defeat tenant
// isolation. Refuses such a role unless AllowPrivilegedRole is set.
func openOwnerPool(ctx context.Context, dia dialect.Dialect, cfg store.Config, ownerDSN string) (*sql.DB, error) {
	// Pinned to the engine schema like every other pool: this one runs the DDL, so
	// an inherited search_path could create the schema's tables somewhere else
	// entirely — and the guards, the checks and the runtime would each address a
	// different relation.
	odb, err := openPGPinnedToEngineSchema(ownerDSN, cfg.MaxConns)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: open owner pool: %w", err)
	}
	// THE POSTURE IS READ EVEN UNDER AllowPrivilegedRole, and dropping that was a
	// regression this merge introduced. main short-circuited here so a privileged
	// deployment need not read pg_roles; the branch adds an owner-side refusal for
	// session_replication_role='replica' that is deliberately NOT opt-out-able,
	// because migrations would install the append-only and cutover guards through a
	// session that never fires them. Returning early skipped it entirely.
	//
	// ownerPostureError already draws that line correctly: it refuses a replica-pinned
	// owner FIRST and only then honors the flag. So the read stays, and the flag's
	// original purpose is preserved where it belongs — an UNREADABLE posture degrades
	// under the flag instead of refusing, exactly as Open does for the app pool.
	posture, perr := dia.ConnRolePosture(ctx, odb)
	if perr != nil {
		if !cfg.AllowPrivilegedRole {
			_ = odb.Close()
			return nil, fmt.Errorf("sqlstore: owner pool role posture: %w", perr)
		}
		// AllowPrivilegedRole can waive unreadable privilege attributes, not
		// identity. ConnRoleIdentity independently requires session_user ==
		// current_user, so a superuser/owner login hidden behind startup SET ROLE
		// cannot turn this degradation path into a migration authority.
		identity, iderr := dia.ConnRoleIdentity(ctx, odb)
		if iderr != nil {
			_ = odb.Close()
			return nil, fmt.Errorf(
				"sqlstore: owner pool role posture: %w; identity fallback refused: %v",
				perr, iderr,
			)
		}
		slog.Warn("could not read the owner role's privilege attributes; proceeding under AllowPrivilegedRole only after independently proving an unassumed login identity",
			"role", identity, "err", perr)
		return odb, nil
	}
	if err := ownerPostureError(posture, cfg.AllowPrivilegedRole); err != nil {
		_ = odb.Close()
		return nil, err
	}
	return odb, nil
}

// ownerPostureError decides whether an owner-pool role is acceptable. The decision
// lives in a pure function so the POLICY is testable without a PostgreSQL server —
// the pool wiring around it is not, and a test that only exercised the posture
// helper would stay green if this barrier were deleted outright.
//
// Two bars, deliberately asymmetric:
//
//   - Triggers: the owner pool runs every migration, so it is a writer. A session
//     that skips ordinary triggers installs the append-only and cutover guards
//     without ever firing them, and any data statement in that session bypasses
//     them. There is NO AllowPrivilegedRole escape here — an inert RLS backstop is
//     a defensible single-tenant trade, inert triggers never are — so this check
//     comes FIRST, before the opt-out.
//   - RLS: FORCE row-level security applies to the table owner too, so a
//     superuser/BYPASSRLS owner would silently defeat tenant isolation. That one
//     IS opt-out-able for a deliberately single-tenant or throwaway deployment.
func ownerPostureError(posture dialect.RolePosture, allowPrivileged bool) error {
	if posture.TriggersDisabled() {
		return fmt.Errorf(
			"sqlstore: refusing to start: the --owner-dsn role %q has session_replication_role=%q, which makes PostgreSQL SKIP every ordinary trigger — migrations would install the append-only and cutover guards through a session that never fires them. Reset it (ALTER ROLE %s RESET session_replication_role, and check the database-level setting)",
			posture.Role, posture.ReplicationRole, posture.Role)
	}
	if allowPrivileged {
		return nil
	}
	if posture.RLSUnsafe() {
		return fmt.Errorf("sqlstore: refusing to start: the --owner-dsn role %q is %s and SILENTLY BYPASSES row-level security; FORCE RLS protects even the schema owner, so a privileged owner defeats tenant isolation (docs/08 §4). Provision a NOSUPERUSER NOBYPASSRLS owner role (deploy/postgres/01-app-role.sql) or pass --allow-privileged-db-role", posture.Role, posture.Why())
	}
	return nil
}

// checkAppTablePrivileges verifies the connecting (application) role holds the DML
// it needs (SELECT/INSERT/UPDATE/DELETE) on the owner-created tables. Used only in
// the owner/app split, where the app role is a non-owner relying on granted DML —
// has_table_privilege accounts for role membership, PUBLIC and ownership, so it
// reports the EFFECTIVE privilege the engine will have.
//
// It takes the MUTABLE tenant tables only. Append-only tables are deliberately
// excluded and are the business of verifyAppendOnlyACL, which demands the exact
// opposite of them: the engine revokes UPDATE/DELETE/TRUNCATE there, so asserting
// those privileges are present would make the two checks contradict each other on
// the same table. That contradiction was live and merely dormant: this function used
// to sample the first five of the SORTED tenant tables, and audit_events sorts sixth
// — one position outside the sample. Any new append-only table sorting earlier would
// have failed every split boot, and the failure message would have pointed the
// operator at a command that reopens the boundary.
//
// It checks every mutable table rather than a sample, in one round trip: a wider
// answer for less work than the five separate queries it replaces.
func checkAppTablePrivileges(ctx context.Context, db *sql.DB, mutableTables []string) error {
	if len(mutableTables) == 0 {
		return nil
	}
	// Pinned schema, bound names, and a resolved-count check — for the same reason the
	// append-only verification has them. Rewriting this from one QueryRow per table
	// into a single JOIN introduced a way to pass over ZERO rows: with the app role's
	// search_path pointing elsewhere, pg_catalog.current_schema() named a schema holding none of
	// these tables, every row dropped out of the join, and the loop below simply had
	// nothing to object to. Measured: boot accepted a split app role that could not
	// read public.agents at all.
	list, args := tableParams([]any{dialect.EngineSchema}, mutableTables)
	// #nosec G202 -- `list` is tableParams' output: ONLY "$2,$3,…" placeholders (appendonly_acl.go:188-197). The table names travel as bound args, the schema as $1
	q := `SELECT c.relname,
       pg_catalog.has_table_privilege(c.oid, 'SELECT'),
       pg_catalog.has_table_privilege(c.oid, 'INSERT'),
       pg_catalog.has_table_privilege(c.oid, 'UPDATE'),
       pg_catalog.has_table_privilege(c.oid, 'DELETE')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p') AND c.relname IN (` + list + `)`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("sqlstore: app-role privilege check: %w", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var table string
		var canSelect, canInsert, canUpdate, canDelete bool
		if err := rows.Scan(&table, &canSelect, &canInsert, &canUpdate, &canDelete); err != nil {
			return fmt.Errorf("sqlstore: app-role privilege check: %w", err)
		}
		seen++
		var missing []string
		for _, p := range []struct {
			name string
			ok   bool
		}{{"SELECT", canSelect}, {"INSERT", canInsert}, {"UPDATE", canUpdate}, {"DELETE", canDelete}} {
			if !p.ok {
				missing = append(missing, p.name)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("sqlstore: refusing to start: the application role lacks %v on table %q — with a separate --owner-dsn the app role is a non-owner and needs DML granted on the owner's tables. Run `olivares db init` (it sets ALTER DEFAULT PRIVILEGES so the app role gets DML on every owner-created table) or grant SELECT,INSERT,UPDATE,DELETE manually (deploy/postgres/01-app-role.sql). Note that `db init` also bulk-grants on the append-only tables; the next boot revokes that again, which is expected and not an error", missing, table)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlstore: app-role privilege check: %w", err)
	}
	if seen != len(mutableTables) {
		return fmt.Errorf(
			"sqlstore: refusing to start: %w — resolved %d of %d tenant tables in schema %q. The engine creates its tables there, so a privilege answer covering fewer is not an answer at all",
			store.ErrTenantTablesUnresolved, seen, len(mutableTables), dialect.EngineSchema)
	}
	return nil
}

// checkSchemaAccess verifies the application role can actually USE the engine's
// schema.
//
// Table privileges alone do not prove a role can read or append: PostgreSQL requires
// USAGE on the containing schema first, and without it every query fails with
// "permission denied for schema" no matter how complete the table ACL looks.
// Provisioning grants it explicitly (grantAppDML), which is precisely why an operator
// who revokes it — or who provisioned by hand — would otherwise get a boot that
// reports the evidence tables readable and appendable and then cannot read a single
// row. Measured: table SELECT/INSERT true, schema USAGE false, boot accepted, first
// productive query refused.
func checkSchemaAccess(ctx context.Context, db *sql.DB) error {
	var canUse bool
	if err := db.QueryRowContext(ctx,
		"SELECT pg_catalog.has_schema_privilege($1, 'USAGE')", dialect.EngineSchema).Scan(&canUse); err != nil {
		return fmt.Errorf("sqlstore: schema access check: %w", err)
	}
	if !canUse {
		return fmt.Errorf(
			"sqlstore: refusing to start: %w — the application role has no USAGE on schema %q, so every query against the engine's tables fails regardless of their table privileges. Grant it (GRANT USAGE ON SCHEMA %s TO <app role>) or re-run `olivares db init`",
			store.ErrEngineSchemaUnusable, dialect.EngineSchema, dialect.EngineSchema)
	}
	return nil
}

// ProbeRolePosture opens a TRANSIENT connection for cfg (no migrations, no schema,
// no admin pool) and reports the connecting role's RLS posture. A connection or
// auth failure is captured in the returned RolePosture (Reachable=false), not as a
// Go error, so `db check` can report every DSN it was given. The returned error is
// reserved for a programmer-level fault (an unsupported engine).
func ProbeRolePosture(ctx context.Context, cfg store.Config) (store.RolePosture, error) {
	dia, ok := dialect.New(cfg.Engine)
	if !ok {
		return store.RolePosture{}, fmt.Errorf("sqlstore: unsupported engine %q", cfg.Engine)
	}
	out := store.RolePosture{Engine: cfg.Engine}
	db, err := openDB(cfg)
	if err != nil {
		out.Err = err.Error()
		return out, nil
	}
	defer db.Close() //nolint:errcheck // transient probe pool
	posture, perr := dia.ConnRolePosture(ctx, db)
	if perr != nil {
		out.Err = perr.Error()
		return out, nil
	}
	out.Reachable = true
	out.Role = posture.Role
	out.Superuser = posture.Superuser
	out.BypassRLS = posture.BypassRLS
	// Carry the replication role too: Open refuses a connection whose ordinary
	// triggers would not fire, so a probe that dropped this field reported a
	// posture the engine will reject.
	out.ReplicationRole = posture.ReplicationRole
	return out, nil
}

// ProbeTargetOccupied opens a TRANSIENT connection for cfg (no migrations, no
// schema, no admin pool) and reports whether the database already holds relations
// of its own — i.e. whether writing into it would land ON TOP of something.
//
// It exists for `dr restore`, which had to decide whether a restore REPLACES an
// estate and could only look at the local filesystem. For Postgres the filesystem
// says nothing: the estate lives at the far end of a DSN, and under external key
// custody (BYOK/CMEK) the data dir is legitimately empty, so every live Postgres
// database classified as "clean target".
//
// It answers a deliberately COARSE question — "is there anything here" — rather
// than "is there an OLIVARES estate here". Restoring a whole database dump on top
// of some other application's tables is the same destructive act, and a probe that
// looked only for the engine's own tables would wave that one through. The engine
// schema is excluded from neither side for the same reason.
//
// An unreachable or unreadable target returns an ERROR, never false: the caller
// must be able to tell "it is empty" from "I could not look", because collapsing
// the two is exactly how the filesystem classifier failed open.
func ProbeTargetOccupied(ctx context.Context, cfg store.Config) (bool, error) {
	if cfg.Engine != store.EnginePostgres {
		return false, fmt.Errorf("sqlstore: ProbeTargetOccupied supports the postgres engine only, got %q", cfg.Engine)
	}
	db, err := openDB(cfg)
	if err != nil {
		return false, fmt.Errorf("sqlstore: open target: %w", err)
	}
	defer db.Close() //nolint:errcheck // transient probe pool
	var n int
	// IT DOES NOT ENUMERATE WHAT COUNTS AS STATE, and the first version did. That
	// version listed five relkinds ('r','p','m','v','S') chosen while thinking about
	// the estate's own tables, and an external contrast measured what the list
	// forgot: on PostgreSQL 16.14 a database holding a schema, a function, an
	// extension, a FOREIGN TABLE ('f') and a large object counted ZERO. An
	// enumeration of "state" silently defines everything outside it as emptiness,
	// which is the same failure the filesystem classifier made one level up.
	//
	// So: EVERY relkind, in every namespace that is not the server's own furniture
	// (information_schema and the reserved pg_* ones — pg_catalog, pg_toast, a
	// session's pg_temp_N), plus schemas that exist at all beyond `public`, plus
	// routines. Dependent objects (indexes, TOAST tables, composite types) ride along
	// with their parents rather than being filtered out; over-counting a relation that
	// only exists because another one does costs nothing, because the parent already
	// made the answer "occupied".
	//
	// Routines EXCLUDE extension-owned ones (pg_depend deptype 'e'). A database that
	// `olivares db init` has prepared carries CREATE EXTENSION vector
	// (deploy/postgres/01-app-role.sql) and nothing else — that is a provisioned
	// target, not an estate, and demanding a declaration for it would put two flags on
	// the documented restore path. A control that also fires where there is nothing to
	// protect is friction an operator routes around in an outage.
	//
	// Still not counted, and named rather than left to be discovered: large objects
	// (pg_largeobject_metadata), which a pg_dump of this product never produces, and
	// server-scoped objects that belong to the cluster and not to this database. An
	// operator restoring into a database whose only content is a large object gets no
	// declaration prompt.
	const q = `SELECT
	  (SELECT count(*) FROM pg_catalog.pg_class c
	     JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	    WHERE n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%')
	+ (SELECT count(*) FROM pg_catalog.pg_namespace n
	    WHERE n.nspname NOT IN ('public','information_schema') AND n.nspname NOT LIKE 'pg\_%')
	+ (SELECT count(*) FROM pg_catalog.pg_proc p
	     JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
	    WHERE n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg\_%'
	      AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
	                       WHERE d.objid = p.oid AND d.classid = 'pg_proc'::regclass
	                         AND d.deptype = 'e'))`
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return false, fmt.Errorf("sqlstore: probe target occupancy: %w", err)
	}
	return n > 0, nil
}

const (
	attrsUnprivileged = "NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION"
	attrsAdmin        = "NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION"
	redactedPassword  = "'********'"
)

// RenderProvisionSQL renders the provisioning steps for display (`db init
// --print-sql`) WITHOUT a database connection. Password literals are redacted; the
// statements are the unconditional CREATE/GRANT forms (the executor applies them
// idempotently, gated on existence). It validates the spec's identifiers so a bad
// name is caught before anything runs.
func RenderProvisionSQL(spec store.PgProvisionSpec) ([]store.PgProvisionStep, error) {
	if _, err := validIdent("database", spec.Database); err != nil {
		return nil, err
	}
	if _, err := validIdent("app role", spec.App.Name); err != nil {
		return nil, err
	}
	owner := spec.App // single-role: the app role owns the database
	if spec.HasSplitOwner() {
		if _, err := validIdent("owner role", spec.Owner.Name); err != nil {
			return nil, err
		}
		owner = spec.Owner
	}
	if spec.Admin != nil {
		if _, err := validIdent("admin role", spec.Admin.Name); err != nil {
			return nil, err
		}
	}

	var steps []store.PgProvisionStep
	add := func(label, sql string, secret bool) {
		steps = append(steps, store.PgProvisionStep{Label: label, SQL: sql, Secret: secret})
	}

	if spec.HasSplitOwner() {
		add("owner role (owns the schema, runs DDL; least-privilege)",
			fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD %s %s;", owner.Name, redactedPassword, attrsUnprivileged), true)
	}
	add("application role (runtime traffic; NOBYPASSRLS so RLS is enforced)",
		fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD %s %s;", spec.App.Name, redactedPassword, attrsUnprivileged), true)
	add("application database (owned by the owner role)",
		fmt.Sprintf("CREATE DATABASE %s OWNER %s;", spec.Database, owner.Name), false)
	// EXPLICIT, because owning the database is not always enough: a NOINHERIT DDL role on 15,
	// or an upgraded/restored database whose `public` kept an older owner, does not reach it.
	// (A clean 15 with an INHERIT owner does — an earlier version of this note said otherwise
	// and was corrected by measurement.) PUBLIC is left alone: 15 removed its default CREATE
	// and this does not restore it — nor does it revoke one an existing database already
	// carries.
	add("owner role: USAGE + CREATE on the engine schema (run IN the new database)",
		fmt.Sprintf("\\connect %s\nGRANT USAGE, CREATE ON SCHEMA %s TO %s;",
			spec.Database, dialect.EngineSchema, owner.Name), false)

	if spec.HasSplitOwner() {
		// In the split, the app role owns nothing; it gets DML on the owner's
		// CURRENT and FUTURE tables. ALTER DEFAULT PRIVILEGES set here BEFORE the
		// engine's first boot is what makes the app role usable the moment the owner
		// creates the schema (no manual GRANT after every migration).
		add("app role: connect",
			fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s;", spec.Database, spec.App.Name), false)
		// One step, one transaction — the same boundary the executor uses. The bulk
		// grant cannot tell an append-only table from a mutable one, so it hands
		// mutation back on the evidence tables; the DO block takes it away again. An
		// operator who copied the grants without the revoke, or committed between
		// them, would leave (or briefly publish) a database in which evidence is
		// mutable, so they are printed as one indivisible unit.
		add("app role: DML on the owner's future + existing tables, minus mutation on the append-only (evidence) ones — ONE TRANSACTION",
			fmt.Sprintf("BEGIN;\nGRANT USAGE ON SCHEMA public TO %s;\nALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public\n  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s;\nALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public\n  GRANT USAGE, SELECT ON SEQUENCES TO %s;\nGRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %s;\nGRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %s;\n%s;\nCOMMIT;",
				spec.App.Name, owner.Name, spec.App.Name, owner.Name, spec.App.Name, spec.App.Name, spec.App.Name,
				dialect.AppendOnlyCatalogRevokeStmt(spec.App.Name, "public")), false)
	}

	if spec.Admin != nil {
		add("cross-tenant admin role (BYPASSRLS, NOSUPERUSER; for --admin-dsn)",
			fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD %s %s;", spec.Admin.Name, redactedPassword, attrsAdmin), true)
		add("admin role: read-only on the owner's future + existing tables",
			fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s;\nGRANT USAGE ON SCHEMA public TO %s;\nGRANT SELECT ON ALL TABLES IN SCHEMA public TO %s;\nALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public\n  GRANT SELECT ON TABLES TO %s;",
				spec.Database, spec.Admin.Name, spec.Admin.Name, spec.Admin.Name, owner.Name, spec.Admin.Name), false)
	}
	return steps, nil
}

// ProvisionPostgres applies spec idempotently against superuserDSN (a superuser /
// maintenance DSN, e.g. postgres://postgres@host/postgres). When execute is false it
// only renders the steps (a dry run). On execute it creates/updates the roles, the
// database and the grants, then RECONNECTS as each provisioned role (when a password
// was supplied) to verify the engine will accept it — the same ConnRolePosture guard
// the boot uses. Identifiers are validated; passwords are quoted SERVER-SIDE via
// pg_catalog.format('%L') from a bound parameter, so a password never enters a Go-assembled
// SQL string.
func ProvisionPostgres(ctx context.Context, superuserDSN string, spec store.PgProvisionSpec, execute bool) (store.PgProvisionResult, error) {
	steps, err := RenderProvisionSQL(spec)
	if err != nil {
		return store.PgProvisionResult{}, err
	}
	res := store.PgProvisionResult{Steps: steps, Executed: execute}
	if !execute {
		return res, nil
	}
	if spec.HasSplitOwner() && spec.Owner.Password == "" {
		// We could ALTER-keep an existing owner password, but on first provisioning a
		// passwordless owner is unusable — fail loudly rather than create a login role
		// with no password.
		return res, fmt.Errorf("db init: a split owner role needs a password (--owner-password / --owner-password-file)")
	}

	superCfg, err := pgx.ParseConfig(superuserDSN)
	if err != nil {
		return res, fmt.Errorf("db init: parse --superuser-dsn: %w", err)
	}
	// Maintenance connection (the superuser DSN's own database) for the role and
	// CREATE DATABASE statements, on a TRUSTED search_path — see openOnTrustedPath.
	maint, err := openOnTrustedPath(superCfg)
	if err != nil {
		return res, fmt.Errorf("db init: open maintenance connection: %w", err)
	}
	defer maint.Close() //nolint:errcheck
	if err := maint.PingContext(ctx); err != nil {
		return res, fmt.Errorf("db init: connect with --superuser-dsn: %w", err)
	}

	owner := spec.App
	if spec.HasSplitOwner() {
		owner = spec.Owner
	}
	// ROLES UNDER ONE CLUSTER-WIDE LOCK. See provisionRolesLockKey: roles are a
	// cluster object, so an isolated database does not isolate this, and upsertRole is
	// a check-then-act. CREATE/ALTER ROLE are transactional in PostgreSQL, so they can
	// live here; CREATE DATABASE is NOT, which is why ensureDatabase stays outside.
	if err := func() error {
		tx, err := maint.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("db init: begin role provisioning: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck // committed below; the rollback is the error path
		if _, err := tx.ExecContext(ctx,
			`SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
			provisionRolesLockKey); err != nil {
			return fmt.Errorf("db init: take the role provisioning lock: %w", err)
		}
		if spec.HasSplitOwner() {
			if err := upsertRole(ctx, tx, owner.Name, attrsUnprivileged, owner.Password); err != nil {
				return fmt.Errorf("db init: provision owner role %q: %w", owner.Name, err)
			}
		}
		if err := upsertRole(ctx, tx, spec.App.Name, attrsUnprivileged, spec.App.Password); err != nil {
			return fmt.Errorf("db init: provision app role %q: %w", spec.App.Name, err)
		}
		if spec.Admin != nil {
			if err := upsertRole(ctx, tx, spec.Admin.Name, attrsAdmin, spec.Admin.Password); err != nil {
				return fmt.Errorf("db init: provision admin role %q: %w", spec.Admin.Name, err)
			}
		}
		return tx.Commit()
	}(); err != nil {
		return res, err
	}
	if err := ensureDatabase(ctx, maint, spec.Database, owner.Name); err != nil {
		return res, fmt.Errorf("db init: provision database %q: %w", spec.Database, err)
	}

	// Schema-scoped grants run IN the target database (USAGE/DEFAULT PRIVILEGES are
	// database-local), so reconnect there.
	//
	// UNCONDITIONALLY, and it used to be `if HasSplitOwner() || Admin != nil`. Under a
	// single role with no admin this block never ran, so nothing was ever granted on the
	// schema — the owner was expected to reach `public` through owning the database.
	//
	// THE PRECONDITION IS NARROWER THAN AN EARLIER VERSION OF THIS COMMENT CLAIMED, and the
	// correction came from an independent contrast. That version said the database owner
	// cannot create in `public` on 15 and can from 16. That is FALSE for a clean 15 database
	// with an INHERIT owner — measured: schema owner pg_database_owner, owner_create=true,
	// CREATE TABLE succeeds. The first measurement that said otherwise was taken against a
	// role a test in this same session had left NOINHERIT, and the "control" that seemed to
	// prove it was not this branch's doing was reading the same contaminated role.
	//
	// What IS true, measured on a clean database with a NOINHERIT owner:
	//
	//	pg_has_role(owner, pg_database_owner, USAGE) / owner CREATE on public
	//	15.18 -> false / false      16.14 -> true / true
	//	17.10 -> true  / true       18.4  -> true / true
	//
	// PostgreSQL 16 changed inheritance to be stored per membership, which is why the
	// implicit membership only reaches a NOINHERIT owner from 16. So the grant earns its
	// place for two real cases and not for "every 15 install": a legitimately NOINHERIT DDL
	// role on 15, and an upgraded or restored database whose `public` kept an older owner or
	// ACL. It is idempotent where the implicit membership already provides it.
	{
		target, closeTarget, err := openOnDatabase(superCfg, spec.Database)
		if err != nil {
			return res, fmt.Errorf("db init: open target database %q: %w", spec.Database, err)
		}
		defer closeTarget()
		if err := grantOwnerSchemaCreate(ctx, target, owner.Name); err != nil {
			return res, fmt.Errorf("db init: grant the owner CREATE on the engine schema: %w", err)
		}
		if spec.HasSplitOwner() {
			if err := grantAppDML(ctx, target, spec.Database, owner.Name, spec.App.Name); err != nil {
				return res, fmt.Errorf("db init: grant app DML: %w", err)
			}
		}
		if spec.Admin != nil {
			if err := grantAdminRead(ctx, target, spec.Database, owner.Name, spec.Admin.Name); err != nil {
				return res, fmt.Errorf("db init: grant admin read: %w", err)
			}
		}
	}

	// Verify: reconnect as each provisioned role (when we hold its password) and
	// confirm the posture the engine will require. This catches a fat-fingered
	// password or a privilege drift before the operator ever runs `serve`.
	res.AppPosture = verifyRole(ctx, superCfg, spec.App, spec.Database)
	if spec.HasSplitOwner() {
		res.OwnerPosture = verifyRole(ctx, superCfg, spec.Owner, spec.Database)
	}
	if spec.Admin != nil {
		res.AdminPosture = verifyRole(ctx, superCfg, *spec.Admin, spec.Database)
	}

	// Ready-to-use, password-free DSN hints (host/port/sslmode from the superuser
	// connection). The operator stores each password in a 0600 file and references
	// it as --dsn=file:<path>.
	res.AppDSNHint = dsnHint(superCfg, spec.App.Name, spec.Database, spec.SSLMode)
	if spec.HasSplitOwner() {
		res.OwnerDSNHint = dsnHint(superCfg, spec.Owner.Name, spec.Database, spec.SSLMode)
	}
	if spec.Admin != nil {
		res.AdminDSNHint = dsnHint(superCfg, spec.Admin.Name, spec.Database, spec.SSLMode)
	}
	return res, nil
}

// dsnHint renders a password-free libpq URL for role@host:port/db?sslmode=…, taking
// host/port from the superuser connection so the operator gets a copy-paste DSN.
func dsnHint(superCfg *pgx.ConnConfig, role, dbName, sslmode string) string {
	if sslmode == "" {
		sslmode = "verify-full"
	}
	host := superCfg.Host
	port := superCfg.Port
	if port == 0 {
		port = 5432
	}
	return fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=%s", role, host, port, dbName, sslmode)
}

// upsertRole creates the role (with a password) or, when it already exists,
// re-asserts its attributes (and rotates the password when one is supplied). The
// password is bound into a server-side pg_catalog.format('%L'), never concatenated in Go.
// execQuerier is what upsertRole needs, and it exists so the role DDL can run on a
// *sql.Tx instead of the pool. Both *sql.DB and *sql.Tx satisfy it.
type execQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// provisionRolesLockKey serializes ROLE provisioning cluster-wide, for the same
// reason migrateLockKey serializes schema DDL: N nodes booting in parallel (the
// Helm chart's podManagementPolicy: Parallel) must not race each other.
//
// Roles are a CLUSTER object, not a database one, so isolating the database does
// NOT isolate this: two provisioners against the same cluster contend on the same
// pg_authid tuple even when their databases are unrelated. upsertRole is a
// check-then-act — SELECT EXISTS, then CREATE or ALTER — and the window between
// the two is exactly wide enough for both to read "absent" and both to CREATE, or
// for both to ALTER the same tuple and for one to be told
// `tuple concurrently updated (XX000)`.
//
// Measured 2026-08-09 on mainline-ci, where it turned main red: the sqlstore suite
// runs its cases with t.Parallel() against one cluster, and the provisioning of the
// shared `olivares_app` role raised exactly that error. It is not a test-only
// defect: the same race is two nodes booting together.
//
// The lock is taken with pg_advisory_XACT_lock rather than the session form, so it
// is released by COMMIT or ROLLBACK and cannot be leaked by an early return.
const provisionRolesLockKey = "olivares.provision.roles.v1"

func upsertRole(ctx context.Context, db execQuerier, name, attrs, password string) error {
	if _, err := validIdent("role", name); err != nil {
		return err
	}
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)", name).Scan(&exists); err != nil {
		return err
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	if !exists && password == "" {
		return fmt.Errorf("cannot create role %q without a password", name)
	}
	if password == "" {
		// Existing role, keep the password; only re-assert the attributes.
		_, err := db.ExecContext(ctx, fmt.Sprintf("ALTER ROLE %s WITH LOGIN %s", name, attrs))
		return err
	}
	// The template carries the validated identifier and the controlled attribute
	// list inline; only the password is a server-side %L from a bound parameter.
	tmpl := fmt.Sprintf("%s ROLE %s WITH LOGIN PASSWORD %%L %s", verb, name, attrs)
	var ddl string
	if err := db.QueryRowContext(ctx, "SELECT pg_catalog.format($1::pg_catalog.text, $2::pg_catalog.text)", tmpl, password).Scan(&ddl); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, ddl)
	return err
}

// ensureDatabase creates the database owned by ownerName when absent, else
// re-asserts its owner. CREATE DATABASE cannot run inside a transaction, so this
// runs as its own statement on the maintenance connection.
func ensureDatabase(ctx context.Context, db *sql.DB, name, ownerName string) error {
	if _, err := validIdent("database", name); err != nil {
		return err
	}
	if _, err := validIdent("owner role", ownerName); err != nil {
		return err
	}
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		_, err := db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s OWNER %s", name, ownerName))
		return err
	}
	_, err := db.ExecContext(ctx, fmt.Sprintf("ALTER DATABASE %s OWNER TO %s", name, ownerName))
	return err
}

// grantOwnerSchemaCreate gives the role that runs the DDL the two privileges it needs on
// the engine's schema, explicitly rather than by inference from database ownership.
//
// It is not always redundant with owning the database, and the cases where it is not are
// narrower than first claimed here. A clean PostgreSQL 15 database whose owner is INHERIT
// does reach `public` — measured. What does NOT reach it is a NOINHERIT owner on 15
// (PostgreSQL 16 changed inheritance to be stored per membership), and neither does an
// upgraded or restored database whose `public` kept an older owner or ACL. In both the
// engine's first migration fails with 42501 on a database `db init` reported as
// provisioned. Both privileges are named: USAGE because every object reference needs it,
// CREATE because migrations create relations.
//
// Only the OWNER gets this. PUBLIC is deliberately untouched — PostgreSQL 15 removed its
// default CREATE on `public` and restoring it here would hand every role in the cluster the
// ability to create objects in the engine's schema, which is a boundary this engine spends
// a great deal of effort holding.
func grantOwnerSchemaCreate(ctx context.Context, db *sql.DB, ownerName string) error {
	if _, err := validIdent("owner role", ownerName); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx,
		fmt.Sprintf("GRANT USAGE, CREATE ON SCHEMA %s TO %s", dialect.EngineSchema, ownerName))
	return err
}

// grantAppDML wires the split: the app role gets DML on the owner's existing tables
// and, via ALTER DEFAULT PRIVILEGES, on every table the owner creates later (the
// engine's migrations). Run IN the target database.
func grantAppDML(ctx context.Context, db *sql.DB, dbName, ownerName, appName string) error {
	// CONNECT is kept outside the transaction below only because it is database-wide
	// and grants no ability to touch a row, so its ordering cannot expose evidence.
	// (It IS transactional — a claim to the contrary here was wrong and was measured:
	// BEGIN; GRANT CONNECT; ROLLBACK leaves the privilege absent.)
	if _, err := db.ExecContext(ctx, fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", dbName, appName)); err != nil {
		return fmt.Errorf("GRANT: %w", err)
	}
	// The rest is ONE transaction, on purpose. The bulk grant is over EXISTING tables
	// and cannot tell an append-only table from a mutable one, so on a re-run — which
	// this command is designed for — it hands UPDATE/DELETE back on every append-only
	// table the engine had revoked them from. Committing that grant separately would
	// publish a state in which evidence is mutable, visible to any concurrently
	// running node, and would make a boot verification racing this run refuse for a
	// reason that is about to stop being true.
	stmts := []string{
		fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s", appName),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s", ownerName, appName),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO %s", ownerName, appName),
		fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %s", appName),
		fmt.Sprintf("GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %s", appName),
		// Take mutation back on the append-only tables, in the SAME transaction and
		// against the SAME schema the grants above name explicitly. Resolving the
		// schema from search_path here instead would scan somewhere else entirely on a
		// maintenance connection that has one, and repair nothing at all.
		dialect.AppendOnlyCatalogRevokeStmt(appName, "public"),
	}
	return execAllTx(ctx, db, stmts)
}

// grantAdminRead grants the cross-tenant admin role read-only on the owner's tables
// (current + future). Run IN the target database.
func grantAdminRead(ctx context.Context, db *sql.DB, dbName, ownerName, adminName string) error {
	stmts := []string{
		fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", dbName, adminName),
		fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s", adminName),
		fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA public TO %s", adminName),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT ON TABLES TO %s", ownerName, adminName),
	}
	return execAll(ctx, db, stmts)
}

func execAll(ctx context.Context, db *sql.DB, stmts []string) error {
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", strings.SplitN(s, " ", 3)[0], err)
		}
	}
	return nil
}

// execAllTx runs stmts in one transaction, so a sequence whose INTERMEDIATE states
// are unsafe never becomes visible to anyone else. GRANT/REVOKE are transactional in
// PostgreSQL, so this is a real atomic boundary and not merely a tidy one.
func execAllTx(ctx context.Context, db *sql.DB, stmts []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", strings.SplitN(s, " ", 3)[0], err)
		}
	}
	return tx.Commit()
}

// openOnDatabase opens a connection to dbName reusing the superuser config's host,
// port, credentials and TLS settings. pgx config-swapping handles both URL and
// keyword DSN forms robustly (vs string-rewriting the DSN).
func openOnDatabase(superCfg *pgx.ConnConfig, dbName string) (*sql.DB, func(), error) {
	cfg := superCfg.Copy()
	cfg.Database = dbName
	db, err := openOnTrustedPath(cfg)
	if err != nil {
		return nil, func() {}, err
	}
	return db, func() { _ = db.Close() }, nil
}

// trustedProvisioningPath is the search_path every provisioning connection runs on: the
// catalog and NOTHING ELSE.
//
// Provisioning is the one place where an OPERATOR SUPERUSER connects to a database an
// UNTRUSTED role may own — in the default single-role topology the application role owns
// it — and a database owner may set a database-wide search_path that later sessions
// inherit, superuser sessions included. Name resolution then covers far more than
// functions: relations, operators, casts and TYPES. A `public.text` DOMAIN is selected by
// an unqualified `::text` cast, its CHECK runs on conversion, and a CHECK may call a
// function that is SECURITY INVOKER by default — so it executes as the operator
// superuser. Qualifying every function does not close that; qualifying every type,
// relation, operator and cast is an unbounded obligation nobody will keep.
//
// Removing writable schemas from the path closes all of it at once, and it is safe here
// because provisioning names its schemas explicitly (the grants, and
// AppendOnlyCatalogRevokeStmt's `sch` argument) and otherwise touches only catalog
// objects. It also drops the RegisterConnConfig global map this used to need, which held
// the password.
const trustedProvisioningPath = "pg_catalog"

// openOnTrustedPath opens a provisioning pool whose every physical connection is pinned to
// trustedProvisioningPath before it runs anything else, and refuses the connection if the
// server does not confirm the value.
func openOnTrustedPath(cfg *pgx.ConnConfig) (*sql.DB, error) {
	return stdlib.OpenDB(*pinBeforeValidate(cfg, trustedProvisioningPath),
		stdlib.OptionAfterConnect(pinTrustedPath)), nil
}

func pinTrustedPath(ctx context.Context, conn *pgx.Conn) error {
	var got string
	// pg_catalog-qualified because this statement is itself resolved on the untrusted
	// path it is about to replace.
	if err := conn.QueryRow(ctx, "SELECT pg_catalog.set_config('search_path', $1, false)", trustedProvisioningPath).Scan(&got); err != nil {
		return fmt.Errorf("sqlstore: pin provisioning search_path: %w", err)
	}
	if got != trustedProvisioningPath {
		return fmt.Errorf("sqlstore: provisioning search_path reads back as %q after pinning it to %q", got, trustedProvisioningPath)
	}
	return nil
}

// pinBeforeValidate returns a copy of cfg whose ValidateConnect installs the search_path
// pin FIRST and only then delegates to whatever validator the DSN asked for.
//
// An AfterConnect hook is too late to be the only pin. pgx runs ValidateConnect inside
// ConnectConfig (pgconn/pgconn.go:514), and stdlib calls AfterConnect only afterwards
// (stdlib/sql.go:271,275). A DSN with target_session_attrs=primary|standby|prefer-standby
// installs a validator that executes `select pg_is_in_recovery()` UNQUALIFIED
// (pgconn/config.go:503-508, :1037,:1052,:1067). On a database whose owner set a hostile
// search_path — which in the default single-role topology is the application role — that
// resolves to a function that role defined, running with INVOKER rights before anything
// this package controls has executed. On a provisioning pool those rights are the
// operator superuser's.
//
// The AfterConnect pin is kept as well: it is the pool-admission check, it covers DSNs
// with no validator at all, and it re-reads the value pgx itself will use.
func pinBeforeValidate(cfg *pgx.ConnConfig, path string) *pgx.ConnConfig {
	c := cfg.Copy()
	prior := c.ValidateConnect
	c.ValidateConnect = func(ctx context.Context, pgConn *pgconn.PgConn) error {
		if err := pinSearchPathOnPgConn(ctx, pgConn, path); err != nil {
			return err
		}
		if prior != nil {
			return prior(ctx, pgConn)
		}
		return nil
	}
	return c
}

// pinSearchPathOnPgConn installs the pin at the raw-connection stage, where no *pgx.Conn
// exists yet.
//
// path is a package CONSTANT, never operator input, and the guard below keeps that true
// rather than leaving it to a reader's assumption — so interpolating it is safe and the
// statement stays one simple-protocol round trip.
//
// Not because parameters are unavailable: pgconn.ExecParams exists at this stage too. An
// earlier revision of this comment claimed the simple protocol was the only option, which
// was false. The choice is deliberate and narrow; if this value ever stops being a
// constant, use ExecParams instead of widening the guard.
func pinSearchPathOnPgConn(ctx context.Context, c *pgconn.PgConn, path string) error {
	if !safeIdentRE.MatchString(path) {
		return fmt.Errorf("sqlstore: refusing to pin a search_path that is not a plain identifier: %q", path)
	}
	res, err := c.Exec(ctx, "SELECT pg_catalog.set_config('search_path', '"+path+"', false)").ReadAll()
	if err != nil {
		return fmt.Errorf("sqlstore: pin search_path before connection validation: %w", err)
	}
	if len(res) == 0 || len(res[0].Rows) == 0 || len(res[0].Rows[0]) == 0 {
		return fmt.Errorf("sqlstore: pinning search_path to %q returned no value to verify", path)
	}
	if got := string(res[0].Rows[0][0]); got != path {
		return fmt.Errorf("sqlstore: search_path reads back as %q after pinning it to %q", got, path)
	}
	return nil
}

// verifyRole reconnects to dbName as role and reports its posture, so db init can
// confirm the engine will accept it. Returns nil when no password is held (an
// existing role whose password we kept — we cannot authenticate to verify).
func verifyRole(ctx context.Context, superCfg *pgx.ConnConfig, role store.PgRole, dbName string) *store.RolePosture {
	if role.Password == "" {
		return nil
	}
	cfg := superCfg.Copy()
	cfg.User = role.Name
	cfg.Password = role.Password
	cfg.Database = dbName
	// Trusted path here too. This connects AS the provisioned role, to a database that
	// role may own, and its answer is the posture `db init` REPORTS to the operator — a
	// forged answer is a misleading all-clear, not just a wrong log line.
	db, err := openOnTrustedPath(cfg)
	if err != nil {
		return &store.RolePosture{Engine: store.EnginePostgres, Err: err.Error()}
	}
	defer db.Close() //nolint:errcheck
	dia, _ := dialect.New(store.EnginePostgres)
	posture, perr := dia.ConnRolePosture(ctx, db)
	if perr != nil {
		return &store.RolePosture{Engine: store.EnginePostgres, Err: perr.Error()}
	}
	return &store.RolePosture{
		Engine: store.EnginePostgres, Reachable: true,
		Role: posture.Role, Superuser: posture.Superuser, BypassRLS: posture.BypassRLS,
	}
}
