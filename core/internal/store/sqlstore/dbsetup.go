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
	"regexp"
	"strings"
	"time"

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

// ProbeConnAuthority opens a TRANSIENT connection for cfg (no migrations, no
// schema change, no admin pool) and reports what that connection actually is and
// may do: the connecting role's posture, whether the engine schema is there and
// grants it CREATE, whether the session can write at all, and which database on
// which cluster it reached.
//
// It exists for the DR pre-flight, which has to answer "may this connection do
// the restore" BEFORE pg_restore writes an estate — the boot guard answers the
// same question AFTER, which is one step too late to be a control. Everything it
// reads is a SELECT: it is safe against a live database and changes nothing.
//
// ⛔ ONE PINNED CONNECTION, ONE TRANSACTION, AND THAT IS THE WHOLE POINT.
//
// The first version of this function took the facts off a *sql.DB. A *sql.DB is a
// POOL: it makes no promise that two statements land on the same physical
// connection, and this repository already says so where it matters — dialect.go's
// Execer exists precisely so schema work can run "on ONE named connection …
// instead of racing the pool for a different one". On a stable direct route
// database/sql happens to reuse the idle connection, which is why that version
// measured green; it was a coincidence, not a guarantee. Behind a pooler or a
// balancer it could return Reachable=true built from one backend's ROLE and
// another backend's DATABASE, CREATE grant and writability — a certificate no
// real session ever satisfied. A probe whose entire job is "these facts describe
// ONE session" must not be assembled from several.
//
// So: db.Conn pins a connection, BeginTx pins it further, and every read below
// runs on that tx. The pin is released by the deferred Close.
//
// THE TRANSACTION IS DELIBERATELY NOT ReadOnly. sql.TxOptions{ReadOnly: true}
// would make PostgreSQL report transaction_read_only=on for our OWN setting, so
// the probe would be measuring the flag it just set and would report every
// session as unwritable. With nil options pgx sends no access mode and the
// session's inherited default is what answers — MEASURED on PostgreSQL 16.15:
// with `ALTER ROLE … SET default_transaction_read_only = on` the nil-options
// transaction reports `on`, and without it `off`. Reading, not writing, is
// enforced by the statements themselves: every one is a SELECT.
//
// A connection, authentication or posture failure is captured in the returned
// value (Posture.Reachable=false, Posture.Err set), not as a Go error, so a
// caller can report every pool it was given in one pass instead of stopping at
// the first. The returned error is reserved for a programmer-level fault (an
// unsupported engine).
func ProbeConnAuthority(ctx context.Context, cfg store.Config) (store.ConnAuthority, error) {
	if cfg.Engine != store.EnginePostgres {
		return store.ConnAuthority{}, fmt.Errorf("sqlstore: ProbeConnAuthority supports the postgres engine only, got %q", cfg.Engine)
	}
	out := store.ConnAuthority{
		Schema:  dialect.EngineSchema,
		Posture: store.RolePosture{Engine: cfg.Engine},
	}
	db, err := openDB(cfg)
	if err != nil {
		out.Posture.Err = err.Error()
		return out, nil
	}
	defer db.Close() //nolint:errcheck // transient probe pool

	// The pin. Both Close calls are deferred so a cancelled ctx still returns the
	// connection to the pool and the pool to the driver.
	conn, err := db.Conn(ctx)
	if err != nil {
		out.Posture.Err = fmt.Errorf("sqlstore: pin a probe connection: %w", err).Error()
		return out, nil
	}
	defer conn.Close() //nolint:errcheck // transient pinned connection
	measured := probeRetainedConnAuthority(ctx, conn)
	return measured.ConnAuthority, nil
}

// retainedConnAuthority adds catalog identity to the public observation without
// making it a maintenance capability. Every field comes from one retained
// connection and one transaction; no caller-supplied role or OID is trusted.
type retainedConnAuthority struct {
	store.ConnAuthority
	DatabaseOID int64
	SchemaOID   int64
	SessionRole string
	CurrentRole string
	RoleOID     int64
	BackendPID  int
	rolePosture dialect.RolePosture
}

func probeRetainedConnAuthority(ctx context.Context, conn *sql.Conn) (out retainedConnAuthority) {
	out.ConnAuthority = store.ConnAuthority{
		Schema:  dialect.EngineSchema,
		Posture: store.RolePosture{Engine: store.EnginePostgres},
	}
	dia, _ := dialect.New(store.EnginePostgres)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		out.Posture.Err = fmt.Errorf("sqlstore: begin the probe transaction: %w", err).Error()
		return out
	}
	// Rollback, never Commit: nothing here writes, and a rollback is also the
	// correct exit on a cancelled context.
	defer func() {
		if err := tx.Rollback(); err != nil {
			out.Posture.Reachable = false
			out.Posture.Err = fmt.Errorf("sqlstore: rollback the authority probe: %w", err).Error()
		}
	}()

	posture, perr := dia.ConnRolePosture(ctx, tx)
	if perr != nil {
		out.Posture.Err = perr.Error()
		return out
	}

	// SchemaExists is asked SEPARATELY from CanCreate on purpose: has_schema_privilege
	// RAISES 3F000 on a schema that does not exist, so folding the two into one call
	// would report a database with no engine schema as an unreachable connection. The
	// scalar subquery yields NULL instead, and the COALESCE turns that into the honest
	// "no, and here is why" the caller can name.
	//
	// transaction_read_only and pg_is_in_recovery() ride in this same statement
	// because they are properties of THIS session and belong to the same snapshot
	// of it as the CREATE grant they qualify.
	const q = `SELECT pg_catalog.current_database(),
       pg_catalog.pg_postmaster_start_time(),
       (SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname = $1) > 0,
       COALESCE((SELECT pg_catalog.has_schema_privilege(oid, 'CREATE')
                   FROM pg_catalog.pg_namespace WHERE nspname = $1), false),
       pg_catalog.current_setting('transaction_read_only') = 'on',
       pg_catalog.pg_is_in_recovery(),
       (SELECT oid::pg_catalog.int8 FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database()),
       COALESCE((SELECT oid::pg_catalog.int8 FROM pg_catalog.pg_namespace WHERE nspname=$1),0),
       SESSION_USER, CURRENT_USER,
       (SELECT oid::pg_catalog.int8 FROM pg_catalog.pg_roles WHERE rolname=CURRENT_USER),
       pg_catalog.pg_backend_pid()`
	var startedAt time.Time
	if err := tx.QueryRowContext(ctx, q, dialect.EngineSchema).Scan(
		&out.Database, &startedAt, &out.SchemaExists, &out.CanCreate,
		&out.SessionReadOnly, &out.InRecovery,
		&out.DatabaseOID, &out.SchemaOID, &out.SessionRole, &out.CurrentRole, &out.RoleOID,
		&out.BackendPID,
	); err != nil {
		out.Posture.Err = fmt.Errorf("sqlstore: probe connection authority: %w", err).Error()
		return out
	}
	// UTC and a fixed layout so two probes of the same instance compare EQUAL as
	// strings: the driver's own location handling is not a property this check may
	// depend on.
	out.InstanceStartedAt = startedAt.UTC().Format(time.RFC3339Nano)

	// LAST, and in its own statement, because it is the one read an operator can
	// take away. EXECUTE on pg_control_system() is granted to PUBLIC by initdb —
	// PostgreSQL 16's system_functions.sql revokes 56 functions from PUBLIC and
	// this is not among them — but a hardened catalogue may revoke it, and then
	// this statement fails with 42501 and ABORTS the transaction. Everything above
	// is already scanned, so the caller still gets a complete picture with one
	// honestly missing field instead of a probe that reports nothing.
	//
	// The failure is recorded, never defaulted: a caller comparing identities must
	// be able to tell "different cluster" from "I could not look", because
	// collapsing those two is how a cross-estate restore gets waved through.
	if err := tx.QueryRowContext(ctx,
		`SELECT system_identifier::text FROM pg_catalog.pg_control_system()`).Scan(&out.SystemIdentifier); err != nil {
		out.SystemIdentifier = ""
		out.SystemIdentifierErr = err.Error()
	}

	out.Posture.Reachable = true
	out.rolePosture = posture
	out.Posture.Role = posture.Role
	out.Posture.Superuser = posture.Superuser
	out.Posture.BypassRLS = posture.BypassRLS
	out.Posture.ReplicationRole = posture.ReplicationRole
	return out
}

// ProbeSameLiveServer mounts the engine's own live-server challenge on TRANSIENT
// connections, so a caller can establish the prerequisite BEFORE it writes
// anything instead of discovering it at Open.
//
// The holder connects, opens a transaction and takes a random advisory key, and
// KEEPS HOLDING IT while each witness connects and tries to take the same key on
// its own transaction. Advisory locks are cluster-scoped and session-held, so a
// witness that ACQUIRES the key has proved it is a different server. Every
// transaction is rolled back and every connection closed; nothing is written and
// no lock outlives the call.
//
// ⛔ IT DOES NOT REPLACE THE ENGINE'S CHECK, IT ANTICIPATES IT. The store still
// runs its own challenge at Open (verifyDirectoryActivationDatabaseIdentity) and
// still refuses there. This exists so `dr restore` stops before installing
// custody and running pg_restore rather than after — the same reading, taken one
// step earlier, from the SAME factored predicate (askAdvisoryLockWitness) so the
// two cannot answer differently.
//
// ⚠ AND IT BINDS THESE SESSIONS, NOT THE NEXT ONES. Like every other pre-flight
// fact, it is closed when it returns; a pg_restore subprocess dials again. It is
// a gate on later writes only where each DSN's reachable backends are uniform.
//
// A witness that cannot be examined is reported as NOT same-server with its Err
// set — never as a pass. The returned error is reserved for a programmer-level
// fault (an unsupported engine).
func ProbeSameLiveServer(
	ctx context.Context,
	holder store.Config,
	holderLabel string,
	witnesses []store.SameServerWitness,
) (out store.SameServerReport, err error) {
	if holder.Engine != store.EnginePostgres {
		return store.SameServerReport{}, fmt.Errorf(
			"sqlstore: ProbeSameLiveServer supports the postgres engine only, got %q", holder.Engine)
	}
	out.HolderLabel = holderLabel
	if len(witnesses) == 0 {
		return out, nil
	}
	db, err := openDB(holder)
	if err != nil {
		out.HolderErr = err.Error()
		return out, nil
	}
	defer db.Close() //nolint:errcheck // transient probe pool
	conn, err := db.Conn(ctx)
	if err != nil {
		out.HolderErr = fmt.Errorf("pin the challenge holder: %w", err).Error()
		return out, nil
	}
	defer conn.Close() //nolint:errcheck // transient pinned connection
	challenge, err := beginRetainedServerChallenge(ctx, conn)
	if err != nil {
		out.HolderErr = err.Error()
		return out, nil
	}
	defer func() {
		if err := challenge.rollback(); err != nil {
			out.HolderErr = fmt.Errorf("rollback the holder challenge: %w", err).Error()
		}
	}()
	out.HolderDatabase = challenge.database
	for _, w := range witnesses {
		out.Witnesses = append(out.Witnesses, askSameServerWitness(ctx, holder, w, challenge))
	}
	return out, nil
}

// Transient public probes and retained work sessions share the same challenge
// predicate. This wrapper closes each transient witness before dialing the next.
func askSameServerWitness(ctx context.Context, holder store.Config, w store.SameServerWitness, challenge *retainedServerChallenge) store.SameServerVerdict {
	v := store.SameServerVerdict{Label: w.Label}
	cfg := holder
	cfg.DSN = w.DSN
	db, err := openDB(cfg)
	if err != nil {
		v.Err = err.Error()
		return v
	}
	defer db.Close() //nolint:errcheck // transient witness pool
	conn, err := db.Conn(ctx)
	if err != nil {
		v.Err = fmt.Errorf("pin the witness connection: %w", err).Error()
		return v
	}
	defer conn.Close() //nolint:errcheck // transient pinned connection
	return challenge.witness(ctx, conn, w.Label).SameServerVerdict
}

const (
	attrsUnprivileged = "NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION"
	attrsAdmin        = "NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION"
	redactedPassword  = "'********'"
)

// RenderProvisionSQL renders the provisioning steps for display (`db init
// --print-sql`) WITHOUT a database connection. Password literals are redacted. It
// validates the spec's identifiers so a bad name is caught before anything runs.
//
// This is the DESIRED STATE, not a transcript. The executor converges a role that
// already exists with LOGIN plus only the attributes that actually differ, and it
// creates one as NOLOGIN followed by a separate credential statement (see upsertRole);
// neither text appears here, and a reader must not take these lines as the statements
// a run executed. The unconditional CREATE form is what the plan is FOR — an operator
// applying the model by hand, against a cluster where nothing exists yet.
func RenderProvisionSQL(spec store.PgProvisionSpec) ([]store.PgProvisionStep, error) {
	if spec.InstallDirectoryInventory {
		return renderDirectoryInventoryInstall(spec)
	}
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
	if spec.InstallDirectoryInventory {
		return provisionDirectoryInventory(ctx, superuserDSN, spec, execute)
	}
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

// execQuerier is what the role stage needs, and it exists so the role DDL can run on
// a *sql.Tx instead of the pool. Both *sql.DB and *sql.Tx satisfy it.
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
// check-then-act — read the catalog posture, then CREATE or ALTER — and the window
// between the two is exactly wide enough for both to read "absent" and both to
// CREATE, or for both to ALTER the same tuple and for one to be told
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

// roleAttrs is the CLOSED posture provisioning knows how to converge: the six flags
// the two published attribute sets name, and nothing else. rolinherit, rolconnlimit
// and rolvaliduntil are deliberately absent — provisioning does not own them, so a
// deployment that keeps its roles NOINHERIT keeps them NOINHERIT.
type roleAttrs struct {
	Login       bool
	Superuser   bool
	BypassRLS   bool
	CreateRole  bool
	CreateDB    bool
	Replication bool
}

// roleOption is one convergeable attribute: how to read it off a posture, and the two
// words that name it in CREATE/ALTER ROLE. The SLICE ORDER IS THE STATEMENT ORDER, so
// the rendered options are deterministic and reviewable — and it is the order the
// published attrsUnprivileged/attrsAdmin constants already print, which is what lets
// the render path and the executor be read side by side.
type roleOption struct {
	Field string
	Get   func(roleAttrs) bool
	Yes   string
	No    string
}

func (o roleOption) word(on bool) string {
	if on {
		return o.Yes
	}
	return o.No
}

// convergeableRoleOptions is every attribute BESIDES login. LOGIN is not here because
// it is not conditional: see alterRoleAttributesSQL.
var convergeableRoleOptions = []roleOption{
	{"superuser", func(a roleAttrs) bool { return a.Superuser }, "SUPERUSER", "NOSUPERUSER"},
	{"bypassrls", func(a roleAttrs) bool { return a.BypassRLS }, "BYPASSRLS", "NOBYPASSRLS"},
	{"createrole", func(a roleAttrs) bool { return a.CreateRole }, "CREATEROLE", "NOCREATEROLE"},
	{"createdb", func(a roleAttrs) bool { return a.CreateDB }, "CREATEDB", "NOCREATEDB"},
	{"replication", func(a roleAttrs) bool { return a.Replication }, "REPLICATION", "NOREPLICATION"},
}

// desiredRoleAttrs resolves one of the two controlled attribute sets to the posture it
// names. Anything else is refused HERE — before a catalog read and before a write —
// rather than reaching the server as arbitrary SQL or quietly degrading to a partial
// default. The sets are internal constants today; this keeps them closed if a later
// caller ever tries to widen them from outside.
func desiredRoleAttrs(attrs string) (roleAttrs, error) {
	switch attrs {
	case attrsUnprivileged:
		return roleAttrs{Login: true}, nil
	case attrsAdmin:
		return roleAttrs{Login: true, BypassRLS: true}, nil
	default:
		return roleAttrs{}, fmt.Errorf(
			"provisioning has no role attribute set %q; the controlled sets are %q (application/owner) and %q (runtime reader)",
			attrs, attrsUnprivileged, attrsAdmin)
	}
}

// createRoleSQL renders creation: NOLOGIN plus every remaining desired attribute, and
// NO password. LOGIN and the credential arrive together in the separate step below, so
// THIS statement — the one that names the privileged attributes, and therefore the one a
// privilege check refuses — carries no credential literal, and the role is never a
// committed login without one, because both statements are the caller's single
// transaction.
//
// The claim is bounded to the create and attribute statements, and an earlier version of
// this comment was not. The credential statement below still carries a literal, can
// itself be refused, and is logged by a server configured to log failed statements.
// Splitting the two narrows the exposure to one statement; it does not remove it, and the
// complete credential-channel design remains a separate open obligation.
func createRoleSQL(name string, want roleAttrs) string {
	opts := make([]string, 0, len(convergeableRoleOptions)+1)
	opts = append(opts, "NOLOGIN")
	for _, o := range convergeableRoleOptions {
		opts = append(opts, o.word(o.Get(want)))
	}
	return fmt.Sprintf("CREATE ROLE %s WITH %s", name, strings.Join(opts, " "))
}

// alterRoleAttributesSQL renders convergence for an EXISTING role: LOGIN always, then
// only the options whose observed value actually differs from the desired one.
//
// LOGIN is unconditional for two reasons that happen to coincide. Both controlled sets
// are LOGIN, so it is always the desired value and converges an observed NOLOGIN; and
// it keeps the statement a real role administration under the server's rules, so a
// rerun still requires CREATEROLE plus ADMIN OPTION on the target. An executor that
// cannot administer a requested role is REFUSED rather than passed silently, which is
// the whole point of not optimizing a zero-drift rerun into no statement at all.
//
// Everything else is conditional because PostgreSQL gates the PRESENCE of a restricted
// option, not a change of value. Measured on 16.15: a non-superuser executor is refused
// NOSUPERUSER on a role that is ALREADY NOSUPERUSER (user.c:764), and the same holds for
// REPLICATION (:808) and BYPASSRLS (:814). Naming the whole list unconditionally is what
// made the rerun path — the one `db init` is built for — unreachable for every
// non-superuser maintenance role. Dropping the restricted options instead would leave
// real drift uncorrected and silently publish a privileged application role, so a
// MISMATCHING privileged attribute is still named here: that operation is meant to be
// refused when the executor lacks the authority, and the refusal rolls the role
// transaction back rather than reporting a converged role that is not.
func alterRoleAttributesSQL(name string, want, have roleAttrs) string {
	opts := make([]string, 0, len(convergeableRoleOptions)+1)
	opts = append(opts, "LOGIN")
	for _, o := range convergeableRoleOptions {
		if o.Get(want) != o.Get(have) {
			opts = append(opts, o.word(o.Get(want)))
		}
	}
	return fmt.Sprintf("ALTER ROLE %s WITH %s", name, strings.Join(opts, " "))
}

// roleCredentialTemplate is the credential step's pg_catalog.format template: the
// validated identifier, LOGIN and PASSWORD, and nothing else. %L is filled SERVER-SIDE
// from a bound parameter, so a password is never concatenated into SQL in Go.
func roleCredentialTemplate(name string) string {
	return fmt.Sprintf("ALTER ROLE %s WITH LOGIN PASSWORD %%L", name)
}

// roleCatalogQuery reads the WHOLE posture of one role, by its exact name, through a
// bound text parameter — never an interpolated name and never a pattern match. The OID
// travels in the same row, so the row this transaction acted on is the row it verifies
// afterwards. pg_roles rather than pg_authid: the view is readable without being a
// superuser and it masks rolpassword.
const roleCatalogQuery = `SELECT r.oid::pg_catalog.int8, r.rolname::pg_catalog.text,
       r.rolcanlogin, r.rolsuper, r.rolbypassrls,
       r.rolcreaterole, r.rolcreatedb, r.rolreplication
  FROM pg_catalog.pg_roles AS r
 WHERE r.rolname = $1::pg_catalog.text`

// executorIdentityQuery resolves the two identities this transaction acts under — the
// login it authenticated as, and the role it is currently executing as, which SET ROLE
// separates — to their catalog OIDs. SESSION_USER and CURRENT_USER are reserved words
// the parser answers itself, so neither depends on a resolvable name or the search_path.
const executorIdentityQuery = `SELECT s.oid::pg_catalog.int8, c.oid::pg_catalog.int8
  FROM pg_catalog.pg_roles AS s, pg_catalog.pg_roles AS c
 WHERE s.rolname = SESSION_USER AND c.rolname = CURRENT_USER`

// errExecutorIdentityUnresolved is the refusal for a transaction that cannot name its
// own identity. It is a distinct value rather than a stage message because it is not a
// server failure: the query succeeded and returned nothing.
var errExecutorIdentityUnresolved = errors.New("this provisioning transaction's session_user/current_user do not resolve to catalog roles")

// observedRole is what the catalog says about the target inside the caller's
// transaction: identity and posture, read together in one row.
type observedRole struct {
	OID   int64
	Name  string
	Attrs roleAttrs
}

// Stage names for the diagnostics below. They say WHERE the role stage stopped and
// carry no server text of their own.
const (
	roleStageLookup        = "catalog lookup"
	roleStageIdentity      = "executor identity"
	roleStageCreate        = "create"
	roleStageAttributes    = "attribute administration"
	roleStageCredential    = "credential"
	roleStagePostcondition = "postcondition"
)

// sqlStateShape is the five characters a SQLSTATE is DEFINED to be (class plus
// subclass, digits and upper-case letters). Anything else is not a SQLSTATE and is not
// repeated into a diagnostic just because a driver put it in that field.
var sqlStateShape = regexp.MustCompile(`^[0-9A-Z]{5}$`)

// roleStageError is the ONLY error the role stage returns for a server or driver
// failure, and it deliberately drops the server's own words.
//
// The reason is the credential step. A refused ALTER ROLE … PASSWORD is logged by the
// server with its statement text under the default log_min_error_statement, and pgx
// hands the driver error back with message, detail and hint attached. Wrapping that
// error verbatim would move the exposure into the product's own diagnostics — an
// operator's terminal, a support bundle, a JSON result — where it is far more likely to
// be copied than a server log is. So a failure becomes: which stage, which role, and
// the five-character SQLSTATE when the server supplied a well-formed one.
//
// Cancellation is the exception, and it is an identity rather than a message:
// context.Canceled and context.DeadlineExceeded are preserved through %w so a caller
// that distinguishes "the operator interrupted this" from "the server refused" still
// can. Neither carries server text.
func roleStageError(stage, name string, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("role %q: %s: %w", name, stage, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("role %q: %s: %w", name, stage, context.DeadlineExceeded)
	}
	if code := serverSQLState(err); code != "" {
		return fmt.Errorf("role %q: %s refused by the server (SQLSTATE %s)", name, stage, code)
	}
	return fmt.Errorf("role %q: %s did not complete", name, stage)
}

// serverSQLState returns the server's SQLSTATE when the failure carries one in the
// shape the standard defines, and "" otherwise.
func serverSQLState(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || !sqlStateShape.MatchString(pgErr.Code) {
		return ""
	}
	return pgErr.Code
}

// readRoleFromCatalog reads the target's posture. A MISSING ROW is the create path and
// is reported as such; every other outcome — an unreadable catalog, a refused query, a
// canceled context — is an error, because "I could not look" must never be spelled the
// same way as "it is not there".
func readRoleFromCatalog(ctx context.Context, db execQuerier, name string) (observedRole, bool, error) {
	var got observedRole
	err := db.QueryRowContext(ctx, roleCatalogQuery, name).Scan(
		&got.OID, &got.Name,
		&got.Attrs.Login, &got.Attrs.Superuser, &got.Attrs.BypassRLS,
		&got.Attrs.CreateRole, &got.Attrs.CreateDB, &got.Attrs.Replication,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return observedRole{}, false, nil
	case err != nil:
		return observedRole{}, false, err
	}
	return got, true, nil
}

// readExecutorIdentity resolves session_user and current_user to OIDs in this
// transaction. Comparing OIDs rather than names is what makes the guard below an
// identity check instead of a naming convention.
func readExecutorIdentity(ctx context.Context, db execQuerier) (sessionOID, currentOID int64, err error) {
	err = db.QueryRowContext(ctx, executorIdentityQuery).Scan(&sessionOID, &currentOID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, 0, errExecutorIdentityUnresolved
	case err != nil:
		return 0, 0, err
	}
	return sessionOID, currentOID, nil
}

// firstRoleAttrMismatch names the first flag that did not converge, in the statement's
// own order, or "" when the whole posture matches. It returns a FIELD NAME rather than
// a rendering of either posture: a postcondition diagnostic has to be safe to print.
func firstRoleAttrMismatch(want, got roleAttrs) string {
	if want.Login != got.Login {
		return "login"
	}
	for _, o := range convergeableRoleOptions {
		if o.Get(want) != o.Get(got) {
			return o.Field
		}
	}
	return ""
}

// verifyRolePosture rereads the whole posture in the SAME transaction that wrote it and
// requires all six desired flags, the exact name, and — for a role that already existed
// — the same OID it was read with. A drop-and-recreate between the two statements, a
// refused write that somehow reported success, or a converged flag that did not stick
// is a failure of this role transaction, not a warning.
//
// What this is NOT: a guarantee about the catalog after commit, or a lock against an
// independent administrator. It is transaction-bound, and the runtime posture checks at
// boot remain the standing verification.
func verifyRolePosture(ctx context.Context, db execQuerier, name string, want roleAttrs, existed bool, priorOID int64) error {
	got, found, err := readRoleFromCatalog(ctx, db, name)
	if err != nil {
		return roleStageError(roleStagePostcondition, name, err)
	}
	switch {
	case !found:
		return fmt.Errorf("role %q: %s: the role is absent from the catalog in the same transaction that provisioned it", name, roleStagePostcondition)
	case got.Name != name:
		return fmt.Errorf("role %q: %s: the catalog resolved a different role name for this exact name", name, roleStagePostcondition)
	case existed && got.OID != priorOID:
		return fmt.Errorf("role %q: %s: the role's catalog identity changed during this transaction", name, roleStagePostcondition)
	}
	if field := firstRoleAttrMismatch(want, got.Attrs); field != "" {
		return fmt.Errorf("role %q: %s: %s did not converge to the requested posture", name, roleStagePostcondition, field)
	}
	return nil
}

// setRolePassword is the separate credential step, and it is separate on purpose: it runs
// only AFTER the create or attribute-administration statement has been accepted, so a
// refusal AT THAT STAGE happens before the credential is ever formatted or sent.
//
// That is the whole of the guarantee, and it says nothing about this statement. The
// credential ALTER carries a literal, can be refused on its own account, and when it is,
// the caller's transaction rolls back. The template carries the validated identifier;
// only the password is a server-side %L from a bound parameter, and neither the template
// nor the formatted statement reaches a diagnostic.
func setRolePassword(ctx context.Context, db execQuerier, name, password string) error {
	var ddl string
	if err := db.QueryRowContext(ctx,
		"SELECT pg_catalog.format($1::pg_catalog.text, $2::pg_catalog.text)",
		roleCredentialTemplate(name), password).Scan(&ddl); err != nil {
		return roleStageError(roleStageCredential, name, err)
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return roleStageError(roleStageCredential, name, err)
	}
	return nil
}

// upsertRole converges ONE requested role to the posture provisioning documents, inside
// the caller's role transaction: read the catalog, refuse the identities that must not
// be touched, apply only what actually differs, set the credential separately, and
// verify the whole posture before the caller is allowed to commit.
//
// The order of the refusals is the design. Both the target's posture and the executor's
// own identity are resolved BEFORE any write, so a role that is this transaction's
// session or effective identity, or any existing superuser, is refused rather than
// converged: an administrative identity is not application-role drift, and demoting one
// inside a provisioning transaction is a way to lose a cluster's only administrator. The
// check is OID equality against session_user and current_user, not a guess from the
// role's name — provisioning must not acquire a heuristic about which names are
// privileged.
//
// This helper opens no pool and commits nothing. Every refusal below leaves the caller's
// transaction to roll back, which is what makes a failure on the second requested role
// undo the first.
func upsertRole(ctx context.Context, db execQuerier, name, attrs, password string) error {
	if _, err := validIdent("role", name); err != nil {
		return err
	}
	want, err := desiredRoleAttrs(attrs)
	if err != nil {
		return err
	}

	have, exists, err := readRoleFromCatalog(ctx, db, name)
	if err != nil {
		return roleStageError(roleStageLookup, name, err)
	}
	sessionOID, currentOID, err := readExecutorIdentity(ctx, db)
	if err != nil {
		if errors.Is(err, errExecutorIdentityUnresolved) {
			return fmt.Errorf("role %q: %w, so provisioning cannot tell whether it is about to administer itself", name, err)
		}
		return roleStageError(roleStageIdentity, name, err)
	}
	if exists {
		if have.OID == sessionOID || have.OID == currentOID {
			return fmt.Errorf("role %q is the identity this provisioning transaction is running as; provisioning will not converge its own login or effective role to an application posture", name)
		}
		if have.Attrs.Superuser {
			return fmt.Errorf("role %q is a SUPERUSER; provisioning will not demote an administrative identity as application-role drift", name)
		}
	}

	if !exists {
		if password == "" {
			return fmt.Errorf("cannot create role %q without a password", name)
		}
		if _, err := db.ExecContext(ctx, createRoleSQL(name, want)); err != nil {
			return roleStageError(roleStageCreate, name, err)
		}
	} else if _, err := db.ExecContext(ctx, alterRoleAttributesSQL(name, want, have.Attrs)); err != nil {
		return roleStageError(roleStageAttributes, name, err)
	}

	// An absent password PRESERVES the existing one: an operator rerunning `db init`
	// without the password file is asking to converge attributes, not to lock a role
	// out of its own database.
	if password != "" {
		if err := setRolePassword(ctx, db, name, password); err != nil {
			return err
		}
	}
	return verifyRolePosture(ctx, db, name, want, exists, have.OID)
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
