// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/store"
)

// TestEnginePoolsShipNoStartupParameters is the regression for a defect this change
// introduced while closing a different one. search_path was pinned by writing it into
// pgx's ConnConfig.RuntimeParams — and pgx copies RuntimeParams verbatim into the
// StartupMessage (pgconn/pgconn.go:402). A connection pooler tracks only a fixed handful
// of startup parameters and rejects every other one outright; PgBouncer answers
// `FATAL: unsupported startup parameter: search_path`, in session mode as much as in
// transaction mode. Every pooled deployment would have stopped dialing entirely, before
// a single one of this package's actionable errors could be produced — and the
// workaround an operator reaches for first, ignore_startup_parameters, makes the pooler
// DROP the parameter, which removes the pin instead of fixing it.
//
// This codebase already paid for the same class through the DSN once (leader_pg.go, the
// FATAL 42704 note). The pin now runs as an ordinary query on each new connection, so
// what has to stay true is the decision below: the engine's connection config adds no
// startup parameter of its own to whatever the operator's DSN already asked for.
func TestEnginePoolsShipNoStartupParameters(t *testing.T) {
	t.Parallel()
	const dsn = "postgres://svc:pw@127.0.0.1:5432/olivares?sslmode=disable"

	base, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse baseline dsn: %v", err)
	}
	got, err := pgEngineConnConfig(dsn)
	if err != nil {
		t.Fatalf("pgEngineConnConfig: %v", err)
	}

	if v, ok := got.RuntimeParams["search_path"]; ok {
		t.Errorf("search_path is shipped as a startup parameter (%q); a pooled deployment is refused at the dial", v)
	}
	for k, v := range got.RuntimeParams {
		if bv, ok := base.RuntimeParams[k]; !ok || bv != v {
			t.Errorf("startup parameter %q=%q was added by the engine; the pooler allowlist is not ours to widen", k, v)
		}
	}
}

// TestRenderProvisionSQLSingleRole proves the single-role render: an app role that
// owns the database, no split grants, and a REDACTED password (never the literal).
func TestRenderProvisionSQLSingleRole(t *testing.T) {
	t.Parallel()
	steps, err := RenderProvisionSQL(store.PgProvisionSpec{
		Database: "olivares",
		App:      store.PgRole{Name: "olivares_app", Password: "s3cr3t-should-not-appear"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	all := joinSteps(steps)
	for _, want := range []string{
		"CREATE ROLE olivares_app WITH LOGIN PASSWORD '********'",
		"CREATE DATABASE olivares OWNER olivares_app",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("render missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "s3cr3t-should-not-appear") {
		t.Fatal("render leaked the plaintext password")
	}
	if strings.Contains(all, "ALTER DEFAULT PRIVILEGES") {
		t.Error("single-role render should not grant DML to a separate app role")
	}
}

// TestRenderProvisionSQLSplitAndAdmin proves the split (separate owner) and the admin
// role add their expected grant statements.
func TestRenderProvisionSQLSplitAndAdmin(t *testing.T) {
	t.Parallel()
	steps, err := RenderProvisionSQL(store.PgProvisionSpec{
		Database: "olivares",
		App:      store.PgRole{Name: "olivares_app", Password: "a"},
		Owner:    store.PgRole{Name: "olivares_owner", Password: "b"},
		Admin:    &store.PgRole{Name: "olivares_admin", Password: "c"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	all := joinSteps(steps)
	for _, want := range []string{
		"CREATE ROLE olivares_owner WITH LOGIN PASSWORD '********'",
		"CREATE ROLE olivares_app WITH LOGIN PASSWORD '********'",
		"CREATE DATABASE olivares OWNER olivares_owner",
		"ALTER DEFAULT PRIVILEGES FOR ROLE olivares_owner IN SCHEMA public",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO olivares_app",
		"CREATE ROLE olivares_admin WITH LOGIN PASSWORD '********' NOSUPERUSER BYPASSRLS",
		"GRANT SELECT ON TABLES TO olivares_admin",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("render missing %q in:\n%s", want, all)
		}
	}
}

// TestRenderProvisionSQLKeepsTheGrantAndTakeBackAtomic pins the boundary an operator
// copying this SQL by hand must preserve. The bulk grant cannot tell an append-only
// table from a mutable one, so it hands mutation back on the evidence tables and the
// DO block takes it away again. Printed as two committed halves — or without the
// transaction at all — the printed recipe would publish, however briefly, a database
// in which evidence is mutable.
//
// This exists because a mutation removing BEGIN/COMMIT from the render, and another
// swapping the executor's transaction for autocommit, both left every other test green.
func TestRenderProvisionSQLKeepsTheGrantAndTakeBackAtomic(t *testing.T) {
	t.Parallel()
	steps, err := RenderProvisionSQL(store.PgProvisionSpec{
		Database: "olivares",
		App:      store.PgRole{Name: "olivares_app", Password: "a"},
		Owner:    store.PgRole{Name: "olivares_owner", Password: "b"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var dml string
	for _, s := range steps {
		if strings.Contains(s.SQL, "ON ALL TABLES IN SCHEMA public") {
			dml = s.SQL
			break
		}
	}
	if dml == "" {
		t.Fatal("no step grants DML on existing tables; the split render lost its bulk grant")
	}
	for _, want := range []string{"BEGIN;", "COMMIT;", "olivares_block_mutation", "REVOKE UPDATE, DELETE, TRUNCATE"} {
		if !strings.Contains(dml, want) {
			t.Errorf("the DML step must contain %q so the grant and its take-back are one transaction, got:\n%s", want, dml)
		}
	}
	// BOTH bounds. Checking only that BEGIN precedes the revoke let a COMMIT moved to
	// just before it still pass — which is the same published-unsafe-state defect in a
	// different disguise.
	begin, revoke, commit := strings.Index(dml, "BEGIN;"), strings.Index(dml, "REVOKE UPDATE, DELETE, TRUNCATE"), strings.LastIndex(dml, "COMMIT;")
	if !(begin < revoke && revoke < commit) {
		t.Errorf("the take-back must sit strictly inside the transaction (BEGIN=%d REVOKE=%d COMMIT=%d):\n%s",
			begin, revoke, commit, dml)
	}
}

// TestGrantAppDMLRollsBackOnFailure is the PRODUCTION-path regression for the same
// boundary. The helper-level test proves execAllTx rolls back; it does not prove
// grantAppDML uses it — reverting that one call site left every other test green.
//
// Deterministic by construction: a non-existent owner role makes the third statement
// fail, so the schema grant that the second one issued must not survive.
func TestGrantAppDMLRollsBackOnFailure(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s to run the provisioning rollback leg", pgtest.EnvSuperuserDSN)
	}
	// This cleanup is registered BEFORE isolatedPG on purpose. t.Cleanup is LIFO, so
	// registering it first makes it run LAST — after the isolated database is dropped —
	// and that is the only order that works: grantAppDML commits GRANT CONNECT on the
	// database before the transaction it then rolls back, so while that database exists
	// the role has a dependency and DROP ROLE fails with SQLSTATE 2BP01. pgtest.Drop
	// documents the same rule for the roles it owns ("the database goes first on
	// purpose"); this test was breaking it and leaking one cluster-global role per run.
	role := "olv_tx_" + pgtest.Suffix(t)
	// The MAINTENANCE dsn, not pg.Superuser: the latter names the isolated database,
	// which by the time this runs has been dropped, so connecting through it fails with
	// SQLSTATE 3D000 and the role leaks anyway.
	maintDSN := os.Getenv(pgtest.EnvSuperuserDSN)
	t.Cleanup(func() {
		if maintDSN == "" {
			return
		}
		// Fresh connection: the deferred Close below runs BEFORE t.Cleanup, so reusing
		// that pool here would execute nothing and report nothing.
		cleanup, err := sql.Open("pgx", maintDSN)
		if err != nil {
			t.Errorf("cleanup: open maintenance connection: %v", err)
			return
		}
		defer cleanup.Close() //nolint:errcheck
		if _, err := cleanup.ExecContext(context.Background(), "DROP ROLE IF EXISTS "+role); err != nil {
			// Errorf, not Logf. A leaked role is CLUSTER-global and every other lane
			// shares this server; a teardown failure that keeps the test green is
			// exactly how fifty-one of them accumulated once already.
			t.Errorf("cleanup: drop role %q: %v", role, err)
		}
	})

	pg := isolatedPG(t)
	db, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close() //nolint:errcheck
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "CREATE ROLE "+role+" LOGIN PASSWORD 'pw' NOSUPERUSER NOBYPASSRLS"); err != nil {
		t.Fatalf("create probe role: %v", err)
	}
	// PUBLIC holds USAGE on the schema by default, which would mask the rollback.
	if _, err := db.ExecContext(ctx, "REVOKE USAGE ON SCHEMA public FROM PUBLIC"); err != nil {
		t.Fatalf("revoke public usage: %v", err)
	}

	err = grantAppDML(ctx, db, pg.Database, "olv_own_"+pgtest.Suffix(t)+"_absent", role)
	if err == nil {
		t.Fatal("grantAppDML succeeded with a non-existent owner role")
	}
	var canUse bool
	if err := db.QueryRowContext(ctx,
		"SELECT has_schema_privilege($1, 'public', 'USAGE')", role).Scan(&canUse); err != nil {
		t.Fatalf("read schema privilege: %v", err)
	}
	if canUse {
		t.Error("the schema grant survived a failed provisioning run — grantAppDML is not running its statements in one transaction")
	}
}

// TestRenderProvisionSQLRejectsBadIdent proves a non-identifier role/db name is
// refused BEFORE anything is rendered or run (no client-side identifier quoting).
func TestRenderProvisionSQLRejectsBadIdent(t *testing.T) {
	t.Parallel()
	for _, spec := range []store.PgProvisionSpec{
		{Database: "olivares", App: store.PgRole{Name: "olivares app"}}, // space
		{Database: "olivares", App: store.PgRole{Name: "drop;table"}},   // punctuation
		{Database: "has-dash", App: store.PgRole{Name: "olivares_app"}}, // dash in db
		{Database: "olivares", App: store.PgRole{Name: "Olivares_App"}}, // uppercase
		{Database: "olivares", App: store.PgRole{Name: "a\"b"}},         // quote
	} {
		if _, err := RenderProvisionSQL(spec); err == nil {
			t.Errorf("RenderProvisionSQL(%+v) accepted an unsafe identifier", spec)
		}
	}
}

// TestProbeRolePostureSQLite proves the read-only probe reports an RLS-safe posture
// for SQLite (no roles) without opening the schema.
func TestProbeRolePostureSQLite(t *testing.T) {
	t.Parallel()
	dsn := filepath.Join(t.TempDir(), "probe.db")
	p, err := ProbeRolePosture(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: dsn})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !p.Reachable {
		t.Fatalf("sqlite probe not reachable: %q", p.Err)
	}
	if p.RLSUnsafe() {
		t.Errorf("sqlite reported RLS-unsafe: %+v", p)
	}
}

// TestOpenRefusesSingleConnPostgres pins the interim guard: MaxConns=1 on
// PostgreSQL must be refused BEFORE any connection is dialed, because a boot
// step pins one pooled connection while another step waits for a second — a
// single-connection pool HANGS instead of failing. The DSN is TEST-NET-1
// (unreachable): if the guard ever moves after the dial, the error becomes a
// connect failure and the message assertions below catch the regression.
//
// The message assertion used to demand the token "R1", and that anchor is now
// wrong twice over. R1 named the migration half and that half is closed —
// migration work runs on the connection holding the advisory lock. And the
// refusal is no longer unconditional: it fires only on the combination that
// actually self-blocks, so the test now pins THE CONDITION, not just the
// message. A blanket refusal would have hidden the very regression this file
// exists to catch, because MaxConns=1 could then never be booted for real.
//
// The three cells below are the whole contract: refused only with a spool budget
// AND no AdminDSN; allowed when either of those is absent.
func TestOpenRefusesSingleConnPostgres(t *testing.T) {
	t.Parallel()
	const unreachable = "postgres://nobody:nope@192.0.2.1:5432/none?connect_timeout=1&sslmode=disable"

	// Cell 1 — the self-blocking combination: refused BEFORE any dial. The DSN is
	// TEST-NET-1 (unreachable): if the guard ever moves after the dial, the error
	// becomes a connect failure and this assertion catches it.
	_, err := Open(context.Background(), store.Config{
		Engine:             store.EnginePostgres,
		DSN:                unreachable,
		MaxConns:           1,
		AuditSpoolMaxBytes: 1 << 20,
	}, nil)
	if err == nil {
		t.Fatal("MaxConns=1 with a spool budget and no AdminDSN opened a store")
	}
	if !strings.Contains(err.Error(), "MaxConns=1") ||
		!strings.Contains(err.Error(), "spool recompute") {
		t.Fatalf("the refusal must name the limit and the boot step that forces it, "+
			"the spool recompute (and must fire before dialing): %v", err)
	}

	// Cell 2 — same pool size, NO spool budget: the recompute never runs, so there
	// is no second-connection demand and the refusal must NOT fire.
	_, err = Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: unreachable, MaxConns: 1,
	}, nil)
	if err == nil {
		t.Fatal("an unreachable postgres DSN opened a store")
	}
	if strings.Contains(err.Error(), "refusing MaxConns=1") {
		t.Fatalf("MaxConns=1 without a spool budget must not be refused — nothing "+
			"asks for a second connection: %v", err)
	}

	// Cell 3 — spool budget present but AdminDSN configured: the cross-tenant sum
	// is read on its OWN pool, so the application pool is never asked twice.
	_, err = Open(context.Background(), store.Config{
		Engine:             store.EnginePostgres,
		DSN:                unreachable,
		AdminDSN:           unreachable,
		MaxConns:           1,
		AuditSpoolMaxBytes: 1 << 20,
	}, nil)
	if err == nil {
		t.Fatal("an unreachable postgres DSN opened a store")
	}
	if strings.Contains(err.Error(), "refusing MaxConns=1") {
		t.Fatalf("MaxConns=1 with AdminDSN must not be refused — the sum has its "+
			"own pool: %v", err)
	}

	// Cell 4 — MaxConns=2 never trips it, whatever the rest of the config says.
	_, err = Open(context.Background(), store.Config{
		Engine:             store.EnginePostgres,
		DSN:                unreachable,
		MaxConns:           2,
		AuditSpoolMaxBytes: 1 << 20,
	}, nil)
	if err == nil {
		t.Fatal("an unreachable postgres DSN opened a store")
	}
	if strings.Contains(err.Error(), "refusing MaxConns=1") {
		t.Fatalf("MaxConns=2 must not trip the single-connection refusal: %v", err)
	}

	// PostgreSQL-only: SQLite with MaxConns=1 keeps opening (single-writer engine,
	// different pool semantics).
	st, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", MaxConns: 1,
	}, registerWidget)
	if err != nil {
		t.Fatalf("sqlite with MaxConns=1 must open: %v", err)
	}
	_ = st.Close()
}

// TestOwnerDSNIgnoredOnSQLite proves the new OwnerDSN field is inert on SQLite (the
// owner pool is Postgres-only): a SQLite store opens normally with OwnerDSN set, so
// the wiring never affects the single-node/air-gap engine.
func TestOwnerDSNIgnoredOnSQLite(t *testing.T) {
	t.Parallel()
	st, err := Open(context.Background(), store.Config{
		Engine:   store.EngineSQLite,
		DSN:      ":memory:",
		OwnerDSN: "postgres://should-be-ignored@localhost/none",
	}, registerWidget)
	if err != nil {
		t.Fatalf("open sqlite with OwnerDSN: %v", err)
	}
	_ = st.Close()
}

// --- Postgres-gated (skips unless a superuser DSN is provided) -----------------

// TestProvisionPostgresSingleRole provisions an application role + database against a
// real Postgres and verifies the engine-accepting posture.
//
// It calls ProvisionPostgres DIRECTLY rather than through isolatedPG because
// ProvisionPostgres is the subject under test (it asserts the returned posture),
// but it now uses crypto/rand names and DROPS what it created — previously the
// names came from the broken uniqueSuffix, so this test and TestOwnerAppSplitOpens
// provisioned the SAME database and role within a run, and every run leaked both.
func TestProvisionPostgresSingleRole(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to run db init provisioning", pgtest.EnvSuperuserDSN)
	}
	superDSN := os.Getenv(pgtest.EnvSuperuserDSN)
	suffix := pgtest.Suffix(t)
	spec := store.PgProvisionSpec{
		Database: "olv_" + suffix,
		App:      store.PgRole{Name: "olv_app_" + suffix, Password: "pw-" + suffix},
	}
	// Through pgtest.Provision, not by hand: it is the SAME ownership-safe sequence
	// Isolate uses — take the cluster-wide provisioning lock, prove both names are
	// absent, register teardown, provision — all inside the lock. Doing it by hand
	// left a window in which the idempotent production path could ADOPT an existing
	// object that teardown was then authorized to drop.
	res := pgtest.Provision(t, superDSN, spec, ProvisionPostgres, spec.App.Name)
	if res.AppPosture == nil || !res.AppPosture.Reachable {
		t.Fatalf("app posture not verified: %+v", res.AppPosture)
	}
	if res.AppPosture.RLSUnsafe() {
		t.Errorf("provisioned app role is RLS-unsafe: %+v", res.AppPosture)
	}
}

// TestOwnerAppSplitOpens provisions the owner/app split, then opens the store with a
// separate OwnerDSN and proves the app (non-owner) role serves under granted DML —
// the previously-dead OwnerDSN field, now consumed end to end.
func TestOwnerAppSplitOpens(t *testing.T) {
	// isolatedPGSplit provisions the owner/app split through the same
	// ProvisionPostgres path this test used to drive by hand, and drops it after.
	pg := isolatedPGSplit(t)
	if pg.Result.OwnerDSNHint == "" || pg.Result.AppDSNHint == "" {
		t.Fatalf("expected DSN hints for the split, got %+v", pg.Result)
	}
	st, err := Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 4,
	}, registerWidget)
	if err != nil {
		t.Fatalf("open split store (owner runs DDL, app serves): %v", err)
	}
	_ = st.Close()
}

// TestOwnerAppSplitRefusesReplicaOwner is the PostgreSQL-gated half of the owner
// barrier: ownerPostureError pins the POLICY without a server, and this proves the
// policy is actually WIRED into the pool a real split deployment opens.
//
// The owner role is pinned to session_replication_role='replica' at the ROLE level —
// the realistic vector: the parameter can be pinned with ALTER ROLE, and since
// PostgreSQL 15 the right to set it can also be granted outright. AllowPrivilegedRole is deliberately set: waiving the RLS
// backstop must NOT waive the trigger bar, because migrations applied through a
// replica session install the append-only and cutover guards without ever firing
// them.
//
// Isolation comes from pgtest, not from a hand-rolled fixture: the APP role
// stays dialect.DefaultAppRole so the schema's hard-coded append-only REVOKE keeps
// targeting the role under test; only the DATABASE and the owner/admin roles are
// per-test. A per-test app role would leave that ACL pointing at nobody while every
// assertion here still passed.
func TestOwnerAppSplitRefusesReplicaOwner(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx := context.Background()
	if pg.Result.OwnerPosture == nil || pg.Result.OwnerPosture.Role == "" {
		t.Fatalf("owner posture unverified, so the role to pin is unknown: %+v", pg.Result.OwnerPosture)
	}
	ownerRole := pg.Result.OwnerPosture.Role

	// pg.Superuser is the maintenance role pointed at THIS database, so pinning the
	// parameter cannot reach a database another suite is using.
	super, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("open superuser pool: %v", err)
	}
	defer super.Close() //nolint:errcheck
	// The role name comes from pgtest's per-test mint, not from user input, and is a
	// plain identifier; it is still validated before interpolation.
	if !plainRoleIdent(ownerRole) {
		t.Fatalf("unsafe role identifier %q", ownerRole)
	}
	if _, err := super.ExecContext(ctx,
		"ALTER ROLE "+ownerRole+" SET session_replication_role = 'replica'"); err != nil {
		t.Fatalf("pin replica on the owner role: %v", err)
	}

	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 4,
		AllowPrivilegedRole: true,
	}, registerWidget)
	if err == nil {
		_ = st.Close()
		t.Fatal("a replica-pinned owner opened the store: migrations would install the " +
			"append-only and cutover guards through a session that never fires them")
	}
	// Both pool guards mention the parameter, so match on the OWNER wording: in a
	// contaminated environment (a replica-pinned app role, say) a bare parameter
	// match would credit the owner barrier for a rejection the app pool made.
	if !strings.Contains(err.Error(), "--owner-dsn") ||
		!strings.Contains(err.Error(), "session_replication_role") {
		t.Fatalf("Open failed for a reason that is not the owner-pool trigger barrier, "+
			"so this proves nothing about it: %v", err)
	}
}

// plainRoleIdent reports whether s is a bare lowercase SQL identifier, so a test
// fixture never interpolates anything else into DDL.
func plainRoleIdent(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// TestExecAllTxRollsBackEveryStatement pins the guarantee the provisioning path leans
// on: the grants and the append-only take-back are one unit, so a failure cannot leave
// the bulk grant committed with mutation open on the evidence tables.
//
// It exercises the primitive directly and deterministically. Observing the window from
// another connection would be a race; making a statement fail is not.
func TestExecAllTxRollsBackEveryStatement(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s to run the transaction leg", pgtest.EnvSuperuserDSN)
	}
	pg := isolatedPG(t)
	db, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close() //nolint:errcheck
	ctx := context.Background()

	err = execAllTx(ctx, db, []string{
		"CREATE TABLE tx_atomicity_probe (id TEXT PRIMARY KEY)",
		"THIS IS NOT SQL",
	})
	if err == nil {
		t.Fatal("execAllTx reported success for an invalid statement")
	}
	var exists bool
	if err := db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND c.relname = 'tx_atomicity_probe')",
	).Scan(&exists); err != nil {
		t.Fatalf("probe table existence: %v", err)
	}
	if exists {
		t.Error("a statement committed although a later one in the same batch failed — the provisioning grants are not atomic")
	}
}

func joinSteps(steps []store.PgProvisionStep) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.Label)
		b.WriteString("\n")
		b.WriteString(s.SQL)
		b.WriteString("\n")
	}
	return b.String()
}
