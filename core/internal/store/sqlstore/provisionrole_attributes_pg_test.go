// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/olivaresai/olivares/core/internal/pgtest"
)

// These exercise the role stage against a REAL PostgreSQL, under a real executor
// identity, because the property the repair exists for is not a property of the text
// it renders: it is which statements a 16.15 server accepts from a maintenance role
// that is not a superuser. The planning half is provisionrole_attributes_test.go and
// runs without a server; neither file stands in for the other.
//
// Scope, stated because the surrounding command is much larger than this: nothing here
// runs ProvisionPostgres, ensureDatabase, any grant stage or any boot path. The helper
// is driven through the transaction interface production hands it, so what is measured
// is the ROLE STAGE and nothing downstream. No claim is made about PostgreSQL 15, 17 or
// 18 or about any managed provider — the arrangements were executed on the version this
// run reports and on no other.
//
// Every role is created fresh with a unique name and dropped again. Passwords are
// synthetic values derived from that name; they are never printed, and no credential
// from the environment is read except the fixture superuser DSN the harness supplies.

// --- the lab ----------------------------------------------------------------

// roleLab is one owned arrangement on the configured server: a superuser connection to
// build fixtures with, a unique suffix, and a record of everything it created so the
// teardown removes exactly that and nothing else.
type roleLab struct {
	t        *testing.T
	superCfg *pgx.ConnConfig
	super    *sql.DB
	suffix   string
	created  []string
	database *roleLabDatabase
}

// A candidate name is not custody. Only a successful CREATE followed by this
// catalog observation permits cleanup, including after later setup failures.
type roleLabDatabase struct {
	OID, OwnerOID int64
	Name          string
}

func confirmRoleLabDatabase(candidate string, observed roleLabDatabase, sessionOwner int64, err error) (*roleLabDatabase, error) {
	if err != nil {
		return nil, err
	}
	if observed.Name != candidate || observed.OID <= 0 || sessionOwner <= 0 || observed.OwnerOID != sessionOwner {
		return nil, errors.New("database identity or session owner is unconfirmed")
	}
	return &observed, nil
}

func roleLabDropAllowed(owned *roleLabDatabase, observed roleLabDatabase, err error) (bool, error) {
	if owned == nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if *owned != observed {
		return false, errors.New("database custody changed")
	}
	return true, nil
}

// Diagnostics retain only a validated SQLSTATE, never a connection error's text.
func roleLabSQLState(err error) string {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		code := state.SQLState()
		if len(code) == 5 && strings.IndexFunc(code, func(r rune) bool {
			return !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z')
		}) == -1 {
			return code
		}
	}
	return "no SQLSTATE"
}

func roleLabAuthenticationResult(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	switch roleLabSQLState(err) {
	case "28000", "28P01":
		return false, nil
	default:
		return false, err
	}
}

func newRoleLab(t *testing.T) *roleLab {
	t.Helper()
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to run the role convergence leg", pgtest.EnvSuperuserDSN)
	}
	cfg, err := pgx.ParseConfig(os.Getenv(pgtest.EnvSuperuserDSN))
	if err != nil {
		t.Fatalf("parse %s: %s", pgtest.EnvSuperuserDSN, roleLabSQLState(err))
	}
	super, err := openOnTrustedPath(cfg)
	if err != nil {
		t.Fatalf("open the maintenance connection: %s", roleLabSQLState(err))
	}
	lab := &roleLab{t: t, superCfg: cfg, super: super}
	t.Cleanup(lab.teardown)
	lab.suffix = pgtest.Suffix(t)
	lab.createDatabase()
	return lab
}

func (l *roleLab) createDatabase() {
	l.t.Helper()
	candidate := l.name("lab")
	if _, err := validIdent("database", candidate); err != nil {
		l.t.Fatalf("invalid lab database candidate %q", candidate)
	}
	// This fixture owns no shared application role, so it takes no provisioning lock.
	quoted := pgx.Identifier{candidate}.Sanitize()
	if _, err := l.super.ExecContext(l.ctx(), "CREATE DATABASE "+quoted); err != nil {
		l.t.Fatalf("CREATE database candidate %s: %s; creation unconfirmed, outcome unknown; not dropped", candidate, roleLabSQLState(err))
	}
	var observed roleLabDatabase
	var sessionOwner int64
	err := l.super.QueryRowContext(l.ctx(),
		`SELECT d.oid::pg_catalog.int8, d.datname::pg_catalog.text, d.datdba::pg_catalog.int8,
		        (SELECT r.oid::pg_catalog.int8 FROM pg_catalog.pg_roles AS r WHERE r.rolname = SESSION_USER)
		   FROM pg_catalog.pg_database AS d WHERE d.datname = $1::pg_catalog.text`, candidate).
		Scan(&observed.OID, &observed.Name, &observed.OwnerOID, &sessionOwner)
	l.database, err = confirmRoleLabDatabase(candidate, observed, sessionOwner, err)
	if err != nil {
		l.t.Fatalf("created database candidate %s: custody unconfirmed (%s); not dropped", candidate, roleLabSQLState(err))
	}
	if _, err := l.super.ExecContext(l.ctx(), "GRANT CONNECT ON DATABASE "+quoted+" TO PUBLIC"); err != nil {
		l.t.Fatalf("grant CONNECT on owned database %s: %s", candidate, roleLabSQLState(err))
	}
	var granted bool
	if err := l.super.QueryRowContext(l.ctx(),
		`SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_database AS d, pg_catalog.aclexplode(d.datacl) AS a
		 WHERE d.datname = $1::pg_catalog.text AND a.grantee = 0 AND a.privilege_type = 'CONNECT')`, candidate).
		Scan(&granted); err != nil {
		l.t.Fatalf("verify PUBLIC CONNECT on owned database %s: %s", candidate, roleLabSQLState(err))
	}
	if !granted {
		l.t.Fatalf("owned database %s has no explicit PUBLIC CONNECT ACL entry", candidate)
	}
}

// teardown drops what this lab created, newest first, and then PROVES the removal by
// asking the catalog for anything left carrying this lab's suffix. A teardown that only
// issues DROP statements reports success when the drop was refused.
func (l *roleLab) teardown() {
	defer func() {
		if err := l.super.Close(); err != nil {
			l.t.Errorf("teardown: close the maintenance connection: %s", roleLabSQLState(err))
		}
	}()
	l.dropDatabase()
	// A failed database DROP must not consume the role cleanup/residue budget.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for i := len(l.created) - 1; i >= 0; i-- {
		if _, err := l.super.ExecContext(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{l.created[i]}.Sanitize()); err != nil {
			l.t.Errorf("teardown: drop role %s: %s", l.created[i], roleLabSQLState(err))
		}
	}
	if l.suffix != "" {
		l.reportResidue("roles",
			`SELECT r.rolname::pg_catalog.text FROM pg_catalog.pg_roles AS r WHERE r.rolname LIKE $1::pg_catalog.text ORDER BY 1`,
		)
		l.reportResidue("databases",
			`SELECT d.datname::pg_catalog.text FROM pg_catalog.pg_database AS d WHERE d.datname LIKE $1::pg_catalog.text ORDER BY 1`,
		)
	}
}

func (l *roleLab) dropDatabase() {
	if l.database == nil {
		return // Unconfirmed creation never permits adoption, DROP, or a gone claim.
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var observed roleLabDatabase
	err := l.super.QueryRowContext(ctx,
		`SELECT oid::pg_catalog.int8, datname::pg_catalog.text, datdba::pg_catalog.int8
		 FROM pg_catalog.pg_database WHERE datname = $1::pg_catalog.text`, l.database.Name).
		Scan(&observed.OID, &observed.Name, &observed.OwnerOID)
	allowed, err := roleLabDropAllowed(l.database, observed, err)
	if err != nil {
		l.t.Errorf("teardown: database custody check failed; recorded=%+v observed=%+v (%s); not dropped", *l.database, observed, roleLabSQLState(err))
		return
	}
	if !allowed {
		return
	}
	// Exclusive fixture custody permits this check then DROP; it is not atomic
	// against a concurrent privileged replacement between the two statements.
	if _, err := l.super.ExecContext(ctx, "DROP DATABASE "+pgx.Identifier{l.database.Name}.Sanitize()); err != nil {
		l.t.Errorf("teardown: drop owned database %s: %s", l.database.Name, roleLabSQLState(err))
		if roleLabSQLState(err) == "55006" {
			l.reportDatabaseConnections()
		}
	}
	var exists bool
	if err := l.super.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_database WHERE datname = $1::pg_catalog.text)`, l.database.Name).
		Scan(&exists); err != nil {
		l.t.Errorf("teardown: verify owned database absence: %s", roleLabSQLState(err))
	} else if exists {
		l.t.Errorf("teardown left owned database %s behind", l.database.Name)
	}
}

func (l *roleLab) reportDatabaseConnections() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var count int64
	if err := l.super.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE datname = $1::pg_catalog.text`, l.database.Name).
		Scan(&count); err != nil {
		l.t.Errorf("teardown: 55006 active-connection count unavailable: %s", roleLabSQLState(err))
	} else {
		l.t.Errorf("teardown: 55006 active-connection count=%d", count)
	}
}

func (l *roleLab) reportResidue(kind, query string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := l.super.QueryContext(ctx, query, "%"+l.suffix)
	if err != nil {
		l.t.Errorf("teardown: read %s residue: %s", kind, roleLabSQLState(err))
	} else {
		defer func() {
			if err := rows.Close(); err != nil {
				l.t.Errorf("teardown: close %s residue: %s", kind, roleLabSQLState(err))
			}
		}()
		var residue []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				l.t.Errorf("teardown: scan %s residue: %s", kind, roleLabSQLState(err))
				break
			}
			residue = append(residue, name)
		}
		if err := rows.Err(); err != nil {
			l.t.Errorf("teardown: %s residue rows: %s", kind, roleLabSQLState(err))
		}
		if len(residue) != 0 {
			l.t.Errorf("teardown left %s behind: %v", kind, residue)
		}
	}
}

func (l *roleLab) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	l.t.Cleanup(cancel)
	return ctx
}

// name mints a unique identifier inside the shape provisioning is willing to accept.
func (l *roleLab) name(kind string) string { return "ra2p1_" + kind + "_" + l.suffix }

// password is a synthetic, disposable value bound to the role's own name. It is a test
// fixture, not a credential for anything, and it is never printed.
func (l *roleLab) password(role, generation string) string { return role + "_" + generation }

// createRole builds a fixture role as the superuser. The password travels through the
// same server-side pg_catalog.format('%L') the product uses, so the fixture never
// concatenates one into SQL either.
func (l *roleLab) createRole(name, options, password string) {
	l.t.Helper()
	tmpl := "CREATE ROLE " + name + " WITH " + options
	var ddl string
	if password == "" {
		ddl = tmpl
	} else {
		tmpl += " LOGIN PASSWORD %L"
		if err := l.super.QueryRowContext(l.ctx(),
			"SELECT pg_catalog.format($1::pg_catalog.text, $2::pg_catalog.text)", tmpl, password).Scan(&ddl); err != nil {
			l.t.Fatalf("format the fixture creation for %s: %v", name, err)
		}
	}
	if _, err := l.super.ExecContext(l.ctx(), ddl); err != nil {
		l.t.Fatalf("create fixture role %s: %v", name, err)
	}
	l.created = append(l.created, name)
}

// exec runs a fixture statement as the superuser. Fixture SQL only: nothing the product
// issues goes through here.
func (l *roleLab) exec(statement string) {
	l.t.Helper()
	if _, err := l.super.ExecContext(l.ctx(), statement); err != nil {
		l.t.Fatalf("fixture statement %q: %v", statement, err)
	}
}

// connectAs opens a pool authenticated as role, on the same trusted search_path the
// product's own provisioning connections use.
func (l *roleLab) connectAs(role, password string) *sql.DB {
	l.t.Helper()
	cfg := l.loginConfig(role, password)
	db, err := openOnTrustedPath(cfg)
	if err != nil {
		l.t.Fatalf("open a connection as %s: %s", role, roleLabSQLState(err))
	}
	l.t.Cleanup(func() {
		if err := db.Close(); err != nil {
			l.t.Errorf("close executor pool for %s: %s", role, roleLabSQLState(err))
		}
	})
	if err := db.PingContext(l.ctx()); err != nil {
		l.t.Fatalf("connect as %s: %s", role, roleLabSQLState(err))
	}
	return db
}

// authenticates reports whether role can log in with password. Used as an assertion
// about credential preservation and rotation. Only authentication refusals are false;
// infrastructure failures fail the test without copying connection error text.
func (l *roleLab) authenticates(role, password string) bool {
	l.t.Helper()
	cfg := l.loginConfig(role, password)
	db, err := openOnTrustedPath(cfg)
	if err != nil {
		l.t.Fatalf("open authentication probe: %s", roleLabSQLState(err))
	}
	defer func() {
		if err := db.Close(); err != nil {
			l.t.Errorf("close authentication probe: %s", roleLabSQLState(err))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ok, err := roleLabAuthenticationResult(db.PingContext(ctx))
	if err != nil {
		l.t.Fatalf("authentication probe failed: %s", roleLabSQLState(err))
	}
	return ok
}

func (l *roleLab) loginConfig(role, password string) *pgx.ConnConfig {
	l.t.Helper()
	if l.database == nil {
		l.t.Fatal("login requires confirmed lab database custody")
	}
	cfg := l.superCfg.Copy()
	cfg.Database = l.database.Name
	cfg.User = role
	cfg.Password = password
	return cfg
}

// labPosture is the test's OWN view of a catalog row. Its query is written out here
// rather than reused from the product, so a defect in the product's reader cannot make
// an assertion about the catalog agree with it.
type labPosture struct {
	OID                                                     int64
	Login, Superuser, BypassRLS, CreateRole, CreateDB, Repl bool
	Inherit                                                 bool
}

func (l *roleLab) posture(name string) (labPosture, bool) {
	l.t.Helper()
	var p labPosture
	err := l.super.QueryRowContext(l.ctx(),
		`SELECT r.oid::pg_catalog.int8, r.rolcanlogin, r.rolsuper, r.rolbypassrls,
		        r.rolcreaterole, r.rolcreatedb, r.rolreplication, r.rolinherit
		   FROM pg_catalog.pg_roles AS r
		  WHERE r.rolname = $1::pg_catalog.text`, name).
		Scan(&p.OID, &p.Login, &p.Superuser, &p.BypassRLS, &p.CreateRole, &p.CreateDB, &p.Repl, &p.Inherit)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return labPosture{}, false
	case err != nil:
		l.t.Fatalf("read the catalog posture of %s: %v", name, err)
	}
	return p, true
}

// mustPosture fails when the role is missing, which is what most assertions want.
func (l *roleLab) mustPosture(name string) labPosture {
	l.t.Helper()
	p, ok := l.posture(name)
	if !ok {
		l.t.Fatalf("role %s is absent from the catalog", name)
	}
	return p
}

// wantConverged asserts the FULL desired posture, every flag named, so a control cannot
// pass by checking only the attribute it changed.
func (l *roleLab) wantConverged(name string, bypassRLS bool) {
	l.t.Helper()
	p := l.mustPosture(name)
	switch {
	case !p.Login:
		l.t.Errorf("%s: rolcanlogin is false", name)
	case p.Superuser:
		l.t.Errorf("%s: rolsuper is true", name)
	case p.BypassRLS != bypassRLS:
		l.t.Errorf("%s: rolbypassrls = %v, want %v", name, p.BypassRLS, bypassRLS)
	case p.CreateRole:
		l.t.Errorf("%s: rolcreaterole is true", name)
	case p.CreateDB:
		l.t.Errorf("%s: rolcreatedb is true", name)
	case p.Repl:
		l.t.Errorf("%s: rolreplication is true", name)
	}
}

// memberships serializes every membership row touching this lab's roles: who is a
// member of what, granted by whom, and the three options 16 stores per membership.
func (l *roleLab) memberships() string {
	l.t.Helper()
	rows, err := l.super.QueryContext(l.ctx(),
		`SELECT ro.rolname::pg_catalog.text, me.rolname::pg_catalog.text, gr.rolname::pg_catalog.text,
		        m.admin_option, m.inherit_option, m.set_option
		   FROM pg_catalog.pg_auth_members AS m
		   JOIN pg_catalog.pg_roles AS ro ON ro.oid = m.roleid
		   JOIN pg_catalog.pg_roles AS me ON me.oid = m.member
		   JOIN pg_catalog.pg_roles AS gr ON gr.oid = m.grantor
		  WHERE ro.rolname LIKE $1::pg_catalog.text OR me.rolname LIKE $1::pg_catalog.text
		  ORDER BY 1, 2, 3`, "%"+l.suffix)
	if err != nil {
		l.t.Fatalf("read memberships: %v", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []string
	for rows.Next() {
		var role, member, grantor string
		var admin, inherit, set bool
		if err := rows.Scan(&role, &member, &grantor, &admin, &inherit, &set); err != nil {
			l.t.Fatalf("scan memberships: %v", err)
		}
		out = append(out, fmt.Sprintf("%s<-%s by %s admin=%v inherit=%v set=%v", role, member, grantor, admin, inherit, set))
	}
	if err := rows.Err(); err != nil {
		l.t.Fatalf("membership rows: %v", err)
	}
	return strings.Join(out, "\n")
}

// inheritFlags serializes rolinherit for this lab's roles. P1 must not touch it: an
// existing test re-provisions a role it deliberately left NOINHERIT and expects success.
func (l *roleLab) inheritFlags() string {
	l.t.Helper()
	rows, err := l.super.QueryContext(l.ctx(),
		`SELECT r.rolname::pg_catalog.text, r.rolinherit FROM pg_catalog.pg_roles AS r
		  WHERE r.rolname LIKE $1::pg_catalog.text ORDER BY 1`, "%"+l.suffix)
	if err != nil {
		l.t.Fatalf("read rolinherit: %v", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []string
	for rows.Next() {
		var name string
		var inherit bool
		if err := rows.Scan(&name, &inherit); err != nil {
			l.t.Fatalf("scan rolinherit: %v", err)
		}
		out = append(out, fmt.Sprintf("%s inherit=%v", name, inherit))
	}
	if err := rows.Err(); err != nil {
		l.t.Fatalf("rolinherit rows: %v", err)
	}
	return strings.Join(out, "\n")
}

// executorIdentity reports what session_user and current_user actually are on a
// connection, so an arrangement records the identity it measured instead of the
// identity it intended.
func (l *roleLab) executorIdentity(db execQuerier) (session, current string) {
	l.t.Helper()
	if err := db.QueryRowContext(l.ctx(),
		"SELECT SESSION_USER::pg_catalog.text, CURRENT_USER::pg_catalog.text").Scan(&session, &current); err != nil {
		l.t.Fatalf("read the executor identity: %v", err)
	}
	return session, current
}

// --- the internal, test-only seam ------------------------------------------

// seamWatcher wraps the execQuerier the caller already owns. It records what the role
// stage issued, and can substitute an alternative literal query for the Nth catalog
// read so a postcondition fault can be produced WITHOUT a product fault switch and
// without a second session taking a lock the transaction under test already holds.
type seamWatcher struct {
	inner    execQuerier
	stmts    []string
	reads    int
	readAt   int    // which catalog read to substitute (1-based); 0 substitutes none
	readWith string // the literal query to run instead
	// afterRead fires once a catalog read has been delegated and its *sql.Row obtained,
	// which is BEFORE the caller's Scan completes. beforeIdentity fires when the
	// executor-identity query arrives and BEFORE it is delegated — a point the role
	// stage reaches only after a target lookup returned a completed result. The two are
	// different boundaries; the controls below say which one each of them measures.
	afterRead      func()
	beforeIdentity func()
}

func (w *seamWatcher) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	w.stmts = append(w.stmts, query)
	return w.inner.ExecContext(ctx, query, args...)
}

func (w *seamWatcher) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	// stmts records what the role stage ISSUED at this seam, which is what makes the
	// recorded sequence evidence of position rather than of server work.
	w.stmts = append(w.stmts, query)
	if query == executorIdentityQuery && w.beforeIdentity != nil {
		w.beforeIdentity()
	}
	if query == roleCatalogQuery {
		w.reads++
		if w.readAt == w.reads && w.readWith != "" {
			query = w.readWith
		}
		defer func() {
			if w.afterRead != nil {
				w.afterRead()
			}
		}()
	}
	return w.inner.QueryRowContext(ctx, query, args...)
}

// issued reports whether any recorded statement contains needle.
func (w *seamWatcher) issued(needle string) bool {
	for _, s := range w.stmts {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// --- arrangements -----------------------------------------------------------

// TestPostgresRoleStageCreatesThenRerunsUnderMaintenanceAuthority is arrangement 1: a
// CREATEROLE non-superuser that is NOT a superuser creates the application and owner
// roles, then reruns over them — the path the frozen helper could not execute at all,
// because it named NOSUPERUSER on a role that was already NOSUPERUSER.
//
// The rerun is measured twice: once with no password, which must PRESERVE the existing
// credential, and once with a new one, which must rotate it. Both are proved by
// authenticating as the role, not by reading a catalog column.
func TestPostgresRoleStageCreatesThenRerunsUnderMaintenanceAuthority(t *testing.T) {
	lab := newRoleLab(t)
	ctx := lab.ctx()

	maint := lab.name("maint")
	maintPw := lab.password(maint, "1")
	lab.createRole(maint, "CREATEROLE NOSUPERUSER NOBYPASSRLS NOCREATEDB NOREPLICATION", maintPw)
	exec := lab.connectAs(maint, maintPw)

	session, current := lab.executorIdentity(exec)
	if session != maint || current != maint {
		t.Fatalf("executor identity is session_user=%q current_user=%q, want %q for both", session, current, maint)
	}

	owner, app := lab.name("own"), lab.name("app")
	ownerPw, appPw := lab.password(owner, "1"), lab.password(app, "1")

	// Creation. Both roles in ONE transaction, the way ProvisionPostgres calls it.
	inTx(t, ctx, exec, func(tx *sql.Tx) error {
		if err := upsertRole(ctx, tx, owner, attrsUnprivileged, ownerPw); err != nil {
			return fmt.Errorf("owner: %w", err)
		}
		return upsertRole(ctx, tx, app, attrsUnprivileged, appPw)
	})
	lab.created = append(lab.created, owner, app)

	lab.wantConverged(owner, false)
	lab.wantConverged(app, false)
	if !lab.authenticates(app, appPw) {
		t.Error("the created application role cannot authenticate with the password it was created with")
	}
	ownerOID, appOID := lab.mustPosture(owner).OID, lab.mustPosture(app).OID

	// Rerun with NO password: attributes converge, the credential is preserved.
	inTx(t, ctx, exec, func(tx *sql.Tx) error {
		if err := upsertRole(ctx, tx, owner, attrsUnprivileged, ""); err != nil {
			return fmt.Errorf("owner: %w", err)
		}
		return upsertRole(ctx, tx, app, attrsUnprivileged, "")
	})
	lab.wantConverged(owner, false)
	lab.wantConverged(app, false)
	if !lab.authenticates(app, appPw) {
		t.Error("a rerun with no password did not preserve the existing credential")
	}
	if got := lab.mustPosture(app).OID; got != appOID {
		t.Errorf("the application role's OID changed across the rerun: %d -> %d", appOID, got)
	}
	if got := lab.mustPosture(owner).OID; got != ownerOID {
		t.Errorf("the owner role's OID changed across the rerun: %d -> %d", ownerOID, got)
	}

	// Rerun WITH a password: rotation, and the old credential stops working.
	appPw2 := lab.password(app, "2")
	inTx(t, ctx, exec, func(tx *sql.Tx) error {
		return upsertRole(ctx, tx, app, attrsUnprivileged, appPw2)
	})
	lab.wantConverged(app, false)
	if !lab.authenticates(app, appPw2) {
		t.Error("a rerun with a password did not rotate the credential")
	}
	if lab.authenticates(app, appPw) {
		t.Error("the superseded credential still authenticates after rotation")
	}
}

// TestPostgresRoleStageRerunNeedsAdminAuthorityEvenWithZeroDrift is the refusal the
// independent review asked for by name. The target already holds the exact desired
// posture and no password is supplied, so a helper that "optimized" this rerun into no
// statement would report success for a role this executor has no authority over.
//
// 16.15 answers 42501 with the CREATEROLE-plus-ADMIN rule, which is why LOGIN stays in
// the statement: it is the operation that makes the server check.
func TestPostgresRoleStageRerunNeedsAdminAuthorityEvenWithZeroDrift(t *testing.T) {
	lab := newRoleLab(t)
	ctx := lab.ctx()

	maint := lab.name("maint")
	maintPw := lab.password(maint, "1")
	lab.createRole(maint, "CREATEROLE NOSUPERUSER NOBYPASSRLS NOCREATEDB NOREPLICATION", maintPw)
	exec := lab.connectAs(maint, maintPw)

	// Created by the SUPERUSER and never granted to the maintenance role: this is the
	// managed-estate shape, a role that exists and was not made by this provisioner.
	target := lab.name("app")
	lab.createRole(target, "NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION", lab.password(target, "1"))
	before := lab.mustPosture(target)

	watch := &seamWatcher{}
	err := inTxErr(ctx, exec, func(tx *sql.Tx) error {
		watch.inner = tx
		return upsertRole(ctx, watch, target, attrsUnprivileged, "")
	})
	if err == nil {
		t.Fatal("a zero-drift rerun was accepted from an executor with no ADMIN option on the target")
	}
	if !strings.Contains(err.Error(), roleStageAttributes) || !strings.Contains(err.Error(), "SQLSTATE 42501") {
		t.Errorf("the refusal does not name the stage and the SQLSTATE: %v", err)
	}
	if !watch.issued("ALTER ROLE " + target + " WITH LOGIN") {
		t.Errorf("no role administration was attempted at all; the statements were %q", watch.stmts)
	}
	if after := lab.mustPosture(target); after != before {
		t.Errorf("the refused rerun changed the posture: %+v -> %+v", before, after)
	}
}

// TestPostgresRoleStageConvergesUnderExplicitAdminOnAPrecreatedRole is the other
// positive of arrangement 2: the same managed-estate shape as above, made workable the
// way an operator would — by granting the maintenance role ADMIN OPTION on it.
func TestPostgresRoleStageConvergesUnderExplicitAdminOnAPrecreatedRole(t *testing.T) {
	lab := newRoleLab(t)
	ctx := lab.ctx()

	maint := lab.name("maint")
	maintPw := lab.password(maint, "1")
	lab.createRole(maint, "CREATEROLE NOSUPERUSER NOBYPASSRLS NOCREATEDB NOREPLICATION", maintPw)
	exec := lab.connectAs(maint, maintPw)

	target := lab.name("app")
	targetPw := lab.password(target, "1")
	lab.createRole(target, "NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION", targetPw)
	lab.exec("GRANT " + target + " TO " + maint + " WITH ADMIN OPTION")

	membersBefore, inheritBefore := lab.memberships(), lab.inheritFlags()
	before := lab.mustPosture(target)

	inTx(t, ctx, exec, func(tx *sql.Tx) error {
		return upsertRole(ctx, tx, target, attrsUnprivileged, "")
	})

	lab.wantConverged(target, false)
	if after := lab.mustPosture(target); after != before {
		t.Errorf("a zero-drift convergence changed the posture: %+v -> %+v", before, after)
	}
	if !lab.authenticates(target, targetPw) {
		t.Error("a convergence with no password disturbed the existing credential")
	}
	if got := lab.memberships(); got != membersBefore {
		t.Errorf("memberships changed\nbefore:\n%s\nafter:\n%s", membersBefore, got)
	}
	if got := lab.inheritFlags(); got != inheritBefore {
		t.Errorf("rolinherit changed\nbefore:\n%s\nafter:\n%s", inheritBefore, got)
	}
}

// TestPostgresRoleStageConvergesNologinToLogin is the LOGIN-drift positive: an existing
// role the operator (or a provider) left NOLOGIN becomes usable, through the same LOGIN
// that is on every convergence statement.
func TestPostgresRoleStageConvergesNologinToLogin(t *testing.T) {
	lab := newRoleLab(t)
	ctx := lab.ctx()

	maint := lab.name("maint")
	maintPw := lab.password(maint, "1")
	lab.createRole(maint, "CREATEROLE NOSUPERUSER NOBYPASSRLS NOCREATEDB NOREPLICATION", maintPw)
	exec := lab.connectAs(maint, maintPw)

	target := lab.name("app")
	lab.createRole(target, "NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION", "")
	lab.exec("GRANT " + target + " TO " + maint + " WITH ADMIN OPTION")
	if lab.mustPosture(target).Login {
		t.Fatal("the fixture was supposed to start NOLOGIN")
	}

	targetPw := lab.password(target, "1")
	watch := &seamWatcher{}
	inTx(t, ctx, exec, func(tx *sql.Tx) error {
		watch.inner = tx
		return upsertRole(ctx, watch, target, attrsUnprivileged, targetPw)
	})

	lab.wantConverged(target, false)
	if !lab.authenticates(target, targetPw) {
		t.Error("the converged role cannot authenticate with the credential it was given")
	}
	if !watch.issued("ALTER ROLE " + target + " WITH LOGIN") {
		t.Errorf("the convergence statement was not the expected one: %q", watch.stmts)
	}
}

// TestPostgresRoleStageSuperuserZeroDriftControl is the fixture control the review asked
// for: the whole path, on final bytes, under the executor every existing consumer of
// this helper actually uses. Without it, a refusal that broke all five arrangements
// could still look like "the arrangements are wrong".
func TestPostgresRoleStageSuperuserZeroDriftControl(t *testing.T) {
	lab := newRoleLab(t)
	ctx := lab.ctx()

	target := lab.name("app")
	targetPw := lab.password(target, "1")
	lab.createRole(target, "NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION", targetPw)
	before := lab.mustPosture(target)

	inTx(t, ctx, lab.super, func(tx *sql.Tx) error {
		return upsertRole(ctx, tx, target, attrsUnprivileged, targetPw)
	})

	lab.wantConverged(target, false)
	if after := lab.mustPosture(target); after.OID != before.OID {
		t.Errorf("the role's OID moved under a zero-drift superuser rerun: %d -> %d", before.OID, after.OID)
	}
	if !lab.authenticates(target, targetPw) {
		t.Error("the role cannot authenticate after a superuser rerun")
	}
}

// TestPostgresRoleStageRollsBackAPriorRoleWhenAnAttributeIsRefused is the rollback
// evidence, in the exact arrangement the contract names: an executor that CAN correct
// the owner's CREATEDB drift and CANNOT correct the application role's BYPASSRLS drift.
//
// Three things are proved at once. The privileged executor really does correct real
// drift; the refusal is the server's, on the mismatching attribute the stage refused to
// omit; and the owner's corrected value is GONE after the rollback, so a partially
// converged cluster is not what a failed `db init` leaves behind.
//
// It also proves the credential ordering: the application role is provisioned WITH a
// password, and after the refusal no credential was formatted or sent.
func TestPostgresRoleStageRollsBackAPriorRoleWhenAnAttributeIsRefused(t *testing.T) {
	lab := newRoleLab(t)
	ctx := lab.ctx()

	maint := lab.name("maint")
	maintPw := lab.password(maint, "1")
	lab.createRole(maint, "CREATEROLE CREATEDB NOSUPERUSER NOBYPASSRLS NOREPLICATION", maintPw)
	exec := lab.connectAs(maint, maintPw)

	owner, app := lab.name("own"), lab.name("app")
	ownerPw, appPw := lab.password(owner, "1"), lab.password(app, "1")
	lab.createRole(owner, "NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION", ownerPw)
	lab.createRole(app, "NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION", appPw)
	lab.exec("GRANT " + owner + " TO " + maint + " WITH ADMIN OPTION")
	lab.exec("GRANT " + app + " TO " + maint + " WITH ADMIN OPTION")

	// The drift, introduced by the superuser: one the executor holds the attribute for,
	// one it does not.
	lab.exec("ALTER ROLE " + owner + " WITH CREATEDB")
	lab.exec("ALTER ROLE " + app + " WITH BYPASSRLS")
	if !lab.mustPosture(owner).CreateDB || !lab.mustPosture(app).BypassRLS {
		t.Fatal("the drift fixture did not take")
	}
	membersBefore, inheritBefore := lab.memberships(), lab.inheritFlags()

	watch := &seamWatcher{}
	err := inTxErr(ctx, exec, func(tx *sql.Tx) error {
		watch.inner = tx
		if err := upsertRole(ctx, watch, owner, attrsUnprivileged, ""); err != nil {
			return fmt.Errorf("owner: %w", err)
		}
		// Inside the transaction the owner IS corrected; that is what makes the
		// rollback assertion below mean something.
		var createdb bool
		if err := tx.QueryRowContext(ctx,
			`SELECT r.rolcreatedb FROM pg_catalog.pg_roles AS r WHERE r.rolname = $1::pg_catalog.text`, owner).Scan(&createdb); err != nil {
			return fmt.Errorf("read the owner inside the transaction: %w", err)
		}
		if createdb {
			return errors.New("the owner's CREATEDB drift was not corrected inside the transaction")
		}
		return upsertRole(ctx, watch, app, attrsUnprivileged, appPw)
	})
	if err == nil {
		t.Fatal("an executor without BYPASSRLS was allowed to converge a BYPASSRLS application role")
	}
	if !strings.Contains(err.Error(), roleStageAttributes) || !strings.Contains(err.Error(), "SQLSTATE 42501") {
		t.Errorf("the refusal does not name the stage and the SQLSTATE: %v", err)
	}

	if lab.mustPosture(owner).CreateDB != true {
		t.Error("the owner's previous CREATEDB value was not restored by the rollback")
	}
	if !lab.mustPosture(app).BypassRLS {
		t.Error("the application role's BYPASSRLS was changed by a transaction that failed")
	}
	if got := lab.memberships(); got != membersBefore {
		t.Errorf("memberships changed\nbefore:\n%s\nafter:\n%s", membersBefore, got)
	}
	if got := lab.inheritFlags(); got != inheritBefore {
		t.Errorf("rolinherit changed\nbefore:\n%s\nafter:\n%s", inheritBefore, got)
	}

	// The credential ordering, as a fact about what was sent rather than an intention.
	if watch.issued("pg_catalog.format(") {
		t.Error("the credential was formatted after the attribute step was refused")
	}
	for _, s := range watch.stmts {
		if strings.Contains(s, "PASSWORD") || strings.Contains(s, appPw) {
			t.Errorf("a credential-bearing statement was issued after the refusal: %q", s)
		}
	}
	if !watch.issued("ALTER ROLE " + app + " WITH LOGIN NOBYPASSRLS") {
		t.Errorf("the mismatching privileged attribute was not named: %q", watch.stmts)
	}
}

// TestPostgresRuntimeReaderKeepsBypassrlsWithoutReaffirmingIt is arrangement 4, and it
// is the one root recorded as an EXPECTATION read off REL_16_15 `user.c` rather than a
// prior measurement: a CREATEROLE executor that does NOT hold BYPASSRLS, holding ADMIN
// OPTION on a precreated non-superuser BYPASSRLS reader, issues LOGIN and a password
// change while the BYPASSRLS option is omitted from the statement.
//
// The transition and the executor identity are measured here rather than assumed. If a
// server refuses this, this test is the record of the refusal and the reader's
// provisioning authority becomes a decision to take again — the stage does not respond
// by skipping the reader or by conferring an attribute on the executor.
func TestPostgresRuntimeReaderKeepsBypassrlsWithoutReaffirmingIt(t *testing.T) {
	lab := newRoleLab(t)
	ctx := lab.ctx()

	maint := lab.name("maint")
	maintPw := lab.password(maint, "1")
	lab.createRole(maint, "CREATEROLE NOSUPERUSER NOBYPASSRLS NOCREATEDB NOREPLICATION", maintPw)
	exec := lab.connectAs(maint, maintPw)
	if lab.mustPosture(maint).BypassRLS {
		t.Fatal("the executor was supposed to lack BYPASSRLS")
	}
	session, current := lab.executorIdentity(exec)
	t.Logf("executor identity: session_user=%s current_user=%s (rolbypassrls=false)", session, current)

	reader := lab.name("rd")
	readerPw := lab.password(reader, "1")
	lab.createRole(reader, "NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION", readerPw)
	lab.exec("GRANT " + reader + " TO " + maint + " WITH ADMIN OPTION")
	before := lab.mustPosture(reader)

	readerPw2 := lab.password(reader, "2")
	watch := &seamWatcher{}
	err := inTxErr(ctx, exec, func(tx *sql.Tx) error {
		watch.inner = tx
		return upsertRole(ctx, watch, reader, attrsAdmin, readerPw2)
	})
	if err != nil {
		t.Fatalf("MEASURED REFUSAL of the expected transition (executor session_user=%s current_user=%s, "+
			"target rolbypassrls=true, BYPASSRLS omitted from the statement): %v; statements: %q",
			session, current, err, watch.stmts)
	}

	if got, want := statementFor(watch, "ALTER ROLE "+reader), "ALTER ROLE "+reader+" WITH LOGIN"; got != want {
		t.Errorf("the attribute statement was %q, want %q — BYPASSRLS must not be reaffirmed", got, want)
	}
	lab.wantConverged(reader, true)
	if after := lab.mustPosture(reader); after.OID != before.OID {
		t.Errorf("the reader's OID moved: %d -> %d", before.OID, after.OID)
	}
	if !lab.authenticates(reader, readerPw2) {
		t.Error("the reader cannot authenticate with the rotated credential")
	}
}

// TestPostgresRoleStageRefusesBypassrlsWithoutServerAuthority is the other half of
// arrangement 4, and the half that must NOT be softened: creating a reader, or
// correcting a real BYPASSRLS drift, still requires the executor to hold the attribute.
// Omitting an unchanged option is not the same as never naming it.
func TestPostgresRoleStageRefusesBypassrlsWithoutServerAuthority(t *testing.T) {
	lab := newRoleLab(t)
	ctx := lab.ctx()

	maint := lab.name("maint")
	maintPw := lab.password(maint, "1")
	lab.createRole(maint, "CREATEROLE NOSUPERUSER NOBYPASSRLS NOCREATEDB NOREPLICATION", maintPw)
	exec := lab.connectAs(maint, maintPw)

	t.Run("creation is refused", func(t *testing.T) {
		fresh := lab.name("rdnew")
		err := inTxErr(ctx, exec, func(tx *sql.Tx) error {
			return upsertRole(ctx, tx, fresh, attrsAdmin, lab.password(fresh, "1"))
		})
		if err == nil {
			t.Fatal("an executor without BYPASSRLS created a BYPASSRLS reader")
		}
		if !strings.Contains(err.Error(), roleStageCreate) || !strings.Contains(err.Error(), "SQLSTATE 42501") {
			t.Errorf("the refusal does not name the create stage and the SQLSTATE: %v", err)
		}
		if _, ok := lab.posture(fresh); ok {
			t.Error("the refused creation left a role behind")
			lab.created = append(lab.created, fresh)
		}
	})

	t.Run("real drift is refused", func(t *testing.T) {
		target := lab.name("rddrift")
		lab.createRole(target, "NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION", lab.password(target, "1"))
		lab.exec("GRANT " + target + " TO " + maint + " WITH ADMIN OPTION")
		before := lab.mustPosture(target)

		watch := &seamWatcher{}
		err := inTxErr(ctx, exec, func(tx *sql.Tx) error {
			watch.inner = tx
			return upsertRole(ctx, watch, target, attrsAdmin, "")
		})
		if err == nil {
			t.Fatal("an executor without BYPASSRLS granted BYPASSRLS")
		}
		if !strings.Contains(err.Error(), roleStageAttributes) || !strings.Contains(err.Error(), "SQLSTATE 42501") {
			t.Errorf("the refusal does not name the stage and the SQLSTATE: %v", err)
		}
		if !watch.issued("ALTER ROLE " + target + " WITH LOGIN BYPASSRLS") {
			t.Errorf("the drifted attribute was not named: %q", watch.stmts)
		}
		if after := lab.mustPosture(target); after != before {
			t.Errorf("the refused convergence changed the posture: %+v -> %+v", before, after)
		}
	})
}

// TestPostgresRoleStageRefusesAdministrativeIdentities is arrangement 5. Each case is an
// identity the role stage must not converge, and each is proved by the posture being
// untouched — the refusal happens before any write, so nothing is left to roll back.
func TestPostgresRoleStageRefusesAdministrativeIdentities(t *testing.T) {
	lab := newRoleLab(t)
	ctx := lab.ctx()

	maint := lab.name("maint")
	maintPw := lab.password(maint, "1")
	lab.createRole(maint, "CREATEROLE NOSUPERUSER NOBYPASSRLS NOCREATEDB NOREPLICATION", maintPw)
	exec := lab.connectAs(maint, maintPw)

	other := lab.name("su")
	lab.createRole(other, "SUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION", lab.password(other, "1"))

	bootstrapSuper := lab.superCfg.User

	t.Run("the executor's own login", func(t *testing.T) {
		before := lab.mustPosture(maint)
		watch := &seamWatcher{}
		err := inTxErr(ctx, exec, func(tx *sql.Tx) error {
			watch.inner = tx
			return upsertRole(ctx, watch, maint, attrsUnprivileged, lab.password(maint, "2"))
		})
		if err == nil {
			t.Fatal("provisioning converged the login it is running as")
		}
		if !strings.Contains(err.Error(), "running as") {
			t.Errorf("the refusal does not say why: %v", err)
		}
		assertNoRoleWrite(t, watch)
		if after := lab.mustPosture(maint); after != before {
			t.Errorf("the executor's own role changed: %+v -> %+v", before, after)
		}
		if !lab.authenticates(maint, maintPw) {
			t.Error("the executor's own credential was rotated by a refused call")
		}
	})

	t.Run("the effective role after SET ROLE", func(t *testing.T) {
		target := lab.name("setrole")
		lab.createRole(target, "NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION", lab.password(target, "1"))
		before := lab.mustPosture(target)

		watch := &seamWatcher{}
		err := inTxErr(ctx, lab.super, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+target); err != nil {
				return fmt.Errorf("set role: %w", err)
			}
			watch.inner = tx
			return upsertRole(ctx, watch, target, attrsUnprivileged, lab.password(target, "2"))
		})
		if err == nil {
			t.Fatal("provisioning converged its own effective role")
		}
		if !strings.Contains(err.Error(), "running as") {
			t.Errorf("the refusal does not say why: %v", err)
		}
		assertNoRoleWrite(t, watch)
		if after := lab.mustPosture(target); after != before {
			t.Errorf("the effective role changed: %+v -> %+v", before, after)
		}
	})

	t.Run("a distinct existing superuser", func(t *testing.T) {
		before := lab.mustPosture(other)
		watch := &seamWatcher{}
		err := inTxErr(ctx, lab.super, func(tx *sql.Tx) error {
			watch.inner = tx
			return upsertRole(ctx, watch, other, attrsUnprivileged, lab.password(other, "2"))
		})
		if err == nil {
			t.Fatal("a superuser executor demoted another superuser as application-role drift")
		}
		if !strings.Contains(err.Error(), "SUPERUSER") {
			t.Errorf("the refusal does not name the posture: %v", err)
		}
		assertNoRoleWrite(t, watch)
		if after := lab.mustPosture(other); after != before || !after.Superuser {
			t.Errorf("the superuser target changed: %+v -> %+v", before, after)
		}
	})

	// The bootstrap superuser, as a rejection control ONLY. Nothing here claims the
	// server would have permitted its demotion — REL_16_15 prevents that separately —
	// and the point is that the product refuses before the question reaches the server.
	t.Run("the bootstrap superuser", func(t *testing.T) {
		before := lab.mustPosture(bootstrapSuper)
		watch := &seamWatcher{}
		err := inTxErr(ctx, exec, func(tx *sql.Tx) error {
			watch.inner = tx
			return upsertRole(ctx, watch, bootstrapSuper, attrsUnprivileged, "")
		})
		if err == nil {
			t.Fatalf("provisioning accepted %q as an application role", bootstrapSuper)
		}
		if !strings.Contains(err.Error(), "SUPERUSER") {
			t.Errorf("the refusal does not name the posture: %v", err)
		}
		assertNoRoleWrite(t, watch)
		if after := lab.mustPosture(bootstrapSuper); after != before || !after.Superuser {
			t.Errorf("the bootstrap superuser changed: %+v -> %+v", before, after)
		}
	})
}

// TestPostgresRoleStageNeverTurnsAnAbsentAnswerIntoACreate is arrangement 3. A missing
// password, a refused catalog read and a cancellation are different failures, and none
// of them may end as a created role.
//
// Cancellation is measured at TWO distinct points, because they are not the same claim.
// One interrupts the catalog read itself; the other fires at the executor-identity query,
// which upsertRole reaches only after the target lookup returned a completed result, and
// is therefore the state in which "canceled" could have been mistaken for "absent".
func TestPostgresRoleStageNeverTurnsAnAbsentAnswerIntoACreate(t *testing.T) {
	lab := newRoleLab(t)
	ctx := lab.ctx()

	maint := lab.name("maint")
	maintPw := lab.password(maint, "1")
	lab.createRole(maint, "CREATEROLE NOSUPERUSER NOBYPASSRLS NOCREATEDB NOREPLICATION", maintPw)
	exec := lab.connectAs(maint, maintPw)

	t.Run("no password", func(t *testing.T) {
		fresh := lab.name("nopw")
		err := inTxErr(ctx, exec, func(tx *sql.Tx) error {
			return upsertRole(ctx, tx, fresh, attrsUnprivileged, "")
		})
		if err == nil || !strings.Contains(err.Error(), "without a password") {
			t.Fatalf("err = %v, want a refusal naming the missing password", err)
		}
		if _, ok := lab.posture(fresh); ok {
			t.Error("a role was created without a password")
			lab.created = append(lab.created, fresh)
		}
	})

	// A catalog read the server actually refuses. The transaction is deliberately put
	// into the aborted state first, which is a real 25P02 from a real server rather
	// than a fabricated driver error — and it is exactly the shape "I could not look"
	// takes in production when an earlier statement in the same transaction failed.
	t.Run("refused catalog read", func(t *testing.T) {
		fresh := lab.name("noread")
		err := inTxErr(ctx, exec, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "SELECT 1::pg_catalog.int4 / 0::pg_catalog.int4"); err == nil {
				return errors.New("the fixture failed to abort the transaction")
			}
			return upsertRole(ctx, tx, fresh, attrsUnprivileged, lab.password(fresh, "1"))
		})
		if err == nil {
			t.Fatal("an unreadable catalog was reported as a provisioned role")
		}
		if !strings.Contains(err.Error(), roleStageLookup) {
			t.Errorf("the refusal does not name the lookup stage: %v", err)
		}
		if _, ok := lab.posture(fresh); ok {
			t.Error("a refused catalog read still selected creation")
			lab.created = append(lab.created, fresh)
		}
	})

	// Cancellation WHILE THE CATALOG READ IS IN FLIGHT: the hook fires once the row has
	// been obtained from the server and before the caller's Scan completes. Which side
	// of that race a given execution takes is not asserted here, and this control is not
	// the post-lookup boundary; it proves that an interrupted read on a real server is
	// reported as a cancellation, writes nothing, and leaves no role behind.
	t.Run("cancellation while the catalog read is in flight", func(t *testing.T) {
		fresh := lab.name("cancel")
		cancelCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		watch := &seamWatcher{afterRead: cancel}
		err := inTxErr(cancelCtx, exec, func(tx *sql.Tx) error {
			watch.inner = tx
			return upsertRole(cancelCtx, watch, fresh, attrsUnprivileged, lab.password(fresh, "1"))
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if watch.issued("CREATE ROLE") {
			t.Errorf("a canceled context selected creation: %q", watch.stmts)
		}
		if _, ok := lab.posture(fresh); ok {
			t.Error("the canceled call left a role behind")
			lab.created = append(lab.created, fresh)
		}
	})

	// Cancellation AT THE EXECUTOR-IDENTITY BOUNDARY, on the real server, after a target
	// lookup that completed and answered "absent". This is the interleaving the
	// construction names: the stage is holding the very result that would select
	// creation, and the context is already gone. The recorded seam sequence is exactly
	// the target lookup followed by the identity query, and nothing after it.
	t.Run("cancellation at the executor-identity boundary after a completed lookup", func(t *testing.T) {
		fresh := lab.name("cancelid")
		cancelCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		watch := &seamWatcher{beforeIdentity: cancel}
		err := inTxErr(cancelCtx, exec, func(tx *sql.Tx) error {
			watch.inner = tx
			return upsertRole(cancelCtx, watch, fresh, attrsUnprivileged, lab.password(fresh, "1"))
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if !strings.Contains(err.Error(), roleStageIdentity) {
			t.Errorf("the diagnostic does not name the stage the cancellation was observed at: %v", err)
		}
		if len(watch.stmts) != 2 || watch.stmts[0] != roleCatalogQuery || watch.stmts[1] != executorIdentityQuery {
			t.Fatalf("the seam saw %d statements; want exactly one completed target lookup then the identity query: %q",
				len(watch.stmts), watch.stmts)
		}
		if watch.issued("CREATE ROLE") || watch.issued("ALTER ROLE") || watch.issued("pg_catalog.format(") {
			t.Errorf("a write or a credential formatting followed the cancellation: %q", watch.stmts)
		}
		_, present := lab.posture(fresh)
		if present {
			t.Error("the canceled call left a role behind")
			lab.created = append(lab.created, fresh)
		}
		// Recorded so the run's own stream carries the measurement rather than only the
		// verdict of an assertion. No value here can contain a credential: the seam saw
		// two catalog reads and the diagnostic is stage plus role name.
		t.Logf("observed boundary: seam statements=%d (target lookup, then executor identity); diagnostic=%v; target present afterwards=%v",
			len(watch.stmts), err, present)
	})
}

// TestPostgresRoleStagePostconditionRefusesRealCatalogDivergence drives the postcondition
// against the real catalog through the caller's own seam. The substituted reads are
// literal queries written here; they perturb exactly one thing each, which is what a
// dropped-and-recreated role or a write that did not take would look like.
//
// This is a test-only substitution at the seam the caller already passes in. There is no
// fault switch in the product, and there is nothing to disable in a deployment.
func TestPostgresRoleStagePostconditionRefusesRealCatalogDivergence(t *testing.T) {
	lab := newRoleLab(t)
	ctx := lab.ctx()

	target := lab.name("app")
	targetPw := lab.password(target, "1")
	lab.createRole(target, "NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION", targetPw)
	before := lab.mustPosture(target)

	const shiftedOID = `SELECT r.oid::pg_catalog.int8 + 1, r.rolname::pg_catalog.text,
	       r.rolcanlogin, r.rolsuper, r.rolbypassrls,
	       r.rolcreaterole, r.rolcreatedb, r.rolreplication
	  FROM pg_catalog.pg_roles AS r WHERE r.rolname = $1::pg_catalog.text`
	const negatedLogin = `SELECT r.oid::pg_catalog.int8, r.rolname::pg_catalog.text,
	       NOT r.rolcanlogin, r.rolsuper, r.rolbypassrls,
	       r.rolcreaterole, r.rolcreatedb, r.rolreplication
	  FROM pg_catalog.pg_roles AS r WHERE r.rolname = $1::pg_catalog.text`
	const noRow = `SELECT r.oid::pg_catalog.int8, r.rolname::pg_catalog.text,
	       r.rolcanlogin, r.rolsuper, r.rolbypassrls,
	       r.rolcreaterole, r.rolcreatedb, r.rolreplication
	  FROM pg_catalog.pg_roles AS r WHERE r.rolname = $1::pg_catalog.text AND false`

	cases := []struct {
		name string
		with string
		want string
	}{
		{"the role's catalog identity moved", shiftedOID, "catalog identity changed"},
		{"a flag did not stick", negatedLogin, "login did not converge"},
		{"the role vanished", noRow, "absent from the catalog"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			watch := &seamWatcher{readAt: 2, readWith: tc.with}
			err := inTxErr(ctx, lab.super, func(tx *sql.Tx) error {
				watch.inner = tx
				return upsertRole(ctx, watch, target, attrsUnprivileged, targetPw)
			})
			if err == nil {
				t.Fatal("the postcondition accepted a catalog that diverged")
			}
			if !strings.Contains(err.Error(), roleStagePostcondition) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the diagnostic does not name the postcondition and %q: %v", tc.want, err)
			}
			if after := lab.mustPosture(target); after != before {
				t.Errorf("the failed transaction changed the posture: %+v -> %+v", before, after)
			}
		})
	}

	// The positive control for the substitution machinery itself: with no substitution
	// the same call succeeds, so the three refusals above are the fault and not the
	// wrapper.
	t.Run("unperturbed control", func(t *testing.T) {
		watch := &seamWatcher{}
		if err := inTxErr(ctx, lab.super, func(tx *sql.Tx) error {
			watch.inner = tx
			return upsertRole(ctx, watch, target, attrsUnprivileged, targetPw)
		}); err != nil {
			t.Fatalf("the unperturbed call was refused: %v", err)
		}
		lab.wantConverged(target, false)
	})
}

// --- small helpers ----------------------------------------------------------

// inTx runs body in a transaction on db and COMMITS, failing the test on any error. It
// is the shape ProvisionPostgres uses around the role stage: one transaction, several
// requested roles, commit at the end.
func inTx(t *testing.T, ctx context.Context, db *sql.DB, body func(*sql.Tx) error) {
	t.Helper()
	if err := inTxErr(ctx, db, body); err != nil {
		t.Fatalf("role transaction: %v", err)
	}
}

// inTxErr is the same, returning the error instead — for the arrangements whose subject
// IS the refusal. The rollback is deferred, so a failing body leaves nothing behind.
func inTxErr(ctx context.Context, db *sql.DB, body func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // committed below; the rollback is the error path
	if err := body(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// statementFor returns the first recorded statement starting with prefix, or "".
func statementFor(w *seamWatcher, prefix string) string {
	for _, s := range w.stmts {
		if strings.HasPrefix(s, prefix) {
			return s
		}
	}
	return ""
}

// assertNoRoleWrite proves a refusal happened before any write, which is the difference
// between "the transaction rolled back" and "nothing was attempted".
func assertNoRoleWrite(t *testing.T, w *seamWatcher) {
	t.Helper()
	for _, s := range w.stmts {
		if strings.HasPrefix(s, "CREATE ROLE") || strings.HasPrefix(s, "ALTER ROLE") || strings.Contains(s, "pg_catalog.format(") {
			t.Errorf("a write or a credential formatting was attempted before the refusal: %q", s)
		}
	}
}
