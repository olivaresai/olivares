// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"testing"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// `db init` MUST LEAVE A DATABASE THE ENGINE CAN MIGRATE, on every supported major.
//
// The provisioner created the database owned by the owner role and then reconnected to it
// only when there was a split owner or an admin role to grant — so under the default
// single-role topology nothing was granted on the schema at all, and the owner was expected
// to reach `public` through owning the database.
//
// THE PRECONDITION IS NARROWER THAN THIS COMMENT FIRST CLAIMED, and the correction is worth
// keeping because the first version was wrong in the direction that flatters the fix. It
// said the database owner cannot create in `public` on 15 and can from 16. Measured on a
// CLEAN 15 with an INHERIT owner: owner_create=true and CREATE TABLE succeeds. The
// measurement that said otherwise was taken against a cluster-wide role that another test in
// this same session had left NOINHERIT without restoring it, and the "control" that appeared
// to prove the defect was not this branch's doing was reading the same contaminated role.
//
// What is measured and true, on a clean database with a NOINHERIT owner:
//
//	pg_has_role(owner, pg_database_owner, USAGE) / owner CREATE on public
//	15.18 -> false / false      16.14 -> true / true
//	17.10 -> true  / true       18.4  -> true / true
//
// PostgreSQL 16 changed inheritance to be stored per membership. So the grant is justified
// for a NOINHERIT DDL role on 15 and for upgraded/restored databases whose `public` kept an
// older owner or ACL — not for "every 15 install".
//
// WHAT THIS TEST DOES NOT PIN, said rather than implied: it uses isolatedPG, whose fixture
// always injects an admin role, so the OLD conditional would still have entered and
// restoring it leaves this green. The single-role-without-admin path is the one the fix is
// for and it is NOT covered here.
func TestPostgresProvisioningGrantsTheOwnerCreateOnTheEngineSchema(t *testing.T) {
	t.Parallel()
	dsns := isolatedPG(t)
	ctx := context.Background()

	// The provisioner has already run (isolatedPG goes through ProvisionPostgres). Ask the
	// server what the owner can actually do, as the server will be asked at the first
	// migration.
	owner, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("owner pool: %v", err)
	}
	defer func() { _ = owner.Close() }()

	var canCreate, canUse bool
	if err := owner.QueryRowContext(ctx,
		"SELECT pg_catalog.has_schema_privilege(current_user, $1, 'CREATE'), pg_catalog.has_schema_privilege(current_user, $1, 'USAGE')",
		dialect.EngineSchema).Scan(&canCreate, &canUse); err != nil {
		t.Fatalf("probe the schema privileges: %v", err)
	}
	t.Logf("PROVISIONED_SCHEMA|schema=%s|owner_create=%v|owner_usage=%v", dialect.EngineSchema, canCreate, canUse)
	if !canUse {
		t.Fatalf("the DDL role cannot USE %q after provisioning; every object reference needs it", dialect.EngineSchema)
	}
	if !canCreate {
		t.Fatalf("the DDL role cannot CREATE in %q after provisioning. `db init` reported success and "+
			"left a database whose FIRST migration fails with 42501 — measured on PostgreSQL 15, the "+
			"floor of the supported range, where owning the database does not confer this", dialect.EngineSchema)
	}

	// And it is not merely a privilege bit: the operation itself has to work, because that
	// is what the engine does one statement into its first boot.
	if _, err := owner.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS olv_provision_probe(id bigint PRIMARY KEY)`); err != nil {
		t.Fatalf("the DDL role could not create a relation after provisioning: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `DROP TABLE olv_provision_probe`); err != nil {
		t.Fatalf("drop the probe relation: %v", err)
	}

	// PUBLIC must NOT have been handed CREATE back. PostgreSQL 15 removed that default
	// deliberately, and restoring it would let any role in the cluster create objects in the
	// engine's schema — a boundary this engine spends real effort holding.
	var publicCreate bool
	if err := owner.QueryRowContext(ctx,
		"SELECT pg_catalog.has_schema_privilege('public', $1, 'CREATE')", dialect.EngineSchema).Scan(&publicCreate); err != nil {
		t.Fatalf("probe PUBLIC's privileges: %v", err)
	}
	if publicCreate {
		t.Fatalf("PUBLIC holds CREATE on %q. The fix for the 15 floor must name the OWNER, not restore "+
			"a cluster-wide default PostgreSQL removed on purpose", dialect.EngineSchema)
	}
}

// R2-M3(a): THE PATH THE FIX EXISTS FOR, which the test above cannot reach.
//
// isolatedPG always injects an admin role, so the OLD condition
// (`if HasSplitOwner() || Admin != nil`) still entered and the test above stays green with
// the fix reverted — measured by the contrast. The defect lived in the ONE shape that
// condition excluded: single role, no split owner, no admin. That is also the default
// topology and what `olivares db init` produces with no extra flags.
//
// It provisions through pgtest.Provision with a bare spec rather than through the isolate
// helper, which is the same door TestProvisionPostgresSingleRole and the custom-role
// regression already use, and it drops exactly what it made.
func TestPostgresProvisioningSingleRoleWithoutAdminGrantsTheSchema(t *testing.T) {
	t.Parallel()
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to run the provisioning leg", pgtest.EnvSuperuserDSN)
	}
	superDSN := os.Getenv(pgtest.EnvSuperuserDSN)
	suffix := pgtest.Suffix(t)
	role := "olv_app_" + suffix
	spec := store.PgProvisionSpec{
		Database: "olv_" + suffix,
		App:      store.PgRole{Name: role, Password: "pw-" + suffix},
		// Owner empty => single role. Admin nil => no admin. This pair is the excluded shape.
	}
	if spec.HasSplitOwner() || spec.Admin != nil {
		t.Fatal("this fixture must be single-role WITHOUT an admin, or it exercises the shape the old condition already covered")
	}
	pgtest.Provision(t, superDSN, spec, ProvisionPostgres, role)
	ctx := context.Background()

	// NOINHERIT IS THE PRECONDITION, and a first version of this test omitted it and could not
	// fail. A freshly provisioned INHERIT owner reaches `public` through pg_database_owner on
	// ALL FOUR majors, so removing the grant leaves it green — measured. The grant only
	// changes an outcome where the implicit path is absent, and that is a NOINHERIT DDL role
	// (PostgreSQL 16 moved inheritance to be stored per membership) or a restored database.
	//
	// The role is put back before the test ends. This session already shipped a fixture that
	// left a shared role NOINHERIT and then reasoned from the wreckage, so the restore is not
	// tidiness — see rolloutguardswiring_pg_test.go.
	super, err := sql.Open("pgx", superDSN)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	t.Cleanup(func() { _ = super.Close() })
	if _, err := super.ExecContext(ctx, `ALTER ROLE `+pgQuoteIdent(role)+` NOINHERIT`); err != nil {
		t.Fatalf("make the DDL role non-inheriting: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.ExecContext(context.Background(), `ALTER ROLE `+pgQuoteIdent(role)+` INHERIT`)
	})
	// Re-run provisioning, which is the idempotent path an operator takes after any role
	// change. THIS is the run whose grant decides the outcome.
	if _, err := ProvisionPostgres(ctx, superDSN, spec, true); err != nil {
		t.Fatalf("re-provision after the role change: %v", err)
	}

	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse %s: %v", pgtest.EnvSuperuserDSN, err)
	}
	u.User = url.UserPassword(spec.App.Name, spec.App.Password)
	u.Path = "/" + spec.Database
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("open as the provisioned role: %v", err)
	}
	defer func() { _ = db.Close() }()
	var canCreate bool
	if err := db.QueryRowContext(ctx,
		"SELECT pg_catalog.has_schema_privilege(current_user, $1, 'CREATE')", dialect.EngineSchema).Scan(&canCreate); err != nil {
		t.Fatalf("probe CREATE: %v", err)
	}
	if !canCreate {
		t.Fatalf("a single-role, NOINHERIT DDL role provisioned WITHOUT an admin cannot CREATE in %q. "+
			"That pair — the default topology and the shape the old conditional skipped — is exactly "+
			"where the implicit pg_database_owner path is absent on 15, so `db init` reports success "+
			"and the engine's first migration fails with 42501", dialect.EngineSchema)
	}
	// The operation, not only the bit.
	if _, err := db.ExecContext(ctx, `CREATE TABLE olv_singlerole_probe(id bigint PRIMARY KEY)`); err != nil {
		t.Fatalf("the provisioned role could not create a relation: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE olv_singlerole_probe`); err != nil {
		t.Fatalf("drop the probe: %v", err)
	}
}

// R2-M3(b): the grant must not reach the APPLICATION role of a split deployment.
//
// The whole point of the split is that the role facing traffic owns nothing and runs no DDL.
// A grant added for the DDL role that also landed on the app role would hand runtime traffic
// the ability to create objects in the engine's schema, and no test asserted otherwise —
// the contrast measured that widening it to the app role left every existing regression
// green.
func TestPostgresTheSchemaGrantDoesNotReachTheSplitApplicationRole(t *testing.T) {
	t.Parallel()
	dsns := isolatedPGSplit(t)
	ctx := context.Background()

	app, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	defer func() { _ = app.Close() }()

	var appCreate, appUsage bool
	if err := app.QueryRowContext(ctx,
		"SELECT pg_catalog.has_schema_privilege(current_user, $1, 'CREATE'), pg_catalog.has_schema_privilege(current_user, $1, 'USAGE')",
		dialect.EngineSchema).Scan(&appCreate, &appUsage); err != nil {
		t.Fatalf("probe the app role: %v", err)
	}
	t.Logf("SPLIT_APP_SCHEMA|create=%v|usage=%v", appCreate, appUsage)
	if !appUsage {
		t.Fatalf("the split application role cannot USE %q; it could not read a single table", dialect.EngineSchema)
	}
	if appCreate {
		t.Fatalf("the split application role holds CREATE on %q. The grant that exists for the DDL role "+
			"must not reach the role that faces traffic: owning nothing and running no DDL is the entire "+
			"content of the split topology", dialect.EngineSchema)
	}

	// And the owner, on the same deployment, must have it — or the assertion above would pass
	// on a database where nobody can create anything.
	owner, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("owner pool: %v", err)
	}
	defer func() { _ = owner.Close() }()
	var ownerCreate bool
	if err := owner.QueryRowContext(ctx,
		"SELECT pg_catalog.has_schema_privilege(current_user, $1, 'CREATE')", dialect.EngineSchema).Scan(&ownerCreate); err != nil {
		t.Fatalf("probe the owner role: %v", err)
	}
	if !ownerCreate {
		t.Fatalf("the split OWNER role cannot CREATE in %q, so the check above measured a database in "+
			"which nothing can be created rather than a boundary", dialect.EngineSchema)
	}
}
