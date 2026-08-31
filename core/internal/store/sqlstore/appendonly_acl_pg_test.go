// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These are the regressions the package doc of core/internal/pgtest admitted did not
// exist: "no test in this repository yet asserts that an UPDATE/DELETE as the app role
// is refused". They assert the stronger property too — that TRUNCATE, which no row
// trigger can see, is refused — and they do it as a role whose name is NOT the
// compile-time default, which is the deployment shape the defect lived in.
//
// They cannot use pgtest.Isolate: it deliberately refuses any app role but
// dialect.DefaultAppRole, because until now the schema hard-coded that name. They go
// through pgtest.Provision instead, the same ownership-safe path
// TestProvisionPostgresSingleRole uses, which takes an arbitrary spec and drops what
// it created.

// customRolePG provisions an isolated database owned by a CUSTOM application role and
// returns a DSN for it. The names follow pgtest's teardown patterns
// (olv_<hex> / olv_app_<hex>) so Drop is authorized to remove exactly what was made.
func customRolePG(t *testing.T) (dsn, role string) {
	t.Helper()
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to run the append-only ACL leg", pgtest.EnvSuperuserDSN)
	}
	superDSN := os.Getenv(pgtest.EnvSuperuserDSN)
	suffix := pgtest.Suffix(t)
	role = "olv_app_" + suffix
	spec := store.PgProvisionSpec{
		Database: "olv_" + suffix,
		App:      store.PgRole{Name: role, Password: "pw-" + suffix},
	}
	pgtest.Provision(t, superDSN, spec, ProvisionPostgres, role)

	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse %s: %v", pgtest.EnvSuperuserDSN, err)
	}
	u.User = url.UserPassword(spec.App.Name, spec.App.Password)
	u.Path = "/" + spec.Database
	return u.String(), role
}

// TestCustomAppRoleCannotTruncateAppendOnly is the headline regression. Before the
// fix the revoke named the literal "olivares_app", the pg_roles gate was false for
// this role, and the statement was a silent no-op — so this role could TRUNCATE the
// audit ledger. The immutability trigger does not help: TRUNCATE is statement-level.
func TestCustomAppRoleCannotTruncateAppendOnly(t *testing.T) {
	dsn, role := customRolePG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open store as custom app role %q: %v", role, err)
	}
	t.Cleanup(func() { _ = st.Close() })

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck

	var canTruncate, canUpdate, canDelete bool
	if err := db.QueryRowContext(ctx, `SELECT
  has_table_privilege('audit_events','TRUNCATE'),
  has_table_privilege('audit_events','UPDATE'),
  has_table_privilege('audit_events','DELETE')`).Scan(&canTruncate, &canUpdate, &canDelete); err != nil {
		t.Fatalf("read effective privileges: %v", err)
	}
	if canTruncate || canUpdate || canDelete {
		t.Errorf("role %q still holds TRUNCATE=%v UPDATE=%v DELETE=%v on audit_events — the append-only revoke did not reach the effective role",
			role, canTruncate, canUpdate, canDelete)
	}

	// Behavioral proof, not just catalog cosmetics: the statement itself must fail.
	if _, err := db.ExecContext(ctx, "TRUNCATE audit_events"); err == nil {
		t.Fatal("TRUNCATE audit_events SUCCEEDED as the application role — the audit ledger is wipeable")
	}
}

// TestBootReconcileHealsAPrivilegeGrantedBack proves the SELF-HEALING half: a
// privilege granted back behind the engine's back is taken away again by the next
// boot's reconcile, so the store opens and the boundary holds.
//
// It is deliberately not a refusal test — an earlier name and comment claimed it was,
// which was wrong, and a test whose name misdescribes its assertion is worse than no
// test. The refusal path is TestBootRefusesWhenTheACLCannotBeClosed below, where the
// privilege arrives through PUBLIC and no role-targeted revoke can remove it.
func TestBootReconcileHealsAPrivilegeGrantedBack(t *testing.T) {
	dsn, role := customRolePG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	// The app role owns the schema in this topology, so it can grant to itself —
	// which is precisely the residual this check exists to DETECT (it cannot prevent
	// it; only the statement-level trigger can).
	if _, err := db.ExecContext(ctx, "GRANT TRUNCATE ON audit_events TO "+role); err != nil {
		t.Fatalf("re-grant TRUNCATE: %v", err)
	}

	// The reconcile pass runs before the verification, so it must have taken the
	// privilege away again and the boot must succeed. That is the self-healing half.
	st2, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("second open after a re-grant should self-heal via the ACL reconcile: %v", err)
	}
	_ = st2.Close()

	var canTruncate bool
	if err := db.QueryRowContext(ctx,
		"SELECT has_table_privilege('audit_events','TRUNCATE')").Scan(&canTruncate); err != nil {
		t.Fatalf("read effective privilege: %v", err)
	}
	if canTruncate {
		t.Error("the boot ACL reconcile did not remove a privilege that was granted back")
	}
}

// TestBootRefusesWhenTheACLCannotBeClosed covers the leg the reconcile CANNOT repair:
// a privilege held through PUBLIC. Revoking from a role by name does not touch it, so
// this is exactly the case that would pass unnoticed without reading the effective
// privilege back — and the boot must refuse, deny-closed.
func TestBootRefusesWhenTheACLCannotBeClosed(t *testing.T) {
	dsn, _ := customRolePG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	if _, err := db.ExecContext(ctx, "GRANT TRUNCATE ON audit_events TO PUBLIC"); err != nil {
		t.Fatalf("grant to PUBLIC: %v", err)
	}

	_, err = Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err == nil {
		t.Fatal("boot SUCCEEDED with TRUNCATE granted to PUBLIC on audit_events — the verification is not fail-closed")
	}
	if !errors.Is(err, store.ErrAppendOnlyACLOpen) {
		t.Errorf("expected ErrAppendOnlyACLOpen, got: %v", err)
	}
}

// TestRoleNameContainingADollarTagStillBoots is the live regression for a defect an
// earlier revision of this change shipped: the revoke was rendered inside a FIXED
// dollar tag, so a legal role name containing that tag made every statement fail to
// parse and a perfectly ordinary deployment stopped booting.
func TestRoleNameContainingADollarTagStillBoots(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s to run the dollar-tag leg", pgtest.EnvSuperuserDSN)
	}
	superDSN := os.Getenv(pgtest.EnvSuperuserDSN)
	suffix := pgtest.Suffix(t)
	// pgtest's teardown patterns constrain the prefix, so the tag goes in the middle
	// of a name that still matches olv_app_<hex>… — PostgreSQL accepts it quoted.
	role := "olv_app_" + suffix + "$olv$x"
	spec := store.PgProvisionSpec{
		Database: "olv_" + suffix,
		App:      store.PgRole{Name: role, Password: "pw-" + suffix},
	}
	// ProvisionPostgres validates identifiers, and this one deliberately is not a
	// plain identifier, so provision the pieces directly and clean up by hand.
	adminDB, err := sql.Open("pgx", superDSN)
	if err != nil {
		t.Fatalf("open superuser: %v", err)
	}
	defer adminDB.Close() //nolint:errcheck
	ctx := context.Background()
	quoted := `"` + strings.ReplaceAll(role, `"`, `""`) + `"`
	if _, err := adminDB.ExecContext(ctx, "CREATE ROLE "+quoted+" LOGIN PASSWORD '"+spec.App.Password+"' NOSUPERUSER NOBYPASSRLS"); err != nil {
		t.Fatalf("create role with a dollar tag in its name: %v", err)
	}
	t.Cleanup(func() {
		// A FRESH maintenance connection: the deferred Close above runs before
		// t.Cleanup, so a cleanup that reused adminDB would silently do nothing and
		// leak the role and database onto a shared server (measured: 21 of each).
		cleanup, err := sql.Open("pgx", superDSN)
		if err != nil {
			t.Logf("cleanup: open maintenance connection: %v", err)
			return
		}
		defer cleanup.Close() //nolint:errcheck
		if _, err := cleanup.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+spec.Database); err != nil {
			t.Logf("cleanup: drop database: %v", err)
		}
		if _, err := cleanup.ExecContext(context.Background(), "DROP ROLE IF EXISTS "+quoted); err != nil {
			t.Logf("cleanup: drop role: %v", err)
		}
	})
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE "+spec.Database+" OWNER "+quoted); err != nil {
		t.Fatalf("create database: %v", err)
	}

	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse %s: %v", pgtest.EnvSuperuserDSN, err)
	}
	u.User = url.UserPassword(role, spec.App.Password)
	u.Path = "/" + spec.Database

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: u.String(), MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("a role whose name contains a dollar-quote tag must still boot: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	var canTruncate bool
	if err := db.QueryRowContext(ctx,
		"SELECT has_table_privilege('audit_events','TRUNCATE')").Scan(&canTruncate); err != nil {
		t.Fatalf("read effective privilege: %v", err)
	}
	if canTruncate {
		t.Error("the revoke did not apply to a role whose name contains a dollar tag")
	}
}

// TestProvisioningRerunLeavesTheACLClosed is the regression round 2 found missing: the
// code was fixed so `db init` takes mutation back in the same transaction as its bulk
// grant, but nothing failed when that fix was reverted. This runs provisioning AGAIN
// over an existing schema — the documented, expected operator action — and asserts the
// append-only ACL is still closed afterwards, without an intervening boot.
func TestProvisioningRerunLeavesTheACLClosed(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s to run the provisioning re-run leg", pgtest.EnvSuperuserDSN)
	}
	superDSN := os.Getenv(pgtest.EnvSuperuserDSN)
	suffix := pgtest.Suffix(t)
	app := store.PgRole{Name: "olv_app_" + suffix, Password: "pw-" + suffix}
	owner := store.PgRole{Name: "olv_own_" + suffix, Password: "opw-" + suffix}
	spec := store.PgProvisionSpec{Database: "olv_" + suffix, App: app, Owner: owner}
	pgtest.Provision(t, superDSN, spec, ProvisionPostgres, app.Name, owner.Name)

	dsn := func(role store.PgRole) string {
		u, err := url.Parse(superDSN)
		if err != nil {
			t.Fatalf("parse %s: %v", pgtest.EnvSuperuserDSN, err)
		}
		u.User = url.UserPassword(role.Name, role.Password)
		u.Path = "/" + spec.Database
		return u.String()
	}
	ctx := context.Background()
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsn(app), OwnerDSN: dsn(owner), MaxConns: 4,
	}, registerWidget)
	if err != nil {
		t.Fatalf("open split store: %v", err)
	}
	_ = st.Close()

	// Re-run provisioning over the now-populated schema, exactly as an operator would
	// after a password rotation or from converged config management.
	if _, err := ProvisionPostgres(ctx, superDSN, spec, true); err != nil {
		t.Fatalf("re-run provisioning: %v", err)
	}

	db, err := sql.Open("pgx", dsn(app))
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	var canSelect, canInsert, canUpdate, canDelete, canTruncate bool
	if err := db.QueryRowContext(ctx, `SELECT
  has_table_privilege('audit_events','SELECT'), has_table_privilege('audit_events','INSERT'),
  has_table_privilege('audit_events','UPDATE'), has_table_privilege('audit_events','DELETE'),
  has_table_privilege('audit_events','TRUNCATE')`).Scan(
		&canSelect, &canInsert, &canUpdate, &canDelete, &canTruncate); err != nil {
		t.Fatalf("read effective privileges: %v", err)
	}
	if !canSelect || !canInsert {
		t.Errorf("provisioning left the app unable to record evidence: SELECT=%v INSERT=%v", canSelect, canInsert)
	}
	if canUpdate || canDelete || canTruncate {
		t.Errorf("a provisioning re-run reopened mutation on audit_events: UPDATE=%v DELETE=%v TRUNCATE=%v — the bulk grant's take-back did not hold",
			canUpdate, canDelete, canTruncate)
	}
}

// TestInactiveOrphanNeedsNoInsert pins the asymmetry between the two halves of the
// verification. A table left behind by a module that is no longer built holds evidence
// that must not become wipeable — but nothing writes it any more, so demanding INSERT
// on it would refuse a legitimate deployment for failing to do something nobody does.
func TestInactiveOrphanNeedsNoInsert(t *testing.T) {
	dsn, role := customRolePG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	for _, stmt := range []string{
		"CREATE TABLE retired_evidence (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL)",
		"CREATE TRIGGER retired_evidence_immutable BEFORE UPDATE OR DELETE ON retired_evidence " +
			"FOR EACH ROW EXECUTE FUNCTION olivares_block_mutation()",
		"REVOKE INSERT ON retired_evidence FROM " + role,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed retired table (%s): %v", stmt, err)
		}
	}

	st2, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("an unregistered retained table needs no INSERT; boot must not refuse: %v", err)
	}
	_ = st2.Close()

	// …and it is still protected against mutation, which is the half that DOES apply.
	var canTruncate bool
	if err := db.QueryRowContext(ctx,
		"SELECT has_table_privilege('retired_evidence','TRUNCATE')").Scan(&canTruncate); err != nil {
		t.Fatalf("read effective privilege: %v", err)
	}
	if canTruncate {
		t.Error("the retained table lost its mutation guard")
	}
}

// TestGuardedTableWithACommaInItsNameIsCovered is the regression for a defect the
// round-1 fix introduced: widening the scope to catalog-discovered names meant the
// names were no longer registry-validated, while the query still packed them into a
// comma-delimited string. A legal table named with a comma was silently dropped from
// the scope and kept its TRUNCATE.
func TestGuardedTableWithACommaInItsNameIsCovered(t *testing.T) {
	dsn, role := customRolePG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	for _, stmt := range []string{
		`CREATE TABLE "retained,evidence" (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL)`,
		`CREATE TRIGGER "retained,evidence_immutable" BEFORE UPDATE OR DELETE ON "retained,evidence" ` +
			`FOR EACH ROW EXECUTE FUNCTION olivares_block_mutation()`,
		`GRANT TRUNCATE ON "retained,evidence" TO ` + role,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed comma-named table (%s): %v", stmt, err)
		}
	}

	st2, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("reopen with a comma-named guarded table: %v", err)
	}
	_ = st2.Close()

	var canTruncate bool
	if err := db.QueryRowContext(ctx,
		`SELECT has_table_privilege('"retained,evidence"','TRUNCATE')`).Scan(&canTruncate); err != nil {
		t.Fatalf("read effective privilege: %v", err)
	}
	if canTruncate {
		t.Error("a guarded table whose name contains a comma was dropped from the ACL scope and kept TRUNCATE")
	}
}

// TestAppSearchPathCannotHideAnOpenACL is the regression for the other defect the
// round-1 fix introduced: the inventory resolved its schema from current_schema(), so
// an application role whose search_path pointed elsewhere produced an EMPTY scope and
// the verification passed while the real ledger sat open in the engine's schema.
func TestAppSearchPathCannotHideAnOpenACL(t *testing.T) {
	dsn, role := customRolePG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	for _, stmt := range []string{
		"CREATE SCHEMA other",
		"ALTER ROLE " + role + " SET search_path = other, public",
		"GRANT TRUNCATE ON audit_events TO PUBLIC",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed search_path case (%s): %v", stmt, err)
		}
	}

	if _, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget); err == nil {
		t.Fatal("boot SUCCEEDED with public.audit_events open to PUBLIC because the app's search_path pointed elsewhere")
	} else if !errors.Is(err, store.ErrAppendOnlyACLOpen) {
		t.Errorf("expected ErrAppendOnlyACLOpen, got: %v", err)
	}
}

// TestBootRefusesWithoutSchemaUsage pins the prerequisite every table privilege
// silently assumes. Table SELECT/INSERT prove nothing if the role may not USE the
// schema: PostgreSQL refuses the query outright. Measured before this check existed:
// boot accepted such a role and the first productive query failed with
// "permission denied for schema public".
// It runs in the SPLIT topology deliberately. In the single-role default the app role
// owns the schema and holds USAGE implicitly, so the privilege cannot be taken away
// from it and the case does not exist; it is the non-owner app of a split that can be
// left with a complete table ACL and no way to reach it.
func TestBootRefusesWithoutSchemaUsage(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 4}

	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("first open (split): %v", err)
	}
	_ = st.Close()

	// Revoke as the OWNER: the grants being undone are the ones `db init` made.
	ownerDB, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatalf("open owner connection: %v", err)
	}
	defer ownerDB.Close() //nolint:errcheck
	var appRole string
	appDB, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatalf("open app connection: %v", err)
	}
	defer appDB.Close() //nolint:errcheck
	if err := appDB.QueryRowContext(ctx, "SELECT current_user").Scan(&appRole); err != nil {
		t.Fatalf("read app role: %v", err)
	}
	for _, stmt := range []string{
		"REVOKE USAGE ON SCHEMA public FROM PUBLIC",
		"REVOKE USAGE ON SCHEMA public FROM " + appRole,
	} {
		if _, err := ownerDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("revoke schema usage (%s): %v", stmt, err)
		}
	}
	// The table ACL is untouched — which is the whole point: it still looks complete.
	// Asked from the OWNER with the three-argument form, because the app connection
	// can no longer even RESOLVE the table name without schema USAGE (it answers
	// `relation "audit_events" does not exist`), which is itself the defect.
	var canSelect bool
	if err := ownerDB.QueryRowContext(ctx,
		"SELECT has_table_privilege($1, 'public.audit_events', 'SELECT')", appRole).Scan(&canSelect); err != nil {
		t.Fatalf("read table privilege: %v", err)
	}
	if !canSelect {
		t.Fatal("fixture precondition: the app should still hold SELECT on the table itself")
	}

	if _, err := Open(ctx, cfg, registerWidget); err == nil {
		t.Fatal("boot SUCCEEDED without USAGE on the engine schema — no query it attested could actually run")
	} else if !errors.Is(err, store.ErrEngineSchemaUnusable) {
		t.Errorf("expected ErrEngineSchemaUnusable, got: %v", err)
	}
}

// TestSplitReadinessRefusesAMissingMutableGrant covers the readiness half for the
// MUTABLE tables: a split app role that cannot touch a registered tenant table must
// not be told the store is ready.
//
// An earlier version of this test dressed the same assertion up as a search_path
// scenario and was VACUOUS — it created the shadowing schema without granting the app
// USAGE, so PostgreSQL skipped it, current_schema() stayed "public", and the scenario
// it claimed to build never existed. The search_path property is now covered properly
// by TestShadowSchemaCannotHijackTheEngine below.
func TestSplitReadinessRefusesAMissingMutableGrant(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 4}

	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("first open (split): %v", err)
	}
	_ = st.Close()

	ownerDB, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatalf("open owner connection: %v", err)
	}
	defer ownerDB.Close() //nolint:errcheck
	var appRole string
	if err := ownerDB.QueryRowContext(ctx,
		"SELECT grantee::regrole::text FROM pg_class c, aclexplode(c.relacl) a WHERE c.relname = 'agents' AND a.privilege_type = 'INSERT' AND a.grantee <> c.relowner LIMIT 1").Scan(&appRole); err != nil {
		t.Fatalf("discover the app role from the agents ACL: %v", err)
	}
	if _, err := ownerDB.ExecContext(ctx, "REVOKE ALL ON public.agents FROM "+appRole); err != nil {
		t.Fatalf("revoke agents: %v", err)
	}

	if _, err := Open(ctx, cfg, registerWidget); err == nil {
		t.Fatal("boot SUCCEEDED for a split app role with no privileges on a registered mutable table")
	}
}

// TestShadowSchemaCannotHijackTheEngine is the real search_path regression, and the
// one that matters most: the engine writes and reads with UNQUALIFIED names while
// every guard and every check it runs addresses its own schema explicitly. If the
// connection's search_path were left to chance, a schema earlier in the path could
// SHADOW an engine table — and boot would verify a locked-down public.audit_events
// while the runtime wrote, and truncated, an unguarded copy. Measured before the pool
// pinned its search_path.
func TestShadowSchemaCannotHijackTheEngine(t *testing.T) {
	dsn, role := customRolePG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	// A shadow of the ledger, in a schema this role puts FIRST in its search_path,
	// with no guards on it whatsoever.
	for _, stmt := range []string{
		"CREATE SCHEMA other",
		"CREATE TABLE other.audit_events (LIKE public.audit_events INCLUDING ALL)",
		"ALTER ROLE " + role + " SET search_path = other, public",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed shadow schema (%s): %v", stmt, err)
		}
	}

	st2, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("reopen with a shadow schema present: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	// A FRESH pool: ALTER ROLE ... SET applies at connection time, so the connection
	// that issued it still carries the old search_path. Checking the precondition on
	// that one would silently test nothing.
	probe, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open post-ALTER probe connection: %v", err)
	}
	defer probe.Close() //nolint:errcheck

	// The engine's own connections must ignore the role's search_path entirely.
	var resolved string
	if err := probe.QueryRowContext(ctx, "SELECT current_schema()").Scan(&resolved); err != nil {
		t.Fatalf("read current_schema on the probe connection: %v", err)
	}
	if resolved != "other" {
		t.Fatalf("fixture precondition: the probe connection should resolve to the shadow schema, got %q", resolved)
	}
	// Write through the store, then prove the row landed in the GUARDED table and not
	// in the shadow.
	if err := st2.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.CreateOrg(ctx, model.Org{Name: "shadow-probe", Slug: "shadow-probe-" + pgtest.Suffix(t)[:8]})
		return err
	}); err != nil {
		t.Fatalf("create org through the store: %v", err)
	}
	var shadowRows, realRows int
	// The shadow copy carries no policies (CREATE TABLE ... LIKE does not copy RLS),
	// so the app role can count it directly. The GUARDED table is under FORCE RLS with
	// a policy that RAISES on an unbound tenant, so it is counted from a maintenance
	// connection that bypasses RLS — the question here is "where did the row land",
	// not "what can this role see".
	if err := probe.QueryRowContext(ctx, "SELECT count(*) FROM other.audit_events").Scan(&shadowRows); err != nil {
		t.Fatalf("count shadow rows: %v", err)
	}
	superProbe, err := sql.Open("pgx", superuserDSNForDatabase(t, dsn))
	if err != nil {
		t.Fatalf("open maintenance probe: %v", err)
	}
	defer superProbe.Close() //nolint:errcheck
	if err := superProbe.QueryRowContext(ctx, "SELECT count(*) FROM public.audit_events").Scan(&realRows); err != nil {
		t.Fatalf("count real rows: %v", err)
	}
	if shadowRows != 0 {
		t.Errorf("the engine wrote %d audit rows into the SHADOW table — its connections are following the role's search_path", shadowRows)
	}
	if realRows == 0 {
		t.Error("no audit rows landed in the guarded table")
	}
}

// TestReconcileRefusesWhenItCannotAdministerTheACL is the regression for the subtlest
// defect found in this whole review: PostgreSQL does NOT error when a role revokes
// privileges it did not grant. It emits "WARNING: no privileges could be revoked" and
// reports SUCCESS. So a reconcile that inferred "applied" from a clean exit would do
// nothing at all whenever the DDL connection is not the tables' owner — and the
// verification's clean result would then be read as "re-asserted and held".
//
// The realistic shape: a deployment created single-role (tables owned by the app role)
// that later adopts --owner-dsn. Provisioning never reassigns table ownership.
func TestReconcileRefusesWhenItCannotAdministerTheACL(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s to run the ownership leg", pgtest.EnvSuperuserDSN)
	}
	superDSN := os.Getenv(pgtest.EnvSuperuserDSN)
	suffix := pgtest.Suffix(t)
	app := store.PgRole{Name: "olv_app_" + suffix, Password: "pw-" + suffix}
	owner := store.PgRole{Name: "olv_own_" + suffix, Password: "opw-" + suffix}
	spec := store.PgProvisionSpec{Database: "olv_" + suffix, App: app, Owner: owner}
	pgtest.Provision(t, superDSN, spec, ProvisionPostgres, app.Name, owner.Name)

	dsn := func(r store.PgRole) string {
		u, err := url.Parse(superDSN)
		if err != nil {
			t.Fatalf("parse %s: %v", pgtest.EnvSuperuserDSN, err)
		}
		u.User = url.UserPassword(r.Name, r.Password)
		u.Path = "/" + spec.Database
		return u.String()
	}
	ctx := context.Background()
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsn(app), OwnerDSN: dsn(owner), MaxConns: 4,
	}, registerWidget)
	if err != nil {
		t.Fatalf("first open (split): %v", err)
	}
	_ = st.Close()

	// Re-create the "grew out of single-role" shape: hand the ledger to the APP role
	// and leave the owner pool with privileges but no ownership.
	adminDB, err := sql.Open("pgx", dsn(store.PgRole{Name: "postgres", Password: "postgres"}))
	if err != nil {
		t.Fatalf("open maintenance connection: %v", err)
	}
	defer adminDB.Close() //nolint:errcheck
	for _, stmt := range []string{
		"ALTER TABLE public.audit_events OWNER TO " + app.Name,
		"GRANT ALL ON public.audit_events TO " + owner.Name,
	} {
		if _, err := adminDB.ExecContext(ctx, stmt); err != nil {
			t.Skipf("cannot build the foreign-ownership fixture (%s): %v", stmt, err)
		}
	}

	_, err = Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsn(app), OwnerDSN: dsn(owner), MaxConns: 4,
	}, registerWidget)
	if err == nil {
		t.Fatal("boot SUCCEEDED with a DDL connection that cannot administer the ledger's ACL — its REVOKEs report success and change nothing")
	}
	if !errors.Is(err, store.ErrAppendOnlyACLUnverifiable) {
		t.Errorf("expected ErrAppendOnlyACLUnverifiable naming the ownership mismatch, got: %v", err)
	}
}

// TestOrphanAppendOnlyTableIsStillProtected covers evidence left behind by a module
// that is no longer in the build. Those tables are retained ON PURPOSE, so a scope
// built only from the current registry would stop protecting exactly the rows nobody
// writes any more — the ones most likely to be wiped unnoticed.
func TestOrphanAppendOnlyTableIsStillProtected(t *testing.T) {
	dsn, role := customRolePG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	// Build a table that looks exactly like one an absent module left behind: the
	// engine's immutability trigger, and NOTHING in the registry pointing at it.
	for _, stmt := range []string{
		"CREATE TABLE orphan_evidence (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL)",
		"CREATE TRIGGER orphan_evidence_immutable BEFORE UPDATE OR DELETE ON orphan_evidence " +
			"FOR EACH ROW EXECUTE FUNCTION olivares_block_mutation()",
		"GRANT TRUNCATE ON orphan_evidence TO " + role,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed orphan table (%s): %v", stmt, err)
		}
	}

	st2, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("reopen with an orphan append-only table: %v", err)
	}
	_ = st2.Close()

	var canTruncate bool
	if err := db.QueryRowContext(ctx,
		"SELECT has_table_privilege('orphan_evidence','TRUNCATE')").Scan(&canTruncate); err != nil {
		t.Fatalf("read effective privilege: %v", err)
	}
	if canTruncate {
		t.Error("an append-only table left behind by an absent module kept TRUNCATE — the reconcile only covers the current registry")
	}
}

// TestBootRefusesWhenEvidenceIsNotAppendable is the other side of the boundary: the
// engine removes UPDATE/DELETE/TRUNCATE, so a role that ALSO lacks SELECT/INSERT
// cannot record evidence at all. Discovering that at the first append — mid-request,
// with the audit already committed to — is the worst possible moment.
func TestBootRefusesWhenEvidenceIsNotAppendable(t *testing.T) {
	dsn, role := customRolePG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	if _, err := db.ExecContext(ctx, "REVOKE INSERT ON audit_events FROM "+role); err != nil {
		t.Fatalf("revoke INSERT: %v", err)
	}

	_, err = Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err == nil {
		t.Fatal("boot SUCCEEDED without INSERT on audit_events — the store cannot record evidence")
	}
	if !errors.Is(err, store.ErrAppendOnlyGrantMissing) {
		t.Errorf("expected ErrAppendOnlyGrantMissing, got: %v", err)
	}
}

// TestBypassRLSAppRoleIsStillVerified pins the correction of a wrong assumption: the
// verification used to be skipped whenever --allow-privileged-db-role was set, on the
// theory that such a role reports every privilege as held. That is true only of a
// SUPERUSER. BYPASSRLS confers no table privilege at all, and that flag is routinely
// set to permit a privileged owner or admin pool while the app stays least-privilege,
// so keying the skip on the flag silenced an enforceable guard.
func TestBypassRLSAppRoleIsStillVerified(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s to run the BYPASSRLS leg", pgtest.EnvSuperuserDSN)
	}
	superDSN := os.Getenv(pgtest.EnvSuperuserDSN)
	suffix := pgtest.Suffix(t)
	role := "olv_app_" + suffix
	spec := store.PgProvisionSpec{
		Database: "olv_" + suffix,
		App:      store.PgRole{Name: role, Password: "pw-" + suffix},
	}
	pgtest.Provision(t, superDSN, spec, ProvisionPostgres, role)

	adminDB, err := sql.Open("pgx", superDSN)
	if err != nil {
		t.Fatalf("open superuser: %v", err)
	}
	defer adminDB.Close() //nolint:errcheck
	ctx := context.Background()
	if _, err := adminDB.ExecContext(ctx, "ALTER ROLE "+role+" BYPASSRLS"); err != nil {
		t.Fatalf("grant BYPASSRLS: %v", err)
	}

	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse %s: %v", pgtest.EnvSuperuserDSN, err)
	}
	u.User = url.UserPassword(role, spec.App.Password)
	u.Path = "/" + spec.Database
	// AllowPrivilegedRole is required only because BYPASSRLS trips the RLS guard; the
	// append-only verification must run regardless.
	cfg := store.Config{Engine: store.EnginePostgres, DSN: u.String(), MaxConns: 4, AllowPrivilegedRole: true}

	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("open with a BYPASSRLS app role: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	// A BYPASSRLS role's ACL behaves like anyone else's: the revoke took.
	var canTruncate bool
	if err := db.QueryRowContext(ctx,
		"SELECT has_table_privilege('audit_events','TRUNCATE')").Scan(&canTruncate); err != nil {
		t.Fatalf("read effective privilege: %v", err)
	}
	if canTruncate {
		t.Error("the revoke did not apply to a BYPASSRLS role")
	}
	// And the verification is NOT skipped for it: grant through PUBLIC, which the
	// reconcile cannot undo, and the boot must refuse.
	if _, err := db.ExecContext(ctx, "GRANT TRUNCATE ON audit_events TO PUBLIC"); err != nil {
		t.Fatalf("grant to PUBLIC: %v", err)
	}
	if _, err := Open(ctx, cfg, registerWidget); !errors.Is(err, store.ErrAppendOnlyACLOpen) {
		t.Errorf("a BYPASSRLS app role must still be verified; got: %v", err)
	}
}

// TestSuperuserAppRoleSkipsTheACLVerification pins the one case that genuinely cannot
// be answered: a superuser is exempt from ACLs, so has_table_privilege reports every
// privilege as held no matter what was revoked (measured), and refusing would be a
// refusal with no remedy. The engine warns instead.
func TestSuperuserAppRoleSkipsTheACLVerification(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s to run the privileged-role leg", pgtest.EnvSuperuserDSN)
	}
	suffix := pgtest.Suffix(t)
	superDSN := os.Getenv(pgtest.EnvSuperuserDSN)
	spec := store.PgProvisionSpec{
		Database: "olv_" + suffix,
		App:      store.PgRole{Name: "olv_app_" + suffix, Password: "pw-" + suffix},
	}
	pgtest.Provision(t, superDSN, spec, ProvisionPostgres, spec.App.Name)

	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse %s: %v", pgtest.EnvSuperuserDSN, err)
	}
	u.Path = "/" + spec.Database // keep the SUPERUSER credentials, point at the fresh db

	st, err := Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: u.String(), MaxConns: 4,
		AllowPrivilegedRole: true,
	}, registerWidget)
	if err != nil {
		t.Fatalf("a privileged role with AllowPrivilegedRole must still boot: %v", err)
	}
	_ = st.Close()
}

// superuserDSNForDatabase returns the maintenance DSN pointed at the same database as
// dsn, for probes that must see past FORCE row-level security.
func superuserDSNForDatabase(t *testing.T, dsn string) string {
	t.Helper()
	target, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse target dsn: %v", err)
	}
	su, err := url.Parse(os.Getenv(pgtest.EnvSuperuserDSN))
	if err != nil {
		t.Fatalf("parse %s: %v", pgtest.EnvSuperuserDSN, err)
	}
	su.Path = target.Path
	return su.String()
}

// TestPinnedSearchPathIgnoresAShadowedSetConfig is the regression for the sharpest
// defect round 5 found: the pin was calling set_config UNQUALIFIED.
//
// That one statement runs while the connection still carries whatever search_path it
// inherited, so it is the only place in the engine where an unqualified name can be
// resolved by a schema the application role controls. A search_path naming a writable
// schema AHEAD of pg_catalog is legal and settable per role, and a same-signature
// other.set_config(text, text, boolean) can return the expected value while setting
// nothing at all — leaving the hook reporting success and every later query resolving
// shadows. Measured before the fix: the pin returned no error while the connection
// reported `search_path = "other, pg_catalog, public"` and `current_schema = "other"`.
//
// Calling pg_catalog.set_config closes it, and closes it completely: once search_path
// holds the engine schema alone, pg_catalog is implicitly searched first again, so
// every later unqualified call is safe.
func TestPinnedSearchPathIgnoresAShadowedSetConfig(t *testing.T) {
	dsn, role := customRolePG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	for _, stmt := range []string{
		"CREATE SCHEMA other",
		"CREATE TABLE other.audit_events (LIKE public.audit_events INCLUDING ALL)",
		// Reports success, changes nothing. Same signature as the real one.
		`CREATE FUNCTION other.set_config(text, text, boolean) RETURNS text LANGUAGE sql AS $fn$ SELECT $2 $fn$`,
		// pg_catalog named explicitly and LAST, which is what lets the shadow win.
		"ALTER ROLE " + role + " SET search_path = other, pg_catalog, public",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed the shadowed set_config (%s): %v", stmt, err)
		}
	}

	// Precondition on a FRESH connection: ALTER ROLE ... SET applies at connect time.
	probe, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open post-ALTER probe connection: %v", err)
	}
	defer probe.Close() //nolint:errcheck
	var hijacked string
	if err := probe.QueryRowContext(ctx, "SELECT set_config('search_path', 'public', false)").Scan(&hijacked); err != nil {
		t.Fatalf("call the shadowed set_config: %v", err)
	}
	var stillShadowed string
	if err := probe.QueryRowContext(ctx, "SELECT current_schema()").Scan(&stillShadowed); err != nil {
		t.Fatalf("read current_schema: %v", err)
	}
	if hijacked != "public" || stillShadowed != "other" {
		t.Fatalf("fixture precondition: the shadow set_config should answer %q while leaving current_schema at %q; got %q and %q",
			"public", "other", hijacked, stillShadowed)
	}

	// The DIRECT assertion, made before anything downstream can confuse the diagnosis:
	// a pool built by the ENGINE's own opener must come up on the engine schema whatever
	// the role's search_path says and whatever its set_config answers. Without this the
	// mutation still fails the test, but through a collateral symptom (the reopen trips
	// over the shadow's own audit_events) that says nothing about the pin.
	pinned, err := openPGPinnedToEngineSchema(dsn, 1)
	if err != nil {
		t.Fatalf("open a pool through the engine's opener: %v", err)
	}
	defer pinned.Close() //nolint:errcheck
	var pinnedPath string
	if err := pinned.QueryRowContext(ctx, "SELECT pg_catalog.current_setting('search_path')").Scan(&pinnedPath); err != nil {
		t.Fatalf("read search_path on a pinned connection: %v", err)
	}
	if pinnedPath != engineSchemaForTest {
		t.Fatalf("the engine's pinned pool came up with search_path %q, want %q: the pin resolved set_config through the role's own schema and believed its answer",
			pinnedPath, engineSchemaForTest)
	}

	st2, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("reopen with a shadowed set_config present: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	if err := st2.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.CreateOrg(ctx, model.Org{Name: "setconfig-probe", Slug: "setconfig-" + pgtest.Suffix(t)[:8]})
		return err
	}); err != nil {
		t.Fatalf("create org through the store: %v", err)
	}

	var shadowRows, realRows int
	if err := probe.QueryRowContext(ctx, "SELECT count(*) FROM other.audit_events").Scan(&shadowRows); err != nil {
		t.Fatalf("count shadow rows: %v", err)
	}
	superProbe, err := sql.Open("pgx", superuserDSNForDatabase(t, dsn))
	if err != nil {
		t.Fatalf("open maintenance probe: %v", err)
	}
	defer superProbe.Close() //nolint:errcheck
	if err := superProbe.QueryRowContext(ctx, "SELECT count(*) FROM public.audit_events").Scan(&realRows); err != nil {
		t.Fatalf("count real rows: %v", err)
	}
	if shadowRows != 0 {
		t.Errorf("the engine wrote %d audit rows into the SHADOW table: the pin called the role's own set_config and believed it", shadowRows)
	}
	if realRows == 0 {
		t.Error("no audit rows landed in the guarded table")
	}
}

// TestACLDiscoveryIgnoresATriggerFromAnotherSchema is the regression for a defect that
// pointed the wrong way from all the others: instead of leaving evidence unprotected, it
// stripped privileges from a table the engine has no business touching.
//
// Live discovery matched the immutability trigger by its BARE function name, so any
// table whose trigger called a DIFFERENT function that merely shared the name — say
// other.olivares_block_mutation() — was classified as append-only, and the boot
// reconcile then revoked UPDATE/DELETE/TRUNCATE from the application role on it.
// Measured before the fix: an ordinary mutable table went from true/true/true to
// false/false/false on a boot that reported no error at all.
func TestACLDiscoveryIgnoresATriggerFromAnotherSchema(t *testing.T) {
	dsn, role := customRolePG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open probe connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	for _, stmt := range []string{
		"CREATE SCHEMA other",
		`CREATE FUNCTION other.olivares_block_mutation() RETURNS trigger LANGUAGE plpgsql AS $fn$ BEGIN RETURN NEW; END $fn$`,
		"CREATE TABLE public.mutable_probe (id int PRIMARY KEY, note text)",
		`CREATE TRIGGER mutable_probe_hook BEFORE UPDATE ON public.mutable_probe
		   FOR EACH ROW EXECUTE FUNCTION other.olivares_block_mutation()`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed the look-alike trigger (%s): %v", stmt, err)
		}
	}

	before := tablePrivs(t, db, role, "mutable_probe")
	if !before.update || !before.delete || !before.truncate {
		t.Fatalf("fixture precondition: the owner should hold all three privileges, got %+v", before)
	}

	st2, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("reopen with a look-alike trigger present: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	after := tablePrivs(t, db, role, "mutable_probe")
	if !after.update || !after.delete || !after.truncate {
		t.Errorf("the boot reconcile stripped privileges from an unrelated MUTABLE table: %+v -> %+v.\nDiscovery is matching the trigger function by bare name instead of by schema", before, after)
	}
}

// engineSchemaForTest mirrors dialect.EngineSchema without importing the dialect
// package into this file, keeping the assertion readable at the call site.
const engineSchemaForTest = "public"

type probePrivs struct{ update, delete, truncate bool }

func tablePrivs(t *testing.T, db *sql.DB, role, table string) probePrivs {
	t.Helper()
	var p probePrivs
	if err := db.QueryRowContext(context.Background(),
		`SELECT has_table_privilege($1, $2, 'UPDATE'),
		        has_table_privilege($1, $2, 'DELETE'),
		        has_table_privilege($1, $2, 'TRUNCATE')`,
		role, "public."+table).Scan(&p.update, &p.delete, &p.truncate); err != nil {
		t.Fatalf("read privileges on %s: %v", table, err)
	}
	return p
}

// TestProvisioningRunsOnATrustedPath is the regression for the deepest finding of this
// review, and for the one that showed qualifying functions was not enough.
//
// Provisioning is where an OPERATOR SUPERUSER connects to a database an UNTRUSTED role may
// own — in the default single-role topology the application role owns it — and a database
// owner may set a database-wide search_path that later sessions inherit, superuser sessions
// included. Name resolution then covers far more than functions: relations, operators,
// casts and TYPES. A `public.text` DOMAIN is selected by an unqualified `::text` cast, its
// CHECK runs on conversion, and a CHECK may call a function that is SECURITY INVOKER by
// default — executing as the operator superuser. `db init` computes DDL through such a cast
// and then EXECUTEs the result.
//
// Qualifying every function does not close that, and qualifying every type, relation,
// operator and cast is an obligation nobody keeps. Removing writable schemas from the path
// closes all of it at once, which is what this asserts: whatever the database says its
// search_path should be, a provisioning connection comes up on the catalog alone, so no
// object the untrusted role can create is even visible.
func TestProvisioningRunsOnATrustedPath(t *testing.T) {
	dsn, _ := customRolePG(t)
	ctx := context.Background()

	cfg, err := pgx.ParseConfig(superuserDSNForDatabase(t, dsn))
	if err != nil {
		t.Fatalf("parse superuser dsn: %v", err)
	}

	// A hostile database-wide default, exactly as a database owner could set it, plus
	// the two shapes the finding named: a shadow domain and a shadow function.
	seed, err := sql.Open("pgx", superuserDSNForDatabase(t, dsn))
	if err != nil {
		t.Fatalf("open seed connection: %v", err)
	}
	defer seed.Close() //nolint:errcheck
	var dbName string
	if err := seed.QueryRowContext(ctx, "SELECT pg_catalog.current_database()").Scan(&dbName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	for _, stmt := range []string{
		`CREATE FUNCTION public.trip_wire() RETURNS boolean LANGUAGE plpgsql AS $fn$ BEGIN RAISE EXCEPTION 'shadow domain CHECK executed'; END $fn$`,
		`CREATE DOMAIN public.text AS pg_catalog.text CHECK (public.trip_wire())`,
		`CREATE FUNCTION public.format(text, text) RETURNS text LANGUAGE sql AS $fn$ SELECT 'SHADOW' $fn$`,
		`ALTER DATABASE "` + dbName + `" SET search_path = public, pg_catalog`,
	} {
		if _, err := seed.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed the hostile database default (%s): %v", stmt, err)
		}
	}

	// Precondition on a FRESH ordinary connection: the database default must really be
	// in force, or this test proves nothing.
	plain, err := sql.Open("pgx", superuserDSNForDatabase(t, dsn))
	if err != nil {
		t.Fatalf("open plain probe: %v", err)
	}
	defer plain.Close() //nolint:errcheck
	var plainPath string
	if err := plain.QueryRowContext(ctx, "SELECT pg_catalog.current_setting('search_path')").Scan(&plainPath); err != nil {
		t.Fatalf("read search_path on a plain connection: %v", err)
	}
	if !strings.HasPrefix(plainPath, "public") {
		t.Fatalf("fixture precondition: an ordinary connection should inherit the hostile database default, got %q", plainPath)
	}
	// And on that connection the shadows really do win, which is the whole point.
	var captured string
	if err := plain.QueryRowContext(ctx, "SELECT format($1::pg_catalog.text, $2::pg_catalog.text)", "a", "b").Scan(&captured); err != nil {
		t.Fatalf("call the shadow format: %v", err)
	}
	if captured != "SHADOW" {
		t.Fatalf("fixture precondition: the shadow format should capture the call, got %q", captured)
	}

	// The provisioning opener must ignore all of it.
	trusted, err := openOnTrustedPath(cfg)
	if err != nil {
		t.Fatalf("openOnTrustedPath: %v", err)
	}
	defer trusted.Close() //nolint:errcheck

	var trustedPath string
	if err := trusted.QueryRowContext(ctx, "SELECT pg_catalog.current_setting('search_path')").Scan(&trustedPath); err != nil {
		t.Fatalf("read search_path on a provisioning connection: %v", err)
	}
	if trustedPath != trustedProvisioningPath {
		t.Fatalf("a provisioning connection came up with search_path %q, want %q: it inherits a default the untrusted database owner controls",
			trustedPath, trustedProvisioningPath)
	}

	// The two concrete vectors, on that same connection: the shadow function is not
	// visible, and the unqualified ::text cast does not reach the shadow DOMAIN (whose
	// CHECK would raise).
	if err := trusted.QueryRowContext(ctx, "SELECT format($1::text, $2::text)", "%s", "ok").Scan(&captured); err != nil {
		t.Fatalf("the unqualified cast or call reached an object the app role controls: %v", err)
	}
	if captured != "ok" {
		t.Errorf("the provisioning connection resolved format() to %q: a shadow the untrusted role defined is visible to the operator superuser", captured)
	}
}

// TestTrustedPathIsInstalledBeforeConnectionValidation is the regression for the last
// member of the search_path family, and for the reason an AfterConnect hook was never
// enough on its own.
//
// pgx runs ValidateConnect INSIDE ConnectConfig (pgconn/pgconn.go:514) and stdlib calls
// AfterConnect only afterwards (stdlib/sql.go:271,275). A DSN carrying
// target_session_attrs=primary|standby|prefer-standby installs a validator that executes
// `select pg_is_in_recovery()` UNQUALIFIED (pgconn/config.go:503-508, :1037). On a database
// whose owner set a hostile search_path — and in the default single-role topology the
// application role IS the owner — that resolves to a function the untrusted role defined,
// with INVOKER rights, before anything this package controls has run. On a provisioning
// pool those rights belong to the operator superuser.
//
// So the pin is installed at the ValidateConnect stage and only then delegates to the
// validator the DSN asked for.
func TestTrustedPathIsInstalledBeforeConnectionValidation(t *testing.T) {
	dsn, _ := customRolePG(t)
	ctx := context.Background()
	superDSN := superuserDSNForDatabase(t, dsn)

	seed, err := sql.Open("pgx", superDSN)
	if err != nil {
		t.Fatalf("open seed connection: %v", err)
	}
	defer seed.Close() //nolint:errcheck
	var dbName string
	if err := seed.QueryRowContext(ctx, "SELECT pg_catalog.current_database()").Scan(&dbName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	for _, stmt := range []string{
		`CREATE FUNCTION public.pg_is_in_recovery() RETURNS boolean LANGUAGE plpgsql AS $fn$ BEGIN RAISE EXCEPTION 'shadow pg_is_in_recovery() executed'; END $fn$`,
		`ALTER DATABASE "` + dbName + `" SET search_path = public, pg_catalog`,
	} {
		if _, err := seed.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed the pre-hook shadow (%s): %v", stmt, err)
		}
	}

	// Precondition on a FRESH ordinary connection: the shadow must really be what an
	// unqualified call reaches, or this test proves nothing.
	plain, err := sql.Open("pgx", superDSN)
	if err != nil {
		t.Fatalf("open plain probe: %v", err)
	}
	defer plain.Close() //nolint:errcheck
	var recovering bool
	if err := plain.QueryRowContext(ctx, "SELECT pg_is_in_recovery()").Scan(&recovering); err == nil {
		t.Fatal("fixture precondition: an unqualified pg_is_in_recovery() should have reached the shadow and raised")
	}

	// The DSN pgx would install a validator for.
	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse superuser dsn: %v", err)
	}
	q := u.Query()
	q.Set("target_session_attrs", "primary")
	u.RawQuery = q.Encode()
	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatalf("parse dsn with target_session_attrs: %v", err)
	}
	if cfg.ValidateConnect == nil {
		t.Fatal("fixture precondition: target_session_attrs=primary should have installed a ValidateConnect callback")
	}

	db, err := openOnTrustedPath(cfg)
	if err != nil {
		t.Fatalf("openOnTrustedPath: %v", err)
	}
	defer db.Close() //nolint:errcheck
	// Ping forces a PHYSICAL connection, which is what runs ValidateConnect.
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("a provisioning connection could not be established: the connection validator ran before the trusted path was installed and reached an object the untrusted database owner defined: %v", err)
	}
}
